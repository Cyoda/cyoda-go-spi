package spitest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// The Unique cases pin the rule for unique keys:
//
//   - A store reads a model's unique keys from its own stored model at
//     commit. No caller passes keys: every case here
//     declares the keys on the model only (EnsureLockedModel).
//   - Inside a transaction a save does no claim work. At commit the claims
//     are computed from the transaction's final entities, so a swap, a chain
//     of moves and a claim before a free all commit when the final state is
//     unique. A value two final entities claim, or one a committed entity
//     outside the transaction's writes holds, fails the commit with
//     ErrUniqueViolation; a partial composite key fails it with
//     ErrPartialUniqueKey. A write outside a transaction does the same
//     checks on its own.

const (
	uniqueModel        = "m-unique"
	uniqueKeylessModel = "m-unique-keyless"
)

func runUniqueSuite(t *testing.T, h Harness, tracker *skipTracker) {
	runSubtest(t, h, tracker, "KeysFromStoredModel/Tx", testUniqueKeysFromStoredModelTx)
	runSubtest(t, h, tracker, "KeysFromStoredModel/NonTx", testUniqueKeysFromStoredModelNonTx)
	runSubtest(t, h, tracker, "SwapInTx", testUniqueSwapInTx)
	runSubtest(t, h, tracker, "ChainInTx", testUniqueChainInTx)
	runSubtest(t, h, tracker, "FreeAfterClaimInTx", testUniqueFreeAfterClaimInTx)
	runSubtest(t, h, tracker, "SameValueTwiceInTx", testUniqueSameValueTwiceInTx)
	runSubtest(t, h, tracker, "PartialKeyAtCommit", testUniquePartialKeyAtCommit)
	runSubtest(t, h, tracker, "SavepointRollbackRestoresClaim", testUniqueSavepointRollbackRestoresClaim)
	runSubtest(t, h, tracker, "KeylessModelCommitsNoClaims", testUniqueKeylessModelCommitsNoClaims)
}

// codeKey is the single-field unique key over $.code the cases declare.
var codeKey = spi.UniqueKey{ID: "code", Fields: []string{"$.code"}}

// uniqueFixture is a fresh tenant with the case's models.
type uniqueFixture struct {
	h   Harness
	ctx spiCtx
}

// newUniqueFixture opens a fresh tenant and creates uniqueModel, LOCKED, with
// codeKey as its only unique key.
func newUniqueFixture(t *testing.T, h Harness) *uniqueFixture {
	t.Helper()
	f := &uniqueFixture{h: h, ctx: tenantContext(h.NewTenant())}
	EnsureLockedModel(t, h.Factory, f.ctx, uniqueRef(uniqueModel), codeKey)
	return f
}

func uniqueRef(model string) spi.ModelRef {
	return spi.ModelRef{EntityName: model, ModelVersion: "1"}
}

func codeOf(v string) map[string]any { return map[string]any{"code": v} }

func (f *uniqueFixture) store(t *testing.T) spi.EntityStore {
	t.Helper()
	es, err := f.h.Factory.EntityStore(f.ctx)
	require.NoError(t, err)
	return es
}

// saveNonTx saves an entity outside any transaction and returns the store's
// answer. The clock advances after a write that succeeded.
func (f *uniqueFixture) saveNonTx(t *testing.T, model, id string, payload map[string]any) error {
	t.Helper()
	_, err := f.store(t).Save(f.ctx, newEntity(t, model, id, payload))
	if err == nil {
		f.h.AdvanceClock(time.Millisecond)
	}
	return err
}

// tx runs fn in a new transaction and returns Commit's answer. Every write
// in fn must succeed: inside a transaction a save does no claim and no model
// work, so any refusal belongs to the commit. The clock advances after a
// commit that succeeded.
func (f *uniqueFixture) tx(t *testing.T, fn func(txID string, txCtx spiCtx, es spi.EntityStore)) error {
	t.Helper()
	tm, err := f.h.Factory.TransactionManager(f.ctx)
	require.NoError(t, err)
	txID, txCtx := beginGuarded(t, tm, f.ctx)
	es, err := f.h.Factory.EntityStore(txCtx)
	require.NoError(t, err)
	fn(txID, txCtx, es)
	err = tm.Commit(txCtx, txID)
	if err == nil {
		f.h.AdvanceClock(time.Millisecond)
	}
	return err
}

