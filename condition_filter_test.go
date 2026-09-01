package spi_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
	"github.com/cyoda-platform/cyoda-go-spi/predicate"
)

func TestConditionToFilter_SimpleEquals(t *testing.T) {
	cond := &predicate.SimpleCondition{
		JsonPath:     "$.name",
		OperatorType: "EQUALS",
		Value:        "Alice",
	}
	f, err := spi.ConditionToFilter(cond, nil)
	if err != nil {
		t.Fatalf("ConditionToFilter: %v", err)
	}
	if f.Op != spi.FilterEq {
		t.Errorf("Op = %s, want eq", f.Op)
	}
	if f.Path != "name" {
		t.Errorf("Path = %s, want name", f.Path)
	}
	if f.Source != spi.SourceData {
		t.Errorf("Source = %s, want data", f.Source)
	}
	if f.Value != "Alice" {
		t.Errorf("Value = %v, want Alice", f.Value)
	}
}

// TestConditionToFilter_PrefixedPathsAccepted is the positive control for the
// mandatory "$." leader: every wire path form that must keep translating.
//
// It matters more than the rejection tests below. Requiring the leader is a
// tightening on accepted input, and the failure mode of getting it wrong is
// not "an invalid path slips through" but "a VALID path stops working" — a
// live caller's query turning into a 400. Each row here is a form real callers
// send, with the bare post-strip Filter.Path it must produce.
func TestConditionToFilter_PrefixedPathsAccepted(t *testing.T) {
	for _, tc := range []struct{ jsonPath, wantPath string }{
		{"$.name", "name"},
		{"$.x", "x"},
		{"$.address.city", "address.city"},
		{"$.some-array.some-object", "some-array.some-object"},
		{"$.a_b.C9", "a_b.C9"},
		// A numeric segment is an ordinary legal field name under the
		// grammar (name = ALPHA / DIGIT / "_" / "-"), not a special form —
		// it no longer has any connection to array-position addressing,
		// which now uses a bracket subscript instead of a dotted digit.
		{"$.tags.0", "tags.0"},
		// The storage meta block addressed as a data path. It is not meta
		// ADDRESSING — that is a lifecycle condition, which never reaches
		// stripDollarDot — but "_meta.state" is a well-formed dotted path and
		// the leader rule must not turn it into a special case.
		{"$._meta.state", "_meta.state"},
		{"$._meta.tenant_id", "_meta.tenant_id"},
	} {
		t.Run("simple/"+tc.jsonPath, func(t *testing.T) {
			f, err := spi.ConditionToFilter(&predicate.SimpleCondition{
				JsonPath: tc.jsonPath, OperatorType: "EQUALS", Value: "v",
			}, nil)
			if err != nil {
				t.Fatalf("ConditionToFilter(%q): unexpected error: %v", tc.jsonPath, err)
			}
			if f.Path != tc.wantPath {
				t.Errorf("Path = %q, want %q", f.Path, tc.wantPath)
			}
		})
		t.Run("array/"+tc.jsonPath, func(t *testing.T) {
			f, err := spi.ConditionToFilter(&predicate.ArrayCondition{
				JsonPath: tc.jsonPath, Values: []any{"v"},
			}, nil)
			if err != nil {
				t.Fatalf("ConditionToFilter(%q): unexpected error: %v", tc.jsonPath, err)
			}
			// None of these carry a trailing "[*]", so DesugarCondition
			// appends the bracket index rather than replacing one.
			if want := tc.wantPath + "[0]"; f.Path != want {
				t.Errorf("Path = %q, want %q", f.Path, want)
			}
		})
	}
}

// TestConditionToFilter_BarePathRejected pins the ruling: jsonPath is JSON Path
// nomenclature, so the "$." leader is required and a bare identifier is an
// invalid path, not a tolerated alias.
//
// The error must wrap [spi.ErrInvalidFilterPath]. Every ConditionToFilter
// caller in the engine treats a translation error as "not pushdownable, fall
// back to in-memory evaluation", and the in-memory evaluator resolves a bare
// path happily — so an unclassifiable error would leave the tightening with no
// observable effect at all, other than a silent drop off the pushdown path.
// The sentinel is what lets a caller separate "invalid input, 400" from "valid
// but unpushdownable, fall back".
func TestConditionToFilter_BarePathRejected(t *testing.T) {
	for _, p := range []string{
		"variantId",      // the ruling's own example
		"city",           // single bare segment
		"address.city",   // bare dotted path
		"_meta.state",    // the bare meta-block probe
		"tags.0",         // bare numeric segment
		"",               // empty
		"$",              // leader without the dot
		"$.",             // leader with nothing after it
		"$name",          // "$" glued to the identifier
		" $.name",        // leading whitespace is not part of the grammar
		"$['x']",         // bracket-quoted property access: no "$." leader
		"$['a']['b']",    // chained bracket-quoted access
		"$.['x']['y']",   // leader plus bracket-quoted access
		"$..name",        // recursive descent
		"$.name.",        // trailing dot
		"$..",            // degenerate
		"$. name",        // whitespace inside the path
		"$.a b",          // whitespace inside a segment
		"$.name';DROP--", // punctuation that must never reach a backend
		"$.naïve",        // non-ASCII
		"$.a..b",         // empty interior segment
		"$.\"x\"",        // quoted property access
	} {
		t.Run("simple/"+p, func(t *testing.T) {
			_, err := spi.ConditionToFilter(&predicate.SimpleCondition{
				JsonPath: p, OperatorType: "EQUALS", Value: "v",
			}, nil)
			if err == nil {
				t.Fatalf("ConditionToFilter(%q): expected an error, got nil", p)
			}
			if !errors.Is(err, spi.ErrInvalidFilterPath) {
				t.Errorf("ConditionToFilter(%q): error %v does not wrap ErrInvalidFilterPath", p, err)
			}
		})
		t.Run("array/"+p, func(t *testing.T) {
			_, err := spi.ConditionToFilter(&predicate.ArrayCondition{
				JsonPath: p, Values: []any{"v"},
			}, nil)
			if err == nil {
				t.Fatalf("ConditionToFilter(%q): expected an error, got nil", p)
			}
			if !errors.Is(err, spi.ErrInvalidFilterPath) {
				t.Errorf("ConditionToFilter(%q): error %v does not wrap ErrInvalidFilterPath", p, err)
			}
		})
	}
}

// TestConditionToFilter_ArrayWildcardTranslates pins the OTHER half of the
// error taxonomy, and it is the half easiest to break while tightening.
//
// "$.tags[*].name" is a well-formed JSON Path, and the kernel now resolves a
// subscripted path directly (see [ResolvePath]), so it must translate to a
// Filter cleanly rather than erroring at all. This used to be the
// "not pushdownable, fall back to in-memory evaluation" class; the kernel
// gaining a real resolver for subscripted paths retired that class for
// well-formed subscript syntax — see stripDollarDot's doc.
func TestConditionToFilter_ArrayWildcardTranslates(t *testing.T) {
	for _, tc := range []struct{ in, wantPath string }{
		{"$.items[*].name", "items[*].name"},
		{"$.arr[0].field", "arr[0].field"},
		{"$.foo[*]", "foo[*]"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			f, err := spi.ConditionToFilter(&predicate.SimpleCondition{
				JsonPath: tc.in, OperatorType: "EQUALS", Value: "v",
			}, nil)
			if err != nil {
				t.Fatalf("ConditionToFilter(%q): unexpected error %v", tc.in, err)
			}
			if f.Path != tc.wantPath {
				t.Errorf("ConditionToFilter(%q).Path = %q, want %q", tc.in, f.Path, tc.wantPath)
			}
		})
	}
}

// malformedSubscriptPaths are the bracket spellings that are NOT well-formed
// JSON Path. Each must land in the INVALID class (wrapping
// [spi.ErrInvalidFilterPath]) rather than the unpushdownable one.
//
// The distinction is not academic. The grammar used to stop scanning at the
// first '[' and accept whatever followed, so everything here classified as
// "valid but unpushdownable" — which every engine caller reads as "fall back
// to in-memory evaluation", where gjson resolves none of these and answers an
// empty page (or a criterion that never fires) for a field that exists. A
// wrong-but-available result, from input that is simply malformed.
var malformedSubscriptPaths = []string{
	"$.a[",           // unclosed subscript
	"$.a]",           // unmatched close
	"$.a[0",          // unclosed after an index
	"$.[0]",          // subscript with no field before it
	"$.[*]",          // ditto, wildcard
	"$.a[]",          // empty subscript
	"$.a[-1]",        // negative index
	"$.a[0:2]",       // slice
	"$.a[0,1]",       // union
	"$.a[?(@.x)]",    // filter expression
	`$.a["x"]`,       // double-quoted property access
	"$.a['x']",       // single-quoted property access
	"$.a[0];DROP",    // punctuation after a well-formed subscript
	"$.a[0].xé",      // non-ASCII after a well-formed subscript
	"$.a[0]b",        // a name glued to a subscript
	"$.a[*]..b",      // empty segment after a subscript
	"$.a[*].",        // trailing dot after a subscript
	"$.a[* ]",        // whitespace inside the subscript
	"$.tags[*][x]",   // non-index chained subscript
	"$.a[0][-1]",     // negative index in a chained subscript
	"$.a[0]['x']",    // bracket-quoted access chained onto an index
	"$.a.b[1e2]",     // exponent notation is not a decimal index
	"$.a[+1]",        // signed index
	"$.a[ 0]",        // leading whitespace inside the subscript
	"$.a[0]$",        // disallowed character after a subscript
	"$.a[0]/etc",     // slash after a subscript
	"$.a[0]'; --",    // SQL tail after a subscript
	"$.a[\x00]",      // NUL inside the subscript
	"$.a[0]\x00",     // NUL after the subscript
	"$.a[*].b[?(x)]", // filter expression in a later segment
}

