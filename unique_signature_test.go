package spi

import (
	"encoding/json"
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

// TestComputeClaims_NoCollision is the permanent regression guard for the
// injectivity (collision-safety) property of the signature scheme.  Distinct
// value-sets must NEVER produce the same signature.
func TestComputeClaims_NoCollision(t *testing.T) {
	ab := []UniqueKey{{ID: "k", Fields: []string{"$.a", "$.b"}}}

	tests := []struct {
		name string
		docA string
		docB string
	}{
		{
			// Value-shift: shifting one character from a to b must differ.
			name: "value_shift",
			docA: `{"a":"a","b":"bc"}`,
			docB: `{"a":"ab","b":"c"}`,
		},
		{
			// Embedded separator byte: U+001F (0x1F) is used to join signature tokens.
			// A value containing this byte must not collide with an alternative that
			// boundary-shifts the same characters across fields (see dedicated subtest
			// below for the json.Marshal-based construction of this scenario).
			// This entry tests a plain boundary shift to anchor the table.
			name: "boundary_shift",
			docA: `{"a":"foo","b":"bar"}`,
			docB: `{"a":"foobar","b":"r"}`,
		},
		{
			// Lookalike token: a value whose content mimics a length-prefixed
			// token (e.g. "as1:b" resembles `s1:` prefix) must not collide with
			// the genuine two-segment arrangement where the other field holds "s1:b".
			name: "lookalike_token",
			docA: `{"a":"as1:b","b":"x"}`,
			docB: `{"a":"a","b":"s1:b"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ca, ea := ComputeClaims(ab, []byte(tc.docA))
			cb, eb := ComputeClaims(ab, []byte(tc.docB))
			if ea != nil || eb != nil {
				t.Fatalf("unexpected error: docA err=%v docB err=%v", ea, eb)
			}
			if len(ca) != 1 || len(cb) != 1 {
				t.Fatalf("expected one claim each: docA=%v docB=%v", ca, cb)
			}
			if ca[0].Signature == cb[0].Signature {
				t.Errorf("COLLISION: both docs produced signature %q", ca[0].Signature)
			}
		})
	}

	// Embedded separator byte: a value containing U+001F (the byte used to join
	// tokens) must not collide with a boundary-shifted alternative. Without the
	// string length-prefix both cases would produce the same raw joined bytes.
	// json.Marshal is used to build the documents so that the JSON encoding of the
	// control character is valid ( escape).
	t.Run("embedded_separator_byte", func(t *testing.T) {
		type row struct {
			A string `json:"a"`
			B string `json:"b"`
		}
		dA, _ := json.Marshal(row{A: "foo\x1fbar", B: "baz"}) // a contains U+001F
		dB, _ := json.Marshal(row{A: "foo", B: "bar\x1fbaz"}) // b contains U+001F
		ca, ea := ComputeClaims(ab, dA)
		cb, eb := ComputeClaims(ab, dB)
		if ea != nil || eb != nil {
			t.Fatalf("unexpected error: docA err=%v docB err=%v", ea, eb)
		}
		if len(ca) != 1 || len(cb) != 1 {
			t.Fatalf("expected one claim each: docA=%s docB=%s", dA, dB)
		}
		if ca[0].Signature == cb[0].Signature {
			t.Errorf("COLLISION via embedded separator: both produced %q", ca[0].Signature)
		}
	})

	// Empty-string-present vs all-absent: the two must NOT be conflated.
	// Empty strings are present (s0:), so all-present → claim emitted.
	// Absent fields → exempt, no claim.
	t.Run("empty_string_vs_absent", func(t *testing.T) {
		cPresent, ePresent := ComputeClaims(ab, []byte(`{"a":"","b":""}`))
		cAbsent, eAbsent := ComputeClaims(ab, []byte(`{}`))
		if ePresent != nil {
			t.Fatalf("empty strings must not error: %v", ePresent)
		}
		if eAbsent != nil {
			t.Fatalf("absent fields must not error: %v", eAbsent)
		}
		if len(cPresent) != 1 {
			t.Errorf("both empty strings present: expected 1 claim, got %d", len(cPresent))
		}
		if len(cAbsent) != 0 {
			t.Errorf("all fields absent: expected 0 claims (exempt), got %d", len(cAbsent))
		}
	})
}

// TestComputeClaims_CoeffDigitBoundary verifies that the coeff-digit DoS guard
// accepts exactly maxCoeffDigits (64) coefficient characters and rejects 65.
func TestComputeClaims_CoeffDigitBoundary(t *testing.T) {
	singleField := []UniqueKey{{ID: "k", Fields: []string{"$.v"}}}

	// 64-digit integer: must canonicalize and emit a claim.
	digits64 := "1234567890123456789012345678901234567890123456789012345678901234" // 64 chars
	doc64 := []byte(`{"v":` + digits64 + `}`)
	c, err := ComputeClaims(singleField, doc64)
	if err != nil || len(c) != 1 {
		t.Errorf("64-digit value must succeed: claims=%v err=%v", c, err)
	}

	// 65-digit integer: must be rejected with ErrPartialUniqueKey (coeff-digit bound).
	digits65 := digits64 + "5" // 65 chars
	doc65 := []byte(`{"v":` + digits65 + `}`)
	_, err = ComputeClaims(singleField, doc65)
	if !errors.Is(err, ErrPartialUniqueKey) {
		t.Errorf("65-digit value must fail with ErrPartialUniqueKey, got: %v", err)
	}
}
