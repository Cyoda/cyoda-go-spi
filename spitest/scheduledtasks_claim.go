package spitest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

func testSTClaimDueWaiting(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	due := f.armDue()
	notDue := f.arm(stNow + 1)
	owner := uuid.New()

	c := f.claimTask(owner, due.ID) // requires notDue to stay unclaimed
	require.Equal(t, due.ArmToken, c.ArmToken, "a claim keeps the life")
	require.Zero(t, c.LostOwners, "a claim of a WAITING task is not a lost-owner claim")
	require.Zero(t, c.Attempts)
	require.False(t, c.UnsafeMarked)
	requireUnchangedClaim(t, c, f.mustGet(due.ID)) // the claim is persisted, not only returned

	require.Empty(t, f.claimWith(f.claimReq(uuid.New(), false)), "a RUNNING task is not claimed again")
	require.Equal(t, spi.ScheduledTaskWaiting, f.mustGet(notDue.ID).Status)

	req := f.claimReq(uuid.New(), false)
	req.NowMs = stNow + 1
	require.Equal(t, []string{notDue.ID}, stIDs(f.claimWith(req)), "due exactly when NextAttemptTime <= NowMs")
}

func testSTClaimNewTokenPerClaim(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	owner := uuid.New()
	first := f.claimTask(owner, task.ID)
	n, err := f.sts.GiveBackIdle(context.Background(), owner, nil)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	second := f.claimTask(owner, task.ID)
	require.NotEqual(t, first.Claim.Token, second.Claim.Token, "every claim draws a new token, even for the same owner")
	require.Equal(t, first.ArmToken, second.ArmToken)
}

func testSTClaimFailedNeverClaimed(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID)
	require.NoError(t, f.sts.Fail(f.ctx, stRef(c), spi.Failure{Reason: spi.FailureRunPanicked, Error: "E", AtMs: stNow}))

	req := f.claimReq(uuid.New(), true)
	req.NowMs = stFuture
	require.Nil(t, findST(f.claimWith(req), task.ID), "a FAILED task is never claimed, by any kind of claim")
}

func testSTClaimRunningNeedsAllowLostOwner(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID) // the owner has no liveness record
	require.Empty(t, f.claimWith(f.claimReq(uuid.New(), false)),
		"without AllowLostOwner a RUNNING task is never claimed, however stale its owner")
	requireUnchangedClaim(t, c, f.mustGet(task.ID))
}

func testSTClaimFreshOwnerKept(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	owner := uuid.New()
	require.NoError(t, f.sts.Heartbeat(context.Background(), owner))
	require.NoError(t, f.sts.Heartbeat(context.Background(), owner), "Heartbeat is an upsert")
	task := f.armDue()
	c := f.claimTask(owner, task.ID)

	require.Empty(t, f.claimWith(f.claimReq(uuid.New(), true)), "a task whose owner heartbeats is not lost")
	requireUnchangedClaim(t, c, f.mustGet(task.ID))
}

func testSTClaimStaleOwnerReclaimed(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	a := uuid.New()
	require.NoError(t, f.sts.Heartbeat(context.Background(), a))
	task := f.armDue()
	ca := f.claimTask(a, task.ID)

	h.AdvanceClock(3 * stShortStale)
	b := uuid.New()
	req := f.claimReq(b, true)
	req.StaleAfter = stShortStale
	cb := f.claimOnly(req, task.ID)
	require.Equal(t, ca.ArmToken, cb.ArmToken, "a lost-owner claim keeps the life")
	require.NotEqual(t, ca.Claim.Token, cb.Claim.Token)
	require.Equal(t, 1, cb.LostOwners)
	require.Zero(t, cb.Attempts)
	require.True(t, cb.ClaimedFromLostOwner, "a lost-owner claim is flagged on the ClaimDue result")
	requireUnchangedClaim(t, cb, f.mustGet(task.ID))
}

