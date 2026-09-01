package spi

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestPrepare_CompilesRegexExactlyOncePerQuery is the test the whole change
// exists for. regexp.Compile is query-invariant work; before the split it ran
// once per candidate entity because MATCHES_PATTERN and LIKE never reached a
// fast path.
//
// Must NOT be t.Parallel() and must not overlap any other test that touches
// compileRegex — the indirection swap is itself a data race otherwise.
func TestPrepare_CompilesRegexExactlyOncePerQuery(t *testing.T) {
	for _, op := range []FilterOp{FilterMatchesRegex} {
		t.Run(string(op), func(t *testing.T) {
			calls := 0
			orig := compileRegex
			compileRegex = func(expr string) (*regexp.Regexp, error) {
				calls++
				return orig(expr)
			}
			defer func() { compileRegex = orig }()

			operand := "A.*"
			p := mustPrepare(t, Filter{
				Op:       op,
				Source:   SourceData,
				Path:     "name",
				Value:    operand,
				Declared: []DataType{String},
			})

			if calls != 1 {
				t.Fatalf("Prepare compiled %d times, want exactly 1", calls)
			}

			data := []byte(`{"name":"Alice"}`)
			for i := 0; i < 1000; i++ {
				if !p.Match(data, EntityMeta{}) {
					t.Fatalf("Match = false on row %d, want true", i)
				}
			}

			if calls != 1 {
				t.Errorf("compiled %d times across Prepare + 1000 Match calls, want exactly 1", calls)
			}
		})
	}
}

// TestPrepare_TokenisesLikeExactlyOncePerQuery mirrors
// TestPrepare_CompilesRegexExactlyOncePerQuery for LIKE. FilterLike was
// dropped from that test when LIKE stopped reaching compileRegex (it has its
// own glob tokeniser, parseLikePattern), but nothing replaced the guard —
// this proves tokenisation still happens once per query, at Prepare time,
// rather than once per row. Without it a future refactor moving
// parseLikePattern into the per-row path would pass the rest of the suite.
//
// Must NOT be t.Parallel() and must not overlap any other test that touches
// parseLikePattern — the indirection swap is itself a data race otherwise.
func TestPrepare_TokenisesLikeExactlyOncePerQuery(t *testing.T) {
	calls := 0
	orig := parseLikePattern
	parseLikePattern = func(operand string) (patternMatcher, error) {
		calls++
		return orig(operand)
	}
	defer func() { parseLikePattern = orig }()

	operand := "A%"
	p := mustPrepare(t, Filter{
		Op:       FilterLike,
		Source:   SourceData,
		Path:     "name",
		Value:    operand,
		Declared: []DataType{String},
	})

	if calls != 1 {
		t.Fatalf("Prepare tokenised %d times, want exactly 1", calls)
	}

	data := []byte(`{"name":"Alice"}`)
	for i := 0; i < 1000; i++ {
		if !p.Match(data, EntityMeta{}) {
			t.Fatalf("Match = false on row %d, want true", i)
		}
	}

	if calls != 1 {
		t.Errorf("tokenised %d times across Prepare + 1000 Match calls, want exactly 1", calls)
	}
}

// TestEvalLeaf_AnchoredPatternMatchesWholeValue asserts that an anchored
// regex pattern leaf matches a value equal to the whole pattern and rejects a
// value that only matches a prefix of it.
func TestEvalLeaf_AnchoredPatternMatchesWholeValue(t *testing.T) {
	exp, err := ExpandLeaf(FilterMatchesRegex, "A.*e", nil, []DataType{String})
	if err != nil {
		t.Fatalf("ExpandLeaf: %v", err)
	}
	if !EvalLeaf(exp, gjson.Parse(`"Alice"`)) {
		t.Error("EvalLeaf = false for a matching anchored pattern, want true")
	}
	if EvalLeaf(exp, gjson.Parse(`"Alicia"`)) {
		t.Error("EvalLeaf = true for a non-matching value, want false")
	}
}

// TestPrepare_Not pins the basic NOT semantics: NOT inverts whether the
// child leaf matched, including on an absent field, where the leaf is false
// and NOT is therefore true (vacuous truth, not a special case).
func TestPrepare_Not(t *testing.T) {
	inner := Filter{Op: FilterEq, Path: "s", Source: SourceData,
		Value: "x", Declared: []DataType{String}}
	p, err := Prepare(Filter{Op: FilterNot, Children: []Filter{inner}})
	require.NoError(t, err)
	require.False(t, p.Match([]byte(`{"s":"x"}`), EntityMeta{}))
	require.True(t, p.Match([]byte(`{"s":"y"}`), EntityMeta{}))
	require.True(t, p.Match([]byte(`{}`), EntityMeta{}), "absent field: leaf is false, NOT is true")
}

