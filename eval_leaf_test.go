package spi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// jsonStr builds a stored gjson.Result for a Go string value, JSON-encoding it
// so backslashes and other specials survive intact (a bare `"a\b"` literal would
// otherwise decode `\b` as a backspace).
func jsonStr(s string) gjson.Result {
	b, _ := json.Marshal(s)
	return gjson.ParseBytes(b)
}

// strp is a small helper: a non-nil *string means "stored value present with
// this raw JSON"; nil means "leaf absent from the document".
func strp(s string) *string { return &s }

// storedFrom builds the gjson.Result an evaluator receives. nil → absent
// (Exists()==false), otherwise the raw JSON parsed as a top-level value.
func storedFrom(raw *string) gjson.Result {
	if raw == nil {
		return gjson.Result{} // absent leaf
	}
	return gjson.Parse(*raw)
}

// evalRow is one oracle row driving EvalLeaf through the full expand+eval path.
type evalRow struct {
	name     string
	op       FilterOp
	operand  string
	values   []string
	declared []DataType
	stored   *string // nil = absent
	want     bool
}

// oracleRows encodes the entity-search.md worked examples (C.1, C.5, C.6, C.8)
// plus the Cloud operator rules the brief enumerates. Each row is evaluated
// through the explicit ExpandLeaf+EvalLeaf pipeline and must equal want.
func oracleRows() []evalRow {
	u := "6ba7b810-9dad-11d1-80b4-00c04fd430c8" // a v1 UUID
	return []evalRow{
		// --- C.1 polymorphic expansion --------------------------------------
		{"poly int|str eq 30 -> stored int", FilterEq, "30", nil, []DataType{Integer, String}, strp("30"), true},
		{"poly int|str eq 30 -> stored str", FilterEq, "30", nil, []DataType{Integer, String}, strp(`"30"`), true},
		{"poly int|str eq 30 -> stored int 31", FilterEq, "30", nil, []DataType{Integer, String}, strp("31"), false},
		{"poly int|str eq hello -> stored str (no error)", FilterEq, "hello", nil, []DataType{Integer, String}, strp(`"hello"`), true},
		{"poly int|str eq hello -> stored int (no match)", FilterEq, "hello", nil, []DataType{Integer, String}, strp("5"), false},
		{"poly dbl|int gte 12.78 -> stored int 13 (ceiling)", FilterGte, "12.78", nil, []DataType{Double, Integer}, strp("13"), true},
		{"poly dbl|int gte 12.78 -> stored int 12", FilterGte, "12.78", nil, []DataType{Double, Integer}, strp("12"), false},
		{"poly dbl|int gte 12.78 -> stored dbl 12.9", FilterGte, "12.78", nil, []DataType{Double, Integer}, strp("12.9"), true},

		// --- C.5 assignability + negative-on-absent -------------------------
		{"assign 5 -> [LONG] eq", FilterEq, "5", nil, []DataType{Long}, strp("5"), true},
		{"assign 6 -> [LONG] gt 5", FilterGt, "5", nil, []DataType{Long}, strp("6"), true},
		{"ne 30 on absent -> non-match", FilterNe, "30", nil, []DataType{Integer}, nil, false},
		{"ine on absent -> non-match", FilterINe, "hello", nil, []DataType{String}, nil, false},
		{"inot_contains on absent -> non-match", FilterINotContains, "x", nil, []DataType{String}, nil, false},
		{"ne 30 on present 40 -> match", FilterNe, "30", nil, []DataType{Integer}, strp("40"), true},
		{"ne 30 on present 30 -> non-match", FilterNe, "30", nil, []DataType{Integer}, strp("30"), false},
		{"ne 30 on null -> non-match", FilterNe, "30", nil, []DataType{Integer}, strp("null"), false},

		// --- C.6 string ops (textual-only) + LIKE ---------------------------
		{"contains 5 on numeric -> non-match", FilterContains, "5", nil, []DataType{Integer}, strp("55"), false},
		{"contains ell -> match", FilterContains, "ell", nil, []DataType{String}, strp(`"hello"`), true},
		{"contains xyz -> non-match", FilterContains, "xyz", nil, []DataType{String}, strp(`"hello"`), false},
		{"starts_with he -> match", FilterStartsWith, "he", nil, []DataType{String}, strp(`"hello"`), true},
		{"ends_with lo -> match", FilterEndsWith, "lo", nil, []DataType{String}, strp(`"hello"`), true},
		{"icontains ELL -> match", FilterIContains, "ELL", nil, []DataType{String}, strp(`"hello"`), true},
		{"ieq HELLO -> match", FilterIEq, "HELLO", nil, []DataType{String}, strp(`"hello"`), true},
		{"ine HELLO -> non-match", FilterINe, "HELLO", nil, []DataType{String}, strp(`"hello"`), false},
		{"inot_contains ELL on hello -> non-match", FilterINotContains, "ELL", nil, []DataType{String}, strp(`"hello"`), false},
		{"inot_contains xyz on hello -> match", FilterINotContains, "xyz", nil, []DataType{String}, strp(`"hello"`), true},
		{"contains on non-textual bool -> non-match", FilterContains, "true", nil, []DataType{Boolean}, strp("true"), false},

		// --- case-sensitive negatives: NOT_CONTAINS / NOT_STARTS_WITH /
		// NOT_ENDS_WITH (null-uniform: absent/null -> non-match, not !positive) --
		{"not_contains on absent -> non-match", FilterNotContains, "x", nil, []DataType{String}, nil, false},
		{"not_contains on null -> non-match", FilterNotContains, "x", nil, []DataType{String}, strp("null"), false},
		{"not_contains ell on hello -> non-match (contains)", FilterNotContains, "ell", nil, []DataType{String}, strp(`"hello"`), false},
		{"not_contains xyz on hello -> match (does not contain)", FilterNotContains, "xyz", nil, []DataType{String}, strp(`"hello"`), true},
		{"not_contains ABC case-sensitive on abc -> match (case differs)", FilterNotContains, "ABC", nil, []DataType{String}, strp(`"abc"`), true},
		// A string operator has no candidate against a non-textual stored slot:
		// it never stringifies the stored value (documented divergence, see
		// eval_leaf.go's Cloud-divergence list), so there is nothing to test
		// it against. NOT_CONTAINS is a negative operator, so the
		// unsatisfiable comparison follows polarity and answers true. This
		// is NOT the numeric-precision rationale ($.n NE 12.5 on an INTEGER,
		// where PostgreSQL's `<>` agrees) — PostgreSQL's LIKE-family
		// coerces and disagrees here (`55 NOT LIKE '%5%'` is false), because
		// SQL stringifies the numeric operand where cyoda-go deliberately
		// does not.
		{"not_contains on non-textual numeric -> match (unsatisfiable, follows polarity)", FilterNotContains, "5", nil, []DataType{Integer}, strp("55"), true},

		{"not_starts_with on absent -> non-match", FilterNotStartsWith, "x", nil, []DataType{String}, nil, false},
		{"not_starts_with on null -> non-match", FilterNotStartsWith, "x", nil, []DataType{String}, strp("null"), false},
		{"not_starts_with he on hello -> non-match (starts with)", FilterNotStartsWith, "he", nil, []DataType{String}, strp(`"hello"`), false},
		{"not_starts_with xy on hello -> match (does not start with)", FilterNotStartsWith, "xy", nil, []DataType{String}, strp(`"hello"`), true},
		{"not_starts_with HE case-sensitive on hello -> match (case differs)", FilterNotStartsWith, "HE", nil, []DataType{String}, strp(`"hello"`), true},
		// Same unsatisfiable-comparison rule as NOT_CONTAINS above: a string
		// operator never stringifies a non-textual stored value, so there is
		// no candidate to test and the negative operator follows polarity.
		// (Not the numeric-precision/PostgreSQL rationale — see the comment
		// on the NOT_CONTAINS row above.)
		{"not_starts_with on non-textual numeric -> match (unsatisfiable, follows polarity)", FilterNotStartsWith, "5", nil, []DataType{Integer}, strp("55"), true},

		{"not_ends_with on absent -> non-match", FilterNotEndsWith, "x", nil, []DataType{String}, nil, false},
		{"not_ends_with on null -> non-match", FilterNotEndsWith, "x", nil, []DataType{String}, strp("null"), false},
		{"not_ends_with lo on hello -> non-match (ends with)", FilterNotEndsWith, "lo", nil, []DataType{String}, strp(`"hello"`), false},
		{"not_ends_with xy on hello -> match (does not end with)", FilterNotEndsWith, "xy", nil, []DataType{String}, strp(`"hello"`), true},
		{"not_ends_with LO case-sensitive on hello -> match (case differs)", FilterNotEndsWith, "LO", nil, []DataType{String}, strp(`"hello"`), true},
		// Same unsatisfiable-comparison rule as NOT_CONTAINS above: a string
		// operator never stringifies a non-textual stored value, so there is
		// no candidate to test and the negative operator follows polarity.
		// (Not the numeric-precision/PostgreSQL rationale — see the comment
		// on the NOT_CONTAINS row above.)
		{"not_ends_with on non-textual numeric -> match (unsatisfiable, follows polarity)", FilterNotEndsWith, "5", nil, []DataType{Integer}, strp("55"), true},

		{"like foo% -> foobar", FilterLike, "foo%", nil, []DataType{String}, strp(`"foobar"`), true},
		{"like foo% anchored -> xfoobar", FilterLike, "foo%", nil, []DataType{String}, strp(`"xfoobar"`), false},
		{"like a_c -> abc", FilterLike, "a_c", nil, []DataType{String}, strp(`"abc"`), true},
		{"like a_c -> ac (underscore is exactly one)", FilterLike, "a_c", nil, []DataType{String}, strp(`"ac"`), false},
		{"like a_c -> abbc", FilterLike, "a_c", nil, []DataType{String}, strp(`"abbc"`), false},
		{"like 50\\% -> 50% literal", FilterLike, `50\%`, nil, []DataType{String}, strp(`"50%"`), true},
		{"like 50\\% -> 50off", FilterLike, `50\%`, nil, []DataType{String}, strp(`"50off"`), false},
		{"like 1.2 dot literal -> 1.2", FilterLike, "1.2", nil, []DataType{String}, strp(`"1.2"`), true},
		{"like 1.2 dot literal -> 1x2", FilterLike, "1.2", nil, []DataType{String}, strp(`"1x2"`), false},
		{"matches [0-9]+ -> 123", FilterMatchesRegex, "[0-9]+", nil, []DataType{String}, strp(`"123"`), true},
		{"matches [0-9]+ anchored -> 12a", FilterMatchesRegex, "[0-9]+", nil, []DataType{String}, strp(`"12a"`), false},

		// --- C.8 BETWEEN (exclusive), null unary, uuid, bool ----------------
		{"between 10..20 -> 15", FilterBetween, "", []string{"10", "20"}, []DataType{Integer}, strp("15"), true},
		{"between 10..20 exclusive lo -> 10", FilterBetween, "", []string{"10", "20"}, []DataType{Integer}, strp("10"), false},
		{"between 10..20 exclusive hi -> 20", FilterBetween, "", []string{"10", "20"}, []DataType{Integer}, strp("20"), false},
		{"between 10..20 -> 25", FilterBetween, "", []string{"10", "20"}, []DataType{Integer}, strp("25"), false},

		// --- BETWEEN_INCLUSIVE (same range, inclusive bounds) ---------------
		{"between_inclusive 10..20 -> inclusive lo 10", FilterBetweenInclusive, "", []string{"10", "20"}, []DataType{Integer}, strp("10"), true},
		{"between_inclusive 10..20 -> 15", FilterBetweenInclusive, "", []string{"10", "20"}, []DataType{Integer}, strp("15"), true},
		{"between_inclusive 10..20 -> inclusive hi 20", FilterBetweenInclusive, "", []string{"10", "20"}, []DataType{Integer}, strp("20"), true},
		{"between_inclusive 10..20 -> below range 9", FilterBetweenInclusive, "", []string{"10", "20"}, []DataType{Integer}, strp("9"), false},
		{"between_inclusive 10..20 -> above range 21", FilterBetweenInclusive, "", []string{"10", "20"}, []DataType{Integer}, strp("21"), false},

		// --- string BETWEEN / BETWEEN_INCLUSIVE (lexicographic bounds) ------
		{"str between abc..abe -> exclusive lo abc", FilterBetween, "", []string{"abc", "abe"}, []DataType{String}, strp(`"abc"`), false},
		{"str between abc..abe -> inside abd", FilterBetween, "", []string{"abc", "abe"}, []DataType{String}, strp(`"abd"`), true},
		{"str between_inclusive abc..abe -> inclusive lo abc", FilterBetweenInclusive, "", []string{"abc", "abe"}, []DataType{String}, strp(`"abc"`), true},
		{"str between_inclusive abc..abe -> inclusive hi abe", FilterBetweenInclusive, "", []string{"abc", "abe"}, []DataType{String}, strp(`"abe"`), true},

		{"is_null on absent -> true", FilterIsNull, "", nil, []DataType{Integer}, nil, true},
		{"is_null on null -> true", FilterIsNull, "", nil, []DataType{Integer}, strp("null"), true},
		{"is_null on present -> false", FilterIsNull, "", nil, []DataType{Integer}, strp("5"), false},
		{"not_null on present -> true", FilterNotNull, "", nil, []DataType{Integer}, strp("5"), true},
		{"not_null on null -> false", FilterNotNull, "", nil, []DataType{Integer}, strp("null"), false},
		{"not_null on absent -> false", FilterNotNull, "", nil, []DataType{Integer}, nil, false},

		{"uuid eq -> same", FilterEq, u, nil, []DataType{UUIDType}, strp(`"` + u + `"`), true},
		{"uuid eq -> other", FilterEq, u, nil, []DataType{UUIDType}, strp(`"6ba7b811-9dad-11d1-80b4-00c04fd430c8"`), false},
		{"bool eq true -> true", FilterEq, "true", nil, []DataType{Boolean}, strp("true"), true},
		{"bool eq true -> false", FilterEq, "true", nil, []DataType{Boolean}, strp("false"), false},

		// --- precision beyond 2^53 ------------------------------------------
		{"precise long eq beyond 2^53 -> match", FilterEq, "9007199254740993", nil, []DataType{Long}, strp("9007199254740993"), true},
		{"precise long eq beyond 2^53 -> off-by-one", FilterEq, "9007199254740992", nil, []DataType{Long}, strp("9007199254740993"), false},

		// --- 20-digit precision, both ends (operand string AND stored raw
		// JSON text survive to the compare without float64 truncation) -------
		{"20-digit precise eq -> match", FilterEq, "12345678901234567890", nil, []DataType{BigInteger}, strp("12345678901234567890"), true},
		{"20-digit precise eq -> off-by-one at the 20th digit -> non-match", FilterEq, "12345678901234567890", nil, []DataType{BigInteger}, strp("12345678901234567891"), false},

		// --- a type accepts the operand but every bucket drops it (no
		// candidate sub-condition survives): EQ is a positive operator, so a
		// no-candidate comparison is a non-match. (NE over the identical
		// no-candidate expansion is a match — see
		// TestEvalLeaf_UnsatisfiableComparisonFollowsPolarity.) -------------
		{"no candidate: [INT] eq 12.5 stored 12 -> non-match", FilterEq, "12.5", nil, []DataType{Integer}, strp("12"), false},
		{"no candidate: [INT] eq 12.5 stored 13 -> non-match", FilterEq, "12.5", nil, []DataType{Integer}, strp("13"), false},

		// --- out-of-range numeric bucket → NOT_NULL degenerate (entity-search.md
		// §6 Step 2 / worked example `[BYTE], LESS_THAN "300" -> NotNull`; cyoda-go
		// has no BYTE bucket (design §4), so INTEGER — the narrowest int bucket —
		// carries the same ABOVE-ceiling/BELOW-floor semantics: the bucket
		// degenerates to "any stored value of that type", matching regardless of
		// magnitude or sign). ---------------------------------------------------
		{"int lt above-ceiling -> NOT_NULL (matches any stored int, positive)", FilterLt, "5000000000", nil, []DataType{Integer}, strp("999999"), true},
		{"int lt above-ceiling -> NOT_NULL (matches any stored int, negative)", FilterLt, "5000000000", nil, []DataType{Integer}, strp("-999999"), true},
		{"int gt below-floor -> NOT_NULL (matches any stored int)", FilterGt, "-5000000000", nil, []DataType{Integer}, strp("0"), true},

		// --- string ordering (monomorphic String, comparables) -------------
		{"str gt -> lexicographic", FilterGt, "abc", nil, []DataType{String}, strp(`"abd"`), true},
		{"str lt -> lexicographic", FilterLt, "abc", nil, []DataType{String}, strp(`"abb"`), true},

		// --- temporal (ZonedDateTime instants) ------------------------------
		{"zdt gte -> later instant", FilterGte, "2024-09-09T00:00:00Z", nil, []DataType{ZonedDateTime}, strp(`"2025-01-01T00:00:00Z"`), true},
		{"zdt gte -> earlier instant", FilterGte, "2024-09-09T00:00:00Z", nil, []DataType{ZonedDateTime}, strp(`"2024-01-01T00:00:00Z"`), false},
		{"zdt eq -> same instant", FilterEq, "2024-09-09T12:00:00Z", nil, []DataType{ZonedDateTime}, strp(`"2024-09-09T12:00:00Z"`), true},
		{"zdt ne -> absent (null rule)", FilterNe, "2024-09-09T12:00:00Z", nil, []DataType{ZonedDateTime}, nil, false},

		// --- temporal coarse subtypes (stored-side type-slot discipline) ----
		// A LocalDate stored value is offset-less/date-only: ParseTemporalMillis
		// cannot read it, so it must be classified to its natural subtype and
		// matched only against the LOCAL_DATE sub-condition.
		{"local_date gte -> same date", FilterGte, "2024-09-09", nil, []DataType{LocalDate}, strp(`"2024-09-09"`), true},
		{"local_date gte -> earlier date", FilterGte, "2024-09-09", nil, []DataType{LocalDate}, strp(`"2024-09-08"`), false},
		{"local_date gte -> later date", FilterGte, "2024-09-09", nil, []DataType{LocalDate}, strp(`"2024-09-10"`), true},
		{"year gte 2024 -> stored 2024", FilterGte, "2024", nil, []DataType{Year}, strp(`"2024"`), true},
		{"year gte 2024 -> stored 2023", FilterGte, "2024", nil, []DataType{Year}, strp(`"2023"`), false},
		{"year gte 2024 -> stored 2025", FilterGte, "2024", nil, []DataType{Year}, strp(`"2025"`), true},
		// Op-mutation: LocalDate operand downscaled to YEAR turns gte into gt
		// (floored to 2024-01-01), so stored 2024 is NOT >= and must not match.
		{"year gte 2024-09-09 (mutated gt) -> stored 2024", FilterGte, "2024-09-09", nil, []DataType{Year}, strp(`"2024"`), false},
		{"year gte 2024-09-09 (mutated gt) -> stored 2025", FilterGte, "2024-09-09", nil, []DataType{Year}, strp(`"2025"`), true},

		// --- temporal imprecise EQUALS dropped (entity-search.md section 6
		// Step 3: "EQUALS on an imprecise value -> dropped", the temporal
		// analogue of the numeric imprecise-EQUALS void rule above). A YEAR
		// bucket cannot precisely hold a day-of-year, so EQUALS against it is
		// void regardless of the stored value. -------------------------------
		{"year eq 2024-09-09 imprecise -> dropped (void, non-match)", FilterEq, "2024-09-09", nil, []DataType{Year}, strp(`"2024"`), false},

		// --- entity-search.md section 6 worked example (literal): [YEAR,
		// LOCAL_DATE], GREATER_OR_EQUAL "2024-09-09" ->
		// OR(localDates>=2024-09-09, years>2024). Combines the identity
		// LOCAL_DATE branch with the imprecise-downscaled, op-mutated YEAR
		// branch in a single polymorphic declared set. -----------------------
		{"poly year|local_date gte 2024-09-09 -> stored local_date same day", FilterGte, "2024-09-09", nil, []DataType{Year, LocalDate}, strp(`"2024-09-09"`), true},
		{"poly year|local_date gte 2024-09-09 -> stored local_date earlier", FilterGte, "2024-09-09", nil, []DataType{Year, LocalDate}, strp(`"2024-09-08"`), false},
		{"poly year|local_date gte 2024-09-09 -> stored year 2024 (not > floor)", FilterGte, "2024-09-09", nil, []DataType{Year, LocalDate}, strp(`"2024"`), false},
		{"poly year|local_date gte 2024-09-09 -> stored year 2025", FilterGte, "2024-09-09", nil, []DataType{Year, LocalDate}, strp(`"2025"`), true},

		// --- meta temporal coarse operand (design spec section 4 / coverage
		// matrix: meta fields are monomorphic ZONED_DATE_TIME but accept a
		// coarse operand like "2024", classified as YEAR then upscaled to the
		// instant at start-of-year UTC; op is never mutated on upscale). ------
		{"meta zdt gte coarse year 2024 -> stored later in year", FilterGte, "2024", nil, []DataType{ZonedDateTime}, strp(`"2024-06-01T00:00:00Z"`), true},
		{"meta zdt gte coarse year 2024 -> stored prior year", FilterGte, "2024", nil, []DataType{ZonedDateTime}, strp(`"2023-12-31T23:59:59Z"`), false},

		// --- polymorphic false-positive guard (type-slot exact match) -------
		// [YEAR, ZONED_DATE_TIME] gte a ZDT operand: the YEAR branch floors to
		// {YEAR gt 2024-01-01}. A stored ZonedDateTime must compare ONLY against
		// the ZDT sub-condition, never spuriously satisfy the YEAR branch.
		{"poly year|zdt gte -> stored zdt earlier (no false positive)", FilterGte, "2024-09-09T00:00:00Z", nil, []DataType{Year, ZonedDateTime}, strp(`"2024-06-01T00:00:00Z"`), false},
		{"poly year|zdt gte -> stored zdt later", FilterGte, "2024-09-09T00:00:00Z", nil, []DataType{Year, ZonedDateTime}, strp(`"2024-12-01T00:00:00Z"`), true},

		// --- temporal BETWEEN on a coarse (non-ZonedDateTime) subtype -------
		{"local_date between -> inside", FilterBetween, "", []string{"2024-03-01", "2024-09-01"}, []DataType{LocalDate}, strp(`"2024-06-15"`), true},
		{"local_date between exclusive lo", FilterBetween, "", []string{"2024-03-01", "2024-09-01"}, []DataType{LocalDate}, strp(`"2024-03-01"`), false},
		{"local_date between -> below range", FilterBetween, "", []string{"2024-03-01", "2024-09-01"}, []DataType{LocalDate}, strp(`"2023-12-31"`), false},

		// --- temporal BETWEEN_INCLUSIVE: same bounds, boundary values now match
		{"local_date between_inclusive -> inclusive lo", FilterBetweenInclusive, "", []string{"2024-03-01", "2024-09-01"}, []DataType{LocalDate}, strp(`"2024-03-01"`), true},
		{"local_date between_inclusive -> inclusive hi", FilterBetweenInclusive, "", []string{"2024-03-01", "2024-09-01"}, []DataType{LocalDate}, strp(`"2024-09-01"`), true},
		{"local_date between_inclusive -> inside", FilterBetweenInclusive, "", []string{"2024-03-01", "2024-09-01"}, []DataType{LocalDate}, strp(`"2024-06-15"`), true},
		{"local_date between_inclusive -> below range", FilterBetweenInclusive, "", []string{"2024-03-01", "2024-09-01"}, []DataType{LocalDate}, strp(`"2023-12-31"`), false},

		// --- UnboundDecimal oracle rows (verbatim bounds, no rounding) ------
		{"ubd eq -> match", FilterEq, "3.14", nil, []DataType{UnboundDecimal}, strp("3.14"), true},
		{"ubd eq -> non-match", FilterEq, "3.14", nil, []DataType{UnboundDecimal}, strp("3.15"), false},
		{"ubd gt -> match", FilterGt, "3.14", nil, []DataType{UnboundDecimal}, strp("3.15"), true},
		{"ubd lte -> match", FilterLte, "100", nil, []DataType{UnboundDecimal}, strp("99.5"), true},
		{"ubd gte precise beyond 2^53", FilterGte, "9007199254740993", nil, []DataType{UnboundDecimal}, strp("9007199254740993"), true},
	}
}

