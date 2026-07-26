package spi

import (
	"reflect"
	"testing"
)

// TestSearchOptions_NoOffset pins the removal of the Offset field. Direct
// search exposes no offset on any transport, and a bounded-or-fail search has
// no page to offset into. If someone re-adds it, this stops passing.
func TestSearchOptions_NoOffset(t *testing.T) {
	typ := reflect.TypeOf(SearchOptions{})
	if _, found := typ.FieldByName("Offset"); found {
		t.Fatal("SearchOptions.Offset must not exist: direct search does not paginate")
	}
}

func TestOrderKind_ZeroValueIsText(t *testing.T) {
	var k OrderKind
	if k != OrderText {
		t.Fatalf("zero OrderKind = %v, want OrderText", k)
	}
}

func TestOrderSpec_CarriesKind(t *testing.T) {
	s := OrderSpec{Path: "price", Source: SourceData, Desc: true, Kind: OrderNumeric}
	if s.Kind != OrderNumeric {
		t.Fatalf("Kind = %v, want OrderNumeric", s.Kind)
	}
}
