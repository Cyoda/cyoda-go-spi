package spitest

import (
	"context"
	"testing"

	spi "github.com/cyoda-platform/cyoda-go-spi"
	"github.com/stretchr/testify/require"
)

const patternModel = "spitest_pattern"

// patternSeed maps an entity's "name" value to the label used in failures.
// The values are chosen so each grammar rule has both a match and a decoy.
var patternSeed = []string{
	"d",    // \d must match this
	"7",    // \d must NOT match this (it is not a digit class)
	`\d`,   // \d must NOT match this either
	"100%", // \% matches the literal percent
	`a\b`,  // \\ matches the literal backslash
	"a\nb", // % and _ must reach a newline
	"abc",
}

// testPatternLikeGrammar pins the LIKE grammar through the Searcher surface.
// LIKE is a glob: '%' is any run INCLUDING newlines, '_' is exactly one rune
// INCLUDING a newline, and '\X' is the literal X for any X. It is NOT a regex,
// and a backend translating it to one will fail these rows.
func testPatternLikeGrammar(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	searcher := seedPatternEntities(t, h, ctx)

	cases := []struct {
		name    string
		op      spi.FilterOp
		operand string
		want    []string // the "name" values that must match, in no order
	}{
		{"EscapedLetterIsLiteral", spi.FilterLike, `\d`, []string{"d"}},
		{"EscapedPercentIsLiteral", spi.FilterLike, `100\%`, []string{"100%"}},
		{"DoubledEscapeIsOneBackslash", spi.FilterLike, `a\\b`, []string{`a\b`}},
		{"AnyRunReachesNewline", spi.FilterLike, `a%b`, []string{"a\nb", `a\b`}},
		{"OneCharReachesNewline", spi.FilterLike, `a_b`, []string{"a\nb", `a\b`}},
		{"AnyRunMatchesEverything", spi.FilterLike, `%`, patternSeed},
		{"WholeStringAnchored", spi.FilterLike, `bc`, nil},
		{"CaseSensitive", spi.FilterLike, `ABC`, nil},
		// MATCHES_PATTERN is a real regex, and is whole-string anchored.
		// '.' does NOT match a newline without an explicit (?s) flag — that
		// is standard regexp.Regexp behaviour, not a LIKE-style glob rule —
		// so "a\nb" is excluded here even though the LIKE rows above treat
		// '%'/'_' as newline-reaching.
		{"RegexIsAnchored", spi.FilterMatchesRegex, `b`, nil},
		{"RegexWholeString", spi.FilterMatchesRegex, `a.b`, []string{`a\b`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := searchPatternNames(t, ctx, searcher, c.op, c.operand)
			require.ElementsMatch(t, c.want, got,
				"%s %q selected the wrong set", c.op, c.operand)
		})
	}
}

// testPatternMalformedLike pins what a backend does with the ONE malformed
// LIKE operand: a trailing unpaired escape.
//
// The contract is Prepare's: "a leaf whose operand cannot be expanded becomes a
// leaf that never matches". So Search returns NO error and NO rows. Rejecting
// it with a 400 is the request boundary's job, above the Searcher, and failing
// the search here instead is a divergence — that is exactly the split this case
// exists to catch.
func testPatternMalformedLike(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	searcher := seedPatternEntities(t, h, ctx)

	for _, operand := range []string{`a\`, `\`} {
		res, err := searcher.Search(ctx, spi.Filter{
			Op:       spi.FilterLike,
			Source:   spi.SourceData,
			Path:     "name",
			Value:    operand,
			Declared: []spi.DataType{spi.String},
		}, spi.SearchOptions{ModelName: patternModel, ModelVersion: "1", Limit: 1000})
		require.NoError(t, err, "malformed LIKE operand %q must not fail the search", operand)
		require.Empty(t, res, "malformed LIKE operand %q must match nothing", operand)
	}
}

func seedPatternEntities(t *testing.T, h Harness, ctx context.Context) spi.Searcher {
	t.Helper()
	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		for _, name := range patternSeed {
			_, err := es.Save(txCtx, newEntity(t, patternModel, newID(), map[string]any{"name": name}))
			require.NoError(t, err)
		}
	})
	es, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	return es.(spi.Searcher)
}

func searchPatternNames(t *testing.T, ctx context.Context, s spi.Searcher, op spi.FilterOp, operand string) []string {
	t.Helper()
	res, err := s.Search(ctx, spi.Filter{
		Op:       op,
		Source:   spi.SourceData,
		Path:     "name",
		Value:    operand,
		Declared: []spi.DataType{spi.String},
	}, spi.SearchOptions{ModelName: patternModel, ModelVersion: "1", Limit: 1000})
	require.NoError(t, err)
	names := make([]string, 0, len(res))
	for _, e := range res {
		names = append(names, entityDataString(t, e, "name"))
	}
	return names
}