// ClaimedFromLostOwner is set only on a ClaimDue result, and only when that
// claim took the task from a stale or missing owner: the scheduler counts
// cyoda.scheduler.claims{reason} from it. A read never carries it.
func testSTClaimLostOwnerFlagged(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	first := f.claimTask(uuid.New(), task.ID) // this owner never heartbeats: missing counts as stale
	require.False(t, first.ClaimedFromLostOwner, "a claim of a WAITING task is not from a lost owner")
	require.False(t, f.mustGet(task.ID).ClaimedFromLostOwner, "Get never carries the flag")

	second := f.reclaim(uuid.New(), task.ID)
	require.True(t, second.ClaimedFromLostOwner, "a lost-owner claim is flagged on the ClaimDue result")
	require.False(t, f.mustGet(task.ID).ClaimedFromLostOwner, "Get never carries the flag")

	page, err := f.sts.Query(f.ctx, f.tenant, spi.ScheduledTaskQuery{EntityID: task.EntityID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.False(t, page.Items[0].ClaimedFromLostOwner, "Query never carries the flag")
}

// Every lost-owner claim adds one to LostOwners (spec §13 row "owner lost 3
// times → FAILED OWNER_LOST_REPEATEDLY": the store counts, the engine
// decides).
func testSTClaimLostOwnersCounted(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID)
	for want := 1; want <= 3; want++ {
		c = f.reclaim(uuid.New(), task.ID)
		require.Equal(t, want, c.LostOwners)
	}
	require.Equal(t, 3, f.mustGet(task.ID).LostOwners)
	require.Zero(t, f.mustGet(task.ID).Attempts, "a lost owner is not a counted attempt")
}

// Two due siblings: one claim per call (spec §13).
func testSTClaimOnePerEntity(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	e := f.newEntity()
	s1 := f.spec(e, "S", "T1", stDue)
	s2 := f.spec(e, "S", "T2", stDue)
	f.reconcile(f.ctx, e, "S", s1, s2)

	first := f.claimWith(f.claimReq(uuid.New(), false))
	require.Len(t, first, 1, "at most one task per entity per call")
	require.Empty(t, f.claimWith(f.claimReq(uuid.New(), false)),
		"a task is not claimed while another task of its entity is RUNNING")

	// A lost-owner claim may take the RUNNING task back, never its sibling.
	again := f.claimWith(f.claimReq(uuid.New(), true))
	require.Len(t, again, 1)
	require.Equal(t, first[0].ID, again[0].ID)

	// Once the RUNNING task is back to WAITING and not due, the sibling is
	// claimable.
	require.NoError(t, f.sts.RecordAttempt(f.ctx, stRef(again[0]), spi.Attempt{
		Error: "E", AtMs: stNow, NextAttemptTime: stFuture}))
	sibling := s2.ID
	if first[0].ID == s2.ID {
		sibling = s1.ID
	}
	require.Equal(t, []string{sibling}, stIDs(f.claimWith(f.claimReq(uuid.New(), false))))
}

func testSTClaimLimitAndOrder(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	byTime := map[int64]string{}
	for _, at := range []int64{stDue - 20, stDue - 50, stDue - 10, stDue - 40, stDue - 30} {
		byTime[at] = f.arm(at).ID
	}
	req := f.claimReq(uuid.New(), false)
	req.Limit = 2
	res, err := f.sts.ClaimDue(context.Background(), req)
	require.NoError(t, err)
	require.LessOrEqual(t, len(res), 2, "Limit caps the whole call")
	require.ElementsMatch(t, []string{byTime[stDue-50], byTime[stDue-40]}, stIDs(f.own(res)),
		"within a tenant, tasks are claimed in NextAttemptTime order")
}

// Limit and PerTenantLimit below 1 are caller errors, not "claim nothing".
func testSTClaimInvalidLimits(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	for name, mutate := range map[string]func(*spi.ClaimRequest){
		"Limit 0":           func(r *spi.ClaimRequest) { r.Limit = 0 },
		"Limit -1":          func(r *spi.ClaimRequest) { r.Limit = -1 },
		"PerTenantLimit 0":  func(r *spi.ClaimRequest) { r.PerTenantLimit = 0 },
		"PerTenantLimit -1": func(r *spi.ClaimRequest) { r.PerTenantLimit = -1 },
	} {
		req := f.claimReq(uuid.New(), false)
		mutate(&req)
		_, err := f.sts.ClaimDue(context.Background(), req)
		require.ErrorIs(t, err, spi.ErrStoreRejected, name)
	}
	require.Equal(t, spi.ScheduledTaskWaiting, f.mustGet(task.ID).Status, "a refused call claims nothing")
}

func testSTClaimTenantsTakeTurns(t *testing.T, h Harness) {
	fa := newSTFixture(t, h)
	fb := newSTFixture(t, h)
	// Tenant A's tasks are all older than tenant B's: an order by time alone
	// would give both slots to A.
	for i := int64(0); i < 3; i++ {
		fa.arm(stDue - 100 + i)
		fb.arm(stDue - 10 + i)
	}
	req := fa.claimReq(uuid.New(), false)
	req.Limit = 2
	res, err := fa.sts.ClaimDue(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, fa.own(res), 1, "tenants take turns")
	require.Len(t, fb.own(res), 1, "tenants take turns")
}

func testSTClaimPerTenantLimit(t *testing.T, h Harness) {
	fa := newSTFixture(t, h)
	fb := newSTFixture(t, h)
	var aOldest []string
	for i := int64(0); i < 4; i++ {
		id := fa.arm(stDue - 100 + i).ID
		if i < 2 {
			aOldest = append(aOldest, id)
		}
		fb.arm(stDue - 10 + i)
	}

	// Tenant A already has 2 runs in progress: at PerTenantLimit 2 it gets
	// none, and B still gets its 2.
	req := fa.claimReq(uuid.New(), false)
	req.PerTenantLimit = 2
	req.TenantInProgress = map[spi.TenantID]int{fa.tenant: 2}
	res, err := fa.sts.ClaimDue(context.Background(), req)
	require.NoError(t, err)
	require.Empty(t, fa.own(res))
	require.Len(t, fb.own(res), 2)

	// With nothing in progress, A gets its 2 oldest.
	req.TenantInProgress = nil
	res, err = fa.sts.ClaimDue(context.Background(), req)
	require.NoError(t, err)
	require.ElementsMatch(t, aOldest, stIDs(fa.own(res)))
}

// Concurrent ClaimDue calls get disjoint sets (spec §13).
func testSTClaimConcurrentDisjoint(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	const tasks, callers = 20, 4
	for i := 0; i < tasks; i++ {
		f.armDue()
	}
	owners := make([]uuid.UUID, callers)
	results := make([][]spi.ScheduledTask, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range owners {
		owners[i] = uuid.New()
		req := f.claimReq(owners[i], false)
		req.Limit = 5
		wg.Add(1)
		go func(i int, req spi.ClaimRequest) {
			defer wg.Done()
			<-start
			results[i], errs[i] = f.sts.ClaimDue(context.Background(), req)
		}(i, req)
	}
	close(start)
	wg.Wait()

	seen := map[string]bool{}
	for i := range results {
		require.NoError(t, errs[i])
		for _, c := range f.own(results[i]) {
			require.False(t, seen[c.ID], "task %s was claimed by two concurrent calls", c.ID)
			seen[c.ID] = true
			require.Equal(t, owners[i], c.Claim.Owner)
			requireUnchangedClaim(t, c, f.mustGet(c.ID))
		}
	}
	require.NotEmpty(t, seen, "the concurrent calls must have actually claimed something")
}

// Two due siblings, two pnodes at once: one wins, the other claims nothing
// of that entity (spec §13).
func testSTClaimSiblingsConcurrent(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	for i := 0; i < 10; i++ {
		e := f.newEntity()
		f.reconcile(f.ctx, e, "S", f.spec(e, "S", "T1", stDue), f.spec(e, "S", "T2", stDue))

		var results [2][]spi.ScheduledTask
		var errs [2]error
		start := make(chan struct{})
		var wg sync.WaitGroup
		for j := 0; j < 2; j++ {
			req := f.claimReq(uuid.New(), false)
			wg.Add(1)
			go func(j int, req spi.ClaimRequest) {
				defer wg.Done()
				<-start
				results[j], errs[j] = f.sts.ClaimDue(context.Background(), req)
			}(j, req)
		}
		close(start)
		wg.Wait()

		claimed := 0
		for j := 0; j < 2; j++ {
			require.NoError(t, errs[j], "losing a sibling race is not an error")
			for _, c := range f.own(results[j]) {
				if c.EntityID == e {
					claimed++
				}
			}
		}
		require.Equal(t, 1, claimed, "iteration %d: exactly one task of the entity is claimed", i)
	}
}

// Contended claim loop: a task claimed elsewhere between ranking and
// locking is never claimed again (spec §13).
func testSTClaimContendedNoReclaim(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	const tasks, callers, rounds = 40, 8, 20
	for i := 0; i < tasks; i++ {
		f.armDue()
	}
	var mu sync.Mutex
	counts := map[string]int{}
	var firstErr error
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		req := f.claimReq(uuid.New(), false)
		req.Limit = 3
		wg.Add(1)
		go func(req spi.ClaimRequest) {
			defer wg.Done()
			<-start
			for r := 0; r < rounds; r++ {
				res, err := f.sts.ClaimDue(context.Background(), req)
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = err
				}
				for _, c := range f.own(res) {
					counts[c.ID]++
				}
				mu.Unlock()
			}
		}(req)
	}
	close(start)
	wg.Wait()
	require.NoError(t, firstErr)

	// Whatever the contention left behind is claimable now, once.
	for _, c := range f.claimWith(f.claimReq(uuid.New(), false)) {
		counts[c.ID]++
	}
	require.Len(t, counts, tasks, "every task is claimed")
	for id, n := range counts {
		require.Equal(t, 1, n, "task %s was claimed %d times", id, n)
	}
}

