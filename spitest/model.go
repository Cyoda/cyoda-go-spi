package spitest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

func runModelSuite(t *testing.T, h Harness, tracker *skipTracker) {
	runSubtest(t, h, tracker, "SaveAndGet", testModelSaveAndGet)
	runSubtest(t, h, tracker, "GetAll/EmptyTenant", testModelGetAllEmpty)
	runSubtest(t, h, tracker, "GetAll/MultipleModels", testModelGetAllMultiple)
	runSubtest(t, h, tracker, "Delete", testModelDelete)
	runSubtest(t, h, tracker, "Delete/NotFound", testModelDeleteNotFound)
	runSubtest(t, h, tracker, "Lock/UnlockRoundTrip", testModelLockUnlock)
	runSubtest(t, h, tracker, "IsLocked/ReflectsState", testModelIsLocked)
	runSubtest(t, h, tracker, "Lock/Idempotent", testModelLockIdempotent)
	runSubtest(t, h, tracker, "SetChangeLevel", testModelSetChangeLevel)
	runSubtest(t, h, tracker, "TenantIsolation", testModelTenantIsolation)
	runSubtest(t, h, tracker, "Lock/NotFound", testModelMissingRef)
	runSubtest(t, h, tracker, "Unlock/RefusesLiveEntities", testModelUnlockRefusesLiveEntities)
	runSubtest(t, h, tracker, "Delete/RefusesLocked", testModelDeleteRefusesLocked)
	runSubtest(t, h, tracker, "Save/RefusesOverLocked", testModelSaveRefusesOverLocked)
	runSubtest(t, h, tracker, "Interleave/UnlockVsUncommittedWrite", testModelInterleaveUnlockVsUncommittedWrite)
}

func newModelDescriptor(name, version string) *spi.ModelDescriptor {
	return &spi.ModelDescriptor{
		Ref:         spi.ModelRef{EntityName: name, ModelVersion: version},
		State:       spi.ModelUnlocked,
		ChangeLevel: spi.ChangeLevelStructural,
	}
}

func testModelSaveAndGet(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ms, err := h.Factory.ModelStore(ctx)
	require.NoError(t, err)
	md := newModelDescriptor("m1", "1")
	require.NoError(t, ms.Save(ctx, md))
	got, err := ms.Get(ctx, md.Ref)
	require.NoError(t, err)
	require.Equal(t, md.Ref, got.Ref)
}

func testModelGetAllEmpty(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ms, _ := h.Factory.ModelStore(ctx)
	got, err := ms.GetAll(ctx)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Len(t, got, 0)
}

func testModelGetAllMultiple(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ms, _ := h.Factory.ModelStore(ctx)
	require.NoError(t, ms.Save(ctx, newModelDescriptor("m1", "1")))
	require.NoError(t, ms.Save(ctx, newModelDescriptor("m2", "1")))
	require.NoError(t, ms.Save(ctx, newModelDescriptor("m3", "1")))
	got, err := ms.GetAll(ctx)
	require.NoError(t, err)
	require.Len(t, got, 3)
}

func testModelDelete(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ms, _ := h.Factory.ModelStore(ctx)
	md := newModelDescriptor("m1", "1")
	require.NoError(t, ms.Save(ctx, md))
	require.NoError(t, ms.Delete(ctx, md.Ref))
	_, err := ms.Get(ctx, md.Ref)
	require.ErrorIs(t, err, spi.ErrNotFound)
}

func testModelDeleteNotFound(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ms, _ := h.Factory.ModelStore(ctx)
	err := ms.Delete(ctx, spi.ModelRef{EntityName: "never", ModelVersion: "1"})
	require.ErrorIs(t, err, spi.ErrNotFound)
}

func testModelLockUnlock(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ms, _ := h.Factory.ModelStore(ctx)
	md := newModelDescriptor("m1", "1")
	require.NoError(t, ms.Save(ctx, md))
	require.NoError(t, ms.Lock(ctx, md.Ref))
	ok, err := ms.IsLocked(ctx, md.Ref)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, ms.Unlock(ctx, md.Ref))
	ok, err = ms.IsLocked(ctx, md.Ref)
	require.NoError(t, err)
	require.False(t, ok)
}