// uniqueSave writes an entity inside a transaction and requires that the
// write succeed.
func uniqueSave(t *testing.T, txCtx spiCtx, es spi.EntityStore, model, id string, payload map[string]any) {
	t.Helper()
	_, err := es.Save(txCtx, newEntity(t, model, id, payload))
	require.NoError(t, err, "a save inside a transaction does no claim or model work; any refusal is the commit's")
}

// seed commits a new entity of uniqueModel holding value in a transaction of
// its own and returns its id.
func (f *uniqueFixture) seed(t *testing.T, value string) string {
	t.Helper()
	id := newID()
	require.NoError(t, f.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		uniqueSave(t, txCtx, es, uniqueModel, id, codeOf(value))
	}))
	return id
}

// requireCode asserts that the committed entity id holds value.
func (f *uniqueFixture) requireCode(t *testing.T, id, value, why string) {
	t.Helper()
	got, err := f.store(t).Get(f.ctx, id)
	require.NoError(t, err, why)
	require.Equal(t, value, entityDataString(t, got, "code"), why)
}

// requireAbsent asserts that no committed entity id exists.
func (f *uniqueFixture) requireAbsent(t *testing.T, id, why string) {
	t.Helper()
	_, err := f.store(t).Get(f.ctx, id)
	require.ErrorIs(t, err, spi.ErrNotFound, why)
}

// requireClaimed asserts that value of codeKey in uniqueModel is held: a
// write outside a transaction of a new entity with it is refused and stores
// nothing.
func (f *uniqueFixture) requireClaimed(t *testing.T, value, why string) {
	t.Helper()
	probe := newID()
	require.ErrorIs(t, f.saveNonTx(t, uniqueModel, probe, codeOf(value)), spi.ErrUniqueViolation,
		"%s: value %q must still be claimed", why, value)
	f.requireAbsent(t, probe, "a refused save stores nothing")
}

// requireFree asserts that value of codeKey in uniqueModel is not held, by
// claiming it with a new entity outside a transaction. The value is held
// afterwards.
func (f *uniqueFixture) requireFree(t *testing.T, value, why string) {
	t.Helper()
	require.NoError(t, f.saveNonTx(t, uniqueModel, newID(), codeOf(value)), "%s: value %q must be free", why, value)
}

// A transaction's commit enforces the keys of the stored model against an
// entity committed earlier: the duplicate is refused and stores nothing.
func testUniqueKeysFromStoredModelTx(t *testing.T, h Harness) {
	f := newUniqueFixture(t, h)
	a := f.seed(t, "x")

	b := newID()
	err := f.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		uniqueSave(t, txCtx, es, uniqueModel, b, codeOf("x"))
	})
	require.ErrorIs(t, err, spi.ErrUniqueViolation, "the commit must enforce the stored model's key")
	f.requireAbsent(t, b, "a refused commit stores nothing")
	f.requireCode(t, a, "x", "the holder is unchanged")
}

// A write outside a transaction enforces the keys of the stored model on its
// own: the duplicate is refused and stores nothing.
func testUniqueKeysFromStoredModelNonTx(t *testing.T, h Harness) {
	f := newUniqueFixture(t, h)
	a := newID()
	require.NoError(t, f.saveNonTx(t, uniqueModel, a, codeOf("x")))

	b := newID()
	require.ErrorIs(t, f.saveNonTx(t, uniqueModel, b, codeOf("x")), spi.ErrUniqueViolation,
		"a save outside a transaction must enforce the stored model's key")
	f.requireAbsent(t, b, "a refused save stores nothing")
	f.requireCode(t, a, "x", "the holder is unchanged")
}

// Two entities swap their values in one transaction, in one order and back
// in the other. Each commit succeeds, and both values stay claimed after it:
// a store that releases and inserts one entity at a time loses a claim here.
func testUniqueSwapInTx(t *testing.T, h Harness) {
	f := newUniqueFixture(t, h)
	a := f.seed(t, "x")
	b := f.seed(t, "y")

	require.NoError(t, f.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		uniqueSave(t, txCtx, es, uniqueModel, a, codeOf("y"))
		uniqueSave(t, txCtx, es, uniqueModel, b, codeOf("x"))
	}), "a swap is unique in its final state")
	f.requireCode(t, a, "y", "swap")
	f.requireCode(t, b, "x", "swap")
	f.requireClaimed(t, "x", "after the swap")
	f.requireClaimed(t, "y", "after the swap")

	require.NoError(t, f.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		uniqueSave(t, txCtx, es, uniqueModel, b, codeOf("y"))
		uniqueSave(t, txCtx, es, uniqueModel, a, codeOf("x"))
	}), "the swap back, saved in the other order, is unique in its final state")
	f.requireCode(t, a, "x", "swap back")
	f.requireCode(t, b, "y", "swap back")
	f.requireClaimed(t, "x", "after the swap back")
	f.requireClaimed(t, "y", "after the swap back")
}