// SweepOwners removes a liveness record no task references. The effect is
// visible through a lost-owner claim: a task claimed by a swept owner is
// lost even under a StaleAfter its heartbeat would have met.
func testSTLivenessSweepRemovesUnreferenced(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	a := uuid.New()
	require.NoError(t, f.sts.Heartbeat(context.Background(), a))
	h.AdvanceClock(3 * stShortStale)
	require.NoError(t, f.sts.SweepOwners(context.Background(), stShortStale))

	task := f.armDue()
	f.claimTask(a, task.ID)
	c := f.reclaim(uuid.New(), task.ID)
	require.Equal(t, 1, c.LostOwners)
}

func testSTLivenessSweepKeepsReferenced(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	a := uuid.New()
	require.NoError(t, f.sts.Heartbeat(context.Background(), a))
	task := f.armDue()
	c := f.claimTask(a, task.ID)
	h.AdvanceClock(3 * stShortStale)
	require.NoError(t, f.sts.SweepOwners(context.Background(), stShortStale))

	require.Empty(t, f.claimWith(f.claimReq(uuid.New(), true)),
		"the record of an owner a task references is not swept")
	requireUnchangedClaim(t, c, f.mustGet(task.ID))
}

// A liveness record swept during a long outage is recreated by the next
// heartbeat (spec §13).
func testSTLivenessHeartbeatRecreatesSwept(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	a := uuid.New()
	require.NoError(t, f.sts.Heartbeat(context.Background(), a))
	h.AdvanceClock(3 * stShortStale)
	require.NoError(t, f.sts.SweepOwners(context.Background(), stShortStale))
	require.NoError(t, f.sts.Heartbeat(context.Background(), a))

	task := f.armDue()
	c := f.claimTask(a, task.ID)
	require.Empty(t, f.claimWith(f.claimReq(uuid.New(), true)), "the recreated record keeps the owner live")
	requireUnchangedClaim(t, c, f.mustGet(task.ID))
}

