package spitest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

func runEntitySuite(t *testing.T, h Harness, tracker *skipTracker) {
	// CRUD group (Task 4)
	runSubtest(t, h, tracker, "CreateAndGet", testEntityCreateAndGet)
	runSubtest(t, h, tracker, "Update", testEntityUpdate)
	runSubtest(t, h, tracker, "SaveAll/Ordering", testEntitySaveAllOrdering)
	runSubtest(t, h, tracker, "SaveAll/PartialFailureAtomicity", testEntitySaveAllAtomicity)
	runSubtest(t, h, tracker, "Get/NotFound", testEntityGetNotFound)
	runSubtest(t, h, tracker, "GetAll/EmptyModel", testEntityGetAllEmpty)
	runSubtest(t, h, tracker, "GetAll/Population", testEntityGetAllPopulation)
	runSubtest(t, h, tracker, "Delete", testEntityDelete)
	runSubtest(t, h, tracker, "Delete/NotFound", testEntityDeleteNotFound)
	runSubtest(t, h, tracker, "DeleteAll", testEntityDeleteAll)
	runSubtest(t, h, tracker, "Exists", testEntityExists)
	runSubtest(t, h, tracker, "Count", testEntityCount)
	runSubtest(t, h, tracker, "CountByState", testEntityCountByState)
	runSubtest(t, h, tracker, "JSONFidelity/DeepNesting", testEntityJSONFidelity)

	// Temporal group (Task 5)
	runSubtest(t, h, tracker, "GetAsAt/Historical", testEntityGetAsAtHistorical)
	runSubtest(t, h, tracker, "GetAsAt/FullMetaPopulated", testEntityGetAsAtMeta)
	runSubtest(t, h, tracker, "GetAsAt/BeforeAnyWrite", testEntityGetAsAtBefore)
	runSubtest(t, h, tracker, "GetAllAsAt", testEntityGetAllAsAt)
	runSubtest(t, h, tracker, "GetVersionMetadata/Ordering", testEntityVersionMetadataOrdering)

	// Paging + purposed history-read group (S5)
	runSubtest(t, h, tracker, "GetPage/OrderAndBounds", testEntityGetPageOrderAndBounds)
	runSubtest(t, h, tracker, "GetPage/AsAtSnapshot", testEntityGetPageAsAtSnapshot)
	runSubtest(t, h, tracker, "GetVersionByTransaction/EarliestWins", testEntityGetVersionByTransactionEarliestWins)
	runSubtest(t, h, tracker, "GetVersionByTransaction/DeletedNeverMatches", testEntityGetVersionByTransactionDeletedNeverMatches)
	runSubtest(t, h, tracker, "GetVersionByTransaction/EmptyTxID", testEntityGetVersionByTransactionEmptyTxID)

	// Concurrent / Isolation group (Task 6)
	runSubtest(t, h, tracker, "CompareAndSave/Success", testEntityCompareAndSaveSuccess)
	runSubtest(t, h, tracker, "CompareAndSave/Conflict", testEntityCompareAndSaveConflict)
	runSubtest(t, h, tracker, "Concurrent/ConflictingUpdate", testEntityConcurrentConflict)
	runSubtest(t, h, tracker, "Concurrent/DifferentEntities", testEntityConcurrentDifferent)
	runSubtest(t, h, tracker, "TenantIsolation/Get", testEntityTenantIsolationGet)
	runSubtest(t, h, tracker, "TenantIsolation/GetAll", testEntityTenantIsolationGetAll)
	runSubtest(t, h, tracker, "TenantIsolation/Delete", testEntityTenantIsolationDelete)
	runSubtest(t, h, tracker, "EmptyTenant", testEntityEmptyTenant)

	// Attribution group (follow-on-action attribution design)
	runSubtest(t, h, tracker, "Attribution/ExecutorRoundTrip", testEntityExecutorRoundTrip)
}

func testEntityCreateAndGet(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	id := newID()
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, "m-crud", id, map[string]any{"k": "v"}))
		require.NoError(t, err)
	})

	es, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	got, err := es.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, id, got.Meta.ID)
	require.Equal(t, "m-crud", got.Meta.ModelRef.EntityName)
	// State is intentionally NOT asserted: it is set by the workflow engine
	// when a model has a workflow defined. Bare saves with no workflow
	// correctly leave State empty. State semantics are validated at the
	// app level (parity suite), not at the SPI layer.
	require.False(t, got.Meta.CreationDate.IsZero(), "CreationDate meta must be populated")
	require.False(t, got.Meta.LastModifiedDate.IsZero(), "LastModifiedDate meta must be populated")
	require.NotEmpty(t, got.Meta.TransactionID, "TransactionID meta must be populated")
}

