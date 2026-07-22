package spi

import "testing"

func TestParseTemporalMillis(t *testing.T) {
	cases := []struct {
		in string
		ms int64
		ok bool
	}{
		{"2021-01-01T00:00:00Z", 1609459200000, true},
		{"2021-01-01T00:00:00.000Z", 1609459200000, true},  // same instant as above
		{"2021-06-01T14:00:00+02:00", 1622548800000, true}, // = 12:00Z
		{"2021-06-01T13:00:00Z", 1622552400000, true},      // 1h after the +02:00 one
		{"2021-01-01T00:00:00.5Z", 1609459200500, true},    // sub-second kept to ms
		{"2021-01-01T00:00:00", 0, false},                  // offset-less rejected
		{"2021-01-01", 0, false},                           // date-only rejected
		{"not-a-date", 0, false},
	}
	for _, c := range cases {
		ms, ok := ParseTemporalMillis(c.in)
		if ok != c.ok || (ok && ms != c.ms) {
			t.Errorf("ParseTemporalMillis(%q) = (%d,%v), want (%d,%v)", c.in, ms, ok, c.ms, c.ok)
		}
	}
}

func TestCompareTemporal(t *testing.T) {
	const a = int64(1000) // stored
	// op, stored, storedOK, lo, hi, loHiOK -> want
	type c struct {
		op        FilterOp
		stored    int64
		sok       bool
		lo, hi    int64
		lok, want bool
	}
	cases := []c{
		{FilterEq, 1000, true, 1000, 0, true, true},
		{FilterEq, 1000, true, 1001, 0, true, false},
		{FilterNe, 1000, true, 1000, 0, true, false},
		{FilterNe, 1000, true, 1001, 0, true, true},
		{FilterGt, 1000, true, 999, 0, true, true},
		{FilterGte, 1000, true, 1000, 0, true, true},
		{FilterLt, 1000, true, 1000, 0, true, false},
		{FilterLte, 1000, true, 1000, 0, true, true},
		{FilterBetween, 1000, true, 900, 1100, true, true},
		{FilterBetween, 1000, true, 1001, 1100, true, false},
		// stored not convertible -> exclude for positive, vacuous-true for NE
		{FilterEq, 0, false, 1000, 0, true, false},
		{FilterGt, 0, false, 1000, 0, true, false},
		{FilterNe, 0, false, 1000, 0, true, true},
	}
	_ = a
	for i, tc := range cases {
		got := CompareTemporal(tc.op, tc.stored, tc.sok, tc.lo, tc.hi, tc.lok)
		if got != tc.want {
			t.Errorf("case %d: CompareTemporal(%v,...) = %v want %v", i, tc.op, got, tc.want)
		}
	}
}

func TestNumericFloatNoStringParse(t *testing.T) {
	if _, ok := NumericFloat("20"); ok {
		t.Error("NumericFloat must NOT parse strings")
	}
	if f, ok := NumericFloat(float64(20)); !ok || f != 20 {
		t.Errorf("NumericFloat(20.0) = (%v,%v)", f, ok)
	}
}
