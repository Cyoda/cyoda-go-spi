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
// txCtx is staged in it until Commit or Rollback. The transaction's
// snapshot is its view as of Begin (C1). A backend may take it lazily —
// PostgreSQL's REPEATABLE READ takes it at the first statement — so a test
// that needs another write to land after the snapshot first reads through
// the transaction (f.get(txCtx, …)), and a test that needs a write inside
// the snapshot makes it before f.begin(). The other write is made with
// f.ctx or a background context, which carry no transaction.

// stTaskWrite is a joining write a run's transaction makes to its own task
// row.
type stTaskWrite struct {
	name string
	// fenced says whether write checks ref's arm/claim tokens
	// (StampSegment). A fenced write may refuse a stale row earlier, from
	// the statement itself, with ErrStaleClaim — that is the fence (C5),
	// not the C1 first-committer-wins check, and requireTxRefused's
	// allowStale accepts it as an earlier-arriving equivalent. RemoveLife
	// carries no fence and can only be refused by a C1 commit failure, so
	// it must never be excused with ErrStaleClaim.
	fenced bool
	write  func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error
}

var stRunWrites = []stTaskWrite{
	{name: "StampSegment", fenced: true, write: func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error {
		return f.sts.StampSegment(ctx, stRef(c), false)
	}},
	{name: "RemoveLife", fenced: false, write: func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error {
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
			requireTxRefused(t, stmtErr, func() error { return f.tm.Commit(txCtx, txID) }, w.fenced)
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
			requireTxRefused(t, stmtErr, func() error { return f.tm.Commit(txCtx, txID) }, w.fenced)
			_ = f.tm.Rollback(txCtx, txID)

			got := f.mustGet(a.ID)
			requireFreshLife(t, s, got)
			require.NotEqual(t, a.ArmToken, got.ArmToken)
		})
	}
}

// A RemoveLife naming a life the transaction's snapshot already shows
// replaced is a no-op: no write, so no C1 conflict at commit, even though
// the row changes again after Begin. This is RearmFailsOldCommit/RemoveLife
// with the re-arm moved before Begin, and that is the only difference that
// decides the outcome: the row still changes after Begin (the outside
// change below stands in for the re-arm there), so a store that counted the
// stale call as a write would refuse this commit exactly as it refuses that
// one. The outside change is bounded by stWait, so a store that wrongly
// locks the row for the no-op fails the case instead of hanging it.
func testSTC1StaleRemoveLifeIsNoWrite(t *testing.T, h Harness) {
	outside := []struct {
		name string
		// change commits a change to the row outside the transaction and
		// verify checks that it stands after the transaction's commit.
		change func(f *stFixture, id string) (verify func(t *testing.T, got spi.ScheduledTask))
	}{
		{"Claim", func(f *stFixture, id string) func(*testing.T, spi.ScheduledTask) {
			c := f.claimTask(uuid.New(), id)
			return func(t *testing.T, got spi.ScheduledTask) { requireUnchangedClaim(t, c, got) }
		}},
		{"Rearm", func(f *stFixture, id string) func(*testing.T, spi.ScheduledTask) {
			before := f.mustGet(id)
			s := f.spec(before.EntityID, "S", "T", stFuture)
			ctx, cancel := context.WithTimeout(f.ctx, stWait)
			defer cancel()
			f.reconcile(ctx, before.EntityID, "S", s)
			return func(t *testing.T, got spi.ScheduledTask) {
				requireFreshLife(t, s, got)
				require.NotEqual(t, before.ArmToken, got.ArmToken)
			}
		}},
	}
	for _, o := range outside {
		t.Run(o.name, func(t *testing.T) {
			f := newSTFixture(t, h)
			a := f.claimTask(uuid.New(), f.armDue().ID)
			f.reconcile(f.ctx, a.EntityID, "S", f.spec(a.EntityID, "S", "T", stDue)) // re-armed before Begin
			require.NotEqual(t, a.ArmToken, f.mustGet(a.ID).ArmToken)

			txID, txCtx := f.begin()
			_, _ = f.get(txCtx, a.ID)
			require.NoError(t, f.sts.RemoveLife(txCtx, f.tenant, a.ID, a.ArmToken),
				"a RemoveLife naming a replaced life is a no-op")
			verify := o.change(f, a.ID) // commits a change to the row after the transaction began

			require.NoError(t, f.tm.Commit(txCtx, txID), "the no-op wrote nothing, so C1 has nothing to refuse")
			verify(t, f.mustGet(a.ID))
		})
	}
}

