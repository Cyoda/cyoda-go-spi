package spi_test

import (
	"encoding/json"
	"testing"
	"time"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

func meta(id, state string) spi.EntityMeta { return spi.EntityMeta{ID: id, State: state} }

func mustJSONFilter(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	return b
}

// --- Brief's explicit cases ---

func TestMatchFilter_ZeroValueMatchesAll(t *testing.T) {
	if !spi.MatchFilter(spi.Filter{}, []byte(`{"a":1}`), meta("e1", "S")) {
		t.Fatal("zero-value filter must match all")
	}
}

func TestMatchFilter_EmptyAndIsTrue_EmptyOrIsFalse(t *testing.T) {
	if !spi.MatchFilter(spi.Filter{Op: spi.FilterAnd}, []byte(`{}`), meta("e1", "S")) {
		t.Fatal("empty AND is identity true")
	}
	if spi.MatchFilter(spi.Filter{Op: spi.FilterOr}, []byte(`{}`), meta("e1", "S")) {
		t.Fatal("empty OR is identity false")
	}
}

func TestMatchFilter_EqAndContainsAndMeta(t *testing.T) {
	data := []byte(`{"name":"alpha","n":7}`)
	eq := spi.Filter{Op: spi.FilterEq, Source: spi.SourceData, Path: "name", Value: "alpha"}
	if !spi.MatchFilter(eq, data, meta("e1", "S")) {
		t.Fatal("eq should match")
	}
	gt := spi.Filter{Op: spi.FilterGt, Source: spi.SourceData, Path: "n", Value: 3}
	if !spi.MatchFilter(gt, data, meta("e1", "S")) {
		t.Fatal("gt numeric should match")
	}
	mstate := spi.Filter{Op: spi.FilterEq, Source: spi.SourceMeta, Path: "state", Value: "ACTIVE"}
	if !spi.MatchFilter(mstate, data, meta("e1", "ACTIVE")) {
		t.Fatal("meta state eq should match")
	}
}

// --- Ported from cyoda-go internal/match/match_filter_test.go ---

func TestMatchFilter_EqString(t *testing.T) {
	data := mustJSONFilter(t, map[string]any{"variantId": "v1"})
	f := spi.Filter{
		Op:     spi.FilterEq,
		Path:   "variantId",
		Source: spi.SourceData,
		Value:  "v1",
	}
	if !spi.MatchFilter(f, data, spi.EntityMeta{}) {
		t.Fatalf("expected MatchFilter to be true for matching data")
	}
	f.Value = "v2"
	if spi.MatchFilter(f, data, spi.EntityMeta{}) {
		t.Fatalf("expected MatchFilter to be false for non-matching data")
	}
}

func TestMatchFilter_EmptyFilterMatchesAll(t *testing.T) {
	data := mustJSONFilter(t, map[string]any{"x": 1})
	if !spi.MatchFilter(spi.Filter{}, data, spi.EntityMeta{}) {
		t.Fatalf("zero-value Filter should match all")
	}
}

func TestMatchFilter_StateEq(t *testing.T) {
	f := spi.Filter{
		Op:     spi.FilterEq,
		Path:   "state",
		Source: spi.SourceMeta,
		Value:  "available",
	}
	if !spi.MatchFilter(f, nil, spi.EntityMeta{State: "available"}) {
		t.Fatalf("expected state match")
	}
	if spi.MatchFilter(f, nil, spi.EntityMeta{State: "shipped"}) {
		t.Fatalf("expected state non-match")
	}
}

func TestMatchFilter_Ne(t *testing.T) {
	data := mustJSONFilter(t, map[string]any{"variantId": "v1"})
	f := spi.Filter{
		Op:     spi.FilterNe,
		Path:   "variantId",
		Source: spi.SourceData,
		Value:  "v2",
	}
	if !spi.MatchFilter(f, data, spi.EntityMeta{}) {
		t.Fatalf("expected Ne to be true for different value")
	}
	f.Value = "v1"
	if spi.MatchFilter(f, data, spi.EntityMeta{}) {
		t.Fatalf("expected Ne to be false for same value")
	}
}

func TestMatchFilter_NumericOrdering(t *testing.T) {
	data := mustJSONFilter(t, map[string]any{"qty": 42})
	cases := []struct {
		name string
		op   spi.FilterOp
		val  any
		want bool
	}{
		{"gt true", spi.FilterGt, 10, true},
		{"gt false", spi.FilterGt, 100, false},
		{"gte equal", spi.FilterGte, 42, true},
		{"lt true", spi.FilterLt, 100, true},
		{"lt false", spi.FilterLt, 10, false},
		{"lte equal", spi.FilterLte, 42, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := spi.Filter{Op: tc.op, Path: "qty", Source: spi.SourceData, Value: tc.val}
			if got := spi.MatchFilter(f, data, spi.EntityMeta{}); got != tc.want {
				t.Fatalf("op=%s val=%v: got %v want %v", tc.op, tc.val, got, tc.want)
			}
		})
	}
}

