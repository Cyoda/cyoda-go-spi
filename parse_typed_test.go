package spi

import "testing"

// TestParseStringOrNull is seeded from oracle C.7 + Cloud's
// DataType.parseStringOrNull rules (DataType.kt:125-166).
//
// Note: the oracle's {"false", Boolean, false} row conflates parse-success
// with the parsed value — "false" parses successfully (ok=true) with value
// false. That is fixed here; the parsed boolean VALUE is asserted
// separately in TestParseStringOrNullBooleanValue.
func TestParseStringOrNull(t *testing.T) {
	cases := []struct {
		operand string
		typ     DataType
		wantOK  bool
	}{
		// --- verbatim from the oracle (Boolean "false" row corrected) ---
		{"30", Integer, true}, {"1.0", Integer, true}, {"1E2", Integer, true},
		{"1.5", Integer, false}, {"2147483648", Integer, false}, // int32 overflow → Long only
		{"2147483648", Long, true}, {"170141183460469231731687303715884105728", BigInteger, false}, // 2^127 → unbound
		{"170141183460469231731687303715884105727", BigInteger, true},
		{"12.78", Double, true}, {"12.5", Integer, false},
		{"true", Boolean, true}, {"false", Boolean, true}, // "false" parses; value asserted separately
		{"TRUE", Boolean, false}, {"yes", Boolean, false},
		{"hello", String, true}, {"", String, true},
		{"a", Character, true}, {"ab", Character, false},
		{"12345678-1234-1234-1234-123456789abc", UUIDType, true},
		{"abc", Integer, false},

		// --- additional branch coverage ---
		{"", Integer, false},                                              // empty operand doesn't parse as a decimal at all
		{"9223372036854775807", Long, true},                               // int64 max — fits Long
		{"9223372036854775808", Long, false},                              // 2^63 — overflows Long, classifies BigInteger
		{"170141183460469231731687303715884105728", UnboundInteger, true}, // 2^127 — doesn't fit BigInteger but fits UnboundInteger
		{"123456789012345.67", Double, false},                             // precision 17 > 15 — fails Double envelope
		{"123456789012345.67", BigDecimal, true},                          // same value fits BigDecimal (scale 2 <= 18, exp 15 <= 20)
		{"1E1000", Double, false},                                         // |scale| 1000 > 292 — fails Double envelope
		{"1E1000", UnboundDecimal, true},                                  // UnboundDecimal accepts any parseable number
		{"2b210fd0-87c7-11f1-aea2-ae468cd3ed17", TimeUUIDType, true},      // version-1 UUID
		{"38d81bba-d200-4751-ae56-cc0e4a0efa95", TimeUUIDType, false},     // version-4 UUID — not TimeUUIDType
		{"38d81bba-d200-4751-ae56-cc0e4a0efa95", UUIDType, true},          // but a valid UUID
		{"12345678-1234-1234-1234-123456789ABC", UUIDType, false},         // uppercase — lowercase-only RFC regex
		{"not-a-uuid", UUIDType, false},
		{"2024-01-01", LocalDate, false}, // temporal types are not handled here
	}
	for _, c := range cases {
		_, ok := ParseStringOrNull(c.operand, c.typ)
		if ok != c.wantOK {
			t.Errorf("ParseStringOrNull(%q,%v)=%v want %v", c.operand, c.typ, ok, c.wantOK)
		}
	}
}

// TestParseStringOrNullBooleanValue asserts the parsed VALUE for Boolean,
// split out from the ok-only table above per the oracle-bug fix.
func TestParseStringOrNullBooleanValue(t *testing.T) {
	v, ok := ParseStringOrNull("true", Boolean)
	if !ok || v != true {
		t.Errorf(`ParseStringOrNull("true", Boolean) = (%v, %v), want (true, true)`, v, ok)
	}
	v, ok = ParseStringOrNull("false", Boolean)
	if !ok || v != false {
		t.Errorf(`ParseStringOrNull("false", Boolean) = (%v, %v), want (false, true)`, v, ok)
	}
}

// TestParseStringOrNullCharacterValue asserts the parsed rune value.
func TestParseStringOrNullCharacterValue(t *testing.T) {
	v, ok := ParseStringOrNull("a", Character)
	if !ok || v != rune('a') {
		t.Errorf(`ParseStringOrNull("a", Character) = (%v, %v), want ('a', true)`, v, ok)
	}
}

// TestParseStringOrNullStringValue asserts String always round-trips the
// operand verbatim, including the empty string.
func TestParseStringOrNullStringValue(t *testing.T) {
	v, ok := ParseStringOrNull("hello", String)
	if !ok || v != "hello" {
		t.Errorf(`ParseStringOrNull("hello", String) = (%v, %v), want ("hello", true)`, v, ok)
	}
	v, ok = ParseStringOrNull("", String)
	if !ok || v != "" {
		t.Errorf(`ParseStringOrNull("", String) = (%v, %v), want ("", true)`, v, ok)
	}
}