func testEntityUpdate(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	id := newID()
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		_, err := es.Save(txCtx, newEntity(t, "m-upd", id, map[string]any{"v": 1}))
		require.NoError(t, err)
	})

	h.AdvanceClock(1 * time.Millisecond)

	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		_, err := es.Save(txCtx, newEntity(t, "m-upd", id, map[string]any{"v": 2}))
		require.NoError(t, err)
	})

	es, _ := h.Factory.EntityStore(ctx)
	got, err := es.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, id, got.Meta.ID)
	require.Contains(t, string(got.Data), `"v":2`)
}

func testEntityGetNotFound(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	es, _ := h.Factory.EntityStore(ctx)
	_, err := es.Get(ctx, newID()) // valid UUID that was never written
	require.ErrorIs(t, err, spi.ErrNotFound)
}

func testEntityGetAllEmpty(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	es, _ := h.Factory.EntityStore(ctx)
	got, err := es.GetAll(ctx, spi.ModelRef{EntityName: "m-empty", ModelVersion: "1"})
	require.NoError(t, err)
	require.NotNil(t, got, "GetAll on empty model must return non-nil slice")
	require.Len(t, got, 0)
}

func testEntityGetAllPopulation(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	const n = 5
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		for i := 0; i < n; i++ {
			_, err := es.Save(txCtx, newEntity(t, "m-pop", newID(), map[string]any{"i": i}))
			require.NoError(t, err)
		}
	})

	es, _ := h.Factory.EntityStore(ctx)
	got, err := es.GetAll(ctx, spi.ModelRef{EntityName: "m-pop", ModelVersion: "1"})
	require.NoError(t, err)
	require.Len(t, got, n)
}

func testEntityDelete(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	id := newID()
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		_, err := es.Save(txCtx, newEntity(t, "m-del", id, map[string]any{}))
		require.NoError(t, err)
	})

	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		require.NoError(t, es.Delete(txCtx, id))
	})

	es, _ := h.Factory.EntityStore(ctx)
	_, err := es.Get(ctx, id)
	require.ErrorIs(t, err, spi.ErrNotFound)
}

func testEntityDeleteNotFound(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		err := es.Delete(txCtx, newID()) // valid UUID that was never created
		require.ErrorIs(t, err, spi.ErrNotFound)
	})
}

func testEntityDeleteAll(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	mref := spi.ModelRef{EntityName: "m-delall", ModelVersion: "1"}
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		for i := 0; i < 3; i++ {
			_, err := es.Save(txCtx, newEntity(t, "m-delall", newID(), map[string]any{}))
			require.NoError(t, err)
		}
	})

	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		require.NoError(t, es.DeleteAll(txCtx, mref))
	})

	es, _ := h.Factory.EntityStore(ctx)
	n, err := es.Count(ctx, mref)
	require.NoError(t, err)
	require.Equal(t, int64(0), n)
}

func testEntityExists(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	id := newID()
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		_, err := es.Save(txCtx, newEntity(t, "m-ex", id, map[string]any{}))
		require.NoError(t, err)
	})
	es, _ := h.Factory.EntityStore(ctx)
	ok, err := es.Exists(ctx, id)
	require.NoError(t, err)
	require.True(t, ok)

	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		require.NoError(t, es.Delete(txCtx, id))
	})
	ok, err = es.Exists(ctx, id)
	require.NoError(t, err)
	require.False(t, ok)
}

func testEntityCount(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	mref := spi.ModelRef{EntityName: "m-cnt", ModelVersion: "1"}
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		for i := 0; i < 7; i++ {
			_, err := es.Save(txCtx, newEntity(t, "m-cnt", newID(), map[string]any{}))
			require.NoError(t, err)
		}
	})
	es, _ := h.Factory.EntityStore(ctx)
	n, err := es.Count(ctx, mref)
	require.NoError(t, err)
	require.Equal(t, int64(7), n)
}