func TestMatchFilter_IsNullAndNotNull(t *testing.T) {
	data := mustJSONFilter(t, map[string]any{"a": "x"})

	missing := spi.Filter{Op: spi.FilterIsNull, Path: "b", Source: spi.SourceData}
	if !spi.MatchFilter(missing, data, spi.EntityMeta{}) {
		t.Fatalf("expected IsNull true for missing field")
	}

	present := spi.Filter{Op: spi.FilterIsNull, Path: "a", Source: spi.SourceData}
	if spi.MatchFilter(present, data, spi.EntityMeta{}) {
		t.Fatalf("expected IsNull false for present field")
	}

	notNull := spi.Filter{Op: spi.FilterNotNull, Path: "a", Source: spi.SourceData}
	if !spi.MatchFilter(notNull, data, spi.EntityMeta{}) {
		t.Fatalf("expected NotNull true for present field")
	}
	missingNotNull := spi.Filter{Op: spi.FilterNotNull, Path: "b", Source: spi.SourceData}
	if spi.MatchFilter(missingNotNull, data, spi.EntityMeta{}) {
		t.Fatalf("expected NotNull false for missing field")
	}
}

func TestMatchFilter_AndGroup(t *testing.T) {
	data := mustJSONFilter(t, map[string]any{"variantId": "v1", "qty": 5})
	f := spi.Filter{
		Op: spi.FilterAnd,
		Children: []spi.Filter{
			{Op: spi.FilterEq, Path: "variantId", Source: spi.SourceData, Value: "v1"},
			{Op: spi.FilterGt, Path: "qty", Source: spi.SourceData, Value: 1},
		},
	}
	if !spi.MatchFilter(f, data, spi.EntityMeta{}) {
		t.Fatalf("expected AND to be true when all children match")
	}

	f.Children[1].Value = 100
	if spi.MatchFilter(f, data, spi.EntityMeta{}) {
		t.Fatalf("expected AND to be false when one child fails")
	}
}

func TestMatchFilter_OrGroup(t *testing.T) {
	data := mustJSONFilter(t, map[string]any{"variantId": "v1"})
	f := spi.Filter{
		Op: spi.FilterOr,
		Children: []spi.Filter{
			{Op: spi.FilterEq, Path: "variantId", Source: spi.SourceData, Value: "vX"},
			{Op: spi.FilterEq, Path: "variantId", Source: spi.SourceData, Value: "v1"},
		},
	}
	if !spi.MatchFilter(f, data, spi.EntityMeta{}) {
		t.Fatalf("expected OR to be true when one child matches")
	}

	f.Children[1].Value = "vY"
	if spi.MatchFilter(f, data, spi.EntityMeta{}) {
		t.Fatalf("expected OR to be false when no children match")
	}
}

func TestMatchFilter_StringOps(t *testing.T) {
	data := mustJSONFilter(t, map[string]any{"name": "Cyoda-Go"})
	cases := []struct {
		name string
		op   spi.FilterOp
		val  string
		want bool
	}{
		{"contains hit", spi.FilterContains, "oda", true},
		{"contains miss", spi.FilterContains, "zzz", false},
		{"starts hit", spi.FilterStartsWith, "Cy", true},
		{"starts miss", spi.FilterStartsWith, "Go", false},
		{"ends hit", spi.FilterEndsWith, "Go", true},
		{"ends miss", spi.FilterEndsWith, "Cy", false},
		{"like hit", spi.FilterLike, "Cy%Go", true},
		{"like underscore", spi.FilterLike, "Cyoda_Go", true},
		{"like miss", spi.FilterLike, "Zzz%", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := spi.Filter{Op: tc.op, Path: "name", Source: spi.SourceData, Value: tc.val}
			if got := spi.MatchFilter(f, data, spi.EntityMeta{}); got != tc.want {
				t.Fatalf("op=%s val=%q: got %v want %v", tc.op, tc.val, got, tc.want)
			}
		})
	}
}

func TestMatchFilter_NestedAndOr(t *testing.T) {
	data := mustJSONFilter(t, map[string]any{"variantId": "v1", "qty": 5, "color": "red"})
	// (variantId == v1) AND (qty > 100 OR color == "red")
	f := spi.Filter{
		Op: spi.FilterAnd,
		Children: []spi.Filter{
			{Op: spi.FilterEq, Path: "variantId", Source: spi.SourceData, Value: "v1"},
			{
				Op: spi.FilterOr,
				Children: []spi.Filter{
					{Op: spi.FilterGt, Path: "qty", Source: spi.SourceData, Value: 100},
					{Op: spi.FilterEq, Path: "color", Source: spi.SourceData, Value: "red"},
				},
			},
		},
	}
	if !spi.MatchFilter(f, data, spi.EntityMeta{}) {
		t.Fatalf("expected nested AND/OR to match")
	}
}

