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
	runSubtest(t, h, tracker, "PIT/CommittedOnlyInTx", testSearcherPITCommittedOnlyInTx)
	runSubtest(t, h, tracker, "FilterPath/Grammar", testSearcherFilterPathGrammar)
	runSubtest(t, h, tracker, "Pattern/LikeGrammar", testPatternLikeGrammar)
	runSubtest(t, h, tracker, "Pattern/MalformedLike", testPatternMalformedLike)
}

// testSearcherPITCommittedOnlyInTx pins the Searcher doc's committed-only
// clause — "In-transaction point-in-time reads are committed-only — they never
// see the transaction's own uncommitted writes for the PIT dimension" — which
// nothing in the suite previously exercised.
//
// Same shared fixture as the rest of the point-in-time family
// (newPITCommittedOnlyFixture): the cutoff is deliberately in the future so
// only committed-only routing, never the window, can be what hides the
// transaction's own writes.
func testSearcherPITCommittedOnlyInTx(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	f := newPITCommittedOnlyFixture(t, h, ctx, "searcher-pit-intx")

	got, err := f.Store.(spi.Searcher).Search(f.Ctx, spi.Filter{}, spi.SearchOptions{
		ModelName:    f.ModelRef.EntityName,
		ModelVersion: f.ModelRef.ModelVersion,
		Limit:        100,
		PointInTime:  &f.AsAt,
	})
	require.NoError(t, err)
	f.requireCommittedOnly(t, "Search(PointInTime)", got)
}

func testSearcherBoundedOrFail(t *testing.T, h Harness) {
	searcherBoundedOrFail(t, h, false)
}

func testSearcherBoundedOrFailInTx(t *testing.T, h Harness) {
	searcherBoundedOrFail(t, h, true)
}

// searcherBoundedOrFail seeds searcherSeedOrder and holds the backend to the
// Searcher doc's contract: Limit is a cap on the matched set, so exceeding it
// fails with ErrSearchResultLimitExceeded rather than returning a truncated
// prefix, exactly-at-limit succeeds, and Limit <= 0 is a contract violation
// the implementation must reject with an error rather than treating as
// unbounded or substituting a default of its own.
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

	t.Run("ZeroLimitRejected", func(t *testing.T) {
		got, err := search(t, 0)
		require.Error(t, err, "Limit <= 0 is a contract violation, not \"unbounded\"")
		require.Empty(t, got)
	})

	t.Run("NegativeLimitRejected", func(t *testing.T) {
		got, err := search(t, -1)
		require.Error(t, err, "Limit <= 0 is a contract violation, not \"unbounded\"")
		require.Empty(t, got)
	})
}

// ---------------------------------------------------------------------------
// Filter.Path grammar
//
// Shared by every filter-taking entry point on the SPI (Searcher.Search here,
// Iterable.Iterate in iterable.go — the same reuse-across-suites arrangement
// iterableModelRef already uses for the seed helpers). The vocabulary lives
// here, next to Search, because Search is where a Filter first reaches a
// backend.
//
// The tables below are the executable form of Filter.Path's documented
// grammar:
//
//	path      = segment ( "." segment )*
//	segment   = name subscript*
//	name      = 1*( ALPHA / DIGIT / "_" / "-" )   ; ASCII only
//	subscript = "[" ( "*" / 1*DIGIT ) "]"
//
// A bracket is an array subscript; a dotted numeric segment ("tags.0") is a
// field whose name is that digit string, not an array position — the two
// address different values and this suite must not collapse them any more
// than a backend may (see docs/cloud-parity/path-grammar.md and
// [spi.Filter.Path]'s own doc). They are NOT a description of what the
// in-tree backends happen to do — the same input must be classified the same
// way on every backend, and a backend that quietly accepts what the others
// reject has diverged even if nothing visibly breaks on it.
// ---------------------------------------------------------------------------

