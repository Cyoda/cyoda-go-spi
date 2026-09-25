package spitest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// How these tests hold "an open transaction": f.begin() opens one through
// the harness's TransactionManager, and every joining call made with its
// txCtx is staged in it until Commit or Rollback. On PostgreSQL a
// REPEATABLE READ snapshot is taken at the transaction's first statement,
// not at Begin, so a test that needs the transaction to have begun before
// another write first reads through it (f.get(txCtx, …)). The other write
// is made with f.ctx or a background context, which carry no transaction.

// stTaskWrite is a joining write a run's transaction makes to its own task
// row.
type stTaskWrite struct {
	name  string
	write func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error
}

var stRunWrites = []stTaskWrite{
	{"StampSegment", func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error {
		return f.sts.StampSegment(ctx, stRef(c), false)
	}},
	{"RemoveLife", func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error {
		return f.sts.RemoveLife(ctx, f.tenant, c.ID, c.ArmToken)
	}},
}

// A reclaimed task makes the old run's commit fail (C1), and the
// non-joining re-read then shows the run was superseded (spec §13 rows "a
// reclaimed or re-armed task makes the old run's commit fail (C1)" and "a
// refusal from inside the run's own transaction goes through §5.6").
func testSTC1ReclaimFailsOldCommit(t *testing.T, h Harness) {
	for _, w := range stRunWrites {
		t.Run(w.name, func(t *testing.T) {
			f := newSTFixture(t, h)
			a := f.claimTask(uuid.New(), f.armDue().ID)

			txID, txCtx := f.begin()
			_, _ = f.get(txCtx, a.ID)
			b := f.reclaim(uuid.New(), a.ID) // commits a change to the row after the run's transaction began

			stmtErr := w.write(txCtx, f, a)
			requireTxRefused(t, stmtErr, func() error { return f.tm.Commit(txCtx, txID) }, true)
			_ = f.tm.Rollback(txCtx, txID)

			got := f.mustGet(a.ID) // the classifying re-read joins no transaction
			require.Equal(t, a.ArmToken, got.ArmToken, "same life")
			require.NotEqual(t, a.Claim.Token, got.Claim.Token, "another claim: the old run was superseded")
			requireUnchangedClaim(t, b, got)
		})
	}
}

func testSTC1RearmFailsOldCommit(t *testing.T, h Harness) {
	for _, w := range stRunWrites {
		t.Run(w.name, func(t *testing.T) {
			f := newSTFixture(t, h)
			a := f.claimTask(uuid.New(), f.armDue().ID)

			txID, txCtx := f.begin()
			_, _ = f.get(txCtx, a.ID)
			s := f.spec(a.EntityID, "S", "T", stFuture)
			f.reconcile(f.ctx, a.EntityID, "S", s) // a client write re-arms the task

			stmtErr := w.write(txCtx, f, a)
			requireTxRefused(t, stmtErr, func() error { return f.tm.Commit(txCtx, txID) }, true)
			_ = f.tm.Rollback(txCtx, txID)

			got := f.mustGet(a.ID)
			requireFreshLife(t, s, got)
			require.NotEqual(t, a.ArmToken, got.ArmToken)
		})
	}
}

// An update, a delete or an import racing a claim gets a C1 conflict; the
// server's retry in a new transaction succeeds; the same on every backend
// (spec §13 row "an update racing a claim → retryable 409; a delete or
// import racing one claim succeeds after the server retry").
func testSTC1ClientWriteAfterClaim(t *testing.T, h Harness) {
	writes := []stTaskWrite{
		{"ReconcileForEntity", func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error {
			_, err := f.sts.ReconcileForEntity(ctx, spi.ReconcileRequest{TenantID: f.tenant, EntityID: c.EntityID,
				CurrentState: "S", Arm: []spi.ScheduledTask{f.spec(c.EntityID, "S", "T", stFuture)}})
			return err
		}},
		{"DeleteForEntities", func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error {
			return f.sts.DeleteForEntities(ctx, f.tenant, []string{c.EntityID})
		}},
		{"DeleteForModel", func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error {
			return f.sts.DeleteForModel(ctx, f.tenant, f.model, 1, nil)
		}},
	}
	for _, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			f := newSTFixture(t, h)
			task := f.armDue()

			txID, txCtx := f.begin()
			_, _ = f.get(txCtx, task.ID)
			c := f.claimTask(uuid.New(), task.ID) // the scheduler changes the row after the client's transaction began

			stmtErr := w.write(txCtx, f, task)
			requireTxRefused(t, stmtErr, func() error { return f.tm.Commit(txCtx, txID) }, false)
			_ = f.tm.Rollback(txCtx, txID)
			requireUnchangedClaim(t, c, f.mustGet(task.ID))

			txID, txCtx = f.begin()
			require.NoError(t, w.write(txCtx, f, task), "the retry in a new transaction is accepted")
			require.NoError(t, f.tm.Commit(txCtx, txID))
		})
	}
}

