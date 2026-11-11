package spitest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// runCoordinationSuite exercises the optional spi.TransactionCoordinationCapable
// contract. Gated on a type assertion (not a Skip key) because the capability
// is optional and a never-matching Skip key fails the run. A factory that
// implements the interface but returns a nil coordinator is not capable
// either.
//
// Each case begins a transaction with TransactionManager.Begin and drives the
// coordinator directly, in the order cyoda-go drives it: a callout is begun
// and advanced to major 1 before a callback opens a scope under it, each
// callback joins the transaction with Join before it opens its scope, and the
// owner seals before it commits.
func runCoordinationSuite(t *testing.T, h Harness, tracker *skipTracker) {
	capable, ok := h.Factory.(spi.TransactionCoordinationCapable)
	if !ok {
		t.Skip("factory is not TransactionCoordinationCapable")
	}
	co, err := capable.TransactionCoordinator(context.Background())
	require.NoError(t, err)
	if co == nil {
		t.Skip("factory is not TransactionCoordinationCapable")
	}
	with := func(fn func(*testing.T, Harness, spi.TransactionCoordinator)) func(*testing.T, Harness) {
		return func(t *testing.T, h Harness) { fn(t, h, co) }
	}
	runSubtest(t, h, tracker, "ScopeWritesBelongToScope", with(testCoordScopeWritesBelongToScope))
	runSubtest(t, h, tracker, "CompleteMergesIntoParent", with(testCoordCompleteMergesIntoParent))
	runSubtest(t, h, tracker, "CompletedVisibleToParent", with(testCoordCompletedVisibleToParent))
	runSubtest(t, h, tracker, "OpenScopeChecksAndAbsorbsMinor", with(testCoordOpenScopeChecksAndAbsorbsMinor))
	runSubtest(t, h, tracker, "AdvanceStopsLowerMajor", with(testCoordAdvanceStopsLowerMajor))
	runSubtest(t, h, tracker, "CompleteRefusalOrder", with(testCoordCompleteRefusalOrder))
	runSubtest(t, h, tracker, "RollbackOnlyMarksParent", with(testCoordRollbackOnlyMarksParent))
	runSubtest(t, h, tracker, "ProcessorScopeAbsorbsMark", with(testCoordProcessorScopeAbsorbsMark))
	runSubtest(t, h, tracker, "SealCutsOff", with(testCoordSealCutsOff))
	runSubtest(t, h, tracker, "ScopeConflict", with(testCoordScopeConflict))
}

const coordModel = "m-coord"

// coordFixture is one open transaction of a fresh tenant, and the coordinator
// under test.
type coordFixture struct {
	h     Harness
	co    spi.TransactionCoordinator
	ctx   spiCtx // the tenant, outside any transaction
	tm    spi.TransactionManager
	txID  string
	txCtx spiCtx // the root scope
}

func newCoordFixture(t *testing.T, h Harness, co spi.TransactionCoordinator) *coordFixture {
	t.Helper()
	ctx := tenantContext(h.NewTenant())
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID, txCtx := beginGuarded(t, tm, ctx)
	return &coordFixture{h: h, co: co, ctx: ctx, tm: tm, txID: txID, txCtx: txCtx}
}

func pair(callout string, major, minor uint32) spi.CalloutPair {
	return spi.CalloutPair{Callout: callout, Major: major, Minor: minor}
}

// callout begins a callout from the scope on ctx, under outer, and advances
// it to major 1, so (id, 1, 0) is current. It returns the callout's id.
func (f *coordFixture) callout(t *testing.T, ctx spiCtx, outer ...spi.CalloutPair) string {
	t.Helper()
	id := "callout-" + newID()
	require.NoError(t, f.co.BeginCallout(ctx, f.txID, id, outer))
	require.NoError(t, f.co.AdvanceCallout(ctx, f.txID, id, 1))
	return id
}

// tryOpen joins the transaction, as a callback does, and opens a callback
// scope with pairs on the joined context.
func (f *coordFixture) tryOpen(t *testing.T, pairs ...spi.CalloutPair) (spiCtx, error) {
	t.Helper()
	jctx, err := f.tm.Join(f.ctx, f.txID)
	require.NoError(t, err)
	return f.co.OpenScope(jctx, f.txID, pairs)
}

func (f *coordFixture) open(t *testing.T, pairs ...spi.CalloutPair) spiCtx {
	t.Helper()
	sctx, err := f.tryOpen(t, pairs...)
	require.NoError(t, err)
	return sctx
}

