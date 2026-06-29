package spi

import (
	"errors"
	"testing"
)

func ks() []UniqueKey { return []UniqueKey{{ID: "k", Fields: []string{"$.email", "$.age"}}} }

func TestComputeClaims_Full(t *testing.T) {
	c, e := ComputeClaims(ks(), []byte(`{"email":"a@x.com","age":42}`))
	if e != nil || len(c) != 1 {
		t.Fatalf("%+v %v", c, e)
	}
}

func TestComputeClaims_NumCanon(t *testing.T) {
	a, _ := ComputeClaims(ks(), []byte(`{"email":"a","age":42}`))
	b, _ := ComputeClaims(ks(), []byte(`{"email":"a","age":42.0}`))
	d, _ := ComputeClaims(ks(), []byte(`{"email":"a","age":4.2e1}`))
	if a[0].Signature != b[0].Signature || b[0].Signature != d[0].Signature {
		t.Fatalf("42/42.0/4.2e1 must collide: a=%q b=%q d=%q", a[0].Signature, b[0].Signature, d[0].Signature)
	}
}

func TestComputeClaims_NegZeroCanon(t *testing.T) {
	// -0 and 0 must produce the same signature (JSON -0 equals zero).
	pos, _ := ComputeClaims(ks(), []byte(`{"email":"a","age":0}`))
	neg, _ := ComputeClaims(ks(), []byte(`{"email":"a","age":-0}`))
	if pos[0].Signature != neg[0].Signature {
		t.Fatalf("-0 and 0 must collide: pos=%q neg=%q", pos[0].Signature, neg[0].Signature)
	}
}

func TestComputeClaims_ExpCaseCanon(t *testing.T) {
	// 1, 1.0, 1e0, and 1E0 must all produce the same signature (case-insensitive exponent).
	one, _ := ComputeClaims(ks(), []byte(`{"email":"a","age":1}`))
	oneF, _ := ComputeClaims(ks(), []byte(`{"email":"a","age":1.0}`))
	oneE, _ := ComputeClaims(ks(), []byte(`{"email":"a","age":1e0}`))
	oneEU, _ := ComputeClaims(ks(), []byte(`{"email":"a","age":1E0}`))
	if one[0].Signature != oneF[0].Signature || oneF[0].Signature != oneE[0].Signature || oneE[0].Signature != oneEU[0].Signature {
		t.Fatalf("1/1.0/1e0/1E0 must collide: %q %q %q %q",
			one[0].Signature, oneF[0].Signature, oneE[0].Signature, oneEU[0].Signature)
	}
}

func TestComputeClaims_BigInt(t *testing.T) {
	a, _ := ComputeClaims(ks(), []byte(`{"email":"a","age":9007199254740993}`))
	b, _ := ComputeClaims(ks(), []byte(`{"email":"a","age":9007199254740992}`))
	if a[0].Signature == b[0].Signature {
		t.Fatal(">2^53 must differ")
	}
}

func TestComputeClaims_TypeTag(t *testing.T) {
	a, _ := ComputeClaims([]UniqueKey{{ID: "k", Fields: []string{"$.v"}}}, []byte(`{"v":"1"}`))
	b, _ := ComputeClaims([]UniqueKey{{ID: "k", Fields: []string{"$.v"}}}, []byte(`{"v":1}`))
	if a[0].Signature == b[0].Signature {
		t.Fatal(`"1" != 1`)
	}
}

func TestComputeClaims_AllNull(t *testing.T) {
	c, e := ComputeClaims(ks(), []byte(`{"email":null,"age":null}`))
	if e != nil || len(c) != 0 {
		t.Fatalf("exempt: %+v %v", c, e)
	}
}

func TestComputeClaims_Partial(t *testing.T) {
	_, e := ComputeClaims(ks(), []byte(`{"email":"a"}`))
	if !errors.Is(e, ErrPartialUniqueKey) {
		t.Fatalf("got %v", e)
	}
}

func TestComputeClaims_OverBound(t *testing.T) {
	_, e := ComputeClaims([]UniqueKey{{ID: "k", Fields: []string{"$.v"}}}, []byte(`{"v":1e1000000000}`))
	if !errors.Is(e, ErrPartialUniqueKey) {
		t.Fatalf("over-bound must reject pre-materialization, got %v", e)
	}
}

func TestComputeClaims_NonScalar(t *testing.T) {
	_, e := ComputeClaims([]UniqueKey{{ID: "k", Fields: []string{"$.v"}}}, []byte(`{"v":{"x":1}}`))
	if !errors.Is(e, ErrPartialUniqueKey) {
		t.Fatalf("non-scalar must reject, got %v", e)
	}
}

func TestComputeClaims_NonScalarArray(t *testing.T) {
	// An array value at a declared key path is non-scalar and must be rejected.
	_, e := ComputeClaims([]UniqueKey{{ID: "k", Fields: []string{"$.v"}}}, []byte(`{"v":[1,2]}`))
	if !errors.Is(e, ErrPartialUniqueKey) {
		t.Fatalf("array non-scalar must reject with ErrPartialUniqueKey, got %v", e)
	}
}

func TestComputeClaims_Nested(t *testing.T) {
	c, e := ComputeClaims([]UniqueKey{{ID: "k", Fields: []string{"$.a.b"}}}, []byte(`{"a":{"b":7}}`))
	if e != nil || len(c) != 1 {
		t.Fatalf("nested: %+v %v", c, e)
	}
}
