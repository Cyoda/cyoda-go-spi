package spitest

import (
	"cmp"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

func (f *stFixture) query(q spi.ScheduledTaskQuery) spi.ScheduledTaskPage {
	f.t.Helper()
	page, err := f.sts.Query(f.ctx, f.tenant, q)
	require.NoError(f.t, err)
	return page
}

func (f *stFixture) queryEntities(q spi.ScheduledTaskQuery) []string {
	f.t.Helper()
	q.Limit = 1000
	var out []string
	for _, x := range f.query(q).Items {
		out = append(out, x.EntityID)
	}
	return out
}

// 200, no filter, several pages (spec §13, GET /scheduled-tasks).
func testSTQueryPagesInOrder(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	var want []spi.ScheduledTask
	// Two tasks share a ScheduledTime, so the ID breaks the tie.
	for _, at := range []int64{stFuture + 30, stFuture + 10, stFuture + 20, stFuture + 10, stFuture + 40} {
		want = append(want, f.arm(at))
	}
	slices.SortFunc(want, func(a, b spi.ScheduledTask) int {
		if c := cmp.Compare(a.ScheduledTime, b.ScheduledTime); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})

	var got []string
	var after *spi.ScheduledTaskCursor
	pages := 0
	for {
		page := f.query(spi.ScheduledTaskQuery{After: after, Limit: 2})
		pages++
		require.LessOrEqual(t, pages, 3, "5 tasks at 2 per page is 3 pages")
		got = append(got, stIDs(page.Items)...)
		if page.Next == nil {
			break
		}
		require.Len(t, page.Items, 2, "only the last page is short")
		last := page.Items[len(page.Items)-1]
		require.Equal(t, spi.ScheduledTaskCursor{ScheduledTime: last.ScheduledTime, ID: last.ID}, *page.Next)
		after = page.Next
	}
	require.Equal(t, stIDs(want), got, "(ScheduledTime, ID) order, IDs compared byte-wise")

	whole := f.query(spi.ScheduledTaskQuery{Limit: 5})
	require.Len(t, whole.Items, 5)
	require.Nil(t, whole.Next, "no further page when exactly Limit tasks remain")

	for i, x := range whole.Items {
		requireFreshLife(t, want[i], x) // Query returns whole records
	}
}

// 200, each filter: status (one and several), model name, name and
// version, entity (spec §13, GET /scheduled-tasks).
func testSTQueryFilters(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	waiting := f.arm(stFuture)
	running := f.claimTask(uuid.New(), f.armDue().ID)
	failed := f.claimTask(uuid.New(), f.armDue().ID)
	require.NoError(t, f.sts.Fail(f.ctx, stRef(failed), spi.Failure{Reason: spi.FailureRunPanicked, Error: "E", AtMs: stNow}))

	v2 := f.spec(f.newEntity(), "S", "T", stFuture)
	v2.ModelVersion = 2
	f.reconcile(f.ctx, v2.EntityID, "S", v2)
	other := f.spec(f.newEntity(), "S", "T", stFuture)
	other.ModelName = f.model + "-other"
	f.reconcile(f.ctx, other.EntityID, "S", other)

	W, R, F := spi.ScheduledTaskWaiting, spi.ScheduledTaskRunning, spi.ScheduledTaskFailed
	for name, tc := range map[string]struct {
		q    spi.ScheduledTaskQuery
		want []string
	}{
		"no filter":              {spi.ScheduledTaskQuery{}, []string{waiting.EntityID, running.EntityID, failed.EntityID, v2.EntityID, other.EntityID}},
		"WAITING":                {spi.ScheduledTaskQuery{Statuses: []spi.ScheduledTaskStatus{W}}, []string{waiting.EntityID, v2.EntityID, other.EntityID}},
		"RUNNING":                {spi.ScheduledTaskQuery{Statuses: []spi.ScheduledTaskStatus{R}}, []string{running.EntityID}},
		"FAILED":                 {spi.ScheduledTaskQuery{Statuses: []spi.ScheduledTaskStatus{F}}, []string{failed.EntityID}},
		"RUNNING or FAILED":      {spi.ScheduledTaskQuery{Statuses: []spi.ScheduledTaskStatus{R, F}}, []string{running.EntityID, failed.EntityID}},
		"model name":             {spi.ScheduledTaskQuery{ModelName: f.model}, []string{waiting.EntityID, running.EntityID, failed.EntityID, v2.EntityID}},
		"model name and version": {spi.ScheduledTaskQuery{ModelName: f.model, ModelVersion: 2}, []string{v2.EntityID}},
		"entity":                 {spi.ScheduledTaskQuery{EntityID: running.EntityID}, []string{running.EntityID}},
		"status and model":       {spi.ScheduledTaskQuery{Statuses: []spi.ScheduledTaskStatus{W}, ModelName: f.model, ModelVersion: 1}, []string{waiting.EntityID}},
		"unknown entity":         {spi.ScheduledTaskQuery{EntityID: newID()}, nil},
		"unknown model":          {spi.ScheduledTaskQuery{ModelName: "st-unknown-" + newID()}, nil},
	} {
		require.ElementsMatch(t, tc.want, f.queryEntities(tc.q), name)
	}

	// A FAILED item carries its reason, error and times.
	page := f.query(spi.ScheduledTaskQuery{EntityID: failed.EntityID, Limit: 1})
	require.Len(t, page.Items, 1)
	got := page.Items[0]
	require.Equal(t, spi.FailureRunPanicked, got.FailureReason)
	require.Equal(t, "E", got.LastError)
	require.NotNil(t, got.FailedTime)
}

// Another tenant's tasks are never returned, under any filter (spec §13,
// GET /scheduled-tasks).
func testSTQueryTenantIsolation(t *testing.T, h Harness) {
	fa := newSTFixture(t, h)
	fb := newSTFixture(t, h)
	fb.model = fa.model
	a := fa.arm(stFuture)
	b := fb.arm(stFuture)
	bRunning := fb.claimTask(uuid.New(), fb.armDue().ID)

	all := []spi.ScheduledTaskStatus{spi.ScheduledTaskWaiting, spi.ScheduledTaskRunning, spi.ScheduledTaskFailed}
	for name, q := range map[string]spi.ScheduledTaskQuery{
		"no filter":        {},
		"every status":     {Statuses: all},
		"shared model":     {ModelName: fa.model},
		"shared model v1":  {ModelName: fa.model, ModelVersion: 1},
		"B's entity":       {EntityID: b.EntityID},
		"B's RUNNING task": {EntityID: bRunning.EntityID, Statuses: []spi.ScheduledTaskStatus{spi.ScheduledTaskRunning}},
	} {
		for _, e := range fa.queryEntities(q) {
			require.Equal(t, a.EntityID, e, "%s: tenant A sees only its own task", name)
		}
	}
	require.Empty(t, fa.queryEntities(spi.ScheduledTaskQuery{EntityID: b.EntityID}))
}

// Every tenant-facing method is scoped to its tenant argument.
func testSTTenantIsolationEveryMethod(t *testing.T, h Harness) {
	fa := newSTFixture(t, h)
	fb := newSTFixture(t, h)
	fb.model = fa.model
	c := fa.claimTask(uuid.New(), fa.armDue().ID)

	_, found := fb.get(fb.ctx, c.ID)
	require.False(t, found, "Get")
	require.NoError(t, fb.sts.RemoveLife(fb.ctx, fb.tenant, c.ID, c.ArmToken))
	require.NoError(t, fb.sts.DeleteForEntities(fb.ctx, fb.tenant, []string{c.EntityID}))
	require.NoError(t, fb.sts.DeleteForModel(fb.ctx, fb.tenant, fa.model, 1, nil))
	removed, err := fb.sts.ReconcileForEntity(fb.ctx, spi.ReconcileRequest{
		TenantID: fb.tenant, EntityID: c.EntityID, CurrentState: "S2"})
	require.NoError(t, err)
	require.Empty(t, removed, "ReconcileForEntity removes only its tenant's tasks")
	crossRef := stRef(c)
	crossRef.TenantID = fb.tenant
	requireAllFencedRefused(t, fb.ctx, fb.sts, crossRef, "another tenant's task")

	requireUnchangedClaim(t, c, fa.mustGet(c.ID))
}

// A joining write whose tenant is not the tenant of the transaction on ctx is
// refused with ErrTxTenantMismatch and changes nothing (README C-S5): a task
// row of tenant B never enters tenant A's transaction.
func testSTTenantJoiningWriteOtherTenantRefused(t *testing.T, h Harness) {
	fb := newSTFixture(t, h)
	fa := newSTFixture(t, h)
	fa.model = fb.model
	c := fb.claimTask(uuid.New(), fb.armDue().ID)
	_, txCtx := fa.begin() // tenant A's transaction; rolled back first at cleanup

	_, err := fa.sts.ReconcileForEntity(txCtx, spi.ReconcileRequest{
		TenantID: fb.tenant, EntityID: c.EntityID, CurrentState: "S",
		Arm: []spi.ScheduledTask{fb.spec(c.EntityID, "S", "T2", stFuture)}})
	require.ErrorIs(t, err, spi.ErrTxTenantMismatch, "ReconcileForEntity")
	require.ErrorIs(t, fa.sts.RemoveLife(txCtx, fb.tenant, c.ID, c.ArmToken), spi.ErrTxTenantMismatch, "RemoveLife")
	require.ErrorIs(t, fa.sts.StampSegment(txCtx, stRef(c), true), spi.ErrTxTenantMismatch, "StampSegment")
	require.ErrorIs(t, fa.sts.DeleteForEntities(txCtx, fb.tenant, []string{c.EntityID}), spi.ErrTxTenantMismatch, "DeleteForEntities")
	require.ErrorIs(t, fa.sts.DeleteForModel(txCtx, fb.tenant, fb.model, 1, nil), spi.ErrTxTenantMismatch, "DeleteForModel")
	require.ErrorIs(t, fa.sts.Fail(txCtx, stRef(c), spi.Failure{Reason: spi.FailureRunPanicked, Error: "E", AtMs: stNow}),
		spi.ErrTxTenantMismatch, "Fail")

	requireUnchangedClaim(t, c, fb.mustGet(c.ID))
}
