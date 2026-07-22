package spi

import "time"

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