// An update, a delete or an import racing a claim gets a C1 conflict; the
// server's retry in a new transaction succeeds; the same on every backend
// (spec §13 row "an update racing a claim → retryable 409; a delete or
// import racing one claim succeeds after the server retry").
func testSTC1ClientWriteAfterClaim(t *testing.T, h Harness) {
	writes := []struct {
		name  string
		write func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error
		// verify checks the retry's effect once it has committed: a fresh
		// life for the re-arm, gone for the two deletes.
		verify func(t *testing.T, f *stFixture, task spi.ScheduledTask)
	}{
		{"ReconcileForEntity",
			func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error {
				_, err := f.sts.ReconcileForEntity(ctx, spi.ReconcileRequest{TenantID: f.tenant, EntityID: c.EntityID,
					CurrentState: "S", Arm: []spi.ScheduledTask{f.spec(c.EntityID, "S", "T", stFuture)}})
				return err
			},
			func(t *testing.T, f *stFixture, task spi.ScheduledTask) {
				requireFreshLife(t, f.spec(task.EntityID, "S", "T", stFuture), f.mustGet(task.ID))
			}},
		{"DeleteForEntities",
			func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error {
				return f.sts.DeleteForEntities(ctx, f.tenant, []string{c.EntityID})
			},
			func(t *testing.T, f *stFixture, task spi.ScheduledTask) { f.requireGone(task.ID) }},
		{"DeleteForModel",
			func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error {
				return f.sts.DeleteForModel(ctx, f.tenant, f.model, 1, nil)
			},
			func(t *testing.T, f *stFixture, task spi.ScheduledTask) { f.requireGone(task.ID) }},
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
			w.verify(t, f, task)
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

// C1 says "another transaction, joining or not" — a joining transaction
// that fully committed counts too, not only a non-joining claim or a
// plain, implicitly-committed call. Driving the second writer through its
// own explicit Begin/Commit (rather than f.ctx or a non-joining call, as
// the other C1 cases do) catches a memory or SQLite store whose Commit
// path never adds its task keys to the committed log that another open
// transaction's conflict check reads.
func testSTC1CommittedTxFailsOldCommit(t *testing.T, h Harness) {
	others := []struct {
		name  string
		write func(ctx context.Context, f *stFixture, a spi.ScheduledTask) error
		// verify checks the second writer's committed effect, which stands
		// regardless of tx1's outcome: gone after DeleteForModel, a fresh
		// life after the re-arm.
		verify func(t *testing.T, f *stFixture, a spi.ScheduledTask)
	}{
		{"DeleteForModel",
			func(ctx context.Context, f *stFixture, a spi.ScheduledTask) error {
				return f.sts.DeleteForModel(ctx, f.tenant, f.model, 1, nil)
			},
			func(t *testing.T, f *stFixture, a spi.ScheduledTask) { f.requireGone(a.ID) }},
		{"Rearm",
			func(ctx context.Context, f *stFixture, a spi.ScheduledTask) error {
				_, err := f.sts.ReconcileForEntity(ctx, spi.ReconcileRequest{TenantID: f.tenant, EntityID: a.EntityID,
					CurrentState: "S", Arm: []spi.ScheduledTask{f.spec(a.EntityID, "S", "T", stFuture)}})
				return err
			},
			func(t *testing.T, f *stFixture, a spi.ScheduledTask) {
				requireFreshLife(t, f.spec(a.EntityID, "S", "T", stFuture), f.mustGet(a.ID))
			}},
	}
	for _, w := range stRunWrites {
		t.Run(w.name, func(t *testing.T) {
			for _, other := range others {
				t.Run(other.name, func(t *testing.T) {
					f := newSTFixture(t, h)
					a := f.claimTask(uuid.New(), f.armDue().ID)

					tx1ID, tx1Ctx := f.begin()
					_, _ = f.get(tx1Ctx, a.ID)

					tx2ID, tx2Ctx := f.begin()
					require.NoError(t, other.write(tx2Ctx, f, a))
					require.NoError(t, f.tm.Commit(tx2Ctx, tx2ID), "the second writer's own transaction fully commits")

					stmtErr := w.write(tx1Ctx, f, a)
					requireTxRefused(t, stmtErr, func() error { return f.tm.Commit(tx1Ctx, tx1ID) }, w.fenced)
					_ = f.tm.Rollback(tx1Ctx, tx1ID)
					other.verify(t, f, a)
				})
			}
		})
	}
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

// A joined callback deletes the fired entity: the run's own RemoveLife
// sees the transaction's own staged delete (C2) and the run commits
// (spec §13).
func testSTC2CallbackDeleteThenRemoveLife(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	c := f.claimTask(uuid.New(), f.armDue().ID)

	txID, txCtx := f.begin()
	require.NoError(t, f.sts.DeleteForEntities(txCtx, f.tenant, []string{c.EntityID}))
	_, found := f.get(txCtx, c.ID)
	require.False(t, found, "the transaction sees its own staged delete (C2)")
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
// claim. notBusy says the write removes nothing, so C6 does not apply: the
// row stays claimable throughout, unlike every other row here.
var stOpenWrites = []struct {
	name    string
	lost    bool
	notBusy bool
	prepare func(f *stFixture) spi.ScheduledTask
	write   func(ctx context.Context, f *stFixture, task spi.ScheduledTask) error
}{
	{"StagedRearm", false, false,
		func(f *stFixture) spi.ScheduledTask { return f.armDue() },
		func(ctx context.Context, f *stFixture, task spi.ScheduledTask) error {
			_, err := f.sts.ReconcileForEntity(ctx, spi.ReconcileRequest{TenantID: f.tenant, EntityID: task.EntityID,
				CurrentState: "S", Arm: []spi.ScheduledTask{f.spec(task.EntityID, "S", "T", stDue)}})
			return err
		}},
	{"StagedDelete", false, false,
		func(f *stFixture) spi.ScheduledTask { return f.armDue() },
		func(ctx context.Context, f *stFixture, task spi.ScheduledTask) error {
			return f.sts.DeleteForEntities(ctx, f.tenant, []string{task.EntityID})
		}},
	{"StagedRemoveLife", false, false,
		func(f *stFixture) spi.ScheduledTask { return f.armDue() },
		func(ctx context.Context, f *stFixture, task spi.ScheduledTask) error {
			return f.sts.RemoveLife(ctx, f.tenant, task.ID, task.ArmToken)
		}},
	// StagedRemoveLifeStale is the non-vacuous counterpart of StagedRemoveLife
	// directly above: same due task, same open transaction, but prepare
	// re-arms the task before Begin, so the transaction's snapshot already
	// shows the new life, and RemoveLife is staged with the old token — a
	// no-op (C1). StagedRemoveLife proves the current-token call makes the
	// row busy; this proves the no-op does not (C6).
	{"StagedRemoveLifeStale", false, true,
		func(f *stFixture) spi.ScheduledTask {
			old := f.armDue()
			f.reconcile(f.ctx, old.EntityID, "S", f.spec(old.EntityID, "S", "T", stDue))
			require.NotEqual(f.t, old.ArmToken, f.mustGet(old.ID).ArmToken)
			return old
		},
		func(ctx context.Context, f *stFixture, task spi.ScheduledTask) error {
			return f.sts.RemoveLife(ctx, f.tenant, task.ID, task.ArmToken)
		}},
	{"StagedDeleteForModel", false, false,
		func(f *stFixture) spi.ScheduledTask { return f.armDue() },
		func(ctx context.Context, f *stFixture, task spi.ScheduledTask) error {
			return f.sts.DeleteForModel(ctx, f.tenant, f.model, 1, nil)
		}},
	{"StagedStamp", true, false,
		func(f *stFixture) spi.ScheduledTask { return f.claimTask(uuid.New(), f.armDue().ID) },
		func(ctx context.Context, f *stFixture, task spi.ScheduledTask) error {
			return f.sts.StampSegment(ctx, stRef(task), false)
		}},
	{"StagedFail", true, false,
		func(f *stFixture) spi.ScheduledTask { return f.claimTask(uuid.New(), f.armDue().ID) },
		func(ctx context.Context, f *stFixture, task spi.ScheduledTask) error {
			return f.sts.Fail(ctx, stRef(task), spi.Failure{Reason: spi.FailureRunPanicked, Error: "E", AtMs: stNow})
		}},
}

// A row written by an open transaction is not claimable (C6), and the claim
// skips it rather than waiting — except a notBusy row (a RemoveLife that
// removes nothing): that row stays claimable throughout, and MarkUnsafe on
// the resulting claim is accepted, because the row was never busy.
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
			claimed := findST(f.own(res), task.ID)

			if w.notBusy {
				require.NotNil(t, claimed,
					"a RemoveLife that removes nothing does not make the row busy (C6): still claimable while the transaction is open")
				require.NoError(t, f.sts.MarkUnsafe(ctx, stRef(*claimed)),
					"MarkUnsafe on the claim is accepted: the row was never busy (C6)")
				require.NoError(t, f.tm.Rollback(txCtx, txID))
				return
			}
			require.Nil(t, claimed)

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
		{name: "Stamp", write: func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error {
			return f.sts.StampSegment(ctx, stRef(c), false)
		}},
		{name: "CallbackRearm", write: func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error {
			_, err := f.sts.ReconcileForEntity(ctx, spi.ReconcileRequest{TenantID: f.tenant, EntityID: c.EntityID,
				CurrentState: "S", Arm: []spi.ScheduledTask{f.spec(c.EntityID, "S", "T", stFuture)}})
			return err
		}},
		{name: "CallbackDelete", write: func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error {
			return f.sts.DeleteForEntities(ctx, f.tenant, []string{c.EntityID})
		}},
		{name: "DeleteForModel", write: func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error {
			return f.sts.DeleteForModel(ctx, f.tenant, f.model, 1, nil)
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
			require.NoError(t, ctx.Err(), "MarkUnsafe answers at once; it does not wait for the transaction to end")

			require.NoError(t, f.tm.Rollback(txCtx, txID))
			require.False(t, f.mustGet(c.ID).UnsafeMarked, "a busy answer writes no mark")
			require.NoError(t, f.sts.MarkUnsafe(f.ctx, stRef(c)), "accepted once the transaction ended")
		})
	}
}

// A never-joining write that meets a row an open transaction wrote gives up
// on its own and answers ErrTaskBusy, making no write; a retry after the
// transaction ends is accepted (spec §13 rows "a scheduler-pool statement
// blocked on a task-row lock gives up after lock_timeout" and "a
// bookkeeping write retried through an outage; accepted after recovery").
// A store may not instead apply the write and let the open transaction's
// commit fail (C6).
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
	require.ErrorIs(t, recErr, spi.ErrTaskBusy,
		"a never-joining write on a row an open transaction wrote always answers ErrTaskBusy and makes no write (C6)")
	requireUnchangedClaim(t, c, f.mustGet(c.ID))

	require.NoError(t, f.tm.Rollback(txCtx, txID))
	require.NoError(t, f.sts.RecordAttempt(context.Background(), stRef(c), attempt), "the retry is accepted")
	got := f.mustGet(c.ID)
	require.Equal(t, spi.ScheduledTaskWaiting, got.Status)
	require.Equal(t, 1, got.Attempts)
}

// GiveBackIdle skips a row an open transaction wrote (C6): the give-back
// neither counts it nor changes it, and the task stays RUNNING under its
// claim until the transaction ends. It first proves GiveBackIdle would
// give the task back if not for the open write — the after-rollback half —
// so the busy half is not vacuous. GiveBackIdle never waits for the
// transaction; the call is bounded by stWait so a store that blocks on the
// row's lock fails the test cleanly instead of hanging it.
func testSTC6GiveBackSkipsBusy(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	owner := uuid.New()
	c := f.claimTask(owner, f.armDue().ID)

	txID, txCtx := f.begin()
	require.NoError(t, f.sts.StampSegment(txCtx, stRef(c), false))

	ctx, cancel := context.WithTimeout(context.Background(), stWait)
	defer cancel()
	n, err := f.sts.GiveBackIdle(ctx, owner, nil)
	require.NoError(t, err)
	require.NoError(t, ctx.Err(), "GiveBackIdle skips a busy row; it does not wait for the transaction to end")
	require.Equal(t, 0, n, "a row an open transaction wrote is not counted")
	got := f.mustGet(c.ID)
	require.Equal(t, spi.ScheduledTaskRunning, got.Status, "the row stays under its claim")
	requireUnchangedClaim(t, c, got)

	require.NoError(t, f.tm.Rollback(txCtx, txID))
	n, err = f.sts.GiveBackIdle(context.Background(), owner, nil)
	require.NoError(t, err)
	require.Equal(t, 1, n, "claimable and countable once the transaction ended")
	require.Equal(t, spi.ScheduledTaskWaiting, f.mustGet(c.ID).Status)
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
