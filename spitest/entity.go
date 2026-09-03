package spitest

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
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
	runSubtest(t, h, tracker, "Delete", testEntityDelete)
	runSubtest(t, h, tracker, "Delete/NotFound", testEntityDeleteNotFound)
	runSubtest(t, h, tracker, "DeleteAll", testEntityDeleteAll)
	runSubtest(t, h, tracker, "Exists", testEntityExists)
	runSubtest(t, h, tracker, "Count", testEntityCount)
	runSubtest(t, h, tracker, "CountByState", testEntityCountByState)
	runSubtest(t, h, tracker, "Count/InTxBufferShapes", testEntityCountInTxBufferShapes)
	runSubtest(t, h, tracker, "JSONFidelity/DeepNesting", testEntityJSONFidelity)

	// Temporal group (Task 5)
	runSubtest(t, h, tracker, "GetAsAt/Historical", testEntityGetAsAtHistorical)
	runSubtest(t, h, tracker, "GetAsAt/FullMetaPopulated", testEntityGetAsAtMeta)
	runSubtest(t, h, tracker, "GetAsAt/BeforeAnyWrite", testEntityGetAsAtBefore)
	runSubtest(t, h, tracker, "GetAsAt/CommittedOnlyInTx", testEntityGetAsAtCommittedOnlyInTx)
	runSubtest(t, h, tracker, "GetVersionMetadata/Ordering", testEntityVersionMetadataOrdering)
	runSubtest(t, h, tracker, "GetVersionMetadata/EmptyWindowIsNotAnError", testEntityGetVersionMetadataEmptyWindowIsNotAnError)
	runSubtest(t, h, tracker, "GetVersionMetadata/LimitCaps", testEntityGetVersionMetadataLimitCaps)
	runSubtest(t, h, tracker, "GetVersionMetadata/UntilBound", testEntityGetVersionMetadataUntilBound)

	// Paging + purposed history-read group (S5)
	runSubtest(t, h, tracker, "GetPage/OrderAndBounds", testEntityGetPageOrderAndBounds)
	runSubtest(t, h, tracker, "GetPage/AsAtSnapshot", testEntityGetPageAsAtSnapshot)
	runSubtest(t, h, tracker, "GetPage/AsAtCommittedOnlyInTx", testEntityGetPageAsAtCommittedOnlyInTx)
	runSubtest(t, h, tracker, "GetPage/InTxWithStagedDeletes", testEntityGetPageInTxWithStagedDeletes)
	runSubtest(t, h, tracker, "GetPage/InTxRecordsReadSet", testEntityGetPageInTxRecordsReadSet)
	runSubtest(t, h, tracker, "GetVersionByTransaction/EarliestWins", testEntityGetVersionByTransactionEarliestWins)
	runSubtest(t, h, tracker, "GetVersionByTransaction/DeletedNeverMatches", testEntityGetVersionByTransactionDeletedNeverMatches)
	runSubtest(t, h, tracker, "GetVersionByTransaction/EmptyTxID", testEntityGetVersionByTransactionEmptyTxID)
	runSubtest(t, h, tracker, "GetVersionByTransaction/UnrelatedTxID", testEntityGetVersionByTransactionUnrelatedTxID)

	// Concurrent / Isolation group (Task 6)
	runSubtest(t, h, tracker, "CompareAndSave/Success", testEntityCompareAndSaveSuccess)
	runSubtest(t, h, tracker, "CompareAndSave/Conflict", testEntityCompareAndSaveConflict)
	runSubtest(t, h, tracker, "CompareAndSave/ExpectedIDIsLiteral", testEntityCompareAndSaveExpectedIDIsLiteral)
	runSubtest(t, h, tracker, "Concurrent/ConflictingUpdate", testEntityConcurrentConflict)
	runSubtest(t, h, tracker, "Concurrent/DifferentEntities", testEntityConcurrentDifferent)
	runSubtest(t, h, tracker, "TenantIsolation/Get", testEntityTenantIsolationGet)
	runSubtest(t, h, tracker, "TenantIsolation/Delete", testEntityTenantIsolationDelete)
	runSubtest(t, h, tracker, "TenantIsolation/GetPage", testEntityTenantIsolationGetPage)
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