func testModelIsLocked(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ms, _ := h.Factory.ModelStore(ctx)
	md := newModelDescriptor("m1", "1")
	require.NoError(t, ms.Save(ctx, md))
	ok, err := ms.IsLocked(ctx, md.Ref)
	require.NoError(t, err)
	require.False(t, ok, "freshly saved model must not be locked")
}

func testModelLockIdempotent(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ms, _ := h.Factory.ModelStore(ctx)
	md := newModelDescriptor("m1", "1")
	require.NoError(t, ms.Save(ctx, md))
	require.NoError(t, ms.Lock(ctx, md.Ref))
	require.NoError(t, ms.Lock(ctx, md.Ref), "re-locking an already-locked model is a no-op")
}

func testModelSetChangeLevel(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ms, _ := h.Factory.ModelStore(ctx)
	md := newModelDescriptor("m1", "1")
	require.NoError(t, ms.Save(ctx, md))
	require.NoError(t, ms.SetChangeLevel(ctx, md.Ref, spi.ChangeLevelType))
	got, err := ms.Get(ctx, md.Ref)
	require.NoError(t, err)
	require.Equal(t, spi.ChangeLevelType, got.ChangeLevel)
}

func testModelTenantIsolation(t *testing.T, h Harness) {
	tA, tB := h.NewTenant(), h.NewTenant()
	ctxA, ctxB := tenantContext(tA), tenantContext(tB)
	msA, _ := h.Factory.ModelStore(ctxA)
	msB, _ := h.Factory.ModelStore(ctxB)
	md := newModelDescriptor("same-name", "1")
	require.NoError(t, msA.Save(ctxA, md))
	_, err := msB.Get(ctxB, md.Ref)
	require.ErrorIs(t, err, spi.ErrNotFound)
}

func testModelMissingRef(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ms, _ := h.Factory.ModelStore(ctx)
	ref := spi.ModelRef{EntityName: "never", ModelVersion: "1"}
	require.ErrorIs(t, ms.Lock(ctx, ref), spi.ErrNotFound)
	require.ErrorIs(t, ms.Unlock(ctx, ref), spi.ErrNotFound)
	require.ErrorIs(t, ms.SetChangeLevel(ctx, ref, spi.ChangeLevelType), spi.ErrNotFound)
	_, err := ms.IsLocked(ctx, ref)
	require.ErrorIs(t, err, spi.ErrNotFound)
}

// testModelUnlockRefusesLiveEntities pins that Unlock refuses a model with
// live entities, with the count in the refusal, and changes nothing. Three
// entities are committed and one deleted, so a store that counts the delete's
// tombstone answers 3, not 2.
func testModelUnlockRefusesLiveEntities(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ref := spi.ModelRef{EntityName: "m-unlock-live", ModelVersion: "1"}
	EnsureLockedModel(t, h.Factory, ctx, ref)
	ids := []string{newID(), newID(), newID()}
	withTx(t, h, ctx, func(txCtx spiCtx) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		for i, id := range ids {
			_, err := es.Save(txCtx, newEntity(t, ref.EntityName, id, map[string]any{"n": i}))
			require.NoError(t, err)
		}
	})
	h.AdvanceClock(time.Millisecond)
	withTx(t, h, ctx, func(txCtx spiCtx) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		require.NoError(t, es.Delete(txCtx, ids[0]))
	})
	h.AdvanceClock(time.Millisecond)

	ms, err := h.Factory.ModelStore(ctx)
	require.NoError(t, err)
	err = ms.Unlock(ctx, ref)
	require.ErrorIs(t, err, spi.ErrModelHasEntities)
	var mhe *spi.ModelHasEntitiesError
	require.ErrorAs(t, err, &mhe)
	require.Equal(t, int64(2), mhe.Count, "the refusal counts the live entities only")
	require.Equal(t, ref, mhe.Ref)

	locked, err := ms.IsLocked(ctx, ref)
	require.NoError(t, err)
	require.True(t, locked, "a refused Unlock leaves the model LOCKED")
}

// testModelDeleteRefusesLocked pins that Delete refuses a LOCKED model and
// leaves it in place.
func testModelDeleteRefusesLocked(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ref := spi.ModelRef{EntityName: "m-delete-locked", ModelVersion: "1"}
	EnsureLockedModel(t, h.Factory, ctx, ref)
	ms, err := h.Factory.ModelStore(ctx)
	require.NoError(t, err)

	require.ErrorIs(t, ms.Delete(ctx, ref), spi.ErrModelLocked)

	got, err := ms.Get(ctx, ref)
	require.NoError(t, err, "a refused Delete leaves the model in place")
	require.Equal(t, spi.ModelLocked, got.State)
}