// TestPrepare_NotOverWildcardIsUniversal pins the headline behaviour: NOT
// over a wildcard path is a universal quantifier ("no element matches"), not
// the same question as the corresponding negative operator applied
// element-wise ("some element differs"). NOT($.tags[*] EQUALS "red") means no
// tag is "red"; $.tags[*] NOT_EQUAL "red" means some tag differs from "red" —
// for ["red","blue"] the first is false and the second is true.
func TestPrepare_NotOverWildcardIsUniversal(t *testing.T) {
	inner := Filter{Op: FilterEq, Path: "tags[*]", Source: SourceData,
		Value: "red", Declared: []DataType{String}}
	p, err := Prepare(Filter{Op: FilterNot, Children: []Filter{inner}})
	require.NoError(t, err)
	require.False(t, p.Match([]byte(`{"tags":["red","blue"]}`), EntityMeta{}))
	require.True(t, p.Match([]byte(`{"tags":["blue"]}`), EntityMeta{}))
	require.True(t, p.Match([]byte(`{"tags":[]}`), EntityMeta{}), "vacuously true")
	require.True(t, p.Match([]byte(`{}`), EntityMeta{}), "vacuously true")

	// The headline contrast, as an assertion rather than only in prose:
	// NOT($.tags[*] EQUALS "red") ("no tag is red") is a different question
	// from $.tags[*] NOT_EQUAL "red" ("some tag differs from red"). On
	// ["red","blue"] the first is false (a "red" element is present) and the
	// second is true (the "blue" element differs) — the two must disagree
	// here, not merely both be computable.
	notEqual := Filter{Op: FilterNe, Path: "tags[*]", Source: SourceData,
		Value: "red", Declared: []DataType{String}}
	pNotEqual := mustPrepare(t, notEqual)
	data := []byte(`{"tags":["red","blue"]}`)
	require.False(t, p.Match(data, EntityMeta{}), "NOT(EQUALS) over the wildcard: no element is red")
	require.True(t, pNotEqual.Match(data, EntityMeta{}), "NOT_EQUAL over the wildcard: some element (blue) differs")
}

// TestPrepare_MalformedNotFailsClosed pins that a FilterNot node with an
// arity other than exactly one child, or whose single child is unevaluable
// (including a zero-Op child), fails Prepare rather than being guessed at —
// never "invert the AND of the children".
//
// The two-child case deliberately uses TWO WELL-FORMED leaves, not two
// zero-Op children: a pair of zero-Op children is already rejected one level
// down by ExpandLeaf's unsupported-operator arm (the same path the
// single-zero-Op-child case below takes), so it never actually exercises the
// arity guard in prepareNode's FilterNot case. Weakening that guard from
// "!= 1" to "< 1" still passes the whole suite if this case carries
// unevaluable children — it silently prepares a 2-child NOT and negates only
// Children[0], discarding the rest. Two well-formed leaves is the only shape
// that isolates the arity check itself.
func TestPrepare_MalformedNotFailsClosed(t *testing.T) {
	leafA := Filter{Op: FilterEq, Path: "a", Source: SourceData,
		Value: "x", Declared: []DataType{String}}
	leafB := Filter{Op: FilterEq, Path: "b", Source: SourceData,
		Value: "y", Declared: []DataType{String}}
	for _, f := range []Filter{
		{Op: FilterNot},
		{Op: FilterNot, Children: []Filter{}},
		{Op: FilterNot, Children: []Filter{leafA, leafB}}, // well-formed, but arity 2
		{Op: FilterNot, Children: []Filter{{}}},           // zero-Op child
	} {
		_, err := Prepare(f)
		require.Error(t, err, "a malformed NOT must fail Prepare, never invert")
	}
}

// TestPrepare_NotDoubleNegationRestoresOriginal pins NOT(NOT(x)) == x:
// negating twice returns exactly the child's own match answer on every kind
// of input — an ordinary match, an ordinary non-match, and a vacuous
// non-match — since FilterNot's match is a plain boolean flip with no
// normalisation that could make double negation diverge from the original.
func TestPrepare_NotDoubleNegationRestoresOriginal(t *testing.T) {
	leaf := Filter{Op: FilterEq, Path: "s", Source: SourceData,
		Value: "x", Declared: []DataType{String}}
	single := mustPrepare(t, leaf)
	doubled := mustPrepare(t, Filter{Op: FilterNot, Children: []Filter{
		{Op: FilterNot, Children: []Filter{leaf}},
	}})
	for _, data := range [][]byte{
		[]byte(`{"s":"x"}`),
		[]byte(`{"s":"y"}`),
		[]byte(`{}`),
	} {
		require.Equal(t, single.Match(data, EntityMeta{}), doubled.Match(data, EntityMeta{}),
			"NOT(NOT(x)) must answer exactly as x does, for %s", data)
	}
}

