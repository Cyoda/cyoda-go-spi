package spitest

import (
	"testing"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// trackingread.go pins WHICH ids an opt-in tracking read records into the
// transaction's read set: the ones the caller was handed, not the ones the
// backend walked to find them. Both filter-taking read entry points carry the
// same flag — IterateOptions.TrackingRead and SearchOptions.TrackingRead —
// and both are driven from the one driver here, so neither can drift from the
// other or from the contract their doc comments state.
//
// A backend that records while building its snapshot — before the filter runs
// — puts rows the caller never saw into the read set, and a concurrent commit
// touching one of those aborts a transaction that had no reason to conflict
// with it. The gating cases (Iterable/TrackingRead/Gating) cannot tell that
// shape from a correct one: they iterate a single entity with a match-all
// filter, where "scanned" and "yielded" are the same set.
//
// Observed black box, never via a backend's internal state: an independent
// transaction overwrites one of the two seeded entities and commits, and the
// tracking transaction's own commit outcome is the read-out of what its read
// recorded — the technique the GetPage read-set cases already use.

// trackingReadModel is the model the tracking-read cases seed into. Each case
// runs under a fresh tenant (see tenantContext), so one fixed name shared by
// the Iterable and Searcher suites is collision-free — the convention
// iterableModelRef and filterNotModel already follow.
const trackingReadModel = "tracking-read"

// trackingReadSearchLimit bounds the Search entry point. Search is
// bounded-or-fail (SearchOptions.Limit >= 1 is required), and the fixture
// seeds two entities, so any bound above two takes the limit out of play:
// what these cases are about is the read set, not the bound.
const trackingReadSearchLimit = 10

// trackingRead runs one entry point's read inside txCtx with its TrackingRead
// option set, and returns the ids it handed back in yield order.
type trackingRead func(t *testing.T, txCtx spiCtx, es spi.EntityStore, mref spi.ModelRef, filter spi.Filter) []string

func trackingReadViaIterate(t *testing.T, txCtx spiCtx, es spi.EntityStore, mref spi.ModelRef, filter spi.Filter) []string {
	t.Helper()
	it, err := es.Iterate(txCtx, mref, filter, spi.IterateOptions{TrackingRead: true})
	require.NoError(t, err)
	got, err := drainIterator(t, it)
	require.NoError(t, err)
	return entityIDs(got)
}

func trackingReadViaSearch(t *testing.T, txCtx spiCtx, es spi.EntityStore, mref spi.ModelRef, filter spi.Filter) []string {
	t.Helper()
	got, err := es.Search(txCtx, filter, spi.SearchOptions{
		ModelName:    mref.EntityName,
		ModelVersion: mref.ModelVersion,
		Limit:        trackingReadSearchLimit,
		TrackingRead: true,
	})
	require.NoError(t, err)
	return entityIDs(got)
}

// trackingReadPredicate is one way of selecting the same single row out of the
// two seeded entities. Two shapes are run, and the pair is the point: a
// backend that translates the predicate into storage never scans the excluded
// row at all and satisfies the contract trivially, which is correct but
// proves nothing about how it records. The negated shape is the one no
// storage translation in the reference backends accepts, so the excluded row
// is fetched and rejected in process — the layer where recording-too-early
// actually happens.
//
// If a backend gains a translation for the negated shape too, this pair stops
// discriminating on that backend and the case needs a shape its planner still
// leaves alone. It stays a correct assertion either way; it just stops being
// a sharp one.
type trackingReadPredicate struct {
	name   string
	filter spi.Filter
}

func trackingReadPredicates() []trackingReadPredicate {
	return []trackingReadPredicate{
		{
			name: "Equality",
			filter: spi.Filter{
				Op:       spi.FilterEq,
				Source:   spi.SourceData,
				Path:     "status",
				Value:    searcherMatchValue,
				Declared: []spi.DataType{spi.String},
			},
		},
		{
			name: "Negated",
			filter: spi.Filter{
				Op: spi.FilterNot,
				Children: []spi.Filter{{
					Op:       spi.FilterEq,
					Source:   spi.SourceData,
					Path:     "status",
					Value:    searcherDecoyValue,
					Declared: []spi.DataType{spi.String},
				}},
			},
		},
	}
}

// runTrackingReadYieldedOnly drives both directions of the read-set contract
// through read, under each predicate shape. Both directions are required:
// conflicting on the yielded row must abort the tracking transaction, so a
// backend that records nothing cannot pass by under-recording either.
func runTrackingReadYieldedOnly(t *testing.T, h Harness, read trackingRead) {
	t.Helper()
	for _, p := range trackingReadPredicates() {
		t.Run(p.name, func(t *testing.T) {
			t.Run("ExcludedRowIsNotRecorded", func(t *testing.T) {
				trackingReadYieldedOnlyOutcome(t, h, read, p.filter, false)
			})
			t.Run("YieldedRowIsRecorded", func(t *testing.T) {
				trackingReadYieldedOnlyOutcome(t, h, read, p.filter, true)
			})
		})
	}
}

func trackingReadYieldedOnlyOutcome(t *testing.T, h Harness, read trackingRead, filter spi.Filter, conflictOnYielded bool) {
	t.Helper()
	ctx := tenantContext(h.NewTenant())
	mref := spi.ModelRef{EntityName: trackingReadModel, ModelVersion: "1"}
	yieldedID, excludedID := newID(), newID()

	withTx(t, h, ctx, func(txCtx spiCtx) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, mref.EntityName, yieldedID, map[string]any{"status": searcherMatchValue}))
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, mref.EntityName, excludedID, map[string]any{"status": searcherDecoyValue}))
		require.NoError(t, err)
	})

	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	tx := trackingTx{tm: tm}
	tx.id, tx.ctx = beginGuarded(t, tm, ctx)

	es, err := h.Factory.EntityStore(tx.ctx)
	require.NoError(t, err)

	// Both rows must be in this transaction's view before the predicate runs.
	// Otherwise "the excluded row was not recorded" could pass because the row
	// was never there to record — the fixture would be asserting nothing. A
	// read with TrackingRead unset records nothing itself, so this cannot
	// stand in for the recording under test.
	all, err := es.Iterate(tx.ctx, mref, spi.Filter{}, spi.IterateOptions{})
	require.NoError(t, err)
	visible, err := drainIterator(t, all)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{yieldedID, excludedID}, entityIDs(visible),
		"both seeded entities must be visible to the transaction before the predicate runs")

	require.Equal(t, []string{yieldedID}, read(t, tx.ctx, es, mref, filter),
		"the predicate selects exactly one of the two seeded entities")

	conflictID := excludedID
	if conflictOnYielded {
		conflictID = yieldedID
	}
	err = commitAfterConflictingWrite(t, h, ctx, mref, conflictID, tx)
	if !conflictOnYielded {
		require.NoError(t, err,
			"the excluded entity was never handed to the caller, so it must not be in the read set: a concurrent commit touching it must not abort this tx")
		return
	}
	require.Error(t, err, "the yielded entity must be in the read set, so the conflicting concurrent commit aborts this tx")
	require.ErrorIs(t, err, spi.ErrConflict)
}

