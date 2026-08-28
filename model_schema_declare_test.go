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