// TestPrepare_NotOverEmptyGroupIsTheGroupsComplement pins NOT(AND[]) = false
// and NOT(OR[]) = true. An empty AND already matches everything and an empty
// OR already matches nothing (both pre-existing identities, unrelated to
// NOT), so negating each is a plain complement — FilterNot needs no
// special-casing for an empty-children group underneath it.
func TestPrepare_NotOverEmptyGroupIsTheGroupsComplement(t *testing.T) {
	notAnd := mustPrepare(t, Filter{Op: FilterNot, Children: []Filter{{Op: FilterAnd}}})
	notOr := mustPrepare(t, Filter{Op: FilterNot, Children: []Filter{{Op: FilterOr}}})
	data := []byte(`{"anything":"whatsoever"}`)
	require.False(t, notAnd.Match(data, EntityMeta{}), "NOT(AND[]) must be false: empty AND matches everything")
	require.True(t, notOr.Match(data, EntityMeta{}), "NOT(OR[]) must be true: empty OR matches nothing")
}

// TestPrepare_NotVacuousOverExplicitNull extends the vacuity table to an
// EXPLICIT null, distinct from an absent field: a JSON null is a present
// value (gjson Type Null, Exists() true — see TestResolvePath's "bare over
// null" / "wildcard over null" cases), not an absent one, but it still makes
// the child leaf false on both a scalar and a wildcard path, so NOT is still
// true.
func TestPrepare_NotVacuousOverExplicitNull(t *testing.T) {
	scalar := Filter{Op: FilterEq, Path: "s", Source: SourceData,
		Value: "x", Declared: []DataType{String}}
	require.True(t, mustPrepare(t, Filter{Op: FilterNot, Children: []Filter{scalar}}).
		Match([]byte(`{"s":null}`), EntityMeta{}), "NOT over an explicit scalar null must be true")

	wildcard := Filter{Op: FilterEq, Path: "tags[*]", Source: SourceData,
		Value: "red", Declared: []DataType{String}}
	require.True(t, mustPrepare(t, Filter{Op: FilterNot, Children: []Filter{wildcard}}).
		Match([]byte(`{"tags":null}`), EntityMeta{}),
		"NOT over a wildcard addressing an explicit null list must be true: a null array presents no elements")
}

// TestPrepare_NotIsNullDiffersFromNotNullOnWildcard pins that NOT($.tags[*]
// IS_NULL) ("no element is null") is a different question from $.tags[*]
// NOT_NULL ("some element is present and non-null") — the same
// universal-vs-existential asymmetry as the EQUALS/NOT_EQUAL contrast. The
// vacuity rule (docs/cloud-parity/path-grammar.md section 5: IS_NULL and
// NOT_NULL both answer false over an empty/null/absent wildcard, because
// neither addresses any element) makes the two diverge even more starkly on
// an empty array: NOT(IS_NULL) is vacuously TRUE there while NOT_NULL is
// FALSE on the very same input.
func TestPrepare_NotIsNullDiffersFromNotNullOnWildcard(t *testing.T) {
	isNull := Filter{Op: FilterIsNull, Path: "tags[*]", Source: SourceData}
	notNull := Filter{Op: FilterNotNull, Path: "tags[*]", Source: SourceData}
	notIsNull := mustPrepare(t, Filter{Op: FilterNot, Children: []Filter{isNull}})
	preparedNotNull := mustPrepare(t, notNull)

	empty := []byte(`{"tags":[]}`)
	require.True(t, notIsNull.Match(empty, EntityMeta{}), "NOT(IS_NULL) over an empty array: vacuously true")
	require.False(t, preparedNotNull.Match(empty, EntityMeta{}), "NOT_NULL over an empty array: vacuously false (section 5)")

	mixed := []byte(`{"tags":[null,"x"]}`)
	require.False(t, notIsNull.Match(mixed, EntityMeta{}), `NOT(IS_NULL): one element IS null, so NOT is false`)
	require.True(t, preparedNotNull.Match(mixed, EntityMeta{}), `NOT_NULL: one element ("x") is present and non-null`)
}
