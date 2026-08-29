package spi

import "testing"

// A branch can be present and empty — an object with no children, an array
// whose element was never observed, a scalar branch with no type yet. Nothing
// in the payload distinguishes such a node from one never observed as that
// kind; only the kind record does, which is why the set is stored rather than
// inferred from the payload.
func TestDeclareKind_EstablishesAnEmptyBranch(t *testing.T) {
	for _, k := range []NodeKind{KindLeaf, KindObject, KindArray} {
		n := &ModelNode{}
		n.DeclareKind(k)
		if n.Branch(k) == nil {
			t.Fatalf("DeclareKind(%s) must establish the branch; kinds=%v", k, n.Kinds())
		}
		if len(n.Kinds()) != 1 {
			t.Errorf("DeclareKind(%s) established more than it was asked for: %v", k, n.Kinds())
		}
	}

	// An empty object node round-trips as one, distinct from a node that
	// declares nothing.
	n := &ModelNode{}
	n.DeclareKind(KindObject)
	raw, err := MarshalModelNode(n)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"kind":"OBJECT"}` {
		t.Errorf("Marshal = %s, want {\"kind\":\"OBJECT\"}", raw)
	}
}

// It never disturbs a branch already there.
func TestDeclareKind_IsIdempotent(t *testing.T) {
	n := NewObjectNode()
	n.SetChild("k", NewLeafNode(String))
	n.DeclareKind(KindObject)
	if n.Object() == nil || n.Object().Child("k") == nil {
		t.Error("DeclareKind must not replace an existing branch")
	}

	a := NewArrayNode(NewLeafNode(String))
	a.DeclareKind(KindArray)
	if a.Array() == nil || a.Array().Element() == nil {
		t.Error("DeclareKind must not clear an observed element")
	}

	s := NewLeafNode(String)
	s.DeclareKind(KindLeaf)
	if got := s.DeclaredTypes(); len(got) != 1 || got[0] != String {
		t.Errorf("DeclareKind must not clear observed types; got %v", got)
	}
}

// D2 holds however the scalar branch was established. DeclareKind is the one
// path that installs an EMPTY one, and it must collapse the marker exactly as
// AddScalarTypes does — a scalar declaration admits null on its own, so the
// marker has nothing left to record. Without this the node does not round-trip:
// the encoder writes the branch's empty type set, and the NULL is lost.
func TestDeclareKind_ScalarBranchCollapsesTheMarker(t *testing.T) {
	n := &ModelNode{}
	n.SetNullable()
	n.DeclareKind(KindLeaf)

	if n.Scalar() == nil {
		t.Fatal("DeclareKind(LEAF) establishes the scalar branch")
	}
	if n.Nullable() {
		t.Error("a node carrying a scalar branch does not record the marker separately")
	}

	raw, err := MarshalModelNode(n)
	if err != nil {
		t.Fatal(err)
	}
	back, err := UnmarshalModelNode(raw)
	if err != nil {
		t.Fatalf("Unmarshal(%s): %v", raw, err)
	}
	if back.Nullable() != n.Nullable() || (back.Scalar() == nil) != (n.Scalar() == nil) {
		t.Errorf("round trip changed the node: %s -> nullable=%v scalar=%v",
			raw, back.Nullable(), back.Scalar() != nil)
	}
}

// Declaring a CONTAINER kind leaves the marker alone: a nullable object is a
// real shape, and the wire form has always spelled it {"kind":"OBJECT","types":["NULL"]}.
func TestDeclareKind_ContainerKeepsTheMarker(t *testing.T) {
	n := &ModelNode{}
	n.SetNullable()
	n.DeclareKind(KindObject)

	if !n.Nullable() {
		t.Error("a nullable container keeps its marker")
	}
	raw, err := MarshalModelNode(n)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"kind":"OBJECT","types":["NULL"]}` {
		t.Errorf("Marshal = %s, want the unchanged nullable-object spelling", raw)
	}
}

// A kind the node NAMES is never dropped. The NULL-versus-empty-LEAF
// disambiguation exists because "kind":"LEAF" alone is ambiguous; where LEAF is
// named alongside another kind there is no ambiguity, so the scalar branch is
// established and the marker collapses into it rather than the branch
// vanishing.
func TestCodec_ANamedKindIsNeverDropped(t *testing.T) {
	const raw = `{"kinds":["LEAF","OBJECT"],"types":["NULL"],"children":{"k":{"kind":"LEAF","types":["STRING"]}}}`
	n, err := UnmarshalModelNode([]byte(raw))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if n.Scalar() == nil {
		t.Errorf("LEAF is named here, so it must not be dropped; kinds=%v", n.Kinds())
	}
	if n.Object() == nil {
		t.Errorf("OBJECT is named here too; kinds=%v", n.Kinds())
	}
}