// A run never conflicts with its own claim, even when the store clock does
// not move between the claim and the run's transactions (spec §13 row
// "SQLite: a run never conflicts with its own claim under a frozen
// clock"). The test never advances the clock.
func testSTC1OwnClaimNoConflict(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	task := f.armDue()
	c := f.claimTask(uuid.New(), task.ID)

	for _, partial := range []bool{false, true} {
		txID, txCtx := f.begin()
		got, found := f.get(txCtx, task.ID)
		require.True(t, found)
		require.Equal(t, c.Claim.Token, got.Claim.Token)
		require.NoError(t, f.sts.StampSegment(txCtx, stRef(c), partial))
		require.NoError(t, f.tm.Commit(txCtx, txID), "a segment commit right after the run's own claim")
	}
	txID, txCtx := f.begin()
	require.NoError(t, f.sts.RemoveLife(txCtx, f.tenant, task.ID, c.ArmToken))
	require.NoError(t, f.tm.Commit(txCtx, txID))
	f.requireGone(task.ID)
}

// A joining read sees its own staged writes (C2).
func testSTC2StagedWritesVisible(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	running := f.claimTask(uuid.New(), f.armDue().ID)
	removable := f.arm(stFuture)
	e := f.newEntity()
	s := f.spec(e, "S", "T", stFuture)

	txID, txCtx := f.begin()

	f.reconcile(txCtx, e, "S", s)
	staged, found := f.get(txCtx, s.ID)
	require.True(t, found, "a staged arm is visible inside the transaction")
	requireFreshLife(t, s, staged)
	_, found = f.get(f.ctx, s.ID)
	require.False(t, found, "and not outside it")

	require.NoError(t, f.sts.StampSegment(txCtx, stRef(running), true))
	stamped, _ := f.get(txCtx, running.ID)
	require.True(t, stamped.PartialCommit)
	require.False(t, f.mustGet(running.ID).PartialCommit)

	require.NoError(t, f.sts.RemoveLife(txCtx, f.tenant, removable.ID, removable.ArmToken))
	_, found = f.get(txCtx, removable.ID)
	require.False(t, found)
	f.mustGet(removable.ID)

	require.NoError(t, f.sts.DeleteForEntities(txCtx, f.tenant, []string{e}))
	_, found = f.get(txCtx, s.ID)
	require.False(t, found, "a staged removal after a staged arm")

	require.NoError(t, f.tm.Commit(txCtx, txID))
	f.requireGone(s.ID)
	f.requireGone(removable.ID)
	require.True(t, f.mustGet(running.ID).PartialCommit)
}

// A joined callback writes the fired entity and no unsafe processor
// follows: the run's own RemoveLife finds the life replaced and the run
// commits (spec §13).
func testSTC2CallbackRearmThenRemoveLife(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	c := f.claimTask(uuid.New(), f.armDue().ID)
	s := f.spec(c.EntityID, "S", "T", stFuture)

	txID, txCtx := f.begin()
	f.reconcile(txCtx, c.EntityID, "S", s) // the callback's write re-arms the task in the run's transaction
	require.NoError(t, f.sts.RemoveLife(txCtx, f.tenant, c.ID, c.ArmToken))
	require.NoError(t, f.tm.Commit(txCtx, txID))
	requireFreshLife(t, s, f.mustGet(c.ID))
}

// A joined callback writes the fired entity in a segmented run: the stamp
// is refused, and the non-joining re-read after the rollback shows the
// life and claim unchanged — an ordinary failure (spec §13).
func testSTC2CallbackRearmThenStamp(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	c := f.claimTask(uuid.New(), f.armDue().ID)

	txID, txCtx := f.begin()
	f.reconcile(txCtx, c.EntityID, "S", f.spec(c.EntityID, "S", "T", stFuture))
	require.ErrorIs(t, f.sts.StampSegment(txCtx, stRef(c), false), spi.ErrStaleClaim,
		"the stamp sees the transaction's own re-arm (C2)")
	require.NoError(t, f.tm.Rollback(txCtx, txID))
	requireUnchangedClaim(t, c, f.mustGet(c.ID))
}

