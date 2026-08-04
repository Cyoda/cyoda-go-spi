package spi_test

import (
	"sort"
	"strings"
	"testing"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// wantField is the expectation for one entry of the flattened fields map.
type wantField struct {
	types   []spi.DataType
	isArray bool
}

func typesEqual(a, b []spi.DataType) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortedPaths(m map[string]spi.FieldDescriptor) []string {
	paths := make([]string, 0, len(m))
	for p := range m {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// TestFieldsMapFromSchema is the contract test for the flattening: schema
// bytes in, JSONPath -> FieldDescriptor out. The map KEYS are asserted
// literally on purpose. A deviation in the key convention ("[*]" placement,
// the "$." root, the dot joins) does not fail loudly at runtime — the lookup
// simply misses, the declared type set comes back empty, and the leaf matches
// nothing. Only a literal assertion catches that.
func TestFieldsMapFromSchema(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		want   map[string]wantField
	}{
		{
			name: "flat object",
			schema: `{"kind":"OBJECT","children":{
				"name":{"kind":"LEAF","types":["STRING"]},
				"age":{"kind":"LEAF","types":["INTEGER"]}}}`,
			want: map[string]wantField{
				"$.name": {types: []spi.DataType{spi.String}},
				"$.age":  {types: []spi.DataType{spi.Integer}},
			},
		},
		{
			name: "nested object emits only the leaf",
			schema: `{"kind":"OBJECT","children":{
				"address":{"kind":"OBJECT","children":{
					"city":{"kind":"LEAF","types":["STRING"]}}}}}`,
			want: map[string]wantField{
				"$.address.city": {types: []spi.DataType{spi.String}},
			},
		},
		{
			name: "deeply nested object",
			schema: `{"kind":"OBJECT","children":{
				"a":{"kind":"OBJECT","children":{
					"b":{"kind":"OBJECT","children":{
						"c":{"kind":"LEAF","types":["LONG"]}}}}}}}`,
			want: map[string]wantField{
				"$.a.b.c": {types: []spi.DataType{spi.Long}},
			},
		},
		{
			name: "array of scalars uses the [*] key and is marked IsArray",
			schema: `{"kind":"OBJECT","children":{
				"tags":{"kind":"ARRAY","element":{"kind":"LEAF","types":["STRING"]}}}}`,
			want: map[string]wantField{
				"$.tags[*]": {types: []spi.DataType{spi.String}, isArray: true},
			},
		},
		{
			name: "array of objects: [*] on the array hop, nested leaves not IsArray",
			schema: `{"kind":"OBJECT","children":{
				"items":{"kind":"ARRAY","element":{"kind":"OBJECT","children":{
					"name":{"kind":"LEAF","types":["STRING"]},
					"price":{"kind":"LEAF","types":["DOUBLE"]}}}}}}`,
			want: map[string]wantField{
				"$.items[*].name":  {types: []spi.DataType{spi.String}},
				"$.items[*].price": {types: []spi.DataType{spi.Double}},
			},
		},
		{
			name: "nested arrays chain the [*] hops",
			schema: `{"kind":"OBJECT","children":{
				"matrix":{"kind":"ARRAY","element":{"kind":"ARRAY",
					"element":{"kind":"LEAF","types":["INTEGER"]}}}}}`,
			want: map[string]wantField{
				"$.matrix[*][*]": {types: []spi.DataType{spi.Integer}, isArray: true},
			},
		},
		{
			name: "array of objects containing an array",
			schema: `{"kind":"OBJECT","children":{
				"orders":{"kind":"ARRAY","element":{"kind":"OBJECT","children":{
					"lines":{"kind":"ARRAY","element":{"kind":"LEAF","types":["STRING"]}}}}}}}`,
			want: map[string]wantField{
				"$.orders[*].lines[*]": {types: []spi.DataType{spi.String}, isArray: true},
			},
		},
		{
			name: "array with no observed element declares nothing",
			schema: `{"kind":"OBJECT","children":{
				"empty":{"kind":"ARRAY"},
				"other":{"kind":"LEAF","types":["BOOLEAN"]}}}`,
			want: map[string]wantField{
				"$.other": {types: []spi.DataType{spi.Boolean}},
			},
		},
		{
			name: "polymorphic leaf keeps every declared type",
			schema: `{"kind":"OBJECT","children":{
				"val":{"kind":"LEAF","types":["STRING","BOOLEAN"]}}}`,
			want: map[string]wantField{
				// TypeSet keeps members sorted by DataType ordinal, and
				// String precedes Boolean in that ordering.
				"$.val": {types: []spi.DataType{spi.String, spi.Boolean}},
			},
		},
		{
			name: "nullable leaf keeps NULL alongside the concrete type",
			schema: `{"kind":"OBJECT","children":{
				"maybe":{"kind":"LEAF","types":["STRING","NULL"]}}}`,
			want: map[string]wantField{
				// TypeSet.Add drops NULL once a concrete type is present.
				"$.maybe": {types: []spi.DataType{spi.String}},
			},
		},
		{
			name: "null-only leaf declares NULL",
			schema: `{"kind":"OBJECT","children":{
				"nothing":{"kind":"LEAF","types":["NULL"]}}}`,
			want: map[string]wantField{
				"$.nothing": {types: []spi.DataType{spi.Null}},
			},
		},
		{
			name: "object-or-scalar node emits a self leaf AND its children",
			schema: `{"kind":"OBJECT","children":{
				"some-object":{"kind":"OBJECT","types":["STRING"],"children":{
					"some-key":{"kind":"LEAF","types":["STRING"]}}}}}`,
			want: map[string]wantField{
				"$.some-object":          {types: []spi.DataType{spi.String}},
				"$.some-object.some-key": {types: []spi.DataType{spi.String}},
			},
		},
		{
			name: "pure object emits no self leaf",
			schema: `{"kind":"OBJECT","children":{
				"some-object":{"kind":"OBJECT","children":{
					"some-key":{"kind":"LEAF","types":["STRING"]}}}}}`,
			want: map[string]wantField{
				"$.some-object.some-key": {types: []spi.DataType{spi.String}},
			},
		},
		{
			name: "null-only object emits no self leaf",
			schema: `{"kind":"OBJECT","children":{
				"score":{"kind":"OBJECT","types":["NULL"],"children":{
					"sub":{"kind":"LEAF","types":["STRING"]}}}}}`,
			want: map[string]wantField{
				"$.score.sub": {types: []spi.DataType{spi.String}},
			},
		},
		{
			name: "object-or-scalar inside an array carries the [*] path",
			schema: `{"kind":"OBJECT","children":{
				"items":{"kind":"ARRAY","element":{"kind":"OBJECT","types":["LONG"],"children":{
					"id":{"kind":"LEAF","types":["UUID_TYPE"]}}}}}}`,
			want: map[string]wantField{
				"$.items[*]":    {types: []spi.DataType{spi.Long}},
				"$.items[*].id": {types: []spi.DataType{spi.UUIDType}},
			},
		},
		{
			name:   "root leaf is the bare root path",
			schema: `{"kind":"LEAF","types":["STRING"]}`,
			want: map[string]wantField{
				"$": {types: []spi.DataType{spi.String}},
			},
		},
		{
			name:   "empty object declares nothing",
			schema: `{"kind":"OBJECT"}`,
			want:   map[string]wantField{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := spi.FieldsMapFromSchema([]byte(tt.schema))
			if err != nil {
				t.Fatalf("FieldsMapFromSchema: unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("field count: got %d %v, want %d", len(got), sortedPaths(got), len(tt.want))
			}
			for path, want := range tt.want {
				fd, ok := got[path]
				if !ok {
					t.Fatalf("missing path %q; got %v", path, sortedPaths(got))
				}
				if fd.Path != path {
					t.Errorf("%q: descriptor Path is %q, want %q", path, fd.Path, path)
				}
				if !typesEqual(fd.Types, want.types) {
					t.Errorf("%q: types = %v, want %v", path, fd.Types, want.types)
				}
				if fd.IsArray != want.isArray {
					t.Errorf("%q: IsArray = %v, want %v", path, fd.IsArray, want.isArray)
				}
				if fd.MaxWidth != 0 {
					t.Errorf("%q: MaxWidth = %d, want 0 (the wire form carries no widths)",
						path, fd.MaxWidth)
				}
			}
		})
	}
}

// TestFieldsMapFromSchemaNoSchemaBound pins the (nil, nil) contract: a model
// with no schema bound has no types to declare, and must not look like a parse
// failure. Reporting an error here would make a plugin reject searches the
// engine accepts.
func TestFieldsMapFromSchemaNoSchemaBound(t *testing.T) {
	for _, tt := range []struct {
		name   string
		schema []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := spi.FieldsMapFromSchema(tt.schema)
			if err != nil {
				t.Fatalf("expected no error for %s schema, got %v", tt.name, err)
			}
			if got != nil {
				t.Fatalf("expected nil map for %s schema, got %v", tt.name, got)
			}
		})
	}
}

// TestFieldsMapFromSchemaMalformed pins fail-closed behaviour: bytes that are
// present but unreadable mean this executor disagrees with whoever wrote the
// model, so it must error rather than silently search against an empty schema.
func TestFieldsMapFromSchemaMalformed(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		wantMsg string
	}{
		{"not json", `{`, "unmarshal"},
		{"json but not an object", `[1,2,3]`, "unmarshal"},
		{"json null", `null`, "unknown node kind"},
		{"missing kind", `{"types":["STRING"]}`, "unknown node kind"},
		{"unknown kind", `{"kind":"BLOB"}`, "unknown node kind"},
		{"unknown data type", `{"kind":"LEAF","types":["QUANTUM"]}`, "unknown data type"},
		{
			name:    "unknown kind nested under a child",
			schema:  `{"kind":"OBJECT","children":{"x":{"kind":"BLOB"}}}`,
			wantMsg: `child "x"`,
		},
		{
			name:    "unknown data type nested in an array element",
			schema:  `{"kind":"OBJECT","children":{"x":{"kind":"ARRAY","element":{"kind":"LEAF","types":["QUANTUM"]}}}}`,
			wantMsg: "array element",
		},
		{
			name:    "null child node",
			schema:  `{"kind":"OBJECT","children":{"x":null}}`,
			wantMsg: `child "x"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := spi.FieldsMapFromSchema([]byte(tt.schema))
			if err == nil {
				t.Fatalf("expected an error for %s, got map %v", tt.name, got)
			}
			if got != nil {
				t.Errorf("expected nil map alongside the error, got %v", got)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", err.Error(), tt.wantMsg)
			}
		})
	}
}

// TestFieldsDeterministicOrder pins the sorted output. Callers compare and log
// these lists; map iteration order alone would make that unstable.
func TestFieldsDeterministicOrder(t *testing.T) {
	schema := `{"kind":"OBJECT","children":{
		"z":{"kind":"LEAF","types":["STRING"]},
		"a":{"kind":"LEAF","types":["STRING"]},
		"m":{"kind":"OBJECT","types":["STRING"],"children":{
			"y":{"kind":"LEAF","types":["STRING"]},
			"b":{"kind":"LEAF","types":["STRING"]}}},
		"arr":{"kind":"ARRAY","element":{"kind":"LEAF","types":["STRING"]}}}}`

	want := []string{"$.a", "$.arr[*]", "$.m", "$.m.b", "$.m.y", "$.z"}

	for i := 0; i < 20; i++ {
		node, err := spi.UnmarshalModelNode([]byte(schema))
		if err != nil {
			t.Fatalf("UnmarshalModelNode: %v", err)
		}
		fields := node.Fields()
		got := make([]string, len(fields))
		for j, f := range fields {
			got[j] = f.Path
		}
		if len(got) != len(want) {
			t.Fatalf("run %d: got %v, want %v", i, got, want)
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("run %d: got %v, want %v", i, got, want)
			}
		}
	}
}

// TestFieldsCachedAndAliased documents the sharing contract: repeated calls
// hand back the same backing array, so a caller that mutates the slice
// corrupts the view for everyone else holding the node.
func TestFieldsCachedAndAliased(t *testing.T) {
	node := spi.NewObjectNode()
	node.SetChild("x", spi.NewLeafNode(spi.Integer))

	f1 := node.Fields()
	f2 := node.Fields()
	if len(f1) != 1 || len(f2) != 1 {
		t.Fatalf("expected 1 field, got %d and %d", len(f1), len(f2))
	}
	if &f1[0] != &f2[0] {
		t.Error("Fields() should return the cached slice, not rebuild it")
	}

	m1 := node.FieldsMap()
	m2 := node.FieldsMap()
	if len(m1) != 1 || len(m2) != 1 {
		t.Fatalf("expected 1 entry, got %d and %d", len(m1), len(m2))
	}
}

// TestSetChildInvalidatesFieldCache guards the build-then-read path: a tree
// extended after a first Fields() call must not keep serving the stale
// flattening, which would silently drop the new field from every lookup.
func TestSetChildInvalidatesFieldCache(t *testing.T) {
	node := spi.NewObjectNode()
	node.SetChild("a", spi.NewLeafNode(spi.Integer))
	if got := len(node.Fields()); got != 1 {
		t.Fatalf("expected 1 field before extension, got %d", got)
	}

	node.SetChild("b", spi.NewLeafNode(spi.String))
	m := node.FieldsMap()
	if _, ok := m["$.b"]; !ok {
		t.Fatalf("expected $.b after SetChild, got %v", sortedPaths(m))
	}
	if got := len(node.Fields()); got != 2 {
		t.Errorf("expected 2 fields after extension, got %d", got)
	}
}

// TestFieldsMapFromSchemaOwnsItsResult confirms each call builds a fresh map,
// so a plugin that annotates the result cannot poison another caller's view.
func TestFieldsMapFromSchemaOwnsItsResult(t *testing.T) {
	schema := []byte(`{"kind":"OBJECT","children":{"a":{"kind":"LEAF","types":["STRING"]}}}`)

	first, err := spi.FieldsMapFromSchema(schema)
	if err != nil {
		t.Fatalf("FieldsMapFromSchema: %v", err)
	}
	delete(first, "$.a")
	first["$.injected"] = spi.FieldDescriptor{Path: "$.injected"}

	second, err := spi.FieldsMapFromSchema(schema)
	if err != nil {
		t.Fatalf("FieldsMapFromSchema: %v", err)
	}
	if _, ok := second["$.a"]; !ok {
		t.Error("second call lost $.a: the result is not independent")
	}
	if _, ok := second["$.injected"]; ok {
		t.Error("second call sees an injected key: the result is not independent")
	}
}

// TestUnmarshalModelNodeShape covers the node-level accessors the flattening
// is built on, including the unobserved-element array that must stay
// Element()==nil rather than becoming an empty leaf.
func TestUnmarshalModelNodeShape(t *testing.T) {
	schema := `{"kind":"OBJECT","children":{
		"leaf":{"kind":"LEAF","types":["STRING"]},
		"obj":{"kind":"OBJECT","children":{"in":{"kind":"LEAF","types":["LONG"]}}},
		"arr":{"kind":"ARRAY","element":{"kind":"LEAF","types":["BOOLEAN"]}},
		"seed":{"kind":"ARRAY"}}}`

	root, err := spi.UnmarshalModelNode([]byte(schema))
	if err != nil {
		t.Fatalf("UnmarshalModelNode: %v", err)
	}
	if root.Kind() != spi.KindObject {
		t.Fatalf("root kind = %v, want KindObject", root.Kind())
	}
	if got := len(root.Children()); got != 4 {
		t.Fatalf("root children = %d, want 4", got)
	}

	leaf := root.Child("leaf")
	if leaf == nil || leaf.Kind() != spi.KindLeaf {
		t.Fatalf("child leaf = %v, want a KindLeaf node", leaf)
	}
	if types := leaf.Types().Types(); !typesEqual(types, []spi.DataType{spi.String}) {
		t.Errorf("leaf types = %v, want [STRING]", types)
	}

	arr := root.Child("arr")
	if arr == nil || arr.Kind() != spi.KindArray {
		t.Fatalf("child arr = %v, want a KindArray node", arr)
	}
	if arr.Element() == nil || arr.Element().Kind() != spi.KindLeaf {
		t.Fatalf("arr element = %v, want a KindLeaf node", arr.Element())
	}

	seed := root.Child("seed")
	if seed == nil || seed.Kind() != spi.KindArray {
		t.Fatalf("child seed = %v, want a KindArray node", seed)
	}
	if seed.Element() != nil {
		t.Error("an ARRAY with no wire element must keep Element()==nil, not gain an empty leaf")
	}

	if root.Child("nope") != nil {
		t.Error("Child of an absent name must be nil")
	}
}

// TestChildrenIsACopy confirms the children map handed out is a copy, so a
// caller cannot restructure a shared tree by writing to it.
func TestChildrenIsACopy(t *testing.T) {
	root := spi.NewObjectNode()
	root.SetChild("a", spi.NewLeafNode(spi.String))

	kids := root.Children()
	delete(kids, "a")
	kids["b"] = spi.NewLeafNode(spi.String)

	if root.Child("a") == nil {
		t.Error("deleting from the returned map must not remove the child")
	}
	if root.Child("b") != nil {
		t.Error("adding to the returned map must not add a child")
	}
}

func TestNodeKindString(t *testing.T) {
	cases := map[spi.NodeKind]string{
		spi.KindLeaf:      "LEAF",
		spi.KindObject:    "OBJECT",
		spi.KindArray:     "ARRAY",
		spi.NodeKind(999): "UNKNOWN",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("NodeKind(%d).String() = %q, want %q", int(k), got, want)
		}
	}
}

// TestNewArrayNodeNilElement pins the constructor's nil-element contract from
// the flattening side: an unobserved array declares nothing at all rather than
// an empty-typed leaf that would look declared and match nothing.
func TestNewArrayNodeNilElement(t *testing.T) {
	root := spi.NewObjectNode()
	root.SetChild("arr", spi.NewArrayNode(nil))

	if got := len(root.Fields()); got != 0 {
		t.Fatalf("expected no fields, got %v", sortedPaths(root.FieldsMap()))
	}
}

// TestFieldsMapFromSchemaModelDescriptor exercises the intended call site: a
// plugin holding a ModelDescriptor derives the fields map from its Schema
// bytes, and an unbound schema degrades to (nil, nil).
func TestFieldsMapFromSchemaModelDescriptor(t *testing.T) {
	desc := &spi.ModelDescriptor{
		Schema: []byte(`{"kind":"OBJECT","children":{"n":{"kind":"LEAF","types":["INTEGER"]}}}`),
	}
	m, err := spi.FieldsMapFromSchema(desc.Schema)
	if err != nil {
		t.Fatalf("FieldsMapFromSchema: %v", err)
	}
	fd, ok := m["$.n"]
	if !ok {
		t.Fatalf("expected $.n, got %v", sortedPaths(m))
	}
	if !typesEqual(fd.Types, []spi.DataType{spi.Integer}) {
		t.Errorf("$.n types = %v, want [INTEGER]", fd.Types)
	}

	unbound := &spi.ModelDescriptor{}
	m, err = spi.FieldsMapFromSchema(unbound.Schema)
	if err != nil || m != nil {
		t.Errorf("unbound schema: got (%v, %v), want (nil, nil)", m, err)
	}
}