func testSTLivenessRetireOwner(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	a := uuid.New()
	require.NoError(t, f.sts.Heartbeat(context.Background(), a))
	task := f.armDue()
	f.claimTask(a, task.ID)
	require.NoError(t, f.sts.RetireOwner(context.Background(), a))
	c := f.reclaim(uuid.New(), task.ID)
	require.Equal(t, 1, c.LostOwners, "a retired owner has no liveness: its tasks are lost")
}

// A lost claim reply: the next GiveBackIdle returns the claimed tasks
// (spec §13).
func testSTGiveBackLostReply(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	var ids []string
	for i := 0; i < 3; i++ {
		ids = append(ids, f.armDue().ID)
	}
	a := uuid.New()
	require.Len(t, f.claimWith(f.claimReq(a, false)), 3) // the reply is "lost": nothing registers it

	n, err := f.sts.GiveBackIdle(context.Background(), a, nil)
	require.NoError(t, err)
	require.Equal(t, 3, n)
	for _, id := range ids {
		got := f.mustGet(id)
		require.Equal(t, spi.ScheduledTaskWaiting, got.Status)
		require.Nil(t, got.Claim)
		require.Zero(t, got.Attempts, "a give-back is not counted")
		require.Zero(t, got.LostOwners)
	}
	require.ElementsMatch(t, ids, stIDs(f.claimWith(f.claimReq(uuid.New(), false))),
		"given-back tasks are claimable at once")
}

