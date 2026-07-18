package spi

import (
	"context"
	"testing"
)

// RunScheduledTaskStoreConformance exercises the ScheduledTaskStore contract
// against any StoreFactory. Each plugin calls this from its test package.
func RunScheduledTaskStoreConformance(t *testing.T, newFactory func() StoreFactory) {
	ctx := context.Background()
	f := newFactory()
	t.Cleanup(func() { _ = f.Close() })

	// Arm two tasks in one tenant, one due, one future.
	sts, err := f.ScheduledTaskStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	due := ScheduledTask{ID: "e1:S:T", TenantID: "t1", Type: ScheduledTaskFireTransition,
		ScheduledTime: 1000, EntityID: "e1", SourceState: "S", Transition: "T"}
	future := ScheduledTask{ID: "e2:S:T", TenantID: "t1", Type: ScheduledTaskFireTransition,
		ScheduledTime: 9_000_000, EntityID: "e2", SourceState: "S", Transition: "T"}
	if err := sts.Upsert(ctx, due); err != nil {
		t.Fatal(err)
	}
	if err := sts.Upsert(ctx, future); err != nil {
		t.Fatal(err)
	}

	// ScanDue at now=2000 returns only the due one.
	got, err := sts.ScanDue(ctx, 2000, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "e1:S:T" {
		t.Fatalf("ScanDue: want [e1:S:T], got %+v", got)
	}

	// Upsert on an existing ID must fully re-arm the row — replace ALL
	// non-ID columns, not merge a subset. Re-upsert the same ID with a
	// changed ModelVersion, ScheduledTime, and TimeoutMs (first nil->non-nil,
	// then non-nil->nil) and confirm Get reflects the latest upsert only.
	rearmID := "e3:S:T"
	if err := sts.Upsert(ctx, ScheduledTask{ID: rearmID, TenantID: "t1", Type: ScheduledTaskFireTransition,
		ScheduledTime: 500, EntityID: "e3", ModelName: "m", ModelVersion: 2,
		SourceState: "S", Transition: "T"}); err != nil {
		t.Fatal(err)
	}
	timeout1 := int64(2000)
	if err := sts.Upsert(ctx, ScheduledTask{ID: rearmID, TenantID: "t1", Type: ScheduledTaskFireTransition,
		ScheduledTime: 600, EntityID: "e3", ModelName: "m", ModelVersion: 3,
		SourceState: "S", Transition: "T", TimeoutMs: &timeout1}); err != nil {
		t.Fatal(err)
	}
	rearmed, found, err := sts.Get(ctx, rearmID)
	if err != nil || !found {
		t.Fatalf("Get after re-arm: found=%v err=%v", found, err)
	}
	if rearmed.ModelVersion != 3 || rearmed.ScheduledTime != 600 ||
		rearmed.TimeoutMs == nil || *rearmed.TimeoutMs != 2000 {
		t.Fatalf("re-arm want ModelVersion=3 ScheduledTime=600 TimeoutMs=2000, got %+v", rearmed)
	}
	if err := sts.Upsert(ctx, ScheduledTask{ID: rearmID, TenantID: "t1", Type: ScheduledTaskFireTransition,
		ScheduledTime: 700, EntityID: "e3", ModelName: "m", ModelVersion: 4,
		SourceState: "S", Transition: "T"}); err != nil {
		t.Fatal(err)
	}
	rearmed, found, err = sts.Get(ctx, rearmID)
	if err != nil || !found {
		t.Fatalf("Get after second re-arm: found=%v err=%v", found, err)
	}
	if rearmed.ModelVersion != 4 || rearmed.ScheduledTime != 700 || rearmed.TimeoutMs != nil {
		t.Fatalf("re-arm want ModelVersion=4 ScheduledTime=700 TimeoutMs=nil, got %+v", rearmed)
	}
	if _, err := sts.Delete(ctx, rearmID); err != nil {
		t.Fatal(err)
	}

	// MarkRedispatch hides it from a subsequent scan.
	if err := sts.MarkRedispatch(ctx, "e1:S:T", 5000); err != nil {
		t.Fatal(err)
	}
	got, _ = sts.ScanDue(ctx, 2000, 10)
	if len(got) != 0 {
		t.Fatalf("MarkRedispatch should hide task, got %+v", got)
	}

	// Get returns it; AttemptCount bumped.
	tk, found, err := sts.Get(ctx, "e1:S:T")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if tk.AttemptCount != 1 {
		t.Errorf("AttemptCount want 1 got %d", tk.AttemptCount)
	}

	// Delete returns removed=true once, false the second time.
	if ok, _ := sts.Delete(ctx, "e1:S:T"); !ok {
		t.Error("first Delete want removed=true")
	}
	if ok, _ := sts.Delete(ctx, "e1:S:T"); ok {
		t.Error("second Delete want removed=false")
	}

	// ReconcileForEntity: entity e2 moved S->S2; arm S2:T2, cancel S:T.
	arm := []ScheduledTask{{ID: "e2:S2:T2", TenantID: "t1", Type: ScheduledTaskFireTransition,
		ScheduledTime: 100, EntityID: "e2", SourceState: "S2", Transition: "T2"}}
	cancelled, err := sts.ReconcileForEntity(ctx, ReconcileRequest{
		TenantID: "t1", EntityID: "e2", CurrentState: "S2", Arm: arm})
	if err != nil {
		t.Fatal(err)
	}
	if len(cancelled) != 1 || cancelled[0].ID != "e2:S:T" {
		t.Fatalf("Reconcile cancelled: want [e2:S:T], got %+v", cancelled)
	}
	if _, found, _ := sts.Get(ctx, "e2:S:T"); found {
		t.Error("old-state task should be deleted")
	}
	if _, found, _ := sts.Get(ctx, "e2:S2:T2"); !found {
		t.Error("new-state task should be armed")
	}

	// ReconcileForEntity: req.Cancel explicitly deletes a task id regardless
	// of SourceState — the born-expired path, where a ScheduleFunction
	// resolves a deadline already in the past for the CURRENT state, so it
	// must never be armed and any previously-armed row for it must be
	// cancelled. Arm a task in the current state, then reconcile again with
	// that id in Cancel (not in Arm): the row must be gone, and — because
	// this is a Cancel-driven delete, not a SourceState-mismatch cancel —
	// the returned cancelled slice must NOT include it (the caller audits
	// Cancel deletes separately, as EXPIRE not CANCEL).
	bornExpiredID := "e4:S3:T3"
	if err := sts.Upsert(ctx, ScheduledTask{ID: bornExpiredID, TenantID: "t1", Type: ScheduledTaskFireTransition,
		ScheduledTime: 100, EntityID: "e4", SourceState: "S3", Transition: "T3"}); err != nil {
		t.Fatal(err)
	}
	cancelled, err = sts.ReconcileForEntity(ctx, ReconcileRequest{
		TenantID: "t1", EntityID: "e4", CurrentState: "S3", Cancel: []string{bornExpiredID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := sts.Get(ctx, bornExpiredID); found {
		t.Error("req.Cancel task should be deleted")
	}
	for _, c := range cancelled {
		if c.ID == bornExpiredID {
			t.Errorf("req.Cancel delete must not appear in the returned cancelled (SourceState-mismatch) slice, got %+v", cancelled)
		}
	}

	// Deleting a nonexistent id via req.Cancel is a harmless no-op.
	if _, err := sts.ReconcileForEntity(ctx, ReconcileRequest{
		TenantID: "t1", EntityID: "e4", CurrentState: "S3", Cancel: []string{"does-not-exist"}}); err != nil {
		t.Fatal(err)
	}

	// Tenant isolation: a t2 task is not returned to a t1-scoped delete but IS
	// visible to the cross-tenant ScanDue with its own TenantID.
	other := ScheduledTask{ID: "e9:S:T", TenantID: "t2", Type: ScheduledTaskFireTransition,
		ScheduledTime: 100, EntityID: "e9", SourceState: "S", Transition: "T"}
	if err := sts.Upsert(ctx, other); err != nil {
		t.Fatal(err)
	}
	all, _ := sts.ScanDue(ctx, 2000, 10)
	var sawT2 bool
	for _, x := range all {
		if x.TenantID == "t2" {
			sawT2 = true
		}
	}
	if !sawT2 {
		t.Error("cross-tenant ScanDue must include t2 task")
	}
}
