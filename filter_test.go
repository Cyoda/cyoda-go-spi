package spi

import "testing"

func TestFilterCoercionZeroValue(t *testing.T) {
	var f Filter
	if f.Coercion != CoerceNone {
		t.Fatalf("zero Filter.Coercion = %v, want CoerceNone", f.Coercion)
	}
}