// filterPathRejects are paths outside the grammar. Each must be refused with
// an error — never answered with an empty result set, which is a legitimate
// answer to a well-formed predicate and must stay distinguishable from a
// malformed one.
//
// One entry deserves naming: "$.status" is in the REJECT set on purpose — a
// bare path is the contract, because spi.ConditionToFilter strips the "$."
// at the wire boundary, so a prefixed path never legitimately reaches a
// plugin. A well-formed bracket subscript ("tags[0]", "tags[*]") is NOT
// here — it is grammar-valid and belongs in the accept table below; only a
// bracket spelling outside the two supported subscript forms stays rejected.
var filterPathRejects = []string{
	"foo';x",                         // quote + semicolon — the shape that diverged
	"status';DROP TABLE entities;--", // full injection attempt
	`foo"bar`,                        // double quote
	`foo\bar`,                        // backslash
	"foo bar",                        // whitespace
	"foo/bar",                        // slash
	"foo*",                           // asterisk
	"foo,bar",                        // comma
	"foo:bar",                        // colon
	"a..b",                           // empty segment
	".status",                        // leading dot (empty first segment)
	"status.",                        // trailing dot
	".",                              // a lone separator
	"a[",                             // unclosed subscript
	"a[-1]",                          // negative index — not one of the two supported forms
	"a[0:2]",                         // slice syntax
	"a[?(@.x)]",                      // filter-expression syntax
	"a[0]b",                          // a name glued directly onto a subscript, no separator
	"a[2147483648]",                  // index overflows int32, the bound (math.MaxInt32+1)
	"a[99999999999999999999]",        // index overflows int64 too, a fortiori
	"$.status",                       // "$."-prefixed — a bare path is the contract
	"$",                              // bare dollar
	"héllo",                          // non-ASCII
	"foo\x00bar",                     // NUL control byte
	// The evaluator's own metacharacters. These are the entries that matter
	// most, because a backend accepting one does not answer an empty page —
	// it answers the WRONG page. Measured against gjson, the evaluator the
	// reference implementation uses in memory: "?" is a single-character key
	// wildcard, so "a?b" is answered by a sibling key "aXb"; "|" is an
	// alternative segment separator, so "a|b" is answered by a nested a→b —
	// the "." collision under another spelling; "#" is the array
	// count/projection segment; and "!" introduces a literal, so "!true"
	// evaluates to true whatever the document holds. A backend with a
	// different evaluator must still refuse them: the grammar is the
	// contract, and its own metacharacters have to be a subset of what the
	// grammar already excludes or it has the same defect under another name.
	"foo?bar", // single-character key wildcard
	"foo#",    // array count/projection segment
	"foo|bar", // alternative segment separator
	"!true",   // literal, independent of the document
}

// filterPathAcceptsData are well-formed SourceData paths that must keep
// working. Without this half the grammar could be satisfied by a backend that
// rejects everything.
var filterPathAcceptsData = []string{
	"status",    // single segment
	"user_name", // underscore
	"user-name", // hyphen — a valid JSON key, and safe inside a quoted path literal
	"Status9",   // mixed case and digits
	"a.b.c",     // nested
	// A dotted digit segment addresses a FIELD NAMED "0" — an ordinary name
	// under the grammar (name = ALPHA / DIGIT / "_" / "-"). It is NOT how an
	// array position is addressed; that is tags[0] below. Collapsing the two
	// is exactly the defect this table exists to catch.
	"tags.0",
	"tags[0]",          // array position via a positional bracket subscript
	"tags[*]",          // array position via a wildcard bracket subscript
	"items[*].sku",     // chained: a wildcard subscript followed by a nested field
	"tags[2147483647]", // largest index that fits int32 (math.MaxInt32) — must still be accepted
}

// filterPathAcceptsMeta is the canonical meta vocabulary (see OrderSpec's doc
// comment). Every name in it is grammar-valid, so a filter on one must be
// accepted; a backend that applies its meta SORT allowlist to meta FILTER
// paths would wrongly reject some of these.
var filterPathAcceptsMeta = []string{
	"id",
	"state",
	"creationDate",
	"lastUpdateTime",
	"transitionForLatestSave",
	"transactionId",
}

// filterPathExec runs one filter through a backend entry point and reports
// how that entry point answered: nil for accepted, non-nil for refused. An
// entry point that streams (Iterate) must additionally have yielded nothing
// when it refuses — an error alongside rows is not a refusal.
type filterPathExec func(t *testing.T, filter spi.Filter) error

// malformedPathFilter builds the realistic shape a malformed path arrives in:
// an ordinary typed equality leaf. The operator is irrelevant to the outcome —
// path validation precedes evaluation — but an eq leaf is what a real
// mistyped condition translates to.
func malformedPathFilter(source spi.FieldSource, path string) spi.Filter {
	return spi.Filter{
		Op:       spi.FilterEq,
		Source:   source,
		Path:     path,
		Value:    "irrelevant",
		Declared: []spi.DataType{spi.String},
	}
}

// wellFormedPathFilter builds the positive-control leaf. FilterNotNull is
// chosen deliberately: it is a presence test the kernel resolves without
// consulting declared types (see ConditionToFilter's doc comment on the
// kindUnary arm), so a failure here can only mean the PATH was refused — it
// cannot be a type-coercion accident on some field whose declared type the
// conformance harness has no schema for.
func wellFormedPathFilter(source spi.FieldSource, path string) spi.Filter {
	return spi.Filter{Op: spi.FilterNotNull, Source: source, Path: path}
}

