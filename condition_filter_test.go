package spi_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

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

func TestConditionToFilter_SimpleNoPrefix(t *testing.T) {
	cond := &predicate.SimpleCondition{
		JsonPath:     "city",
		OperatorType: "EQUALS",
		Value:        "Berlin",
	}
	f, err := spi.ConditionToFilter(cond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Path != "city" {
		t.Errorf("Path = %s, want city", f.Path)
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
	// Array conditions expand to AND of positional equality checks.
	// Values: ["go", nil, "test"] → tags.0 = "go" AND tags.2 = "test"
	if f.Op != spi.FilterAnd {
		t.Errorf("Op = %s, want and for array condition", f.Op)
	}
	if len(f.Children) != 2 {
		t.Fatalf("Children count = %d, want 2 (nil positions skipped)", len(f.Children))
	}
	if f.Children[0].Path != "tags.0" {
		t.Errorf("Children[0].Path = %s, want tags.0", f.Children[0].Path)
	}
	if f.Children[0].Op != spi.FilterEq {
		t.Errorf("Children[0].Op = %s, want eq", f.Children[0].Op)
	}
	if f.Children[0].Value != "go" {
		t.Errorf("Children[0].Value = %v, want go", f.Children[0].Value)
	}
	if f.Children[1].Path != "tags.2" {
		t.Errorf("Children[1].Path = %s, want tags.2", f.Children[1].Path)
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
	if f.Path != "items.1" {
		t.Errorf("Path = %s, want items.1", f.Path)
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

// TestConditionToFilter_WildcardPath_ReturnsError verifies that paths
// containing JSONPath array-wildcard or subscript syntax (e.g. "[*]", "[0]")
// cause ConditionToFilter to return an error so the caller falls back to
// in-memory evaluation. Such paths cannot be translated to pushdown filters.
func TestConditionToFilter_WildcardPath_ReturnsError(t *testing.T) {
	wildcardPaths := []string{
		"$.items[*].name",
		"$.arr[0].field",
		"$.foo[*]",
	}
	for _, path := range wildcardPaths {
		cond := &predicate.SimpleCondition{
			JsonPath:     path,
			OperatorType: "EQUALS",
			Value:        "x",
		}
		_, err := spi.ConditionToFilter(cond, nil)
		if err == nil {
			t.Errorf("ConditionToFilter with path %q: expected error (non-pushdownable), got nil", path)
		}
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
// Filter.Value — leaving Values unset means BETWEEN silently never matches.
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

// TestConditionToFilter_LifecycleBetween_PopulatesValues verifies that a
// BETWEEN LifecycleCondition on a temporal meta field (creationDate)
// populates Filter.Values with the two bounds AND stamps CoerceTemporal, so
// storage-plugin BETWEEN pushdown and spi.MatchFilter can actually match.
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

// TestConditionToFilter_Array_StampsDeclaredFromFieldsMap verifies that
// arrayToFilter's positional-equality leaves stamp Filter.Declared from the
// array element's fields-map entry (recorded under the base path with a
// trailing "[*]", per the model tree's flattening convention).
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

// TestConditionToFilter_Array_DeclaredNilWhenUnresolvable verifies that
// arrayToFilter's positional leaves leave Declared nil when the array's
// element path is not present in the fields map (e.g. a nil fields map) — the
// kernel falls back to non-type-directed comparison for such leaves.
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

// TestConditionToFilter_NilFields_DegradesInconsistently pins the hazard
// ConditionToFilter's doc comment warns about, and the reason this translator
// lives in the SPI at all.
//
// The naive expectation — "no declared types means nothing matches" — is
// WRONG, and believing it is what makes the bug dangerous. The kernel only
// consults declared types where it needs a type slot to compare in, so with
// fields == nil a condition degrades in two different directions at once:
// comparison/ordering leaves annihilate to false, while string, substring and
// presence leaves evaluate normally. A mixed condition therefore returns
// wrong results whose direction depends on its boolean structure (AND drops
// rows that should match; OR admits rows a failed comparison should have
// excluded) — strictly worse than uniformly returning nothing, which at least
// looks like an anomaly.
//
// This table is the executable statement of that contract. If a future kernel
// change makes undeclared leaves uniformly fail-closed, these expectations
// move — deliberately, not by accident.
func TestConditionToFilter_NilFields_DegradesInconsistently(t *testing.T) {
	// Each op is checked by COMPARING the nil-fields answer against the
	// correctly-declared answer over three documents. That comparison is what
	// makes the test discriminating: asserting a hardcoded boolean would pass
	// coincidentally whenever false happens to be the correct answer anyway
	// (IS_NULL over a present field is exactly that trap), and would keep
	// passing through a refactor that changed the mechanism entirely.
	docs := map[string][]byte{
		"present": []byte(`{"name":"Alice"}`),
		"null":    []byte(`{"name":null}`),
		"absent":  []byte(`{}`),
	}

	cases := []struct {
		op string
		// value is the operand; for the range ops it is the two bounds.
		value any
		// annihilates records whether an empty declared set changes the
		// answer. True for leaves that need a type slot to compare in.
		annihilates bool
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

			diverged := false
			for name, doc := range docs {
				gotBare := spi.MatchFilter(bare, doc, spi.EntityMeta{})
				gotTyped := spi.MatchFilter(typed, doc, spi.EntityMeta{})
				if gotBare != gotTyped {
					diverged = true
					if !tc.annihilates {
						t.Errorf("doc %s: nil-declared=%v but typed=%v — this op must not depend on declared types",
							name, gotBare, gotTyped)
					}
				}
			}
			if tc.annihilates && !diverged {
				t.Errorf("expected an empty declared set to change the answer for %s, but it did not on any document", tc.op)
			}
		})
	}

	// The consequence, made concrete: an AND of a surviving substring leaf and
	// an annihilated equality leaf drops a document that satisfies BOTH.
	t.Run("MixedAndDropsAMatchingDocument", func(t *testing.T) {
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
		if !spi.MatchFilter(typed, doc, spi.EntityMeta{}) {
			t.Fatal("setup invariant: the document must match when declared types are supplied")
		}
		if spi.MatchFilter(bare, doc, spi.EntityMeta{}) {
			t.Error("MatchFilter = true; expected the annihilated EQUALS conjunct to drop a document that genuinely satisfies both leaves")
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
	if !spi.MatchFilter(f, data, spi.EntityMeta{}) {
		t.Error("MatchFilter = false, want true: a declared string leaf must match an equal stored value")
	}
	// And it still discriminates — it is not matching everything.
	if spi.MatchFilter(f, []byte(`{"name":"Bob"}`), spi.EntityMeta{}) {
		t.Error("MatchFilter = true for a non-equal value, want false")
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
	if !spi.MatchFilter(f, []byte(`{}`), spi.EntityMeta{State: "ACTIVE"}) {
		t.Error("MatchFilter = false, want true: a meta leaf must match with a nil fields map")
	}
	if spi.MatchFilter(f, []byte(`{}`), spi.EntityMeta{State: "LOCKED"}) {
		t.Error("MatchFilter = true for a non-equal state, want false")
	}
}

// TestConditionToFilter_FieldsLookupNormalisesPath pins that a condition's
// jsonPath resolves against the FieldsMap whichever way it is spelled.
//
// FieldsMap keys are canonically "$."-prefixed; a condition may omit the
// prefix, and callers' path validation normalises before checking, so an
// unprefixed path arrives here as a known field. Looking it up raw silently
// yielded no declared types, and the type-directed kernel turns a comparison
// leaf with no declared type into a permanent non-match — a field that exists
// and holds matching data answers with an empty page.
//
// An earlier revision of this file asserted the opposite and called it "a
// deliberate, load-bearing asymmetry inherited verbatim from the engine". It
// was neither deliberate nor load-bearing: it was a defect introduced upstream
// on 2026-07-25 and fixed there in Cyoda/cyoda-go#490. Behaviour inferred from
// code is not intent.
func TestConditionToFilter_FieldsLookupNormalisesPath(t *testing.T) {
	fields := map[string]spi.FieldDescriptor{
		"$.age":     {Path: "$.age", Types: []spi.DataType{spi.Long}},
		"$.when":    {Path: "$.when", Types: []spi.DataType{spi.ZonedDateTime}},
		"$.tags[*]": {Path: "$.tags[*]", Types: []spi.DataType{spi.String}, IsArray: true},
	}

	for _, path := range []string{"age", "$.age"} {
		t.Run("declared/"+path, func(t *testing.T) {
			c := &predicate.SimpleCondition{JsonPath: path, OperatorType: "EQUALS", Value: 30}
			f, err := spi.ConditionToFilter(c, fields)
			if err != nil {
				t.Fatalf("ConditionToFilter: %v", err)
			}
			if len(f.Declared) != 1 || f.Declared[0] != spi.Long {
				t.Errorf("Declared = %v, want [LONG]: %q must resolve against key %q", f.Declared, path, "$.age")
			}
		})
	}

	// Coercion is looked up with the same key and had the same defect: a
	// declared-temporal field reached without the prefix was stamped
	// CoerceNone, so SQL planners compared it as text rather than as an instant.
	for _, path := range []string{"when", "$.when"} {
		t.Run("coercion/"+path, func(t *testing.T) {
			c := &predicate.SimpleCondition{JsonPath: path, OperatorType: "GREATER_THAN", Value: "2020-01-01T00:00:00Z"}
			f, err := spi.ConditionToFilter(c, fields)
			if err != nil {
				t.Fatalf("ConditionToFilter: %v", err)
			}
			if f.Coercion != spi.CoerceTemporal {
				t.Errorf("Coercion = %v, want CoerceTemporal for %q", f.Coercion, path)
			}
		})
	}

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
	t.Run("AllNilArrayYieldsNilChildren", func(t *testing.T) {
		f, err := spi.ConditionToFilter(&predicate.ArrayCondition{
			JsonPath: "$.arr", Values: []any{nil, nil},
		}, nil)
		if err != nil {
			t.Fatalf("ConditionToFilter: %v", err)
		}
		if f.Op != spi.FilterAnd {
			t.Fatalf("Op = %s, want and", f.Op)
		}
		if f.Children != nil {
			t.Errorf("Children = %#v, want nil (the all-nil array tautology)", f.Children)
		}
		if !spi.MatchFilter(f, []byte(`{}`), spi.EntityMeta{}) {
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
