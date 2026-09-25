package spitest

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// Every fenced method refuses a stale token (spec §13).
func testSTFenceStaleTokensRefused(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	fb := newSTFixture(t, h)
	c := f.claimTask(uuid.New(), f.armDue().ID)
	good := stRef(c)

	bad := map[string]spi.TaskRef{
		"another claim token": {TenantID: good.TenantID, ID: good.ID, ArmToken: good.ArmToken, ClaimToken: uuid.New()},
		"the nil claim token": {TenantID: good.TenantID, ID: good.ID, ArmToken: good.ArmToken, ClaimToken: uuid.Nil},
		"another arm token":   {TenantID: good.TenantID, ID: good.ID, ArmToken: uuid.New(), ClaimToken: good.ClaimToken},
		"a missing task":      {TenantID: good.TenantID, ID: "st-missing-" + newID(), ArmToken: good.ArmToken, ClaimToken: good.ClaimToken},
		"another tenant":      {TenantID: fb.tenant, ID: good.ID, ArmToken: good.ArmToken, ClaimToken: good.ClaimToken},
	}
	for why, r := range bad {
		ctx := f.ctx
		if r.TenantID == fb.tenant {
			ctx = fb.ctx
		}
		requireAllFencedRefused(t, ctx, f.sts, r, why)
	}
	requireUnchangedClaim(t, c, f.mustGet(c.ID))
}

func testSTFenceWaitingRefused(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.arm(stFuture)
	for _, claim := range []uuid.UUID{uuid.Nil, uuid.New()} {
		r := spi.TaskRef{TenantID: f.tenant, ID: task.ID, ArmToken: task.ArmToken, ClaimToken: claim}
		requireAllFencedRefused(t, f.ctx, f.sts, r, "a task that is not claimed")
	}
	got := f.mustGet(task.ID)
	require.Equal(t, spi.ScheduledTaskWaiting, got.Status)
	require.False(t, got.UnsafeMarked)
	require.False(t, got.PartialCommit)
	require.Empty(t, got.LastError)
}

// After a re-arm, every fenced write of the old life is refused (spec §13).
func testSTFenceOldLifeRefused(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID)
	s := f.spec(task.EntityID, "S", "T", stFuture)
	f.reconcile(f.ctx, task.EntityID, "S", s)

	requireAllFencedRefused(t, f.ctx, f.sts, stRef(c), "a claim of a life that was re-armed")
	requireFreshLife(t, s, f.mustGet(task.ID))
}

// ABA: the old token is refused after a re-arm and a new claim (spec §13).
func testSTFenceABA(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	c1 := f.claimTask(uuid.New(), task.ID)
	f.reconcile(f.ctx, task.EntityID, "S", f.spec(task.EntityID, "S", "T", stDue))
	c2 := f.claimTask(uuid.New(), task.ID)
	require.NotEqual(t, c1.ArmToken, c2.ArmToken)

	for why, r := range map[string]spi.TaskRef{
		"the old life and old claim":  stRef(c1),
		"the new life, the old claim": {TenantID: f.tenant, ID: task.ID, ArmToken: c2.ArmToken, ClaimToken: c1.Claim.Token},
		"the old life, the new claim": {TenantID: f.tenant, ID: task.ID, ArmToken: c1.ArmToken, ClaimToken: c2.Claim.Token},
	} {
		requireAllFencedRefused(t, f.ctx, f.sts, r, why)
	}
	requireUnchangedClaim(t, c2, f.mustGet(task.ID))
	require.NoError(t, f.sts.StampSegment(f.ctx, stRef(c2), false))
	require.NoError(t, f.sts.MarkUnsafe(f.ctx, stRef(c2)))
}

// A replaced owner's segment commit is refused by its stamp (spec §13).
func testSTFenceReplacedOwnerStampRefused(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	a := f.claimTask(uuid.New(), task.ID)
	b := f.reclaim(uuid.New(), task.ID)

	txID, txCtx := f.begin()
	require.ErrorIs(t, f.sts.StampSegment(txCtx, stRef(a), false), spi.ErrStaleClaim)
	_ = f.tm.Rollback(txCtx, txID)
	requireAllFencedRefused(t, f.ctx, f.sts, stRef(a), "a replaced owner's claim")
	requireUnchangedClaim(t, b, f.mustGet(task.ID))
}

