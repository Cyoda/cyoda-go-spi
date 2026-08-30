package spi

import (
	"errors"
	"testing"
)

func TestParseFilterPath_Accepts(t *testing.T) {
	cases := []struct {
		path string
		want []PathHop
	}{
		{"", nil},
		{"amount", []PathHop{{Name: "amount"}}},
		{"obj.0", []PathHop{{Name: "obj"}, {Name: "0"}}},
		{"tags[0]", []PathHop{{Name: "tags", Subs: []PathSub{{Index: 0}}}}},
		{"tags[*]", []PathHop{{Name: "tags", Subs: []PathSub{{Wildcard: true}}}}},
		{"matrix[*][1]", []PathHop{{Name: "matrix", Subs: []PathSub{{Wildcard: true}, {Index: 1}}}}},
		{"orders[*].lines[*].sku", []PathHop{
			{Name: "orders", Subs: []PathSub{{Wildcard: true}}},
			{Name: "lines", Subs: []PathSub{{Wildcard: true}}},
			{Name: "sku"},
		}},
	}
	for _, tc := range cases {
		got, err := ParseFilterPath(tc.path)
		if err != nil {
			t.Errorf("ParseFilterPath(%q): unexpected error %v", tc.path, err)
			continue
		}
		if !equalHops(got, tc.want) {
			t.Errorf("ParseFilterPath(%q) = %+v, want %+v", tc.path, got, tc.want)
		}
	}
}

func TestParseFilterPath_Rejects(t *testing.T) {
	// Every one of these must wrap ErrInvalidFilterPath: a backend that
	// interpolates a path into SQL relies on this rejection as its injection
	// guard, so a "merely unparseable" classification is not enough.
	bad := []string{
		".a", "a.", "a..b", "$.a", "a b", "a;DROP", "a/etc", "a\\b", "a|b",
		"a[", "a[0", "a]", "a].b", "[0]", "a[]", "a[-1]", "a[+1]", "a[1e2]",
		"a[0:2]", "a[0,1]", "a[?(@.x)]", "a[ 0]", "a[0 ]", "a[0]b", "a[0];DROP",
		"a[x]", "a[*]..b", "a[*].", "a'b", "a\"b", "aé",
	}
	for _, p := range bad {
		if err := ValidateFilterPath(p); err == nil {
			t.Errorf("ValidateFilterPath(%q): want error, got nil", p)
		} else if !errors.Is(err, ErrInvalidFilterPath) {
			t.Errorf("ValidateFilterPath(%q): error does not wrap ErrInvalidFilterPath: %v", p, err)
		}
	}
}

// TestParseFilterPath_SubscriptInt32Bound pins the magnitude bound at int32,
// not Go's int (int64 on every supported platform). PostgreSQL renders a
// filter path's positional index as a jsonb operand; an index above
// math.MaxInt32 has no bounded representation any in-tree backend can
// address, so it must be rejected the same way an int64-overflowing digit
// run already is — wrapping ErrInvalidFilterPath — rather than accepted and
// left to fail downstream as an ungraded 5xx.
func TestParseFilterPath_SubscriptInt32Bound(t *testing.T) {
	if _, err := ParseFilterPath("tags[2147483647]"); err != nil {
		t.Errorf("ParseFilterPath(tags[2147483647]) (int32 max): unexpected error %v", err)
	}
	for _, p := range []string{"tags[2147483648]", "tags[4294967296]", "tags[99999999999999999999]"} {
		if err := ValidateFilterPath(p); err == nil {
			t.Errorf("ValidateFilterPath(%q): want error, got nil", p)
		} else if !errors.Is(err, ErrInvalidFilterPath) {
			t.Errorf("ValidateFilterPath(%q): error does not wrap ErrInvalidFilterPath: %v", p, err)
		}
	}
}

func equalHops(a, b []PathHop) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || len(a[i].Subs) != len(b[i].Subs) {
			return false
		}
		for j := range a[i].Subs {
			if a[i].Subs[j] != b[i].Subs[j] {
				return false
			}
		}
	}
	return true
}