// save writes a new entity on ctx and returns its id.
func (f *coordFixture) save(t *testing.T, ctx spiCtx) string {
	t.Helper()
	id := newID()
	es, err := f.h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	_, err = es.Save(ctx, newEntity(t, coordModel, id, map[string]any{"by": "coordination"}))
	require.NoError(t, err)
	return id
}

func (f *coordFixture) get(t *testing.T, ctx spiCtx, id string) (*spi.Entity, error) {
	t.Helper()
	es, err := f.h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	return es.Get(ctx, id)
}

// sealAndCommit is the owner's end of a transaction whose root is not
// marked.
func (f *coordFixture) sealAndCommit(t *testing.T) {
	t.Helper()
	require.NoError(t, f.co.Seal(f.txCtx, f.txID))
	require.NoError(t, f.tm.Commit(f.txCtx, f.txID))
}

func (f *coordFixture) requireExists(t *testing.T, id, why string) {
	t.Helper()
	_, err := f.get(t, f.ctx, id)
	require.NoError(t, err, why)
}

func (f *coordFixture) requireAbsent(t *testing.T, id, why string) {
	t.Helper()
	_, err := f.get(t, f.ctx, id)
	require.ErrorIs(t, err, spi.ErrNotFound, why)
}

// returnsWithoutWaiting runs fn, which must not wait for a scope in progress,
// and fails the case if it has not returned in time.
func returnsWithoutWaiting(t *testing.T, what string, fn func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		require.NoError(t, err, what)
	case <-time.After(10 * time.Second): // realtime: guards a call that must not block
		t.Fatalf("%s waited for a scope in progress; it must return at once", what)
	}
}

// Contract 1. A write on a scope's context belongs to the scope, so discarding
// the scope removes it.
func testCoordScopeWritesBelongToScope(t *testing.T, h Harness, co spi.TransactionCoordinator) {
	f := newCoordFixture(t, h, co)
	root := f.save(t, f.txCtx)
	c := f.callout(t, f.txCtx)

	s := f.open(t, pair(c, 1, 0))
	w := f.save(t, s)
	require.NoError(t, f.co.DiscardScope(s, false))
	require.NoError(t, f.co.EndCallout(f.txCtx, f.txID, c))

	f.sealAndCommit(t)
	f.requireExists(t, root, "the root's own write must commit")
	f.requireAbsent(t, w, "a write on a discarded scope's context must not commit")
}

// Contract 2. A completed scope's writes become its parent's, so a later
// discard of the parent removes them; a completed parent keeps them.
func testCoordCompleteMergesIntoParent(t *testing.T, h Harness, co spi.TransactionCoordinator) {
	run := func(t *testing.T, completeProcessor bool) (root, w string, f *coordFixture) {
		f = newCoordFixture(t, h, co)
		root = f.save(t, f.txCtx)
		p, err := f.co.OpenChildScope(f.txCtx)
		require.NoError(t, err)
		c := f.callout(t, p)

		s := f.open(t, pair(c, 1, 0))
		w = f.save(t, s)
		require.NoError(t, f.co.CompleteScope(s))
		require.NoError(t, f.co.EndCallout(p, f.txID, c))
		if completeProcessor {
			require.NoError(t, f.co.CompleteScope(p))
		} else {
			require.NoError(t, f.co.DiscardScope(p, false))
		}
		f.sealAndCommit(t)
		return root, w, f
	}

	t.Run("ParentDiscarded", func(t *testing.T) {
		root, w, f := run(t, false)
		f.requireExists(t, root, "the root's own write must commit")
		f.requireAbsent(t, w, "a completed child's write must go with its discarded parent")
	})
	t.Run("ParentCompleted", func(t *testing.T) {
		root, w, f := run(t, true)
		f.requireExists(t, root, "the root's own write must commit")
		f.requireExists(t, w, "a completed child of a completed parent must commit")
	})
}