// runFilterPathGrammar drives the full grammar table through one entry point.
// entry names it for failure messages.
func runFilterPathGrammar(t *testing.T, entry string, exec filterPathExec) {
	t.Helper()
	sources := []spi.FieldSource{spi.SourceData, spi.SourceMeta}

	// Rejects, at both sources. Both are held to the same grammar: a
	// backend that validates only the source it interpolates into SQL leaves
	// the other one open.
	for _, source := range sources {
		for _, path := range filterPathRejects {
			err := exec(t, malformedPathFilter(source, path))
			if err == nil {
				t.Errorf("%s with %s path %q was accepted; a path outside the Filter.Path grammar must be refused with an error, not answered with an empty result set",
					entry, source, path)
				continue
			}
			if !errors.Is(err, spi.ErrInvalidFilterPath) {
				t.Errorf("%s with %s path %q was refused with %v; the refusal must wrap spi.ErrInvalidFilterPath so callers can classify a malformed path without knowing the backend",
					entry, source, path, err)
			}
		}
	}

	// A malformed path nested under a tree operator is still malformed —
	// validation must walk the whole tree, not just the root.
	nested := spi.Filter{Op: spi.FilterAnd, Children: []spi.Filter{
		wellFormedPathFilter(spi.SourceData, "status"),
		malformedPathFilter(spi.SourceData, "foo';x"),
	}}
	switch err := exec(t, nested); {
	case err == nil:
		t.Errorf("%s accepted a malformed path nested under an and-branch; path validation must walk the whole filter tree", entry)
	case !errors.Is(err, spi.ErrInvalidFilterPath):
		t.Errorf("%s refused a malformed path nested under an and-branch with %v; the refusal must wrap spi.ErrInvalidFilterPath", entry, err)
	}

	// A malformed path nested under a branch node that is neither And nor
	// Or is still malformed. FilterNot does not exist on this SPI version
	// yet, so an unrecognised Op carrying Children stands in for it: to a
	// validator that only recurses on a case list of named branch operators,
	// this node is indistinguishable from any other operator it has never
	// seen, which is exactly the shape a NOT node will have until every
	// backend is rebuilt against the SPI version that defines FilterNot. A
	// validator that recurses on "does this node have children" rather than
	// on the operator's name must still catch it.
	nestedUnderUnknownBranch := spi.Filter{Op: spi.FilterOp("not"), Children: []spi.Filter{
		malformedPathFilter(spi.SourceData, "foo';x"),
	}}
	switch err := exec(t, nestedUnderUnknownBranch); {
	case err == nil:
		t.Errorf("%s accepted a malformed path nested under a non-and/or branch node; path validation must walk any node carrying Children, not a fixed list of branch operators", entry)
	case !errors.Is(err, spi.ErrInvalidFilterPath):
		t.Errorf("%s refused a malformed path nested under a non-and/or branch node with %v; the refusal must wrap spi.ErrInvalidFilterPath", entry, err)
	}

	// Accepts: the grammar must not have been satisfied by refusing
	// everything.
	for _, path := range filterPathAcceptsData {
		if err := exec(t, wellFormedPathFilter(spi.SourceData, path)); err != nil {
			t.Errorf("%s with well-formed data path %q was refused: %v", entry, path, err)
		}
	}
	for _, path := range filterPathAcceptsMeta {
		if err := exec(t, wellFormedPathFilter(spi.SourceMeta, path)); err != nil {
			t.Errorf("%s with canonical meta path %q was refused: %v", entry, path, err)
		}
	}
}

// testSearcherFilterPathGrammar holds Search to the Filter.Path grammar.
//
// The model is seeded first so a wrongly-accepting backend has real rows to
// scan: the failure mode this guards against is answering a malformed path
// with a silently empty page, which is only distinguishable from a correct
// refusal when the model is non-empty.
func testSearcherFilterPathGrammar(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		seedSearcherEntities(t, txCtx, es, searcherSeedOrder)
	})

	es, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	searcher := es.(spi.Searcher)

	runFilterPathGrammar(t, "Search", func(t *testing.T, filter spi.Filter) error {
		// Limit is comfortably above the seeded match count, so
		// bounded-or-fail can never be what produces the error.
		_, err := searcher.Search(ctx, filter, spi.SearchOptions{
			ModelName:    searcherModel,
			ModelVersion: "1",
			Limit:        1000,
		})
		return err
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
