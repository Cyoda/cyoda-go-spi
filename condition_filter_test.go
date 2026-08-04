package spi_test

import (
	"reflect"
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

func TestConditionToFilter_UnknownOperator(t *testing.T) {
	cond := &predicate.SimpleCondition{
		JsonPath:     "$.field",
		OperatorType: "SOME_UNKNOWN_OP",
		Value:        "val",
	}
	f, err := spi.ConditionToFilter(cond, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Unknown operators map to matches_regex to force post-filtering.
	if f.Op != spi.FilterMatchesRegex {
		t.Errorf("Op = %s, want matches_regex for unknown op", f.Op)
	}
	if got := spi.MapOperator("SOME_UNKNOWN_OP"); got != spi.FilterMatchesRegex {
		t.Errorf("MapOperator(unknown) = %s, want matches_regex", got)
	}
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
// Op: spi.FilterNotContains — NOT a matches_regex leaf. A regression here
// makes the case-sensitive negatives fall through MapOperator's default case
// to FilterMatchesRegex, silently mistranslating the condition into a regex
// match on Searcher (sqlite/postgres) backends.
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

// TestConditionToFilter_RawVsNormalisedFieldsLookup pins a deliberate,
// load-bearing asymmetry inherited verbatim from the engine: simple/data
// leaves look the fields map up with the RAW JsonPath, while array leaves
// normalise first (via the "$."-prefixing arrayElementPath).
//
// The asymmetry is NOT tidy, and that is exactly why it needs a test. Every
// other fields-map case in this file uses a "$."-prefixed path, where raw and
// normalised lookups agree — so "helpfully" normalising the simple-leaf lookup
// would keep the whole suite green while silently changing which entities
// match for unprefixed paths. predicate parsing does not normalise jsonPath,
// so unprefixed paths do reach here.
//
// If this behaviour is ever changed, it must be changed on purpose, upstream
// and here together, with the engine's own filter_translate_test.go moving in
// lockstep — not as a drive-by cleanup.
func TestConditionToFilter_RawVsNormalisedFieldsLookup(t *testing.T) {
	t.Run("SimpleLeafUsesRawPath", func(t *testing.T) {
		// The map is keyed canonically; the condition is not prefixed.
		fields := map[string]spi.FieldDescriptor{
			"$.age": {Path: "$.age", Types: []spi.DataType{spi.Long}},
		}
		c := &predicate.SimpleCondition{JsonPath: "age", OperatorType: "EQUALS", Value: 30}
		f, err := spi.ConditionToFilter(c, fields)
		if err != nil {
			t.Fatalf("ConditionToFilter: %v", err)
		}
		if f.Declared != nil {
			t.Errorf("Declared = %v, want nil: the raw path %q must NOT resolve against key %q",
				f.Declared, "age", "$.age")
		}
		if f.Coercion != spi.CoerceNone {
			t.Errorf("Coercion = %v, want CoerceNone for an unresolved raw path", f.Coercion)
		}
	})

	t.Run("ArrayLeafNormalisesPath", func(t *testing.T) {
		// Same shape, but the array branch normalises and appends "[*]",
		// so an unprefixed condition path DOES resolve.
		fields := map[string]spi.FieldDescriptor{
			"$.tags[*]": {Path: "$.tags[*]", Types: []spi.DataType{spi.String}},
		}
		c := &predicate.ArrayCondition{JsonPath: "tags", Values: []any{"a"}}
		f, err := spi.ConditionToFilter(c, fields)
		if err != nil {
			t.Fatalf("ConditionToFilter: %v", err)
		}
		if len(f.Declared) != 1 || f.Declared[0] != spi.String {
			t.Errorf("Declared = %v, want [String]: the array path must normalise to %q",
				f.Declared, "$.tags[*]")
		}
	})
}

// TestConditionToFilter_EmptyGroupIdentityEncodings pins the nil-vs-empty
// distinction on Filter.Children for the two "empty group" shapes.
//
// It is wire-visible, not cosmetic: Filter.Children carries no json tag, so a
// nil slice marshals to null and an empty slice to []. A backend that
// round-trips a Filter, or that reads "AND with no children" as "match
// nothing" rather than the tautology MatchFilter implements, inverts the
// result set. Asserting only len()==0 would let either encoding pass.
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

// TestLookupOperator pins the safe operator-validation form callers are told
// to use before ConditionToFilter.
//
// The hazard it exists to close: MapOperator maps ANY unrecognised name to
// FilterMatchesRegex, so a misspelling translates without error and then
// evaluates as a regular expression. The engine never sees this because it
// validates conditions first; a self-executing backend calling the SPI
// directly has no such gate, which is exactly the caller this relocation
// serves.
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
}