// A give-back releases the claim: the old ref is refused by every fenced
// method (spec §13; GiveBackIdle's doc, "returns every task RUNNING under
// owner ... to WAITING").
func testSTFenceGiveBackRefused(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	owner := uuid.New()
	c := f.claimTask(owner, task.ID)

	n, err := f.sts.GiveBackIdle(context.Background(), owner, nil)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	requireAllFencedRefused(t, f.ctx, f.sts, stRef(c), "a claim given back")
	got := f.mustGet(task.ID)
	require.Equal(t, spi.ScheduledTaskWaiting, got.Status)
	require.Nil(t, got.Claim)
	require.Zero(t, got.Attempts)
	require.Empty(t, got.LastError)
}

// RemoveLife then a re-arm of the same id starts a new life; the
// pre-removal ref is refused by every fenced method (spec §13).
func testSTFenceRemoveLifeThenRearmRefused(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID)

	require.NoError(t, f.sts.RemoveLife(f.ctx, f.tenant, task.ID, c.ArmToken))
	f.requireGone(task.ID)

	s := f.spec(task.EntityID, "S", "T", stFuture)
	f.reconcile(f.ctx, task.EntityID, "S", s)
	requireAllFencedRefused(t, f.ctx, f.sts, stRef(c), "a claim of a life RemoveLife removed and a re-arm replaced")
	requireFreshLife(t, s, f.mustGet(task.ID))
}

// StampSegment sets PartialCommit and the next claim of the life carries it
// (spec §13 row "owner killed after a cascade-step commit → next claim
// FAILED STOPPED_AFTER_PARTIAL_COMMIT": the store keeps the flag, the
// engine decides).
func testSTStampPartialCommit(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID)

	require.NoError(t, f.sts.StampSegment(f.ctx, stRef(c), false))
	require.False(t, f.mustGet(task.ID).PartialCommit)
	require.NoError(t, f.sts.StampSegment(f.ctx, stRef(c), true))
	require.True(t, f.mustGet(task.ID).PartialCommit)
	require.NoError(t, f.sts.StampSegment(f.ctx, stRef(c), false))
	got := f.mustGet(task.ID)
	require.True(t, got.PartialCommit, "a stamp without partial keeps PartialCommit")
	require.Equal(t, spi.ScheduledTaskRunning, got.Status, "a stamp changes neither status nor claim")
	require.Equal(t, c.Claim.Token, got.Claim.Token)

	// The owner is gone; the next claim of the life carries the flag.
	next := f.reclaim(uuid.New(), task.ID)
	require.True(t, next.PartialCommit)

	// StampSegment joins: a rolled-back segment leaves no flag.
	other := f.armDue()
	oc := f.claimTask(uuid.New(), other.ID)
	txID, txCtx := f.begin()
	require.NoError(t, f.sts.StampSegment(txCtx, stRef(oc), true))
	require.NoError(t, f.tm.Rollback(txCtx, txID))
	require.False(t, f.mustGet(other.ID).PartialCommit)

	// StampSegment joins: a committed segment keeps the flag.
	other2 := f.armDue()
	oc2 := f.claimTask(uuid.New(), other2.ID)
	txID2, txCtx2 := f.begin()
	require.NoError(t, f.sts.StampSegment(txCtx2, stRef(oc2), true))
	require.NoError(t, f.tm.Commit(txCtx2, txID2))
	require.True(t, f.mustGet(other2.ID).PartialCommit)
}

func testSTMarkAcceptedAndIdempotent(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID)
	require.NoError(t, f.sts.MarkUnsafe(f.ctx, stRef(c)))
	require.NoError(t, f.sts.MarkUnsafe(f.ctx, stRef(c)), "MarkUnsafe is idempotent for the same claim")
	got := f.mustGet(task.ID)
	require.True(t, got.UnsafeMarked)
	require.Equal(t, spi.ScheduledTaskRunning, got.Status)
	require.Equal(t, c.Claim.Token, got.Claim.Token)
}