// Contract 3. A completed scope's writes are visible to its parent and to the
// parent's later scopes, before commit.
func testCoordCompletedVisibleToParent(t *testing.T, h Harness, co spi.TransactionCoordinator) {
	f := newCoordFixture(t, h, co)
	c := f.callout(t, f.txCtx)

	s1 := f.open(t, pair(c, 1, 0))
	w := f.save(t, s1)
	require.NoError(t, f.co.CompleteScope(s1))

	_, err := f.get(t, f.txCtx, w)
	require.NoError(t, err, "the parent (the root) must see a completed scope's write")
	s2 := f.open(t, pair(c, 1, 0))
	_, err = f.get(t, s2, w)
	require.NoError(t, err, "a later scope of the parent must see a completed scope's write")
	require.NoError(t, f.co.CompleteScope(s2))
	require.NoError(t, f.co.EndCallout(f.txCtx, f.txID, c))

	f.sealAndCommit(t)
	f.requireExists(t, w, "a completed scope's write must commit")
}

// Contract 4. OpenScope admits only current pairs and absorbs a higher minor
// in the same atomic step; BeginCallout refuses an outer pair that is not
// current; an ended callout has no current pair.
func testCoordOpenScopeChecksAndAbsorbsMinor(t *testing.T, h Harness, co spi.TransactionCoordinator) {
	f := newCoordFixture(t, h, co)
	c := f.callout(t, f.txCtx)

	s1 := f.open(t, pair(c, 1, 2))

	_, err := f.tryOpen(t, pair(c, 1, 5), pair("callout-never-begun-"+newID(), 1, 0))
	require.ErrorIs(t, err, spi.ErrCalloutSuperseded,
		"a pass with a pair of an unregistered callout must be refused")
	_, err = f.tryOpen(t, pair(c, 1, 2))
	require.NoError(t, err,
		"a refused pass must absorb nothing: minor 2 must still be current")

	_, err = f.tryOpen(t, pair(c, 1, 1))
	require.ErrorIs(t, err, spi.ErrCalloutSuperseded,
		"a minor below the highest seen must be refused")
	err = f.co.BeginCallout(s1, f.txID, "callout-"+newID(), []spi.CalloutPair{pair(c, 1, 1)})
	require.ErrorIs(t, err, spi.ErrCalloutSuperseded,
		"BeginCallout must refuse an outer pair that is not current")

	require.NoError(t, f.co.DiscardScope(s1, false))
	require.NoError(t, f.co.EndCallout(f.txCtx, f.txID, c))
	_, err = f.tryOpen(t, pair(c, 1, 2))
	require.ErrorIs(t, err, spi.ErrCalloutSuperseded,
		"a pair of an ended callout must be refused")
}

// Contract 5. After AdvanceCallout(c, m) no scope admitted under c below m
// completes, and after EndCallout(c) no scope admitted under c completes.
// Neither call waits for the scope in progress; the scope's own
// CompleteScope refuses and discards it.
func testCoordAdvanceStopsLowerMajor(t *testing.T, h Harness, co spi.TransactionCoordinator) {
	f := newCoordFixture(t, h, co)
	root := f.save(t, f.txCtx)
	c := f.callout(t, f.txCtx)

	s1 := f.open(t, pair(c, 1, 0))
	w1 := f.save(t, s1)
	returnsWithoutWaiting(t, "AdvanceCallout", func() error {
		return f.co.AdvanceCallout(f.txCtx, f.txID, c, 2)
	})
	require.ErrorIs(t, f.co.CompleteScope(s1), spi.ErrCalloutSuperseded,
		"a scope admitted below the advanced major must not complete")

	s2 := f.open(t, pair(c, 2, 0))
	w2 := f.save(t, s2)
	returnsWithoutWaiting(t, "EndCallout", func() error {
		return f.co.EndCallout(f.txCtx, f.txID, c)
	})
	require.ErrorIs(t, f.co.CompleteScope(s2), spi.ErrCalloutSuperseded,
		"a scope admitted under an ended callout must not complete")

	f.sealAndCommit(t)
	f.requireExists(t, root, "the root's own write must commit")
	f.requireAbsent(t, w1, "a refused completion must discard the scope")
	f.requireAbsent(t, w2, "a refused completion must discard the scope")
}

