package spi_test

import (
	"sync"
	"testing"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// TestPrepare_ZeroValueAsymmetry pins spec §3's table: a zero-Op filter is
// match-all at the ROOT only. A zero-Op CHILD is a leaf that never matches —
// evalFilter routed it to the leaf evaluator, ExpandLeaf hit its default arm,
// and the leaf was false for every row. Hoisting the Op == "" check into the
// recursion silently flips the AND/OR child rows, so they are pinned here.
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

	tests := []struct {
		name string
		f    spi.Filter
		want bool
	}{
		{"root zero filter matches all", spi.Filter{}, true},
		{"root empty AND is the AND identity", spi.Filter{Op: spi.FilterAnd}, true},
		{"root empty OR is the OR identity", spi.Filter{Op: spi.FilterOr}, false},
		{
			"zero-Op child annihilates an AND",
			spi.Filter{Op: spi.FilterAnd, Children: []spi.Filter{leaf, {}}},
			false,
		},
		{
			"zero-Op child does not rescue an OR",
			spi.Filter{
				Op: spi.FilterOr,
				Children: []spi.Filter{
					{Op: spi.FilterEq, Source: spi.SourceData, Path: "name",
						Value: "Bob", Declared: []spi.DataType{spi.String}},
					{},
				},
			},
			false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := spi.Prepare(tc.f).Match(data, spi.EntityMeta{}); got != tc.want {
				t.Errorf("Prepare(%+v).Match() = %v, want %v", tc.f, got, tc.want)
			}
		})
	}
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
	p := spi.Prepare(spi.Filter{
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
			if got := spi.Prepare(tc.f).Match([]byte(tc.doc), spi.EntityMeta{}); got != tc.want {
				t.Errorf("Match(%s) on %+v = %v, want %v", tc.doc, tc.f, got, tc.want)
			}
		})
	}
}

// TestPreparedFilter_MalformedPathNeverResolves pins path-grammar.md's
// requirement, stated in prepared_filter.go's prepareNode, that a path
// failing to parse leaves the node unexpanded rather than falling through to
// ExpandLeaf: a malformed path must never resolve to anything.
//
// A presence test is the case that actually discriminates. NOT_NULL over a
// real, present value ("x") is true if the path resolved at all — so a
// malformed path answering false here can only mean the parse failure
// suppressed evaluation before ExpandLeaf/EvalLeaf ever ran. A comparison
// operator alone would not prove this: it would also answer false for a
// path that parsed fine but happened not to match the operand, so the
// second row pairs a comparison with an operand that DOES match the real
// data, to show the same suppression there too.
func TestPreparedFilter_MalformedPathNeverResolves(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		f    spi.Filter
	}{
		{"unclosed bracket, presence test", `{"a":["x"]}`,
			spi.Filter{Op: spi.FilterNotNull, Path: "a[", Source: spi.SourceData}},
		{"trailing char after subscript, matching comparison", `{"a":["x"]}`,
			spi.Filter{Op: spi.FilterEq, Path: "a[0]b", Source: spi.SourceData,
				Value: "x", Declared: []spi.DataType{spi.String}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := spi.Prepare(tc.f).Match([]byte(tc.doc), spi.EntityMeta{}); got {
				t.Errorf("Match(%s) on %+v = true, want false: a path that fails to parse must never resolve", tc.doc, tc.f)
			}
		})
	}
}