// A mark outlives its owner: the next claim sees it and cannot mark again
// (spec §13 rows "ErrMarkedByAnotherClaim → FAILED" and
// "RecordAttempt{ClearOwnMark} retried in an outage; pnode dies first →
// FAILED at the next claim").
func testSTMarkMarkedByAnotherClaim(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	a := f.claimTask(uuid.New(), task.ID)
	require.NoError(t, f.sts.MarkUnsafe(f.ctx, stRef(a)))

	b := f.reclaim(uuid.New(), task.ID) // A died holding the mark
	require.True(t, b.UnsafeMarked, "the claim returns the life's mark (C3)")
	require.Equal(t, 1, b.LostOwners)

	// A's claim is stale: its ClearOwnMark is refused, and A's mark stays.
	require.ErrorIs(t, f.sts.RecordAttempt(f.ctx, stRef(a), spi.Attempt{
		Error: "E", AtMs: stNow, NextAttemptTime: stNow, ClearOwnMark: true}), spi.ErrStaleClaim)
	require.True(t, f.mustGet(task.ID).UnsafeMarked, "A's refused ClearOwnMark did not remove the mark")

	require.ErrorIs(t, f.sts.MarkUnsafe(f.ctx, stRef(b)), spi.ErrMarkedByAnotherClaim)
	requireUnchangedClaim(t, b, f.mustGet(task.ID))

	// B never wrote a mark of its own: its ClearOwnMark removes nothing,
	// and the next claim still sees A's mark.
	require.NoError(t, f.sts.RecordAttempt(f.ctx, stRef(b), spi.Attempt{
		Error: "E", AtMs: stNow, NextAttemptTime: stNow, ClearOwnMark: true}))
	next := f.claimTask(uuid.New(), task.ID)
	require.True(t, next.UnsafeMarked,
		"B's refused mark did not take over A's mark, and B's ClearOwnMark did not remove it")
}

// A mark survives the rollback of the transaction on ctx (spec §13).
func testSTMarkSurvivesRollback(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID)
	txID, txCtx := f.begin()
	_, _ = f.get(txCtx, task.ID)
	require.NoError(t, f.sts.MarkUnsafe(txCtx, stRef(c)), "MarkUnsafe never joins the transaction on ctx")
	require.NoError(t, f.tm.Rollback(txCtx, txID))
	require.True(t, f.mustGet(task.ID).UnsafeMarked)
	require.True(t, f.reclaim(uuid.New(), task.ID).UnsafeMarked)
}

// A mark does not survive DeleteForEntities: the id, re-armed, starts
// unmarked at once, with no sweep required.
func testSTMarkDeleteThenRearmUnmarked(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID)
	require.NoError(t, f.sts.MarkUnsafe(f.ctx, stRef(c)))
	require.True(t, f.mustGet(task.ID).UnsafeMarked, "the mark was really written")

	require.NoError(t, f.sts.DeleteForEntities(f.ctx, f.tenant, []string{task.EntityID}))
	f.requireGone(task.ID)

	s := f.spec(task.EntityID, "S", "T", stDue)
	f.reconcile(f.ctx, task.EntityID, "S", s)
	got := f.mustGet(task.ID)
	require.False(t, got.UnsafeMarked, "a re-armed life after a delete starts unmarked")
	require.False(t, f.claimTask(uuid.New(), task.ID).UnsafeMarked)
}

// Marking one task leaves a sibling task's mark untouched.
func testSTMarkSiblingUnaffected(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	// Armed and claimed one at a time: two due tasks armed together could
	// both land in a single ClaimDue call, and f.claimTask requires that
	// call take exactly one task.
	e1 := f.newEntity()
	s1 := f.spec(e1, "S", "T", stDue)
	f.reconcile(f.ctx, e1, "S", s1)
	c1 := f.claimTask(uuid.New(), s1.ID)

	e2 := f.newEntity()
	s2 := f.spec(e2, "S", "T", stDue)
	f.reconcile(f.ctx, e2, "S", s2)
	c2 := f.claimTask(uuid.New(), s2.ID)

	require.NoError(t, f.sts.MarkUnsafe(f.ctx, stRef(c1)))

	require.True(t, f.mustGet(s1.ID).UnsafeMarked, "the mark was really written")
	require.False(t, f.mustGet(s2.ID).UnsafeMarked, "marking one task does not mark another")
	requireUnchangedClaim(t, c2, f.mustGet(s2.ID))
}

