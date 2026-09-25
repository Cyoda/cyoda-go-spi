package spitest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// Clock values the suite passes as ClaimRequest.NowMs and arms tasks at.
// NextAttemptTime is compared with the caller's NowMs, never with the store
// clock, so these are plain numbers.
const (
	stDue    int64 = 1_000_000         // a task armed at stDue is due at stNow
	stNow    int64 = 2_000_000         // NowMs of every suite claim
	stFuture int64 = 9_000_000_000_000 // a task armed at stFuture is never due
)

const (
	// stShortStale is the StaleAfter a test uses when a heartbeat must go
	// stale: AdvanceClock(3 * stShortStale) passes it. 60 ms stays under
	// the 100 ms ceiling the postgres harness puts on AdvanceClock.
	stShortStale = 20 * time.Millisecond
	// stLongStale is the StaleAfter a test uses when a heartbeat must stay
	// fresh. An owner with no liveness record is stale under any
	// StaleAfter, so a lost-owner claim with stLongStale takes exactly the
	// tasks whose owner never heartbeated.
	stLongStale = time.Hour
	// stWait bounds a call that must not wait on another transaction.
	stWait = 10 * time.Second
	// stBigLimit is the Limit and PerTenantLimit of a claim that is not
	// about limits.
	stBigLimit = 1000
)