// A chain of moves in one transaction, each entity taking the next one's
// value, saved in the order in which every save claims a value still held:
// the commit succeeds, the first value is freed and every other one is held.
func testUniqueChainInTx(t *testing.T, h Harness) {
	f := newUniqueFixture(t, h)
	a := f.seed(t, "v1")
	b := f.seed(t, "v2")
	c := f.seed(t, "v3")

	require.NoError(t, f.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		uniqueSave(t, txCtx, es, uniqueModel, a, codeOf("v2"))
		uniqueSave(t, txCtx, es, uniqueModel, b, codeOf("v3"))
		uniqueSave(t, txCtx, es, uniqueModel, c, codeOf("v4"))
	}), "a chain of moves is unique in its final state")
	f.requireCode(t, a, "v2", "chain")
	f.requireCode(t, b, "v3", "chain")
	f.requireCode(t, c, "v4", "chain")
	for _, v := range []string{"v2", "v3", "v4"} {
		f.requireClaimed(t, v, "after the chain")
	}
	f.requireFree(t, "v1", "the chain moved every entity off v1")
}

// A transaction claims a held value first and frees it after, once by a
// re-key of the holder and once by a delete of it: each commit succeeds and
// the new holder keeps the value.
func testUniqueFreeAfterClaimInTx(t *testing.T, h Harness) {
	f := newUniqueFixture(t, h)

	t.Log("freed by a re-key")
	a := f.seed(t, "x")
	b := newID()
	require.NoError(t, f.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		uniqueSave(t, txCtx, es, uniqueModel, b, codeOf("x"))
		uniqueSave(t, txCtx, es, uniqueModel, a, codeOf("y"))
	}), "a claim before the holder's re-key is unique in the final state")
	f.requireCode(t, b, "x", "the new holder")
	f.requireCode(t, a, "y", "the re-keyed old holder")
	f.requireClaimed(t, "x", "held by the new holder")
	f.requireClaimed(t, "y", "held by the re-keyed old holder")

	t.Log("freed by a delete")
	c := f.seed(t, "z")
	d := newID()
	require.NoError(t, f.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		uniqueSave(t, txCtx, es, uniqueModel, d, codeOf("z"))
		require.NoError(t, es.Delete(txCtx, c))
	}), "a claim before the holder's delete is unique in the final state")
	f.requireAbsent(t, c, "the deleted old holder")
	f.requireCode(t, d, "z", "the new holder")
	f.requireClaimed(t, "z", "held by the new holder")
}

// Two entities of one transaction claim the same value: the commit is
// refused and stores neither. One entity saved twice with the same value is
// one final entity and commits.
func testUniqueSameValueTwiceInTx(t *testing.T, h Harness) {
	f := newUniqueFixture(t, h)

	a, b := newID(), newID()
	err := f.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		uniqueSave(t, txCtx, es, uniqueModel, a, codeOf("x"))
		uniqueSave(t, txCtx, es, uniqueModel, b, codeOf("x"))
	})
	require.ErrorIs(t, err, spi.ErrUniqueViolation, "a value two final entities claim must fail the commit")
	f.requireAbsent(t, a, "a refused commit stores nothing")
	f.requireAbsent(t, b, "a refused commit stores nothing")

	c := newID()
	require.NoError(t, f.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		uniqueSave(t, txCtx, es, uniqueModel, c, codeOf("x"))
		uniqueSave(t, txCtx, es, uniqueModel, c, codeOf("x"))
	}), "one entity saved twice is one final entity and claims the value once")
	f.requireCode(t, c, "x", "the one entity")
	f.requireClaimed(t, "x", "held by the one entity")
}