func testSTRecordCounted(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID)
	next := stNow + 60_000
	require.NoError(t, f.sts.RecordAttempt(f.ctx, stRef(c), spi.Attempt{
		Error: "CONFLICT: a concurrent write changed the entity or its task", AtMs: stNow + 5, NextAttemptTime: next}))

	got := f.mustGet(task.ID)
	require.Equal(t, spi.ScheduledTaskWaiting, got.Status)
	require.Nil(t, got.Claim)
	require.Equal(t, c.ArmToken, got.ArmToken)
	require.Equal(t, 1, got.Attempts)
	require.Zero(t, got.LostOwners)
	require.Equal(t, "CONFLICT: a concurrent write changed the entity or its task", got.LastError)
	require.NotNil(t, got.LastAttemptTime)
	require.Equal(t, stNow+5, *got.LastAttemptTime)
	require.Equal(t, next, got.NextAttemptTime)

	require.Empty(t, f.claimWith(f.claimReq(uuid.New(), false)), "not claimable before NextAttemptTime")
	req := f.claimReq(uuid.New(), false)
	req.NowMs = next
	c2 := f.claimOnly(req, task.ID)
	require.NoError(t, f.sts.RecordAttempt(f.ctx, stRef(c2), spi.Attempt{Error: "E2", AtMs: next, NextAttemptTime: next}))
	require.Equal(t, 2, f.mustGet(task.ID).Attempts)
}

func testSTRecordNotCounted(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID)
	require.NoError(t, f.sts.RecordAttempt(f.ctx, stRef(c), spi.Attempt{
		Error: "E0", AtMs: stNow - 10, NextAttemptTime: stNow}))
	require.Equal(t, 1, f.mustGet(task.ID).Attempts, "the counted attempt above was really counted")

	c = f.claimTask(uuid.New(), task.ID)
	require.NoError(t, f.sts.RecordAttempt(f.ctx, stRef(c), spi.Attempt{
		Error: "CANCELLED: the run was stopped by the scheduler", AtMs: stNow, NextAttemptTime: stNow, NotCounted: true}))
	got := f.mustGet(task.ID)
	require.Equal(t, spi.ScheduledTaskWaiting, got.Status)
	require.Equal(t, 1, got.Attempts, "an attempt marked NotCounted does not add to Attempts")
	require.Equal(t, "CANCELLED: the run was stopped by the scheduler", got.LastError,
		"an attempt that is not counted is still recorded")
	require.NotNil(t, got.LastAttemptTime)
	require.Equal(t, stNow, *got.LastAttemptTime)
	f.claimTask(uuid.New(), task.ID) // claimable at once
}

// RecordAttempt{ClearOwnMark} removes this claim's mark, and is accepted
// when the claim holds none (spec §13 row "database outage during
// MarkUnsafe → RecordAttempt{ClearOwnMark} after recovery, WAITING").
func testSTRecordClearOwnMark(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID)
	require.NoError(t, f.sts.MarkUnsafe(f.ctx, stRef(c)))
	require.NoError(t, f.sts.RecordAttempt(f.ctx, stRef(c), spi.Attempt{
		Error: "E", AtMs: stNow, NextAttemptTime: stNow, ClearOwnMark: true}))
	require.False(t, f.mustGet(task.ID).UnsafeMarked)

	next := f.claimTask(uuid.New(), task.ID)
	require.False(t, next.UnsafeMarked)
	require.NoError(t, f.sts.RecordAttempt(f.ctx, stRef(next), spi.Attempt{
		Error: "E", AtMs: stNow, NextAttemptTime: stNow, ClearOwnMark: true}),
		"ClearOwnMark with no mark held is accepted")
	require.Equal(t, spi.ScheduledTaskWaiting, f.mustGet(task.ID).Status)
}