// A RUNNING task with no live run is given back; a live run never is
// (spec §13).
func testSTGiveBackKeepsLiveRuns(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	a := uuid.New()
	live := f.claimTask(a, f.armDue().ID)
	idle := f.claimTask(a, f.armDue().ID)
	other := f.claimTask(uuid.New(), f.armDue().ID)

	n, err := f.sts.GiveBackIdle(context.Background(), a, []uuid.UUID{live.Claim.Token})
	require.NoError(t, err)
	require.Equal(t, 1, n)
	requireUnchangedClaim(t, live, f.mustGet(live.ID))
	require.Equal(t, spi.ScheduledTaskWaiting, f.mustGet(idle.ID).Status)
	requireUnchangedClaim(t, other, f.mustGet(other.ID)) // another owner's task

	n, err = f.sts.GiveBackIdle(context.Background(), a, []uuid.UUID{live.Claim.Token})
	require.NoError(t, err)
	require.Zero(t, n, "nothing left to give back is a normal no-op")
}

// GiveBackIdle is not counted and keeps the life's mark (spec §13 row
// "GiveBackIdle is not counted; RetireOwner removes liveness").
func testSTGiveBackNotCountedKeepsMark(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	a := uuid.New()
	task := f.armDue()
	c := f.claimTask(a, task.ID)
	require.NoError(t, f.sts.MarkUnsafe(f.ctx, stRef(c)))
	n, err := f.sts.GiveBackIdle(context.Background(), a, nil)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	next := f.claimTask(uuid.New(), task.ID)
	require.Zero(t, next.Attempts)
	require.Zero(t, next.LostOwners)
	require.True(t, next.UnsafeMarked, "the mark belongs to the life; a give-back does not remove it")
}

// --- Fix round 1: coverage gaps found by review. ---

// A second heartbeat before the old one goes stale keeps the owner live;
// without it the elapsed time alone would exceed StaleAfter.
func testSTLivenessHeartbeatRefreshes(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	a := uuid.New()
	require.NoError(t, f.sts.Heartbeat(context.Background(), a))
	task := f.armDue()
	c := f.claimTask(a, task.ID)

	h.AdvanceClock(60 * time.Millisecond)
	h.AdvanceClock(60 * time.Millisecond)
	h.AdvanceClock(60 * time.Millisecond)
	require.NoError(t, f.sts.Heartbeat(context.Background(), a))

	// StaleAfter leaves about 150ms of headroom between the refreshing
	// heartbeat and this claim, so a real-clock harness (PostgreSQL, whose
	// AdvanceClock sleeps for real) has margin against flaking: the record
	// only needs to read as fresher than 150ms old, not fresher than the
	// 180ms it would be without the refresh.
	req := f.claimReq(uuid.New(), true)
	req.StaleAfter = 150 * time.Millisecond
	require.Empty(t, f.claimWith(req),
		"the refreshing heartbeat keeps the owner live; without it, the 180ms elapsed would exceed a 150ms StaleAfter")
	requireUnchangedClaim(t, c, f.mustGet(task.ID))
}