func TestMatchFilter_MetaOtherFields(t *testing.T) {
	metaVal := spi.EntityMeta{
		ID:               "ent-1",
		State:            "available",
		Version:          7,
		CreationDate:     time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		LastModifiedDate: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC),
		ChangeType:       "UPDATED",
	}

	cases := []struct {
		name string
		path string
		val  any
		want bool
	}{
		{"entity_id match", "entity_id", "ent-1", true},
		{"entity_id miss", "entity_id", "ent-2", false},
		{"change_type match", "change_type", "UPDATED", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := spi.Filter{Op: spi.FilterEq, Path: tc.path, Source: spi.SourceMeta, Value: tc.val}
			if got := spi.MatchFilter(f, nil, metaVal); got != tc.want {
				t.Fatalf("path=%s: got %v want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestMatchFilter_EmptyAndGroupIsTrue(t *testing.T) {
	// An empty AND is the identity element — tautology.
	f := spi.Filter{Op: spi.FilterAnd}
	if !spi.MatchFilter(f, nil, spi.EntityMeta{}) {
		t.Fatalf("expected empty AND to be true (tautology)")
	}
}

func TestMatchFilter_EmptyOrGroupIsFalse(t *testing.T) {
	// An empty OR is the identity element for OR — false.
	// Op is explicitly FilterOr, so the zero-value-Filter early-out (Op == "")
	// is not triggered and the group evaluator runs over zero children.
	f := spi.Filter{Op: spi.FilterOr, Children: []spi.Filter{}}
	if spi.MatchFilter(f, nil, spi.EntityMeta{}) {
		t.Fatalf("expected empty OR to be false")
	}
}

// --- Additional coverage for ops not exercised above: Between, case-insensitive
// variants, and MatchesRegex (which exercises the ported opMatchesPattern helper). ---

func TestMatchFilter_Between(t *testing.T) {
	data := mustJSONFilter(t, map[string]any{"qty": 42})
	f := spi.Filter{Op: spi.FilterBetween, Path: "qty", Source: spi.SourceData, Values: []any{10, 200}}
	if !spi.MatchFilter(f, data, spi.EntityMeta{}) {
		t.Fatalf("expected 42 to be between 10 and 200")
	}
	f.Values = []any{100, 200}
	if spi.MatchFilter(f, data, spi.EntityMeta{}) {
		t.Fatalf("expected 42 to not be between 100 and 200")
	}
}

func TestMatchFilter_CaseInsensitiveOps(t *testing.T) {
	data := mustJSONFilter(t, map[string]any{"color": "Red"})
	cases := []struct {
		name string
		op   spi.FilterOp
		val  string
		want bool
	}{
		{"ieq match", spi.FilterIEq, "RED", true},
		{"ieq miss", spi.FilterIEq, "BLUE", false},
		{"ine match", spi.FilterINe, "BLUE", true},
		{"ine miss", spi.FilterINe, "RED", false},
		{"icontains match", spi.FilterIContains, "ED", true},
		{"inot_contains match", spi.FilterINotContains, "ZZ", true},
		{"istarts_with match", spi.FilterIStartsWith, "RE", true},
		{"inot_starts_with match", spi.FilterINotStartsWith, "BL", true},
		{"iends_with match", spi.FilterIEndsWith, "ED", true},
		{"inot_ends_with match", spi.FilterINotEndsWith, "ZZ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := spi.Filter{Op: tc.op, Path: "color", Source: spi.SourceData, Value: tc.val}
			if got := spi.MatchFilter(f, data, spi.EntityMeta{}); got != tc.want {
				t.Fatalf("op=%s val=%q: got %v want %v", tc.op, tc.val, got, tc.want)
			}
		})
	}
}

func TestMatchFilter_MatchesRegex(t *testing.T) {
	data := mustJSONFilter(t, map[string]any{"name": "Cyoda-Go"})
	hit := spi.Filter{Op: spi.FilterMatchesRegex, Path: "name", Source: spi.SourceData, Value: "^Cyoda-.*$"}
	if !spi.MatchFilter(hit, data, spi.EntityMeta{}) {
		t.Fatalf("expected regex to match")
	}
	miss := spi.Filter{Op: spi.FilterMatchesRegex, Path: "name", Source: spi.SourceData, Value: "^Zzz.*$"}
	if spi.MatchFilter(miss, data, spi.EntityMeta{}) {
		t.Fatalf("expected regex to not match")
	}
}

func TestMatchFilter_UnsupportedOpIsNonMatch(t *testing.T) {
	f := spi.Filter{Op: spi.FilterOp("bogus"), Path: "x", Source: spi.SourceData}
	if spi.MatchFilter(f, []byte(`{"x":1}`), spi.EntityMeta{}) {
		t.Fatalf("expected unsupported op to be a non-match, not a panic or true")
	}
}