func TestEvalLeaf_Oracle(t *testing.T) {
	for _, r := range oracleRows() {
		r := r
		t.Run(r.name, func(t *testing.T) {
			stored := storedFrom(r.stored)

			exp, err := ExpandLeaf(r.op, r.operand, r.values, r.declared)
			if err != nil {
				t.Fatalf("ExpandLeaf unexpected error: %v", err)
			}
			got := EvalLeaf(exp, stored)
			if got != r.want {
				t.Errorf("EvalLeaf(expand) = %v, want %v", got, r.want)
			}
		})
	}
}

func TestExpandLeaf_TypeMismatchError(t *testing.T) {
	// Operand parses into no declared type → error (caller maps CONDITION_TYPE_MISMATCH).
	if _, err := ExpandLeaf(FilterEq, "abc", nil, []DataType{Integer}); err == nil {
		t.Fatalf("expected error for [INTEGER] eq \"abc\"")
	}
	// LOCAL_DATE with a non-temporal operand.
	if _, err := ExpandLeaf(FilterEq, "12.5", nil, []DataType{LocalDate}); err == nil {
		t.Fatalf("expected error for [LOCAL_DATE] eq \"12.5\"")
	}
	// STRING among the types → never an error (STRING parses anything).
	if _, err := ExpandLeaf(FilterEq, "abc", nil, []DataType{Integer, String}); err != nil {
		t.Fatalf("unexpected error when STRING present: %v", err)
	}
}