// Contract 6. CompleteScope refuses with ErrCalloutSuperseded before
// ErrTxRollbackOnly, and with ErrTxRollbackOnly before ErrScopeConflict.
// CheckScope reports as CompleteScope would (contract 7).
func testCoordCompleteRefusalOrder(t *testing.T, h Harness, co spi.TransactionCoordinator) {
	t.Run("SupersededBeforeRollbackOnly", func(t *testing.T) {
		f := newCoordFixture(t, h, co)
		c := f.callout(t, f.txCtx)
		s1 := f.open(t, pair(c, 1, 0))
		n := f.callout(t, s1, pair(c, 1, 0))
		s2 := f.open(t, pair(n, 1, 0), pair(c, 1, 0))
		s3 := f.open(t, pair(n, 1, 0), pair(c, 1, 0))

		require.NoError(t, f.co.DiscardScope(s3, true))
		require.ErrorIs(t, f.co.CheckScope(s1), spi.ErrTxRollbackOnly,
			"discarding s3 with rollbackOnly must mark its parent s1")
		require.NoError(t, f.co.AdvanceCallout(s1, f.txID, n, 2))

		require.ErrorIs(t, f.co.CheckScope(s2), spi.ErrCalloutSuperseded,
			"CheckScope of a superseded scope under a marked parent must report superseded first")
		require.ErrorIs(t, f.co.CompleteScope(s2), spi.ErrCalloutSuperseded,
			"CompleteScope of a superseded scope under a marked parent must refuse as superseded first")
		require.NoError(t, f.tm.Rollback(f.txCtx, f.txID))
	})

	t.Run("RollbackOnlyBeforeScopeConflict", func(t *testing.T) {
		f := newCoordFixture(t, h, co)
		id := seedCoordEntity(t, f)
		c := f.callout(t, f.txCtx)
		s1 := f.open(t, pair(c, 1, 0))
		s2 := f.open(t, pair(c, 1, 0))
		e1, e2 := readCoordEntity(t, f, s1, id), readCoordEntity(t, f, s2, id)
		writeCoordEntity(t, f, s1, e1, "s1")
		writeCoordEntity(t, f, s2, e2, "s2")
		require.NoError(t, f.co.CompleteScope(s1))

		n := f.callout(t, s2, pair(c, 1, 0))
		s3 := f.open(t, pair(n, 1, 0), pair(c, 1, 0))
		require.NoError(t, f.co.DiscardScope(s3, true))
		require.NoError(t, f.co.EndCallout(s2, f.txID, n))

		require.ErrorIs(t, f.co.CompleteScope(s2), spi.ErrTxRollbackOnly,
			"a marked scope that also conflicts must refuse as rollback-only first")
		require.NoError(t, f.tm.Rollback(f.txCtx, f.txID))
	})
}

// Contract 7. DiscardScope(ctx, true) marks the parent: a scope under it
// cannot open or complete, CheckScope reports it, and with the root marked
// Seal fails.
func testCoordRollbackOnlyMarksParent(t *testing.T, h Harness, co spi.TransactionCoordinator) {
	f := newCoordFixture(t, h, co)
	c := f.callout(t, f.txCtx)
	s0 := f.open(t, pair(c, 1, 0))
	s1 := f.open(t, pair(c, 1, 0))
	f.save(t, s1)

	require.NoError(t, f.co.DiscardScope(s1, true))

	_, err := f.tryOpen(t, pair(c, 1, 0))
	require.ErrorIs(t, err, spi.ErrTxRollbackOnly,
		"a scope whose parent (the root) is marked must not open")
	require.ErrorIs(t, f.co.CheckScope(s0), spi.ErrTxRollbackOnly,
		"CheckScope must report a marked parent")
	require.ErrorIs(t, f.co.CompleteScope(s0), spi.ErrTxRollbackOnly,
		"a scope whose parent is marked must not complete")
	require.NoError(t, f.co.EndCallout(f.txCtx, f.txID, c))

	require.ErrorIs(t, f.co.Seal(f.txCtx, f.txID), spi.ErrTxRollbackOnly,
		"Seal must fail when the root is marked")
	require.NoError(t, f.tm.Rollback(f.txCtx, f.txID))
}

// Contracts 2 and 7. A mark set on a child scope goes with it when that scope
// is discarded without rollbackOnly: the root stays unmarked.
func testCoordProcessorScopeAbsorbsMark(t *testing.T, h Harness, co spi.TransactionCoordinator) {
	f := newCoordFixture(t, h, co)
	root := f.save(t, f.txCtx)
	p, err := f.co.OpenChildScope(f.txCtx)
	require.NoError(t, err)
	c := f.callout(t, p)

	s := f.open(t, pair(c, 1, 0))
	w := f.save(t, s)
	require.NoError(t, f.co.DiscardScope(s, true))
	require.ErrorIs(t, f.co.CheckScope(p), spi.ErrTxRollbackOnly,
		"discarding a callback scope with rollbackOnly must mark its parent, the child scope")
	require.NoError(t, f.co.EndCallout(p, f.txID, c))
	require.NoError(t, f.co.DiscardScope(p, false))

	require.NoError(t, f.co.Seal(f.txCtx, f.txID),
		"a mark must not climb past a child scope discarded without rollbackOnly")
	require.NoError(t, f.tm.Commit(f.txCtx, f.txID))
	f.requireExists(t, root, "the root's own write must commit")
	f.requireAbsent(t, w, "a discarded scope's write must not commit")
}