func testEntityCountByState(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	mref := spi.ModelRef{EntityName: "m-cbs", ModelVersion: "1"}

	// Empty model: nil filter -> empty map.
	es, _ := h.Factory.EntityStore(ctx)
	got, err := es.CountByState(ctx, mref, nil)
	require.NoError(t, err)
	require.Empty(t, got, "empty model with nil filter should return empty map")

	// Empty model: non-nil-but-empty-slice filter -> empty map (no storage call expected).
	got, err = es.CountByState(ctx, mref, []string{})
	require.NoError(t, err)
	require.Empty(t, got, "empty filter slice should return empty map")

	// Save 3 in "new", 2 in "approved", 1 in "rejected", and 1 deleted "approved" (must NOT count).
	withTx(t, h, ctx, func(txCtx context.Context) {
		esTx, _ := h.Factory.EntityStore(txCtx)
		for i := 0; i < 3; i++ {
			e := newEntity(t, "m-cbs", newID(), map[string]any{"i": i})
			e.Meta.State = "new"
			_, err := esTx.Save(txCtx, e)
			require.NoError(t, err)
		}
		for i := 0; i < 2; i++ {
			e := newEntity(t, "m-cbs", newID(), map[string]any{"i": i})
			e.Meta.State = "approved"
			_, err := esTx.Save(txCtx, e)
			require.NoError(t, err)
		}
		e := newEntity(t, "m-cbs", newID(), map[string]any{"i": 99})
		e.Meta.State = "rejected"
		_, err := esTx.Save(txCtx, e)
		require.NoError(t, err)

		toDel := newEntity(t, "m-cbs", newID(), map[string]any{"i": 100})
		toDel.Meta.State = "approved"
		_, err = esTx.Save(txCtx, toDel)
		require.NoError(t, err)
		require.NoError(t, esTx.Delete(txCtx, toDel.Meta.ID))
	})

	// nil filter -> all states (deleted excluded).
	got, err = es.CountByState(ctx, mref, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"new": 3, "approved": 2, "rejected": 1}, got)

	// Filter to "approved" only.
	got, err = es.CountByState(ctx, mref, []string{"approved"})
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"approved": 2}, got)

	// Filter including a missing state — missing omitted (not zero-valued).
	got, err = es.CountByState(ctx, mref, []string{"approved", "missing"})
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"approved": 2}, got)

	// Tenant isolation.
	otherCtx := tenantContext(h.NewTenant())
	esOther, _ := h.Factory.EntityStore(otherCtx)
	got, err = esOther.CountByState(otherCtx, mref, nil)
	require.NoError(t, err)
	require.Empty(t, got, "different tenant must not see other tenant's entities")

	// Transactional visibility.
	withTx(t, h, ctx, func(txCtx context.Context) {
		esTx, _ := h.Factory.EntityStore(txCtx)
		e := newEntity(t, "m-cbs", newID(), map[string]any{"tx": true})
		e.Meta.State = "in_review"
		_, err := esTx.Save(txCtx, e)
		require.NoError(t, err)

		got, err := esTx.CountByState(txCtx, mref, []string{"in_review"})
		require.NoError(t, err)
		require.Equal(t, map[string]int64{"in_review": 1}, got, "uncommitted tx save must be visible inside tx")
	})

	// State transition: an entity saved at one state and re-saved at another
	// in a separate transaction must count under its CURRENT state, not its
	// prior state. This catches a class of indexed-backend bugs where same-
	// commit IN/OUT pairs from a re-save can be misclassified depending on
	// scan order across value-keyed partitions.
	//
	// "approved" → "rejected" is chosen deliberately: when an indexed backend
	// partitions by the indexed value (e.g. cassandra's index_string_data
	// keyed by period_val derived from the value), period_val("approved") =
	// "ap" sorts BEFORE period_val("rejected") = "re". A scan that processes
	// the OUT row in the "approved" partition first and uses strict
	// submitTime > w.submitTime to update its winner map will fail to update
	// the winner when the IN row arrives in the "rejected" partition with the
	// SAME submit_time — silently dropping the entity from the count. Any
	// backend that survives this scenario must tie-break IN over OUT at
	// equal submit_time.
	//
	// Backends that read state directly from the current entity row
	// (memory/sqlite/postgres in cyoda-go) pass this naturally because they
	// never see historical IN/OUT markers; the test still locks the contract
	// for them so a future indexed implementation cannot regress.
	// The payload also embeds {"_meta": {"state": ...}} alongside Meta.State
	// because some backends (notably the cassandra plugin's lifecycle indexer
	// at AddLifecycleIndexEntries) read the prior state from the prevData
	// payload, not from Meta.State. Without an embedded _meta.state in the
	// PRIOR payload, those backends see oldState="" on re-save and emit only
	// IN(newState) — the bug's IN+OUT-at-same-submit-time pattern never
	// arises and the bug is not exercised. Backends that read state directly
	// from the entity row (memory/sqlite/postgres) ignore the payload _meta
	// and pass naturally.
	transitionID := newID()
	withTx(t, h, ctx, func(txCtx context.Context) {
		esTx, _ := h.Factory.EntityStore(txCtx)
		e := newEntity(t, "m-cbs", transitionID, map[string]any{
			"v":     1,
			"_meta": map[string]any{"state": "approved"},
		})
		e.Meta.State = "approved"
		_, err := esTx.Save(txCtx, e)
		require.NoError(t, err)
	})
	withTx(t, h, ctx, func(txCtx context.Context) {
		esTx, _ := h.Factory.EntityStore(txCtx)
		e := newEntity(t, "m-cbs", transitionID, map[string]any{
			"v":     2,
			"_meta": map[string]any{"state": "rejected"},
		})
		e.Meta.State = "rejected"
		_, err := esTx.Save(txCtx, e)
		require.NoError(t, err)
	})

	// After the transition, totals should be:
	//   new: 3 (unchanged from initial setup)
	//   approved: 2 (unchanged — transitionID was saved at approved then moved away)
	//   rejected: 2 (was 1 before; +1 for the transitioned entity)
	//   in_review: 1 (from the prior transactional-visibility section, which committed)
	got, err = es.CountByState(ctx, mref, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"new": 3, "approved": 2, "rejected": 2, "in_review": 1}, got,
		"after state transition, entity must count under post-transition state")
}

