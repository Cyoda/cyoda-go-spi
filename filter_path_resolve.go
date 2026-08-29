package spi

import "github.com/tidwall/gjson"

// ResolvePath resolves a parsed filter path against a JSON document and
// returns the values the path addresses, in document order.
//
// This is the whole of the spec's addressing rule (see
// docs/cloud-parity/path-grammar.md, sections 3 and 5): the path says what it
// addresses, and the shape of the stored value never decides what the path
// meant.
//
//   - A bare hop contributes exactly one result: the value at that key,
//     whatever its shape (scalar, object, or array), never unwrapped.
//   - A "[N]" subscript contributes exactly one result: the element at that
//     index, non-existent when the value is not an array or the index is
//     past the end.
//   - A "[*]" subscript contributes one result per element, and none at all
//     when the value is not an array — it never wraps a scalar into a
//     one-element sequence.
//   - A missing key contributes one non-existent result rather than being
//     dropped, so a presence test (e.g. IS_NULL) can see it.
//
// Each hop is resolved with [gjson.Result.Get], never a joined path string:
// gjson resolves a numeric path segment against an array as an index, which
// is exactly the data-driven behaviour this function must not exhibit. A hop
// named "0" is always an object-key lookup — see [fieldResult], which guards
// Get so this holds even when the current result is itself an array (gjson's
// Get applies the same array-index rule per call, not just when a full path
// string is joined).
func ResolvePath(data []byte, hops []PathHop) []gjson.Result {
	results := []gjson.Result{gjson.ParseBytes(data)}
	for _, hop := range hops {
		next := make([]gjson.Result, 0, len(results))
		for _, r := range results {
			next = append(next, fieldResult(r, hop.Name))
		}
		results = next

		for _, sub := range hop.Subs {
			next = make([]gjson.Result, 0, len(results))
			if sub.Wildcard {
				for _, r := range results {
					if !r.IsArray() {
						continue
					}
					r.ForEach(func(_, elem gjson.Result) bool {
						next = append(next, elem)
						return true
					})
				}
			} else {
				for _, r := range results {
					next = append(next, indexResult(r, sub.Index))
				}
			}
			results = next
		}
	}
	return results
}

// fieldResult returns the object field named name inside r, or a
// non-existent result when r is not an object with that field.
//
// r.Get(name) is not safe to call unconditionally: gjson's Get resolves an
// all-digit path segment against an ARRAY as a positional index, which is
// exactly the data-driven collapse this resolver must not perform — a hop
// named "0" addresses a field literally named "0", never array element 0.
// Field access only ever applies to an object, so an array (or any
// non-object) short-circuits to non-existent before name reaches Get; a
// digit-named field on an actual object (e.g. {"0":"Z"}) still resolves
// through Get's ordinary object-key lookup, which is not path-syntax at all
// for a single flat key.
func fieldResult(r gjson.Result, name string) gjson.Result {
	if !r.IsObject() {
		return gjson.Result{}
	}
	return r.Get(name)
}

// indexResult returns the element of r at position idx when r is an array
// and idx is in range, and a non-existent (zero) gjson.Result otherwise —
// never r itself and never an element from a different shape.
func indexResult(r gjson.Result, idx int) gjson.Result {
	if !r.IsArray() {
		return gjson.Result{}
	}
	arr := r.Array()
	if idx < 0 || idx >= len(arr) {
		return gjson.Result{}
	}
	return arr[idx]
}
