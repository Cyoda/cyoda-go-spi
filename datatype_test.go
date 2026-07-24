package spi_test

import (
	"testing"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

func TestDataTypeString(t *testing.T) {
	if spi.String.String() != "STRING" {
		t.Errorf("expected STRING, got %s", spi.String)
	}
	if spi.Integer.String() != "INTEGER" {
		t.Errorf("expected INTEGER, got %s", spi.Integer)
	}
}

func TestTypeSetAdd(t *testing.T) {
	ts := spi.NewTypeSet()
	ts.Add(spi.String)
	ts.Add(spi.Integer)
	ts.Add(spi.String) // duplicate

	types := ts.Types()
	if len(types) != 2 {
		t.Fatalf("expected 2 types, got %d", len(types))
	}
}

func TestTypeSetIsSorted(t *testing.T) {
	ts := spi.NewTypeSet()
	ts.Add(spi.String)
	ts.Add(spi.Boolean)
	ts.Add(spi.Integer)

	types := ts.Types()
	for i := 1; i < len(types); i++ {
		if types[i] < types[i-1] {
			t.Errorf("types not sorted: %v", types)
		}
	}
}

func TestTypeSetUnion(t *testing.T) {
	a := spi.NewTypeSet()
	a.Add(spi.String)

	b := spi.NewTypeSet()
	b.Add(spi.Integer)

	c := spi.Union(a, b)
	types := c.Types()
	if len(types) != 2 {
		t.Fatalf("expected 2 types in union, got %d", len(types))
	}
}

func TestParseDataType(t *testing.T) {
	dt, ok := spi.ParseDataType("INTEGER")
	if !ok {
		t.Fatal("expected ParseDataType to find INTEGER")
	}
	if dt != spi.Integer {
		t.Errorf("expected Integer, got %v", dt)
	}

	_, ok = spi.ParseDataType("BOGUS")
	if ok {
		t.Error("expected ParseDataType to return false for unknown name")
	}
}

func TestTypeSetIsEmpty(t *testing.T) {
	ts := spi.NewTypeSet()
	if !ts.IsEmpty() {
		t.Error("new TypeSet should be empty")
	}
	ts.Add(spi.String)
	if ts.IsEmpty() {
		t.Error("TypeSet should not be empty after Add")
	}
}

func TestDataTypeStringUnknown(t *testing.T) {
	unknown := spi.DataType(9999)
	if unknown.String() != "UNKNOWN" {
		t.Errorf("expected UNKNOWN, got %s", unknown.String())
	}
}

func TestTypeSetUnionOverlapping(t *testing.T) {
	a := spi.NewTypeSet()
	a.Add(spi.String)
	a.Add(spi.Integer)

	b := spi.NewTypeSet()
	b.Add(spi.String)
	b.Add(spi.Boolean)

	c := spi.Union(a, b)
	types := c.Types()
	if len(types) != 3 {
		t.Fatalf("expected 3 types in overlapping union, got %d", len(types))
	}
}

func TestTypeSetEqual(t *testing.T) {
	a := spi.NewTypeSet()
	b := spi.NewTypeSet()
	if !a.Equal(b) {
		t.Error("two empty TypeSets should be equal")
	}

	a.Add(spi.String)
	a.Add(spi.Integer)
	b.Add(spi.String)
	b.Add(spi.Integer)
	if !a.Equal(b) {
		t.Error("identical TypeSets should be equal")
	}

	c := spi.NewTypeSet()
	c.Add(spi.Boolean)
	if a.Equal(c) {
		t.Error("different TypeSets should not be equal")
	}
}

func TestTypeSetNumericLatching(t *testing.T) {
	ts := spi.NewTypeSet()
	ts.Add(spi.Integer)
	ts.Add(spi.Long)
	types := ts.Types()
	if len(types) != 1 {
		t.Fatalf("expected 1 type after latching, got %d: %v", len(types), types)
	}
	if types[0] != spi.Long {
		t.Errorf("expected Long, got %v", types[0])
	}
}

func TestTypeSetNumericLatchingDecimal(t *testing.T) {
	// DOUBLE does not widen to BIG_DECIMAL (different decimal branches).
	// Their join in the widening lattice is UNBOUND_DECIMAL.
	ts := spi.NewTypeSet()
	ts.Add(spi.Double)
	ts.Add(spi.BigDecimal)
	types := ts.Types()
	if len(types) != 1 {
		t.Fatalf("expected 1 type after latching, got %d: %v", len(types), types)
	}
	if types[0] != spi.UnboundDecimal {
		t.Errorf("expected UnboundDecimal, got %v", types[0])
	}
}

func TestTypeSetNumericCrossFamily(t *testing.T) {
	// Integer widens to Double (IsAssignableTo(Integer, Double) = true).
	// Double is the narrowest common supertype; no escalation needed.
	ts := spi.NewTypeSet()
	ts.Add(spi.Integer)
	ts.Add(spi.Double)
	types := ts.Types()
	if len(types) != 1 {
		t.Fatalf("expected 1 type after cross-family collapse, got %d: %v", len(types), types)
	}
	if types[0] != spi.Double {
		t.Errorf("expected Double, got %v", types[0])
	}
}

func TestTypeSetNumericLatchingViaUnion(t *testing.T) {
	a := spi.NewTypeSet()
	a.Add(spi.Integer)
	b := spi.NewTypeSet()
	b.Add(spi.Long)
	c := spi.Union(a, b)
	types := c.Types()
	if len(types) != 1 {
		t.Fatalf("expected 1 type after union latching, got %d: %v", len(types), types)
	}
	if types[0] != spi.Long {
		t.Errorf("expected Long, got %v", types[0])
	}
}

func TestTypeSetIsPolymorphic(t *testing.T) {
	mono := spi.NewTypeSet()
	mono.Add(spi.String)
	if mono.IsPolymorphic() {
		t.Error("single type should not be polymorphic")
	}

	poly := spi.NewTypeSet()
	poly.Add(spi.String)
	poly.Add(spi.Integer)
	if !poly.IsPolymorphic() {
		t.Error("two types should be polymorphic")
	}
}

func TestTypeSetAdd_NumericCollapse_SameFamily(t *testing.T) {
	ts := spi.NewTypeSet()
	ts.Add(spi.Integer)
	ts.Add(spi.Long)
	got := ts.Types()
	if len(got) != 1 || got[0] != spi.Long {
		t.Errorf("Integer+Long: got %v, want [Long]", got)
	}
}

func TestTypeSetAdd_NumericCollapse_CrossFamily(t *testing.T) {
	// Integer widens to Double; Double is the narrowest common supertype.
	ts := spi.NewTypeSet()
	ts.Add(spi.Integer)
	ts.Add(spi.Double)
	got := ts.Types()
	if len(got) != 1 || got[0] != spi.Double {
		t.Errorf("Integer+Double: got %v, want [Double]", got)
	}
}

func TestTypeSetAdd_NullDropsOnConcrete(t *testing.T) {
	ts := spi.NewTypeSet()
	ts.Add(spi.Null)
	ts.Add(spi.Integer)
	got := ts.Types()
	if len(got) != 1 || got[0] != spi.Integer {
		t.Errorf("Null+Integer: got %v, want [Integer]", got)
	}
}

func TestTypeSetAdd_ConcreteDropsNull(t *testing.T) {
	ts := spi.NewTypeSet()
	ts.Add(spi.Integer)
	ts.Add(spi.Null)
	got := ts.Types()
	if len(got) != 1 || got[0] != spi.Integer {
		t.Errorf("Integer+Null: got %v, want [Integer]", got)
	}
}

func TestTypeSetAdd_NullAlone(t *testing.T) {
	ts := spi.NewTypeSet()
	ts.Add(spi.Null)
	got := ts.Types()
	if len(got) != 1 || got[0] != spi.Null {
		t.Errorf("Null alone: got %v, want [Null]", got)
	}
}

func TestTypeSetAdd_CrossKindPreserved(t *testing.T) {
	ts := spi.NewTypeSet()
	ts.Add(spi.Integer)
	ts.Add(spi.String)
	got := ts.Types()
	if len(got) != 2 {
		t.Errorf("Integer+String: got %v, want 2 elements", got)
	}
	hasInt := false
	hasStr := false
	for _, dt := range got {
		if dt == spi.Integer {
			hasInt = true
		}
		if dt == spi.String {
			hasStr = true
		}
	}
	if !hasInt || !hasStr {
		t.Errorf("Integer+String: expected both; got %v", got)
	}
}

func TestTypeSetAdd_CrossKindWithNumericCollapse(t *testing.T) {
	ts := spi.NewTypeSet()
	ts.Add(spi.Integer)
	ts.Add(spi.Double)
	ts.Add(spi.String)
	ts.Add(spi.Null)
	got := ts.Types()
	// Expected: [Double, String] — Null drops, Integer+Double collapse to Double
	// (Integer widens to Double; Double is the narrowest common supertype).
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 elements", got)
	}
	hasDbl := false
	hasStr := false
	for _, dt := range got {
		if dt == spi.Double {
			hasDbl = true
		}
		if dt == spi.String {
			hasStr = true
		}
	}
	if !hasDbl || !hasStr {
		t.Errorf("got %v, want [Double, String]", got)
	}
}

func TestTypeSetAdd_NonNumericOnlyUnchangedBehavior(t *testing.T) {
	ts := spi.NewTypeSet()
	ts.Add(spi.String)
	ts.Add(spi.Boolean)
	got := ts.Types()
	if len(got) != 2 {
		t.Errorf("String+Boolean: got %v, want 2 elements", got)
	}
}
