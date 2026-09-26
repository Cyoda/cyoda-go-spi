package spitest

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// The LostRace cases pin TransactionManager.LostRace: a transaction that has
// written an entity another transaction committed after its snapshot has lost
// the race, on every backend, before it commits — whether the backend refused
// the write or accepted it. The fixture is savepointRace's: the transaction
// has written another entity first, so every backend has taken its snapshot
// before any rival commits.

// requireLostRace asserts LostRace answers want without error.
func requireLostRace(t *testing.T, tm spi.TransactionManager, txCtx spiCtx, txID string, want bool, msg string) {
	t.Helper()
	lost, err := tm.LostRace(txCtx, txID)
	require.NoError(t, err, "LostRace must answer, also on a transaction its engine has aborted")
	require.Equal(t, want, lost, msg)
}

// A rival commits the target after the snapshot; the transaction then writes
// it. LostRace answers true, also after a later statement that an engine
// which aborted the transaction refuses, and Commit refuses the transaction.
func testTxLostRaceWriteLost(t *testing.T, h Harness) {
	r := newSavepointRace(t, h)
	r.rivalCommit(t, h)
	r.writeTarget(t)
	requireLostRace(t, r.tm, r.txCtx, r.txID, true,
		"a write to an entity a rival committed after the snapshot has lost the race")
	if _, err := r.es.Get(r.txCtx, r.pre); err != nil {
		require.ErrorIs(t, err, spi.ErrTxAborted, "a statement after the lost write may only be refused as aborted")
	}
	requireLostRace(t, r.tm, r.txCtx, r.txID, true, "the answer stays true until the transaction ends")
	r.requireCommitConflict(t, h)
}

// Control: no rival. LostRace answers false and Commit applies.
func testTxLostRaceNoRival(t *testing.T, h Harness) {
	r := newSavepointRace(t, h)
	r.writeTarget(t)
	requireLostRace(t, r.tm, r.txCtx, r.txID, false, "a write no rival raced has not lost")
	r.requireCommitApplies(t, h, `{"v":"mine"}`)
}

// Control: a rival that committed the target before the transaction began
// raced nothing: the transaction sees its write.
func testTxLostRaceRivalBeforeBegin(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	model, target := "m-lost-race", newID()
	for _, v := range []string{"seed", "rival"} {
		withTx(t, h, ctx, func(txCtx spiCtx) {
			es, err := h.Factory.EntityStore(txCtx)
			require.NoError(t, err)
			_, err = es.Save(txCtx, newEntity(t, model, target, map[string]any{"v": v}))
			require.NoError(t, err)
		})
	}
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID, txCtx := beginGuarded(t, tm, ctx)
	es, err := h.Factory.EntityStore(txCtx)
	require.NoError(t, err)
	_, err = es.Save(txCtx, newEntity(t, model, target, map[string]any{"v": "mine"}))
	require.NoError(t, err)
	requireLostRace(t, tm, txCtx, txID, false, "a rival that committed before the snapshot raced nothing")
	require.NoError(t, tm.Commit(txCtx, txID))
}

// Control: a rival that writes the target after the transaction wrote it,
// and has not committed, has not won. LostRace answers false; the
// transaction commits first and the rival loses. A backend that locks the
// row at the write holds the rival's write until the transaction ends, so
// the rival writes from its own goroutine.
func testTxLostRaceRivalNotCommitted(t *testing.T, h Harness) {
	r := newSavepointRace(t, h)
	r.writeTarget(t)

	rivalID, rivalCtx := beginGuarded(t, r.tm, r.ctx)
	rivalES, err := h.Factory.EntityStore(rivalCtx)
	require.NoError(t, err)
	_, err = rivalES.Get(rivalCtx, r.target) // the rival's snapshot, before the transaction commits
	require.NoError(t, err)
	rivalWrite := newEntity(t, r.model, r.target, map[string]any{"v": "rival"})
	proceed := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(proceed) }) }
	t.Cleanup(release) // a failed assertion must not strand the rival
	rivalDone := make(chan error, 1)
	go func() {
		if _, err := rivalES.Save(rivalCtx, rivalWrite); err != nil {
			rivalDone <- err
			return
		}
		<-proceed
		rivalDone <- r.tm.Commit(rivalCtx, rivalID)
	}()

	requireLostRace(t, r.tm, r.txCtx, r.txID, false, "a rival that has not committed has not won")
	r.requireCommitApplies(t, h, `{"v":"mine"}`)
	release()
	require.ErrorIs(t, <-rivalDone, spi.ErrConflict, "the rival lost: the transaction committed first")
}

// A savepoint rollback that discards a write which had already lost keeps the
// loss: LostRace still answers true.
func testTxLostRaceAfterSavepointRollback(t *testing.T, h Harness) {
	r := newSavepointRace(t, h)
	sp, err := r.tm.Savepoint(r.txCtx, r.txID)
	require.NoError(t, err)
	r.rivalCommit(t, h)
	r.writeTarget(t)
	require.NoError(t, r.tm.RollbackToSavepoint(r.txCtx, r.txID, sp))
	requireLostRace(t, r.tm, r.txCtx, r.txID, true,
		"a savepoint rollback does not undo a lost race")
	r.requireCommitConflict(t, h)
}

// A savepoint rollback that discards a write no rival raced leaves nothing
// lost, even when a rival commits the entity afterwards.
func testTxLostRaceDiscardedRivalAfter(t *testing.T, h Harness) {
	r := newSavepointRace(t, h)
	sp, err := r.tm.Savepoint(r.txCtx, r.txID)
	require.NoError(t, err)
	r.writeTarget(t)
	require.NoError(t, r.tm.RollbackToSavepoint(r.txCtx, r.txID, sp))
	r.rivalCommit(t, h)
	requireLostRace(t, r.tm, r.txCtx, r.txID, false,
		"a rival that committed after the rollback raced no write of the transaction")
	r.requireCommitApplies(t, h, `{"v":"rival"}`)
}

// Tenant B cannot ask about tenant A's transaction: the answer wraps
// ErrTxTenantMismatch, as every other TransactionManager method does, and
// discloses nothing about it.
func testTxLostRaceTenantMismatch(t *testing.T, h Harness) {
	r := newSavepointRace(t, h)
	r.rivalCommit(t, h)
	r.writeTarget(t)

	ctxB := tenantContext(h.NewTenant())
	tmB, err := h.Factory.TransactionManager(ctxB)
	require.NoError(t, err)
	lost, err := tmB.LostRace(ctxB, r.txID)
	require.True(t, errors.Is(err, spi.ErrTxTenantMismatch),
		"cross-tenant LostRace must wrap ErrTxTenantMismatch; got: %v", err)
	require.False(t, lost, "a refused call answers false")

	requireLostRace(t, r.tm, r.txCtx, r.txID, true, "the refused call changes nothing")
	r.requireCommitConflict(t, h)
}

// A transaction that is not open answers ErrTxNotFound.
func testTxLostRaceNotFound(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	_, err = tm.LostRace(ctx, newID())
	require.ErrorIs(t, err, spi.ErrTxNotFound)
}
