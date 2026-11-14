package spitest

import (
	"testing"

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
