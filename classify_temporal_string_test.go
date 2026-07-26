package spi

import "testing"

func TestClassifyTemporalString(t *testing.T) {
	cases := []struct {
		s      string
		want   DataType
		wantOK bool
	}{
		{"2024-09-09", LocalDate, true},
		{"2024-09-09T10:30:00", LocalDateTime, true},
		{"2024-09-09T10:30:00+01:00", ZonedDateTime, true},
		{"10:30:00", LocalTime, true},
		{"2024-09", YearMonth, true},
		{"2024", Year, true},
		{"hello", Null, false},
		{"", Null, false},
		{"not-a-date", Null, false},
	}
	for _, c := range cases {
		got, ok := ClassifyTemporalString(c.s)
		if ok != c.wantOK || (ok && got != c.want) {
			t.Errorf("ClassifyTemporalString(%q) = (%v,%v), want (%v,%v)", c.s, got, ok, c.want, c.wantOK)
		}
	}
}