// A joined callback deletes the fired entity: the run commits (spec §13).
func testSTC2CallbackDeleteThenRemoveLife(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	c := f.claimTask(uuid.New(), f.armDue().ID)

	txID, txCtx := f.begin()
	require.NoError(t, f.sts.DeleteForEntities(txCtx, f.tenant, []string{c.EntityID}))
	require.NoError(t, f.sts.RemoveLife(txCtx, f.tenant, c.ID, c.ArmToken))
	require.NoError(t, f.tm.Commit(txCtx, txID))
	f.requireGone(c.ID)
}

// MarkUnsafe racing ClaimDue (C3): an accepted mark is always seen by the
// claim that races it or the one after; a refused mark leaves none.
func testSTC3MarkRacesClaim(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	b := uuid.New()
	require.NoError(t, f.sts.Heartbeat(context.Background(), b)) // B's claims stay B's
	for i := 0; i < 20; i++ {
		task := f.armDue()
		a := f.claimTask(uuid.New(), task.ID) // A never heartbeats: lost to any lost-owner claim

		var markErr, claimErr error
		var claimed []spi.ScheduledTask
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			markErr = f.sts.MarkUnsafe(context.Background(), stRef(a))
		}()
		go func() {
			defer wg.Done()
			<-start
			claimed, claimErr = f.sts.ClaimDue(context.Background(), f.claimReq(b, true))
		}()
		close(start)
		wg.Wait()
		require.NoError(t, claimErr)

		got := findST(f.own(claimed), task.ID)
		if got == nil { // B claimed nothing this tick; its next claim decides
			c := f.reclaim(b, task.ID)
			got = &c
		}
		if markErr == nil {
			require.True(t, got.UnsafeMarked, "iteration %d: an accepted mark must reach the claim", i)
		} else {
			require.True(t, errors.Is(markErr, spi.ErrStaleClaim) || errors.Is(markErr, spi.ErrTaskBusy),
				"iteration %d: a mark that loses the race is refused, got %v", i, markErr)
			require.False(t, got.UnsafeMarked, "iteration %d: a refused mark leaves none", i)
		}
	}
}

// stOpenWrites are joining writes that leave a task row written by an open
// transaction. lost says whether the row is claimable only by a lost-owner
// claim.
var stOpenWrites = []struct {
	name    string
	lost    bool
	prepare func(f *stFixture) spi.ScheduledTask
	write   func(ctx context.Context, f *stFixture, task spi.ScheduledTask) error
}{
	{"StagedRearm", false,
		func(f *stFixture) spi.ScheduledTask { return f.armDue() },
		func(ctx context.Context, f *stFixture, task spi.ScheduledTask) error {
			_, err := f.sts.ReconcileForEntity(ctx, spi.ReconcileRequest{TenantID: f.tenant, EntityID: task.EntityID,
				CurrentState: "S", Arm: []spi.ScheduledTask{f.spec(task.EntityID, "S", "T", stDue)}})
			return err
		}},
	{"StagedDelete", false,
		func(f *stFixture) spi.ScheduledTask { return f.armDue() },
		func(ctx context.Context, f *stFixture, task spi.ScheduledTask) error {
			return f.sts.DeleteForEntities(ctx, f.tenant, []string{task.EntityID})
		}},
	{"StagedStamp", true,
		func(f *stFixture) spi.ScheduledTask { return f.claimTask(uuid.New(), f.armDue().ID) },
		func(ctx context.Context, f *stFixture, task spi.ScheduledTask) error {
			return f.sts.StampSegment(ctx, stRef(task), false)
		}},
}

// A row written by an open transaction is not claimable (C6), and the claim
// skips it rather than waiting.
func testSTC6OpenWriteNotClaimable(t *testing.T, h Harness) {
	for _, w := range stOpenWrites {
		t.Run(w.name, func(t *testing.T) {
			f := newSTFixture(t, h)
			task := w.prepare(f)
			txID, txCtx := f.begin()
			require.NoError(t, w.write(txCtx, f, task))

			ctx, cancel := context.WithTimeout(context.Background(), stWait)
			defer cancel()
			res, err := f.sts.ClaimDue(ctx, f.claimReq(uuid.New(), w.lost))
			require.NoError(t, err, "a claim skips a row an open transaction wrote; it does not wait for it")
			require.Nil(t, findST(f.own(res), task.ID))

			require.NoError(t, f.tm.Rollback(txCtx, txID))
			require.NotNil(t, findST(f.claimWith(f.claimReq(uuid.New(), w.lost)), task.ID),
				"claimable once the transaction ended")
		})
	}
}

