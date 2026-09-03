package spitest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// iterableModelRef reuses the Searcher suite's seeding model and helpers
// (searcherModel, searcherSeedOrder, seedSearcherEntities) rather than
// duplicating the decoy-interleaving pattern: every Iterable subtest that
// needs the 7-entity seed runs under a fresh tenant (see tenantContext),
// so reusing the model name across suites is collision-free.
var iterableModelRef = spi.ModelRef{EntityName: searcherModel, ModelVersion: "1"}

// runIterableSuite exercises the EntityStore.Iterate contract.
func runIterableSuite(t *testing.T, h Harness, tracker *skipTracker) {
	runSubtest(t, h, tracker, "Unordered/YieldsAllMatches", testIterableUnorderedYieldsAllMatches)
	runSubtest(t, h, tracker, "Ordered/EntityID", testIterableOrderedEntityID)
	runSubtest(t, h, tracker, "Ordered/UserFieldWithTieBreak", testIterableOrderedUserFieldWithTieBreak)
	runSubtest(t, h, tracker, "Ordered/InTxErrors", testIterableOrderedInTxErrors)
	runSubtest(t, h, tracker, "Residual/AppliedInNext", testIterableResidualAppliedInNext)
	runSubtest(t, h, tracker, "Ctx/CancelObserved", testIterableCtxCancelObserved)
	runSubtest(t, h, tracker, "Err/Sticky", testIterableErrSticky)
	runSubtest(t, h, tracker, "Close/Idempotent", testIterableCloseIdempotent)
	runSubtest(t, h, tracker, "PIT/SnapshotVariant", testIterablePITSnapshotVariant)
	runSubtest(t, h, tracker, "PIT/CommittedOnlyInTx", testIterablePITCommittedOnlyInTx)
	runSubtest(t, h, tracker, "Overlay/SnapshotAtOpen", testIterableOverlaySnapshotAtOpen)
	runSubtest(t, h, tracker, "TrackingRead/Gating", testIterableTrackingReadGating)
	runSubtest(t, h, tracker, "FilterPath/Grammar", testIterableFilterPathGrammar)
	runSubtest(t, h, tracker, "FilterNot", testIterableFilterNot)
}

// testIterableFilterPathGrammar holds Iterate to the Filter.Path grammar,
// using the same table Search is held to (searcher.go). Both are filter-taking
// entry points on the same contract, so a backend that guards one and not the
// other has left the grammar unenforced on whichever path the engine's
// streamed reads happen to take.
//
// A refusal may surface either from Iterate itself or, for a backend that
// validates lazily, from the iterator's sticky Err(). What it may NOT do is
// yield rows: an error alongside results is not a refusal, and a nil error
// with zero rows is the silently-empty-page failure this whole subtest exists
// to catch.
//
// The model is seeded first so a wrongly-accepting backend has real rows to
// yield rather than trivially reaching the end of an empty scan.
func testIterableFilterPathGrammar(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	seedIterable(t, h, ctx)

	store, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)

	runFilterPathGrammar(t, "Iterate", func(t *testing.T, filter spi.Filter) error {
		it, err := store.Iterate(ctx, iterableModelRef, filter, spi.IterateOptions{})
		if err != nil {
			return err
		}
		n := 0
		for it.Next() {
			n++
		}
		err = it.Err()
		require.NoError(t, it.Close())
		if err != nil {
			require.Zero(t, n, "an iterator that refuses a filter path must not also have yielded rows")
		}
		return err
	})
}

// drainIterator consumes it fully, collecting every yielded entity, closes
// it, and returns Err(). Used by subtests that only care about the final
// result set, not the fine-grained Next()/Err()/Close() sequencing.
func drainIterator(t *testing.T, it spi.Iterator) ([]*spi.Entity, error) {
	t.Helper()
	var out []*spi.Entity
	for it.Next() {
		out = append(out, it.Entity())
	}
	err := it.Err()
	require.NoError(t, it.Close())
	return out, err
}

// iterableStatus reads back the "status" field seedSearcherEntities stamps
// (searcherMatchValue / searcherDecoyValue).
func iterableStatus(t *testing.T, e *spi.Entity) string {
	t.Helper()
	var payload struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(e.Data, &payload))
	return payload.Status
}

// seedIterable seeds searcherSeedOrder (7 entities, decoys interleaved with
// matches — see seedSearcherEntities's doc comment) into iterableModelRef
// under a committed transaction.
func seedIterable(t *testing.T, h Harness, ctx spiCtx) {
	t.Helper()
	withTx(t, h, ctx, func(txCtx spiCtx) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		seedSearcherEntities(t, txCtx, es, searcherSeedOrder)
	})
}