// In-transaction Count and CountByState reflect the transaction's own view
// for every buffer shape: create, update with state change, delete of a
// committed entity, create-then-delete, delete-then-save, and DeleteAll.
func testEntityCountInTxBufferShapes(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	mref := spi.ModelRef{EntityName: "m-cnt-tx", ModelVersion: "1"}
	ids := make([]string, 4)
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		for i := range ids {
			ids[i] = newID()
			e := newEntity(t, mref.EntityName, ids[i], map[string]any{"i": i})
			e.Meta.State = []string{"open", "closed"}[i%2]
			_, err := es.Save(txCtx, e)
			require.NoError(t, err)
		}
	})

	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	_, txCtx := beginGuarded(t, tm, ctx)
	es, _ := h.Factory.EntityStore(txCtx)
	save := func(id, state string) {
		e := newEntity(t, mref.EntityName, id, map[string]any{})
		e.Meta.State = state
		_, err := es.Save(txCtx, e)
		require.NoError(t, err)
	}
	check := func(step string, total int64, byState map[string]int64) {
		n, err := es.Count(txCtx, mref)
		require.NoError(t, err, step)
		require.Equal(t, total, n, step)
		got, err := es.CountByState(txCtx, mref, nil)
		require.NoError(t, err, step)
		require.Equal(t, byState, got, step)
	}
	check("baseline", 4, map[string]int64{"open": 2, "closed": 2})
	n0 := newID()
	save(n0, "open")
	check("create", 5, map[string]int64{"open": 3, "closed": 2})
	save(ids[1], "open")
	check("update with state change", 5, map[string]int64{"open": 4, "closed": 1})
	require.NoError(t, es.Delete(txCtx, ids[2]))
	check("delete committed", 4, map[string]int64{"open": 3, "closed": 1})
	n1 := newID()
	save(n1, "closed")
	require.NoError(t, es.Delete(txCtx, n1))
	check("create then delete", 4, map[string]int64{"open": 3, "closed": 1})
	require.NoError(t, es.Delete(txCtx, ids[3]))
	save(ids[3], "open")
	check("delete then save", 4, map[string]int64{"open": 4})
	require.NoError(t, es.DeleteAll(txCtx, mref))
	check("after DeleteAll", 0, map[string]int64{})
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

// pitFixture is the shared setup for the point-in-time committed-only family
// (GetAsAt, GetPage(asAt), Iterate(PointInTime), Search(PointInTime)).
// See newPITCommittedOnlyFixture.
type pitFixture struct {
	// ModelRef scopes every collection-shaped read in the family.
	ModelRef spi.ModelRef
	// CommittedID exists in committed state carrying pitCommittedValue, and
	// has been UPDATED to pitDirtyValue inside the open transaction.
	CommittedID string
	// DirtyID was CREATED inside the open transaction and has never been
	// committed. No point-in-time read may surface it.
	DirtyID string
	// AsAt is the point-in-time bound every read in the family uses.
	AsAt time.Time
	// Store and Ctx are the transaction-scoped EntityStore and context. The
	// point-in-time read is issued through THESE, which is the whole point:
	// the read must ignore the transaction it is issued from.
	Store spi.EntityStore
	Ctx   spiCtx
}

const (
	pitCommittedValue = "committed"
	pitDirtyValue     = "dirty"
)

// newPITCommittedOnlyFixture builds the shared scenario for the point-in-time
// committed-only contract: a point-in-time read ignores any ambient
// transaction and answers from committed state as of the requested instant.
//
// It commits one entity, opens a transaction, and inside that transaction both
// CREATES a second entity and UPDATES the committed one. Both writes are
// covered because they fail differently: a backend that consults the
// transaction's overlay surfaces the create as an extra row, and the update as
// a correct row carrying the wrong payload — a result set of the right SHAPE
// that is silently wrong, which a create-only scenario never catches.
//
// AsAt is deliberately an hour in the FUTURE (on the harness clock, so it is
// the same clock domain the backend stamps versions from). Every uncommitted
// write therefore falls strictly INSIDE the requested window: the window
// itself can never be the reason a write is invisible, so the only thing the
// assertion can be passing on is committed-only routing. A cutoff placed
// before the transaction would pass on a backend that reads its own
// uncommitted writes, which is exactly the defect this pins.
//
// The transaction is left open — beginGuarded's cleanup rolls it back — so the
// caller reads through a genuinely in-flight transaction.
func newPITCommittedOnlyFixture(t *testing.T, h Harness, ctx spiCtx, modelName string) pitFixture {
	t.Helper()
	mref := spi.ModelRef{EntityName: modelName, ModelVersion: "1"}

	committedID := newID()
	withTx(t, h, ctx, func(txCtx spiCtx) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, modelName, committedID, map[string]any{"v": pitCommittedValue}))
		require.NoError(t, err)
	})

	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	_, txCtx := beginGuarded(t, tm, ctx)

	esTx, err := h.Factory.EntityStore(txCtx)
	require.NoError(t, err)

	dirtyID := newID()
	_, err = esTx.Save(txCtx, newEntity(t, modelName, dirtyID, map[string]any{"v": pitDirtyValue}))
	require.NoError(t, err)
	_, err = esTx.Save(txCtx, newEntity(t, modelName, committedID, map[string]any{"v": pitDirtyValue}))
	require.NoError(t, err)

	return pitFixture{
		ModelRef:    mref,
		CommittedID: committedID,
		DirtyID:     dirtyID,
		AsAt:        h.Now().UTC().Add(1 * time.Hour),
		Store:       esTx,
		Ctx:         txCtx,
	}
}

