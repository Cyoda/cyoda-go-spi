package spitest

import (
	"testing"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// runGroupedAggregatorSuite exercises the optional spi.GroupedAggregator
// contract. Gated on a type assertion (not a Skip key) because the interface
// is optional and a never-matching Skip key fails the run.
func runGroupedAggregatorSuite(t *testing.T, h Harness, tracker *skipTracker) {
	ctx := tenantContext(h.NewTenant())
	store, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	if _, ok := store.(spi.GroupedAggregator); !ok {
		t.Skip("EntityStore does not implement spi.GroupedAggregator (optional interface)")
	}
	runSubtest(t, h, tracker, "InTxRecordsNothing", testGroupedAggregatorInTxRecordsNothing)
}

// testGroupedAggregatorInTxRecordsNothing has two arms:
//
//   - The main arm: in-transaction grouped aggregation records nothing into
//     the read-set — a concurrent commit to an aggregated entity must not
//     abort the aggregating transaction. (The engine requests no tracking for
//     stats; a backend that recorded the whole model would abort every
//     in-transaction stats caller on any concurrent write.)
//   - A positive control, run after the main arm on a fresh transaction C:
//     a GetPage read of the same entity (which the SPI documents as recording
//     unconditionally in-transaction — see GetPage/InTxRecordsReadSet in
//     entity.go) followed by the same concurrent-overwrite-and-commit must
//     abort C. Without this control, a backend whose commit-time conflict
//     check short-circuits whenever the committing transaction made no
//     writes would pass the main arm for the wrong reason — not because it
//     recorded no read-set, but because it never checks read-sets on a
//     read-only commit at all.
func testGroupedAggregatorInTxRecordsNothing(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	mref := spi.ModelRef{EntityName: "m-ga-tx", ModelVersion: "1"}
	id := newID()
	withTx(t, h, ctx, func(txCtx spiCtx) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		e := newEntity(t, mref.EntityName, id, map[string]any{})
		e.Meta.State = "open"
		_, err = es.Save(txCtx, e)
		require.NoError(t, err)
	})

	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID, txCtx := beginGuarded(t, tm, ctx)
	esA, err := h.Factory.EntityStore(txCtx)
	require.NoError(t, err)
	aggregator, ok := esA.(spi.GroupedAggregator)
	require.True(t, ok, "tx-scoped EntityStore must implement spi.GroupedAggregator when the plain store does")
	_, err = aggregator.GroupedAggregate(txCtx, mref,
		[]spi.GroupExpr{{Kind: spi.GroupExprState}}, spi.Filter{},
		spi.GroupedAggregationsOptions{MaxBuckets: 10})
	require.NoError(t, err)

	// A concurrent transaction overwrites the aggregated entity and commits.
	tm2, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID2, txCtx2, err := tm2.Begin(ctx)
	require.NoError(t, err)
	esB, err := h.Factory.EntityStore(txCtx2)
	require.NoError(t, err)
	_, err = esB.Save(txCtx2, newEntity(t, mref.EntityName, id, map[string]any{"v": "conflict"}))
	require.NoError(t, err)
	require.NoError(t, tm2.Commit(txCtx2, txID2))

	require.NoError(t, tm.Commit(txCtx, txID),
		"grouped aggregation must record nothing, so the concurrent commit must not abort this transaction")

	// Positive control: a recorded read of the same entity, in an otherwise
	// identical shape, must abort on the same concurrent-write pattern. This
	// proves the backend's commit-time conflict check actually validates
	// read-only transactions, so the NoError above is evidence that grouped
	// aggregation records nothing rather than evidence that the check never
	// runs.
	txIDC, txCtxC := beginGuarded(t, tm, ctx)
	esC, err := h.Factory.EntityStore(txCtxC)
	require.NoError(t, err)
	_, err = esC.GetPage(txCtxC, mref, 10, 0, nil) // asAt == nil: unconditional read-set recording
	require.NoError(t, err)

	tm3, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID3, txCtx3, err := tm3.Begin(ctx)
	require.NoError(t, err)
	esD, err := h.Factory.EntityStore(txCtx3)
	require.NoError(t, err)
	_, err = esD.Save(txCtx3, newEntity(t, mref.EntityName, id, map[string]any{"v": "conflict-2"}))
	require.NoError(t, err)
	require.NoError(t, tm3.Commit(txCtx3, txID3))

	err = tm.Commit(txCtxC, txIDC)
	require.Error(t, err,
		"control: a recorded read of the same entity must abort — proves the backend validates read-only commits, so the grouped-aggregation arm above passed for the right reason")
	require.ErrorIs(t, err, spi.ErrConflict)
}
