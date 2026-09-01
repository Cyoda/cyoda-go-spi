package spitest

import (
	"testing"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// filter_not.go pins spi.FilterNot's universal-quantifier semantics and its
// arity guard at every filter-taking entry point the suite covers (Searcher,
// Iterable). Without cases here, [spi.FilterNot]'s doc comment and
// docs/cloud-parity/path-grammar.md section 5 are advice a backend can drift
// from silently — nothing in the harness previously ran a FilterNot through a
// real backend.

// filterNotModel is the model FilterNot subtests seed into. Each subtest
// runs under a fresh tenant (see tenantContext), so reusing a fixed name
// across the Searcher and Iterable suites is collision-free — the same
// convention iterableModelRef already uses for searcherModel.
const filterNotModel = "filter-not"

// filterNotFixture seeds four entities that together pin the headline
// contrast and the vacuity rule in one pass:
//
//   - an entity whose tags contain "red" is the only NON-match: NOT($.tags[*]
//     EQUALS "red") is false exactly when some element IS "red".
//   - an entity whose tags do not contain "red" matches: an ordinary,
//     non-vacuous "no element is red".
//   - an entity with an empty tags array matches vacuously: no element
//     exists to be "red" (docs/cloud-parity/path-grammar.md section 5).
//   - an entity with no "tags" field at all matches vacuously too: an absent
//     field addresses no element either (mirrors TestPrepare_Not's "absent
//     field: leaf is false, NOT is true" in the kernel-level suite).
//
// label lets assertions identify which case a returned entity is without
// re-deriving the answer from tags, which would just restate the predicate
// under test.
type filterNotFixtureEntry struct {
	label   string
	tags    []string
	noTags  bool
	matches bool
}

var filterNotFixture = []filterNotFixtureEntry{
	{label: "contains-red", tags: []string{"red", "blue"}, matches: false},
	{label: "no-red", tags: []string{"blue", "green"}, matches: true},
	{label: "empty-array", tags: []string{}, matches: true},
	{label: "absent-field", noTags: true, matches: true},
}

// filterNotMatchN is the number of filterNotFixture entries NOT($.tags[*]
// EQUALS "red") must select, derived rather than declared so the two can
// never drift apart.
var filterNotMatchN = countFilterNotMatches(filterNotFixture)

func countFilterNotMatches(fixture []filterNotFixtureEntry) int {
	n := 0
	for _, f := range fixture {
		if f.matches {
			n++
		}
	}
	return n
}

// seedFilterNotEntities saves filterNotFixture into filterNotModel.
func seedFilterNotEntities(t *testing.T, ctx spiCtx, es spi.EntityStore) {
	t.Helper()
	for _, f := range filterNotFixture {
		payload := map[string]any{"label": f.label}
		if !f.noTags {
			payload["tags"] = f.tags
		}
		_, err := es.Save(ctx, newEntity(t, filterNotModel, newID(), payload))
		require.NoError(t, err)
	}
}

// filterNotTagsFilter builds NOT($.tags[*] EQUALS "red") over the seeded
// fixture's data field.
func filterNotTagsFilter() spi.Filter {
	return spi.Filter{
		Op: spi.FilterNot,
		Children: []spi.Filter{{
			Op:       spi.FilterEq,
			Source:   spi.SourceData,
			Path:     "tags[*]",
			Value:    "red",
			Declared: []spi.DataType{spi.String},
		}},
	}
}

// malformedFilterNotZeroChildren and malformedFilterNotTwoChildren are the
// two arities [spi.Prepare] must reject — Children of length 0 or length >=
// 2 — rather than guessing an "invert the AND of the children" reading (see
// Filter.Children's doc comment and TestPrepare_MalformedNotFailsClosed).
// Both children in the two-child case are well-formed leaves: a pair of
// zero-Op children would be rejected one level down by the leaf's own
// unsupported-operator arm without ever reaching FilterNot's arity guard, so
// they would not isolate the property this case exists to pin.
func malformedFilterNotZeroChildren() spi.Filter {
	return spi.Filter{Op: spi.FilterNot}
}

func malformedFilterNotTwoChildren() spi.Filter {
	return spi.Filter{Op: spi.FilterNot, Children: []spi.Filter{
		{Op: spi.FilterNotNull, Source: spi.SourceData, Path: "label"},
		{Op: spi.FilterNotNull, Source: spi.SourceData, Path: "label"},
	}}
}

// filterNotExec runs one filter through a search entry point and returns the
// matched entities plus the error that entry point reported. Unlike
// filterPathExec (searcher.go), FilterNot conformance is not just
// accept/reject — it is a specific matched set, so the exec needs the rows
// back.
type filterNotExec func(t *testing.T, filter spi.Filter) ([]*spi.Entity, error)

// runFilterNotConformance drives FilterNot's universal-quantifier reading
// and its arity guard through one filter-taking entry point. entry names it
// for failure messages. The caller must have already seeded
// filterNotFixture via seedFilterNotEntities under the same model/tenant
// exec reads from.
func runFilterNotConformance(t *testing.T, entry string, exec filterNotExec) {
	t.Helper()

	t.Run("UniversalQuantifier", func(t *testing.T) {
		got, err := exec(t, filterNotTagsFilter())
		require.NoError(t, err)
		require.Len(t, got, filterNotMatchN,
			"%s: NOT($.tags[*] EQUALS \"red\") must select exactly the entities where no element equals \"red\" (the universal-quantifier reading)", entry)
		for _, e := range got {
			require.NotEqual(t, "contains-red", entityDataString(t, e, "label"),
				"%s: NOT($.tags[*] EQUALS \"red\") matched an entity that has a \"red\" element", entry)
		}
	})

	t.Run("Malformed/ZeroChildren", func(t *testing.T) {
		got, err := exec(t, malformedFilterNotZeroChildren())
		require.Error(t, err, "%s: a FilterNot with zero children must fail rather than match", entry)
		require.Empty(t, got, "a rejected FilterNot must not also return a partial match set")
		require.ErrorIs(t, err, spi.ErrUnevaluableLeaf,
			"%s: the refusal must wrap spi.ErrUnevaluableLeaf", entry)
	})

	t.Run("Malformed/TwoChildren", func(t *testing.T) {
		got, err := exec(t, malformedFilterNotTwoChildren())
		require.Error(t, err, "%s: a FilterNot with two children must fail rather than match", entry)
		require.Empty(t, got, "a rejected FilterNot must not also return a partial match set")
		require.ErrorIs(t, err, spi.ErrUnevaluableLeaf,
			"%s: the refusal must wrap spi.ErrUnevaluableLeaf", entry)
	})
}

// testSearcherFilterNot holds Search to FilterNot's universal-quantifier
// reading and arity guard.
func testSearcherFilterNot(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	withTx(t, h, ctx, func(txCtx spiCtx) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		seedFilterNotEntities(t, txCtx, es)
	})

	es, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	searcher := es.(spi.Searcher)

	runFilterNotConformance(t, "Search", func(t *testing.T, filter spi.Filter) ([]*spi.Entity, error) {
		return searcher.Search(ctx, filter, spi.SearchOptions{
			ModelName:    filterNotModel,
			ModelVersion: "1",
			Limit:        1000,
		})
	})
}

// testIterableFilterNot holds Iterate to the same FilterNot properties as
// testSearcherFilterNot, using the same table — both are filter-taking entry
// points on the same contract (mirrors testIterableFilterPathGrammar's reuse
// of runFilterPathGrammar).
func testIterableFilterNot(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	withTx(t, h, ctx, func(txCtx spiCtx) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		seedFilterNotEntities(t, txCtx, es)
	})

	store, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	iterable := store.(spi.Iterable)
	mref := spi.ModelRef{EntityName: filterNotModel, ModelVersion: "1"}

	runFilterNotConformance(t, "Iterate", func(t *testing.T, filter spi.Filter) ([]*spi.Entity, error) {
		it, err := iterable.Iterate(ctx, mref, filter, spi.IterateOptions{})
		if err != nil {
			return nil, err
		}
		got, err := drainIterator(t, it)
		if err != nil {
			require.Empty(t, got, "an iterator that refuses a filter must not also have yielded rows")
		}
		return got, err
	})
}
