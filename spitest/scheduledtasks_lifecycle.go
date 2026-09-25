package spitest

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// requireFreshLife asserts got is a new life armed from s.
func requireFreshLife(t *testing.T, s, got spi.ScheduledTask) {
	t.Helper()
	require.Equal(t, spi.ScheduledTaskWaiting, got.Status)
	require.NotEqual(t, uuid.Nil, got.ArmToken, "the store draws an arm token on every arm")
	require.Equal(t, s.ScheduledTime, got.NextAttemptTime, "a new life is due at its scheduled time")
	require.Zero(t, got.Attempts)
	require.Zero(t, got.LostOwners)
	require.Nil(t, got.LastAttemptTime)
	require.Empty(t, got.LastError)
	require.Empty(t, got.FailureReason)
	require.Nil(t, got.FailedTime)
	require.False(t, got.PartialCommit)
	require.Nil(t, got.Claim)
	require.False(t, got.UnsafeMarked)
}

func testSTArmNewLife(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	e := f.newEntity()
	timeout := int64(5000)
	s := f.spec(e, "S", "T", stDue)
	s.TimeoutMs = &timeout
	s.ArmedBy = spi.Principal{ID: "svc-arm", Kind: spi.PrincipalService}

	// The life fields are the store's: whatever the caller puts in them is
	// ignored.
	callerToken := uuid.New()
	last := int64(5)
	caller := s
	caller.Status = spi.ScheduledTaskFailed
	caller.ArmToken = callerToken
	caller.NextAttemptTime = stFuture
	caller.Attempts = 7
	caller.LostOwners = 2
	caller.LastAttemptTime = &last
	caller.LastError = "caller"
	caller.FailureReason = spi.FailureRunPanicked
	caller.FailedTime = &last
	caller.PartialCommit = true
	caller.Claim = &spi.TaskClaim{Token: uuid.New(), Owner: uuid.New()}
	caller.UnsafeMarked = true

	require.Empty(t, f.reconcile(f.ctx, e, "S", caller))
	got := f.mustGet(s.ID)
	requireFreshLife(t, s, got)
	require.NotEqual(t, callerToken, got.ArmToken, "the arm token is drawn by the store, not taken from the caller")

	require.Equal(t, s.ID, got.ID)
	require.Equal(t, f.tenant, got.TenantID)
	require.Equal(t, spi.ScheduledTaskFireTransition, got.Type)
	require.Equal(t, s.ScheduledTime, got.ScheduledTime)
	require.NotNil(t, got.TimeoutMs)
	require.Equal(t, timeout, *got.TimeoutMs)
	require.Equal(t, e, got.EntityID)
	require.Equal(t, f.model, got.ModelName)
	require.Equal(t, 1, got.ModelVersion)
	require.Equal(t, "S", got.SourceState)
	require.Equal(t, "T", got.Transition)
	require.Equal(t, s.ArmedAt, got.ArmedAt)
	require.Equal(t, s.ArmedBy, got.ArmedBy)

	// A zero ArmedBy stays the zero Principal; a nil TimeoutMs stays nil.
	e2 := f.newEntity()
	s2 := f.spec(e2, "S", "T", stDue)
	f.reconcile(f.ctx, e2, "S", s2)
	got2 := f.mustGet(s2.ID)
	require.Equal(t, spi.Principal{}, got2.ArmedBy)
	require.Nil(t, got2.TimeoutMs)
}

// ReconcileForEntity takes the tenant and the entity from the request,
// never from an Arm item.
func testSTArmTenantAndEntityFromRequest(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	fb := newSTFixture(t, h)
	e := f.newEntity()
	other := f.arm(stFuture) // another entity of the same tenant
	s := f.spec(e, "S", "T", stFuture)
	s.TenantID = fb.tenant
	s.EntityID = other.EntityID
	require.Empty(t, f.reconcile(f.ctx, e, "S", s))

	got := f.mustGet(s.ID)
	require.Equal(t, f.tenant, got.TenantID)
	require.Equal(t, e, got.EntityID)
	_, found := fb.get(fb.ctx, s.ID)
	require.False(t, found, "the task is not armed in the tenant an Arm item names")
	f.mustGet(other.ID) // the entity an Arm item names keeps its task

	// The task is the request entity's: that entity's next write removes it.
	require.Equal(t, []string{s.ID}, stIDs(f.reconcile(f.ctx, e, "S")))
}