// TestExpandLeaf_TypeMismatchError_BoundsOperandLength pins the security-review
// fix: a search request's operand can be caller-sized (request bodies are
// capped at 10 MiB), and "operand parses into no declared type" is the
// documented-normal INVALID_CONDITION case a bare-typed field always hits —
// not a rare fault worth paying to echo in full. Without truncateOperand the
// error message grows linearly with the operand, and prepared_filter.go wraps
// this same string a second time on top, doubling it again.
func TestExpandLeaf_TypeMismatchError_BoundsOperandLength(t *testing.T) {
	huge := strings.Repeat("a", 1<<20) // 1 MiB, mirrors the 10 MiB request-body cap's order of magnitude
	_, err := ExpandLeaf(FilterEq, huge, nil, []DataType{Integer})
	if err == nil {
		t.Fatalf("expected error for [INTEGER] eq huge non-numeric operand")
	}
	msg := err.Error()
	if len(msg) > maxEchoedOperandBytes+100 {
		t.Fatalf("error message not bounded: got %d bytes, want <= ~%d", len(msg), maxEchoedOperandBytes+100)
	}
	if !strings.Contains(msg, "...(truncated)") {
		t.Fatalf("expected truncation marker in error message, got %q", msg)
	}
	if strings.Contains(msg, huge) {
		t.Fatalf("error message must not contain the full operand")
	}
}

