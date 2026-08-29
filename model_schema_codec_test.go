package spi

import "testing"

// Every shape a node can take round-trips to itself. The monomorphic ones —
// nearly every node in every existing model, nullable or not — keep the
// spelling they already have on disk, so there is no migration.
func TestCodec_EveryShapeRoundTrips(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"LEAF","types":["STRING"]}`,
		`{"kind":"LEAF","types":["NULL"]}`,
		`{"kind":"LEAF"}`,
		`{"kind":"OBJECT"}`,
		`{"kind":"OBJECT","types":["NULL"]}`,
		`{"kind":"ARRAY"}`,
		`{"kind":"ARRAY","types":["NULL"]}`,
		`{"kind":"ARRAY","element":{"kind":"LEAF","types":["STRING"]}}`,
		`{"kind":"OBJECT","children":{"k":{"kind":"LEAF","types":["INTEGER"]}}}`,
	} {
		n, err := UnmarshalModelNode([]byte(raw))
		if err != nil {
			t.Fatalf("Unmarshal(%s): %v", raw, err)
		}
		out, err := MarshalModelNode(n)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if string(out) != raw {
			t.Errorf("round trip\n  in  = %s\n  out = %s", raw, out)
		}
	}
}

// kind:"LEAF" alone is ambiguous, so the two spellings are separated by an
// explicit rule: a scalar branch never holds NULL, so NULL with no concrete
// type beside it is the branchless marker and nothing else.
func TestCodec_LeafSpellingsAreDistinct(t *testing.T) {
	marker, err := UnmarshalModelNode([]byte(`{"kind":"LEAF","types":["NULL"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if marker.Scalar() != nil || !marker.Nullable() || len(marker.Kinds()) != 0 {
		t.Errorf("kind:LEAF types:[NULL] is the branchless marker; kinds=%v nullable=%v",
			marker.Kinds(), marker.Nullable())
	}

	empty, err := UnmarshalModelNode([]byte(`{"kind":"LEAF"}`))
	if err != nil {
		t.Fatal(err)
	}
	if empty.Scalar() == nil || empty.Nullable() {
		t.Errorf("kind:LEAF with no types is an empty scalar branch; kinds=%v nullable=%v",
			empty.Kinds(), empty.Nullable())
	}
}

// A node that declares more than one kind spells its whole set.
func TestCodec_UnionSpellsEveryKind(t *testing.T) {
	n := NewObjectNode()
	n.SetElement(NewLeafNode(Integer))

	raw, err := MarshalModelNode(n)
	if err != nil {
		t.Fatal(err)
	}
	back, err := UnmarshalModelNode(raw)
	if err != nil {
		t.Fatalf("Unmarshal(%s): %v", raw, err)
	}
	if back.Object() == nil || back.Array() == nil || len(back.Kinds()) != 2 {
		t.Errorf("round trip lost a branch: %s -> kinds=%v", raw, back.Kinds())
	}

	again, err := MarshalModelNode(back)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(raw) {
		t.Errorf("a union does not round-trip: %s -> %s", raw, again)
	}
}

// A model persisted under the old dominant-kind spelling restores every branch
// its payload carries. The label named one; the payload always held them all.
func TestCodec_ReadsTheOldDominantKindSpelling(t *testing.T) {
	const legacy = `{"kind":"OBJECT","types":["STRING"],` +
		`"children":{"k":{"kind":"LEAF","types":["INTEGER"]}},` +
		`"element":{"kind":"LEAF","types":["BOOLEAN"]}}`

	n, err := UnmarshalModelNode([]byte(legacy))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if n.Scalar() == nil || n.Object() == nil || n.Array() == nil {
		t.Fatalf("a legacy node must restore all three branches; kinds=%v", n.Kinds())
	}
	if got := n.Scalar().Types(); len(got) != 1 || got[0] != String {
		t.Errorf("scalar branch types = %v, want [STRING]", got)
	}
}

// An ARRAY label with no element stays an unobserved-element array, and an
// OBJECT label with no children stays an empty object: a branch can be present
// and empty, and only the kind record says so.
func TestCodec_KeepsAnEmptyBranch(t *testing.T) {
	arr, err := UnmarshalModelNode([]byte(`{"kind":"ARRAY"}`))
	if err != nil {
		t.Fatal(err)
	}
	if arr.Array() == nil || arr.Array().Element() != nil {
		t.Errorf("kind:ARRAY with no element declares the array branch with no element; kinds=%v", arr.Kinds())
	}

	obj, err := UnmarshalModelNode([]byte(`{"kind":"OBJECT"}`))
	if err != nil {
		t.Fatal(err)
	}
	if obj.Object() == nil || obj.Object().Len() != 0 {
		t.Errorf("kind:OBJECT with no children declares the object branch with none; kinds=%v", obj.Kinds())
	}
}

// The codec fails closed: a node that names no kind and carries no payload
// declares nothing, and a tree that under-declares matches nothing with no
// error at all.
func TestCodec_RejectsAKindlessNode(t *testing.T) {
	for _, raw := range []string{`{}`, `{"kind":""}`, `{"kinds":[]}`} {
		if _, err := UnmarshalModelNode([]byte(raw)); err == nil {
			t.Errorf("Unmarshal(%s) must be rejected", raw)
		}
	}
	if _, err := UnmarshalModelNode([]byte(`{"kinds":["WAT"]}`)); err == nil {
		t.Error("an unknown kind name must be rejected")
	}
}