func testEntitySaveAllOrdering(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	mref := spi.ModelRef{EntityName: "m-sa", ModelVersion: "1"}
	ents := []*spi.Entity{
		newEntity(t, "m-sa", newID(), map[string]any{"i": 0}),
		newEntity(t, "m-sa", newID(), map[string]any{"i": 1}),
		newEntity(t, "m-sa", newID(), map[string]any{"i": 2}),
	}
	var versions []int64
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		v, err := es.SaveAll(txCtx, iterSeq(ents))
		require.NoError(t, err)
		versions = v
	})
	require.Len(t, versions, 3)

	es, _ := h.Factory.EntityStore(ctx)
	n, _ := es.Count(ctx, mref)
	require.Equal(t, int64(3), n)
}

func testEntitySaveAllAtomicity(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	mref := spi.ModelRef{EntityName: "m-saa", ModelVersion: "1"}
	tm, _ := h.Factory.TransactionManager(ctx)
	txID, txCtx, err := tm.Begin(ctx)
	require.NoError(t, err)
	es, _ := h.Factory.EntityStore(txCtx)
	_, err = es.SaveAll(txCtx, iterSeq([]*spi.Entity{
		newEntity(t, "m-saa", newID(), map[string]any{}),
		newEntity(t, "m-saa", newID(), map[string]any{}),
	}))
	require.NoError(t, err)
	// Use txCtx (not ctx) so backends that embed tx-state in context (e.g.
	// Cassandra) can locate the transaction on Rollback.
	require.NoError(t, tm.Rollback(txCtx, txID))

	esOut, _ := h.Factory.EntityStore(ctx)
	n, _ := esOut.Count(ctx, mref)
	require.Equal(t, int64(0), n, "no SaveAll entities visible after rollback")
}

func testEntityJSONFidelity(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	id := newID()
	payload := map[string]any{
		"nested": map[string]any{
			"arr":     []any{1.0, 2.0, nil, "three", map[string]any{"k": "v"}},
			"unicode": "λ κόσμε 🌍",
			"null":    nil,
			"deep":    map[string]any{"d1": map[string]any{"d2": map[string]any{"d3": "bottom"}}},
		},
	}
	// Note: json.Unmarshal decodes JSON numbers as float64. The payload
	// above intentionally uses values that round-trip safely through
	// float64. Larger integers or precision-sensitive values would need
	// json.Number decoding for a reliable equality check.
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		_, err := es.Save(txCtx, newEntity(t, "m-json", id, payload))
		require.NoError(t, err)
	})
	es, _ := h.Factory.EntityStore(ctx)
	got, err := es.Get(ctx, id)
	require.NoError(t, err)
	var roundTripped map[string]any
	require.NoError(t, json.Unmarshal(got.Data, &roundTripped))
	require.Equal(t, payload, roundTripped, "deep JSON payload must round-trip")
}

func testEntityGetAsAtHistorical(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	id := newID()
	// Write v=1, advance, capture tBetween12, advance, write v=2, advance.
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		_, err := es.Save(txCtx, newEntity(t, "m-asat", id, map[string]any{"v": 1}))
		require.NoError(t, err)
	})
	h.AdvanceClock(1 * time.Millisecond)
	tBetween12 := h.Now().UTC()
	h.AdvanceClock(1 * time.Millisecond)

	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		_, err := es.Save(txCtx, newEntity(t, "m-asat", id, map[string]any{"v": 2}))
		require.NoError(t, err)
	})
	h.AdvanceClock(1 * time.Millisecond)

	es, _ := h.Factory.EntityStore(ctx)
	got, err := es.GetAsAt(ctx, id, tBetween12)
	require.NoError(t, err)
	require.Contains(t, string(got.Data), `"v":1`, "GetAsAt(tBetween12) must return v=1")
}

func testEntityGetAsAtMeta(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	id := newID()
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		_, err := es.Save(txCtx, newEntity(t, "m-meta", id, map[string]any{}))
		require.NoError(t, err)
	})
	h.AdvanceClock(1 * time.Millisecond)
	asAt := h.Now().UTC()
	h.AdvanceClock(1 * time.Millisecond)

	es, _ := h.Factory.EntityStore(ctx)
	got, err := es.GetAsAt(ctx, id, asAt)
	require.NoError(t, err)
	// State intentionally not asserted (see testEntityCreateAndGet).
	require.False(t, got.Meta.CreationDate.IsZero(), "GetAsAt must populate CreationDate")
	require.False(t, got.Meta.LastModifiedDate.IsZero(), "GetAsAt must populate LastModifiedDate")
	require.NotEmpty(t, got.Meta.TransactionID, "GetAsAt must populate TransactionID")
	require.Equal(t, id, got.Meta.ID)
}