func testSTArmEveryArmIsNewLife(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	first := f.arm(stFuture)
	s := f.spec(first.EntityID, "S", "T", stFuture)
	require.Empty(t, f.reconcile(f.ctx, first.EntityID, "S", s), "re-arming an id replaces it; it is not removed")
	second := f.mustGet(s.ID)
	requireFreshLife(t, s, second)
	require.NotEqual(t, first.ArmToken, second.ArmToken, "every arm starts a new life, even with an identical payload")
}

func testSTArmRemovesOtherTasks(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	e := f.newEntity()
	other := f.newEntity()
	a := f.spec(e, "S", "T1", stFuture)
	b := f.spec(e, "S", "T2", stFuture)
	c := f.spec(e, "S0", "T0", stFuture)
	o := f.spec(other, "S", "T1", stFuture)

	require.Empty(t, f.reconcile(f.ctx, e, "S0", c))
	cArmed := f.mustGet(c.ID)
	f.reconcile(f.ctx, other, "S", o)

	// The entity moved S0 -> S: the S0 task is removed and reported.
	removed := f.reconcile(f.ctx, e, "S", a, b)
	require.Equal(t, []string{c.ID}, stIDs(removed))
	require.Equal(t, cArmed.ArmToken, removed[0].ArmToken)
	require.Equal(t, e, removed[0].EntityID)
	require.Equal(t, "S0", removed[0].SourceState)
	require.Equal(t, "T0", removed[0].Transition)
	f.requireGone(c.ID)

	// T2 is no longer scheduled in S: the next write removes it.
	removed = f.reconcile(f.ctx, e, "S", a)
	require.Equal(t, []string{b.ID}, stIDs(removed))
	f.requireGone(b.ID)
	f.mustGet(a.ID)
	f.mustGet(o.ID) // another entity's task is never touched
}

func testSTArmCancelNotReported(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	e := f.newEntity()
	x := f.spec(e, "S", "TX", stFuture)
	y := f.spec(e, "S", "TY", stFuture)
	f.reconcile(f.ctx, e, "S", x, y)

	removed, err := f.sts.ReconcileForEntity(f.ctx, spi.ReconcileRequest{
		TenantID: f.tenant, EntityID: e, CurrentState: "S",
		Cancel: []string{x.ID, "st-does-not-exist-" + newID()}})
	require.NoError(t, err, "a Cancel id that does not exist is a no-op")
	require.Equal(t, []string{y.ID}, stIDs(removed),
		"a task named in Cancel is removed but not reported; every other removed task is")
	f.requireGone(x.ID)
	f.requireGone(y.ID)
}

func testSTArmCancelOtherEntityUntouched(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	x := f.newEntity()
	y := f.newEntity()
	xTask := f.spec(x, "S", "T", stFuture)
	f.reconcile(f.ctx, x, "S", xTask)
	before := f.mustGet(xTask.ID)

	removed, err := f.sts.ReconcileForEntity(f.ctx, spi.ReconcileRequest{
		TenantID: f.tenant, EntityID: y, CurrentState: "S", Cancel: []string{xTask.ID}})
	require.NoError(t, err, "a Cancel id naming another entity's task is a no-op")
	require.Empty(t, removed, "x's task belongs to x, not y: Cancel does not touch it, and y had nothing else to remove")
	after := f.mustGet(xTask.ID)
	require.Equal(t, before.ArmToken, after.ArmToken, "x's task is unchanged")
	require.Equal(t, before.Status, after.Status)
}

func testSTArmJoinsTransaction(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	e := f.newEntity()
	s := f.spec(e, "S", "T", stFuture)

	txID, txCtx := f.begin()
	f.reconcile(txCtx, e, "S", s)
	require.NoError(t, f.tm.Rollback(txCtx, txID))
	f.requireGone(s.ID)

	txID, txCtx = f.begin()
	f.reconcile(txCtx, e, "S", s)
	require.NoError(t, f.tm.Commit(txCtx, txID))
	requireFreshLife(t, s, f.mustGet(s.ID))
}

// A self-loop fires and re-arms the same id as a new life (spec §13 row
// "self-loop fires and re-arms the same id as a new life").
func testSTArmSelfLoopRearmsRunning(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID)

	txID, txCtx := f.begin()
	s := f.spec(task.EntityID, "S", "T", stFuture)
	require.Empty(t, f.reconcile(txCtx, task.EntityID, "S", s))
	// The run's own RemoveLife comes after the re-arm in the same
	// transaction: it names a life this transaction already replaced.
	require.NoError(t, f.sts.RemoveLife(txCtx, f.tenant, task.ID, c.ArmToken))
	require.NoError(t, f.tm.Commit(txCtx, txID))

	got := f.mustGet(task.ID)
	requireFreshLife(t, s, got)
	require.NotEqual(t, c.ArmToken, got.ArmToken)
	requireAllFencedRefused(t, f.ctx, f.sts, stRef(c), "a claim of the life the self-loop replaced")
}