// requireCommittedOnly asserts a collection-shaped point-in-time result holds
// exactly the committed entity at its committed payload — neither the
// transaction's uncommitted create nor its uncommitted update showing through.
func (f pitFixture) requireCommittedOnly(t *testing.T, method string, got []*spi.Entity) {
	t.Helper()
	ids := make([]string, 0, len(got))
	for _, e := range got {
		ids = append(ids, e.Meta.ID)
	}
	require.Equal(t, []string{f.CommittedID}, ids,
		"%s issued inside a transaction must not see the transaction's own uncommitted writes (got %v)", method, ids)
	require.Contains(t, string(got[0].Data), `"v":"`+pitCommittedValue+`"`,
		"%s issued inside a transaction must return the COMMITTED payload, not the transaction's uncommitted update", method)
}

// testEntityGetAsAtCommittedOnlyInTx: GetAsAt issued inside a transaction
// answers from committed state — the committed version of an entity the
// transaction has updated, and ErrNotFound for one the transaction created.
func testEntityGetAsAtCommittedOnlyInTx(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	f := newPITCommittedOnlyFixture(t, h, ctx, "m-pit-getasat")

	got, err := f.Store.GetAsAt(f.Ctx, f.CommittedID, f.AsAt)
	require.NoError(t, err)
	require.Contains(t, string(got.Data), `"v":"`+pitCommittedValue+`"`,
		"GetAsAt issued inside a transaction must return the COMMITTED payload, not the transaction's uncommitted update")

	_, err = f.Store.GetAsAt(f.Ctx, f.DirtyID, f.AsAt)
	require.ErrorIs(t, err, spi.ErrNotFound,
		"GetAsAt must not surface an entity the ambient transaction created but has not committed")
}

// testEntityGetPageAsAtCommittedOnlyInTx holds GetPage's asAt path to the same
// family contract its own doc comment already states ("asAt != nil ignores any
// ambient transaction and reads committed-only state as of that instant").
//
// GetPage/AsAtSnapshot already covers the uncommitted CREATE half against a
// cutoff placed after the buffered write. This adds the half that scenario
// cannot reach: an uncommitted UPDATE of an entity that legitimately belongs on
// the page, where the row count stays correct and only the payload is wrong.
func testEntityGetPageAsAtCommittedOnlyInTx(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	f := newPITCommittedOnlyFixture(t, h, ctx, "m-pit-getpage")

	got, err := f.Store.GetPage(f.Ctx, f.ModelRef, 10, 0, &f.AsAt)
	require.NoError(t, err)
	f.requireCommittedOnly(t, "GetPage(asAt)", got)
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

// testEntityGetVersionMetadataEmptyWindowIsNotAnError pins the intended
// contract (see GetVersionMetadata's doc comment): ErrNotFound is returned
// ONLY when entityID has no version history at all. An entity that EXISTS
// but whose versions all fall outside the requested From/Until window must
// come back as an empty slice with a nil error — never ErrNotFound. Two
// real backends diverged here: one returned ErrNotFound for an existing
// entity whose window excluded all its versions; the other correctly
// returned (empty slice, nil). A buggy backend conflating "no rows in the
// window" with "no such entity" fails the first assertion below.
func testEntityGetVersionMetadataEmptyWindowIsNotAnError(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	id := newID()
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, "m-vmw", id, map[string]any{}))
		require.NoError(t, err)
	})

	es, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)

	// A window entirely in the future excludes every version this entity
	// actually has. The entity still EXISTS, so this must come back
	// empty+nil, never ErrNotFound. Derived from h.Now() rather than a
	// wall-clock literal so it stays correct under any backend's clock.
	future := h.Now().UTC().Add(365 * 24 * time.Hour)
	metas, err := es.GetVersionMetadata(ctx, id, spi.VersionMetadataOptions{From: &future})
	require.NoError(t, err, "an existing entity queried with an empty window must not return an error")
	require.Empty(t, metas, "a window excluding all of an existing entity's versions must yield an empty slice")

	// A genuinely missing entity (no version history at all) must still
	// return ErrNotFound — the window-vs-missing-entity distinction is the
	// whole point of this contract.
	_, err = es.GetVersionMetadata(ctx, newID(), spi.VersionMetadataOptions{})
	require.ErrorIs(t, err, spi.ErrNotFound, "an entity with no version history at all must return ErrNotFound")
}