func testEntityGetAsAtBefore(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	past := h.Now().UTC().Add(-1 * time.Hour)
	es, _ := h.Factory.EntityStore(ctx)
	_, err := es.GetAsAt(ctx, newID(), past) // valid UUID, never written
	require.ErrorIs(t, err, spi.ErrNotFound)
}

func testEntityGetAllAsAt(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	mref := spi.ModelRef{EntityName: "m-allasat", ModelVersion: "1"}
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		for i := 0; i < 3; i++ {
			_, err := es.Save(txCtx, newEntity(t, "m-allasat", newID(), map[string]any{"i": i}))
			require.NoError(t, err)
		}
	})
	h.AdvanceClock(1 * time.Millisecond)
	asAt := h.Now().UTC()
	h.AdvanceClock(1 * time.Millisecond)

	// Fourth entity written AFTER asAt — must not be returned.
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		_, err := es.Save(txCtx, newEntity(t, "m-allasat", newID(), map[string]any{"i": 99}))
		require.NoError(t, err)
	})

	es, _ := h.Factory.EntityStore(ctx)
	got, err := es.GetAllAsAt(ctx, mref, asAt)
	require.NoError(t, err)
	require.Len(t, got, 3, "GetAllAsAt must exclude writes after asAt")
}

// testEntityVersionMetadataOrdering seeds 3 saves + 1 delete and asserts
// GetVersionMetadata returns all 4 rows newest-first (tie-break Version
// DESC), with Deleted true only on the tombstone and Version populated on
// every row, including the tombstone.
func testEntityVersionMetadataOrdering(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	id := newID()
	for i := 0; i < 3; i++ {
		withTx(t, h, ctx, func(txCtx context.Context) {
			es, _ := h.Factory.EntityStore(txCtx)
			_, err := es.Save(txCtx, newEntity(t, "m-hist", id, map[string]any{"v": i}))
			require.NoError(t, err)
		})
		h.AdvanceClock(1 * time.Millisecond)
	}
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		require.NoError(t, es.Delete(txCtx, id))
	})
	es, _ := h.Factory.EntityStore(ctx)
	metas, err := es.GetVersionMetadata(ctx, id, spi.VersionMetadataOptions{})
	require.NoError(t, err)
	require.Len(t, metas, 4, "3 saves + 1 delete tombstone")

	for i := 1; i < len(metas); i++ {
		require.False(t, metas[i].Timestamp.After(metas[i-1].Timestamp),
			"metas must be newest-first by Timestamp (meta %d after meta %d)", i, i-1)
		if metas[i].Timestamp.Equal(metas[i-1].Timestamp) {
			require.Greater(t, metas[i-1].Version, metas[i].Version,
				"equal-timestamp metas must tie-break Version DESC")
		}
	}

	require.True(t, metas[0].Deleted, "the newest meta (index 0) must be the DELETE tombstone")
	for i := 1; i < len(metas); i++ {
		require.False(t, metas[i].Deleted, "no version before the tombstone may be marked Deleted")
	}

	for i, m := range metas {
		require.NotZero(t, m.Version, "meta %d must have Version populated", i)
	}
}

// testEntityGetPageOrderAndBounds seeds 5 entities and checks that
// GetPage(0,4) ≡ GetPage(0,2) ++ GetPage(2,2) under h.IDOrder, that the page
// itself is h.IDOrder-ascending, and that limit<1 / offset<0 are contract
// violations.
func testEntityGetPageOrderAndBounds(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	mref := spi.ModelRef{EntityName: "m-page", ModelVersion: "1"}
	const n = 5
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		for i := 0; i < n; i++ {
			_, err := es.Save(txCtx, newEntity(t, "m-page", newID(), map[string]any{"i": i}))
			require.NoError(t, err)
		}
	})

	es, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)

	full, err := es.GetPage(ctx, mref, 4, 0, nil)
	require.NoError(t, err)
	require.Len(t, full, 4, "GetPage(limit=4,offset=0) must return 4 of the 5 seeded entities")
	for i := 1; i < len(full); i++ {
		require.LessOrEqual(t, h.IDOrder(full[i-1].Meta.ID, full[i].Meta.ID), 0,
			"GetPage must yield entities in h.IDOrder ascending order")
	}

	first, err := es.GetPage(ctx, mref, 2, 0, nil)
	require.NoError(t, err)
	require.Len(t, first, 2)

	second, err := es.GetPage(ctx, mref, 2, 2, nil)
	require.NoError(t, err)
	require.Len(t, second, 2)

	require.Equal(t, []string{full[0].Meta.ID, full[1].Meta.ID},
		[]string{first[0].Meta.ID, first[1].Meta.ID},
		"page 0-2 must match the first 2 entities of page 0-4")
	require.Equal(t, []string{full[2].Meta.ID, full[3].Meta.ID},
		[]string{second[0].Meta.ID, second[1].Meta.ID},
		"page 2-2 must match the last 2 entities of page 0-4")

	_, err = es.GetPage(ctx, mref, 0, 0, nil)
	require.Error(t, err, "limit 0 must be a contract violation")

	_, err = es.GetPage(ctx, mref, 1, -1, nil)
	require.Error(t, err, "negative offset must be a contract violation")
}