// trackingTx is the transaction under test in a read-set case: the manager it
// was begun on, its id, and its context. Grouped because the three travel
// together and two loose context arguments of the same type are easy to
// transpose at a call site.
type trackingTx struct {
	tm  spi.TransactionManager
	id  string
	ctx spiCtx
}

// commitAfterConflictingWrite is the second half of every read-set case: an
// independent transaction overwrites conflictID and commits, then tx commits.
// The returned error is that second commit's — whether it survived is the
// only black-box read-out of what the first transaction's read recorded.
//
// ctx is the plain tenant context the independent transaction begins on, NOT
// tx.ctx: the two must not share a transaction, or there is no conflict to
// observe.
func commitAfterConflictingWrite(t *testing.T, h Harness, ctx spiCtx, mref spi.ModelRef, conflictID string, tx trackingTx) error {
	t.Helper()
	tm2, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID2, txCtx2, err := tm2.Begin(ctx)
	require.NoError(t, err)
	es, err := h.Factory.EntityStore(txCtx2)
	require.NoError(t, err)
	_, err = es.Save(txCtx2, newEntity(t, mref.EntityName, conflictID, map[string]any{"v": "conflict"}))
	require.NoError(t, err)
	require.NoError(t, tm2.Commit(txCtx2, txID2))
	return tx.tm.Commit(tx.ctx, tx.id)
}