func TestExpandLeaf_Void(t *testing.T) {
	// [INTEGER] eq "12.5": operand IS numeric but every int bucket drops it.
	// Expansion no longer carries a distinct void flag — every bucket is
	// simply empty, the same shape EvalLeaf treats as "no candidate" for the
	// stored value's own type family (see Expansion's doc comment).
	exp, err := ExpandLeaf(FilterEq, "12.5", nil, []DataType{Integer})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(exp.numeric) != 0 || len(exp.temporal) != 0 || len(exp.others) != 0 {
		t.Fatalf("expected every bucket empty for [INTEGER] eq 12.5, got numeric=%v temporal=%v others=%v",
			exp.numeric, exp.temporal, exp.others)
	}
	// A positive operator (EQ) with no candidate is a non-match.
	if EvalLeaf(exp, gjson.Parse("12")) {
		t.Errorf("EQ with no candidate must not match")
	}
	// A negative operator (NE) over the identical no-candidate expansion
	// follows polarity and matches — the unsatisfiable-comparison rule this
	// package's TestEvalLeaf_UnsatisfiableComparisonFollowsPolarity pins.
	expNe, err := ExpandLeaf(FilterNe, "12.5", nil, []DataType{Integer})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !EvalLeaf(expNe, gjson.Parse("12")) {
		t.Errorf("NE with no candidate must match (unsatisfiable comparison follows polarity)")
	}
}