// testEntityGetPageAsAtSnapshot verifies GetPage's asAt parameter reads
// committed-only state as of the given instant, excluding writes after it —
// mirroring GetAllAsAt's contract.
func testEntityGetPageAsAtSnapshot(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	mref := spi.ModelRef{EntityName: "m-page-asat", ModelVersion: "1"}
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		for i := 0; i < 3; i++ {
			_, err := es.Save(txCtx, newEntity(t, "m-page-asat", newID(), map[string]any{"i": i}))
			require.NoError(t, err)
		}
	})
	h.AdvanceClock(1 * time.Millisecond)
	asAt := h.Now().UTC()
	h.AdvanceClock(1 * time.Millisecond)

	// Fifth entity written AFTER asAt — must not be returned.
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, "m-page-asat", newID(), map[string]any{"i": 99}))
		require.NoError(t, err)
	})

	es, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	got, err := es.GetPage(ctx, mref, 10, 0, &asAt)
	require.NoError(t, err)
	require.Len(t, got, 3, "GetPage with asAt must exclude writes after the cutoff")
}

// testEntityGetVersionByTransactionEarliestWins verifies that when one
// transaction saves the same entity twice before committing, the earlier
// (lower-Version) save is returned for that shared txID — not the latest,
// which is what a naive "most recent row for this txID" implementation
// would return.
func testEntityGetVersionByTransactionEarliestWins(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	id := newID()
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, "m-gvbt", id, map[string]any{"v": 1}))
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, "m-gvbt", id, map[string]any{"v": 2}))
		require.NoError(t, err)
	})

	es, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	got, err := es.Get(ctx, id)
	require.NoError(t, err)
	txID := got.Meta.TransactionID
	require.NotEmpty(t, txID)

	v, err := es.GetVersionByTransaction(ctx, id, txID)
	require.NoError(t, err)
	require.NotNil(t, v)
	require.NotNil(t, v.Entity)
	require.Contains(t, string(v.Entity.Data), `"v":1`,
		"same-tx double-save: GetVersionByTransaction must return the earlier version, not the latest")
}

// testEntityGetVersionByTransactionDeletedNeverMatches verifies that a
// DELETED tombstone never matches GetVersionByTransaction, even queried by
// its own deleting transaction's ID: this method surfaces entity content,
// and a tombstone has none.
func testEntityGetVersionByTransactionDeletedNeverMatches(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	id := newID()
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, "m-gvbt-del", id, map[string]any{}))
		require.NoError(t, err)
	})

	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		require.NoError(t, es.Delete(txCtx, id))
	})

	es, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	// Recover the deleting transaction's ID from the tombstone's metadata —
	// Get(id) would just return ErrNotFound, since the entity is gone.
	metas, err := es.GetVersionMetadata(ctx, id, spi.VersionMetadataOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, metas)
	deleteTxID := metas[0].TransactionID
	require.NotEmpty(t, deleteTxID, "tombstone's TransactionID must be populated to drive this test")

	_, err = es.GetVersionByTransaction(ctx, id, deleteTxID)
	require.ErrorIs(t, err, spi.ErrNotFound,
		"GetVersionByTransaction must never match a DELETED tombstone, even by its own deleting txID")
}

// testEntityGetVersionByTransactionEmptyTxID verifies that an empty txID
// never matches: it must always return ErrNotFound rather than being
// treated as a wildcard or matching a stored-empty TransactionID.
func testEntityGetVersionByTransactionEmptyTxID(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	id := newID()
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, "m-gvbt-empty", id, map[string]any{}))
		require.NoError(t, err)
	})

	es, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	_, err = es.GetVersionByTransaction(ctx, id, "")
	require.ErrorIs(t, err, spi.ErrNotFound,
		"empty txID must never match; it must always return ErrNotFound")
}

func testEntityCompareAndSaveSuccess(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	id := newID()
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		_, err := es.Save(txCtx, newEntity(t, "m-cas", id, map[string]any{"v": 1}))
		require.NoError(t, err)
	})

	es, _ := h.Factory.EntityStore(ctx)
	got, err := es.Get(ctx, id)
	require.NoError(t, err)
	firstTxID := got.Meta.TransactionID

	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		_, err := es.CompareAndSave(txCtx, newEntity(t, "m-cas", id, map[string]any{"v": 2}), firstTxID)
		require.NoError(t, err)
	})

	got, err = es.Get(ctx, id)
	require.NoError(t, err)
	require.Contains(t, string(got.Data), `"v":2`)
}