func runScheduledTasksSuite(t *testing.T, h Harness, tracker *skipTracker) {
	_, err := h.Factory.ScheduledTaskStore(context.Background())
	if errors.Is(err, errors.ErrUnsupported) {
		t.Skipf("backend has no ScheduledTaskStore: %v", err)
	}
	require.NoError(t, err)

	// Arm, remove, delete, get (S-4).
	runSubtest(t, h, tracker, "Arm/NewLife", testSTArmNewLife)
	runSubtest(t, h, tracker, "Arm/TenantAndEntityFromRequest", testSTArmTenantAndEntityFromRequest)
	runSubtest(t, h, tracker, "Arm/EveryArmIsNewLife", testSTArmEveryArmIsNewLife)
	runSubtest(t, h, tracker, "Arm/RemovesOtherTasks", testSTArmRemovesOtherTasks)
	runSubtest(t, h, tracker, "Arm/CancelNotReported", testSTArmCancelNotReported)
	runSubtest(t, h, tracker, "Arm/JoinsTransaction", testSTArmJoinsTransaction)
	runSubtest(t, h, tracker, "Arm/SelfLoopRearmsRunning", testSTArmSelfLoopRearmsRunning)
	runSubtest(t, h, tracker, "Arm/RearmResetsLife", testSTArmRearmResetsLife)
	runSubtest(t, h, tracker, "Arm/ArmAndCancelRejected", testSTArmAndCancelRejected)
	runSubtest(t, h, tracker, "Arm/EmptyIDRejected", testSTArmEmptyIDRejected)
	runSubtest(t, h, tracker, "RemoveLife/CurrentLife", testSTRemoveLifeCurrentLife)
	runSubtest(t, h, tracker, "RemoveLife/OtherLifeIsNoOp", testSTRemoveLifeOtherLifeIsNoOp)
	runSubtest(t, h, tracker, "RemoveLife/JoinsTransaction", testSTRemoveLifeJoinsTransaction)
	runSubtest(t, h, tracker, "DeleteForEntities/RemovesListed", testSTDeleteForEntitiesRemovesListed)
	runSubtest(t, h, tracker, "DeleteForEntities/JoinsTransaction", testSTDeleteForEntitiesJoinsTransaction)
	runSubtest(t, h, tracker, "DeleteForModel/Keep", testSTDeleteForModelKeep)
	runSubtest(t, h, tracker, "DeleteForModel/NilKeepRemovesAll", testSTDeleteForModelNilKeep)
	runSubtest(t, h, tracker, "DeleteForModel/TenantScoped", testSTDeleteForModelTenantScoped)
	runSubtest(t, h, tracker, "Get/Missing", testSTGetMissing)
	runSubtest(t, h, tracker, "Get/OtherTenantTransaction", testSTGetOtherTenantTransaction)

	// Claims, liveness, give-back (S-5).
	runSubtest(t, h, tracker, "Claim/DueWaiting", testSTClaimDueWaiting)
	runSubtest(t, h, tracker, "Claim/NewTokenPerClaim", testSTClaimNewTokenPerClaim)
	runSubtest(t, h, tracker, "Claim/FailedNeverClaimed", testSTClaimFailedNeverClaimed)
	runSubtest(t, h, tracker, "Claim/RunningNeedsAllowLostOwner", testSTClaimRunningNeedsAllowLostOwner)
	runSubtest(t, h, tracker, "Claim/FreshOwnerKept", testSTClaimFreshOwnerKept)
	runSubtest(t, h, tracker, "Claim/StaleOwnerReclaimed", testSTClaimStaleOwnerReclaimed)
	runSubtest(t, h, tracker, "Claim/LostOwnerFlagged", testSTClaimLostOwnerFlagged)
	runSubtest(t, h, tracker, "Claim/LostOwnersCounted", testSTClaimLostOwnersCounted)
	runSubtest(t, h, tracker, "Claim/OnePerEntity", testSTClaimOnePerEntity)
	runSubtest(t, h, tracker, "Claim/LimitAndOrder", testSTClaimLimitAndOrder)
	runSubtest(t, h, tracker, "Claim/InvalidLimits", testSTClaimInvalidLimits)
	runSubtest(t, h, tracker, "Claim/TenantsTakeTurns", testSTClaimTenantsTakeTurns)
	runSubtest(t, h, tracker, "Claim/PerTenantLimit", testSTClaimPerTenantLimit)
	runSubtest(t, h, tracker, "Claim/ConcurrentDisjoint", testSTClaimConcurrentDisjoint)
	runSubtest(t, h, tracker, "Claim/SiblingsConcurrent", testSTClaimSiblingsConcurrent)
	runSubtest(t, h, tracker, "Claim/ContendedNoReclaim", testSTClaimContendedNoReclaim)
	runSubtest(t, h, tracker, "Liveness/SweepRemovesUnreferenced", testSTLivenessSweepRemovesUnreferenced)
	runSubtest(t, h, tracker, "Liveness/SweepKeepsReferenced", testSTLivenessSweepKeepsReferenced)
	runSubtest(t, h, tracker, "Liveness/HeartbeatRecreatesSwept", testSTLivenessHeartbeatRecreatesSwept)
	runSubtest(t, h, tracker, "Liveness/RetireOwner", testSTLivenessRetireOwner)
	runSubtest(t, h, tracker, "GiveBack/LostReply", testSTGiveBackLostReply)
	runSubtest(t, h, tracker, "GiveBack/KeepsLiveRuns", testSTGiveBackKeepsLiveRuns)
	runSubtest(t, h, tracker, "GiveBack/NotCountedKeepsMark", testSTGiveBackNotCountedKeepsMark)

	// Claims, liveness, give-back — fix round 1 (coverage gaps).
	runSubtest(t, h, tracker, "Liveness/HeartbeatRefreshes", testSTLivenessHeartbeatRefreshes)
	runSubtest(t, h, tracker, "Claim/LostOwnerIgnoresNextAttemptTime", testSTClaimLostOwnerIgnoresNextAttemptTime)
	runSubtest(t, h, tracker, "Claim/LostOwnerFlagPerTask", testSTClaimLostOwnerFlagPerTask)
	runSubtest(t, h, tracker, "Claim/OrderEarliestTenantFirst", testSTClaimOrderEarliestTenantFirst)
	runSubtest(t, h, tracker, "Claim/OrderTenantTieBrokenByID", testSTClaimOrderTenantTieBrokenByID)
	runSubtest(t, h, tracker, "Claim/OrderEntityTieBrokenByID", testSTClaimOrderEntityTieBrokenByID)
	runSubtest(t, h, tracker, "Claim/PerTenantLimitPartialQuota", testSTClaimPerTenantLimitPartialQuota)
	runSubtest(t, h, tracker, "Claim/EntityKeyIsPerTenant", testSTClaimEntityKeyIsPerTenant)
	runSubtest(t, h, tracker, "GiveBack/KeepsCounters", testSTGiveBackKeepsCounters)

	// Fenced writes, marks, recorded outcomes, error text (S-6).
	runSubtest(t, h, tracker, "Fence/StaleTokensRefused", testSTFenceStaleTokensRefused)
	runSubtest(t, h, tracker, "Fence/WaitingRefused", testSTFenceWaitingRefused)
	runSubtest(t, h, tracker, "Fence/OldLifeRefused", testSTFenceOldLifeRefused)
	runSubtest(t, h, tracker, "Fence/ABA", testSTFenceABA)
	runSubtest(t, h, tracker, "Fence/ReplacedOwnerStampRefused", testSTFenceReplacedOwnerStampRefused)
	runSubtest(t, h, tracker, "Fence/GiveBackRefused", testSTFenceGiveBackRefused)
	runSubtest(t, h, tracker, "Fence/RemoveLifeThenRearmRefused", testSTFenceRemoveLifeThenRearmRefused)
	runSubtest(t, h, tracker, "Stamp/PartialCommit", testSTStampPartialCommit)
	runSubtest(t, h, tracker, "Mark/AcceptedAndIdempotent", testSTMarkAcceptedAndIdempotent)
	runSubtest(t, h, tracker, "Mark/MarkedByAnotherClaim", testSTMarkMarkedByAnotherClaim)
	runSubtest(t, h, tracker, "Mark/SurvivesRollback", testSTMarkSurvivesRollback)
	runSubtest(t, h, tracker, "Mark/DeleteThenRearmUnmarked", testSTMarkDeleteThenRearmUnmarked)
	runSubtest(t, h, tracker, "Mark/SiblingUnaffected", testSTMarkSiblingUnaffected)
	runSubtest(t, h, tracker, "Record/Counted", testSTRecordCounted)
	runSubtest(t, h, tracker, "Record/NotCounted", testSTRecordNotCounted)
	runSubtest(t, h, tracker, "Record/ClearOwnMark", testSTRecordClearOwnMark)
	runSubtest(t, h, tracker, "Record/OtherClaimsMarkKept", testSTRecordOtherClaimsMarkKept)
	runSubtest(t, h, tracker, "Record/RepeatRefused", testSTRecordRepeatRefused)
	runSubtest(t, h, tracker, "Fail/Fields", testSTFailFields)
	runSubtest(t, h, tracker, "Fail/OverwritesLastError", testSTFailOverwritesLastError)
	runSubtest(t, h, tracker, "Fail/JoinsTransaction", testSTFailJoinsTransaction)
	runSubtest(t, h, tracker, "Fail/UnknownReasonRejected", testSTFailUnknownReasonRejected)
	runSubtest(t, h, tracker, "ErrorText/RoundTrip", testSTErrorTextRoundTrip)
	runSubtest(t, h, tracker, "ErrorText/StoreRejected", testSTErrorTextStoreRejected)
	runSubtest(t, h, tracker, "SweepMarks/KeepsCurrentLife", testSTSweepMarksKeepsCurrentLife)

	// Clauses C1, C2, C3, C6 (S-7).
	runSubtest(t, h, tracker, "C1/ReclaimFailsOldCommit", testSTC1ReclaimFailsOldCommit)
	runSubtest(t, h, tracker, "C1/RearmFailsOldCommit", testSTC1RearmFailsOldCommit)
	runSubtest(t, h, tracker, "C1/ClientWriteAfterClaim", testSTC1ClientWriteAfterClaim)
	runSubtest(t, h, tracker, "C1/OwnClaimNoConflict", testSTC1OwnClaimNoConflict)
	runSubtest(t, h, tracker, "C1/CommittedTxFailsOldCommit", testSTC1CommittedTxFailsOldCommit)
	runSubtest(t, h, tracker, "C2/StagedWritesVisible", testSTC2StagedWritesVisible)
	runSubtest(t, h, tracker, "C2/CallbackRearmThenRemoveLife", testSTC2CallbackRearmThenRemoveLife)
	runSubtest(t, h, tracker, "C2/CallbackRearmThenStamp", testSTC2CallbackRearmThenStamp)
	runSubtest(t, h, tracker, "C2/CallbackDeleteThenRemoveLife", testSTC2CallbackDeleteThenRemoveLife)
	runSubtest(t, h, tracker, "C3/MarkRacesClaim", testSTC3MarkRacesClaim)
	runSubtest(t, h, tracker, "C6/OpenWriteNotClaimable", testSTC6OpenWriteNotClaimable)
	runSubtest(t, h, tracker, "C6/MarkBusy", testSTC6MarkBusy)
	runSubtest(t, h, tracker, "C6/NeverJoiningWriteBounded", testSTC6NeverJoiningWriteBounded)

	// Query and tenant isolation (S-8).
	runSubtest(t, h, tracker, "Query/PagesInOrder", testSTQueryPagesInOrder)
	runSubtest(t, h, tracker, "Query/FilterPagesAcrossNonMatchingRows", testSTQueryFilterPagesAcrossNonMatchingRows)
	runSubtest(t, h, tracker, "Query/Filters", testSTQueryFilters)
	runSubtest(t, h, tracker, "Query/TenantIsolation", testSTQueryTenantIsolation)
	runSubtest(t, h, tracker, "TenantIsolation/EveryMethod", testSTTenantIsolationEveryMethod)
	runSubtest(t, h, tracker, "Tenant/JoiningWriteOtherTenantRefused", testSTTenantJoiningWriteOtherTenantRefused)
}

