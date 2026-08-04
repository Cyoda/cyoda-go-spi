package spi_test

import (
	"testing"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

func TestClassifyType(t *testing.T) {
	cases := []struct {
		name  string
		in    []spi.DataType
		want  spi.OrderKind
		isErr bool
	}{
		{"int", []spi.DataType{spi.Integer}, spi.OrderNumeric, false},
		{"double", []spi.DataType{spi.Double}, spi.OrderNumeric, false},
		{"numeric union same class", []spi.DataType{spi.Integer, spi.Long}, spi.OrderNumeric, false},
		{"string", []spi.DataType{spi.String}, spi.OrderText, false},
		{"uuid", []spi.DataType{spi.UUIDType}, spi.OrderText, false},
		{"localdate is temporal", []spi.DataType{spi.LocalDate}, spi.OrderTemporal, false},
		{"localdatetime is temporal", []spi.DataType{spi.LocalDateTime}, spi.OrderTemporal, false},
		{"localtime is temporal", []spi.DataType{spi.LocalTime}, spi.OrderTemporal, false},
		{"zoneddatetime is temporal", []spi.DataType{spi.ZonedDateTime}, spi.OrderTemporal, false},
		{"year is temporal", []spi.DataType{spi.Year}, spi.OrderTemporal, false},
		{"yearmonth is temporal", []spi.DataType{spi.YearMonth}, spi.OrderTemporal, false},
		{"bool", []spi.DataType{spi.Boolean}, spi.OrderBool, false},
		{"nullable string", []spi.DataType{spi.String, spi.Null}, spi.OrderText, false},
		{"bytearray rejected", []spi.DataType{spi.ByteArray}, 0, true},
		{"disagreeing union rejected", []spi.DataType{spi.Integer, spi.String}, 0, true},
		{"null only rejected", []spi.DataType{spi.Null}, 0, true},
		{"empty rejected", nil, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := spi.ClassifyType(c.in)
			if c.isErr {
				if err == nil {
					t.Fatalf("want error, got Kind=%v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("Kind = %v, want %v", got, c.want)
			}
		})
	}
}

// TestClassifyTypesFold_TemporalFoldedToText exercises the shared unification
// core through a caller-supplied fold, the shape the engine's ORDER-BY
// classification uses: it folds temporal subtypes onto OrderText, decoupling
// the SORT path from the FILTER path (which keeps OrderTemporal — do not
// change ClassifyType). A data-temporal field is stored as its bare ISO-8601
// string, and ISO-8601 lexical order IS chronological order and is
// byte-identical across every backend: memory bytes.Compare (OrderText),
// postgres COLLATE "C", sqlite COLLATE BINARY. Sorting it as OrderTemporal
// would instead demand an epoch-ms normalization the bare stored subtype
// cannot supply (offset-less "2024-09-09"/"2024"), which each backend degrades
// differently — memory ties on Num=0, postgres yields NULL, sqlite coerces
// leading digits — producing three divergent orderings (and, with no residual,
// a wrong pushed LIMIT/OFFSET page).
//
// Because temporal folds to OrderText, a polymorphic [String, LocalDate] field
// unifies to OrderText and sorts lexically rather than erroring on mixed
// class, while a genuinely inconsistent mix (numeric vs temporal/text) still
// errors.
func TestClassifyTypesFold_TemporalFoldedToText(t *testing.T) {
	fold := func(k spi.OrderKind) spi.OrderKind {
		if k == spi.OrderTemporal {
			return spi.OrderText
		}
		return k
	}
	cases := []struct {
		name  string
		in    []spi.DataType
		want  spi.OrderKind
		isErr bool
	}{
		{"localdate sorts as text", []spi.DataType{spi.LocalDate}, spi.OrderText, false},
		{"localdatetime sorts as text", []spi.DataType{spi.LocalDateTime}, spi.OrderText, false},
		{"year sorts as text", []spi.DataType{spi.Year}, spi.OrderText, false},
		{"zoneddatetime sorts as text", []spi.DataType{spi.ZonedDateTime}, spi.OrderText, false},
		{"string stays text", []spi.DataType{spi.String}, spi.OrderText, false},
		{"integer stays numeric", []spi.DataType{spi.Integer}, spi.OrderNumeric, false},
		{"bool stays bool", []spi.DataType{spi.Boolean}, spi.OrderBool, false},
		{"nullable localdate sorts as text", []spi.DataType{spi.LocalDate, spi.Null}, spi.OrderText, false},
		{"mixed string+localdate unifies to text", []spi.DataType{spi.String, spi.LocalDate}, spi.OrderText, false},
		{"mixed localdate+string unifies to text", []spi.DataType{spi.LocalDate, spi.String}, spi.OrderText, false},
		{"mixed numeric+temporal still errors", []spi.DataType{spi.Integer, spi.LocalDate}, 0, true},
		{"mixed numeric+string still errors", []spi.DataType{spi.Integer, spi.String}, 0, true},
		{"bytearray rejected", []spi.DataType{spi.ByteArray}, 0, true},
		{"null only rejected", []spi.DataType{spi.Null}, 0, true},
		{"empty rejected", nil, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := spi.ClassifyTypesFold(c.in, fold)
			if c.isErr {
				if err == nil {
					t.Fatalf("want error, got Kind=%v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("Kind = %v, want %v", got, c.want)
			}
		})
	}
}

// TestClassifyTypesFold_NilFoldIsClassifyType pins the documented equivalence
// ClassifyTypesFold(t, nil) == ClassifyType(t): a caller passing no fold must
// get the unfolded FILTER-path classification, temporal included.
func TestClassifyTypesFold_NilFoldIsClassifyType(t *testing.T) {
	in := []spi.DataType{spi.LocalDate, spi.Null}
	got, err := spi.ClassifyTypesFold(in, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want, err := spi.ClassifyType(in)
	if err != nil {
		t.Fatalf("ClassifyType: unexpected error: %v", err)
	}
	if got != want || got != spi.OrderTemporal {
		t.Fatalf("ClassifyTypesFold(%v, nil) = %v, want %v (OrderTemporal)", in, got, want)
	}
}

func TestResolveMetaField(t *testing.T) {
	// All six canonical meta sort fields must resolve with exact Source, Path, and Kind.
	// A copy-paste error on Kind (e.g. Text vs Temporal) will fail this table.
	cases := []struct {
		name string
		kind spi.OrderKind
	}{
		{"state", spi.OrderText},
		{"creationDate", spi.OrderTemporal},
		{"lastUpdateTime", spi.OrderTemporal},
		{"transitionForLatestSave", spi.OrderText},
		{"transactionId", spi.OrderText},
		{"id", spi.OrderText},
	}
	for _, c := range cases {
		mf, ok := spi.ResolveMetaField(c.name)
		if !ok {
			t.Errorf("%s: should resolve, got ok=false", c.name)
			continue
		}
		if mf.Source != spi.SourceMeta {
			t.Errorf("%s: Source = %v, want SourceMeta", c.name, mf.Source)
		}
		if mf.Path != c.name {
			t.Errorf("%s: Path = %q, want %q", c.name, mf.Path, c.name)
		}
		if mf.Kind != c.kind {
			t.Errorf("%s: Kind = %v, want %v", c.name, mf.Kind, c.kind)
		}
	}

	// Negative: unknown and nested paths must not resolve.
	if _, ok := spi.ResolveMetaField("nope"); ok {
		t.Fatal("unknown meta field must not resolve")
	}
	// A dotted name is not a map key — this enforces "no nested meta paths".
	if _, ok := spi.ResolveMetaField("label.position.x"); ok {
		t.Fatal("nested meta path must not resolve")
	}
}

// TestIsTemporalMetaField pins the temporal routing derived from the meta
// vocabulary — the predicate ConditionToFilter uses to decide whether a
// lifecycle leaf is stamped CoerceTemporal/[ZonedDateTime] or
// CoerceNone/[String]. It is deliberately NOT alias-aware: callers must
// canonicalize "previousTransition" first, which is why the raw alias is
// asserted false here.
func TestIsTemporalMetaField(t *testing.T) {
	cases := []struct {
		field string
		want  bool
	}{
		{"creationDate", true},
		{"lastUpdateTime", true},
		{"state", false},
		{"transitionForLatestSave", false},
		{"transactionId", false},
		{"id", false},
		{"previousTransition", false}, // alias, must be canonicalized by the caller
		{"nope", false},
	}
	for _, c := range cases {
		if got := spi.IsTemporalMetaField(c.field); got != c.want {
			t.Errorf("IsTemporalMetaField(%q) = %v, want %v", c.field, got, c.want)
		}
	}
}