func testEntityCompareAndSaveConflict(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	id := newID()
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		_, err := es.Save(txCtx, newEntity(t, "m-cas", id, map[string]any{}))
		require.NoError(t, err)
	})

	tm, _ := h.Factory.TransactionManager(ctx)
	txID, txCtx, err := tm.Begin(ctx)
	require.NoError(t, err)
	// Use txCtx so backends that embed tx-state in context (e.g. Cassandra)
	// can locate the transaction on Rollback.
	defer func() { _ = tm.Rollback(txCtx, txID) }()
	es, _ := h.Factory.EntityStore(txCtx)
	_, err = es.CompareAndSave(txCtx, newEntity(t, "m-cas", id, map[string]any{}), "stale-tx-id")
	require.ErrorIs(t, err, spi.ErrConflict, "CompareAndSave with stale expectedTxID must return ErrConflict")
}

func testEntityConcurrentConflict(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	id := newID()
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		_, err := es.Save(txCtx, newEntity(t, "m-cc", id, map[string]any{"v": 0}))
		require.NoError(t, err)
	})
	es0, _ := h.Factory.EntityStore(ctx)
	got, _ := es0.Get(ctx, id)
	baseTxID := got.Meta.TransactionID

	errs := make(chan error, 2)
	run := func(v int) {
		tm, e := h.Factory.TransactionManager(ctx)
		if e != nil {
			errs <- e
			return
		}
		txID, txCtx, e := tm.Begin(ctx)
		if e != nil {
			errs <- e
			return
		}
		es, _ := h.Factory.EntityStore(txCtx)
		_, e = es.CompareAndSave(txCtx, newEntity(t, "m-cc", id, map[string]any{"v": v}), baseTxID)
		if e != nil {
			// Use txCtx so backends that embed tx-state in context can
			// locate the transaction on Rollback (e.g. Cassandra).
			_ = tm.Rollback(txCtx, txID)
			errs <- e
			return
		}
		// Use txCtx so backends that embed tx-state in context can
		// locate the transaction on Commit (e.g. Cassandra).
		errs <- tm.Commit(txCtx, txID)
	}
	go run(1)
	go run(2)
	results := []error{<-errs, <-errs}

	var winners, conflicts int
	for _, e := range results {
		switch {
		case e == nil:
			winners++
		case errors.Is(e, spi.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", e)
		}
	}
	require.Equal(t, 1, winners, "exactly one winner")
	require.Equal(t, 1, conflicts, "exactly one ErrConflict")
}

func testEntityConcurrentDifferent(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	mref := spi.ModelRef{EntityName: "m-cd", ModelVersion: "1"}
	const n = 8
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			id := newID()
			tm, e := h.Factory.TransactionManager(ctx)
			if e != nil {
				errs <- e
				return
			}
			txID, txCtx, e := tm.Begin(ctx)
			if e != nil {
				errs <- e
				return
			}
			es, _ := h.Factory.EntityStore(txCtx)
			_, e = es.Save(txCtx, newEntity(t, "m-cd", id, map[string]any{"i": i}))
			if e != nil {
				// Use txCtx so backends that embed tx-state in context can
				// locate the transaction on Rollback (e.g. Cassandra).
				_ = tm.Rollback(txCtx, txID)
				errs <- e
				return
			}
			// Use txCtx so backends that embed tx-state in context can
			// locate the transaction on Commit (e.g. Cassandra).
			errs <- tm.Commit(txCtx, txID)
		}(i)
	}
	for i := 0; i < n; i++ {
		require.NoError(t, <-errs)
	}
	es, _ := h.Factory.EntityStore(ctx)
	count, err := es.Count(ctx, mref)
	require.NoError(t, err)
	require.Equal(t, int64(n), count)
}

func testEntityTenantIsolationGet(t *testing.T, h Harness) {
	tA := h.NewTenant()
	tB := h.NewTenant()
	ctxA, ctxB := tenantContext(tA), tenantContext(tB)
	id := newID()

	withTx(t, h, ctxA, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		_, err := es.Save(txCtx, newEntity(t, "m-ti", id, map[string]any{"t": "A"}))
		require.NoError(t, err)
	})

	esB, _ := h.Factory.EntityStore(ctxB)
	_, err := esB.Get(ctxB, id)
	require.ErrorIs(t, err, spi.ErrNotFound, "cross-tenant Get must return ErrNotFound")
}

func testEntityTenantIsolationGetAll(t *testing.T, h Harness) {
	tA, tB := h.NewTenant(), h.NewTenant()
	ctxA, ctxB := tenantContext(tA), tenantContext(tB)
	mref := spi.ModelRef{EntityName: "m-tigetall", ModelVersion: "1"}

	withTx(t, h, ctxA, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		_, err := es.Save(txCtx, newEntity(t, "m-tigetall", newID(), map[string]any{}))
		require.NoError(t, err)
	})

	esB, _ := h.Factory.EntityStore(ctxB)
	got, err := esB.GetAll(ctxB, mref)
	require.NoError(t, err)
	require.Len(t, got, 0, "tenant B must not see tenant A's writes")
}