// Contract 8. After Seal, OpenScope and CompleteScope both refuse with
// ErrCalloutSuperseded, while the callout is still current. Commit then
// commits the root and every scope completed before Seal.
func testCoordSealCutsOff(t *testing.T, h Harness, co spi.TransactionCoordinator) {
	f := newCoordFixture(t, h, co)
	root := f.save(t, f.txCtx)
	c := f.callout(t, f.txCtx)

	s0 := f.open(t, pair(c, 1, 0))
	w0 := f.save(t, s0)
	require.NoError(t, f.co.CompleteScope(s0))
	s1 := f.open(t, pair(c, 1, 0))
	w1 := f.save(t, s1)

	returnsWithoutWaiting(t, "Seal", func() error { return f.co.Seal(f.txCtx, f.txID) })
	_, err := f.tryOpen(t, pair(c, 1, 0))
	require.ErrorIs(t, err, spi.ErrCalloutSuperseded, "OpenScope must refuse after Seal as superseded")
	require.ErrorIs(t, f.co.CompleteScope(s1), spi.ErrCalloutSuperseded,
		"a scope opened before Seal must not complete after it")

	require.NoError(t, f.tm.Commit(f.txCtx, f.txID))
	f.requireExists(t, root, "the root's own write must commit")
	f.requireExists(t, w0, "a scope completed before Seal must commit")
	f.requireAbsent(t, w1, "a scope refused completion after Seal must not commit")
}

// Contract 6. Two scopes read an entity at the same committed version, then
// each writes it. The first to complete wins; the second refuses with
// ErrScopeConflict and is discarded.
func testCoordScopeConflict(t *testing.T, h Harness, co spi.TransactionCoordinator) {
	f := newCoordFixture(t, h, co)
	id := seedCoordEntity(t, f)
	c := f.callout(t, f.txCtx)

	s1 := f.open(t, pair(c, 1, 0))
	s2 := f.open(t, pair(c, 1, 0))
	e1, e2 := readCoordEntity(t, f, s1, id), readCoordEntity(t, f, s2, id)
	writeCoordEntity(t, f, s1, e1, "s1")
	writeCoordEntity(t, f, s2, e2, "s2")

	require.NoError(t, f.co.CompleteScope(s1))
	require.ErrorIs(t, f.co.CompleteScope(s2), spi.ErrScopeConflict,
		"a scope that wrote an entity from the version a completed scope also wrote it from must conflict")
	require.NoError(t, f.co.EndCallout(f.txCtx, f.txID, c))

	f.sealAndCommit(t)
	got, err := f.get(t, f.ctx, id)
	require.NoError(t, err)
	require.Equal(t, "s1", entityDataString(t, got, "by"),
		"the completed scope's write must commit and the conflicting one must be discarded")
}

// seedCoordEntity commits an entity in a transaction of its own, before the
// case's transaction reads it, and returns its id.
func seedCoordEntity(t *testing.T, f *coordFixture) string {
	t.Helper()
	id := newID()
	withTx(t, f.h, f.ctx, func(txCtx spiCtx) {
		es, err := f.h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, coordModel, id, map[string]any{"by": "seed"}))
		require.NoError(t, err)
	})
	return id
}

// readCoordEntity reads the seeded entity on a scope's context; it must still
// be the seeded version.
func readCoordEntity(t *testing.T, f *coordFixture, ctx spiCtx, id string) *spi.Entity {
	t.Helper()
	e, err := f.get(t, ctx, id)
	require.NoError(t, err)
	require.Equal(t, "seed", entityDataString(t, e, "by"))
	return e
}

// writeCoordEntity saves e, as read, with its payload replaced, on a scope's
// context.
func writeCoordEntity(t *testing.T, f *coordFixture, ctx spiCtx, e *spi.Entity, by string) {
	t.Helper()
	next := *e
	next.Data = newEntity(t, coordModel, e.Meta.ID, map[string]any{"by": by}).Data
	es, err := f.h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	_, err = es.Save(ctx, &next)
	require.NoError(t, err)
}
