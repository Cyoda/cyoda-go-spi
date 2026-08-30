package spi

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestResolvePath(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		path string
		want []string // gjson .String() of each addressed value; "<absent>" for a non-existent one
	}{
		// A bare path addresses the value, never the elements. This is the
		// rule the previous evaluator broke: it unwrapped an array and
		// compared element-wise, so $.a EQUALS "A" matched ["A","B"].
		{"bare over scalar", `{"a":"A"}`, "a", []string{"A"}},
		{"bare over array", `{"a":["A","B"]}`, "a", []string{`["A","B"]`}},
		{"bare over empty array", `{"a":[]}`, "a", []string{`[]`}},
		{"bare over absent", `{}`, "a", []string{"<absent>"}},
		// A JSON null is a present value, not an absent one — Exists() is
		// true and Type is Null. resultText renders it as the empty string
		// (r.String() for a Null result), which is what distinguishes
		// present-but-null from absent when a caller checks r.Exists().
		{"bare over null", `{"a":null}`, "a", []string{""}},

		// A wildcard addresses the elements, never the array, and never
		// wraps a scalar into a one-element sequence.
		{"wildcard over array", `{"a":["A","B"]}`, "a[*]", []string{"A", "B"}},
		{"wildcard over empty array", `{"a":[]}`, "a[*]", nil},
		{"wildcard over scalar", `{"a":"A"}`, "a[*]", nil},
		{"wildcard over null", `{"a":null}`, "a[*]", nil},
		{"wildcard over absent", `{}`, "a[*]", nil},

		// A positional subscript addresses one position, which may be absent.
		{"index present", `{"a":["A","B"]}`, "a[0]", []string{"A"}},
		{"index second", `{"a":["A","B"]}`, "a[1]", []string{"B"}},
		{"index past end", `{"a":["A"]}`, "a[3]", []string{"<absent>"}},
		{"index over empty array", `{"a":[]}`, "a[0]", []string{"<absent>"}},
		{"index over scalar", `{"a":"A"}`, "a[0]", []string{"<absent>"}},
		{"index over null", `{"a":null}`, "a[0]", []string{"<absent>"}},
		{"index over absent field", `{}`, "a[0]", []string{"<absent>"}},

		// A dotted numeric segment is a field name, not an index.
		{"numeric field name", `{"obj":{"0":"Z"}}`, "obj.0", []string{"Z"}},
		{"numeric segment is not an index", `{"tags":["A"]}`, "tags.0", []string{"<absent>"}},

		// An ordinary non-numeric object-to-object hop, not preceded by a
		// wildcard and not digit-named.
		{"nested object field", `{"a":{"b":"B"}}`, "a.b", []string{"B"}},

		// Nested hops flatten; an element missing the key contributes an
		// absent value rather than being dropped, so IS_NULL can see it.
		{"nested wildcard", `{"items":[{"sku":"A"},{"sku":"B"}]}`, "items[*].sku", []string{"A", "B"}},
		{"element missing key", `{"items":[{"sku":"A"},{}]}`, "items[*].sku", []string{"A", "<absent>"}},
		{"two hops", `{"o":[{"l":[{"s":"A"},{"s":"B"}]},{"l":[{"s":"C"}]}]}`, "o[*].l[*].s", []string{"A", "B", "C"}},
		{"chained subscripts", `{"m":[["A","B"],["C"]]}`, "m[*][*]", []string{"A", "B", "C"}},
		{"chained index", `{"m":[["A","B"],["C"]]}`, "m[0][1]", []string{"B"}},
		{"mixed chained subscripts", `{"a":[[{"b":"B"}]]}`, "a[0][*].b", []string{"B"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hops, err := ParseFilterPath(tc.path)
			if err != nil {
				t.Fatalf("ParseFilterPath(%q): %v", tc.path, err)
			}
			got := ResolvePath([]byte(tc.doc), hops)
			var gotS []string
			for _, r := range got {
				if !r.Exists() {
					gotS = append(gotS, "<absent>")
					continue
				}
				gotS = append(gotS, resultText(r))
			}
			if len(gotS) != len(tc.want) {
				t.Fatalf("ResolvePath(%s, %q) = %v, want %v", tc.doc, tc.path, gotS, tc.want)
			}
			for i := range gotS {
				if gotS[i] != tc.want[i] {
					t.Fatalf("ResolvePath(%s, %q) = %v, want %v", tc.doc, tc.path, gotS, tc.want)
				}
			}
		})
	}
}

func resultText(r gjson.Result) string {
	if r.IsArray() || r.IsObject() {
		return r.Raw
	}
	return r.String()
}