func TestExpandLeaf_BetweenArity(t *testing.T) {
	for _, op := range []FilterOp{FilterBetween, FilterBetweenInclusive} {
		for _, vals := range [][]string{nil, {"1"}, {"1", "2", "3"}} {
			if _, err := ExpandLeaf(op, "", vals, []DataType{Integer}); err == nil {
				t.Errorf("expected arity error for %s with %d values", op, len(vals))
			}
		}
	}
}

func TestExpandLeaf_TemporalDownscaleOpMutation(t *testing.T) {
	// [YEAR] gte "2024-09-09": LocalDate seed downscaled to YEAR, imprecise →
	// gte becomes gt, floored to 2024-01-01.
	exp, err := ExpandLeaf(FilterGte, "2024-09-09", nil, []DataType{Year})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(exp.temporal) != 1 {
		t.Fatalf("want 1 temporal sub-condition, got %d", len(exp.temporal))
	}
	sc := exp.temporal[0]
	wantMs := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	if sc.Type != Year || sc.Op != FilterGt || sc.Millis != wantMs {
		t.Errorf("got {Type:%v Op:%v Millis:%d}, want {YEAR gt %d}", sc.Type, sc.Op, sc.Millis, wantMs)
	}
}

func TestLike_Grammar(t *testing.T) {
	cases := []struct {
		pattern, in string
		want        bool
	}{
		{"foo%", "foobar", true},
		{"foo%", "foo", true},
		{"foo%", "xfoo", false},
		{"a_c", "abc", true},
		{"a_c", "ac", false},
		{`50\%`, "50%", true},
		{`50\%`, "5000", false},
		{`a\\b`, `a\b`, true},
		{"1.2", "1.2", true},
		{"1.2", "1x2", false},
		{"[a]", "[a]", true}, // regex metachars are literals — LIKE is a glob
		{"a<b>c", "a<b>c", true},
	}
	for _, c := range cases {
		exp, err := ExpandLeaf(FilterLike, c.pattern, nil, []DataType{String})
		if err != nil {
			t.Fatalf("ExpandLeaf(like %q) error: %v", c.pattern, err)
		}
		got := EvalLeaf(exp, jsonStr(c.in))
		if got != c.want {
			t.Errorf("LIKE %q vs %q = %v, want %v", c.pattern, c.in, got, c.want)
		}
	}
}

// TestLike_SQLParity pins the twelve probe rows from the spec's Why table
// against the SQL column. PostgreSQL 17 and SQLite agree on every one, and so
// does today's kernel.
func TestLike_SQLParity(t *testing.T) {
	cases := []struct {
		pattern, in string
		want        bool // what PostgreSQL and SQLite return
	}{
		{`\d`, "7", false},
		{`\d`, "d", true},
		{`\d`, `\d`, false},
		{`\w`, "q", false},
		{`a\nb`, "a\nb", false}, // \n is a literal 'n', not a newline
		{`a\nb`, "anb", true},
		{"a_b", "a\nb", true}, // _ matches a newline
		{"%", "a\nb", true},   // % matches a newline
		{`\Q`, "Q", true},
		{`\Q`, `\Q`, false},
		{`\p{Foo}`, "p{Foo}", true},
		{`\p{Foo}`, "x", false},
	}
	for _, c := range cases {
		exp, err := ExpandLeaf(FilterLike, c.pattern, nil, []DataType{String})
		if err != nil {
			t.Fatalf("ExpandLeaf(like %q) error: %v", c.pattern, err)
		}
		if got := EvalLeaf(exp, jsonStr(c.in)); got != c.want {
			t.Errorf("LIKE %q vs %q = %v, want %v (SQL)", c.pattern, c.in, got, c.want)
		}
	}
}