func testEntityTenantIsolationDelete(t *testing.T, h Harness) {
	tA, tB := h.NewTenant(), h.NewTenant()
	ctxA, ctxB := tenantContext(tA), tenantContext(tB)
	id := newID()

	withTx(t, h, ctxA, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		_, err := es.Save(txCtx, newEntity(t, "m-tidel", id, map[string]any{}))
		require.NoError(t, err)
	})

	tmB, _ := h.Factory.TransactionManager(ctxB)
	txIDB, txCtxB, err := tmB.Begin(ctxB)
	require.NoError(t, err)
	// Always roll back the test tx — even if Delete returns ErrNotFound
	// (which is the expected outcome), the tx is still open and must be
	// cleaned up. Use txCtxB so backends that embed tx-state in the
	// context (e.g. Cassandra) can locate the transaction.
	defer func() { _ = tmB.Rollback(txCtxB, txIDB) }()
	esB, _ := h.Factory.EntityStore(txCtxB)
	err = esB.Delete(txCtxB, id)
	require.ErrorIs(t, err, spi.ErrNotFound, "cross-tenant Delete must return ErrNotFound")
}

// testEntityExecutorRoundTrip verifies that the ChangeUser/ChangeUserKind/
// ChangeExecutor attribution fields a caller stamps on Entity.Meta before
// Save round-trip through GetVersionMetadata as EntityVersionMeta.User/
// AttributedKind/Executor — including for a DELETED version. Unlike the
// deleted EntityVersion.Entity field on some backends, EntityVersionMeta
// carries no entity payload at all, so there is nothing to dereference:
// User/AttributedKind/Executor are populated directly on every row.
func testEntityExecutorRoundTrip(t *testing.T, h Harness) {
	tenant := h.NewTenant()
	ctx := tenantContext(tenant)
	id := newID()
	createdExecutor := spi.Principal{ID: "svc-1", Kind: spi.PrincipalService}

	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = es.Save(txCtx, newAttributedEntity(t, "m-exec", id, map[string]any{"v": 1},
			"origin-user", spi.PrincipalUser, createdExecutor))
		require.NoError(t, err)
	})
	h.AdvanceClock(1 * time.Millisecond)

	wantDeleteExecutor := spi.Principal{ID: "del-user", Kind: spi.PrincipalUser}
	deleterCtx := tenantContextAs(tenant, wantDeleteExecutor.ID, wantDeleteExecutor.Kind)
	withTx(t, h, deleterCtx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		require.NoError(t, es.Delete(txCtx, id))
	})

	es, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	metas, err := es.GetVersionMetadata(ctx, id, spi.VersionMetadataOptions{})
	require.NoError(t, err)
	require.Len(t, metas, 2, "one CREATE version + one DELETE tombstone")

	// Newest-first: metas[0] is the DELETE tombstone, metas[1] is the CREATE.
	deletedMeta := metas[0]
	createdMeta := metas[1]

	require.Equal(t, "origin-user", createdMeta.User,
		"CREATE version's User must equal Meta.ChangeUser as written at Save")
	require.Equal(t, spi.PrincipalUser, createdMeta.AttributedKind,
		"CREATE version's AttributedKind must equal Meta.ChangeUserKind as written at Save")
	require.Equal(t, createdExecutor, createdMeta.Executor,
		"CREATE version's Executor must equal Meta.ChangeExecutor as written at Save")

	require.True(t, deletedMeta.Deleted, "the newest version must be the DELETE tombstone")
	require.Equal(t, wantDeleteExecutor.ID, deletedMeta.User,
		"a DELETED version's User (attributed) must equal the deleting caller's origin identity")
	require.Equal(t, wantDeleteExecutor.Kind, deletedMeta.AttributedKind,
		"a DELETED version's AttributedKind must likewise round-trip")
	require.Equal(t, wantDeleteExecutor, deletedMeta.Executor,
		"a DELETED version's Executor must be readable directly — EntityVersionMeta has no Entity field to dereference")
}

func testEntityEmptyTenant(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	mref := spi.ModelRef{EntityName: "m-empty", ModelVersion: "1"}
	es, _ := h.Factory.EntityStore(ctx)
	got, err := es.GetAll(ctx, mref)
	require.NoError(t, err)
	// Note: testEntityGetAllEmpty asserts non-nil; this subtest tests the
	// broader EmptyTenant invariant (Count == 0). If the memory plugin
	// returns nil from GetAll, this still works because len(nil) == 0.
	require.Len(t, got, 0)
	n, err := es.Count(ctx, mref)
	require.NoError(t, err)
	require.Equal(t, int64(0), n)
}