// testModelSaveRefusesOverLocked pins that Save of a descriptor over a stored
// LOCKED model is refused and changes neither the stored keys nor the state:
// a LOCKED model's descriptor changes only while it is UNLOCKED.
func testModelSaveRefusesOverLocked(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ref := spi.ModelRef{EntityName: "m-save-locked", ModelVersion: "1"}
	keys := []spi.UniqueKey{{ID: "k1", Fields: []string{"$.code"}}}
	EnsureLockedModel(t, h.Factory, ctx, ref, keys...)
	ms, err := h.Factory.ModelStore(ctx)
	require.NoError(t, err)

	over := newModelDescriptor(ref.EntityName, ref.ModelVersion)
	over.UniqueKeys = []spi.UniqueKey{{ID: "k2", Fields: []string{"$.other"}}}
	require.ErrorIs(t, ms.Save(ctx, over), spi.ErrModelLocked)

	got, err := ms.Get(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, spi.ModelLocked, got.State, "a refused Save leaves the model LOCKED")
	require.Equal(t, keys, got.UniqueKeys, "a refused Save leaves the stored unique keys")
}

// testModelInterleaveUnlockVsUncommittedWrite pins that an entity commit and
// an Unlock of its model never both succeed, in either order.
//
//   - Unlock first: an entity written in an open transaction is not yet a
//     live entity, so Unlock succeeds (it counts none). The transaction's
//     commit then finds the model not LOCKED and is refused, writing
//     nothing: ErrModelNotLocked, or ErrConflict from a store whose
//     transaction reads from a snapshot taken before the Unlock (see
//     requireModelNotLockedAnswer).
//   - Commit first: the entity is live, so Unlock refuses with a
//     *ModelHasEntitiesError and the model stays LOCKED.
func testModelInterleaveUnlockVsUncommittedWrite(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ms, err := h.Factory.ModelStore(ctx)
	require.NoError(t, err)
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)

	t.Log("unlock first: the commit is refused")
	refA := spi.ModelRef{EntityName: "m-interleave-unlock-first", ModelVersion: "1"}
	EnsureLockedModel(t, h.Factory, ctx, refA)
	idA := newID()
	txA, txCtxA := beginGuarded(t, tm, ctx)
	esA, err := h.Factory.EntityStore(txCtxA)
	require.NoError(t, err)
	_, err = esA.Save(txCtxA, newEntity(t, refA.EntityName, idA, map[string]any{"n": 1}))
	require.NoError(t, err)
	require.NoError(t, ms.Unlock(ctx, refA), "an uncommitted entity is not a live entity")
	requireModelNotLockedAnswer(t, tm.Commit(txCtxA, txA), true)
	es, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	_, err = es.Get(ctx, idA)
	require.ErrorIs(t, err, spi.ErrNotFound, "the refused commit writes nothing")
	locked, err := ms.IsLocked(ctx, refA)
	require.NoError(t, err)
	require.False(t, locked)
	h.AdvanceClock(time.Millisecond)

	t.Log("commit first: the unlock is refused")
	refB := spi.ModelRef{EntityName: "m-interleave-commit-first", ModelVersion: "1"}
	EnsureLockedModel(t, h.Factory, ctx, refB)
	txB, txCtxB := beginGuarded(t, tm, ctx)
	esB, err := h.Factory.EntityStore(txCtxB)
	require.NoError(t, err)
	_, err = esB.Save(txCtxB, newEntity(t, refB.EntityName, newID(), map[string]any{"n": 1}))
	require.NoError(t, err)
	require.NoError(t, tm.Commit(txCtxB, txB))
	h.AdvanceClock(time.Millisecond)
	err = ms.Unlock(ctx, refB)
	var mhe *spi.ModelHasEntitiesError
	require.ErrorAs(t, err, &mhe, "an Unlock after the commit must see the entity")
	require.Equal(t, int64(1), mhe.Count)
	locked, err = ms.IsLocked(ctx, refB)
	require.NoError(t, err)
	require.True(t, locked, "a refused Unlock leaves the model LOCKED")
}
