package spi

import (
	"fmt"
	"strconv"
	"strings"
)

// PathHop is one segment of a parsed filter path: a field name, optionally
// followed by one or more array subscripts when the field addresses a JSON
// array.
type PathHop struct {
	Name string
	Subs []PathSub
}

// PathSub is one bracketed subscript following a PathHop's name: either the
// wildcard "[*]" (Wildcard true, Index unused) or a non-negative decimal
// index "[N]" (Wildcard false, Index the parsed value).
type PathSub struct {
	Wildcard bool
	Index    int
}

// ParseFilterPath parses a plugin-facing filter path — the spelling a
// [Filter.Path] or [OrderSpec.Path] carries, with no "$." leader — into its
// hops.
//
// Grammar:
//
//	path      = "" / hop ( "." hop )*
//	hop       = name subscript*
//	name      = 1*( ALPHA / DIGIT / "_" / "-" )        ; ASCII only
//	subscript = "[" ( "*" / 1*DIGIT ) "]"
//
// An empty path parses to a nil hop slice and is valid: the tree operators
// (AND/OR/NOT) carry one instead of a leaf condition, and their Path is
// always empty.
//
// This is the same grammar scanWirePathBody enforces for the wire jsonPath
// form, minus the "$." leader a wire path carries and this one does not —
// callers that hold a wire path strip the leader (or call
// [ConditionToFilter], which does that translation) before reaching this
// grammar.
func ParseFilterPath(p string) ([]PathHop, error) {
	if p == "" {
		return nil, nil
	}
	var hops []PathHop
	i, n := 0, len(p)
	for {
		nameStart := i
		for i < n && isPathNameByte(p[i]) {
			i++
		}
		if i == nameStart {
			if i == n {
				return nil, invalidFilterPathError(p, "ends in a trailing dot")
			}
			switch p[i] {
			case '.':
				return nil, invalidFilterPathError(p, "contains an empty path segment")
			case '[':
				return nil, invalidFilterPathError(p, "has an array subscript with no field name before it")
			case ']':
				return nil, invalidFilterPathError(p, `contains an unmatched "]"`)
			default:
				return nil, invalidFilterPathError(p, disallowedCharReason(p[i:]))
			}
		}
		hop := PathHop{Name: p[nameStart:i]}
		for i < n && p[i] == '[' {
			rel := strings.IndexByte(p[i:], ']')
			if rel < 0 {
				return nil, invalidFilterPathError(p, "has an unclosed array subscript")
			}
			inner := p[i+1 : i+rel]
			sub, ok := parsePathSub(inner)
			if !ok {
				return nil, invalidFilterPathError(p, fmt.Sprintf(
					"has an unsupported array subscript %q; only the wildcard [*] and a non-negative index (e.g. [0]) are supported",
					"["+inner+"]"))
			}
			hop.Subs = append(hop.Subs, sub)
			i += rel + 1
		}
		hops = append(hops, hop)
		if i == n {
			return hops, nil
		}
		switch p[i] {
		case '.':
			i++
			if i == n {
				return nil, invalidFilterPathError(p, "ends in a trailing dot")
			}
		case ']':
			return nil, invalidFilterPathError(p, `contains an unmatched "]"`)
		default:
			// The subscript loop above already consumed every "[...]" group
			// immediately following the name, so a leftover '[' cannot reach
			// here: this is always a byte that isn't a valid separator.
			return nil, invalidFilterPathError(p, disallowedCharReason(p[i:]))
		}
	}
}

// ValidateFilterPath reports whether p is a well-formed filter path,
// discarding the parsed hops. The error, when non-nil, wraps
// [ErrInvalidFilterPath].
func ValidateFilterPath(p string) error {
	_, err := ParseFilterPath(p)
	return err
}

// parsePathSub parses the text between "[" and "]" into a PathSub. It accepts
// exactly two forms: the wildcard "*", or a non-negative decimal index (no
// sign, no whitespace, no exponent, digits only).
func parsePathSub(inner string) (PathSub, bool) {
	if inner == "*" {
		return PathSub{Wildcard: true}, true
	}
	if !IsArrayIndex(inner) {
		return PathSub{}, false
	}
	idx, err := strconv.Atoi(inner)
	if err != nil {
		// IsArrayIndex guarantees a non-empty ASCII-digit run, but not that it
		// fits an int: a subscript of 19+ digits overflows here. Atoi's error
		// is the cheapest way to catch that, and rejecting is the correct
		// (safe, over-rejecting) response — there is no entity array long
		// enough for such an index to ever be meaningful.
		return PathSub{}, false
	}
	return PathSub{Index: idx}, true
}

// IsArrayIndex reports whether s is a non-empty run of ASCII digits — the
// digit-class half of "is this a well-formed array index" for a filter-path
// subscript body (the text between "[" and "]", once the wildcard "*" case
// has been ruled out). It says nothing about magnitude: [parsePathSub] is the
// full predicate, checking this and then that the run fits an int, and
// [isSupportedSubscript] in condition_filter.go delegates to parsePathSub —
// not to this function directly — so the wire boundary and the parser agree
// on the complete rule, digit class and magnitude both. Every other place in
// this module and its consumers that needs the digit-class check alone
// delegates here instead of scanning its own copy: the consuming repo's
// schema.IsArrayIndex is intended to be pointed at this one.
func IsArrayIndex(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// invalidFilterPathError builds the [ErrInvalidFilterPath]-wrapping
// diagnostic for a plugin-facing filter path outside the grammar, echoing the
// offending path so the caller can correct it.
func invalidFilterPathError(path, reason string) error {
	return fmt.Errorf("%w: filter path %q %s", ErrInvalidFilterPath, path, reason)
}
