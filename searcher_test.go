package spi

import "testing"

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