// TestConditionToFilter_MalformedSubscriptIsInvalidPath pins that a bracket
// spelling outside the supported subscript forms ("[*]" and a non-negative
// decimal index) is INVALID INPUT, not merely unpushdownable.
func TestConditionToFilter_MalformedSubscriptIsInvalidPath(t *testing.T) {
	for _, p := range malformedSubscriptPaths {
		t.Run("simple/"+p, func(t *testing.T) {
			_, err := spi.ConditionToFilter(&predicate.SimpleCondition{
				JsonPath: p, OperatorType: "EQUALS", Value: "v",
			}, nil)
			if err == nil {
				t.Fatalf("ConditionToFilter(%q): expected an error, got nil", p)
			}
			if !errors.Is(err, spi.ErrInvalidFilterPath) {
				t.Errorf("ConditionToFilter(%q): error %v does not wrap ErrInvalidFilterPath", p, err)
			}
		})
		t.Run("array/"+p, func(t *testing.T) {
			_, err := spi.ConditionToFilter(&predicate.ArrayCondition{
				JsonPath: p, Values: []any{"v"},
			}, nil)
			if err == nil {
				t.Fatalf("ConditionToFilter(%q): expected an error, got nil", p)
			}
			if !errors.Is(err, spi.ErrInvalidFilterPath) {
				t.Errorf("ConditionToFilter(%q): error %v does not wrap ErrInvalidFilterPath", p, err)
			}
		})
	}
}

// TestConditionToFilter_WellFormedSubscriptTranslates is the positive control
// for the tightening above: every subscript form the grammar admits ("[*]", a
// non-negative decimal index, chained and mid-path) must translate to a
// Filter with the subscript preserved in Filter.Path, not error.
func TestConditionToFilter_WellFormedSubscriptTranslates(t *testing.T) {
	for _, tc := range []struct{ in, wantPath string }{
		{"$.tags[*]", "tags[*]"},
		{"$.tags[*].name", "tags[*].name"},
		{"$.arr[0]", "arr[0]"},
		{"$.arr[0].field", "arr[0].field"},
		{"$.arr[12].a.b", "arr[12].a.b"},
		{"$.matrix[*][*]", "matrix[*][*]"},
		{"$.matrix[0][1]", "matrix[0][1]"},
		{"$.orders[*].lines[*].sku", "orders[*].lines[*].sku"},
		{"$.a[0][*].b", "a[0][*].b"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			f, err := spi.ConditionToFilter(&predicate.SimpleCondition{
				JsonPath: tc.in, OperatorType: "EQUALS", Value: "v",
			}, nil)
			if err != nil {
				t.Fatalf("ConditionToFilter(%q): unexpected error %v", tc.in, err)
			}
			if f.Path != tc.wantPath {
				t.Errorf("ConditionToFilter(%q).Path = %q, want %q", tc.in, f.Path, tc.wantPath)
			}
		})
	}
}

// TestSimpleToFilter_KeepsWellFormedSubscript pins simpleToFilter's part of
// the same rule directly: a well-formed subscript translates rather than
// falling back, whether or not the caller supplies a fields map that
// declares the array's element type.
func TestSimpleToFilter_KeepsWellFormedSubscript(t *testing.T) {
	fields := map[string]spi.FieldDescriptor{
		"$.tags[*]": {Types: []spi.DataType{spi.String}},
	}
	for _, tc := range []struct{ in, wantPath string }{
		{"$.tags[0]", "tags[0]"},
		{"$.tags[*]", "tags[*]"},
		{"$.items[*].sku", "items[*].sku"},
	} {
		f, err := spi.ConditionToFilter(&predicate.SimpleCondition{
			JsonPath: tc.in, OperatorType: "EQUALS", Value: "A",
		}, fields)
		if err != nil {
			t.Fatalf("ConditionToFilter(%q): unexpected error %v", tc.in, err)
		}
		if f.Path != tc.wantPath {
			t.Errorf("ConditionToFilter(%q).Path = %q, want %q", tc.in, f.Path, tc.wantPath)
		}
	}
}

// TestSimpleToFilter_StillRejectsMalformedSubscript pins the other side: the
// subscript tightening in stripDollarDot did not loosen the malformed-bracket
// rejection at all — only the well-formed class stopped erroring.
func TestSimpleToFilter_StillRejectsMalformedSubscript(t *testing.T) {
	for _, p := range []string{"$.a[-1]", "$.a[0:2]", "$.a[?(@.x)]", "$.a[", "$.a[0]b"} {
		_, err := spi.ConditionToFilter(&predicate.SimpleCondition{
			JsonPath: p, OperatorType: "EQUALS", Value: "A",
		}, nil)
		if !errors.Is(err, spi.ErrInvalidFilterPath) {
			t.Errorf("ConditionToFilter(%q): want ErrInvalidFilterPath, got %v", p, err)
		}
	}
}

// TestConditionToFilter_DataLeaf_PositionalSubscriptDeclaredFoldsToWildcardKey
// pins the fold required alongside the subscript tightening above: the model
// tree records an array's element type ONCE, under the wildcard subscript
// ("$.tags[*]"), never once per index. Once a well-formed positional
// subscript stopped erroring out of simpleToFilter, "$.tags[0]" started
// reaching the fields-map lookup for the first time — and without folding the
// lookup key to the wildcard form, it misses: Declared comes back empty, and
// per the kernel's type-directed contract an empty declared set silently
// annihilates a comparison leaf to a non-match for data that is present.
//
// Filter.Path must still carry the caller's positional spelling — only the
// lookup key folds.
func TestConditionToFilter_DataLeaf_PositionalSubscriptDeclaredFoldsToWildcardKey(t *testing.T) {
	fields := map[string]spi.FieldDescriptor{
		"$.tags[*]": {Path: "$.tags[*]", Types: []spi.DataType{spi.String}, IsArray: true},
	}
	f, err := spi.ConditionToFilter(&predicate.SimpleCondition{
		JsonPath: "$.tags[0]", OperatorType: "EQUALS", Value: "go",
	}, fields)
	if err != nil {
		t.Fatalf("ConditionToFilter: %v", err)
	}
	if f.Path != "tags[0]" {
		t.Errorf("Path = %q, want %q: the lookup key folds, Filter.Path does not", f.Path, "tags[0]")
	}
	want := []spi.DataType{spi.String}
	if !reflect.DeepEqual(f.Declared, want) {
		t.Errorf("Declared = %v, want %v: %q must resolve against the wildcard key %q",
			f.Declared, want, "$.tags[0]", "$.tags[*]")
	}
}

// TestConditionToFilter_SubscriptAcceptanceMatchesValidateFilterPath pins the
// invariant that keeps the wire boundary and the plugin-facing parser from
// drifting apart: a subscript body is well-formed WIRE syntax exactly when
// [spi.ParseFilterPath] (via [spi.ValidateFilterPath]) can turn it into a
// filter path.
//
// This must hold as an if-and-only-if, not just "the wire side is a subset":
// ConditionToFilter is the boundary a caller crosses BEFORE a path ever
// reaches a plugin, and every plugin validator downstream (schema import,
// search, conditional delete) is built on ParseFilterPath. If the wire
// scanner ever accepts a subscript body the parser rejects (or vice versa),
// the engine translates a condition into a Filter that a plugin then bounces
// with ErrInvalidFilterPath — a 400 for input the engine itself accepted.
//
// The digit-run overflow case is the one that actually caught a real drift:
// IsArrayIndex only checks the byte class (digits), so a subscript with no
// magnitude bound passed the wire scan, while parsePathSub additionally
// requires strconv.Atoi to succeed and rejected it. A table pinning "same
// verdict for every body" is what stops that gap from reopening.
func TestConditionToFilter_SubscriptAcceptanceMatchesValidateFilterPath(t *testing.T) {
	for _, body := range []string{
		"0",
		"12",
		"*",
		"999999999999999999999999999999", // overflows strconv.Atoi(int)
		"18446744073709551616",           // one past uint64 max, well past int64 max
		"-1",
		"0:2",
		"?(@.x)",
		"",
		" 0",
		"0 ",
		"+1",
		"1e2",
	} {
		t.Run(body, func(t *testing.T) {
			_, condErr := spi.ConditionToFilter(&predicate.SimpleCondition{
				JsonPath: "$.a[" + body + "]", OperatorType: "EQUALS", Value: "v",
			}, nil)
			validateErr := spi.ValidateFilterPath("a[" + body + "]")

			condRejects := condErr != nil
			validateRejects := validateErr != nil
			if condRejects != validateRejects {
				t.Errorf("subscript body %q: ConditionToFilter rejects=%v (%v), ValidateFilterPath rejects=%v (%v) — must agree",
					body, condRejects, condErr, validateRejects, validateErr)
			}
			if condRejects && !errors.Is(condErr, spi.ErrInvalidFilterPath) {
				t.Errorf("subscript body %q: ConditionToFilter error %v does not wrap ErrInvalidFilterPath", body, condErr)
			}
		})
	}
}

// TestConditionToFilter_LifecycleUnaffectedByPathLeader records that meta
// ADDRESSING does not go through the wire-path rule at all: a lifecycle
// condition names a member of the closed meta vocabulary
// ([spi.MetaFieldNames]) directly, never a JSON Path, so it neither needs nor
// tolerates a "$." leader. Pinned because "how is _meta addressed now?" is the
// first question the leader rule raises.
func TestConditionToFilter_LifecycleUnaffectedByPathLeader(t *testing.T) {
	f, err := spi.ConditionToFilter(&predicate.LifecycleCondition{
		Field: "state", OperatorType: "EQUALS", Value: "CREATED",
	}, nil)
	if err != nil {
		t.Fatalf("ConditionToFilter: %v", err)
	}
	if f.Source != spi.SourceMeta || f.Path != "state" {
		t.Errorf("got Source=%s Path=%q, want meta/state", f.Source, f.Path)
	}
	if _, err := spi.ConditionToFilter(&predicate.LifecycleCondition{
		Field: "$.state", OperatorType: "EQUALS", Value: "CREATED",
	}, nil); err != nil {
		t.Fatalf("a lifecycle Field is not a JSON Path and is not validated as one: %v", err)
	}
}