// testEntityGetVersionMetadataLimitCaps pins opts.Limit as a genuine cap on
// the returned row count, not a hint a backend may ignore. It seeds 6
// versions, reads the full unbounded history, then reads again with
// Limit=3 and asserts the capped read returns EXACTLY the first 3 rows of
// the unbounded read (same newest-first prefix) — a backend that ignores
// Limit and returns all 6 fails the length assertion; a backend that caps
// but reorders or drops the wrong rows fails the prefix-equality assertion.
func testEntityGetVersionMetadataLimitCaps(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	id := newID()
	const n = 6
	for i := 0; i < n; i++ {
		withTx(t, h, ctx, func(txCtx context.Context) {
			es, err := h.Factory.EntityStore(txCtx)
			require.NoError(t, err)
			_, err = es.Save(txCtx, newEntity(t, "m-vlim", id, map[string]any{"v": i}))
			require.NoError(t, err)
		})
		h.AdvanceClock(1 * time.Millisecond)
	}

	es, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)

	full, err := es.GetVersionMetadata(ctx, id, spi.VersionMetadataOptions{})
	require.NoError(t, err)
	require.Len(t, full, n, "sanity: unbounded read must return every seeded version")

	const limit = 3
	capped, err := es.GetVersionMetadata(ctx, id, spi.VersionMetadataOptions{Limit: limit})
	require.NoError(t, err)
	require.Len(t, capped, limit, "Limit must cap the returned row count; a backend that ignores Limit returns all %d rows instead of %d", n, limit)
	for i := range capped {
		require.Equal(t, full[i].Version, capped[i].Version,
			"the capped read must be the same newest-first prefix as the unbounded read (row %d)", i)
	}
}

// testEntityGetVersionMetadataUntilBound pins opts.Until as an inclusive
// upper bound on the returned window, mirroring GetAsAt's asAt semantics: a
// version written strictly after Until must never be returned, even though
// it is newer and would otherwise sort first. A backend that ignores Until
// returns every version instead of only the pre-cutoff one.
func testEntityGetVersionMetadataUntilBound(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	id := newID()

	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, "m-vuntil", id, map[string]any{"v": 1}))
		require.NoError(t, err)
	})
	h.AdvanceClock(1 * time.Millisecond)
	until := h.Now().UTC()
	h.AdvanceClock(1 * time.Millisecond)

	// Two more versions written AFTER until — must be excluded.
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, "m-vuntil", id, map[string]any{"v": 2}))
		require.NoError(t, err)
	})
	h.AdvanceClock(1 * time.Millisecond)
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, "m-vuntil", id, map[string]any{"v": 3}))
		require.NoError(t, err)
	})

	es, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	metas, err := es.GetVersionMetadata(ctx, id, spi.VersionMetadataOptions{Until: &until})
	require.NoError(t, err)
	require.Len(t, metas, 1, "Until must exclude every version written after the cutoff, even though they are newer")
	require.False(t, metas[0].Timestamp.After(until), "the one returned version must not be timestamped after Until")
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
// mirroring GetAsAt's contract — AND that asAt ignores any ambient
// transaction's own overlay, reading committed-only state even when called
// through a transaction's own context.
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

	// Fourth entity written AFTER asAt — must not be returned.
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

	// asAt must also ignore the ambient transaction's own uncommitted
	// overlay, not just future committed writes. Capture a committed-only
	// baseline right before opening a transaction, buffer a new save inside
	// that transaction (uncommitted), capture a cutoff AFTER the buffered
	// write, and confirm GetPage(asAt) — called through the transaction's
	// own context — still returns exactly the pre-transaction baseline: if
	// the overlay were consulted instead, the buffered entity's write-time
	// would fall inside the cutoff and it would wrongly appear.
	h.AdvanceClock(1 * time.Millisecond)
	beforeTxAsAt := h.Now().UTC()
	baseline, err := es.GetPage(ctx, mref, 10, 0, &beforeTxAsAt)
	require.NoError(t, err)

	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID, txCtx := beginGuarded(t, tm, ctx)
	esTx, err := h.Factory.EntityStore(txCtx)
	require.NoError(t, err)
	bufferedID := newID()
	_, err = esTx.Save(txCtx, newEntity(t, "m-page-asat", bufferedID, map[string]any{"i": 100}))
	require.NoError(t, err)
	h.AdvanceClock(1 * time.Millisecond)
	laterAsAt := h.Now().UTC()

	gotInTx, err := esTx.GetPage(txCtx, mref, 10, 0, &laterAsAt)
	require.NoError(t, err)
	require.Len(t, gotInTx, len(baseline),
		"GetPage with asAt must ignore the ambient transaction's own uncommitted write — count must match the committed-only baseline captured just before the transaction opened")
	for _, e := range gotInTx {
		require.NotEqual(t, bufferedID, e.Meta.ID,
			"the buffered, uncommitted entity must never appear on an asAt page")
	}
	require.NoError(t, tm.Rollback(txCtx, txID))
}

