package spitest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda/cyoda-go-spi"
)

// EnsureLockedModel makes sure the model at ref exists and is LOCKED in the
// tenant carried by ctx, so a test can write entities of that model.
//
// If the model is absent it is saved UNLOCKED with keys and a minimal
// descriptor, then locked. If it is already present and LOCKED, the call
// returns; the test fails if keys differ from the stored unique keys. A model
// present but UNLOCKED fails the test. The call is idempotent per ref, and
// every failure is required.
//
// Call it once per (tenant ctx, model ref) the test writes entities into,
// before the first save, outside any transaction.
func EnsureLockedModel(t testing.TB, f spi.StoreFactory, ctx context.Context, ref spi.ModelRef, keys ...spi.UniqueKey) {
	t.Helper()
	ms, err := f.ModelStore(ctx)
	require.NoError(t, err)
	got, err := ms.Get(ctx, ref)
	if err == nil && got != nil {
		require.Equal(t, spi.ModelLocked, got.State, "model %v exists but is not LOCKED", ref)
		require.Equal(t, normalizeKeys(keys), normalizeKeys(got.UniqueKeys), "model %v exists with different unique keys", ref)
		return
	}
	require.ErrorIs(t, err, spi.ErrNotFound, "model Get failed for %v", ref)
	desc := newModelDescriptor(ref.EntityName, ref.ModelVersion)
	desc.UniqueKeys = keys
	require.NoError(t, ms.Save(ctx, desc))
	require.NoError(t, ms.Lock(ctx, ref))
}

func normalizeKeys(keys []spi.UniqueKey) []spi.UniqueKey {
	if len(keys) == 0 {
		return nil
	}
	return keys
}