// Re-arming starts a clean life from FAILED, and from WAITING after a
// failed attempt with PartialCommit set (spec §13 rows "FAILED task
// re-armed by an update in the state" and "a re-arm resets PartialCommit").
func testSTArmRearmResetsLife(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	s := f.spec(task.EntityID, "S", "T", stDue)

	// FAILED, with a mark, PartialCommit and a lost-owner reclaim: the
	// LostOwners reset below is proven from a non-zero value, not vacuously.
	f.claimTask(uuid.New(), task.ID) // this owner never heartbeats: lost at once
	c := f.reclaim(uuid.New(), task.ID)
	require.Equal(t, 1, c.LostOwners)
	require.NoError(t, f.sts.MarkUnsafe(f.ctx, stRef(c)))
	require.NoError(t, f.sts.StampSegment(f.ctx, stRef(c), true))
	require.NoError(t, f.sts.Fail(f.ctx, stRef(c), spi.Failure{
		Reason: spi.FailureUnsafeWorkNotCompleted, Error: "UNSAFE", AtMs: stNow}))
	failed := f.mustGet(task.ID)
	require.Equal(t, spi.ScheduledTaskFailed, failed.Status)
	// The mark and the other fields were really written: the reset
	// assertions below would pass vacuously against a zero value otherwise.
	require.True(t, failed.UnsafeMarked, "the mark was really written")
	require.True(t, failed.PartialCommit, "PartialCommit was really set")
	require.Equal(t, "UNSAFE", failed.LastError)
	require.Equal(t, spi.FailureUnsafeWorkNotCompleted, failed.FailureReason)
	require.NotNil(t, failed.FailedTime)
	require.Equal(t, 1, failed.LostOwners)

	f.reconcile(f.ctx, task.EntityID, "S", s)
	got := f.mustGet(task.ID)
	requireFreshLife(t, s, got)
	next := f.claimTask(uuid.New(), task.ID)
	require.False(t, next.UnsafeMarked, "the new life has no mark")
	require.False(t, next.PartialCommit)

	// WAITING after a counted attempt, with PartialCommit set.
	require.NoError(t, f.sts.StampSegment(f.ctx, stRef(next), true))
	require.NoError(t, f.sts.RecordAttempt(f.ctx, stRef(next), spi.Attempt{
		Error: "E", AtMs: stNow, NextAttemptTime: stFuture}))
	waiting := f.mustGet(task.ID)
	require.Equal(t, 1, waiting.Attempts)
	require.True(t, waiting.PartialCommit)
	// LastAttemptTime and LastError were really set: the reset assertion
	// on requireFreshLife below would pass vacuously against nil/empty
	// otherwise.
	require.NotNil(t, waiting.LastAttemptTime)
	require.Equal(t, stNow, *waiting.LastAttemptTime)
	require.Equal(t, "E", waiting.LastError)

	f.reconcile(f.ctx, task.EntityID, "S", s)
	requireFreshLife(t, s, f.mustGet(task.ID))
}

// A rejected ReconcileForEntity changes nothing: the entity's existing task
// keeps its life (ReconcileRequest.Cancel, ValidateArm).
func testSTArmAndCancelRejected(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	e := f.newEntity()
	existing := f.spec(e, "S", "T0", stFuture)
	f.reconcile(f.ctx, e, "S", existing)
	before := f.mustGet(existing.ID)

	s := f.spec(e, "S", "T1", stFuture)
	_, err := f.sts.ReconcileForEntity(f.ctx, spi.ReconcileRequest{
		TenantID: f.tenant, EntityID: e, CurrentState: "S",
		Arm: []spi.ScheduledTask{s}, Cancel: []string{s.ID}})
	require.ErrorIs(t, err, spi.ErrStoreRejected, "an id in both Arm and Cancel is rejected")
	after := f.mustGet(existing.ID)
	require.Equal(t, before.ArmToken, after.ArmToken, "a rejected request leaves the entity's existing task unchanged")
	f.requireGone(s.ID)
}

