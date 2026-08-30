package spi_test

import (
	"testing"
	"time"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

func ent(id string, data string) *spi.Entity {
	return &spi.Entity{Data: []byte(data), Meta: spi.EntityMeta{ID: id}}
}

func TestLessByOrder_TiebreakByEntityID(t *testing.T) {
	specs := []spi.OrderSpec{{Path: "n", Source: spi.SourceData, Kind: spi.OrderNumeric}}
	a, b := ent("aaa", `{"n":1}`), ent("bbb", `{"n":1}`)
	if !spi.LessByOrder(a, b, specs) {
		t.Fatal("equal keys must break by entity_id asc")
	}
	if spi.LessByOrder(b, a, specs) {
		t.Fatal("reversed order must not also report less")
	}
}

// TestLessByOrder_EmptyDataPathNeverResolves pins the sort-side twin of
// TestPreparedFilter_EmptyLeafPathNeverResolves (prepared_filter_test.go).
// orderLeafValue resolves s.Path through ParseFilterPath + ResolvePath, same
// as a leaf's Match. ParseFilterPath("") legitimately succeeds with a nil
// hop slice (the tree-operator convention this SPI's Filter.Path shares),
// so before this fix ResolvePath(data, nil) returned the parsed ROOT
// DOCUMENT as the sort key for an OrderSpec carrying an empty Path — every
// entity therefore "had" a value to sort by (the entity's own document),
// rather than being treated as missing (nulls-last) the way an addressless
// leaf must be.
func TestLessByOrder_EmptyDataPathNeverResolves(t *testing.T) {
	specs := []spi.OrderSpec{{Path: "", Source: spi.SourceData, Kind: spi.OrderText}}
	// z sorts before a by raw JSON text ({"n":9} < {"n":5} lexically is
	// false, so pick documents where a root-document comparison would give
	// the OPPOSITE answer to "both absent, fall through to entity_id asc":
	// entity "z" holds the lexically-SMALLER document, entity "a" the
	// lexically-larger one. If the empty path still resolved to the root
	// document, "z" would sort first (smaller document). Since it must
	// instead resolve to nothing for both, the tiebreak is entity_id asc,
	// so "a" sorts first.
	a, z := ent("a", `{"n":9}`), ent("z", `{"n":1}`)
	if !spi.LessByOrder(a, z, specs) {
		t.Fatal("an empty data path must resolve to nothing (both missing ⇒ equal ⇒ entity_id tiebreak), not the root document — expected \"a\" (by id) to sort first")
	}
	if spi.LessByOrder(z, a, specs) {
		t.Fatal("reversed order must not also report less")
	}
}

func TestLessByOrder_NullsLast(t *testing.T) {
	specs := []spi.OrderSpec{{Path: "n", Source: spi.SourceData, Kind: spi.OrderNumeric}}
	present, missing := ent("a", `{"n":5}`), ent("b", `{}`)
	if !spi.LessByOrder(present, missing, specs) {
		t.Fatal("present must sort before missing (NULLS LAST) ascending")
	}
	if spi.LessByOrder(missing, present, specs) {
		t.Fatal("missing must not sort before present ascending")
	}
	descSpecs := []spi.OrderSpec{{Path: "n", Source: spi.SourceData, Kind: spi.OrderNumeric, Desc: true}}
	if !spi.LessByOrder(present, missing, descSpecs) {
		t.Fatal("present must still sort before missing (NULLS LAST) descending")
	}
	if spi.LessByOrder(missing, present, descSpecs) {
		t.Fatal("missing must not sort before present descending")
	}
}

// TestLessByOrder_NumericSegmentIsNotAnIndex pins path-grammar.md §3/§10's
// addressing rule on the SORT surface: a bare hop named "0" is a field-name
// lookup, never an array-index shortcut, regardless of what shape the stored
// value turns out to be. gjson.GetBytes's own path syntax disagrees — it
// resolves an all-digit segment against an array receiver as a positional
// index — so a sort key that goes through gjson.GetBytes directly (bypassing
// ParseFilterPath/ResolvePath) sees "obj.0" over {"obj":["X","Y"]} as "X",
// diverging from every other resolver in the stack (spi.ResolvePath, and both
// SQL backends, which return NULL/non-existent for the same shape). Both
// present cases must therefore report ABSENT (aok=false), matching a missing
// field, not the first array element.
func TestLessByOrder_NumericSegmentIsNotAnIndex(t *testing.T) {
	specs := []spi.OrderSpec{{Path: "obj.0", Source: spi.SourceData, Kind: spi.OrderText}}
	// entity_id "a" holds the array element that alphabetically sorts AFTER
	// entity_id "z"'s. A resolver that (wrongly) treats "obj.0" as array
	// index 0 would order these by that value — "z" (element "A") before "a"
	// (element "B") — the opposite of the entity_id tiebreak. Neither entity
	// actually HAS a field literally named "0", so the correct answer is
	// "both absent under this key", which falls through to the entity_id
	// tiebreak: "a" < "z".
	a := ent("a", `{"obj":["B","Y"]}`)
	z := ent("z", `{"obj":["A","Y"]}`)
	if !spi.LessByOrder(a, z, specs) {
		t.Fatal("both absent under this key: expected entity_id tiebreak (\"a\" < \"z\"), got value-based ordering")
	}
	if spi.LessByOrder(z, a, specs) {
		t.Fatal("both absent under this key: entity_id tiebreak must not reverse")
	}
}

func TestLessByOrder_NumericAscDesc(t *testing.T) {
	small, big := ent("a", `{"n":1}`), ent("b", `{"n":2}`)
	asc := []spi.OrderSpec{{Path: "n", Source: spi.SourceData, Kind: spi.OrderNumeric}}
	if !spi.LessByOrder(small, big, asc) {
		t.Fatal("smaller numeric must sort first ascending")
	}
	if spi.LessByOrder(big, small, asc) {
		t.Fatal("bigger numeric must not sort first ascending")
	}
	desc := []spi.OrderSpec{{Path: "n", Source: spi.SourceData, Kind: spi.OrderNumeric, Desc: true}}
	if !spi.LessByOrder(big, small, desc) {
		t.Fatal("bigger numeric must sort first descending")
	}
	if spi.LessByOrder(small, big, desc) {
		t.Fatal("smaller numeric must not sort first descending")
	}
}

func TestLessByOrder_TemporalMsFloorTie(t *testing.T) {
	// Two timestamps differing only in the sub-millisecond range must compare
	// equal under the ms-floor Temporal kind and fall through to the
	// entity_id tiebreaker (matching the SQL backends' floor-to-ms ORDER BY).
	base := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	a := &spi.Entity{Meta: spi.EntityMeta{ID: "aaa", CreationDate: base.Add(100 * time.Microsecond)}}
	b := &spi.Entity{Meta: spi.EntityMeta{ID: "bbb", CreationDate: base.Add(900 * time.Microsecond)}}
	specs := []spi.OrderSpec{{Path: "creationDate", Source: spi.SourceMeta, Kind: spi.OrderTemporal}}
	if !spi.LessByOrder(a, b, specs) {
		t.Fatal("ms-floor tie must fall through to entity_id tiebreaker (a < b)")
	}
	if spi.LessByOrder(b, a, specs) {
		t.Fatal("ms-floor tie must fall through to entity_id tiebreaker (b !< a)")
	}
}

func TestLessByOrder_IDPathIgnoresKind(t *testing.T) {
	// Path="id" ordering is the engine's canonical entity-ID order — a
	// byte-wise comparison — regardless of the Kind the caller supplies.
	// "10" vs "9": numerically 9 < 10 (would put "9" first), but byte-wise
	// '1' < '9' puts "10" first. A Kind=OrderNumeric spec on the id path
	// must still produce the byte-wise answer, proving Kind is ignored.
	specs := []spi.OrderSpec{{Path: "id", Source: spi.SourceMeta, Kind: spi.OrderNumeric}}
	ten, nine := ent("10", `{}`), ent("9", `{}`)
	if !spi.LessByOrder(ten, nine, specs) {
		t.Fatal(`byte-wise order must put "10" before "9" even under Kind=OrderNumeric`)
	}
	if spi.LessByOrder(nine, ten, specs) {
		t.Fatal(`byte-wise order must not put "9" before "10" even under Kind=OrderNumeric`)
	}
}

func TestLessByOrder_TerminalEntityIDSpecNoDoubleTiebreak(t *testing.T) {
	// When the terminal spec already resolves to entity_id, no extra
	// tiebreaker clause is appended (it would be redundant, matching the SQL
	// backends' `!(last.Source == SourceMeta && last.Path == "id")` guard).
	specs := []spi.OrderSpec{{Path: "id", Source: spi.SourceMeta, Kind: spi.OrderText, Desc: true}}
	a, b := ent("aaa", `{}`), ent("bbb", `{}`)
	if spi.LessByOrder(a, b, specs) {
		t.Fatal("descending entity_id spec must sort bbb before aaa")
	}
	if !spi.LessByOrder(b, a, specs) {
		t.Fatal("descending entity_id spec must sort bbb before aaa")
	}
}
