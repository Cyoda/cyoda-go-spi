package spi

import (
	"encoding/json"
	"time"
)

// ParseTemporalMillis parses an offset-bearing RFC3339 timestamp to floored
// epoch-milliseconds. Returns ok=false for any input that is not full RFC3339
// with an explicit offset (Z or ±hh:mm). The mandatory offset makes the value an
// absolute instant — which is what lets the SQL cyoda_epoch_millis be IMMUTABLE.
// Shared kernel: called by internal/match, spi.MatchFilter, and the SQL planners
// (to precompute operands). Do not duplicate this logic (#431).
func ParseTemporalMillis(s string) (int64, bool) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}

// CompareTemporal is the single per-operator temporal decision, shared by both
// Go evaluators (#431). storedOK=false (stored value not a valid instant) →
// excluded for positive ops, vacuously true for NE. loMs is the (single) operand
// for non-BETWEEN ops; loMs..hiMs are the inclusive bounds for BETWEEN. loHiOK is
// false only if an operand failed to parse (validation makes this unreachable for
// validated callers; evaluators still degrade safely).
func CompareTemporal(op FilterOp, storedMs int64, storedOK bool, loMs, hiMs int64, loHiOK bool) bool {
	if !storedOK || !loHiOK {
		return op == FilterNe // vacuous-true for NE, exclude otherwise
	}
	switch op {
	case FilterEq:
		return storedMs == loMs
	case FilterNe:
		return storedMs != loMs
	case FilterGt:
		return storedMs > loMs
	case FilterLt:
		return storedMs < loMs
	case FilterGte:
		return storedMs >= loMs
	case FilterLte:
		return storedMs <= loMs
	case FilterBetween:
		return storedMs >= loMs && storedMs <= hiMs
	}
	return false
}

// NumericFloat coerces genuine numeric Go types to float64. It deliberately does
// NOT parse strings — this is the canonical numeric-leaf coercion both evaluators
// use (#423 numeric alignment / #431 seed). Mirrors the existing toFilterFloat64.
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