// stFixture is one subtest's tenant, store, transaction manager and model.
//
// ClaimDue, GiveBackIdle and the sweepers are cross-tenant, so a fresh
// tenant alone does not isolate a subtest. The fixture removes every task
// it armed when the subtest ends, so a cross-tenant claim sees only the
// running subtest's tasks. Tests still read their own tenant's results
// only (own), so a task another subtest failed to remove cannot fail them.
type stFixture struct {
	t        *testing.T
	h        Harness
	tenant   spi.TenantID
	ctx      context.Context // tenant context, no transaction
	sts      spi.ScheduledTaskStore
	tm       spi.TransactionManager
	model    string
	entities []string
}

func newSTFixture(t *testing.T, h Harness) *stFixture {
	t.Helper()
	f := &stFixture{t: t, h: h, tenant: h.NewTenant(), model: "st-" + uuid.NewString()}
	f.ctx = tenantContext(f.tenant)
	var err error
	f.sts, err = h.Factory.ScheduledTaskStore(f.ctx)
	require.NoError(t, err)
	f.tm, err = h.Factory.TransactionManager(f.ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		if len(f.entities) == 0 {
			return
		}
		if err := f.sts.DeleteForEntities(f.ctx, f.tenant, f.entities); err != nil {
			t.Errorf("cleanup: DeleteForEntities: %v", err)
		}
	})
	return f
}

