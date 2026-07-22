package spi

import "testing"

func TestParseTemporalMillis(t *testing.T) {
	cases := []struct {
		in string
		ms int64
		ok bool
	}{
		{"2021-01-01T00:00:00Z", 1609459200000, true},
		{"2021-01-01T00:00:00.000Z", 1609459200000, true}, // same instant as above
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
