package spi_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// TestPrepare_RejectsUnevaluableLeaf pins spec §4.3/§9: a leaf Prepare cannot
// evaluate is rejected with an error wrapping ErrUnevaluableLeaf, not silently
// turned into a leaf that never matches. That silent version is safe only
// while the language has no negation — a NOT would invert a never-match leaf
// into matches-everything, so every cause must be decided at prepare time,
// before any entity is read.
func TestPrepare_RejectsUnevaluableLeaf(t *testing.T) {
	cases := []struct {
		name string
		f    spi.Filter
	}{
		{"operand fits no declared type", spi.Filter{
			Op: spi.FilterGt, Path: "n", Source: spi.SourceData, Value: "abc",
			Declared: []spi.DataType{spi.Integer}}},
		{"path outside the grammar", spi.Filter{
			Op: spi.FilterEq, Path: "a[", Source: spi.SourceData, Value: "x",
			Declared: []spi.DataType{spi.String}}},
		{"empty data path", spi.Filter{
			Op: spi.FilterEq, Path: "", Source: spi.SourceData, Value: "x",
			Declared: []spi.DataType{spi.String}}},
		{"pattern will not compile", spi.Filter{
			Op: spi.FilterLike, Path: "s", Source: spi.SourceData, Value: `a\`,
			Declared: []spi.DataType{spi.String}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := spi.Prepare(c.f)
			require.Error(t, err)
			require.ErrorIs(t, err, spi.ErrUnevaluableLeaf)
		})
	}
}

// TestPrepare_UnevaluableLeaf_BoundsOperandInErrorMessage pins the
// security-review fix for prepared_filter.go's own operand echo (the "%w:
// operand %s for op %q: %v" wrap): a Filter's Value can be a search request's
// caller-sized operand (bodies are capped at 10 MiB), and Prepare's error
// text — logged at WARN by internal/domain/search's ClassifyStoreQueryError —
// must not grow linearly with it.
func TestPrepare_UnevaluableLeaf_BoundsOperandInErrorMessage(t *testing.T) {
	huge := strings.Repeat("a", 1<<20) // 1 MiB
	_, err := spi.Prepare(spi.Filter{
		Op: spi.FilterGt, Path: "n", Source: spi.SourceData, Value: huge,
		Declared: []spi.DataType{spi.Integer},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, spi.ErrUnevaluableLeaf)
	msg := err.Error()
	// Two independent truncations compose into this message (Prepare's own
	// wrap and ExpandLeaf's inner error, both capped at
	// maxEchoedOperandBytes), plus fixed wrapper text — a few hundred bytes,
	// nowhere near the 1 MiB operand. 1000 leaves comfortable headroom for
	// wrapper wording changes without chasing an exact byte count.
	if len(msg) > 1000 {
		t.Fatalf("error message not bounded: got %d bytes", len(msg))
	}
	require.NotContains(t, msg, huge, "error message must not contain the full operand")
}

func TestPrepare_AcceptsMatchAllAndOrdinaryLeaves(t *testing.T) {
	for _, f := range []spi.Filter{
		{},
		{Op: spi.FilterEq, Path: "s", Source: spi.SourceData, Value: "x", Declared: []spi.DataType{spi.String}},
		{Op: spi.FilterAnd, Children: []spi.Filter{
			{Op: spi.FilterEq, Path: "s", Source: spi.SourceData, Value: "x", Declared: []spi.DataType{spi.String}}}},
	} {
		_, err := spi.Prepare(f)
		require.NoError(t, err)
	}
}

// TestPrepare_ZeroValueAsymmetry pins spec §3's table: a zero-Op filter is
// match-all at the ROOT only. A zero-Op CHILD is an unevaluable leaf —
// ExpandLeaf hits its default arm ("unsupported leaf operator"), and Prepare
// now rejects the whole request (ErrUnevaluableLeaf) rather than silently
// building a leaf that is false for every row. Hoisting the root's Op == ""
// check into the recursion would instead turn a zero-Op child into an
// identity element, which is the mistake this test guards against — a
// zero-Op child must never be treated as match-all, whether that shows up as
// a wrong Match answer or as Prepare wrongly succeeding.
//
// sqlite depends on the root behaviour in plugins/sqlite/grouped_stats.go,
// which special-cases an empty Op before reaching the evaluator.
func TestPrepare_ZeroValueAsymmetry(t *testing.T) {
	leaf := spi.Filter{
		Op:       spi.FilterEq,
		Source:   spi.SourceData,
		Path:     "name",
		Value:    "Alice",
		Declared: []spi.DataType{spi.String},
	}
	data := []byte(`{"name":"Alice"}`)

	t.Run("root cases", func(t *testing.T) {
		tests := []struct {
			name string
			f    spi.Filter
			want bool
		}{
			{"root zero filter matches all", spi.Filter{}, true},
			{"root empty AND is the AND identity", spi.Filter{Op: spi.FilterAnd}, true},
			{"root empty OR is the OR identity", spi.Filter{Op: spi.FilterOr}, false},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				if got := mustPrepare(t, tc.f).Match(data, spi.EntityMeta{}); got != tc.want {
					t.Errorf("Prepare(%+v).Match() = %v, want %v", tc.f, got, tc.want)
				}
			})
		}
	})

	// A zero-Op child is unevaluable regardless of where it sits in the tree
	// or what its siblings would otherwise decide: Prepare rejects the whole
	// filter rather than computing an AND/OR answer around it.
	t.Run("zero-Op child rejects the whole filter", func(t *testing.T) {
		tests := []struct {
			name string
			f    spi.Filter
		}{
			{
				"zero-Op child under an AND",
				spi.Filter{Op: spi.FilterAnd, Children: []spi.Filter{leaf, {}}},
			},
			{
				"zero-Op child under an OR with a matching sibling",
				spi.Filter{
					Op: spi.FilterOr,
					Children: []spi.Filter{
						{Op: spi.FilterEq, Source: spi.SourceData, Path: "name",
							Value: "Alice", Declared: []spi.DataType{spi.String}},
						{},
					},
				},
			},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				_, err := spi.Prepare(tc.f)
				require.Error(t, err)
				require.ErrorIs(t, err, spi.ErrUnevaluableLeaf)
			})
		}
	})
}

// TestPreparedFilter_ZeroValueMatchesAll pins that the zero PreparedFilter —
// the value a caller gets from a nil-able field or an unassigned variable —
// matches everything, mirroring Prepare(Filter{}). Both spellings of "no
// filter" must agree.
func TestPreparedFilter_ZeroValueMatchesAll(t *testing.T) {
	var p spi.PreparedFilter
	if !p.Match([]byte(`{"a":1}`), spi.EntityMeta{}) {
		t.Error("zero PreparedFilter.Match() = false, want true (match-all)")
	}
}

// TestPreparedFilter_ConcurrentMatch pins that one prepared filter is safe to
// share across goroutines and that they all agree. Asserting agreement, not
// merely absence of a race report, is what catches a lazily-resolved field:
// under -race a torn read shows up as a wrong answer even when the detector
// misses the write.
//
// The commercial Cassandra direct-search fan-out hands one prepared filter to
// N errgroup workers, so this is a real usage shape, not a synthetic one.
func TestPreparedFilter_ConcurrentMatch(t *testing.T) {
	p := mustPrepare(t, spi.Filter{
		Op: spi.FilterOr,
		Children: []spi.Filter{
			{Op: spi.FilterMatchesRegex, Source: spi.SourceData, Path: "name",
				Value: "A.*", Declared: []spi.DataType{spi.String}},
			{Op: spi.FilterGt, Source: spi.SourceData, Path: "qty",
				Value: "10", Declared: []spi.DataType{spi.Integer}},
			{Op: spi.FilterEq, Source: spi.SourceMeta, Path: "state",
				Value: "active", Declared: []spi.DataType{spi.String}},
		},
	})

	rows := []struct {
		data []byte
		meta spi.EntityMeta
		want bool
	}{
		{[]byte(`{"name":"Alice","qty":1}`), spi.EntityMeta{State: "idle"}, true},
		{[]byte(`{"name":"Bob","qty":50}`), spi.EntityMeta{State: "idle"}, true},
		{[]byte(`{"name":"Bob","qty":1}`), spi.EntityMeta{State: "active"}, true},
		{[]byte(`{"name":"Bob","qty":1}`), spi.EntityMeta{State: "idle"}, false},
	}

	const workers = 16
	const iterations = 200

	results := make([][]bool, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			got := make([]bool, 0, len(rows)*iterations)
			for i := 0; i < iterations; i++ {
				for _, r := range rows {
					got = append(got, p.Match(r.data, r.meta))
				}
			}
			results[w] = got
		}(w)
	}
	wg.Wait()

	for w := 0; w < workers; w++ {
		for i, got := range results[w] {
			want := rows[i%len(rows)].want
			if got != want {
				t.Fatalf("worker %d observation %d = %v, want %v", w, i, got, want)
			}
		}
	}
}

// TestPreparedFilter_ResolvesByPathSyntax pins spec sections 3 and 5 of
// docs/cloud-parity/path-grammar.md at the kernel: the leaf addresses exactly
// what the path syntax says, never routing on the stored shape.
func TestPreparedFilter_ResolvesByPathSyntax(t *testing.T) {
	str := []spi.DataType{spi.String}
	cases := []struct {
		name string
		doc  string
		f    spi.Filter
		want bool
	}{
		// A bare path does not unwrap an array.
		{"bare eq over array", `{"a":["A","B"]}`,
			spi.Filter{Op: spi.FilterEq, Path: "a", Source: spi.SourceData, Value: "A", Declared: str}, false},
		{"bare eq over scalar", `{"a":"A"}`,
			spi.Filter{Op: spi.FilterEq, Path: "a", Source: spi.SourceData, Value: "A", Declared: str}, true},

		// A wildcard is existential over the elements and does not wrap a scalar.
		{"wildcard eq over array", `{"a":["A","B"]}`,
			spi.Filter{Op: spi.FilterEq, Path: "a[*]", Source: spi.SourceData, Value: "B", Declared: str}, true},
		{"wildcard eq over scalar", `{"a":"A"}`,
			spi.Filter{Op: spi.FilterEq, Path: "a[*]", Source: spi.SourceData, Value: "A", Declared: str}, false},

		// A trailing wildcard is not the array's length.
		{"wildcard is not length", `{"tags":["red","blue"]}`,
			spi.Filter{Op: spi.FilterEq, Path: "tags[*]", Source: spi.SourceData, Value: "2", Declared: []spi.DataType{spi.Integer}}, false},

		// Vacuity, per path-grammar.md section 5.
		{"bare NOT_NULL over empty array", `{"a":[]}`,
			spi.Filter{Op: spi.FilterNotNull, Path: "a", Source: spi.SourceData}, true},
		{"wildcard NOT_NULL over empty array", `{"a":[]}`,
			spi.Filter{Op: spi.FilterNotNull, Path: "a[*]", Source: spi.SourceData}, false},
		{"wildcard IS_NULL over empty array", `{"a":[]}`,
			spi.Filter{Op: spi.FilterIsNull, Path: "a[*]", Source: spi.SourceData}, false},
		{"wildcard IS_NULL over null", `{"a":null}`,
			spi.Filter{Op: spi.FilterIsNull, Path: "a[*]", Source: spi.SourceData}, false},
		{"wildcard IS_NULL over absent", `{}`,
			spi.Filter{Op: spi.FilterIsNull, Path: "a[*]", Source: spi.SourceData}, false},
		{"positional IS_NULL over empty array", `{"a":[]}`,
			spi.Filter{Op: spi.FilterIsNull, Path: "a[0]", Source: spi.SourceData}, true},

		// An element missing the key is evaluated, not dropped.
		{"element missing key IS_NULL", `{"items":[{"sku":"A"},{}]}`,
			spi.Filter{Op: spi.FilterIsNull, Path: "items[*].sku", Source: spi.SourceData}, true},

		// A numeric segment is a field name.
		{"numeric field name", `{"obj":{"0":"Z"}}`,
			spi.Filter{Op: spi.FilterEq, Path: "obj.0", Source: spi.SourceData, Value: "Z", Declared: str}, true},
		{"numeric segment is not an index", `{"tags":["A"]}`,
			spi.Filter{Op: spi.FilterEq, Path: "tags.0", Source: spi.SourceData, Value: "A", Declared: str}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustPrepare(t, tc.f).Match([]byte(tc.doc), spi.EntityMeta{}); got != tc.want {
				t.Errorf("Match(%s) on %+v = %v, want %v", tc.doc, tc.f, got, tc.want)
			}
		})
	}
}

// TestPreparedFilter_EmptyLeafPathNeverResolves pins that an empty Path on a
// SourceData LEAF addresses nothing. Filter.Path's doc comment used to say an
// empty Path "is legal and is not checked" because the AND/OR tree operators
// carry one instead of a leaf condition — worded so a reader could misread
// leaf-empty-path as an equally-legal alternate spelling, not just the tree
// operators' own case. It never was: before the fix this pins,
// ParseFilterPath("") returned nil hops with no error, and ResolvePath with
// nil hops resolves to the parsed root document — so a SourceData leaf with
// an empty Path matched every entity via NOT_NULL, and even matched via
// EQUALS whenever the operand happened to compare equal to the root's own
// gjson.Result. This is not reachable through HTTP or gRPC today (every
// transport requires a leaf's jsonPath/Path to be non-empty before it ever
// reaches a Filter), but a caller constructing a Filter directly must not
// get an answer-instead-of-refuse leaf.
//
// Since Task 2, a SourceData leaf with an empty Path is unevaluable —
// Prepare rejects it (ErrUnevaluableLeaf) rather than building a leaf whose
// Match answer happens to always be false, for the same reason as every
// other unevaluable-leaf cause: a never-match leaf inverts into
// matches-everything under a NOT. Filter.Path's doc comment now says so
// directly; see TestPreparedFilter_MetaLeafPathMustBeInVocabulary for the
// SourceMeta counterpart.
func TestPreparedFilter_EmptyLeafPathNeverResolves(t *testing.T) {
	cases := []struct {
		name string
		f    spi.Filter
	}{
		{"empty data path, presence test",
			spi.Filter{Op: spi.FilterNotNull, Path: "", Source: spi.SourceData}},
		{"empty data path, string equality",
			spi.Filter{Op: spi.FilterEq, Path: "", Source: spi.SourceData,
				Value: "x", Declared: []spi.DataType{spi.String}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := spi.Prepare(tc.f)
			require.Error(t, err)
			require.ErrorIs(t, err, spi.ErrUnevaluableLeaf)
		})
	}
}

// TestPreparedFilter_MetaLeafPathMustBeInVocabulary pins the SourceMeta
// counterpart to TestPreparedFilter_EmptyLeafPathNeverResolves. An earlier
// version of this test asserted the OPPOSITE — that an empty SourceMeta Path
// "resolves to not-found" and simply never matches, on the reasoning that
// extractFilterMetaValue's switch has no "" case so Match alone already
// answers false. That reasoning is exactly the hazard this task exists to
// close: a leaf that silently never matches today inverts into "matches
// every entity" the moment a NOT wraps it, and the boundary that is meant to
// keep an unrecognized meta field from ever reaching Prepare
// (lifecycleToFilter passes c.Field through unvalidated) is not a reason to
// require the wrong answer underneath it if that boundary is ever bypassed —
// same ruling as the SourceData case.
//
// So both an empty Path and a Path outside the closed meta vocabulary
// (isRecognizedMetaPath / extractFilterMetaValue's keyset) reject with
// ErrUnevaluableLeaf, exactly like the SourceData causes.
func TestPreparedFilter_MetaLeafPathMustBeInVocabulary(t *testing.T) {
	cases := []struct {
		name string
		f    spi.Filter
	}{
		{"empty meta path, presence test",
			spi.Filter{Op: spi.FilterNotNull, Path: "", Source: spi.SourceMeta}},
		{"unrecognized meta path, presence test",
			spi.Filter{Op: spi.FilterNotNull, Path: "bogus", Source: spi.SourceMeta}},
		{"unrecognized meta path, equality",
			spi.Filter{Op: spi.FilterEq, Path: "bogus", Source: spi.SourceMeta,
				Value: "x", Declared: []spi.DataType{spi.String}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := spi.Prepare(tc.f)
			require.Error(t, err)
			require.ErrorIs(t, err, spi.ErrUnevaluableLeaf)
		})
	}
}

// TestPreparedFilter_ValidMetaPathAbsentValueStillNonMatches is the positive
// control: this is a PATH rule, not a value rule. A meta path that IS in the
// vocabulary but whose value happens to be absent on this entity (an unset
// zero time.Time, an empty string) must keep resolving normally and simply
// non-match — it must not be swept into the same rejection as an
// out-of-vocabulary path.
func TestPreparedFilter_ValidMetaPathAbsentValueStillNonMatches(t *testing.T) {
	notNull := spi.Filter{Op: spi.FilterNotNull, Path: "creationDate", Source: spi.SourceMeta}
	if got := mustPrepare(t, notNull).Match(nil, spi.EntityMeta{}); got {
		t.Error("NOT_NULL on an unset creationDate = true, want false (absent value, not a rejected path)")
	}
	isNull := spi.Filter{Op: spi.FilterIsNull, Path: "creationDate", Source: spi.SourceMeta}
	if got := mustPrepare(t, isNull).Match(nil, spi.EntityMeta{}); !got {
		t.Error("IS_NULL on an unset creationDate = false, want true (absent value, not a rejected path)")
	}
}

// TestPreparedFilter_EmptyTreeOperatorStillLegal is the positive control:
// an AND/OR node legitimately carries an empty Path (it addresses no field
// at all — Children carry the real leaves), and
// TestPreparedFilter_EmptyLeafPathNeverResolves's fix must not have made an
// empty-Path tree node itself refuse to prepare or match.
func TestPreparedFilter_EmptyTreeOperatorStillLegal(t *testing.T) {
	f := spi.Filter{
		Op: spi.FilterAnd,
		Children: []spi.Filter{
			{Op: spi.FilterEq, Path: "a", Source: spi.SourceData, Value: "x", Declared: []spi.DataType{spi.String}},
		},
	}
	if got := mustPrepare(t, f).Match([]byte(`{"a":"x"}`), spi.EntityMeta{}); !got {
		t.Errorf("Match on AND node with empty Path = false, want true: an empty Path on a tree operator stays legal")
	}
}

// TestPreparedFilter_MalformedPathNeverResolves pins path-grammar.md's
// requirement that a path outside the documented grammar never resolves to
// anything. Since Task 2 that requirement is enforced by rejecting the leaf
// at Prepare time (ErrUnevaluableLeaf) rather than by building a leaf whose
// Match answer happens to always be false.
//
// The two rows pin parse failure on both sides of a boundary inside
// ParseFilterPath: row one's unclosed-bracket check fires before any hop is
// appended, while "a[0]b"'s trailing-garbage check runs AFTER the "a[0]" hop
// has already been appended.
func TestPreparedFilter_MalformedPathNeverResolves(t *testing.T) {
	cases := []struct {
		name string
		f    spi.Filter
	}{
		{"unclosed bracket, presence test",
			spi.Filter{Op: spi.FilterNotNull, Path: "a[", Source: spi.SourceData}},
		{"trailing char after subscript, matching comparison",
			spi.Filter{Op: spi.FilterEq, Path: "a[0]b", Source: spi.SourceData,
				Value: "x", Declared: []spi.DataType{spi.String}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := spi.Prepare(tc.f)
			require.Error(t, err)
			require.ErrorIs(t, err, spi.ErrUnevaluableLeaf)
		})
	}
}