// Without ClearOwnMark the mark stays; ClearOwnMark never removes a mark
// another claim wrote.
func testSTRecordOtherClaimsMarkKept(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	a := f.claimTask(uuid.New(), task.ID)
	require.NoError(t, f.sts.MarkUnsafe(f.ctx, stRef(a)))
	require.NoError(t, f.sts.RecordAttempt(f.ctx, stRef(a), spi.Attempt{Error: "E", AtMs: stNow, NextAttemptTime: stNow}))

	b := f.claimTask(uuid.New(), task.ID)
	require.True(t, b.UnsafeMarked)
	require.NoError(t, f.sts.RecordAttempt(f.ctx, stRef(b), spi.Attempt{
		Error: "E", AtMs: stNow, NextAttemptTime: stNow, ClearOwnMark: true}))

	c := f.claimTask(uuid.New(), task.ID)
	require.True(t, c.UnsafeMarked, "ClearOwnMark removes only the calling claim's mark")
	require.ErrorIs(t, f.sts.MarkUnsafe(f.ctx, stRef(c)), spi.ErrMarkedByAnotherClaim)
}

// A bookkeeping write repeated after it was accepted is refused and not
// applied twice (spec §13 row "a bookkeeping write retried through an
// outage; accepted after recovery": a retry whose first attempt landed
// learns it through the refusal).
func testSTRecordRepeatRefused(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID)
	a := spi.Attempt{Error: "E", AtMs: stNow, NextAttemptTime: stFuture}
	require.NoError(t, f.sts.RecordAttempt(f.ctx, stRef(c), a))
	require.ErrorIs(t, f.sts.RecordAttempt(f.ctx, stRef(c), a), spi.ErrStaleClaim)
	require.Equal(t, 1, f.mustGet(task.ID).Attempts)

	// The claim the accepted record released is refused by every fenced
	// method, not just a repeat of the same write.
	requireAllFencedRefused(t, f.ctx, f.sts, stRef(c), "a claim already recorded")
	require.Equal(t, 1, f.mustGet(task.ID).Attempts)
}

func testSTFailFields(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	for _, reason := range []spi.ScheduledTaskFailureReason{
		spi.FailureUnsafeWorkNotCompleted, spi.FailureOwnerLostRepeatedly,
		spi.FailureExpiredAfterFailedAttempts, spi.FailureRunPanicked, spi.FailureStoppedAfterPartialCommit,
	} {
		task := f.armDue()
		c := f.claimTask(uuid.New(), task.ID)
		fl := spi.Failure{Reason: reason, Error: "internal error [ticket: " + newID() + "]", AtMs: stNow + 7}
		require.NoError(t, f.sts.Fail(f.ctx, stRef(c), fl))

		got := f.mustGet(task.ID)
		require.Equal(t, spi.ScheduledTaskFailed, got.Status)
		require.Equal(t, reason, got.FailureReason)
		require.Equal(t, fl.Error, got.LastError)
		require.NotNil(t, got.FailedTime)
		require.Equal(t, stNow+7, *got.FailedTime)
		require.Nil(t, got.LastAttemptTime, "Fail leaves LastAttemptTime unchanged")
		require.Nil(t, got.Claim)
		require.Equal(t, c.ArmToken, got.ArmToken)
		requireAllFencedRefused(t, f.ctx, f.sts, stRef(c), "a claim of a FAILED task")
		after := f.mustGet(task.ID)
		require.Equal(t, got.LastError, after.LastError, "a refused write changes nothing")
		require.Equal(t, got.PartialCommit, after.PartialCommit, "a refused write changes nothing")
	}

	// After a recorded attempt and a lost-owner reclaim, Fail keeps
	// LastAttemptTime, Attempts and LostOwners.
	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID)
	require.NoError(t, f.sts.RecordAttempt(f.ctx, stRef(c), spi.Attempt{Error: "E1", AtMs: stNow - 3, NextAttemptTime: stNow}))
	c = f.claimTask(uuid.New(), task.ID) // this owner never heartbeats: lost at once
	c = f.reclaim(uuid.New(), task.ID)
	require.Equal(t, 1, c.LostOwners, "the reclaim above was really a lost-owner claim")
	require.NoError(t, f.sts.Fail(f.ctx, stRef(c), spi.Failure{Reason: spi.FailureRunPanicked, Error: "E2", AtMs: stNow + 7}))
	got := f.mustGet(task.ID)
	require.NotNil(t, got.LastAttemptTime)
	require.Equal(t, stNow-3, *got.LastAttemptTime, "Fail leaves LastAttemptTime unchanged")
	require.Equal(t, 1, got.Attempts, "Fail leaves Attempts unchanged")
	require.Equal(t, 1, got.LostOwners, "Fail leaves LostOwners unchanged")
}