func testSTArmEmptyIDRejected(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	e := f.newEntity()
	existing := f.spec(e, "S", "T0", stFuture)
	f.reconcile(f.ctx, e, "S", existing)
	before := f.mustGet(existing.ID)

	s := f.spec(e, "S", "T1", stFuture)
	s.ID = ""
	_, err := f.sts.ReconcileForEntity(f.ctx, spi.ReconcileRequest{
		TenantID: f.tenant, EntityID: e, CurrentState: "S", Arm: []spi.ScheduledTask{s}})
	require.ErrorIs(t, err, spi.ErrStoreRejected, "an Arm item without an id is rejected")
	after := f.mustGet(existing.ID)
	require.Equal(t, before.ArmToken, after.ArmToken, "a rejected request leaves the entity's existing task unchanged")
}

func testSTRemoveLifeCurrentLife(t *testing.T, h Harness) {
	f := newSTFixture(t, h)

	waiting := f.arm(stFuture)
	require.NoError(t, f.sts.RemoveLife(f.ctx, f.tenant, waiting.ID, waiting.ArmToken))
	f.requireGone(waiting.ID)

	running := f.armDue()
	c := f.claimTask(uuid.New(), running.ID)
	require.NoError(t, f.sts.RemoveLife(f.ctx, f.tenant, c.ID, c.ArmToken))
	f.requireGone(c.ID)

	failed := f.armDue()
	fc := f.claimTask(uuid.New(), failed.ID)
	require.NoError(t, f.sts.Fail(f.ctx, stRef(fc), spi.Failure{Reason: spi.FailureRunPanicked, Error: "E", AtMs: stNow}))
	require.NoError(t, f.sts.RemoveLife(f.ctx, f.tenant, fc.ID, fc.ArmToken))
	f.requireGone(fc.ID)
}

func testSTRemoveLifeOtherLifeIsNoOp(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.arm(stFuture)
	require.NoError(t, f.sts.RemoveLife(f.ctx, f.tenant, task.ID, uuid.New()))
	require.Equal(t, task.ArmToken, f.mustGet(task.ID).ArmToken, "a RemoveLife naming another life does nothing")
	require.NoError(t, f.sts.RemoveLife(f.ctx, f.tenant, "st-missing-"+newID(), uuid.New()),
		"a RemoveLife of a missing task does nothing")
}

func testSTRemoveLifeJoinsTransaction(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.arm(stFuture)
	txID, txCtx := f.begin()
	require.NoError(t, f.sts.RemoveLife(txCtx, f.tenant, task.ID, task.ArmToken))
	_, found := f.get(txCtx, task.ID)
	require.False(t, found, "the transaction sees its own removal (C2)")
	require.NoError(t, f.sts.RemoveLife(txCtx, f.tenant, task.ID, task.ArmToken),
		"RemoveLife does nothing once this transaction has already removed the task")
	f.mustGet(task.ID) // not yet outside it
	require.NoError(t, f.tm.Rollback(txCtx, txID))
	require.Equal(t, task.ArmToken, f.mustGet(task.ID).ArmToken)
}

func testSTDeleteForEntitiesRemovesListed(t *testing.T, h Harness) {
	f := newSTFixture(t, h)

	e1 := f.newEntity()
	w1 := f.spec(e1, "S", "T1", stFuture)
	w2 := f.spec(e1, "S", "T2", stFuture)
	f.reconcile(f.ctx, e1, "S", w1, w2)
	running := f.claimTask(uuid.New(), f.armDue().ID)
	failedTask := f.armDue()
	fc := f.claimTask(uuid.New(), failedTask.ID)
	require.NoError(t, f.sts.Fail(f.ctx, stRef(fc), spi.Failure{Reason: spi.FailureRunPanicked, Error: "E", AtMs: stNow}))
	kept := f.arm(stFuture)

	require.NoError(t, f.sts.DeleteForEntities(f.ctx, f.tenant,
		[]string{e1, running.EntityID, failedTask.EntityID, "st-unknown-" + newID()}))
	f.requireGone(w1.ID)
	f.requireGone(w2.ID)
	f.requireGone(running.ID)
	f.requireGone(failedTask.ID)
	f.mustGet(kept.ID)

	require.NoError(t, f.sts.DeleteForEntities(f.ctx, f.tenant, nil), "an empty list is a no-op")
	f.mustGet(kept.ID)
}

func testSTDeleteForEntitiesJoinsTransaction(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.arm(stFuture)
	txID, txCtx := f.begin()
	require.NoError(t, f.sts.DeleteForEntities(txCtx, f.tenant, []string{task.EntityID}))
	_, found := f.get(txCtx, task.ID)
	require.False(t, found)
	require.NoError(t, f.tm.Rollback(txCtx, txID))
	f.mustGet(task.ID)
}

