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

// testGroupedAggregatorInTxRecordsNothing: in-transaction grouped
// aggregation records nothing into the read-set — a concurrent commit to an
// aggregated entity must not abort the aggregating transaction. (The engine
// requests no tracking for stats; a backend that recorded the whole model
// would abort every in-transaction stats caller on any concurrent write.)
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
	_, err = esA.(spi.GroupedAggregator).GroupedAggregate(txCtx, mref,
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
}