// Fail replaces LastError, even with an empty text.
func testSTFailOverwritesLastError(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID)
	require.NoError(t, f.sts.RecordAttempt(f.ctx, stRef(c), spi.Attempt{Error: "E1", AtMs: stNow, NextAttemptTime: stNow}))
	require.Equal(t, "E1", f.mustGet(task.ID).LastError)

	c2 := f.claimTask(uuid.New(), task.ID)
	require.NoError(t, f.sts.Fail(f.ctx, stRef(c2), spi.Failure{
		Reason: spi.FailureExpiredAfterFailedAttempts, Error: "", AtMs: stNow}))
	got := f.mustGet(task.ID)
	require.Equal(t, spi.ScheduledTaskFailed, got.Status)
	require.Empty(t, got.LastError, "Fail replaces LastError even with an empty text")
}

func testSTFailJoinsTransaction(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID)
	fl := spi.Failure{Reason: spi.FailureRunPanicked, Error: "E", AtMs: stNow}

	txID, txCtx := f.begin()
	require.NoError(t, f.sts.Fail(txCtx, stRef(c), fl))
	staged, _ := f.get(txCtx, task.ID)
	require.Equal(t, spi.ScheduledTaskFailed, staged.Status, "the transaction sees its own Fail (C2)")
	require.NoError(t, f.tm.Rollback(txCtx, txID))
	requireUnchangedClaim(t, c, f.mustGet(task.ID))

	txID, txCtx = f.begin()
	require.NoError(t, f.sts.Fail(txCtx, stRef(c), fl))
	require.NoError(t, f.tm.Commit(txCtx, txID))
	require.Equal(t, spi.ScheduledTaskFailed, f.mustGet(task.ID).Status)
}

// An unknown or empty Failure.Reason is rejected on every backend and
// changes nothing (types.go, Failure.Reason doc;
// scheduled_task_helpers.go ValidateFailureReason).
func testSTFailUnknownReasonRejected(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID)
	for why, reason := range map[string]spi.ScheduledTaskFailureReason{
		"empty":        "",
		"not a reason": "NOT_A_REASON",
	} {
		err := f.sts.Fail(f.ctx, stRef(c), spi.Failure{Reason: reason, Error: "E", AtMs: stNow})
		require.ErrorIs(t, err, spi.ErrStoreRejected, "Fail with a %s reason", why)
		requireUnchangedClaim(t, c, f.mustGet(task.ID))
		got := f.mustGet(task.ID)
		require.Empty(t, got.LastError, "a refused Fail changes nothing, %s reason", why)
		require.Nil(t, got.FailedTime, "a refused Fail changes nothing, %s reason", why)
	}
}

// stMaxErrorText is 1024 bytes of valid UTF-8 with multi-byte characters
// and the U+FFFD a sanitiser puts in place of a NUL.
func stMaxErrorText() string {
	return "\uFFFD" + strings.Repeat("\u00e9", 509) + "\u20ac"
}

// The longest error text a caller may send is stored intact on every
// backend (spec §13 row "lastError over 1 024 bytes with multi-byte
// characters and a NUL → cut at a character boundary, stored on every
// backend": the engine cuts, the store keeps).
func testSTErrorTextRoundTrip(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	text := stMaxErrorText()
	require.Len(t, text, 1024)
	require.True(t, utf8.ValidString(text))

	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID)
	require.NoError(t, f.sts.RecordAttempt(f.ctx, stRef(c), spi.Attempt{Error: text, AtMs: stNow, NextAttemptTime: stNow}))
	require.Equal(t, text, f.mustGet(task.ID).LastError)

	c2 := f.claimTask(uuid.New(), task.ID)
	require.NoError(t, f.sts.Fail(f.ctx, stRef(c2), spi.Failure{Reason: spi.FailureRunPanicked, Error: text, AtMs: stNow}))
	require.Equal(t, text, f.mustGet(task.ID).LastError)
}