// entityIDs extracts Meta.ID from a GetPage result in order.
func entityIDs(es []*spi.Entity) []string {
	ids := make([]string, len(es))
	for i, e := range es {
		ids[i] = e.Meta.ID
	}
	return ids
}

// testEntityGetPageInTxWithStagedDeletes reproduces a Critical bug: a
// backend that prefetches a BOUNDED committed prefix (`LIMIT offset+limit`)
// and then merges the ambient transaction's overlay silently under-fills or
// empties the page, because the merge skips committed rows whose IDs are
// staged for deletion without ever extending the prefetch to compensate —
// the scan runs off the end of the artificially bounded prefix. Reproduced
// on one backend: 10 committed entities, deletes staged on the first 5
// inside the ambient tx, GetPage(limit=5, offset=0) returned 0 rows instead
// of the correct next 5.
//
// Three independent entity groups exercise three shapes of the defect, all
// read through ONE shared ambient transaction:
//
//	(a) deletes exactly fill the head of the requested page — the reported
//	    repro: a naive `LIMIT offset+limit` prefetch returns nothing left
//	    to merge.
//	(b) deletes span across the naive prefetch boundary: the deleted block
//	    starts inside the window a naive `LIMIT offset+limit` would fetch
//	    and extends past it, so the naive fetch under-fills even though
//	    most of the deleted rows lie outside the requested page.
//	(c) the same spanning shape as (b), with offset > 0.
//
// A fourth check, layered on group (a), confirms a staged ADD inside the
// same tx still appears in the merged page at its correct h.IDOrder
// position alongside the delete-thinned committed survivors — complementing
// (not duplicating) the existing add-only overlay coverage
// (testIterableOverlaySnapshotAtOpen), which asserts add-visibility but not
// page position.
func testEntityGetPageInTxWithStagedDeletes(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())

	// idOrderSort sorts ids in place by h.IDOrder — used only where the
	// expected order cannot be read directly off an already-ordered
	// GetPage result (i.e. once a freshly-generated ID is mixed in).
	idOrderSort := func(ids []string) {
		sort.Slice(ids, func(i, j int) bool { return h.IDOrder(ids[i], ids[j]) < 0 })
	}

	// seedOrdered commits n freshly-created entities under mref and returns
	// their IDs in canonical h.IDOrder-ascending order (read back via a
	// committed-only GetPage, per GetPage's documented canonical-order
	// contract).
	seedOrdered := func(mref spi.ModelRef, n int) []string {
		t.Helper()
		withTx(t, h, ctx, func(txCtx context.Context) {
			es, err := h.Factory.EntityStore(txCtx)
			require.NoError(t, err)
			for i := 0; i < n; i++ {
				_, err := es.Save(txCtx, newEntity(t, mref.EntityName, newID(), map[string]any{"i": i}))
				require.NoError(t, err)
			}
		})
		es, err := h.Factory.EntityStore(ctx)
		require.NoError(t, err)
		page, err := es.GetPage(ctx, mref, n, 0, nil)
		require.NoError(t, err)
		require.Len(t, page, n)
		return entityIDs(page)
	}

	mrefA := spi.ModelRef{EntityName: "m-page-txdel-a", ModelVersion: "1"}
	mrefB := spi.ModelRef{EntityName: "m-page-txdel-b", ModelVersion: "1"}
	mrefC := spi.ModelRef{EntityName: "m-page-txdel-c", ModelVersion: "1"}

	orderA := seedOrdered(mrefA, 10)
	orderB := seedOrdered(mrefB, 10)
	orderC := seedOrdered(mrefC, 10)

	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID, txCtx := beginGuarded(t, tm, ctx)
	esTx, err := h.Factory.EntityStore(txCtx)
	require.NoError(t, err)

	// (a) deletes exactly fill the head of the requested page.
	for _, id := range orderA[:5] {
		require.NoError(t, esTx.Delete(txCtx, id))
	}
	gotA, err := esTx.GetPage(txCtx, mrefA, 5, 0, nil)
	require.NoError(t, err)
	require.Equal(t, orderA[5:10], entityIDs(gotA),
		"GetPage must return the full next page in canonical order when the entire requested page's committed prefix is staged-deleted")

	// Layer a staged ADD onto the same overlay and confirm it appears at
	// its correct h.IDOrder position once merged with the delete-thinned
	// committed survivors.
	bufferedID := newID()
	_, err = esTx.Save(txCtx, newEntity(t, mrefA.EntityName, bufferedID, map[string]any{"buffered": true}))
	require.NoError(t, err)
	survivorsPlusAdd := append(append([]string{}, orderA[5:10]...), bufferedID)
	idOrderSort(survivorsPlusAdd)
	gotAWithAdd, err := esTx.GetPage(txCtx, mrefA, 6, 0, nil)
	require.NoError(t, err)
	require.Equal(t, survivorsPlusAdd, entityIDs(gotAWithAdd),
		"a staged ADD inside the same tx must appear in the merged page at its correct h.IDOrder position, alongside the delete-thinned committed survivors")

	// (b) deletes span across the naive `LIMIT offset+limit` prefetch
	// boundary: orderB[3:8] (5 rows) are deleted, straddling the boundary a
	// naive backend would compute for GetPage(limit=4, offset=0) — a
	// `LIMIT 4` prefetch covers only orderB[0:4], of which just orderB[3]
	// falls inside; the other 4 deletes (orderB[4:8]) lie entirely outside
	// that naive window, so a backend that never looks past it silently
	// under-fills the page even though most deletes aren't even requested.
	for _, id := range orderB[3:8] {
		require.NoError(t, esTx.Delete(txCtx, id))
	}
	survivorsB := append(append([]string{}, orderB[0:3]...), orderB[8:10]...)
	gotB, err := esTx.GetPage(txCtx, mrefB, 4, 0, nil)
	require.NoError(t, err)
	require.Equal(t, survivorsB[:4], entityIDs(gotB),
		"GetPage must extend past a naive offset+limit prefetch boundary when staged deletes span it, returning the full next page")

	// (c) the same spanning shape as (b), with offset > 0 — a naive
	// `LIMIT offset+limit` = `LIMIT 4` prefetch (orderC[0:4], one deleted)
	// leaves only 2 survivors after applying offset=1, one short of the
	// requested 3; the correct 3rd row (orderC[8]) lies well past the
	// naive window.
	for _, id := range orderC[3:8] {
		require.NoError(t, esTx.Delete(txCtx, id))
	}
	survivorsC := append(append([]string{}, orderC[0:3]...), orderC[8:10]...)
	gotC, err := esTx.GetPage(txCtx, mrefC, 3, 1, nil)
	require.NoError(t, err)
	require.Equal(t, survivorsC[1:4], entityIDs(gotC),
		"GetPage must return the correct full page when offset > 0 and staged deletes span the naive prefetch boundary")

	require.NoError(t, tm.Commit(txCtx, txID))
}