// begin opens a transaction in the fixture's tenant. It is rolled back when
// the subtest ends unless the test ended it: an open transaction would hold
// task-row locks on PostgreSQL and block the fixture's cleanup. Cleanups
// run last-in first-out, so this rollback runs before the fixture's.
func (f *stFixture) begin() (string, context.Context) {
	f.t.Helper()
	txID, txCtx, err := f.tm.Begin(f.ctx)
	require.NoError(f.t, err)
	f.t.Cleanup(func() { _ = f.tm.Rollback(txCtx, txID) })
	return txID, txCtx
}

func (f *stFixture) newEntity() string {
	e := newID()
	f.entities = append(f.entities, e)
	return e
}

// taskID is a stable id for (entity, state, transition). Real ids are
// engine-defined hashes; stores treat them as opaque.
func (f *stFixture) taskID(entity, state, transition string) string {
	return fmt.Sprintf("st:%s:%s:%s:%s", f.tenant, entity, state, transition)
}

// spec is an arm request for one transition of entity out of state.
func (f *stFixture) spec(entity, state, transition string, scheduledTime int64) spi.ScheduledTask {
	return spi.ScheduledTask{
		ID: f.taskID(entity, state, transition), TenantID: f.tenant, Type: spi.ScheduledTaskFireTransition,
		ScheduledTime: scheduledTime, EntityID: entity, ModelName: f.model, ModelVersion: 1,
		Transition: transition, SourceState: state, ArmedAt: scheduledTime - 1,
	}
}

