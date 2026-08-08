package spi

import (
	"encoding/json"
	"time"
)

// ParseTemporalMillis parses an offset-bearing RFC3339 timestamp to floored
// epoch-milliseconds. Returns ok=false for any input that is not full RFC3339
// with an explicit offset (Z or ±hh:mm). The mandatory offset makes the value an
// absolute instant — which is what lets the SQL cyoda_epoch_millis be IMMUTABLE.
// Shared kernel: called by internal/match, spi.PreparedFilter.Match, and the
// SQL planners (to precompute operands). Do not duplicate this logic — it is
// the single home for the temporal-scalar rule.
func ParseTemporalMillis(s string) (int64, bool) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}

// CompareTemporal is the single per-operator temporal decision for the
// single-sided comparison ops, shared by both Go evaluators. storedOK=false
// (stored value not a valid instant) → excluded for positive ops, vacuously
// true for NE. cmpMs is the operand instant; cmpOK is false only if the operand
// failed to parse (validation makes this unreachable for validated callers;
// evaluators still degrade safely). Range ops (BETWEEN / BETWEEN_INCLUSIVE) do
// not route through this helper — eval_leaf.go's precise-range path handles them.
func CompareTemporal(op FilterOp, storedMs int64, storedOK bool, cmpMs int64, cmpOK bool) bool {
	if !storedOK || !cmpOK {
		return op == FilterNe // vacuous-true for NE, exclude otherwise
	}
	switch op {
	case FilterEq:
		return storedMs == cmpMs
	case FilterNe:
		return storedMs != cmpMs
	case FilterGt:
		return storedMs > cmpMs
	case FilterLt:
		return storedMs < cmpMs
	case FilterGte:
		return storedMs >= cmpMs
	case FilterLte:
		return storedMs <= cmpMs
	}
	return false
}

// NumericFloat coerces genuine numeric Go types to float64. It deliberately does
// NOT parse strings — this is the canonical numeric-leaf coercion both evaluators
// use. Mirrors the sqlite plugin's toFloat64.
func NumericFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}
