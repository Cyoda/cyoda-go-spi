package spi

import (
	"testing"
	"time"
)

// utcMillis is a test helper: epoch-millis of the given wall-clock fields
// interpreted at UTC (the anchor this kernel uses for every zone-less subtype).
func utcMillis(year int, month time.Month, day, hour, min, sec int) int64 {
	return time.Date(year, month, day, hour, min, sec, 0, time.UTC).UnixMilli()
}

// ---------------------------------------------------------------------------
// ParseTemporalSubtype — per-subtype ISO-8601 acceptance (LeafFieldParser.kt)
// ---------------------------------------------------------------------------

func TestTemporalParseSubtype_Acceptance(t *testing.T) {
	cases := []struct {
		operand string
		typ     DataType
		wantOK  bool
	}{
		{"2024", Year, true},
		{"2024-09", YearMonth, true},
		{"2024-09-09", LocalDate, true},
		{"2024-09-09T10:30:00", LocalDateTime, true},
		{"10:30:00", LocalTime, true},
		{"2024-09-09T10:30:00+01:00", ZonedDateTime, true},
		{"2024-09-09T10:30:00Z", ZonedDateTime, true},

		// Seconds-optional forms (Java ISO_ZONED_DATE_TIME / ISO_DATE_TIME
		// parity — seconds are optional, unlike RFC3339Nano).
		{"2024-09-09T10:30+01:00", ZonedDateTime, true},
		{"2024-09-09T10:30Z", ZonedDateTime, true},
		{"2024-09-09T10:30", LocalDateTime, true},
		{"2024-09-09T10:30+01:00", LocalDateTime, true},

		// ZONED_DATE_TIME REQUIRES an offset — offset-less input must fail
		// (the data-field offset-mandatory rule), seconds-present or not.
		{"2024-09-09T10:30:00", ZonedDateTime, false},
		{"2024-09-09T10:30", ZonedDateTime, false},

		// Cross-type rejections: a coarse string must not parse as a finer type.
		{"2024", YearMonth, false},
		{"2024", LocalDate, false},
		{"2024-09", LocalDate, false},
		{"2024-09-09", YearMonth, false},
		{"2024-09-09", Year, false},
		{"10:30:00", LocalDate, false},
		{"not-a-date", LocalDate, false},
	}
	for _, c := range cases {
		_, ok := ParseTemporalSubtype(c.operand, c.typ)
		if ok != c.wantOK {
			t.Errorf("ParseTemporalSubtype(%q, %v) ok=%v, want %v", c.operand, c.typ, ok, c.wantOK)
		}
	}
}

func TestTemporalParseSubtype_Millis(t *testing.T) {
	// Year anchors to Jan-1 at UTC midnight.
	tv, ok := ParseTemporalSubtype("2024", Year)
	if !ok || tv.Millis() != utcMillis(2024, time.January, 1, 0, 0, 0) {
		t.Fatalf("Year millis: ok=%v millis=%d", ok, tv.Millis())
	}
	// LocalDate anchors to UTC midnight of the date.
	tv, ok = ParseTemporalSubtype("2024-09-09", LocalDate)
	if !ok || tv.Millis() != utcMillis(2024, time.September, 9, 0, 0, 0) {
		t.Fatalf("LocalDate millis: ok=%v millis=%d", ok, tv.Millis())
	}
	// ZonedDateTime uses the true instant from its offset:
	// 2024-09-09T10:30+01:00 == 09:30Z.
	tv, ok = ParseTemporalSubtype("2024-09-09T10:30:00+01:00", ZonedDateTime)
	if !ok || tv.Millis() != utcMillis(2024, time.September, 9, 9, 30, 0) {
		t.Fatalf("ZDT millis: ok=%v millis=%d", ok, tv.Millis())
	}
}