// MarkUnsafe answers ErrTaskBusy for a row an open transaction wrote (C6),
// including the row a joined callback re-armed or removed (spec §13 rows
// "ErrTaskBusy → safe failure" and "joined callback writes the fired
// entity, then an unsafe processor → ErrTaskBusy, safe failure, no hang").
func testSTC6MarkBusy(t *testing.T, h Harness) {
	writes := []stTaskWrite{
		{"Stamp", func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error {
			return f.sts.StampSegment(ctx, stRef(c), false)
		}},
		{"CallbackRearm", func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error {
			_, err := f.sts.ReconcileForEntity(ctx, spi.ReconcileRequest{TenantID: f.tenant, EntityID: c.EntityID,
				CurrentState: "S", Arm: []spi.ScheduledTask{f.spec(c.EntityID, "S", "T", stFuture)}})
			return err
		}},
		{"CallbackDelete", func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error {
			return f.sts.DeleteForEntities(ctx, f.tenant, []string{c.EntityID})
		}},
	}
	for _, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			f := newSTFixture(t, h)
			c := f.claimTask(uuid.New(), f.armDue().ID)
			txID, txCtx := f.begin()
			require.NoError(t, w.write(txCtx, f, c))

			ctx, cancel := context.WithTimeout(context.Background(), stWait)
			defer cancel()
			require.ErrorIs(t, f.sts.MarkUnsafe(ctx, stRef(c)), spi.ErrTaskBusy)
			require.NoError(t, ctx.Err(), "MarkUnsafe answers at once; it does not wait for the transaction")

			require.NoError(t, f.tm.Rollback(txCtx, txID))
			require.False(t, f.mustGet(c.ID).UnsafeMarked, "a busy answer writes no mark")
			require.NoError(t, f.sts.MarkUnsafe(f.ctx, stRef(c)), "accepted once the transaction ended")
		})
	}
}

// A never-joining write that meets a row an open transaction wrote gives up
// on its own, or is applied and makes that transaction's commit fail; a
// retry after the lock is gone is accepted (spec §13 rows "a
// scheduler-pool statement blocked on a task-row lock gives up after
// lock_timeout" and "a bookkeeping write retried through an outage;
// accepted after recovery").
func testSTC6NeverJoiningWriteBounded(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	c := f.claimTask(uuid.New(), f.armDue().ID)
	txID, txCtx := f.begin()
	require.NoError(t, f.sts.StampSegment(txCtx, stRef(c), false))

	attempt := spi.Attempt{Error: "E", AtMs: stNow, NextAttemptTime: stFuture}
	ctx, cancel := context.WithTimeout(context.Background(), stWait)
	defer cancel()
	recErr := f.sts.RecordAttempt(ctx, stRef(c), attempt)
	require.NoError(t, ctx.Err(), "the store must give up on its own, not run into the caller's deadline")

	if recErr != nil {
		require.NotErrorIs(t, recErr, spi.ErrStaleClaim, "a lock wait is not a refusal")
		require.NotErrorIs(t, recErr, spi.ErrStoreRejected, "a lock wait is retryable")
		requireUnchangedClaim(t, c, f.mustGet(c.ID))
		require.NoError(t, f.tm.Rollback(txCtx, txID))
		require.NoError(t, f.sts.RecordAttempt(context.Background(), stRef(c), attempt), "the retry is accepted")
	} else {
		require.ErrorIs(t, f.tm.Commit(txCtx, txID), spi.ErrConflict,
			"the write was applied at once, so the open transaction's commit fails (C1)")
	}
	got := f.mustGet(c.ID)
	require.Equal(t, spi.ScheduledTaskWaiting, got.Status)
	require.Equal(t, 1, got.Attempts)
}

// requireTxRefused asserts that a transaction's task-row write was refused
// under C1: by the statement, or by the commit. A C1 refusal satisfies
// ErrConflict (C5). With allowStale, a fenced statement may refuse earlier
// with ErrStaleClaim instead — a backend that checks the fence against the
// latest committed row sees the other transaction's change at once.
func requireTxRefused(t *testing.T, stmtErr error, commit func() error, allowStale bool) {
	t.Helper()
	if stmtErr != nil {
		if allowStale && errors.Is(stmtErr, spi.ErrStaleClaim) {
			return
		}
		require.ErrorIs(t, stmtErr, spi.ErrConflict)
		return
	}
	require.ErrorIs(t, commit(), spi.ErrConflict,
		"the commit of a transaction whose task row another transaction changed since it began must fail")
}
