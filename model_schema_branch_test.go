package spi

import "testing"

// A node names the branches it carries. The accessors are the whole surface:
// there is no dominant kind to ask for, because a label could only ever name
// one of three independent payload slots.
func TestBranchAccessors(t *testing.T) {
	n := NewObjectNode()
	if n.Object() == nil {
		t.Fatal("an object node with no children still declares the object branch")
	}
	if n.Scalar() != nil || n.Array() != nil {
		t.Errorf("kinds = %v, want only OBJECT", n.Kinds())
	}
	if n.IsPolymorphic() {
		t.Error("one branch is not polymorphic")
	}

	marker := NewLeafNode(Null)
	if marker.Scalar() != nil {
		t.Error("NULL is a marker, not a scalar observation: no scalar branch")
	}
	if !marker.Nullable() || len(marker.Kinds()) != 0 {
		t.Errorf("the nullable marker declares no kind; kinds=%v nullable=%v", marker.Kinds(), marker.Nullable())
	}
	if got := marker.DeclaredTypes(); len(got) != 1 || got[0] != Null {
		t.Errorf("DeclaredTypes() = %v, want [NULL] — the persisted spelling is unchanged", got)
	}

	s := NewLeafNode(String)
	s.SetNullable()
	if s.Nullable() {
		t.Error("a node carrying a scalar branch does not record the marker separately")
	}
	if got := s.DeclaredTypes(); len(got) != 1 || got[0] != String {
		t.Errorf("DeclaredTypes() = %v, want [STRING]", got)
	}

	k := NewLeafNode(Null)
	k.AddScalarTypes(String)
	if k.Scalar() == nil || k.Nullable() {
		t.Error("adding a concrete type establishes the scalar branch and clears the marker")
	}
}

// Kinds() is ordered so callers, encoders and diagnostics are deterministic.
func TestKinds_AscendingOrder(t *testing.T) {
	n := NewObjectNode()
	n.AddScalarTypes(String)
	n.SetElement(NewLeafNode(Integer))
	want := []NodeKind{KindLeaf, KindObject, KindArray}
	got := n.Kinds()
	if len(got) != len(want) {
		t.Fatalf("Kinds() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Kinds() = %v, want %v", got, want)
		}
	}
	if !n.IsPolymorphic() {
		t.Error("three branches is polymorphic")
	}
}

// A mutator establishes the branch it needs and leaves the others alone.
func TestMutators_EstablishOneBranchEach(t *testing.T) {
	n := NewObjectNode()
	n.SetElement(NewLeafNode(String))
	if n.Array() == nil || n.Array().Element() == nil {
		t.Fatal("SetElement establishes the array branch")
	}
	if n.Object() == nil {
		t.Error("SetElement must not disturb the object branch")
	}

	m := NewLeafNode(String)
	m.SetChild("k", NewLeafNode(Integer))
	if m.Object() == nil || m.Object().Child("k") == nil {
		t.Fatal("SetChild establishes the object branch")
	}
	if m.Scalar() == nil {
		t.Error("SetChild must not disturb the scalar branch")
	}
}

// The scalar branch never holds NULL: that is what lets the persisted
// kind:"LEAF" spelling stay unambiguous.
func TestScalarBranch_NeverHoldsNull(t *testing.T) {
	n := NewLeafNode(String)
	n.AddScalarTypes(Null)
	for _, dt := range n.Scalar().Types() {
		if dt == Null {
			t.Fatalf("the scalar branch holds NULL: %v", n.Scalar().Types())
		}
	}
	if n.Nullable() {
		t.Error("and a scalar branch suppresses the marker")
	}
}
