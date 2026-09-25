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

// armID arms one task on a new entity with an explicit id, so a test can
// control the byte order of a (ScheduledTime, ID) tie directly instead of
// leaving it to however the store generates ids.
func (f *stFixture) armID(id string, scheduledTime int64) spi.ScheduledTask {
	f.t.Helper()
	e := f.newEntity()
	s := f.spec(e, "S", "T", scheduledTime)
	s.ID = id
	f.reconcile(f.ctx, e, "S", s)
	return f.mustGet(id)
}

// 200, no filter, several pages, IDs compared byte-wise (spec §13,
// GET /scheduled-tasks).
func testSTQueryPagesInOrder(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	tiedBase := uuid.NewString()

	var want []spi.ScheduledTask
	want = append(want, f.arm(stFuture+10))
	// A tied pair at the same ScheduledTime, armed "-a" then "-B". Byte-wise,
	// "B" (0x42) sorts before "a" (0x61): the correct order is the reverse
	// of arm order, and also differs from en_US/ICU collation (which would
	// put "-a" first case-insensitively) — a PostgreSQL store without
	// COLLATE "C" fails this.
	tiedA := f.armID(tiedBase+"-a", stFuture+20)
	tiedB := f.armID(tiedBase+"-B", stFuture+20)
	want = append(want, tiedA, tiedB)
	want = append(want, f.arm(stFuture+30))
	want = append(want, f.arm(stFuture+40))
	slices.SortFunc(want, func(a, b spi.ScheduledTask) int {
		if c := cmp.Compare(a.ScheduledTime, b.ScheduledTime); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	// want is now [+10, +20-B, +20-a, +30, +40]: tiedB before tiedA.
	require.Equal(t, tiedB.ID, want[1].ID, "test setup: the tie-break puts -B before -a")
	require.Equal(t, tiedA.ID, want[2].ID, "test setup: the tie-break puts -B before -a")

	var got []string
	var pages [][]string
	var after *spi.ScheduledTaskCursor
	for {
		page := f.query(spi.ScheduledTaskQuery{After: after, Limit: 2})
		require.LessOrEqual(t, len(pages)+1, 3, "5 tasks at 2 per page is 3 pages")
		ids := stIDs(page.Items)
		pages = append(pages, ids)
		got = append(got, ids...)
		if page.Next == nil {
			break
		}
		require.Len(t, page.Items, 2, "only the last page is short")
		last := page.Items[len(page.Items)-1]
		require.Equal(t, spi.ScheduledTaskCursor{ScheduledTime: last.ScheduledTime, ID: last.ID}, *page.Next)
		after = page.Next
	}
	require.Equal(t, stIDs(want), got, "(ScheduledTime, ID) order, IDs compared byte-wise")
	// The page boundary falls inside the tie: page 1 ends on tiedB (the
	// first of the tied pair in the correct order), page 2 starts on
	// tiedA. A cursor that compares only ScheduledTime, ignoring the tied
	// ID already returned, would drop tiedA or repeat tiedB here.
	require.Len(t, pages, 3, "5 tasks at 2 per page is 3 pages") // guards the indexing below
	require.Equal(t, []string{want[0].ID, want[1].ID}, pages[0], "page 1 ends inside the tie")
	require.Equal(t, []string{want[2].ID, want[3].ID}, pages[1], "page 2 starts inside the tie")

	whole := f.query(spi.ScheduledTaskQuery{Limit: 5})
	require.Len(t, whole.Items, 5)
	require.Nil(t, whole.Next, "no further page when exactly Limit tasks remain")

	for i, x := range whole.Items {
		require.Equal(t, want[i], x, "Query returns the whole record, in order")
	}
}

// A filtered page walks past non-matching rows: LIMIT applies after the
// filter, not before, and Next is built from the last matching row, not
// the last raw row (spec §13, GET /scheduled-tasks).
func testSTQueryFilterPagesAcrossNonMatchingRows(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	pageModel := f.model + "-paged"
	var matched []spi.ScheduledTask
	// A trailing filler row after the last match (+60) catches a store that
	// decides Next by peeking one unfiltered row ahead instead of by
	// whether the filter has any more matches.
	for i, at := range []int64{stFuture + 10, stFuture + 20, stFuture + 30, stFuture + 40, stFuture + 50, stFuture + 60} {
		e := f.newEntity()
		s := f.spec(e, "S", "T", at)
		if i%2 == 1 {
			s.ModelName = pageModel + "-filler" // a non-matching row between two matches
		} else {
			s.ModelName = pageModel
		}
		f.reconcile(f.ctx, e, "S", s)
		if i%2 == 0 {
			matched = append(matched, f.mustGet(s.ID))
		}
	}
	require.Len(t, matched, 3, "test setup: three matching rows, with a filler row between each pair and one trailing")

	var got []string
	var after *spi.ScheduledTaskCursor
	pages := 0
	for {
		page := f.query(spi.ScheduledTaskQuery{ModelName: pageModel, After: after, Limit: 1})
		pages++
		require.LessOrEqual(t, pages, 3, "3 matching rows at 1 per page is 3 pages")
		got = append(got, stIDs(page.Items)...)
		if page.Next == nil {
			break
		}
		require.Len(t, page.Items, 1)
		last := page.Items[0]
		require.Equal(t, spi.ScheduledTaskCursor{ScheduledTime: last.ScheduledTime, ID: last.ID}, *page.Next)
		after = page.Next
	}
	require.Equal(t, stIDs(matched), got, "a filtered page skips the non-matching rows between matches")
	require.Equal(t, 3, pages, "3 matching rows at 1 per page is exactly 3 pages") // a store that ignores Limit fails here
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
	require.Equal(t, stNow, *got.FailedTime)
}

// Limit < 1 is a caller error even when the tenant has matching rows: a
// store that pages instead of rejecting, or panics on a non-positive
// limit, fails this.
func testSTQueryInvalidLimit(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	f.arm(stFuture)

	for name, limit := range map[string]int{"0": 0, "-1": -1} {
		q := spi.ScheduledTaskQuery{Limit: limit}
		page, err := f.sts.Query(f.ctx, f.tenant, q)
		require.ErrorIs(t, err, spi.ErrStoreRejected, "Limit %s", name)
		require.Empty(t, page.Items, "Limit %s: no page is returned", name)
		require.Nil(t, page.Next, "Limit %s: no page is returned", name)
	}
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
	for name, tc := range map[string]struct {
		q    spi.ScheduledTaskQuery
		want []string
	}{
		"no filter":        {spi.ScheduledTaskQuery{}, []string{a.EntityID}},
		"every status":     {spi.ScheduledTaskQuery{Statuses: all}, []string{a.EntityID}},
		"shared model":     {spi.ScheduledTaskQuery{ModelName: fa.model}, []string{a.EntityID}},
		"shared model v1":  {spi.ScheduledTaskQuery{ModelName: fa.model, ModelVersion: 1}, []string{a.EntityID}},
		"B's entity":       {spi.ScheduledTaskQuery{EntityID: b.EntityID}, nil},
		"B's RUNNING task": {spi.ScheduledTaskQuery{EntityID: bRunning.EntityID, Statuses: []spi.ScheduledTaskStatus{spi.ScheduledTaskRunning}}, nil},
	} {
		// Exact-set, not a loop that passes vacuously on an empty result:
		// tenant A sees exactly its own task and never B's, under every
		// filter, including one that names B's own entity/status.
		require.ElementsMatch(t, tc.want, fa.queryEntities(tc.q), name)
	}

	// Positive control: the same filters against tenant B DO find B's
	// tasks, proving the empty results above are tenant scoping and not an
	// unmatched filter.
	require.ElementsMatch(t, []string{b.EntityID}, fb.queryEntities(spi.ScheduledTaskQuery{EntityID: b.EntityID}),
		"B's entity, queried as B, is found")
	require.ElementsMatch(t, []string{bRunning.EntityID},
		fb.queryEntities(spi.ScheduledTaskQuery{EntityID: bRunning.EntityID, Statuses: []spi.ScheduledTaskStatus{spi.ScheduledTaskRunning}}),
		"B's RUNNING task, queried as B, is found")
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
// refused with ErrTxTenantMismatch and changes nothing: a task row of
// tenant B never enters tenant A's transaction.
func testSTTenantJoiningWriteOtherTenantRefused(t *testing.T, h Harness) {
	fb := newSTFixture(t, h)
	fa := newSTFixture(t, h)
	fa.model = fb.model
	c := fb.claimTask(uuid.New(), fb.armDue().ID)
	txID, txCtx := fa.begin() // tenant A's transaction; committed below, after the refusals

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

	// A refused joining write must not even be staged: committing the
	// transaction after every refusal must not surface any of them.
	_ = fa.tm.Commit(txCtx, txID)
	requireUnchangedClaim(t, c, fb.mustGet(c.ID))
}