// TestTemporalParseSubtype_SecondsOptional pins the Java ISO_ZONED_DATE_TIME /
// ISO_DATE_TIME parity fix: seconds are optional, unlike time.RFC3339Nano
// which mandates them. A seconds-absent operand must parse to the same
// instant/wall-clock as its seconds-present (":00") equivalent, and
// ZonedDateTime must still require an explicit offset.
func TestTemporalParseSubtype_SecondsOptional(t *testing.T) {
	// ZonedDateTime, offset present, seconds absent: same instant as the
	// seconds-present form (seconds default to :00).
	withSecs, ok := ParseTemporalSubtype("2024-09-09T10:30:00+01:00", ZonedDateTime)
	if !ok {
		t.Fatalf("seconds-present ZDT operand failed to parse")
	}
	noSecs, ok := ParseTemporalSubtype("2024-09-09T10:30+01:00", ZonedDateTime)
	if !ok {
		t.Fatalf("ParseTemporalSubtype(%q, ZonedDateTime) ok=false, want true", "2024-09-09T10:30+01:00")
	}
	if noSecs.Millis() != withSecs.Millis() {
		t.Fatalf("seconds-optional ZDT millis mismatch: got %d, want %d (from seconds-present form)",
			noSecs.Millis(), withSecs.Millis())
	}

	// LocalDateTime, offset absent, seconds absent: same wall-clock as the
	// seconds-present form.
	withSecsLDT, ok := ParseTemporalSubtype("2024-09-09T10:30:00", LocalDateTime)
	if !ok {
		t.Fatalf("seconds-present LocalDateTime operand failed to parse")
	}
	noSecsLDT, ok := ParseTemporalSubtype("2024-09-09T10:30", LocalDateTime)
	if !ok {
		t.Fatalf("ParseTemporalSubtype(%q, LocalDateTime) ok=false, want true", "2024-09-09T10:30")
	}
	if noSecsLDT.Millis() != withSecsLDT.Millis() {
		t.Fatalf("seconds-optional LocalDateTime millis mismatch: got %d, want %d (from seconds-present form)",
			noSecsLDT.Millis(), withSecsLDT.Millis())
	}

	// ZonedDateTime still REQUIRES an offset: a bare seconds-optional string
	// must not parse as ZonedDateTime, only as LocalDateTime.
	if _, ok := ParseTemporalSubtype("2024-09-09T10:30", ZonedDateTime); ok {
		t.Fatalf("ParseTemporalSubtype(%q, ZonedDateTime) ok=true, want false (offset still required)", "2024-09-09T10:30")
	}
}

// ---------------------------------------------------------------------------
// ExpandTemporalOperand — resolution graph (PolymorphicTemporalConversions.kt)
// ---------------------------------------------------------------------------

func condEqual(a TemporalSubCondition, typ DataType, op FilterOp, millis int64) bool {
	return a.Type == typ && a.Op == op && a.Millis == millis
}

func TestTemporalExpand_DownscaleOpMutation(t *testing.T) {
	// ">=","2024-09-09",[YEAR] → LD→YM→YEAR, imprecise → >= becomes >, value 2024.
	got := ExpandTemporalOperand("2024-09-09", []DataType{Year}, FilterGte)
	if len(got) != 1 || !condEqual(got[0], Year, FilterGt, utcMillis(2024, time.January, 1, 0, 0, 0)) {
		t.Fatalf("Gte downscale: got %+v", got)
	}

	// "<","2024-09-09",[YEAR] → imprecise LESS_THAN → LESS_OR_EQUAL.
	got = ExpandTemporalOperand("2024-09-09", []DataType{Year}, FilterLt)
	if len(got) != 1 || !condEqual(got[0], Year, FilterLte, utcMillis(2024, time.January, 1, 0, 0, 0)) {
		t.Fatalf("Lt downscale: got %+v", got)
	}

	// ">","2024-09-09",[YEAR] → GREATER_THAN unchanged (idempotent).
	got = ExpandTemporalOperand("2024-09-09", []DataType{Year}, FilterGt)
	if len(got) != 1 || !condEqual(got[0], Year, FilterGt, utcMillis(2024, time.January, 1, 0, 0, 0)) {
		t.Fatalf("Gt downscale: got %+v", got)
	}

	// "<=","2024-09-09",[YEAR] → LESS_OR_EQUAL unchanged.
	got = ExpandTemporalOperand("2024-09-09", []DataType{Year}, FilterLte)
	if len(got) != 1 || !condEqual(got[0], Year, FilterLte, utcMillis(2024, time.January, 1, 0, 0, 0)) {
		t.Fatalf("Lte downscale: got %+v", got)
	}
}

func TestTemporalExpand_ImpreciseEqualsDropped(t *testing.T) {
	// "=","2024-09-09",[YEAR] → imprecise EQUALS → whole branch dropped.
	got := ExpandTemporalOperand("2024-09-09", []DataType{Year}, FilterEq)
	if len(got) != 0 {
		t.Fatalf("imprecise EQUALS should drop: got %+v", got)
	}
}

func TestTemporalExpand_PreciseEqualsKept(t *testing.T) {
	// "=","2024-01-01T00:00:00",[LOCAL_DATE,YEAR_MONTH,YEAR] all precise → kept.
	got := ExpandTemporalOperand("2024-01-01T00:00:00", []DataType{LocalDate, YearMonth, Year}, FilterEq)
	if len(got) != 3 {
		t.Fatalf("precise EQUALS should keep all: got %+v", got)
	}
	if !condEqual(got[0], LocalDate, FilterEq, utcMillis(2024, time.January, 1, 0, 0, 0)) {
		t.Errorf("LocalDate branch: got %+v", got[0])
	}
	if !condEqual(got[1], YearMonth, FilterEq, utcMillis(2024, time.January, 1, 0, 0, 0)) {
		t.Errorf("YearMonth branch: got %+v", got[1])
	}
	if !condEqual(got[2], Year, FilterEq, utcMillis(2024, time.January, 1, 0, 0, 0)) {
		t.Errorf("Year branch: got %+v", got[2])
	}
}

