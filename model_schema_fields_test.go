package spi

import "testing"

// The flattening is a contract every executor must agree on: a path spelled
// differently here simply misses at lookup, the leaf comparison matches
// nothing, and the search returns fewer rows with no error at all. So every
// branch a field declares has to be reachable.
func TestFieldsMap_NamesEveryBranchOfAUnion(t *testing.T) {
	// object ∪ array — the label said OBJECT and the element was lost
	f := NewObjectNode()
	f.SetChild("k", NewLeafNode(String))
	f.SetElement(NewLeafNode(Integer))
	// object ∪ scalar
	g := NewObjectNode()
	g.SetChild("in", NewLeafNode(Integer))
	g.AddScalarTypes(String)
	// array ∪ scalar — the label said ARRAY and the scalar was lost
	h := NewArrayNode(NewLeafNode(String))
	h.AddScalarTypes(String)

	root := NewObjectNode()
	root.SetChild("f", f)
	root.SetChild("g", g)
	root.SetChild("h", h)

	raw, err := MarshalModelNode(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := FieldsMapFromSchema(raw)
	if err != nil {
		t.Fatalf("FieldsMapFromSchema: %v", err)
	}
	for _, path := range []string{"$.f.k", "$.f[*]", "$.g", "$.g.in", "$.h", "$.h[*]"} {
		if _, ok := got[path]; !ok {
			t.Errorf("declared path %q is missing from the fields map (have %d entries: %v)",
				path, len(got), keysOf(got))
		}
	}
}

// IsArray keeps its narrow meaning: set only for a leaf reached directly as an
// array's element type. Consumers key off the narrow meaning.
func TestFieldsMap_IsArrayStaysNarrow(t *testing.T) {
	elem := NewObjectNode()
	elem.SetChild("price", NewLeafNode(Integer))

	root := NewObjectNode()
	root.SetChild("tags", NewArrayNode(NewLeafNode(String)))
	root.SetChild("items", NewArrayNode(elem))

	raw, _ := MarshalModelNode(root)
	got, err := FieldsMapFromSchema(raw)
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := got["$.tags[*]"]; !ok || !d.IsArray {
		t.Errorf("$.tags[*] is a leaf reached as an array element: IsArray must be set (%+v)", d)
	}
	if d, ok := got["$.items[*].price"]; !ok || d.IsArray {
		t.Errorf("$.items[*].price is a scalar per item, not an array-valued field (%+v)", d)
	}
}

// A path observed only as null is still a declared path, and it declares NULL.
func TestFieldsMap_NullableMarkerDeclaresNull(t *testing.T) {
	root := NewObjectNode()
	root.SetChild("n", NewLeafNode(Null))

	raw, _ := MarshalModelNode(root)
	got, err := FieldsMapFromSchema(raw)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := got["$.n"]
	if !ok {
		t.Fatal("a null-only field is still a declared field")
	}
	if len(d.Types) != 1 || d.Types[0] != Null {
		t.Errorf("Types = %v, want [NULL]", d.Types)
	}
}

// A merely nullable container stays a pure container: null is the marker, not
// a scalar observation, so it opens no self-descriptor.
func TestFieldsMap_NullableContainerEmitsNoSelfDescriptor(t *testing.T) {
	c := NewObjectNode()
	c.SetChild("in", NewLeafNode(Integer))
	c.SetNullable()

	root := NewObjectNode()
	root.SetChild("c", c)

	raw, _ := MarshalModelNode(root)
	got, _ := FieldsMapFromSchema(raw)
	if _, ok := got["$.c"]; ok {
		t.Error("a nullable object is a pure container, not a searchable scalar leaf")
	}
	if _, ok := got["$.c.in"]; !ok {
		t.Error("its children are still declared")
	}
}

// An array whose element was never observed declares nothing at all. It must
// not emit an empty-typed leaf, which would match nothing while looking like a
// declared field.
func TestFieldsMap_UnobservedArrayElementDeclaresNothing(t *testing.T) {
	root := NewObjectNode()
	root.SetChild("seed", NewArrayNode(nil))

	raw, _ := MarshalModelNode(root)
	got, _ := FieldsMapFromSchema(raw)
	if _, ok := got["$.seed[*]"]; ok {
		t.Error("an unobserved array element declares no leaf")
	}
}

// An array element observed as both a scalar and an object reaches both.
func TestFieldsMap_UnionArrayElement(t *testing.T) {
	elem := NewObjectNode()
	elem.SetChild("k", NewLeafNode(Integer))
	elem.AddScalarTypes(String)

	root := NewObjectNode()
	root.SetChild("m", NewArrayNode(elem))

	raw, _ := MarshalModelNode(root)
	got, err := FieldsMapFromSchema(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"$.m[*]", "$.m[*].k"} {
		if _, ok := got[path]; !ok {
			t.Errorf("declared path %q missing (have %v)", path, keysOf(got))
		}
	}
	if d := got["$.m[*]"]; d.IsArray {
		t.Error("the self-descriptor of an object-or-scalar array element does not carry IsArray")
	}
}

func keysOf(m map[string]FieldDescriptor) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A scalar branch that declares no types is not a searchable leaf on a node
// that also declares a container — the same reasoning the array arm applies to
// an unobserved element: an empty-typed descriptor matches nothing while
// looking like a declared field. A node that declares ONLY that branch still
// emits it, which is what a bare {"kind":"LEAF"} has always meant.
func TestFieldsMap_EmptyScalarBranchBesideAContainer(t *testing.T) {
	both := NewObjectNode()
	both.SetChild("k", NewLeafNode(String))
	both.DeclareKind(KindLeaf)

	root := NewObjectNode()
	root.SetChild("both", both)

	got := root.FieldsMap()
	if d, ok := got["$.both"]; ok {
		t.Errorf("an empty scalar branch beside a container declares no leaf; got %+v", d)
	}
	if _, ok := got["$.both.k"]; !ok {
		t.Error("its children are still declared")
	}

	// Alone, it is the bare LEAF node, and it keeps its descriptor.
	only := &ModelNode{}
	only.DeclareKind(KindLeaf)
	solo := NewObjectNode()
	solo.SetChild("only", only)
	if _, ok := solo.FieldsMap()["$.only"]; !ok {
		t.Error("a node declaring only the scalar branch still declares a leaf")
	}
}