func (f *stFixture) reconcile(ctx context.Context, entity, state string, arm ...spi.ScheduledTask) []spi.ScheduledTask {
	f.t.Helper()
	removed, err := f.sts.ReconcileForEntity(ctx, spi.ReconcileRequest{
		TenantID: f.tenant, EntityID: entity, CurrentState: state, Arm: arm})
	require.NoError(f.t, err)
	return removed
}

// arm arms one task on a new entity and returns the stored record.
func (f *stFixture) arm(scheduledTime int64) spi.ScheduledTask {
	f.t.Helper()
	e := f.newEntity()
	s := f.spec(e, "S", "T", scheduledTime)
	f.reconcile(f.ctx, e, "S", s)
	return f.mustGet(s.ID)
}

func (f *stFixture) armDue() spi.ScheduledTask { return f.arm(stDue) }

func (f *stFixture) get(ctx context.Context, id string) (spi.ScheduledTask, bool) {
	f.t.Helper()
	got, found, err := f.sts.Get(ctx, f.tenant, id)
	require.NoError(f.t, err)
	if !found {
		require.Nil(f.t, got, "Get must return a nil task when found is false")
		return spi.ScheduledTask{}, false
	}
	require.NotNil(f.t, got)
	return *got, true
}

func (f *stFixture) mustGet(id string) spi.ScheduledTask {
	f.t.Helper()
	got, found := f.get(f.ctx, id)
	require.True(f.t, found, "task %s must exist", id)
	return got
}

func (f *stFixture) requireGone(id string) {
	f.t.Helper()
	_, found := f.get(f.ctx, id)
	require.False(f.t, found, "task %s must be gone", id)
}

func (f *stFixture) claimReq(owner uuid.UUID, lost bool) spi.ClaimRequest {
	return spi.ClaimRequest{Owner: owner, NowMs: stNow, StaleAfter: stLongStale,
		Limit: stBigLimit, PerTenantLimit: stBigLimit, AllowLostOwner: lost}
}

// claimWith runs ClaimDue with a tenant-less context and returns this
// fixture's tasks only. Bounded by stWait, so a store that serialises
// ClaimDue behind a row an open transaction holds fails the test cleanly
// instead of hanging it.
func (f *stFixture) claimWith(req spi.ClaimRequest) []spi.ScheduledTask {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), stWait)
	defer cancel()
	res, err := f.sts.ClaimDue(ctx, req)
	require.NoError(f.t, err)
	return f.own(res)
}

func (f *stFixture) own(tasks []spi.ScheduledTask) []spi.ScheduledTask {
	var out []spi.ScheduledTask
	for _, x := range tasks {
		if x.TenantID == f.tenant {
			out = append(out, x)
		}
	}
	return out
}

// claimTask claims the due WAITING task id as owner. It requires that the
// claim takes exactly that task of this tenant: a test that leaves another
// task claimable has a set-up error.
func (f *stFixture) claimTask(owner uuid.UUID, id string) spi.ScheduledTask {
	f.t.Helper()
	return f.claimOnly(f.claimReq(owner, false), id)
}