// TestLike_MalformedOperandNeverMatches pins ExpandLeaf's own contract,
// unchanged since Task 2: a pattern operand that will not compile still
// yields a leaf whose Expansion never matches (nil strMatch) rather than an
// error, because ExpandLeaf has callers other than prepared_filter.go's
// Prepare that still want that leaf built. Prepare detects this swallow
// AFTER the fact (exp.strMatch == nil) and rejects the leaf itself
// (ErrUnevaluableLeaf) — see Prepare's doc comment — so this test's
// never-match outcome is not the answer a caller going through Prepare ever
// observes; it is the building block Prepare's post-hoc check relies on. The
// 400 a client sees for a malformed pattern happens at the request boundary,
// via ValidateConditionPatterns — not here.
func TestLike_MalformedOperandNeverMatches(t *testing.T) {
	exp, err := ExpandLeaf(FilterLike, `a\`, nil, []DataType{String})
	if err != nil {
		t.Fatalf("ExpandLeaf should not surface the error, got %v", err)
	}
	for _, in := range []string{"a", `a\`, "ab", ""} {
		if EvalLeaf(exp, jsonStr(in)) {
			t.Errorf("malformed LIKE operand matched %q", in)
		}
	}
}

// TestValidatorAgreesWithKernel is the anti-drift guard: for every corpus
// operand, compileLeafPattern erroring must be exactly when the kernel ends up
// with no matcher.
func TestValidatorAgreesWithKernel(t *testing.T) {
	corpus := []struct {
		op      FilterOp
		operand string
	}{
		{FilterLike, `%`}, {FilterLike, `a\`}, {FilterLike, `\`}, {FilterLike, `\d`},
		{FilterLike, `a\\b`}, {FilterLike, ``},
		{FilterMatchesRegex, `A.*e`}, {FilterMatchesRegex, `\Q`}, {FilterMatchesRegex, `)|(`},
		{FilterMatchesRegex, `[`}, {FilterMatchesRegex, `a|b`}, {FilterMatchesRegex, ``},
	}
	for _, c := range corpus {
		_, valErr := compileLeafPattern(c.op, c.operand)
		exp, err := ExpandLeaf(c.op, c.operand, nil, []DataType{String})
		if err != nil {
			t.Fatalf("ExpandLeaf(%s, %q) error: %v", c.op, c.operand, err)
		}
		if (valErr != nil) != (exp.strMatch == nil) {
			t.Errorf("skew for (%s, %q): validator err=%v, kernel matcher nil=%v",
				c.op, c.operand, valErr, exp.strMatch == nil)
		}
	}
}

func TestCompileMatchesPattern_AnchorEscapeRejected(t *testing.T) {
	// anchor() is string concatenation, so a body with a net-unmatched ')'
	// escapes the group: ")|(" becomes \A(?:)|()\z, an alternation whose
	// first branch matches the empty string at position 0 — it matches EVERY
	// stored value. These must be rejected, not accepted.
	for _, operand := range []string{`)|(`, `)\z|(?:`, `)$|(`, `)x(`} {
		if _, err := compileLeafPattern(FilterMatchesRegex, operand); err == nil {
			t.Errorf("compileLeafPattern(MATCHES_PATTERN, %q) = nil error, want rejection", operand)
		}
	}
}

func TestCompileMatchesPattern_AcceptSet(t *testing.T) {
	// Rejected: compiles bare, fails anchored (\Q swallows the wrapper's )\z).
	err := func() error { _, err := compileLeafPattern(FilterMatchesRegex, `\Q`); return err }()
	if err == nil {
		t.Fatal(`compileLeafPattern(MATCHES_PATTERN, "\\Q") = nil error, want rejection`)
	}
	// The bare parse succeeds for "\Q", so any syntax.Error.Code from the
	// ANCHORED compile can only describe anchor's own \A(?:...)\z wrapper —
	// never the operand. The message must not report on the wrapper's
	// parentheses (e.g. a "missing closing )" about anchor's "(?:").
	if msg := err.Error(); strings.ContainsAny(msg, "()") {
		t.Errorf(`error for "\Q" mentions a paren, which can only describe anchor's wrapper: %q`, msg)
	}
	// Accepted: legitimate patterns are unaffected by the bare requirement.
	for _, operand := range []string{`a|b`, `^foo`, `A.*e`, ``} {
		if _, err := compileLeafPattern(FilterMatchesRegex, operand); err != nil {
			t.Errorf("compileLeafPattern(MATCHES_PATTERN, %q) = %v, want accepted", operand, err)
		}
	}
}

func TestCompileMatchesPattern_ErrorIsHonestAndCarriesNoInternals(t *testing.T) {
	_, err := compileLeafPattern(FilterMatchesRegex, `[`)
	if err == nil {
		t.Fatal(`compileLeafPattern(MATCHES_PATTERN, "[") = nil error, want rejection`)
	}
	if !errors.Is(err, ErrInvalidPattern) {
		t.Errorf("error %v does not wrap ErrInvalidPattern", err)
	}
	msg := err.Error()
	// The BARE diagnostic. Anchored, RE2 reports "invalid escape sequence"
	// about a \z the user never wrote.
	if !strings.Contains(msg, "missing closing ]") {
		t.Errorf("want the bare code %q, got %q", "missing closing ]", msg)
	}
	if strings.Contains(msg, `\A(?:`) {
		t.Errorf("error leaks the anchored form: %q", msg)
	}
	if strings.Contains(msg, `[`) {
		t.Errorf("error echoes the operand: %q", msg)
	}
}

func TestCompileLeafPattern_TypedNilHazard(t *testing.T) {
	// A non-pattern operator must yield an UNTYPED nil. A typed nil through
	// the interface is non-nil, and evalStringOp would call a method on it.
	for _, op := range []FilterOp{FilterEq, FilterContains, FilterIsNull, ""} {
		m, err := compileLeafPattern(op, "anything")
		if err != nil {
			t.Errorf("compileLeafPattern(%q) = %v, want nil error", op, err)
		}
		if m != nil {
			t.Errorf("compileLeafPattern(%q) returned a non-nil matcher %#v", op, m)
		}
	}
	// The error paths must do the same.
	if m, _ := compileLeafPattern(FilterLike, `a\`); m != nil {
		t.Errorf("rejected LIKE returned a non-nil matcher %#v", m)
	}
	if m, _ := compileLeafPattern(FilterMatchesRegex, `[`); m != nil {
		t.Errorf("rejected MATCHES_PATTERN returned a non-nil matcher %#v", m)
	}
}

func TestCompileLeafPattern_DerivesOperandLikeTheKernel(t *testing.T) {
	// Takes `any` and applies OperandString itself, so a caller cannot supply
	// a differently-derived operand. A nil operand is "" here, never "<nil>".
	m, err := compileLeafPattern(FilterLike, nil)
	if err != nil {
		t.Fatalf("compileLeafPattern(LIKE, nil) = %v", err)
	}
	if !m.matches("") {
		t.Error("nil operand should derive the empty pattern, which matches only \"\"")
	}
	if m.matches("<nil>") {
		t.Error(`nil operand derived "<nil>" instead of ""`)
	}
}

func TestEvalLeaf_UnsatisfiableComparisonFollowsPolarity(t *testing.T) {
	cases := []struct {
		name     string
		op       FilterOp
		operand  string
		declared []DataType
		stored   string
		want     bool
	}{
		// Whole-expansion void: no declared type accepts the operand's value space.
		{"eq int vs fractional", FilterEq, "12.5", []DataType{Integer}, `5`, false},
		{"ne int vs fractional", FilterNe, "12.5", []DataType{Integer}, `5`, true},
		// Polymorphic: String accepts "12.5", so the expansion is NOT void,
		// but the numeric family still has no surviving sub-condition.
		{"eq int|string vs fractional", FilterEq, "12.5", []DataType{Integer, String}, `5`, false},
		{"ne int|string vs fractional", FilterNe, "12.5", []DataType{Integer, String}, `5`, true},
		// Null and absent keep operator-semantics.md section 2: never match, negatives included.
		{"ne over null", FilterNe, "12.5", []DataType{Integer}, `null`, false},
		// A satisfiable comparison is untouched.
		{"ne int vs integer operand", FilterNe, "13", []DataType{Integer}, `5`, true},
		{"eq int vs integer operand", FilterEq, "5", []DataType{Integer}, `5`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			exp, err := ExpandLeaf(c.op, c.operand, nil, c.declared)
			require.NoError(t, err)
			got := EvalLeaf(exp, gjson.Parse(c.stored))
			require.Equal(t, c.want, got)
		})
	}
}

// TestEvalLeaf_UnreadableStoredValueFailsClosed pins
// correctness-over-availability.md against Important-finding #1 from the
// task-1 review: a stored value the engine CANNOT read (as opposed to one
// whose type family has no sub-condition to try) must never be treated as
// unsatisfiable-hence-polarity-flippable. It is a fail-closed non-match for
// every operator, positive and negative — a value the engine cannot read
// must never ride a negative operator into the result set as a substituted
// "true".
//
// 0.1e-99999999999 is syntactically a valid JSON number (gjson types it
// gjson.Number without complaint), but its scale overflows what Decimal can
// represent, so ParseDecimal(stored.Raw) fails inside evalCompare's Number
// arm. That is a read failure, not "no candidate for the Number family".
func TestEvalLeaf_UnreadableStoredValueFailsClosed(t *testing.T) {
	const unreadable = `0.1e-99999999999`

	// Sanity: confirm the premise still holds — this value really is
	// unparseable as a Decimal, and gjson really does type it as a Number.
	if _, err := ParseDecimal(unreadable); err == nil {
		t.Fatalf("test premise broken: ParseDecimal(%q) now succeeds", unreadable)
	}
	if typ := gjson.Parse(unreadable).Type; typ != gjson.Number {
		t.Fatalf("test premise broken: gjson types %q as %v, want Number", unreadable, typ)
	}

	cases := []struct {
		name string
		op   FilterOp
	}{
		{"eq: positive operator, unreadable stored value -> non-match", FilterEq},
		{"ne: negative operator, unreadable stored value -> non-match (NOT true)", FilterNe},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			exp, err := ExpandLeaf(c.op, "5", nil, []DataType{Integer})
			require.NoError(t, err)
			got := EvalLeaf(exp, gjson.Parse(unreadable))
			require.False(t, got, "an unreadable stored value must fail closed regardless of operator polarity")
		})
	}
}

// TestEvalLeaf_UnsatisfiableComparisonReachabilityMatrix is spec §4.2's
// "settled by test, not asserted": for every operator that CAN reach the
// no-candidate path (determined by running the two scenarios below, not by
// reading the switch statements), pin the exact answer EvalLeaf gives.
//
// Two families reach the no-candidate path, each exercised by one canonical
// scenario that puts every operator in that family through it at once:
//
//   - kindCompare (EQ/NE/GT/GTE/LT/LTE): a field declared [String] only,
//     compared against a stored JSON number. e.numeric and e.temporal are
//     both empty regardless of op (String is the only declared type), so the
//     numeric-kind stored value has no surviving sub-condition. (The
//     single-declared-numeric-type void case is already covered by
//     TestEvalLeaf_UnsatisfiableComparisonFollowsPolarity; GT/GTE/LT/LTE are
//     NOT void there — the numeric bucket widens instead of dropping — so
//     that scenario alone does not exercise all six compare operators.)
//   - kindStringOp (every string operator, plain and I-prefixed, positive and
//     negative): a field declared [String], compared against a non-textual
//     stored value (JSON number or boolean) — string ops never stringify a
//     non-textual stored value, so there is no candidate.
//
// Within each family every operator reaches the SAME no-candidate path, so
// the answer is purely a function of isNegativeOp(op): false for a positive
// operator, true for a negative one.
func TestEvalLeaf_UnsatisfiableComparisonReachabilityMatrix(t *testing.T) {
	t.Run("kindCompare: declared [String] vs stored number", func(t *testing.T) {
		cases := []struct {
			op   FilterOp
			want bool
		}{
			{FilterEq, false},
			{FilterNe, true},
			{FilterGt, false},
			{FilterGte, false},
			{FilterLt, false},
			{FilterLte, false},
		}
		for _, c := range cases {
			t.Run(string(c.op), func(t *testing.T) {
				require.Equal(t, c.want, isNegativeOp(c.op), "table must agree with isNegativeOp")
				// Operand is a plain string ("bob") — nothing temporal about
				// it. What makes this a no-candidate case is declared being
				// [String] only while the STORED value is a JSON number, so
				// e.numeric/e.temporal stay empty regardless of the operand.
				exp, err := ExpandLeaf(c.op, "bob", nil, []DataType{String})
				require.NoError(t, err)
				got := EvalLeaf(exp, gjson.Parse("-5"))
				require.Equal(t, c.want, got)
			})
		}
	})

	t.Run("kindStringOp: declared [String] vs non-textual stored value", func(t *testing.T) {
		cases := []struct {
			op   FilterOp
			want bool
		}{
			{FilterContains, false},
			{FilterStartsWith, false},
			{FilterEndsWith, false},
			{FilterLike, false},
			{FilterMatchesRegex, false},
			{FilterNotContains, true},
			{FilterNotStartsWith, true},
			{FilterNotEndsWith, true},
			{FilterIEq, false},
			{FilterINe, true},
			{FilterIContains, false},
			{FilterINotContains, true},
			{FilterIStartsWith, false},
			{FilterINotStartsWith, true},
			{FilterIEndsWith, false},
			{FilterINotEndsWith, true},
		}
		for _, stored := range []string{"5", "true"} {
			for _, c := range cases {
				t.Run(string(c.op)+"/stored="+stored, func(t *testing.T) {
					require.Equal(t, c.want, isNegativeOp(c.op), "table must agree with isNegativeOp")
					exp, err := ExpandLeaf(c.op, "foo", nil, []DataType{String})
					require.NoError(t, err)
					got := EvalLeaf(exp, gjson.Parse(stored))
					require.Equal(t, c.want, got)
				})
			}
		}
	})
}

// One literal must have one meaning. Ingestion strips trailing zeros before
// classifying; the operand side must too, or EQUALS and NOT_EQUAL disagree
// with what was stored.
func TestExpandCompare_OperandTrailingZerosStripped(t *testing.T) {
	cases := []struct {
		name     string
		declared []DataType
		op       FilterOp
		operand  string
		stored   string
		want     bool
	}{
		// The integer family — the originally reported defect.
		{"eq 5.0 finds a stored 5", []DataType{Integer}, FilterEq, "5.0", "5", true},
		{"ne 5.0 does not match a stored 5", []DataType{Integer}, FilterNe, "5.0", "5", false},
		{"eq 5 still finds a stored 5", []DataType{Integer}, FilterEq, "5", "5", true},
		{"eq 12.5 still finds nothing on an integer leaf", []DataType{Integer}, FilterEq, "12.5", "12", false},

		// The decimal family — the same defect, one function away.
		{"eq 5.000000000000000000 finds a stored 5", []DataType{Double}, FilterEq, "5.000000000000000000", "5", true},
		{"eq 10.500000000000000000 finds a stored 10.5", []DataType{Double}, FilterEq, "10.500000000000000000", "10.5", true},
		{"ne 10.500000000000000000 does not match a stored 10.5", []DataType{Double}, FilterNe, "10.500000000000000000", "10.5", false},

		// Negative zero, which stripping also settles.
		{"ne -0.0 does not match a stored 0", []DataType{Integer}, FilterNe, "-0.0", "0", false},
		{"eq 0.000 finds a stored 0", []DataType{Integer}, FilterEq, "0.000", "0", true},

		// A genuinely imprecise operand is still dropped.
		{"eq on a 16-digit operand still finds nothing", []DataType{Double}, FilterEq, "1.234567890123456", "10.5", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exp, err := ExpandLeaf(tc.op, tc.operand, nil, tc.declared)
			if err != nil {
				t.Fatalf("ExpandLeaf: %v", err)
			}
			if got := EvalLeaf(exp, gjson.Parse(tc.stored)); got != tc.want {
				t.Errorf("EvalLeaf(%s %s vs stored %s) = %v, want %v",
					tc.op, tc.operand, tc.stored, got, tc.want)
			}
		})
	}
}

// Ordering operands must be unaffected: rounding a whole value is identity.
func TestExpandCompare_StripDoesNotDisturbOrderingOps(t *testing.T) {
	cases := []struct {
		op      FilterOp
		operand string
		stored  string
		want    bool
	}{
		{FilterGt, "12.5", "13", true},
		{FilterGt, "12.5", "12", false},
		{FilterGt, "5.0", "6", true},
		{FilterLte, "5.0", "5", true},
		{FilterLt, "3e100", "5", true},
	}
	for _, tc := range cases {
		exp, err := ExpandLeaf(tc.op, tc.operand, nil, []DataType{Integer})
		if err != nil {
			t.Fatalf("ExpandLeaf: %v", err)
		}
		if got := EvalLeaf(exp, gjson.Parse(tc.stored)); got != tc.want {
			t.Errorf("%s %s vs stored %s = %v, want %v", tc.op, tc.operand, tc.stored, got, tc.want)
		}
	}
}

// A stored value the declared type admits must be matched. Before this, the
// kernel derived the stored value's label and asked IsAssignableTo, so a
// whole number past 2^31 in a [DOUBLE] leaf was skipped even though the
// operand had produced a DOUBLE sub-condition.
func TestEvalCompare_StoredValueJudgedByAdmission(t *testing.T) {
	cases := []struct {
		name     string
		declared []DataType
		op       FilterOp
		operand  string
		stored   string
		want     bool
	}{
		{"double leaf holds a whole past 2^31", []DataType{Double}, FilterEq, "2147483648", "2147483648", true},
		{"double leaf, ordering op", []DataType{Double}, FilterGt, "2147483647", "2147483648", true},
		{"big decimal leaf, high scale", []DataType{BigDecimal}, FilterEq,
			"1.23456789012345678901234567890", "1.23456789012345678901234567890", true},
		{"out-of-range operand NotNull residual", []DataType{Double}, FilterLt, "1e300", "2147483648", true},
		{"a value the type does not admit is still not matched", []DataType{Integer}, FilterEq, "5", "2147483648", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exp, err := ExpandLeaf(tc.op, tc.operand, nil, tc.declared)
			if err != nil {
				t.Fatalf("ExpandLeaf: %v", err)
			}
			if got := EvalLeaf(exp, gjson.Parse(tc.stored)); got != tc.want {
				t.Errorf("EvalLeaf = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEvalBetween_StoredValueJudgedByAdmission(t *testing.T) {
	exp, err := ExpandLeaf(FilterBetweenInclusive, "", []string{"2147483647", "2147483649"}, []DataType{Double})
	if err != nil {
		t.Fatalf("ExpandLeaf: %v", err)
	}
	if !EvalLeaf(exp, gjson.Parse("2147483648")) {
		t.Error("a stored value the DOUBLE leaf admits must fall inside an inclusive between")
	}
}