// A lost-owner RUNNING task is claimable whatever its NextAttemptTime (the
// NowMs-vs-NextAttemptTime comparison applies only to WAITING tasks).
func testSTClaimLostOwnerIgnoresNextAttemptTime(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.arm(stNow + 10)

	due := f.claimReq(uuid.New(), false)
	due.NowMs = stNow + 10
	f.claimOnly(due, task.ID)
	require.Equal(t, stNow+10, f.mustGet(task.ID).NextAttemptTime,
		"claiming a WAITING task does not change NextAttemptTime")

	lost := f.claimReq(uuid.New(), true)
	lost.NowMs = stNow // earlier than the task's NextAttemptTime
	cb := f.claimOnly(lost, task.ID)
	require.Equal(t, 1, cb.LostOwners,
		"a lost-owner RUNNING task is claimable whatever its NextAttemptTime")
}

// One AllowLostOwner call claims a due WAITING task and a RUNNING task whose
// owner is missing in the same round; ClaimedFromLostOwner and LostOwners
// are set per task, not for the whole result.
func testSTClaimLostOwnerFlagPerTask(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	running := f.armDue()
	f.claimTask(uuid.New(), running.ID) // this owner never heartbeats: missing counts as stale
	waiting := f.armDue()               // armed only now: claimTask above requires exactly one due task

	res := f.claimWith(f.claimReq(uuid.New(), true))
	require.Len(t, res, 2, "one AllowLostOwner call claims both the due WAITING task and the lost RUNNING task")

	w := findST(res, waiting.ID)
	require.NotNil(t, w)
	require.False(t, w.ClaimedFromLostOwner, "a claim of a WAITING task is not from a lost owner")
	require.Zero(t, w.LostOwners)

	r := findST(res, running.ID)
	require.NotNil(t, r)
	require.True(t, r.ClaimedFromLostOwner, "a claim of a lost RUNNING task is flagged")
	require.Equal(t, 1, r.LostOwners)
}

// Order: the tenant with the earliest candidate goes first, whatever its
// tenant id or arm (insertion) order, and each tenant's turn recurs every
// round (persistence.go ClaimDue doc). Round-robin fairness itself —
// each tenant gets at least one — is Claim/TenantsTakeTurns's job; this
// case pins the exact split, which a naive "take the earliest N overall"
// selection (ignoring tenant fairness) would get wrong.
func testSTClaimOrderEarliestTenantFirst(t *testing.T, h Harness) {
	fa := newSTFixture(t, h)
	fb := newSTFixture(t, h)
	small, big := fa, fb
	if big.tenant < small.tenant {
		small, big = big, small
	}
	// small has the smaller tenant id and is armed FIRST, with the single
	// LATEST candidate. big has the larger tenant id, is armed LAST, and
	// holds the 3 EARLIEST candidates. Neither insertion order nor
	// tenant-id order coincides with the correct NextAttemptTime order, so
	// only a store that actually orders by NextAttemptTime passes.
	small.arm(stDue - 10)
	big.arm(stDue - 100)
	big.arm(stDue - 90)
	big.arm(stDue - 80)

	// (a) Limit 1: the earliest candidate's tenant (big) gets the only
	// slot, despite its larger tenant id and later insertion.
	req := big.claimReq(uuid.New(), false)
	req.Limit = 1
	res, err := big.sts.ClaimDue(context.Background(), req)
	require.NoError(t, err)
	got := big.own(res)
	require.Len(t, got, 1, "the earliest candidate's tenant gets the only slot, whatever its tenant id or insertion order")
	require.Empty(t, small.own(res))

	// Give the claim back so the due set is unchanged for (b).
	n, err := big.sts.GiveBackIdle(context.Background(), got[0].Claim.Owner, nil)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	// (b) Limit 3: big holds the 3 earliest candidates overall, so a naive
	// "take the earliest N, ignoring tenant fairness" selection would give
	// big all 3 and small none. Round-robin instead gives every tenant a
	// turn each round: big goes first each round (2 turns fit in 3 slots),
	// small goes once — 2 for big, 1 for small; this is the split
	// round-robin gets right and plain time order gets wrong.
	req.Limit = 3
	res, err = big.sts.ClaimDue(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, big.own(res), 2, "the earliest-candidate tenant's turn comes first each round")
	require.Len(t, small.own(res), 1, "round-robin still gives the other tenant its turn, unlike a naive earliest-N-overall selection")
}