func TestTemporalExpand_LDTToLocalTimeNoOpMutation(t *testing.T) {
	// LDT→LOCAL_TIME is the sole downscale edge with modifyOperationOnNonPrecise=false:
	// value is floored to the time-of-day, but the op is NEVER mutated.
	// "2024-11-30T23:59:00" (date != EPOCH → imprecise) with ">=" keeps ">=".
	got := ExpandTemporalOperand("2024-11-30T23:59:00", []DataType{LocalTime}, FilterGte)
	wantMs := utcMillis(1970, time.January, 1, 23, 59, 0)
	if len(got) != 1 || !condEqual(got[0], LocalTime, FilterGte, wantMs) {
		t.Fatalf("LDT->LT no mutation: got %+v want {LOCAL_TIME >= %d}", got, wantMs)
	}
	// EQUALS across an imprecise hop is still dropped (the drop is op-agnostic to
	// modifyOperationOnNonPrecise).
	got = ExpandTemporalOperand("2024-11-30T23:59:00", []DataType{LocalTime}, FilterEq)
	if len(got) != 0 {
		t.Fatalf("LDT->LT imprecise EQUALS should drop: got %+v", got)
	}
}

func TestTemporalExpand_UpscaleNoOpMutation(t *testing.T) {
	// ">=","2024",[ZONED_DATE_TIME] (meta ZDT) → upscale YEAR→...→ZDT at start-of-
	// year UTC, op UNCHANGED (upscale never mutates, never drops).
	got := ExpandTemporalOperand("2024", []DataType{ZonedDateTime}, FilterGte)
	wantMs := utcMillis(2024, time.January, 1, 0, 0, 0)
	if len(got) != 1 || !condEqual(got[0], ZonedDateTime, FilterGte, wantMs) {
		t.Fatalf("upscale no mutation: got %+v want {ZDT >= %d}", got, wantMs)
	}
	// Even EQUALS survives an upscale (never dropped).
	got = ExpandTemporalOperand("2024", []DataType{ZonedDateTime}, FilterEq)
	if len(got) != 1 || !condEqual(got[0], ZonedDateTime, FilterEq, wantMs) {
		t.Fatalf("upscale EQUALS kept: got %+v", got)
	}
}

func TestTemporalExpand_MultiDeclared(t *testing.T) {
	// ">=","2024-09-09",[YEAR,LOCAL_DATE] → identity {LOCAL_DATE >= 2024-09-09}
	// plus downscale {YEAR > 2024}. Order follows the declared slice.
	got := ExpandTemporalOperand("2024-09-09", []DataType{LocalDate, Year}, FilterGte)
	if len(got) != 2 {
		t.Fatalf("multi-declared: expected 2 branches, got %+v", got)
	}
	if !condEqual(got[0], LocalDate, FilterGte, utcMillis(2024, time.September, 9, 0, 0, 0)) {
		t.Errorf("identity LOCAL_DATE branch: got %+v", got[0])
	}
	if !condEqual(got[1], Year, FilterGt, utcMillis(2024, time.January, 1, 0, 0, 0)) {
		t.Errorf("downscale YEAR branch: got %+v", got[1])
	}
}

func TestTemporalExpand_ZDTDataVsMetaOffset(t *testing.T) {
	// DATA ZonedDateTime: a direct parse as ZDT of an offset-less string fails —
	// data ZDT operands require an offset.
	if _, ok := ParseTemporalSubtype("2024-09-09T10:30:00", ZonedDateTime); ok {
		t.Errorf("data ZDT direct parse should require an offset")
	}
	// META ZonedDateTime: a coarse operand is accepted via source-parse + upscale
	// to the instant (the meta relaxation of the offset-mandatory rule).
	got := ExpandTemporalOperand("2024", []DataType{ZonedDateTime}, FilterGte)
	if len(got) != 1 || got[0].Type != ZonedDateTime {
		t.Fatalf("meta ZDT coarse operand should upscale to an instant: got %+v", got)
	}
}

func TestTemporalExpand_Unparseable(t *testing.T) {
	if got := ExpandTemporalOperand("not-a-date", []DataType{Year, LocalDate}, FilterGte); len(got) != 0 {
		t.Fatalf("unparseable operand should yield no branches: got %+v", got)
	}
}

func TestTemporalExpand_NoPathSkipped(t *testing.T) {
	// LocalTime has no path to Year (disconnected in both graphs) → skipped.
	if got := ExpandTemporalOperand("10:30:00", []DataType{Year}, FilterGte); len(got) != 0 {
		t.Fatalf("LocalTime->Year has no path: got %+v", got)
	}
}
