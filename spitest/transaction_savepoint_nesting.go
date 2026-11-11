package spitest

import (
	"testing"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// savepointNesting is the fixture of the savepoint nesting cases: one open
// transaction and a way to save entities in it and to see, after Commit, which
// of them exist.
type savepointNesting struct {
	h     Harness
	ctx   spiCtx
	tm    spi.TransactionManager
	txID  string
	txCtx spiCtx
	es    spi.EntityStore
}

func newSavepointNesting(t *testing.T, h Harness) *savepointNesting {
	t.Helper()
	ctx := tenantContext(h.NewTenant())
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID, txCtx := beginGuarded(t, tm, ctx)
	es, err := h.Factory.EntityStore(txCtx)
	require.NoError(t, err)
	return &savepointNesting{h: h, ctx: ctx, tm: tm, txID: txID, txCtx: txCtx, es: es}
}

// save writes a new entity in the transaction and returns its id.
func (n *savepointNesting) save(t *testing.T) string {
	t.Helper()
	id := newID()
	_, err := n.es.Save(n.txCtx, newEntity(t, "m-sp", id, map[string]any{}))
	require.NoError(t, err)
	return id
}

func (n *savepointNesting) savepoint(t *testing.T) string {
	t.Helper()
	sp, err := n.tm.Savepoint(n.txCtx, n.txID)
	require.NoError(t, err)
	return sp
}

// commit commits the transaction, so requireExists and requireAbsent can read
// what it kept.
func (n *savepointNesting) commit(t *testing.T) {
	t.Helper()
	require.NoError(t, n.tm.Commit(n.txCtx, n.txID))
}

func (n *savepointNesting) requireExists(t *testing.T, id, why string) {
	t.Helper()
	es, err := n.h.Factory.EntityStore(n.ctx)
	require.NoError(t, err)
	_, err = es.Get(n.ctx, id)
	require.NoError(t, err, why)
}

func (n *savepointNesting) requireAbsent(t *testing.T, id, why string) {
	t.Helper()
	es, err := n.h.Factory.EntityStore(n.ctx)
	require.NoError(t, err)
	_, err = es.Get(n.ctx, id)
	require.ErrorIs(t, err, spi.ErrNotFound, why)
}

// A rollback to a savepoint destroys every savepoint taken after it.
func testTxSavepointRollbackDropsLater(t *testing.T, h Harness) {
	n := newSavepointNesting(t, h)
	a := n.savepoint(t)
	e1 := n.save(t)
	b := n.savepoint(t)
	e2 := n.save(t)

	require.NoError(t, n.tm.RollbackToSavepoint(n.txCtx, n.txID, a))
	err := n.tm.RollbackToSavepoint(n.txCtx, n.txID, b)
	require.ErrorIs(t, err, spi.ErrSavepointNotFound,
		"a savepoint taken after the one rolled back to must be gone")

	n.commit(t)
	n.requireAbsent(t, e1, "work after the rolled-back-to savepoint must be discarded")
	n.requireAbsent(t, e2, "work after the rolled-back-to savepoint must be discarded")
}

// A rollback to a savepoint keeps the savepoint itself.
func testTxSavepointSurvivesOwnRollback(t *testing.T, h Harness) {
	n := newSavepointNesting(t, h)
	a := n.savepoint(t)
	e1 := n.save(t)
	require.NoError(t, n.tm.RollbackToSavepoint(n.txCtx, n.txID, a))

	e2 := n.save(t)
	require.NoError(t, n.tm.RollbackToSavepoint(n.txCtx, n.txID, a),
		"the savepoint must survive a rollback to it")
	require.NoError(t, n.tm.ReleaseSavepoint(n.txCtx, n.txID, a),
		"the savepoint must still be releasable after a rollback to it")

	n.commit(t)
	n.requireAbsent(t, e1, "work rolled back must stay discarded")
	n.requireAbsent(t, e2, "work rolled back a second time must stay discarded")
}

// A release destroys the savepoint and every savepoint taken after it, and
// keeps their work. Rolling back to or releasing a destroyed savepoint
// returns ErrSavepointNotFound.
func testTxSavepointReleaseReleasesLater(t *testing.T, h Harness) {
	n := newSavepointNesting(t, h)
	a := n.savepoint(t)
	b := n.savepoint(t)
	e1 := n.save(t)

	require.NoError(t, n.tm.ReleaseSavepoint(n.txCtx, n.txID, a))
	err := n.tm.RollbackToSavepoint(n.txCtx, n.txID, b)
	require.ErrorIs(t, err, spi.ErrSavepointNotFound,
		"a savepoint taken after the one released must be gone")
	err = n.tm.ReleaseSavepoint(n.txCtx, n.txID, a)
	require.ErrorIs(t, err, spi.ErrSavepointNotFound,
		"a released savepoint must not be releasable again")
	err = n.tm.ReleaseSavepoint(n.txCtx, n.txID, b)
	require.ErrorIs(t, err, spi.ErrSavepointNotFound,
		"a savepoint released with an earlier one must not be releasable")

	n.commit(t)
	n.requireExists(t, e1, "a release must keep the work")
}

// Releasing a nested savepoint leaves the outer one able to roll back the
// nested savepoint's work too.
func testTxSavepointOuterRollbackAfterReleasedNested(t *testing.T, h Harness) {
	n := newSavepointNesting(t, h)
	e0 := n.save(t)
	a := n.savepoint(t)
	e1 := n.save(t)
	b := n.savepoint(t)
	e2 := n.save(t)

	require.NoError(t, n.tm.ReleaseSavepoint(n.txCtx, n.txID, b))
	e3 := n.save(t)
	require.NoError(t, n.tm.RollbackToSavepoint(n.txCtx, n.txID, a))

	n.commit(t)
	n.requireExists(t, e0, "work before the outer savepoint must survive")
	n.requireAbsent(t, e1, "work after the outer savepoint must be discarded")
	n.requireAbsent(t, e2, "released nested work must be discarded by the outer rollback")
	n.requireAbsent(t, e3, "work after the release must be discarded by the outer rollback")
}