// Order: a NextAttemptTime tie across tenants is broken by tenant id
// byte-wise, smaller first (persistence.go ClaimDue doc).
func testSTClaimOrderTenantTieBrokenByID(t *testing.T, h Harness) {
	fa := newSTFixture(t, h)
	fb := newSTFixture(t, h)
	lo, hi := fa, fb
	if hi.tenant < lo.tenant {
		lo, hi = hi, lo
	}

	// hi (the larger tenant id) is armed FIRST and given the
	// lexicographically SMALLER task id; lo (the smaller tenant id) is
	// armed SECOND with the LARGER task id. Neither insertion order nor
	// task-id order coincides with the correct tenant-id tie-break, so
	// only a store that breaks the NextAttemptTime tie on tenant id (lo
	// wins) passes.
	hiEntity := hi.newEntity()
	hiTask := hi.spec(hiEntity, "S", "T", stDue)
	hiTask.ID = "aaa-" + hiTask.ID
	hi.reconcile(hi.ctx, hiEntity, "S", hiTask)

	loEntity := lo.newEntity()
	loTask := lo.spec(loEntity, "S", "T", stDue)
	loTask.ID = "zzz-" + loTask.ID
	lo.reconcile(lo.ctx, loEntity, "S", loTask)

	req := lo.claimReq(uuid.New(), false)
	req.Limit = 1
	res, err := lo.sts.ClaimDue(context.Background(), req)
	require.NoError(t, err)
	got := append(lo.own(res), hi.own(res)...)
	require.Len(t, got, 1)
	require.Equal(t, loTask.ID, got[0].ID,
		"a NextAttemptTime tie is broken by tenant id byte-wise (lo wins), never by insertion order or task id (hi's task id sorts lower)")
}

// Order: a NextAttemptTime tie within one tenant is broken by task ID
// byte-wise, smaller first (persistence.go ClaimDue doc). newID() is
// time-based and its string order is not predictable, so the two task ids
// are constructed with a KNOWN order, and the larger one is reconciled
// FIRST: only a store that actually orders by task id passes; one that
// orders by insertion fails.
func testSTClaimOrderEntityTieBrokenByID(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	eHi, eLo := f.newEntity(), f.newEntity()
	hiTask := f.spec(eHi, "S", "T", stDue)
	hiTask.ID = "zzz-" + hiTask.ID
	f.reconcile(f.ctx, eHi, "S", hiTask) // larger id, reconciled first

	loTask := f.spec(eLo, "S", "T", stDue)
	loTask.ID = "aaa-" + loTask.ID
	f.reconcile(f.ctx, eLo, "S", loTask) // smaller id, reconciled second

	req := f.claimReq(uuid.New(), false)
	req.Limit = 1
	res, err := f.sts.ClaimDue(context.Background(), req)
	require.NoError(t, err)
	got := f.own(res)
	require.Len(t, got, 1)
	require.Equal(t, loTask.ID, got[0].ID,
		"a NextAttemptTime tie among one tenant's tasks is broken by task id byte-wise (smaller wins), never by insertion order")
}