func TestConditionToFilter_SimpleNestedPath(t *testing.T) {
	cond := &predicate.SimpleCondition{
		JsonPath:     "$.address.city",
		OperatorType: "NOT_EQUAL",
		Value:        "Berlin",
	}
	f, err := spi.ConditionToFilter(cond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Op != spi.FilterNe {
		t.Errorf("Op = %s, want ne", f.Op)
	}
	if f.Path != "address.city" {
		t.Errorf("Path = %s, want address.city", f.Path)
	}
}

func TestConditionToFilter_AllSimpleOperators(t *testing.T) {
	tests := []struct {
		op   string
		want spi.FilterOp
	}{
		{"EQUALS", spi.FilterEq},
		{"NOT_EQUAL", spi.FilterNe},
		{"GREATER_THAN", spi.FilterGt},
		{"LESS_THAN", spi.FilterLt},
		{"GREATER_OR_EQUAL", spi.FilterGte},
		{"LESS_OR_EQUAL", spi.FilterLte},
		{"CONTAINS", spi.FilterContains},
		{"STARTS_WITH", spi.FilterStartsWith},
		{"ENDS_WITH", spi.FilterEndsWith},
		{"LIKE", spi.FilterLike},
		{"IS_NULL", spi.FilterIsNull},
		{"NOT_NULL", spi.FilterNotNull},
		{"BETWEEN", spi.FilterBetween},
		{"BETWEEN_INCLUSIVE", spi.FilterBetweenInclusive},
		{"MATCHES_PATTERN", spi.FilterMatchesRegex},
		{"IEQUALS", spi.FilterIEq},
		{"ICONTAINS", spi.FilterIContains},
		{"ISTARTS_WITH", spi.FilterIStartsWith},
		{"IENDS_WITH", spi.FilterIEndsWith},
		{"NOT_CONTAINS", spi.FilterNotContains},
		{"NOT_STARTS_WITH", spi.FilterNotStartsWith},
		{"NOT_ENDS_WITH", spi.FilterNotEndsWith},
	}

	for _, tt := range tests {
		t.Run(tt.op, func(t *testing.T) {
			cond := &predicate.SimpleCondition{
				JsonPath:     "$.field",
				OperatorType: tt.op,
				Value:        "val",
			}
			f, err := spi.ConditionToFilter(cond, nil)
			if err != nil {
				t.Fatal(err)
			}
			if f.Op != tt.want {
				t.Errorf("Op = %s, want %s", f.Op, tt.want)
			}
			// MapOperator is the exported mapping ConditionToFilter routes
			// through; the two must not drift.
			if got := spi.MapOperator(tt.op); got != tt.want {
				t.Errorf("MapOperator(%s) = %s, want %s", tt.op, got, tt.want)
			}
		})
	}
}

// TestConditionToFilter_UnknownOperator pins the rejection that replaced the
// old regex fallback.
//
// The fallback mapped any unrecognised name to FilterMatchesRegex, on the
// theory that a pattern leaf is never pushed down and so "degrades to
// post-filtering". Not pushing down does not mean not evaluating: the kernel
// compiles the operand as an anchored pattern and matches it. So the leaf
// degraded to a DIFFERENT predicate rather than a slower one, and
// "NOT_EQUALS" — the obvious misspelling of NOT_EQUAL — became ^value$ and
// returned precisely the rows the caller meant to exclude.
func TestConditionToFilter_UnknownOperator(t *testing.T) {
	t.Run("SimpleConditionRejected", func(t *testing.T) {
		cond := &predicate.SimpleCondition{
			JsonPath:     "$.field",
			OperatorType: "SOME_UNKNOWN_OP",
			Value:        "val",
		}
		_, err := spi.ConditionToFilter(cond, nil)
		if err == nil {
			t.Fatal("unknown operator translated without error")
		}
		if !errors.Is(err, spi.ErrUnknownOperator) {
			t.Errorf("error %v does not wrap ErrUnknownOperator; callers cannot map it to 400", err)
		}
		if !strings.Contains(err.Error(), "SOME_UNKNOWN_OP") {
			t.Errorf("error %q does not name the offending operator", err)
		}
	})

	t.Run("LifecycleConditionRejected", func(t *testing.T) {
		cond := &predicate.LifecycleCondition{
			Field:        "state",
			OperatorType: "SOME_UNKNOWN_OP",
			Value:        "val",
		}
		if _, err := spi.ConditionToFilter(cond, nil); !errors.Is(err, spi.ErrUnknownOperator) {
			t.Errorf("meta leaf: got %v, want ErrUnknownOperator", err)
		}
	})

	// The specific misspelling that motivated the change: under the old
	// fallback this produced an anchored regex behaving as EQUALS, inverting
	// the caller's polarity with no diagnostic.
	t.Run("NOT_EQUALS_MisspellingRejected", func(t *testing.T) {
		cond := &predicate.SimpleCondition{
			JsonPath:     "$.status",
			OperatorType: "NOT_EQUALS",
			Value:        "draft",
		}
		if _, err := spi.ConditionToFilter(cond, nil); !errors.Is(err, spi.ErrUnknownOperator) {
			t.Errorf("got %v, want ErrUnknownOperator", err)
		}
	})

	// A nested bad leaf must not be masked by the surrounding group.
	t.Run("RejectionPropagatesOutOfAGroup", func(t *testing.T) {
		cond := &predicate.GroupCondition{
			Operator: "AND",
			Conditions: []predicate.Condition{
				&predicate.SimpleCondition{JsonPath: "$.a", OperatorType: "EQUALS", Value: 1},
				&predicate.SimpleCondition{JsonPath: "$.b", OperatorType: "NOPE", Value: 2},
			},
		}
		if _, err := spi.ConditionToFilter(cond, nil); !errors.Is(err, spi.ErrUnknownOperator) {
			t.Errorf("got %v, want ErrUnknownOperator", err)
		}
	})

	t.Run("MapOperatorReturnsZeroNotTheRegexOp", func(t *testing.T) {
		got := spi.MapOperator("SOME_UNKNOWN_OP")
		if got == spi.FilterMatchesRegex {
			t.Error("MapOperator still routes unknown names to the pattern operator")
		}
		if got != spi.FilterOp("") {
			t.Errorf("MapOperator(unknown) = %q, want the zero FilterOp", got)
		}
	})
}

func TestConditionToFilter_Lifecycle(t *testing.T) {
	cond := &predicate.LifecycleCondition{
		Field:        "state",
		OperatorType: "EQUALS",
		Value:        "ACTIVE",
	}
	f, err := spi.ConditionToFilter(cond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Op != spi.FilterEq {
		t.Errorf("Op = %s, want eq", f.Op)
	}
	if f.Source != spi.SourceMeta {
		t.Errorf("Source = %s, want meta", f.Source)
	}
	if f.Path != "state" {
		t.Errorf("Path = %s, want state", f.Path)
	}
	if f.Value != "ACTIVE" {
		t.Errorf("Value = %v, want ACTIVE", f.Value)
	}
}

// TestConditionToFilter_PreviousTransitionAlias verifies that a
// LifecycleCondition naming the "previousTransition" client-facing alias
// is canonicalized by lifecycleToFilter to the storage-vocabulary path
// "transitionForLatestSave" (see sortableMetaFields in order_class.go, the
// single source of truth for the meta vocabulary).
func TestConditionToFilter_PreviousTransitionAlias(t *testing.T) {
	c := &predicate.LifecycleCondition{
		Field:        "previousTransition",
		OperatorType: "EQUALS",
		Value:        "t",
	}
	f, err := spi.ConditionToFilter(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Path != "transitionForLatestSave" {
		t.Errorf("Path = %q, want transitionForLatestSave (previousTransition alias canonicalization)", f.Path)
	}
}

func TestConditionToFilter_GroupAND(t *testing.T) {
	cond := &predicate.GroupCondition{
		Operator: "AND",
		Conditions: []predicate.Condition{
			&predicate.SimpleCondition{JsonPath: "$.name", OperatorType: "EQUALS", Value: "Alice"},
			&predicate.SimpleCondition{JsonPath: "$.age", OperatorType: "GREATER_THAN", Value: float64(25)},
		},
	}
	f, err := spi.ConditionToFilter(cond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Op != spi.FilterAnd {
		t.Errorf("Op = %s, want and", f.Op)
	}
	if len(f.Children) != 2 {
		t.Fatalf("Children count = %d, want 2", len(f.Children))
	}
	if f.Children[0].Op != spi.FilterEq {
		t.Errorf("Children[0].Op = %s, want eq", f.Children[0].Op)
	}
	if f.Children[1].Op != spi.FilterGt {
		t.Errorf("Children[1].Op = %s, want gt", f.Children[1].Op)
	}
}

func TestConditionToFilter_GroupOR(t *testing.T) {
	cond := &predicate.GroupCondition{
		Operator: "OR",
		Conditions: []predicate.Condition{
			&predicate.SimpleCondition{JsonPath: "$.city", OperatorType: "EQUALS", Value: "Berlin"},
			&predicate.SimpleCondition{JsonPath: "$.city", OperatorType: "EQUALS", Value: "Munich"},
		},
	}
	f, err := spi.ConditionToFilter(cond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Op != spi.FilterOr {
		t.Errorf("Op = %s, want or", f.Op)
	}
	if len(f.Children) != 2 {
		t.Fatalf("Children count = %d, want 2", len(f.Children))
	}
}

func TestConditionToFilter_NestedGroup(t *testing.T) {
	cond := &predicate.GroupCondition{
		Operator: "AND",
		Conditions: []predicate.Condition{
			&predicate.SimpleCondition{JsonPath: "$.active", OperatorType: "EQUALS", Value: true},
			&predicate.GroupCondition{
				Operator: "OR",
				Conditions: []predicate.Condition{
					&predicate.SimpleCondition{JsonPath: "$.city", OperatorType: "EQUALS", Value: "Berlin"},
					&predicate.SimpleCondition{JsonPath: "$.city", OperatorType: "EQUALS", Value: "Munich"},
				},
			},
		},
	}
	f, err := spi.ConditionToFilter(cond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Op != spi.FilterAnd {
		t.Errorf("Op = %s, want and", f.Op)
	}
	if len(f.Children) != 2 {
		t.Fatalf("Children count = %d, want 2", len(f.Children))
	}
	if f.Children[1].Op != spi.FilterOr {
		t.Errorf("Children[1].Op = %s, want or", f.Children[1].Op)
	}
}

func TestConditionToFilter_Array(t *testing.T) {
	cond := &predicate.ArrayCondition{
		JsonPath: "$.tags",
		Values:   []any{"go", nil, "test"},
	}
	f, err := spi.ConditionToFilter(cond, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Array conditions desugar to an AND of positional equality checks
	// addressed by bracket index, not a dotted numeric segment — a dotted
	// segment reads as an object key to both SQL dialects, not an array
	// index, and silently drops the row.
	// Values: ["go", nil, "test"] → tags[0] = "go" AND tags[2] = "test"
	if f.Op != spi.FilterAnd {
		t.Errorf("Op = %s, want and for array condition", f.Op)
	}
	if len(f.Children) != 2 {
		t.Fatalf("Children count = %d, want 2 (nil positions skipped)", len(f.Children))
	}
	if f.Children[0].Path != "tags[0]" {
		t.Errorf("Children[0].Path = %s, want tags[0]", f.Children[0].Path)
	}
	if f.Children[0].Op != spi.FilterEq {
		t.Errorf("Children[0].Op = %s, want eq", f.Children[0].Op)
	}
	if f.Children[0].Value != "go" {
		t.Errorf("Children[0].Value = %v, want go", f.Children[0].Value)
	}
	if f.Children[1].Path != "tags[2]" {
		t.Errorf("Children[1].Path = %s, want tags[2]", f.Children[1].Path)
	}
	if f.Children[1].Value != "test" {
		t.Errorf("Children[1].Value = %v, want test", f.Children[1].Value)
	}
}

func TestConditionToFilter_ArraySingleValue(t *testing.T) {
	cond := &predicate.ArrayCondition{
		JsonPath: "$.items",
		Values:   []any{nil, "only"},
	}
	f, err := spi.ConditionToFilter(cond, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Single non-nil value should produce a bare eq filter (no AND wrapper).
	if f.Op != spi.FilterEq {
		t.Errorf("Op = %s, want eq for single-value array", f.Op)
	}
	if f.Path != "items[1]" {
		t.Errorf("Path = %s, want items[1]", f.Path)
	}
}

func TestConditionToFilter_ArrayAllNil(t *testing.T) {
	cond := &predicate.ArrayCondition{
		JsonPath: "$.arr",
		Values:   []any{nil, nil},
	}
	f, err := spi.ConditionToFilter(cond, nil)
	if err != nil {
		t.Fatal(err)
	}
	// All-nil values produce an empty AND (tautology — matches everything).
	if f.Op != spi.FilterAnd {
		t.Errorf("Op = %s, want and for all-nil array", f.Op)
	}
	if len(f.Children) != 0 {
		t.Errorf("Children count = %d, want 0", len(f.Children))
	}
}

func TestConditionToFilter_Function(t *testing.T) {
	cond := &predicate.FunctionCondition{}
	_, err := spi.ConditionToFilter(cond, nil)
	if err == nil {
		t.Fatal("expected error for FunctionCondition, got nil")
	}
}

func TestConditionToFilter_Nil(t *testing.T) {
	_, err := spi.ConditionToFilter(nil, nil)
	if err == nil {
		t.Fatal("expected error for nil condition, got nil")
	}
}

// TestConditionToFilter_HyphenatedPath_Accepted verifies that hyphenated
// field names (e.g. "some-array", "some-object") are accepted by
// ConditionToFilter — they are valid JSON key characters and safe for
// storage backend pushdown.
func TestConditionToFilter_HyphenatedPath_Accepted(t *testing.T) {
	cond := &predicate.SimpleCondition{
		JsonPath:     "$.some-array.some-object",
		OperatorType: "EQUALS",
		Value:        "abc",
	}
	f, err := spi.ConditionToFilter(cond, nil)
	if err != nil {
		t.Fatalf("ConditionToFilter with hyphenated path: unexpected error: %v", err)
	}
	if f.Path != "some-array.some-object" {
		t.Errorf("Path = %q, want some-array.some-object", f.Path)
	}
}

// TestConditionToFilter_StampsTemporalMeta verifies that a lifecycle
// condition against a known temporal meta field (creationDate) stamps
// Filter.Coercion = CoerceTemporal so storage plugins compare it as
// floored epoch-millis rather than lexicographically.
func TestConditionToFilter_StampsTemporalMeta(t *testing.T) {
	c := &predicate.LifecycleCondition{Field: "creationDate", OperatorType: "GREATER_THAN", Value: "2021-01-01T00:00:00Z"}
	f, err := spi.ConditionToFilter(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Coercion != spi.CoerceTemporal {
		t.Errorf("creationDate leaf Coercion = %v, want CoerceTemporal", f.Coercion)
	}
}

// TestConditionToFilter_DataLeafStampsNone verifies that a data-field leaf
// without a schema fields map stamps Filter.Coercion = CoerceNone (no
// classification information available → default, non-temporal comparison).
func TestConditionToFilter_DataLeafStampsNone(t *testing.T) {
	c := &predicate.SimpleCondition{JsonPath: "$.name", OperatorType: "EQUALS", Value: "x"}
	f, _ := spi.ConditionToFilter(c, nil) // no schema → CoerceNone
	if f.Coercion != spi.CoerceNone {
		t.Errorf("data leaf Coercion = %v, want CoerceNone", f.Coercion)
	}
}

// TestConditionToFilter_DataLeafTemporalType_StampsTemporal verifies the other
// half of dataCoercion: when the fields map DOES classify the data leaf as
// temporal (ClassifyType → OrderTemporal), the leaf is stamped
// CoerceTemporal. Without this the temporal-aware pushdown never lights up for
// data fields and a date leaf is compared lexically against whatever mixed
// subtype the document happens to store.
func TestConditionToFilter_DataLeafTemporalType_StampsTemporal(t *testing.T) {
	fields := map[string]spi.FieldDescriptor{
		"$.due": {Path: "$.due", Types: []spi.DataType{spi.LocalDate}},
	}
	c := &predicate.SimpleCondition{JsonPath: "$.due", OperatorType: "GREATER_THAN", Value: "2021-01-01"}
	f, err := spi.ConditionToFilter(c, fields)
	if err != nil {
		t.Fatal(err)
	}
	if f.Coercion != spi.CoerceTemporal {
		t.Errorf("temporal data leaf Coercion = %v, want CoerceTemporal", f.Coercion)
	}
}

// TestConditionToFilter_SimpleBetween_PopulatesValues verifies that a
// BETWEEN SimpleCondition (data leaf) populates Filter.Values with the two
// bounds. Every downstream BETWEEN consumer (the leaf kernel's range
// evaluation, postgres/sqlite query planners) reads Filter.Values, not
// Filter.Value — leaving Values unset means Prepare rejects the leaf with
// ErrUnevaluableLeaf (expandBetween's own arity check) instead of silently
// never matching. See TestConditionToFilter_MalformedBetweenValuesIsUnevaluable
// for that failure case.
func TestConditionToFilter_SimpleBetween_PopulatesValues(t *testing.T) {
	c := &predicate.SimpleCondition{
		JsonPath:     "$.age",
		OperatorType: "BETWEEN",
		Value:        []any{float64(18), float64(65)},
	}
	f, err := spi.ConditionToFilter(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Op != spi.FilterBetween {
		t.Fatalf("Op = %s, want between", f.Op)
	}
	if len(f.Values) != 2 {
		t.Fatalf("Values = %v, want 2-element slice [18, 65]", f.Values)
	}
	if f.Values[0] != float64(18) || f.Values[1] != float64(65) {
		t.Errorf("Values = %v, want [18 65]", f.Values)
	}
}

// TestConditionToFilter_MalformedBetweenValuesIsUnevaluable pins the
// consequence of betweenValues' arity check: a BETWEEN condition whose Value
// is not a 2-element slice produces a Filter with Values == nil, and Prepare
// now rejects that leaf outright (ErrUnevaluableLeaf, from expandBetween's
// own arity check) rather than silently building a leaf that never matches.
func TestConditionToFilter_MalformedBetweenValuesIsUnevaluable(t *testing.T) {
	c := &predicate.SimpleCondition{
		JsonPath:     "$.age",
		OperatorType: "BETWEEN",
		Value:        []any{float64(18)}, // one bound instead of two
	}
	f, err := spi.ConditionToFilter(c, nil)
	if err != nil {
		t.Fatalf("ConditionToFilter: %v", err)
	}
	if f.Values != nil {
		t.Fatalf("Values = %v, want nil for a malformed (non-2-element) BETWEEN value", f.Values)
	}
	_, err = spi.Prepare(f)
	if err == nil {
		t.Fatal("Prepare succeeded for a BETWEEN leaf with one bound, want ErrUnevaluableLeaf")
	}
	if !errors.Is(err, spi.ErrUnevaluableLeaf) {
		t.Fatalf("Prepare error = %v, want it to wrap ErrUnevaluableLeaf", err)
	}
}

// TestConditionToFilter_LifecycleBetween_PopulatesValues verifies that a
// BETWEEN LifecycleCondition on a temporal meta field (creationDate)
// populates Filter.Values with the two bounds AND stamps CoerceTemporal, so
// storage-plugin BETWEEN pushdown and spi.Prepare(f).Match can actually match.
func TestConditionToFilter_LifecycleBetween_PopulatesValues(t *testing.T) {
	c := &predicate.LifecycleCondition{
		Field:        "creationDate",
		OperatorType: "BETWEEN",
		Value:        []any{"2021-01-01T00:00:00Z", "2021-12-31T00:00:00Z"},
	}
	f, err := spi.ConditionToFilter(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Op != spi.FilterBetween {
		t.Fatalf("Op = %s, want between", f.Op)
	}
	if f.Source != spi.SourceMeta {
		t.Errorf("Source = %s, want meta", f.Source)
	}
	if f.Coercion != spi.CoerceTemporal {
		t.Errorf("Coercion = %v, want CoerceTemporal", f.Coercion)
	}
	if len(f.Values) != 2 {
		t.Fatalf("Values = %v, want 2-element slice", f.Values)
	}
	if f.Values[0] != "2021-01-01T00:00:00Z" || f.Values[1] != "2021-12-31T00:00:00Z" {
		t.Errorf("Values = %v, want [2021-01-01T00:00:00Z 2021-12-31T00:00:00Z]", f.Values)
	}
}

// TestConditionToFilter_SimpleBetweenInclusive_PopulatesValues verifies that
// a BETWEEN_INCLUSIVE SimpleCondition (data leaf) populates Filter.Values
// with the two bounds, exactly like BETWEEN — both range ops share the same
// downstream Values contract (the leaf kernel, postgres/sqlite planners).
func TestConditionToFilter_SimpleBetweenInclusive_PopulatesValues(t *testing.T) {
	c := &predicate.SimpleCondition{
		JsonPath:     "$.age",
		OperatorType: "BETWEEN_INCLUSIVE",
		Value:        []any{float64(18), float64(65)},
	}
	f, err := spi.ConditionToFilter(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Op != spi.FilterBetweenInclusive {
		t.Fatalf("Op = %s, want between_inclusive", f.Op)
	}
	if len(f.Values) != 2 {
		t.Fatalf("Values = %v, want 2-element slice [18, 65]", f.Values)
	}
	if f.Values[0] != float64(18) || f.Values[1] != float64(65) {
		t.Errorf("Values = %v, want [18 65]", f.Values)
	}
}

// TestConditionToFilter_NotContains_RoutesToKernelOp verifies that a
// NOT_CONTAINS SimpleCondition translates to a spi.Filter with
// Op: spi.FilterNotContains. Dropping the case-sensitive negatives from
// MapOperator's table would now surface as ErrUnknownOperator rather than the
// silent regex mistranslation it once caused, but the leaf must route to the
// kernel operator, not merely avoid being rejected.
func TestConditionToFilter_NotContains_RoutesToKernelOp(t *testing.T) {
	c := &predicate.SimpleCondition{
		JsonPath:     "$.name",
		OperatorType: "NOT_CONTAINS",
		Value:        "foo",
	}
	f, err := spi.ConditionToFilter(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Op != spi.FilterNotContains {
		t.Fatalf("Op = %s, want not_contains (must not fall through to matches_regex)", f.Op)
	}
}

// TestConditionToFilter_SimpleBetween_MalformedValue_LeavesValuesNil verifies
// that a malformed BETWEEN value (not a 2-element []any) leaves Filter.Values
// nil rather than panicking — validation elsewhere rejects malformed BETWEEN
// conditions, and a nil Values correctly no-matches downstream.
func TestConditionToFilter_SimpleBetween_MalformedValue_LeavesValuesNil(t *testing.T) {
	c := &predicate.SimpleCondition{
		JsonPath:     "$.age",
		OperatorType: "BETWEEN",
		Value:        "not-a-slice",
	}
	f, err := spi.ConditionToFilter(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Values != nil {
		t.Errorf("Values = %v, want nil for malformed BETWEEN value", f.Values)
	}
}

// TestConditionToFilter_DataLeaf_StampsDeclaredFromFieldsMap verifies that a
// data-field leaf stamps Filter.Declared from the model's fields map, keyed by
// the SAME (unstripped) JsonPath that dataCoercion uses — not the "$."-
// stripped Path stored on the Filter itself.
func TestConditionToFilter_DataLeaf_StampsDeclaredFromFieldsMap(t *testing.T) {
	fields := map[string]spi.FieldDescriptor{
		"$.age": {Path: "$.age", Types: []spi.DataType{spi.Integer, spi.String}},
	}
	c := &predicate.SimpleCondition{JsonPath: "$.age", OperatorType: "EQUALS", Value: float64(1)}
	f, err := spi.ConditionToFilter(c, fields)
	if err != nil {
		t.Fatal(err)
	}
	want := []spi.DataType{spi.Integer, spi.String}
	if !reflect.DeepEqual(f.Declared, want) {
		t.Errorf("Declared = %v, want %v", f.Declared, want)
	}
}

// TestConditionToFilter_DataLeaf_DeclaredNilWhenUnresolvable verifies that a
// data-field leaf whose path isn't present in the fields map (or with a nil
// fields map) leaves Filter.Declared nil rather than panicking.
func TestConditionToFilter_DataLeaf_DeclaredNilWhenUnresolvable(t *testing.T) {
	c := &predicate.SimpleCondition{JsonPath: "$.unknown", OperatorType: "EQUALS", Value: "x"}
	f, err := spi.ConditionToFilter(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Declared != nil {
		t.Errorf("Declared = %v, want nil for unresolvable field", f.Declared)
	}
}

// TestConditionToFilter_LifecycleTemporalMeta_StampsDeclaredZonedDateTime
// verifies that a lifecycle condition on a temporal meta field (creationDate)
// stamps Filter.Declared = [ZonedDateTime] — the fixed declared type for
// temporal meta leaves, mirroring the IsTemporalMetaField routing that also
// drives Coercion.
func TestConditionToFilter_LifecycleTemporalMeta_StampsDeclaredZonedDateTime(t *testing.T) {
	c := &predicate.LifecycleCondition{Field: "creationDate", OperatorType: "GREATER_THAN", Value: "2021-01-01T00:00:00Z"}
	f, err := spi.ConditionToFilter(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []spi.DataType{spi.ZonedDateTime}
	if !reflect.DeepEqual(f.Declared, want) {
		t.Errorf("Declared = %v, want %v", f.Declared, want)
	}
}

// TestConditionToFilter_LifecycleStringMeta_StampsDeclaredString verifies
// that a lifecycle condition on a non-temporal meta field (state) stamps
// Filter.Declared = [String].
func TestConditionToFilter_LifecycleStringMeta_StampsDeclaredString(t *testing.T) {
	c := &predicate.LifecycleCondition{Field: "state", OperatorType: "EQUALS", Value: "ACTIVE"}
	f, err := spi.ConditionToFilter(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []spi.DataType{spi.String}
	if !reflect.DeepEqual(f.Declared, want) {
		t.Errorf("Declared = %v, want %v", f.Declared, want)
	}
}

// TestConditionToFilter_Array_StampsDeclaredFromFieldsMap verifies that an
// array clause's desugared positional-equality leaves stamp Filter.Declared
// from the array element's fields-map entry (recorded under the base path
// with a trailing "[*]", per the model tree's flattening convention). Each
// leaf resolves this the same way any ordinary SimpleCondition on a
// positional subscript does — simpleToFilter's foldSubscriptWildcards folds
// "$.tags[0]" back to the "$.tags[*]" lookup key — with no array-specific
// declared-type logic left anywhere.
func TestConditionToFilter_Array_StampsDeclaredFromFieldsMap(t *testing.T) {
	fields := map[string]spi.FieldDescriptor{
		"$.tags[*]": {Path: "$.tags[*]", Types: []spi.DataType{spi.String}, IsArray: true},
	}
	cond := &predicate.ArrayCondition{
		JsonPath: "$.tags",
		Values:   []any{"go", nil, "test"},
	}
	f, err := spi.ConditionToFilter(cond, fields)
	if err != nil {
		t.Fatal(err)
	}
	want := []spi.DataType{spi.String}
	if len(f.Children) != 2 {
		t.Fatalf("Children count = %d, want 2", len(f.Children))
	}
	for i, child := range f.Children {
		if !reflect.DeepEqual(child.Declared, want) {
			t.Errorf("Children[%d].Declared = %v, want %v", i, child.Declared, want)
		}
	}
}

// TestConditionToFilter_Array_DeclaredNilWhenUnresolvable verifies that an
// array clause's desugared positional leaves leave Declared nil when the
// array's element path is not present in the fields map (e.g. a nil fields
// map) — the kernel falls back to non-type-directed comparison for such
// leaves.
func TestConditionToFilter_Array_DeclaredNilWhenUnresolvable(t *testing.T) {
	cond := &predicate.ArrayCondition{
		JsonPath: "$.tags",
		Values:   []any{"go", nil, "test"},
	}
	f, err := spi.ConditionToFilter(cond, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, child := range f.Children {
		if child.Declared != nil {
			t.Errorf("Children[%d].Declared = %v, want nil", i, child.Declared)
		}
	}
}

// TestConditionToFilter_NilFields_ComparisonLeavesAreUnevaluable pins the
// hazard ConditionToFilter's doc comment warns about, and the reason this
// translator lives in the SPI at all — updated for Task 2's fail-closed
// Prepare.
//
// The naive expectation — "no declared types means nothing matches" — used
// to be WRONG in a dangerous way: the kernel only consults declared types
// where it needs a type slot to compare in, so with fields == nil a
// condition degraded in two different directions at once — comparison/
// ordering leaves silently annihilated to a never-match, while string,
// substring and presence leaves evaluated normally — and a mixed condition
// returned wrong results whose direction depended on its boolean structure
// (AND dropped rows that should match; OR admitted rows a failed comparison
// should have excluded).
//
// Prepare now closes the dangerous half of that: a comparison/ordering leaf
// with no declared type to compare in is unevaluable (ErrUnevaluableLeaf),
// not a silent never-match, so Prepare rejects the whole request instead of
// answering wrong. Presence and string/pattern leaves still do not need a
// declared type at all, so they are unaffected — that half was never the bug.
//
// This table is the executable statement of that contract. If a future
// kernel change makes undeclared comparison leaves evaluable some other way,
// these expectations move — deliberately, not by accident.
func TestConditionToFilter_NilFields_ComparisonLeavesAreUnevaluable(t *testing.T) {
	docs := map[string][]byte{
		"present": []byte(`{"name":"Alice"}`),
		"null":    []byte(`{"name":null}`),
		"absent":  []byte(`{}`),
	}

	cases := []struct {
		op string
		// value is the operand; for the range ops it is the two bounds.
		value any
		// unevaluableWithoutTypes records whether Prepare rejects the
		// nil-declared leaf outright. True for leaves that need a type slot
		// to compare in.
		unevaluableWithoutTypes bool
	}{
		{"EQUALS", "Alice", true},
		{"NOT_EQUAL", "Bob", true},
		{"GREATER_THAN", "A", true},
		{"GREATER_OR_EQUAL", "A", true},
		{"LESS_THAN", "z", true},
		{"LESS_OR_EQUAL", "z", true},
		{"BETWEEN", []any{"A", "B"}, true},
		{"BETWEEN_INCLUSIVE", []any{"A", "B"}, true},

		// Presence leaves are decided from the stored value alone. They are
		// NOT comparisons despite the null operand — ExpandLeaf returns on
		// its kindUnary arm before declared is ever read.
		{"IS_NULL", nil, false},
		{"NOT_NULL", nil, false},

		// String and pattern leaves never needed a declared type.
		{"CONTAINS", "lic", false},
		{"STARTS_WITH", "Ali", false},
		{"ENDS_WITH", "ice", false},
		{"LIKE", "Al%", false},
		{"MATCHES_PATTERN", "^Ali.*$", false},
	}

	fields := map[string]spi.FieldDescriptor{
		"$.name": {Path: "$.name", Types: []spi.DataType{spi.String}},
	}

	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			c := &predicate.SimpleCondition{JsonPath: "$.name", OperatorType: tc.op, Value: tc.value}

			bare, err := spi.ConditionToFilter(c, nil)
			if err != nil {
				t.Fatalf("ConditionToFilter(nil fields): %v", err)
			}
			typed, err := spi.ConditionToFilter(c, fields)
			if err != nil {
				t.Fatalf("ConditionToFilter(with fields): %v", err)
			}
			if bare.Declared != nil {
				t.Fatalf("Declared = %v, want nil with a nil fields map", bare.Declared)
			}

			typedPrepared := mustPrepare(t, typed)
			barePrepared, bareErr := spi.Prepare(bare)

			if tc.unevaluableWithoutTypes {
				if bareErr == nil {
					t.Fatalf("Prepare(bare) succeeded for %s with no declared types, want ErrUnevaluableLeaf", tc.op)
				}
				if !errors.Is(bareErr, spi.ErrUnevaluableLeaf) {
					t.Fatalf("Prepare(bare) error = %v, want it to wrap ErrUnevaluableLeaf", bareErr)
				}
				return
			}
			if bareErr != nil {
				t.Fatalf("Prepare(bare): %v", bareErr)
			}
			for name, doc := range docs {
				gotBare := barePrepared.Match(doc, spi.EntityMeta{})
				gotTyped := typedPrepared.Match(doc, spi.EntityMeta{})
				if gotBare != gotTyped {
					t.Errorf("doc %s: nil-declared=%v but typed=%v — this op must not depend on declared types",
						name, gotBare, gotTyped)
				}
			}
		})
	}

	// The consequence, made concrete: an AND of a surviving substring leaf and
	// an unevaluable equality leaf is REJECTED outright, rather than silently
	// dropping a document that satisfies both leaves.
	t.Run("MixedAndRejectsRatherThanDropAMatchingDocument", func(t *testing.T) {
		cond := &predicate.GroupCondition{
			Operator: "AND",
			Conditions: []predicate.Condition{
				&predicate.SimpleCondition{JsonPath: "$.name", OperatorType: "CONTAINS", Value: "lic"},
				&predicate.SimpleCondition{JsonPath: "$.name", OperatorType: "EQUALS", Value: "Alice"},
			},
		}
		doc := docs["present"]

		bare, err := spi.ConditionToFilter(cond, nil)
		if err != nil {
			t.Fatalf("ConditionToFilter: %v", err)
		}
		typed, err := spi.ConditionToFilter(cond, fields)
		if err != nil {
			t.Fatalf("ConditionToFilter: %v", err)
		}
		if !mustPrepare(t, typed).Match(doc, spi.EntityMeta{}) {
			t.Fatal("setup invariant: the document must match when declared types are supplied")
		}
		_, err = spi.Prepare(bare)
		if err == nil {
			t.Fatal("Prepare(bare) succeeded; want rejection — an AND containing an unevaluable EQUALS leaf must not silently drop a matching document")
		}
		if !errors.Is(err, spi.ErrUnevaluableLeaf) {
			t.Fatalf("Prepare(bare) error = %v, want it to wrap ErrUnevaluableLeaf", err)
		}
	})
}

// TestConditionToFilter_WithFields_DataLeafMatches is the other half of the
// pair above: the SAME condition and the SAME document, translated with a
// correct fields map, matches. The contrast is the whole point of threading a
// fields map through ConditionToFilter — the declared type set is what turns a
// silently-empty filter into a correct one.
func TestConditionToFilter_WithFields_DataLeafMatches(t *testing.T) {
	data := []byte(`{"name":"Alice"}`)
	fields := map[string]spi.FieldDescriptor{
		"$.name": {Path: "$.name", Types: []spi.DataType{spi.String}},
	}
	c := &predicate.SimpleCondition{JsonPath: "$.name", OperatorType: "EQUALS", Value: "Alice"}

	f, err := spi.ConditionToFilter(c, fields)
	if err != nil {
		t.Fatalf("ConditionToFilter: %v", err)
	}
	want := []spi.DataType{spi.String}
	if !reflect.DeepEqual(f.Declared, want) {
		t.Fatalf("Declared = %v, want %v", f.Declared, want)
	}
	if !mustPrepare(t, f).Match(data, spi.EntityMeta{}) {
		t.Error("Prepare(f).Match = false, want true: a declared string leaf must match an equal stored value")
	}
	// And it still discriminates — it is not matching everything.
	if mustPrepare(t, f).Match([]byte(`{"name":"Bob"}`), spi.EntityMeta{}) {
		t.Error("Prepare(f).Match = true for a non-equal value, want false")
	}
}

// TestConditionToFilter_NilFields_MetaLeafStillMatches verifies the exemption
// stated in ConditionToFilter's contract: meta leaves draw their declared
// types from the static meta vocabulary, not from the fields map, so a nil
// fields map does NOT degrade them. Only data leaves fail closed.
func TestConditionToFilter_NilFields_MetaLeafStillMatches(t *testing.T) {
	c := &predicate.LifecycleCondition{Field: "state", OperatorType: "EQUALS", Value: "ACTIVE"}
	f, err := spi.ConditionToFilter(c, nil)
	if err != nil {
		t.Fatalf("ConditionToFilter: %v", err)
	}
	if !mustPrepare(t, f).Match([]byte(`{}`), spi.EntityMeta{State: "ACTIVE"}) {
		t.Error("Prepare(f).Match = false, want true: a meta leaf must match with a nil fields map")
	}
	if mustPrepare(t, f).Match([]byte(`{}`), spi.EntityMeta{State: "LOCKED"}) {
		t.Error("Prepare(f).Match = true for a non-equal state, want false")
	}
}

// TestConditionToFilter_FieldsLookupUsesPrefixedKey pins that a condition's
// jsonPath resolves against the FieldsMap, whose keys are canonically
// "$."-prefixed.
//
// The lookup missing is not a visible failure: it silently yields no declared
// types, and the type-directed kernel turns a comparison leaf with no declared
// type into a permanent non-match — a field that exists and holds matching data
// answers with an empty page.
//
// This test used to assert the same for an UNPREFIXED path, because a bare
// jsonPath was accepted at the wire boundary and had to be normalised before
// the lookup. It no longer is: the leader is mandatory, so the only spelling
// that reaches here is the prefixed one. The bare spellings moved to
// TestConditionToFilter_BarePathRejected.
func TestConditionToFilter_FieldsLookupUsesPrefixedKey(t *testing.T) {
	fields := map[string]spi.FieldDescriptor{
		"$.age":     {Path: "$.age", Types: []spi.DataType{spi.Long}},
		"$.when":    {Path: "$.when", Types: []spi.DataType{spi.ZonedDateTime}},
		"$.tags[*]": {Path: "$.tags[*]", Types: []spi.DataType{spi.String}, IsArray: true},
	}

	t.Run("declared", func(t *testing.T) {
		c := &predicate.SimpleCondition{JsonPath: "$.age", OperatorType: "EQUALS", Value: 30}
		f, err := spi.ConditionToFilter(c, fields)
		if err != nil {
			t.Fatalf("ConditionToFilter: %v", err)
		}
		if len(f.Declared) != 1 || f.Declared[0] != spi.Long {
			t.Errorf("Declared = %v, want [LONG]: %q must resolve against key %q", f.Declared, "$.age", "$.age")
		}
	})

	// Coercion is looked up with the same key: a declared-temporal field whose
	// lookup misses is stamped CoerceNone, so SQL planners compare it as text
	// rather than as an instant.
	t.Run("coercion", func(t *testing.T) {
		c := &predicate.SimpleCondition{JsonPath: "$.when", OperatorType: "GREATER_THAN", Value: "2020-01-01T00:00:00Z"}
		f, err := spi.ConditionToFilter(c, fields)
		if err != nil {
			t.Fatalf("ConditionToFilter: %v", err)
		}
		if f.Coercion != spi.CoerceTemporal {
			t.Errorf("Coercion = %v, want CoerceTemporal for %q", f.Coercion, "$.when")
		}
	})

	// A genuinely unknown path still carries no declared types — the deliberate
	// degrade-to-non-match this must not disturb.
	t.Run("unknown path unresolved", func(t *testing.T) {
		c := &predicate.SimpleCondition{JsonPath: "$.nosuch", OperatorType: "EQUALS", Value: 1}
		f, err := spi.ConditionToFilter(c, fields)
		if err != nil {
			t.Fatalf("ConditionToFilter: %v", err)
		}
		if f.Declared != nil {
			t.Errorf("Declared = %v, want nil for an unknown path", f.Declared)
		}
		if f.Coercion != spi.CoerceNone {
			t.Errorf("Coercion = %v, want CoerceNone for an unknown path", f.Coercion)
		}
	})
}

func TestConditionToFilter_EmptyGroupIdentityEncodings(t *testing.T) {
	t.Run("AllNilArrayYieldsNonNilEmptyChildren", func(t *testing.T) {
		// DesugarCondition turns an all-nil array into a literal empty AND
		// GroupCondition (no bespoke tautology encoding of its own), so this
		// now goes through the exact same groupToFilter path — and gets the
		// exact same non-nil-empty-slice shape — as the caller-written empty
		// group case below.
		f, err := spi.ConditionToFilter(&predicate.ArrayCondition{
			JsonPath: "$.arr", Values: []any{nil, nil},
		}, nil)
		if err != nil {
			t.Fatalf("ConditionToFilter: %v", err)
		}
		if f.Op != spi.FilterAnd {
			t.Fatalf("Op = %s, want and", f.Op)
		}
		if f.Children == nil {
			t.Error("Children = nil, want a non-nil empty slice (groupToFilter always allocates)")
		}
		if len(f.Children) != 0 {
			t.Errorf("len(Children) = %d, want 0", len(f.Children))
		}
		if !mustPrepare(t, f).Match([]byte(`{}`), spi.EntityMeta{}) {
			t.Error("an empty AND must be the identity (match everything), not match nothing")
		}
	})

	for _, op := range []struct {
		operator string
		want     spi.FilterOp
	}{{"AND", spi.FilterAnd}, {"OR", spi.FilterOr}} {
		t.Run("EmptyGroup"+op.operator+"YieldsNonNilEmptyChildren", func(t *testing.T) {
			f, err := spi.ConditionToFilter(&predicate.GroupCondition{
				Operator: op.operator, Conditions: nil,
			}, nil)
			if err != nil {
				t.Fatalf("ConditionToFilter: %v", err)
			}
			if f.Op != op.want {
				t.Fatalf("Op = %s, want %s", f.Op, op.want)
			}
			if f.Children == nil {
				t.Error("Children = nil, want a non-nil empty slice (groupToFilter always allocates)")
			}
			if len(f.Children) != 0 {
				t.Errorf("len(Children) = %d, want 0", len(f.Children))
			}
		})
	}
}

// TestLookupOperator pins the up-front operator-validation form, for callers
// that want to reject a whole request before any partial translation work
// rather than take ConditionToFilter's ErrUnknownOperator mid-tree.
func TestLookupOperator(t *testing.T) {
	t.Run("KnownOperatorsResolve", func(t *testing.T) {
		for _, op := range []string{
			"EQUALS", "NOT_EQUAL", "GREATER_THAN", "LESS_THAN",
			"GREATER_OR_EQUAL", "LESS_OR_EQUAL", "CONTAINS", "STARTS_WITH",
			"ENDS_WITH", "LIKE", "IS_NULL", "NOT_NULL", "BETWEEN",
			"BETWEEN_INCLUSIVE", "MATCHES_PATTERN", "IEQUALS", "INOT_EQUAL",
			"ICONTAINS", "INOT_CONTAINS", "NOT_CONTAINS", "ISTARTS_WITH",
			"INOT_STARTS_WITH", "NOT_STARTS_WITH", "IENDS_WITH",
			"INOT_ENDS_WITH", "NOT_ENDS_WITH",
		} {
			got, ok := spi.LookupOperator(op)
			if !ok {
				t.Errorf("LookupOperator(%q) reported unknown", op)
			}
			if want := spi.MapOperator(op); got != want {
				t.Errorf("LookupOperator(%q) = %s, want %s (must agree with MapOperator)", op, got, want)
			}
		}
	})

	t.Run("MATCHES_PATTERN_IsNotMistakenForTheFallback", func(t *testing.T) {
		got, ok := spi.LookupOperator("MATCHES_PATTERN")
		if !ok {
			t.Error("MATCHES_PATTERN must be recognised — it is a real operator, not the unknown-op fallback")
		}
		if got != spi.FilterMatchesRegex {
			t.Errorf("got %s, want matches_regex", got)
		}
	})

	t.Run("UnknownOperatorsRejected", func(t *testing.T) {
		// NOT_EQUALS is the dangerous one: a plausible misspelling of
		// NOT_EQUAL that MapOperator turns into an anchored regex, inverting
		// the caller's intended polarity.
		for _, op := range []string{"NOT_EQUALS", "REGEX_MATCH", "EQUAL", "", "'; DROP", "IS_CHANGED"} {
			if _, ok := spi.LookupOperator(op); ok {
				t.Errorf("LookupOperator(%q) reported known; unknown names must be rejected so callers do not evaluate them as regexes", op)
			}
		}
	})

	// The careless call form `op, _ := LookupOperator(name)` must not hand
	// back the regex fallback, or the safe function is as dangerous as the
	// unsafe one for anyone who drops the ok.
	t.Run("UnknownReturnsZeroOpNotTheRegexFallback", func(t *testing.T) {
		for _, op := range []string{"NOT_EQUALS", "REGEX_MATCH", ""} {
			got, _ := spi.LookupOperator(op)
			if got == spi.FilterMatchesRegex {
				t.Errorf("LookupOperator(%q) returned the regex fallback; a caller ignoring ok would evaluate the operand as a regex", op)
			}
			if got != spi.FilterOp("") {
				t.Errorf("LookupOperator(%q) = %q, want the zero FilterOp", op, got)
			}
		}
	})
}

// TestOperatorNames_MatchesMapOperator is the drift guard between the
// enumerable name list and the MapOperator type switch, which Go cannot
// enumerate. Without it the two are free to disagree silently — which is the
// state the engine's own copy of this table is in today.
func TestOperatorNames_MatchesMapOperator(t *testing.T) {
	names := spi.OperatorNames()

	t.Run("EveryNameIsRecognised", func(t *testing.T) {
		for _, n := range names {
			if _, ok := spi.LookupOperator(n); !ok {
				t.Errorf("OperatorNames lists %q but LookupOperator rejects it", n)
			}
		}
	})

	t.Run("SortedAndDeduplicated", func(t *testing.T) {
		seen := make(map[string]bool, len(names))
		for i, n := range names {
			if seen[n] {
				t.Errorf("OperatorNames repeats %q", n)
			}
			seen[n] = true
			if i > 0 && names[i-1] >= n {
				t.Errorf("OperatorNames not sorted at index %d: %q >= %q", i, names[i-1], n)
			}
		}
	})

	// A change detector: adding a case to MapOperator without adding the name
	// here (or vice versa) is caught by the count, since nothing else can
	// compare a slice against a type switch.
	t.Run("CountMatchesTheOperatorTable", func(t *testing.T) {
		if len(names) != 26 {
			t.Errorf("OperatorNames has %d entries, want 26 — if you added an operator to MapOperator, add it here too", len(names))
		}
	})

	t.Run("ReturnsAFreshSliceCallersCanMutate", func(t *testing.T) {
		a := spi.OperatorNames()
		a[0] = "MUTATED"
		if b := spi.OperatorNames(); b[0] == "MUTATED" {
			t.Error("OperatorNames leaks its backing array; a caller mutating the result corrupts the table")
		}
	})
}

func TestValidateConditionOperators(t *testing.T) {
	t.Run("AcceptsAValidNestedTree", func(t *testing.T) {
		cond := &predicate.GroupCondition{
			Operator: "AND",
			Conditions: []predicate.Condition{
				&predicate.SimpleCondition{JsonPath: "$.name", OperatorType: "EQUALS", Value: "Alice"},
				&predicate.GroupCondition{
					Operator: "OR",
					Conditions: []predicate.Condition{
						&predicate.LifecycleCondition{Field: "state", OperatorType: "NOT_EQUAL", Value: "draft"},
						&predicate.ArrayCondition{JsonPath: "$.tags", Values: []any{"a", nil}},
					},
				},
			},
		}
		if err := spi.ValidateConditionOperators(cond); err != nil {
			t.Errorf("ValidateConditionOperators: %v", err)
		}
	})

	// The whole point: the bad operator is buried, not at the root.
	t.Run("RejectsAnUnknownOperatorNestedInAGroup", func(t *testing.T) {
		cond := &predicate.GroupCondition{
			Operator: "AND",
			Conditions: []predicate.Condition{
				&predicate.SimpleCondition{JsonPath: "$.a", OperatorType: "EQUALS", Value: 1},
				&predicate.SimpleCondition{JsonPath: "$.b", OperatorType: "NOT_EQUALS", Value: 2},
			},
		}
		err := spi.ValidateConditionOperators(cond)
		if err == nil {
			t.Fatal("ValidateConditionOperators accepted NOT_EQUALS; it translates to an anchored regex and inverts the caller's polarity")
		}
		if !strings.Contains(err.Error(), "NOT_EQUALS") {
			t.Errorf("error %q does not name the offending operator", err)
		}
		if !strings.Contains(err.Error(), "NOT_EQUAL,") && !strings.Contains(err.Error(), "NOT_EQUAL ") {
			t.Errorf("error %q does not list the canonical set the caller needs to self-correct", err)
		}
	})

	t.Run("RejectsAnEmptyOperatorOnBothLeafKinds", func(t *testing.T) {
		for _, cond := range []predicate.Condition{
			&predicate.SimpleCondition{JsonPath: "$.a", OperatorType: ""},
			&predicate.LifecycleCondition{Field: "state", OperatorType: ""},
		} {
			if err := spi.ValidateConditionOperators(cond); err == nil {
				t.Errorf("%T with an empty operatorType was accepted", cond)
			}
		}
	})

	t.Run("NilAndOperatorlessNodesPass", func(t *testing.T) {
		for _, cond := range []predicate.Condition{
			nil,
			&predicate.ArrayCondition{JsonPath: "$.tags", Values: []any{"a"}},
			&predicate.FunctionCondition{},
		} {
			if err := spi.ValidateConditionOperators(cond); err != nil {
				t.Errorf("%T carries no operator but was rejected: %v", cond, err)
			}
		}
	})

	// A programmatically built tree bypasses the parser's own depth cap, so
	// the walker must not recurse until the stack blows.
	t.Run("DepthCapped", func(t *testing.T) {
		var cond predicate.Condition = &predicate.SimpleCondition{
			JsonPath: "$.a", OperatorType: "EQUALS", Value: 1,
		}
		for i := 0; i < spi.MaxConditionDepth+10; i++ {
			cond = &predicate.GroupCondition{Operator: "AND", Conditions: []predicate.Condition{cond}}
		}
		err := spi.ValidateConditionOperators(cond)
		if err == nil {
			t.Fatal("a tree deeper than MaxConditionDepth was accepted")
		}
		if !strings.Contains(err.Error(), "depth") {
			t.Errorf("error %q does not identify the depth cap as the cause", err)
		}
	})
}

func TestNormalisePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"name", "$.name"},
		{"$.name", "$.name"},
		{"  name  ", "$.name"},
		{"$name", "$name"},
		{"a.b.c", "$.a.b.c"},
		{"$.tags[*]", "$.tags[*]"},
		{"", ""},
	}
	for _, c := range cases {
		if got := spi.NormalisePath(c.in); got != c.want {
			t.Errorf("NormalisePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// Idempotence is the property callers rely on when assembling fields-map
	// keys from paths of unknown provenance.
	t.Run("Idempotent", func(t *testing.T) {
		for _, c := range cases {
			once := spi.NormalisePath(c.in)
			if twice := spi.NormalisePath(once); twice != once {
				t.Errorf("NormalisePath(%q) not idempotent: %q then %q", c.in, once, twice)
			}
		}
	})
}

func TestValidateConditionPatterns(t *testing.T) {
	bad := &predicate.SimpleCondition{JsonPath: "$.name", OperatorType: "MATCHES_PATTERN", Value: `\Q`}
	good := &predicate.SimpleCondition{JsonPath: "$.name", OperatorType: "MATCHES_PATTERN", Value: `a|b`}
	badLike := &predicate.SimpleCondition{JsonPath: "$.name", OperatorType: "LIKE", Value: `a\`}

	if err := spi.ValidateConditionPatterns(good); err != nil {
		t.Errorf("valid pattern rejected: %v", err)
	}
	cases := map[string]struct {
		cond   predicate.Condition
		wantOp string
	}{
		// wantOp is the domain operator string the caller actually wrote
		// (predicate.SimpleCondition.OperatorType) — the vocabulary
		// ValidateConditionPatterns' errors speak, never the internal
		// FilterOp spelling ("matches_regex").
		"regex": {bad, "MATCHES_PATTERN"},
		"like":  {badLike, "LIKE"},
	}
	for name, c := range cases {
		err := spi.ValidateConditionPatterns(c.cond)
		if err == nil {
			t.Errorf("%s: invalid pattern accepted", name)
			continue
		}
		if !errors.Is(err, spi.ErrInvalidPattern) {
			t.Errorf("%s: error %v does not wrap ErrInvalidPattern", name, err)
		}
		// Actionable against a large tree: the leaf is named.
		if !strings.Contains(err.Error(), "$.name") {
			t.Errorf("%s: error does not name the leaf: %v", name, err)
		}
		// The operator name the caller wrote is safe to surface and must
		// survive; the operand (checked elsewhere) must not.
		if !strings.Contains(err.Error(), c.wantOp) {
			t.Errorf("%s: error does not name the operator %q: %v", name, c.wantOp, err)
		}
		// Regression guard: the internal FilterOp spelling must never leak
		// into a client-facing error — the caller never wrote "matches_regex".
		if strings.Contains(err.Error(), "matches_regex") {
			t.Errorf("%s: error leaks internal FilterOp spelling %q: %v", name, "matches_regex", err)
		}
	}

	// Nested: the walker recurses into groups.
	group := &predicate.GroupCondition{Operator: "AND", Conditions: []predicate.Condition{good, bad}}
	if err := spi.ValidateConditionPatterns(group); err == nil {
		t.Error("group containing an invalid pattern accepted")
	}

	// Lifecycle leaves are checked too, and named by Field.
	lc := &predicate.LifecycleCondition{Field: "state", OperatorType: "LIKE", Value: `x\`}
	err := spi.ValidateConditionPatterns(lc)
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Errorf("lifecycle leaf not checked or not named: %v", err)
	}

	// Arms that carry no operator, and nil.
	for name, cond := range map[string]predicate.Condition{
		"array":    &predicate.ArrayCondition{JsonPath: "$.tags", Values: []any{"a"}},
		"function": &predicate.FunctionCondition{},
	} {
		if err := spi.ValidateConditionPatterns(cond); err != nil {
			t.Errorf("%s condition should pass, got %v", name, err)
		}
	}
	if err := spi.ValidateConditionPatterns(nil); err != nil {
		t.Errorf("nil condition should pass, got %v", err)
	}

	// Non-pattern and unrecognised operators are not this function's job.
	for _, op := range []string{"EQUALS", "NOT_AN_OPERATOR"} {
		c := &predicate.SimpleCondition{JsonPath: "$.a", OperatorType: op, Value: `\Q`}
		if err := spi.ValidateConditionPatterns(c); err != nil {
			t.Errorf("operator %q should pass ValidateConditionPatterns, got %v", op, err)
		}
	}
}

func TestValidateConditionPatterns_DepthGuard(t *testing.T) {
	var cond predicate.Condition = &predicate.SimpleCondition{
		JsonPath: "$.a", OperatorType: "EQUALS", Value: "x",
	}
	for i := 0; i < spi.MaxConditionDepth+1; i++ {
		cond = &predicate.GroupCondition{Operator: "AND", Conditions: []predicate.Condition{cond}}
	}
	if err := spi.ValidateConditionPatterns(cond); err == nil {
		t.Error("depth guard did not fire")
	}
}

func TestValidateLeafPattern(t *testing.T) {
	err := spi.ValidateLeafPattern(spi.FilterMatchesRegex, `)|(`)
	if err == nil {
		t.Fatal("anchor-escape operand accepted")
	}
	// Accurate here: op is exactly what the caller passed in, so naming it
	// back — in FilterOp's own spelling — is honest, unlike
	// ValidateConditionPatterns, which speaks the caller's domain vocabulary.
	if !strings.Contains(err.Error(), string(spi.FilterMatchesRegex)) {
		t.Errorf("error does not name the FilterOp %q: %v", spi.FilterMatchesRegex, err)
	}
	if err := spi.ValidateLeafPattern(spi.FilterLike, `100%`); err != nil {
		t.Errorf("valid LIKE operand rejected: %v", err)
	}
	if err := spi.ValidateLeafPattern(spi.FilterEq, `\Q`); err != nil {
		t.Errorf("non-pattern operator should pass, got %v", err)
	}
}

func TestDesugarCondition_ArrayClause(t *testing.T) {
	got := spi.DesugarCondition(&predicate.ArrayCondition{
		JsonPath: "$.tags[*]",
		Values:   []any{"A", nil, "C"},
	})
	g, ok := got.(*predicate.GroupCondition)
	if !ok {
		t.Fatalf("want *GroupCondition, got %T", got)
	}
	if g.Operator != "AND" || len(g.Conditions) != 2 {
		t.Fatalf("want AND of 2, got %q of %d", g.Operator, len(g.Conditions))
	}
	want := []struct{ path, value string }{{"$.tags[0]", "A"}, {"$.tags[2]", "C"}}
	for i, w := range want {
		s, ok := g.Conditions[i].(*predicate.SimpleCondition)
		if !ok {
			t.Fatalf("child %d: want *SimpleCondition, got %T", i, g.Conditions[i])
		}
		if s.JsonPath != w.path || s.OperatorType != "EQUALS" || s.Value != w.value {
			t.Errorf("child %d = {%q,%q,%v}, want {%q,EQUALS,%q}",
				i, s.JsonPath, s.OperatorType, s.Value, w.path, w.value)
		}
	}
}

func TestDesugarCondition_AllNullIsTautology(t *testing.T) {
	got := spi.DesugarCondition(&predicate.ArrayCondition{
		JsonPath: "$.tags[*]", Values: []any{nil, nil},
	})
	g, ok := got.(*predicate.GroupCondition)
	if !ok || g.Operator != "AND" || len(g.Conditions) != 0 {
		t.Fatalf("want an empty AND, got %#v", got)
	}
}

// TestDesugarCondition_EmptyValuesIsTautology pins a case distinct from
// TestDesugarCondition_AllNullIsTautology above: Values is a literally EMPTY
// slice (zero elements), not a slice of nils. The loop over c.Values simply
// never executes either way, so both collapse to the same empty AND — but
// that equivalence is a property of the range-over-nothing loop, not
// something the all-null case exercises, and was unpinned before this test.
func TestDesugarCondition_EmptyValuesIsTautology(t *testing.T) {
	got := spi.DesugarCondition(&predicate.ArrayCondition{
		JsonPath: "$.tags[*]", Values: []any{},
	})
	g, ok := got.(*predicate.GroupCondition)
	if !ok || g.Operator != "AND" || len(g.Conditions) != 0 {
		t.Fatalf("want an empty AND, got %#v", got)
	}
}

func TestDesugarCondition_RecursesIntoGroups(t *testing.T) {
	got := spi.DesugarCondition(&predicate.GroupCondition{
		Operator: "OR",
		Conditions: []predicate.Condition{
			&predicate.ArrayCondition{JsonPath: "$.tags[*]", Values: []any{"A"}},
		},
	})
	g := got.(*predicate.GroupCondition)
	if _, isArray := g.Conditions[0].(*predicate.ArrayCondition); isArray {
		t.Fatal("nested ArrayCondition was not desugared")
	}
}

func TestConditionToFilter_ArrayClauseProducesBracketPaths(t *testing.T) {
	// The defect this closes: the positional leaf used to carry the dotted
	// path "tags.0", which both SQL dialects read as a field named "0", so
	// the row was dropped by the WHERE clause and no residual could recover it.
	f, err := spi.ConditionToFilter(&predicate.ArrayCondition{
		JsonPath: "$.tags[*]", Values: []any{"A"},
	}, map[string]spi.FieldDescriptor{"$.tags[*]": {Types: []spi.DataType{spi.String}}})
	if err != nil {
		t.Fatalf("ConditionToFilter: %v", err)
	}
	if f.Path != "tags[0]" {
		t.Errorf("Path = %q, want %q", f.Path, "tags[0]")
	}
	if len(f.Declared) != 1 || f.Declared[0] != spi.String {
		t.Errorf("Declared = %v, want [String]", f.Declared)
	}
}

// TestGroupToFilter_RejectsUnknownOperator pins that groupToFilter (reached
// via ConditionToFilter) no longer folds any operator that is not
// case-insensitively "OR" into FilterAnd. "NOTT", "xor" and "" must all be
// rejected, and so must "and": the wire operator is matched exactly against
// the closed set {"AND","OR","NOT"}, consistent with MapOperator's own
// exact-case leaf-operator convention — a case variant is an unrecognised
// operator, not a tolerated alias.
func TestGroupToFilter_RejectsUnknownOperator(t *testing.T) {
	for _, op := range []string{"NOTT", "xor", "", "and"} {
		_, err := spi.ConditionToFilter(&predicate.GroupCondition{
			Operator: op, Conditions: []predicate.Condition{}}, nil)
		require.ErrorIs(t, err, spi.ErrUnknownOperator, "operator %q must not map to AND", op)
	}
}

// TestGroupToFilter_Not pins the wire mapping GroupCondition{Operator:"NOT"}
// -> Filter{Op: FilterNot, Children: [...]}.
func TestGroupToFilter_Not(t *testing.T) {
	f, err := spi.ConditionToFilter(&predicate.GroupCondition{
		Operator: "NOT",
		Conditions: []predicate.Condition{
			&predicate.SimpleCondition{JsonPath: "$.s", OperatorType: "EQUALS", Value: "x"},
		},
	}, map[string]spi.FieldDescriptor{"$.s": {Types: []spi.DataType{spi.String}}})
	require.NoError(t, err)
	require.Equal(t, spi.FilterNot, f.Op)
	require.Len(t, f.Children, 1)
	require.Equal(t, spi.FilterEq, f.Children[0].Op)
}

// TestValidateConditionOperators_RejectsAnUnknownGroupOperator closes a gap
// that TestGroupToFilter_RejectsUnknownOperator's fix exposes: the front-door
// validator walked into a GroupCondition's children but never checked the
// group's OWN operator, because before this change every string that was not
// case-insensitively "OR" was a silently-valid alias for AND. Now that a
// group operator can be genuinely invalid, a caller relying on
// ValidateConditionOperators as the sole boundary check (per its own doc)
// must not let a bad group operator through to ConditionToFilter undetected.
func TestValidateConditionOperators_RejectsAnUnknownGroupOperator(t *testing.T) {
	cond := &predicate.GroupCondition{
		Operator: "XOR",
		Conditions: []predicate.Condition{
			&predicate.SimpleCondition{JsonPath: "$.a", OperatorType: "EQUALS", Value: 1},
		},
	}
	err := spi.ValidateConditionOperators(cond)
	require.ErrorIs(t, err, spi.ErrUnknownOperator)
	require.Contains(t, err.Error(), "XOR")
}