// reclaim takes the RUNNING task id as owner through a lost-owner claim.
// With stLongStale it succeeds only when the old owner has no liveness
// record — the suite's owners never heartbeat unless a test says so.
func (f *stFixture) reclaim(owner uuid.UUID, id string) spi.ScheduledTask {
	f.t.Helper()
	return f.claimOnly(f.claimReq(owner, true), id)
}

func (f *stFixture) claimOnly(req spi.ClaimRequest, id string) spi.ScheduledTask {
	f.t.Helper()
	got := f.claimWith(req)
	require.Len(f.t, got, 1, "the claim must take exactly task %s of this tenant", id)
	c := got[0]
	require.Equal(f.t, id, c.ID)
	require.Equal(f.t, spi.ScheduledTaskRunning, c.Status)
	require.NotNil(f.t, c.Claim, "a claimed task carries its claim")
	require.Equal(f.t, req.Owner, c.Claim.Owner)
	require.NotEqual(f.t, uuid.Nil, c.Claim.Token)
	return c
}

func findST(tasks []spi.ScheduledTask, id string) *spi.ScheduledTask {
	for i := range tasks {
		if tasks[i].ID == id {
			return &tasks[i]
		}
	}
	return nil
}

func stIDs(tasks []spi.ScheduledTask) []string {
	out := make([]string, 0, len(tasks))
	for _, x := range tasks {
		out = append(out, x.ID)
	}
	return out
}

// stRef is the TaskRef of a claimed task.
func stRef(c spi.ScheduledTask) spi.TaskRef {
	return spi.TaskRef{TenantID: c.TenantID, ID: c.ID, ArmToken: c.ArmToken, ClaimToken: c.Claim.Token}
}

// stFencedWrites calls each fenced method once with r.
var stFencedWrites = []struct {
	name string
	call func(ctx context.Context, sts spi.ScheduledTaskStore, r spi.TaskRef) error
}{
	{"StampSegment", func(ctx context.Context, sts spi.ScheduledTaskStore, r spi.TaskRef) error {
		return sts.StampSegment(ctx, r, true)
	}},
	{"MarkUnsafe", func(ctx context.Context, sts spi.ScheduledTaskStore, r spi.TaskRef) error {
		return sts.MarkUnsafe(ctx, r)
	}},
	{"RecordAttempt", func(ctx context.Context, sts spi.ScheduledTaskStore, r spi.TaskRef) error {
		return sts.RecordAttempt(ctx, r, spi.Attempt{Error: "E", AtMs: stNow, NextAttemptTime: stNow})
	}},
	{"Fail", func(ctx context.Context, sts spi.ScheduledTaskStore, r spi.TaskRef) error {
		return sts.Fail(ctx, r, spi.Failure{Reason: spi.FailureRunPanicked, Error: "E", AtMs: stNow})
	}},
}

func requireAllFencedRefused(t *testing.T, ctx context.Context, sts spi.ScheduledTaskStore, r spi.TaskRef, why string) {
	t.Helper()
	for _, m := range stFencedWrites {
		require.ErrorIs(t, m.call(ctx, sts, r), spi.ErrStaleClaim, "%s must refuse %s", m.name, why)
	}
}

// requireUnchangedClaim asserts a RUNNING task is exactly as its claim
// left it.
func requireUnchangedClaim(t *testing.T, want, got spi.ScheduledTask) {
	t.Helper()
	require.Equal(t, spi.ScheduledTaskRunning, got.Status)
	require.Equal(t, want.ArmToken, got.ArmToken)
	require.NotNil(t, got.Claim)
	require.Equal(t, want.Claim.Token, got.Claim.Token)
	require.Equal(t, want.Claim.Owner, got.Claim.Owner)
	require.Equal(t, want.Attempts, got.Attempts)
	require.Equal(t, want.LostOwners, got.LostOwners)
	require.Equal(t, want.PartialCommit, got.PartialCommit)
	require.Equal(t, want.UnsafeMarked, got.UnsafeMarked)
	require.Empty(t, got.FailureReason)
}
