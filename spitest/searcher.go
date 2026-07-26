package spitest

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

const (
	// searcherMatchN is the size of the match-set the bounded-or-fail
	// subtests seed. Deliberately tiny: every backend runs this suite,
	// including ones where each save is a network round-trip.
	searcherMatchN = 5

	// searcherTxStagedN is how many of the searcherMatchN matches the in-tx
	// variant leaves uncommitted inside the searching transaction. The rest
	// are committed beforehand, so the bound is held over a merge of the
	// committed side with the transaction's own write-set rather than over
	// the committed side alone.
	searcherTxStagedN = 2

	// searcherDecoyN is the number of non-matching entities seeded alongside
	// the match set. They make the predicate load-bearing: a backend that
	// bounds the scan instead of the matched set fails AtLimitSucceeds.
	searcherDecoyN = 2

	// searcherMatchValue is the data value the conformance predicate selects on.
	searcherMatchValue = "match"

	// searcherModel is the model every Searcher subtest seeds into. Each
	// subtest gets a fresh tenant, so a fixed name is collision-free.
	searcherModel = "searcher-bounded"
)

// runSearcherSuite exercises the optional spi.Searcher contract.
//
// Backends whose EntityStore does not implement Searcher skip the whole group:
// the interface is optional by design, so its absence is conformant, not a
// failure. This is a type assertion rather than a Harness.Skip entry because
// StoreFactoryConformance fails the run on any Skip key that never matches, so
// a Skip entry for an absent interface would turn a conformant backend red.
func runSearcherSuite(t *testing.T, h Harness, tracker *skipTracker) {
	ctx := tenantContext(h.NewTenant())
	store, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	if _, ok := store.(spi.Searcher); !ok {
		t.Skip("EntityStore does not implement spi.Searcher (optional interface)")
	}

	// Two registered subtests, each seeding once and then asserting every
	// case of the contract against that one seed. The nested case names
	// (OverLimitFails, ...) are plain t.Run and are not Harness.Skip keys —
	// a backend suppresses the group with "Searcher/BoundedOrFail" or
	// "Searcher/BoundedOrFail/InTx".
	runSubtest(t, h, tracker, "BoundedOrFail", testSearcherBoundedOrFail)
	runSubtest(t, h, tracker, "BoundedOrFail/InTx", testSearcherBoundedOrFailInTx)
}

func testSearcherBoundedOrFail(t *testing.T, h Harness) {
	searcherBoundedOrFail(t, h, false)
}

func testSearcherBoundedOrFailInTx(t *testing.T, h Harness) {
	searcherBoundedOrFail(t, h, true)
}

// searcherBoundedOrFail seeds searcherMatchN matching entities (plus decoys)
// and holds the backend to the Searcher doc's contract: a positive Limit is a
// cap on the matched set, so exceeding it fails with
// ErrSearchResultLimitExceeded rather than returning a truncated prefix,
// exactly-at-limit succeeds, and a non-positive Limit is unbounded — the
// implementation must not substitute a default of its own.
//
// When inTx is set the assertions run inside a live transaction with part of
// the match set staged but uncommitted, so each backend's read-your-own-writes
// overlay is held to the same bound as its committed path.
func searcherBoundedOrFail(t *testing.T, h Harness, inTx bool) {
	t.Helper()
	ctx := tenantContext(h.NewTenant())

	committedN := searcherMatchN
	if inTx {
		committedN = searcherMatchN - searcherTxStagedN
	}

	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		seedSearcherEntities(t, es, txCtx, committedN, searcherMatchValue)
		seedSearcherEntities(t, es, txCtx, searcherDecoyN, "no-match")
	})

	filter := spi.Filter{
		Op:       spi.FilterEq,
		Source:   spi.SourceData,
		Path:     "status",
		Value:    searcherMatchValue,
		Declared: []spi.DataType{spi.String},
	}
	opts := func(limit int) spi.SearchOptions {
		return spi.SearchOptions{
			ModelName:    searcherModel,
			ModelVersion: "1",
			Limit:        limit,
		}
	}

	// search runs one bounded search. Out of transaction it hits the
	// committed path directly. In transaction it stages the remaining matches
	// as uncommitted own-writes, searches over the overlay, then rolls back so
	// every case starts from the same committed baseline.
	search := func(t *testing.T, limit int) ([]*spi.Entity, error) {
		t.Helper()
		if !inTx {
			es, err := h.Factory.EntityStore(ctx)
			require.NoError(t, err)
			return es.(spi.Searcher).Search(ctx, filter, opts(limit))
		}
		tm, err := h.Factory.TransactionManager(ctx)
		require.NoError(t, err)
		txID, txCtx, err := tm.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = tm.Rollback(txCtx, txID) }()
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		seedSearcherEntities(t, es, txCtx, searcherTxStagedN, searcherMatchValue)
		return es.(spi.Searcher).Search(txCtx, filter, opts(limit))
	}

	t.Run("OverLimitFails", func(t *testing.T) {
		got, err := search(t, searcherMatchN-1)
		if !errors.Is(err, spi.ErrSearchResultLimitExceeded) {
			t.Fatalf("%d matches with limit %d: got %d entities and err %v, want ErrSearchResultLimitExceeded",
				searcherMatchN, searcherMatchN-1, len(got), err)
		}
		require.Empty(t, got,
			"an exceeded bound must not also return a truncated prefix the caller could mistake for a complete result")
	})

	t.Run("AtLimitSucceeds", func(t *testing.T) {
		got, err := search(t, searcherMatchN)
		require.NoError(t, err)
		require.Len(t, got, searcherMatchN)
	})

	t.Run("ZeroLimitUnbounded", func(t *testing.T) {
		got, err := search(t, 0)
		require.NoError(t, err)
		require.Len(t, got, searcherMatchN)
	})

	t.Run("NegativeLimitUnbounded", func(t *testing.T) {
		got, err := search(t, -1)
		require.NoError(t, err)
		require.Len(t, got, searcherMatchN)
	})
}

// seedSearcherEntities saves n entities into searcherModel whose "status" data
// field is status.
func seedSearcherEntities(t *testing.T, es spi.EntityStore, ctx context.Context, n int, status string) {
	t.Helper()
	for i := 0; i < n; i++ {
		_, err := es.Save(ctx, newEntity(t, searcherModel, newID(), map[string]any{"status": status}))
		require.NoError(t, err)
	}
}