// testIterableUnorderedYieldsAllMatches: a zero-value Filter with an empty
// OrderBy yields every entity for the model, in any order — the "yield all"
// half of EntityStore.Iterate's zero-value-Filter clause.
func testIterableUnorderedYieldsAllMatches(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	seedIterable(t, h, ctx)

	store, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	it, err := store.Iterate(ctx, iterableModelRef, spi.Filter{}, spi.IterateOptions{})
	require.NoError(t, err)
	got, err := drainIterator(t, it)
	require.NoError(t, err)
	require.Len(t, got, len(searcherSeedOrder), "zero-value Filter must yield every seeded entity")
}

// testIterableOrderedEntityID: OrderBy {meta,"id"} ascending must yield
// entities in h.IDOrder order — the engine's canonical entity-ID comparator,
// which Kind does not affect (see OrderSpec's doc comment).
func testIterableOrderedEntityID(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	seedIterable(t, h, ctx)

	store, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	opts := spi.IterateOptions{OrderBy: []spi.OrderSpec{{Source: spi.SourceMeta, Path: "id"}}}
	it, err := store.Iterate(ctx, iterableModelRef, spi.Filter{}, opts)
	require.NoError(t, err)
	got, err := drainIterator(t, it)
	require.NoError(t, err)
	require.Len(t, got, len(searcherSeedOrder))

	for i := 1; i < len(got); i++ {
		require.LessOrEqual(t, h.IDOrder(got[i-1].Meta.ID, got[i].Meta.ID), 0,
			"OrderBy {meta,id} must yield in h.IDOrder ascending order (got[%d]=%s after got[%d]=%s)",
			i, got[i].Meta.ID, i-1, got[i-1].Meta.ID)
	}
}

// testIterableOrderedUserFieldWithTieBreak: OrderBy on a data field with
// duplicate values must be non-decreasing on that field, and — within a run
// of equal keys — ascending in h.IDOrder, the documented tiebreak.
func testIterableOrderedUserFieldWithTieBreak(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	seedIterable(t, h, ctx)

	store, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	opts := spi.IterateOptions{OrderBy: []spi.OrderSpec{{Source: spi.SourceData, Path: "status", Kind: spi.OrderText}}}
	it, err := store.Iterate(ctx, iterableModelRef, spi.Filter{}, opts)
	require.NoError(t, err)
	got, err := drainIterator(t, it)
	require.NoError(t, err)
	require.Len(t, got, len(searcherSeedOrder))

	for i := 1; i < len(got); i++ {
		prevStatus, curStatus := iterableStatus(t, got[i-1]), iterableStatus(t, got[i])
		require.LessOrEqual(t, prevStatus, curStatus, "status must be non-decreasing")
		if prevStatus == curStatus {
			require.LessOrEqual(t, h.IDOrder(got[i-1].Meta.ID, got[i].Meta.ID), 0,
				"within equal status, entities must be h.IDOrder ascending")
		}
	}
}

// testIterableOrderedInTxErrors: a non-empty OrderBy with an ambient
// transaction is unsupported and Iterate MUST return an error rather than
// silently ignoring the order (see EntityStore.Iterate's doc comment).
func testIterableOrderedInTxErrors(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID, txCtx := beginGuarded(t, tm, ctx)
	defer func() { _ = tm.Rollback(txCtx, txID) }()

	es, err := h.Factory.EntityStore(txCtx)
	require.NoError(t, err)
	opts := spi.IterateOptions{OrderBy: []spi.OrderSpec{{Source: spi.SourceMeta, Path: "id"}}}
	_, err = es.Iterate(txCtx, iterableModelRef, spi.Filter{}, opts)
	require.Error(t, err, "a non-empty OrderBy with an ambient transaction must error")
}

// testIterableResidualAppliedInNext: a filter predicate must be fully
// applied end to end — whichever slice a plugin pushes into storage and
// whichever slice it evaluates as residual inside Next(), only matches are
// yielded. FilterNe against the seeded decoy value isolates the matches.
func testIterableResidualAppliedInNext(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	seedIterable(t, h, ctx)

	store, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	filter := spi.Filter{
		Op:       spi.FilterNe,
		Source:   spi.SourceData,
		Path:     "status",
		Value:    searcherDecoyValue,
		Declared: []spi.DataType{spi.String},
	}
	it, err := store.Iterate(ctx, iterableModelRef, filter, spi.IterateOptions{})
	require.NoError(t, err)
	got, err := drainIterator(t, it)
	require.NoError(t, err)
	require.Len(t, got, searcherMatchN)
	for _, e := range got {
		require.Equal(t, searcherMatchValue, iterableStatus(t, e))
	}
}

