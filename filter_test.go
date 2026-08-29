package spi

import "testing"

func TestFilterCoercionZeroValue(t *testing.T) {
	var f Filter
	if f.Coercion != CoerceNone {
		t.Fatalf("zero Filter.Coercion = %v, want CoerceNone", f.Coercion)
	}
}

func TestFilterPathGrammarDoc_MatchesValidator(t *testing.T) {
	// The godoc on Filter.Path is what a plugin author implements against.
	// It stated that subscripts are outside the grammar and that an array
	// position is spelled "tags.0". Both are now false, and a stale grammar
	// comment is how a backend ends up with a second, wrong validator.
	if err := ValidateFilterPath("tags[0]"); err != nil {
		t.Fatalf("tags[0] must be a legal filter path: %v", err)
	}
	if err := ValidateFilterPath("tags[*]"); err != nil {
		t.Fatalf("tags[*] must be a legal filter path: %v", err)
	}
	if err := ValidateFilterPath("obj.0"); err != nil {
		t.Fatalf("obj.0 must be a legal filter path (a field named 0): %v", err)
	}
}