func testSTDeleteForModelKeep(t *testing.T, h Harness) {
	f := newSTFixture(t, h)

	keepMe := f.spec(f.newEntity(), "S", "T1", stFuture)
	dropT2 := f.spec(f.newEntity(), "S", "T2", stFuture)
	dropS2 := f.spec(f.newEntity(), "S2", "T1", stFuture)
	v2 := f.spec(f.newEntity(), "S", "T2", stFuture)
	v2.ModelVersion = 2
	otherModel := f.spec(f.newEntity(), "S", "T2", stFuture)
	otherModel.ModelName = f.model + "-other"
	for _, s := range []spi.ScheduledTask{keepMe, dropT2, dropS2, v2, otherModel} {
		f.reconcile(f.ctx, s.EntityID, s.SourceState, s)
	}
	// A RUNNING task follows the same rule.
	runningDrop := f.spec(f.newEntity(), "S", "T2", stDue)
	f.reconcile(f.ctx, runningDrop.EntityID, "S", runningDrop)
	f.claimTask(uuid.New(), runningDrop.ID)

	require.NoError(t, f.sts.DeleteForModel(f.ctx, f.tenant, f.model, 1,
		func(sourceState, transition string) bool { return sourceState == "S" && transition == "T1" }))
	f.mustGet(keepMe.ID)
	f.requireGone(dropT2.ID)
	f.requireGone(dropS2.ID)
	f.requireGone(runningDrop.ID)
	f.mustGet(v2.ID)         // another version of the model
	f.mustGet(otherModel.ID) // another model
}

func testSTDeleteForModelNilKeep(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	a := f.arm(stFuture)
	b := f.arm(stFuture)

	txID, txCtx := f.begin()
	require.NoError(t, f.sts.DeleteForModel(txCtx, f.tenant, f.model, 1, nil))
	require.NoError(t, f.tm.Rollback(txCtx, txID))
	f.mustGet(a.ID) // joins: the rollback undid it

	require.NoError(t, f.sts.DeleteForModel(f.ctx, f.tenant, f.model, 1, nil))
	f.requireGone(a.ID)
	f.requireGone(b.ID)
}

func testSTDeleteForModelTenantScoped(t *testing.T, h Harness) {
	fa := newSTFixture(t, h)
	fb := newSTFixture(t, h)
	fb.model = fa.model // the same model name in both tenants
	a := fa.arm(stFuture)
	b := fb.arm(stFuture)

	require.NoError(t, fb.sts.DeleteForModel(fb.ctx, fb.tenant, fa.model, 1, nil))
	fb.requireGone(b.ID)
	fa.mustGet(a.ID)
}

func testSTGetMissing(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	_, found := f.get(f.ctx, "st-missing-"+newID())
	require.False(t, found)
}

// Get is tenant-filtered like every read: a transaction on ctx from another
// tenant never refuses the call and never changes what it answers (the
// ScheduledTaskStore doc comment, "Get carries no such refusal").
func testSTGetOtherTenantTransaction(t *testing.T, h Harness) {
	fa := newSTFixture(t, h)
	fb := newSTFixture(t, h)
	bTask := fb.arm(stFuture)

	_, txCtxA := fa.begin()
	aTask := fa.spec(fa.newEntity(), "S", "T", stFuture)
	fa.reconcile(txCtxA, aTask.EntityID, "S", aTask) // staged in A's open transaction, uncommitted

	// The staged task is visible in A's own transaction, under A's tenant —
	// proves the setup below is not vacuous.
	_, foundOwn := fa.get(txCtxA, aTask.ID)
	require.True(t, foundOwn, "the transaction sees its own staged arm (C2)")

	// B's committed task, read through A's transaction context but B's
	// tenant: found, and Get does not refuse the cross-tenant transaction.
	gotB, foundB, err := fa.sts.Get(txCtxA, fb.tenant, bTask.ID)
	require.NoError(t, err)
	require.True(t, foundB)
	require.NotNil(t, gotB)
	require.Equal(t, bTask.ID, gotB.ID)

	// The same id, read under tenant A instead of B: not found. Get answers
	// from the tenant argument, not from whose transaction ctx carries.
	_, foundWrongTenant, err := fa.sts.Get(txCtxA, fa.tenant, bTask.ID)
	require.NoError(t, err)
	require.False(t, foundWrongTenant)

	// A's own staged task, read under tenant B through A's transaction: not
	// visible — tenant filtering applies to the transaction's own writes too.
	_, foundStaged, err := fa.sts.Get(txCtxA, fb.tenant, aTask.ID)
	require.NoError(t, err)
	require.False(t, foundStaged)
}