// testIterableCtxCancelObserved: cancelling ctx mid-iteration must surface an
// error via Err() and make Next() report done — the iterator must not ignore
// cancellation and quietly finish the full seeded set.
func testIterableCtxCancelObserved(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	seedIterable(t, h, ctx)

	cancelCtx, cancel := context.WithCancel(ctx)
	store, err := h.Factory.EntityStore(cancelCtx)
	require.NoError(t, err)
	it, err := store.Iterate(cancelCtx, iterableModelRef, spi.Filter{}, spi.IterateOptions{})
	require.NoError(t, err)
	defer func() { _ = it.Close() }()

	require.True(t, it.Next(), "at least one entity must be available before cancellation")
	cancel()

	// A backend may have buffered rows ahead of where cancellation is
	// observed; bound the drain so a non-conformant implementation that
	// never stops fails loudly instead of hanging the suite.
	for i := 0; i < len(searcherSeedOrder)+1 && it.Next(); i++ {
	}
	require.False(t, it.Next(), "Next() must report done once cancellation is observed")
	require.Error(t, it.Err(), "Err() must report the cancellation rather than nil")
}

// testIterableErrSticky verifies Err() is sticky: once an iterator has
// entered its terminal error state, repeated Next()/Err() calls must keep
// reporting the same outcome, not clear it or resurrect rows.
func testIterableErrSticky(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	seedIterable(t, h, ctx)

	cancelCtx, cancel := context.WithCancel(ctx)
	store, err := h.Factory.EntityStore(cancelCtx)
	require.NoError(t, err)
	it, err := store.Iterate(cancelCtx, iterableModelRef, spi.Filter{}, spi.IterateOptions{})
	require.NoError(t, err)
	defer func() { _ = it.Close() }()

	require.True(t, it.Next())
	cancel()
	for i := 0; i < len(searcherSeedOrder)+1 && it.Next(); i++ {
	}
	require.False(t, it.Next())
	err1 := it.Err()
	require.Error(t, err1)

	require.False(t, it.Next(), "Next() must stay false on repeated calls after the terminal error")
	require.False(t, it.Next())
	err2 := it.Err()
	require.Error(t, err2)
	require.Equal(t, err1.Error(), err2.Error(), "Err() must be sticky — the same error across repeated calls")
}

// testIterableCloseIdempotent verifies Close() is idempotent and that
// Next() reports done once the iterator has been closed.
func testIterableCloseIdempotent(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	seedIterable(t, h, ctx)

	store, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	it, err := store.Iterate(ctx, iterableModelRef, spi.Filter{}, spi.IterateOptions{})
	require.NoError(t, err)

	require.True(t, it.Next(), "at least one entity must be available mid-iteration")

	require.NoError(t, it.Close())
	require.False(t, it.Next(), "Next() after Close must report done")
	require.NoError(t, it.Close(), "a second Close must be a no-op returning nil")
}

// testIterablePITSnapshotVariant: PointInTime set to a cutoff between two
// saves must yield the pre-cutoff state, exactly like Search/GetAsAt.
func testIterablePITSnapshotVariant(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	mref := spi.ModelRef{EntityName: "iterable-pit", ModelVersion: "1"}
	id := newID()

	withTx(t, h, ctx, func(txCtx spiCtx) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, mref.EntityName, id, map[string]any{"v": 1}))
		require.NoError(t, err)
	})
	h.AdvanceClock(1 * time.Millisecond)
	asAt := h.Now().UTC()
	h.AdvanceClock(1 * time.Millisecond)

	withTx(t, h, ctx, func(txCtx spiCtx) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, mref.EntityName, id, map[string]any{"v": 2}))
		require.NoError(t, err)
	})

	store, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	it, err := store.Iterate(ctx, mref, spi.Filter{}, spi.IterateOptions{PointInTime: &asAt})
	require.NoError(t, err)
	got, err := drainIterator(t, it)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Contains(t, string(got[0].Data), `"v":1`, "PointInTime Iterate must yield the pre-cutoff state")
}

