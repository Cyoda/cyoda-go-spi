package spi

import (
	"regexp"
	"testing"

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
	for _, op := range []FilterOp{FilterMatchesRegex, FilterLike} {
		t.Run(string(op), func(t *testing.T) {
			calls := 0
			orig := compileRegex
			compileRegex = func(expr string) (*regexp.Regexp, error) {
				calls++
				return orig(expr)
			}
			defer func() { compileRegex = orig }()

			operand := "A.*"
			if op == FilterLike {
				operand = "A%"
			}
			p := Prepare(Filter{
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

// TestEvalLeaf_UsesStoredRawForRegex guards the indirection itself: swapping
// compileRegex must not change what a pattern leaf answers.
func TestEvalLeaf_UsesStoredRawForRegex(t *testing.T) {
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
