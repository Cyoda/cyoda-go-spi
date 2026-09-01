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
}

// TestPrepare_MalformedNotFailsClosed pins that a FilterNot node with an
// arity other than exactly one child, or whose single child is unevaluable
// (including a zero-Op child), fails Prepare rather than being guessed at —
// never "invert the AND of the children".
func TestPrepare_MalformedNotFailsClosed(t *testing.T) {
	for _, f := range []Filter{
		{Op: FilterNot},
		{Op: FilterNot, Children: []Filter{}},
		{Op: FilterNot, Children: []Filter{{}, {}}},
		{Op: FilterNot, Children: []Filter{{}}}, // zero-Op child
	} {
		_, err := Prepare(f)
		require.Error(t, err, "a malformed NOT must fail Prepare, never invert")
	}
}
