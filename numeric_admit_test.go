package spi

import (
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestAdmitsNumeric(t *testing.T) {
	cases := []struct {
		name  string
		typ   DataType
		value string
		want  bool
	}{
		// INTEGER: whole and in bounds.
		{"integer at ceiling", Integer, "2147483647", true},
		{"integer past ceiling", Integer, "2147483648", false},
		{"integer at floor", Integer, "-2147483648", true},
		{"integer fractional", Integer, "13.111", false},
		{"integer whole with trailing zeros", Integer, "5.0", true},
		{"integer whole via exponent", Integer, "1e3", true},

		// LONG / BIG_INTEGER: same rule, wider bounds.
		{"long holds past 2^31", Long, "2147483648", true},
		{"long past ceiling", Long, "9223372036854775808", false},
		{"big integer holds past 2^63", BigInteger, "9223372036854775808", true},

		// UNBOUND_INTEGER: whole, no bound.
		{"unbound integer holds a huge whole", UnboundInteger, "1e40", true},
		{"unbound integer refuses a fraction", UnboundInteger, "1.5", false},

		// DOUBLE: range AND precision AND scale. This is the heart of §5.
		{"double holds a whole past 2^31", Double, "2147483648", true},
		{"double holds 1000", Double, "1000", true},
		{"double at the ceiling", Double, "9.99999999999999e292", true},
		{"double above the ceiling", Double, "9.99999999999999e300", false},
		{"double refuses 16 significant digits", Double, "9007199254740993", false},
		{"double refuses 16 significant digits, fractional", Double, "1.234567890123456", false},
		{"double holds 15 significant digits", Double, "1.23456789012345", true},
		{"double refuses scale past 292", Double, "1e-400", false},
		{"double holds scale at 292", Double, "1e-292", true},

		// BIG_DECIMAL: magnitude only — high scale is admitted.
		{"big decimal holds a high-scale value", BigDecimal, "1.23456789012345678901234567890", true},
		{"big decimal above magnitude", BigDecimal, "1e40", false},

		// UNBOUND_DECIMAL: the sink.
		{"unbound decimal holds anything", UnboundDecimal, "9.99999999999999e300", true},
		{"unbound decimal holds a fraction", UnboundDecimal, "1.5", true},

		// Non-numeric declared types are never asked, and answer false.
		{"string is not numeric", String, "5", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := ParseDecimal(tc.value)
			if err != nil {
				t.Fatalf("ParseDecimal(%q): %v", tc.value, err)
			}
			if got := AdmitsNumeric(tc.typ, v); got != tc.want {
				t.Errorf("AdmitsNumeric(%s, %s) = %v, want %v", tc.typ, tc.value, got, tc.want)
			}
		})
	}
}

// AdmitsNumeric runs on every number in a write body. A 13-byte literal
// with an enormous exponent must be answered, not expanded.
func TestAdmitsNumeric_HugeScaleIsCheap(t *testing.T) {
	for _, raw := range []string{"1e10000000", "-1e10000000", "1e-10000000", "123456e2000000000"} {
		v, err := ParseDecimal(raw)
		if err != nil {
			t.Fatalf("ParseDecimal(%q): %v", raw, err)
		}
		for _, dt := range []DataType{Integer, Long, BigInteger, UnboundInteger, Double, BigDecimal, UnboundDecimal} {
			done := make(chan bool, 1)
			go func() { done <- AdmitsNumeric(dt, v) }()
			select {
			case got := <-done:
				// Only the unbound sinks — plus BIG_DECIMAL, whose rule is
				// magnitude only (see AdmitsNumeric's doc comment: "high
				// scale is admitted") — can admit these; every other bounded
				// type must refuse them, and refuse them quickly. Of the four
				// literals only "1e-10000000" has an in-magnitude value (it
				// is within a whisker of zero); the other three exceed even
				// BIG_DECIMAL's ±INT128/10^18 magnitude bound.
				stripped := v.StripTrailingZeros()
				wantAdmit := dt == UnboundDecimal ||
					(dt == UnboundInteger && stripped.Scale() <= 0) ||
					(dt == BigDecimal && toRange(stripped, bd128Min, bd128Max) == inRangePos)
				if got != wantAdmit {
					t.Errorf("AdmitsNumeric(%s, %s) = %v, want %v", dt, raw, got, wantAdmit)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("AdmitsNumeric(%s, %s) did not return within 2s", dt, raw)
			}
		}
	}
}

// The invariant the predicate exists to secure: a value a type admits is a
// value an EQUALS on that type can find. Ranges alone do not give this, and
// neither does the predicate alone — the kernel's stored-value filter has
// to ask the same question, which is what this task makes it do.
func TestAdmitsNumeric_AdmittedValueIsFindable(t *testing.T) {
	values := []string{
		"1000", "2147483648", "9.99999999999999e292", "9007199254740993",
		"1.234567890123456", "1e-400", "1.23456789012345", "13.111", "1.5",
		"1.23456789012345678901234567890", "5.0", "-0.0",
	}
	types := []DataType{Integer, Long, BigInteger, UnboundInteger, Double, BigDecimal, UnboundDecimal}

	for _, raw := range values {
		v, err := ParseDecimal(raw)
		if err != nil {
			t.Fatalf("ParseDecimal(%q): %v", raw, err)
		}
		for _, dt := range types {
			if !AdmitsNumeric(dt, v) {
				continue
			}
			exp, err := ExpandLeaf(FilterEq, raw, nil, []DataType{dt})
			if err != nil {
				t.Errorf("%s admits %s but ExpandLeaf errored: %v", dt, raw, err)
				continue
			}
			if !EvalLeaf(exp, gjson.Parse(raw)) {
				t.Errorf("%s admits %s but EQUALS cannot find it", dt, raw)
			}
		}
	}
}
