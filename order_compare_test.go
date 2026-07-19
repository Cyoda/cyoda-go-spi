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
