package spi

import (
	"encoding/json"
	"testing"
	"time"

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
// plus the Cloud operator rules the brief enumerates. Every row is evaluated
// twice — once through EvalLeafString (fast path enabled) and once through the
// explicit ExpandLeaf+EvalLeaf pipeline — and both must equal want.
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

		// --- void (parses to a type but no surviving bucket) → non-match ----
		{"void: [INT] eq 12.5 stored 12 -> non-match", FilterEq, "12.5", nil, []DataType{Integer}, strp("12"), false},
		{"void: [INT] eq 12.5 stored 13 -> non-match", FilterEq, "12.5", nil, []DataType{Integer}, strp("13"), false},

		// --- string ordering (monomorphic String, comparables) -------------
		{"str gt -> lexicographic", FilterGt, "abc", nil, []DataType{String}, strp(`"abd"`), true},
		{"str lt -> lexicographic", FilterLt, "abc", nil, []DataType{String}, strp(`"abb"`), true},

		// --- temporal (ZonedDateTime instants) ------------------------------
		{"zdt gte -> later instant", FilterGte, "2024-09-09T00:00:00Z", nil, []DataType{ZonedDateTime}, strp(`"2025-01-01T00:00:00Z"`), true},
		{"zdt gte -> earlier instant", FilterGte, "2024-09-09T00:00:00Z", nil, []DataType{ZonedDateTime}, strp(`"2024-01-01T00:00:00Z"`), false},
		{"zdt eq -> same instant", FilterEq, "2024-09-09T12:00:00Z", nil, []DataType{ZonedDateTime}, strp(`"2024-09-09T12:00:00Z"`), true},
		{"zdt ne -> absent (null rule)", FilterNe, "2024-09-09T12:00:00Z", nil, []DataType{ZonedDateTime}, nil, false},
	}
}

func TestEvalLeaf_Oracle(t *testing.T) {
	for _, r := range oracleRows() {
		r := r
		t.Run(r.name, func(t *testing.T) {
			stored := storedFrom(r.stored)

			// Path A: explicit expand + eval.
			exp, err := ExpandLeaf(r.op, r.operand, r.values, r.declared)
			if err != nil {
				t.Fatalf("ExpandLeaf unexpected error: %v", err)
			}
			gotA := EvalLeaf(exp, stored)
			if gotA != r.want {
				t.Errorf("EvalLeaf(expand) = %v, want %v", gotA, r.want)
			}

			// Path B: convenience one-shot (fast path enabled).
			gotB, err := EvalLeafString(r.op, r.operand, r.values, r.declared, stored)
			if err != nil {
				t.Fatalf("EvalLeafString unexpected error: %v", err)
			}
			if gotB != r.want {
				t.Errorf("EvalLeafString = %v, want %v", gotB, r.want)
			}
			if gotA != gotB {
				t.Errorf("fast/slow divergence: expand=%v fast=%v", gotA, gotB)
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

func TestExpandLeaf_Void(t *testing.T) {
	// [INTEGER] eq "12.5": operand IS numeric but every int bucket drops it → void.
	exp, err := ExpandLeaf(FilterEq, "12.5", nil, []DataType{Integer})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exp.void {
		t.Fatalf("expected void expansion for [INTEGER] eq 12.5")
	}
	// A void leaf evaluates to non-match for any stored value.
	if EvalLeaf(exp, gjson.Parse("12")) {
		t.Errorf("void leaf must not match")
	}
}

func TestExpandLeaf_BetweenArity(t *testing.T) {
	for _, vals := range [][]string{nil, {"1"}, {"1", "2", "3"}} {
		if _, err := ExpandLeaf(FilterBetween, "", vals, []DataType{Integer}); err == nil {
			t.Errorf("expected arity error for between with %d values", len(vals))
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

func TestLikeToRegex_Grammar(t *testing.T) {
	cases := []struct{ pattern, in string; want bool }{
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
		{"[a]", "[a]", true}, // regex metachars escaped to literals
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