// testEntityGetPageInTxRecordsReadSet pins GetPage's documented unconditional
// (non-opt-in) read-set recording when asAt == nil inside a transaction —
// unlike Searcher/Iterate's opt-in TrackingRead — and, discriminatingly,
// that the recording is scoped to the PAGE, not the whole model. This is
// the deliberate narrowing of first-committer-wins from model-wide to
// page-wide the GetPage doc comment calls out.
//
// Observed black-box (never via internal state), the same technique
// testIterableTrackingReadGating uses: read a page in tx A, have a second,
// independent tx B modify an entity and commit, then check whether tx A's
// own commit is aborted by first-committer-wins.
//
//   - EntityOnPage: B modifies an entity A's page actually returned — A's
//     commit must be rejected (ErrConflict). A backend that implements
//     GetPage as a plain snapshot read with no read-set effect fails this
//     half by committing successfully instead.
//   - EntityOffPage: B modifies an entity that exists in the model but was
//     NOT on A's returned page — A's commit must succeed. A backend that
//     over-broadly records the whole model (not just the page) fails this
//     half by aborting A's commit.
func testEntityGetPageInTxRecordsReadSet(t *testing.T, h Harness) {
	t.Run("EntityOnPage/ConflictAborts", func(t *testing.T) {
		testEntityGetPageReadSetOutcome(t, h, true, false)
	})
	t.Run("EntityOffPage/ConflictSucceeds", func(t *testing.T) {
		testEntityGetPageReadSetOutcome(t, h, false, true)
	})
}

