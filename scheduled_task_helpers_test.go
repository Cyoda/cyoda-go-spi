package spi

import (
	"errors"
	"strings"
	"testing"
)

func cand(tenant TenantID, entity, id string, next int64) ScheduledTask {
	return ScheduledTask{ID: id, TenantID: tenant, EntityID: entity, NextAttemptTime: next}
}

func claimIDs(ts []ScheduledTask) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.ID)
	}
	return out
}

func TestSelectClaims_OnePerEntity(t *testing.T) {
	got := SelectClaims([]ScheduledTask{
		cand("A", "e1", "e1:S:T2", 20),
		cand("A", "e1", "e1:S:T1", 10),
		cand("A", "e2", "e2:S:T", 30),
	}, ClaimRequest{Limit: 10, PerTenantLimit: 10})
	if want := "e1:S:T1,e2:S:T"; strings.Join(claimIDs(got), ",") != want {
		t.Fatalf("claimed %v, want %s: the earliest task of each entity, one per entity", claimIDs(got), want)
	}
}

func TestSelectClaims_WithinATenantByNextAttemptTimeThenID(t *testing.T) {
	got := SelectClaims([]ScheduledTask{
		cand("A", "e3", "c", 10),
		cand("A", "e2", "b", 5),
		cand("A", "e1", "a", 10),
	}, ClaimRequest{Limit: 10, PerTenantLimit: 10})
	if want := "b,a,c"; strings.Join(claimIDs(got), ",") != want {
		t.Fatalf("claimed %v, want %s", claimIDs(got), want)
	}
}

// Tenants take turns: each tenant's first task comes before any tenant's
// second. The tenant with the earliest candidate goes first; ties go to the
// lower tenant id.
func TestSelectClaims_TenantsTakeTurns(t *testing.T) {
	cands := []ScheduledTask{
		cand("A", "a1", "a1", 1), cand("A", "a2", "a2", 2), cand("A", "a3", "a3", 3),
		cand("B", "b1", "b1", 5), cand("B", "b2", "b2", 6),
		cand("C", "c1", "c1", 5),
	}
	got := SelectClaims(cands, ClaimRequest{Limit: 5, PerTenantLimit: 10})
	if want := "a1,b1,c1,a2,b2"; strings.Join(claimIDs(got), ",") != want {
		t.Fatalf("claimed %v, want %s", claimIDs(got), want)
	}
	got = SelectClaims(cands, ClaimRequest{Limit: 2, PerTenantLimit: 10})
	if want := "a1,b1"; strings.Join(claimIDs(got), ",") != want {
		t.Fatalf("with Limit 2 claimed %v, want %s", claimIDs(got), want)
	}
}

func TestSelectClaims_PerTenantLimitCountsRunsInProgress(t *testing.T) {
	cands := []ScheduledTask{
		cand("A", "a1", "a1", 1), cand("A", "a2", "a2", 2), cand("A", "a3", "a3", 3),
		cand("B", "b1", "b1", 4),
		cand("C", "c1", "c1", 5),
	}
	got := SelectClaims(cands, ClaimRequest{
		Limit: 10, PerTenantLimit: 2,
		TenantInProgress: map[TenantID]int{"A": 1, "C": 3},
	})
	if want := "a1,b1"; strings.Join(claimIDs(got), ",") != want {
		t.Fatalf("claimed %v, want %s: A has room for one, B for two, C for none", claimIDs(got), want)
	}
}

func TestSelectClaims_NoCandidates(t *testing.T) {
	if got := SelectClaims(nil, ClaimRequest{Limit: 1, PerTenantLimit: 1}); len(got) != 0 {
		t.Fatalf("claimed %v from no candidates", claimIDs(got))
	}
}

func TestValidateTaskErrorText(t *testing.T) {
	const secret = "do-not-echo"
	for name, text := range map[string]string{
		"empty":                     "",
		"1 024 bytes, 2-byte runes": strings.Repeat("é", 512),
	} {
		if err := ValidateTaskErrorText(text); err != nil {
			t.Errorf("%s: %v, want nil", name, err)
		}
	}
	for name, text := range map[string]string{
		"NUL":             secret + "\x00",
		"invalid UTF-8":   secret + "\xff",
		"over 1024 bytes": secret + strings.Repeat("x", MaxTaskErrorBytes),
	} {
		err := ValidateTaskErrorText(text)
		if !errors.Is(err, ErrStoreRejected) {
			t.Errorf("%s: err = %v, want ErrStoreRejected", name, err)
			continue
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: the rejection repeats the rejected text: %v", name, err)
		}
	}
}

func TestValidateFailureReason(t *testing.T) {
	for _, r := range []ScheduledTaskFailureReason{
		FailureUnsafeWorkNotCompleted, FailureOwnerLostRepeatedly,
		FailureExpiredAfterFailedAttempts, FailureRunPanicked, FailureStoppedAfterPartialCommit,
	} {
		if err := ValidateFailureReason(r); err != nil {
			t.Errorf("%s: %v, want nil", r, err)
		}
	}
	for _, r := range []ScheduledTaskFailureReason{"", "NOT_A_REASON"} {
		if err := ValidateFailureReason(r); !errors.Is(err, ErrStoreRejected) {
			t.Errorf("%q: err = %v, want ErrStoreRejected", r, err)
		}
	}
}

func TestValidateArm(t *testing.T) {
	ok := ReconcileRequest{TenantID: "A", EntityID: "e1", Arm: []ScheduledTask{{ID: "e1:S:T"}}}
	if err := ValidateArm(ok); err != nil {
		t.Fatalf("an arm with ids: %v", err)
	}
	if err := ValidateArm(ReconcileRequest{TenantID: "A", EntityID: "e1"}); err != nil {
		t.Fatalf("an empty arm: %v", err)
	}
	noID := ReconcileRequest{TenantID: "A", EntityID: "e1", Arm: []ScheduledTask{{ID: "e1:S:T"}, {}}}
	if err := ValidateArm(noID); !errors.Is(err, ErrStoreRejected) {
		t.Fatalf("an arm task without an id: err = %v, want ErrStoreRejected", err)
	}
}

func TestValidateArm_RejectsIDInBothArmAndCancel(t *testing.T) {
	req := ReconcileRequest{
		TenantID: "A", EntityID: "e1",
		Arm:    []ScheduledTask{{ID: "e1:S:T"}},
		Cancel: []string{"e1:S:T"},
	}
	if err := ValidateArm(req); !errors.Is(err, ErrStoreRejected) {
		t.Fatalf("an id in both Arm and Cancel: err = %v, want ErrStoreRejected", err)
	}
}
