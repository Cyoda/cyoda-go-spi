package spitest

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// searcherSeedOrder is the order in which the bounded-or-fail subtests create
// their entities: true is a match for the conformance predicate, false a
// non-matching decoy. Seven entities total — deliberately tiny, because every
// backend runs this suite, including ones where each save is a network
// round-trip.
//
// The decoys are INTERLEAVED, and that placement is the point. newID returns
// v1 UUIDs, so creation order is id order, and the default sort is entity id
// ascending; decoys appended after the matches would therefore always sort
// last, and a backend that bounds its SCAN rather than the matched set would
// still return all five matches and pass. Interleaved, a scan-level bound of
// five takes {decoy, match, match, decoy, match} and loses two matches, so
// AtLimitSucceeds fails. This is what makes the predicate load-bearing rather
// than decorative.
//
// The tail beyond searcherCommittedN must be all matches — the in-tx variant
// stages exactly that tail inside the searching transaction.
var searcherSeedOrder = []bool{false, true, true, false, true, true, true}

// searcherMatchN is the number of matches in searcherSeedOrder, derived rather
// than declared so the two can never drift apart.
var searcherMatchN = countSearcherMatches(searcherSeedOrder)

const (
	// searcherCommittedN is how many of searcherSeedOrder the in-tx variant
	// commits before searching. The tail is staged uncommitted inside the
	// searching transaction, so the bound is held over a merge of the
	// committed side with the transaction's own write-set rather than over
	// the committed side alone. The non-tx variant commits all of it.
	searcherCommittedN = 5

	// searcherMatchValue is the data value the conformance predicate selects
	// on; searcherDecoyValue is what the decoys carry instead.
	searcherMatchValue = "match"
	searcherDecoyValue = "no-match"

	// searcherModel is the model every Searcher subtest seeds into. Each
	// subtest gets a fresh tenant, so a fixed name is collision-free.
	searcherModel = "searcher-bounded"
)

func countSearcherMatches(order []bool) int {
	n := 0
	for _, isMatch := range order {
		if isMatch {
			n++
		}
	}
	return n
}

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

// searcherBoundedOrFail seeds searcherSeedOrder and holds the backend to the
// Searcher doc's contract: a positive Limit is a cap on the matched set, so
// exceeding it fails with ErrSearchResultLimitExceeded rather than returning a
// truncated prefix, exactly-at-limit succeeds, and a non-positive Limit is
// unbounded — the implementation must not substitute a default of its own.
//
// When inTx is set the assertions run inside a live transaction with the tail
// of the match set staged but uncommitted, so each backend's
// read-your-own-writes overlay is held to the same bound as its committed path.
func searcherBoundedOrFail(t *testing.T, h Harness, inTx bool) {
	t.Helper()
	ctx := tenantContext(h.NewTenant())

	committed, staged := searcherSeedOrder, []bool(nil)
	if inTx {
		committed, staged = searcherSeedOrder[:searcherCommittedN], searcherSeedOrder[searcherCommittedN:]
	}

	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		seedSearcherEntities(t, txCtx, es, committed)
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
	// committed path directly. In transaction it stages the tail of the seed
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
		seedSearcherEntities(t, txCtx, es, staged)
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

// seedSearcherEntities saves one entity into searcherModel per element of
// order, in that order — a match for true, a decoy for false. Creation order
// is preserved as id order, which is what makes the interleaving meaningful
// (see searcherSeedOrder).
func seedSearcherEntities(t *testing.T, ctx context.Context, es spi.EntityStore, order []bool) {
	t.Helper()
	for _, isMatch := range order {
		status := searcherDecoyValue
		if isMatch {
			status = searcherMatchValue
		}
		_, err := es.Save(ctx, newEntity(t, searcherModel, newID(), map[string]any{"status": status}))
		require.NoError(t, err)
	}
}