// testIterablePITCommittedOnlyInTx: a point-in-time Iterate issued INSIDE a
// transaction ignores that transaction and answers from committed state.
//
// PIT/SnapshotVariant above runs Iterate outside any transaction, so it can
// only exercise the cutoff — not the routing. The two options are independent
// and a backend can get one right and the other wrong: this is the case where
// PointInTime and an ambient transaction are set together, which the shared
// fixture arranges so the cutoff cannot mask the answer (see
// newPITCommittedOnlyFixture on why AsAt is in the future).
//
// OrderBy is left empty — ordered iteration inside a transaction is
// unsupported per EntityStore.Iterate's contract (see Ordered/InTxErrors),
// and this subtest is about visibility, not order.
func testIterablePITCommittedOnlyInTx(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	f := newPITCommittedOnlyFixture(t, h, ctx, "iterable-pit-intx")

	it, err := f.Store.Iterate(f.Ctx, f.ModelRef, spi.Filter{}, spi.IterateOptions{PointInTime: &f.AsAt})
	require.NoError(t, err)
	got, err := drainIterator(t, it)
	require.NoError(t, err)
	f.requireCommittedOnly(t, "Iterate(PointInTime)", got)
}

// testIterableOverlaySnapshotAtOpen: inside a transaction, an entity saved
// (buffered, uncommitted) before Iterate() is called must be visible in the
// merged committed+buffer overlay exactly once.
func testIterableOverlaySnapshotAtOpen(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	mref := spi.ModelRef{EntityName: "iterable-overlay", ModelVersion: "1"}

	committedID := newID()
	withTx(t, h, ctx, func(txCtx spiCtx) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, mref.EntityName, committedID, map[string]any{}))
		require.NoError(t, err)
	})

	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID, txCtx := beginGuarded(t, tm, ctx)

	es, err := h.Factory.EntityStore(txCtx)
	require.NoError(t, err)
	bufferedID := newID()
	_, err = es.Save(txCtx, newEntity(t, mref.EntityName, bufferedID, map[string]any{}))
	require.NoError(t, err)

	it, err := es.Iterate(txCtx, mref, spi.Filter{}, spi.IterateOptions{})
	require.NoError(t, err)
	got, err := drainIterator(t, it)
	require.NoError(t, err)
	require.NoError(t, tm.Rollback(txCtx, txID))

	require.Len(t, got, 2, "overlay must merge the committed entity with the tx's own buffered write")
	count := 0
	for _, e := range got {
		if e.Meta.ID == bufferedID {
			count++
		}
	}
	require.Equal(t, 1, count, "the buffered entity must be visible exactly once")
}

// testIterableTrackingReadGating verifies IterateOptions.TrackingRead gates
// read-set recording exactly like SearchOptions.TrackingRead: observed
// black-box (never via internal state) by having a second transaction commit
// a conflicting write to the yielded entity, then checking whether the
// first transaction's own commit is aborted by first-committer-wins — the
// same technique the plugin-level Search/TrackingRead tests use.
func testIterableTrackingReadGating(t *testing.T, h Harness) {
	t.Run("Enabled", func(t *testing.T) {
		iterableTrackingReadCommitOutcome(t, h, true, false)
	})
	t.Run("Disabled", func(t *testing.T) {
		iterableTrackingReadCommitOutcome(t, h, false, true)
	})
}

func iterableTrackingReadCommitOutcome(t *testing.T, h Harness, trackingRead, wantCommitSucceeds bool) {
	t.Helper()
	ctx := tenantContext(h.NewTenant())
	mref := spi.ModelRef{EntityName: "iterable-tracking", ModelVersion: "1"}
	id := newID()

	withTx(t, h, ctx, func(txCtx spiCtx) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, mref.EntityName, id, map[string]any{}))
		require.NoError(t, err)
	})

	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID, txCtx := beginGuarded(t, tm, ctx)

	esA, err := h.Factory.EntityStore(txCtx)
	require.NoError(t, err)
	it, err := esA.Iterate(txCtx, mref, spi.Filter{}, spi.IterateOptions{TrackingRead: trackingRead})
	require.NoError(t, err)
	got, err := drainIterator(t, it)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, id, got[0].Meta.ID)

	// Tx B: a concurrent, independent transaction overwrites the same
	// entity and commits before Tx A commits.
	tm2, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID2, txCtx2, err := tm2.Begin(ctx)
	require.NoError(t, err)
	esB, err := h.Factory.EntityStore(txCtx2)
	require.NoError(t, err)
	_, err = esB.Save(txCtx2, newEntity(t, mref.EntityName, id, map[string]any{"v": "conflict"}))
	require.NoError(t, err)
	require.NoError(t, tm2.Commit(txCtx2, txID2))

	err = tm.Commit(txCtx, txID)
	if wantCommitSucceeds {
		require.NoError(t, err,
			"TrackingRead=false must record nothing, so the conflicting concurrent commit must not abort this tx")
		return
	}
	require.Error(t, err, "TrackingRead=true must record the yielded id, so the conflicting concurrent commit aborts this tx")
	require.ErrorIs(t, err, spi.ErrConflict)
}