func testEntityGetPageReadSetOutcome(t *testing.T, h Harness, conflictOnPage, wantCommitSucceeds bool) {
	t.Helper()
	ctx := tenantContext(h.NewTenant())
	mref := spi.ModelRef{EntityName: "m-page-readset", ModelVersion: "1"}

	// Seed 4 entities and read back committed canonical order so we know
	// exactly which id lands on a 2-entity first page vs off it.
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		for i := 0; i < 4; i++ {
			_, err := es.Save(txCtx, newEntity(t, mref.EntityName, newID(), map[string]any{"i": i}))
			require.NoError(t, err)
		}
	})
	es0, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	full, err := es0.GetPage(ctx, mref, 4, 0, nil)
	require.NoError(t, err)
	require.Len(t, full, 4)

	onPageID := full[0].Meta.ID
	offPageID := full[3].Meta.ID

	targetID := offPageID
	if conflictOnPage {
		targetID = onPageID
	}

	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID, txCtx := beginGuarded(t, tm, ctx)
	esA, err := h.Factory.EntityStore(txCtx)
	require.NoError(t, err)

	page, err := esA.GetPage(txCtx, mref, 2, 0, nil) // asAt == nil: unconditional read-set recording
	require.NoError(t, err)
	require.Len(t, page, 2)
	require.Equal(t, onPageID, page[0].Meta.ID, "sanity: the 2-entity first page must contain onPageID")

	// Tx B: a concurrent, independent transaction overwrites targetID and
	// commits before Tx A commits.
	tm2, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID2, txCtx2, err := tm2.Begin(ctx)
	require.NoError(t, err)
	esB, err := h.Factory.EntityStore(txCtx2)
	require.NoError(t, err)
	_, err = esB.Save(txCtx2, newEntity(t, mref.EntityName, targetID, map[string]any{"conflict": true}))
	require.NoError(t, err)
	require.NoError(t, tm2.Commit(txCtx2, txID2))

	err = tm.Commit(txCtx, txID)
	if wantCommitSucceeds {
		require.NoError(t, err,
			"a concurrent write to an entity NOT on the returned page must not abort the GetPage transaction — read-set recording is page-scoped, not model-wide")
		return
	}
	require.Error(t, err, "a concurrent write to an entity ON the returned page must abort the GetPage transaction under first-committer-wins")
	require.ErrorIs(t, err, spi.ErrConflict)
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
// never matches, in either of the two ways it could accidentally succeed:
// as a wildcard against an entity with a real, non-empty TransactionID, or
// by matching a stored-empty TransactionID. The latter requires an entity
// actually written with no ambient transaction — a plain Save on a bare
// (non-tx) ctx is a legal SPI operation, and backends that record
// TransactionID stamp an empty one for such writes (see
// EntityVersionMeta.TransactionID's doc comment: "may be empty
// (non-transactional writes)"). Both scenarios must return ErrNotFound.
func testEntityGetVersionByTransactionEmptyTxID(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	es, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)

	// Scenario 1 (wildcard non-match): the entity has a real, non-empty
	// TransactionID; an empty query txID must not match it.
	idReal := newID()
	withTx(t, h, ctx, func(txCtx context.Context) {
		esTx, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = esTx.Save(txCtx, newEntity(t, "m-gvbt-empty", idReal, map[string]any{}))
		require.NoError(t, err)
	})
	_, err = es.GetVersionByTransaction(ctx, idReal, "")
	require.ErrorIs(t, err, spi.ErrNotFound,
		"empty txID must not match an entity with a real, non-empty TransactionID")

	// Scenario 2 (stored-empty non-match): a plain Save with no ambient
	// transaction leaves a stored-empty TransactionID; an empty query txID
	// must not match that either.
	idBare := newID()
	_, err = es.Save(ctx, newEntity(t, "m-gvbt-empty", idBare, map[string]any{}))
	require.NoError(t, err)
	gotBare, err := es.Get(ctx, idBare)
	require.NoError(t, err)
	require.Empty(t, gotBare.Meta.TransactionID,
		"a non-transactional Save must leave TransactionID empty — the scenario this subtest pins")

	_, err = es.GetVersionByTransaction(ctx, idBare, "")
	require.ErrorIs(t, err, spi.ErrNotFound,
		"empty txID must never match a stored-empty TransactionID; it must always return ErrNotFound")
}