// PerTenantLimit and TenantInProgress combine by subtraction: a partial
// quota claims exactly that many, the oldest first.
func testSTClaimPerTenantLimitPartialQuota(t *testing.T, h Harness) {
	fa := newSTFixture(t, h)
	fb := newSTFixture(t, h)
	oldest := fa.arm(stDue - 100).ID
	fa.arm(stDue - 90)
	fb.arm(stDue - 10)

	req := fa.claimReq(uuid.New(), false)
	req.PerTenantLimit = 2
	req.TenantInProgress = map[spi.TenantID]int{fa.tenant: 1}
	res, err := fa.sts.ClaimDue(context.Background(), req)
	require.NoError(t, err)
	got := fa.own(res)
	require.Len(t, got, 1, "quota is PerTenantLimit minus TenantInProgress[tenant]")
	require.Equal(t, oldest, got[0].ID, "the one slot goes to the oldest candidate")
}

// The OnePerEntity dedup key is (tenant, entity), not the bare entity id: two
// tenants may each arm a task under the same EntityID string, and neither
// blocks the other.
func testSTClaimEntityKeyIsPerTenant(t *testing.T, h Harness) {
	fa := newSTFixture(t, h)
	fb := newSTFixture(t, h)

	// First half: one call claims both tenants' due tasks under the same
	// EntityID string. A store deduping candidates on the bare entity id
	// would claim only one of the two in this single call.
	entity := newID()
	fa.entities = append(fa.entities, entity)
	fb.entities = append(fb.entities, entity)
	fa.reconcile(fa.ctx, entity, "S", fa.spec(entity, "S", "T", stDue))
	fb.reconcile(fb.ctx, entity, "S", fb.spec(entity, "S", "T", stDue))

	res, err := fa.sts.ClaimDue(context.Background(), fa.claimReq(uuid.New(), false))
	require.NoError(t, err)
	require.Len(t, fa.own(res), 1, "same entity id in another tenant does not collide")
	require.Len(t, fb.own(res), 1, "one call claims both tenants' tasks of the same entity id")

	// Second half: tenant A's task of one entity id is claimed (RUNNING) in
	// one call; a SECOND, later call must still claim tenant B's due task
	// of the SAME entity id. A candidate query keyed on the bare entity id
	// (not (tenant, entity)) would wrongly treat B's task as blocked by
	// A's RUNNING one — this is exactly where that bug would hide.
	entity2 := newID()
	fa.entities = append(fa.entities, entity2)
	fb.entities = append(fb.entities, entity2)
	fa.reconcile(fa.ctx, entity2, "S", fa.spec(entity2, "S", "T", stDue))
	fa.claimTask(uuid.New(), fa.taskID(entity2, "S", "T")) // call 1: only A's task exists yet

	fb.reconcile(fb.ctx, entity2, "S", fb.spec(entity2, "S", "T", stDue)) // B's task armed while A's is RUNNING
	claimedB := fb.claimTask(uuid.New(), fb.taskID(entity2, "S", "T"))    // call 2: B's task, same entity id, still claimable
	require.Equal(t, entity2, claimedB.EntityID)
}

// GiveBackIdle leaves Attempts and LostOwners exactly as they were: it is
// not counted and does not clear the lost-owner count.
func testSTGiveBackKeepsCounters(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	a := uuid.New()
	task := f.armDue()
	c := f.claimTask(a, task.ID)
	require.NoError(t, f.sts.RecordAttempt(f.ctx, stRef(c), spi.Attempt{
		Error: "E", AtMs: stNow, NextAttemptTime: stNow}))
	c = f.claimTask(a, task.ID)
	require.Equal(t, 1, c.Attempts)

	c = f.reclaim(a, task.ID) // a never heartbeats: its own claim is lost-owner reclaimable
	require.Equal(t, 1, c.LostOwners)
	require.Equal(t, 1, c.Attempts)

	n, err := f.sts.GiveBackIdle(context.Background(), a, nil)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	got := f.mustGet(task.ID)
	require.Equal(t, spi.ScheduledTaskWaiting, got.Status)
	require.Equal(t, 1, got.Attempts, "give-back does not reset Attempts")
	require.Equal(t, 1, got.LostOwners, "give-back does not reset LostOwners")
}