// A partial composite key in a final entity fails the commit with
// ErrPartialUniqueKey; the commit judges the final entity, not an earlier
// save of it.
func testUniquePartialKeyAtCommit(t *testing.T, h Harness) {
	f := &uniqueFixture{h: h, ctx: tenantContext(h.NewTenant())}
	const model = "m-unique-composite"
	EnsureLockedModel(t, h.Factory, f.ctx, uniqueRef(model),
		spi.UniqueKey{ID: "ab", Fields: []string{"$.a", "$.b"}})

	t.Log("the only save is partial")
	e1 := newID()
	err := f.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		uniqueSave(t, txCtx, es, model, e1, map[string]any{"a": "1"})
	})
	require.ErrorIs(t, err, spi.ErrPartialUniqueKey, "a partial composite key must fail the commit")
	f.requireAbsent(t, e1, "a refused commit stores nothing")

	t.Log("a partial save completed by a later save")
	e2 := newID()
	require.NoError(t, f.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		uniqueSave(t, txCtx, es, model, e2, map[string]any{"a": "1"})
		uniqueSave(t, txCtx, es, model, e2, map[string]any{"a": "1", "b": "2"})
	}), "the final entity's key is complete")

	t.Log("a complete save made partial by a later save")
	e3 := newID()
	err = f.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		uniqueSave(t, txCtx, es, model, e3, map[string]any{"a": "1", "b": "3"})
		uniqueSave(t, txCtx, es, model, e3, map[string]any{"a": "1"})
	})
	require.ErrorIs(t, err, spi.ErrPartialUniqueKey, "the final entity's key is partial")
	f.requireAbsent(t, e3, "a refused commit stores nothing")
}

// A rollback to a savepoint restores the claims of the writes it undoes: a
// re-key and a delete rolled back leave their committed holders' values
// claimed after the commit, and the value the re-key would have taken stays
// free for another entity of the transaction.
func testUniqueSavepointRollbackRestoresClaim(t *testing.T, h Harness) {
	f := newUniqueFixture(t, h)
	a := f.seed(t, "x")
	c := f.seed(t, "w")
	tm, err := h.Factory.TransactionManager(f.ctx)
	require.NoError(t, err)

	b := newID()
	require.NoError(t, f.tx(t, func(txID string, txCtx spiCtx, es spi.EntityStore) {
		sp, err := tm.Savepoint(txCtx, txID)
		require.NoError(t, err)
		uniqueSave(t, txCtx, es, uniqueModel, a, codeOf("y"))
		require.NoError(t, es.Delete(txCtx, c))
		require.NoError(t, tm.RollbackToSavepoint(txCtx, txID, sp))
		uniqueSave(t, txCtx, es, uniqueModel, b, codeOf("y"))
	}), "after the rollback the transaction's final state is unique")
	f.requireCode(t, a, "x", "the rolled-back re-key")
	f.requireCode(t, c, "w", "the rolled-back delete")
	f.requireCode(t, b, "y", "the save after the rollback")
	f.requireClaimed(t, "x", "the rolled-back re-key")
	f.requireClaimed(t, "w", "the rolled-back delete")
	f.requireClaimed(t, "y", "the save after the rollback")
}

// A model with no unique keys has nothing to claim: entities with equal
// values, with no value and with a non-scalar value at a path another model
// keys all commit, in a transaction and outside one, beside an entity of a
// keyed model that holds the same value.
func testUniqueKeylessModelCommitsNoClaims(t *testing.T, h Harness) {
	f := newUniqueFixture(t, h)
	EnsureLockedModel(t, h.Factory, f.ctx, uniqueRef(uniqueKeylessModel))

	ids := []string{newID(), newID(), newID(), newID()}
	keyed := newID()
	require.NoError(t, f.tx(t, func(_ string, txCtx spiCtx, es spi.EntityStore) {
		uniqueSave(t, txCtx, es, uniqueKeylessModel, ids[0], codeOf("x"))
		uniqueSave(t, txCtx, es, uniqueKeylessModel, ids[1], codeOf("x"))
		uniqueSave(t, txCtx, es, uniqueKeylessModel, ids[2], map[string]any{"other": 1})
		uniqueSave(t, txCtx, es, uniqueKeylessModel, ids[3], map[string]any{"code": map[string]any{"n": 1}})
		uniqueSave(t, txCtx, es, uniqueModel, keyed, codeOf("x"))
	}), "a keyless model claims nothing; claims are per model")
	for _, id := range ids {
		_, err := f.store(t).Get(f.ctx, id)
		require.NoError(t, err, "every entity of the keyless model is committed")
	}
	f.requireCode(t, keyed, "x", "the keyed entity")

	require.NoError(t, f.saveNonTx(t, uniqueKeylessModel, newID(), codeOf("x")),
		"a save outside a transaction into a keyless model claims nothing")
	f.requireClaimed(t, "x", "the keyed model's holder is unaffected by the keyless model")

}