// Every store marks a deterministic rejection with ErrStoreRejected
// (spec §13 row "spi.ErrStoreRejected → ERROR with ticket, node latched
// (every backend sets the marker)"). Error text that breaks the documented
// precondition is the portable trigger.
func testSTErrorTextStoreRejected(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID)
	// A counted attempt first, so LastError, LastAttemptTime and Attempts
	// are non-zero: the "unchanged" assertions below are not vacuous.
	require.NoError(t, f.sts.RecordAttempt(f.ctx, stRef(c), spi.Attempt{Error: "E0", AtMs: stNow - 10, NextAttemptTime: stNow}))
	before := f.mustGet(task.ID)
	require.Equal(t, "E0", before.LastError)
	require.NotNil(t, before.LastAttemptTime)
	require.Equal(t, 1, before.Attempts)
	require.Nil(t, before.FailedTime)

	c = f.claimTask(uuid.New(), task.ID)
	for why, text := range map[string]string{
		"a NUL":                "a\x00b",
		"invalid UTF-8":        "a\xffb",
		"more than 1024 bytes": stMaxErrorText() + "x",
	} {
		// NextAttemptTime: stFuture, distinct from the task's current
		// NextAttemptTime (stNow, from the accepted attempt above): a store
		// that applied a refused write despite the error would otherwise
		// leave NextAttemptTime looking unchanged by coincidence.
		err := f.sts.RecordAttempt(f.ctx, stRef(c), spi.Attempt{Error: text, AtMs: stNow, NextAttemptTime: stFuture})
		require.ErrorIs(t, err, spi.ErrStoreRejected, "RecordAttempt with %s", why)
		err = f.sts.Fail(f.ctx, stRef(c), spi.Failure{Reason: spi.FailureRunPanicked, Error: text, AtMs: stNow})
		require.ErrorIs(t, err, spi.ErrStoreRejected, "Fail with %s", why)
		requireUnchangedClaim(t, c, f.mustGet(task.ID))

		got := f.mustGet(task.ID)
		require.Equal(t, before.LastError, got.LastError, "a refused write changes nothing, %s", why)
		require.NotNil(t, got.LastAttemptTime, "a refused write changes nothing, %s", why)
		require.Equal(t, *before.LastAttemptTime, *got.LastAttemptTime, "a refused write changes nothing, %s", why)
		require.Equal(t, before.Attempts, got.Attempts, "a refused write changes nothing, %s", why)
		require.Equal(t, before.NextAttemptTime, got.NextAttemptTime, "a refused write changes nothing, %s", why)
		require.Nil(t, got.FailedTime, "a refused write changes nothing, %s", why)
	}
	// A refused write is not a stale claim: the claim still holds.
	require.NoError(t, f.sts.RecordAttempt(f.ctx, stRef(c), spi.Attempt{Error: "E", AtMs: stNow, NextAttemptTime: stNow}))
}

// SweepMarks keeps the mark of a task's current life in every status and
// the next life starts unmarked (spec §13 row "dead owners and the marks
// of ended lives are swept"; the owner half is Liveness/Sweep*).
func testSTSweepMarksKeepsCurrentLife(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID)
	require.NoError(t, f.sts.MarkUnsafe(f.ctx, stRef(c)))

	require.NoError(t, f.sts.SweepMarks(context.Background()))
	require.True(t, f.mustGet(task.ID).UnsafeMarked, "RUNNING: the mark stays")

	require.NoError(t, f.sts.RecordAttempt(f.ctx, stRef(c), spi.Attempt{Error: "E", AtMs: stNow, NextAttemptTime: stNow}))
	require.NoError(t, f.sts.SweepMarks(context.Background()))
	require.True(t, f.mustGet(task.ID).UnsafeMarked, "WAITING: the mark stays")

	c2 := f.claimTask(uuid.New(), task.ID)
	require.NoError(t, f.sts.Fail(f.ctx, stRef(c2), spi.Failure{Reason: spi.FailureUnsafeWorkNotCompleted, Error: "E", AtMs: stNow}))
	require.NoError(t, f.sts.SweepMarks(context.Background()))
	require.True(t, f.mustGet(task.ID).UnsafeMarked, "FAILED: the mark stays")

	f.reconcile(f.ctx, task.EntityID, "S", f.spec(task.EntityID, "S", "T", stDue))
	require.NoError(t, f.sts.SweepMarks(context.Background()))
	require.False(t, f.mustGet(task.ID).UnsafeMarked, "a re-armed life has no mark")
	require.False(t, f.claimTask(uuid.New(), task.ID).UnsafeMarked)
}
