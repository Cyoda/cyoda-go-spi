package spitest

import (
	"context"
	"sync"
	"testing"

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
	requireUnchangedClaim(t, cb, f.mustGet(task.ID))
}

// ClaimedFromLostOwner is set only on a ClaimDue result, and only when that
// claim took the task from a stale or missing owner (README C-S1): the
// scheduler counts cyoda.scheduler.claims{reason} from it. A read never
// carries it.
func testSTClaimLostOwnerFlagged(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	first := f.claimTask(uuid.New(), task.ID) // this owner never heartbeats: missing counts as stale
	require.False(t, first.ClaimedFromLostOwner, "a claim of a WAITING task is not from a lost owner")
	require.False(t, f.mustGet(task.ID).ClaimedFromLostOwner, "Get never carries the flag")

	second := f.reclaim(uuid.New(), task.ID)
	require.True(t, second.ClaimedFromLostOwner, "a lost-owner claim is flagged on the ClaimDue result")
	require.False(t, f.mustGet(task.ID).ClaimedFromLostOwner, "Get never carries the flag")
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
		require.Error(t, err, name)
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
	_, err := f.sts.GiveBackIdle(context.Background(), a, nil)
	require.NoError(t, err)

	next := f.claimTask(uuid.New(), task.ID)
	require.Zero(t, next.Attempts)
	require.Zero(t, next.LostOwners)
	require.True(t, next.UnsafeMarked, "the mark belongs to the life; a give-back does not remove it")
}