// testEntityGetVersionByTransactionUnrelatedTxID verifies the third
// non-match case alongside EmptyTxID (an empty txID) and
// DeletedNeverMatches (a DELETED tombstone): a well-formed, real,
// non-empty txID that simply never wrote to this entity must also return
// ErrNotFound.
func testEntityGetVersionByTransactionUnrelatedTxID(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	id := newID()
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, "m-gvbt-unrel", id, map[string]any{}))
		require.NoError(t, err)
	})

	// Obtain a real, well-formed txID by writing an UNRELATED entity in its
	// own transaction — that txID never touched id.
	otherID := newID()
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, "m-gvbt-unrel", otherID, map[string]any{}))
		require.NoError(t, err)
	})

	es, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	otherEntity, err := es.Get(ctx, otherID)
	require.NoError(t, err)
	unrelatedTxID := otherEntity.Meta.TransactionID
	require.NotEmpty(t, unrelatedTxID)

	_, err = es.GetVersionByTransaction(ctx, id, unrelatedTxID)
	require.ErrorIs(t, err, spi.ErrNotFound,
		"a well-formed, non-empty txID that never wrote to this entity must return ErrNotFound")
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

// testEntityCompareAndSaveExpectedIDIsLiteral pins expectedTxID's literal
// comparison rule: a missing or deleted entity has the empty transaction
// ID, so only expectedTxID == "" matches it. Run both outside a transaction
// and inside one, since the comparison is against the caller's own
// transactional view either way.
func testEntityCompareAndSaveExpectedIDIsLiteral(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())

	assertLiteralTrio := func(opCtx spiCtx, es spi.EntityStore, label string) {
		// A non-empty expectedTxID against a missing entity conflicts —
		// it does not create, even though there is nothing to compare
		// against but "no entity".
		missingID := newID()
		_, err := es.CompareAndSave(opCtx, newEntity(t, "m-cas-lit", missingID, map[string]any{"v": 1}), "nonexistent-tx-id")
		require.ErrorIs(t, err, spi.ErrConflict,
			"%s: non-empty expectedTxID against a missing entity must conflict, not create", label)
		_, err = es.Get(opCtx, missingID)
		require.ErrorIs(t, err, spi.ErrNotFound,
			"%s: a conflicting CompareAndSave must not have created the entity", label)

		// The empty expectedTxID means "expect no entity": it creates
		// against a missing entity.
		createID := newID()
		_, err = es.CompareAndSave(opCtx, newEntity(t, "m-cas-lit", createID, map[string]any{"v": 2}), "")
		require.NoError(t, err, "%s: empty expectedTxID against a missing entity must create", label)
		got, err := es.Get(opCtx, createID)
		require.NoError(t, err)
		require.JSONEq(t, `{"v":2}`, string(got.Data))

		// The same empty expectedTxID against the entity that now exists
		// conflicts: "expect no entity" no longer matches the current state.
		_, err = es.CompareAndSave(opCtx, newEntity(t, "m-cas-lit", createID, map[string]any{"v": 3}), "")
		require.ErrorIs(t, err, spi.ErrConflict,
			"%s: empty expectedTxID against an existing entity must conflict", label)
	}

	es, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	assertLiteralTrio(ctx, es, "outside tx")

	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID, txCtx := beginGuarded(t, tm, ctx)
	esTx, err := h.Factory.EntityStore(txCtx)
	require.NoError(t, err)
	assertLiteralTrio(txCtx, esTx, "inside tx")
	require.NoError(t, tm.Commit(txCtx, txID))
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

func testEntityTenantIsolationGetPage(t *testing.T, h Harness) {
	tA, tB := h.NewTenant(), h.NewTenant()
	ctxA, ctxB := tenantContext(tA), tenantContext(tB)
	mref := spi.ModelRef{EntityName: "m-tigetpage", ModelVersion: "1"}

	withTx(t, h, ctxA, func(txCtx context.Context) {
		es, _ := h.Factory.EntityStore(txCtx)
		_, err := es.Save(txCtx, newEntity(t, "m-tigetpage", newID(), map[string]any{}))
		require.NoError(t, err)
	})

	esB, _ := h.Factory.EntityStore(ctxB)
	got, err := esB.GetPage(ctxB, mref, 10, 0, nil)
	require.NoError(t, err)
	require.Len(t, got, 0, "tenant B must not see tenant A's writes")

	it, err := esB.Iterate(ctxB, mref, spi.Filter{}, spi.IterateOptions{})
	require.NoError(t, err)
	rows, err := drainIterator(t, it)
	require.NoError(t, err)
	require.Len(t, rows, 0, "tenant B must not iterate tenant A's writes")
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
	got, err := es.GetPage(ctx, mref, 10, 0, nil)
	require.NoError(t, err)
	require.NotNil(t, got, "GetPage on an empty model must return a non-nil, empty page")
	require.Len(t, got, 0)
	n, err := es.Count(ctx, mref)
	require.NoError(t, err)
	require.Equal(t, int64(0), n)
}
