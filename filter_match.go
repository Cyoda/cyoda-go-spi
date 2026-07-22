package spi

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// --- Filter-based evaluation (used by Iterable/GroupedAggregator/streaming-tally) ---
//
// The helpers below mirror plugins/sqlite/post_filter.go semantics so that an
// in-process evaluator (memory Iterate, residual post-filter, streaming tally)
// produces bit-identical results to the sqlite backend's post-filter step.
// Drift between the two would silently change grouped-stats results across
// backends — see e2e/parity/MatchFilterSqliteEvaluateFilterParity (the smoke
// test that pins this contract).

// MatchFilter evaluates a Filter against an entity. Filter is the
// pushdown-friendly subset of predicate.Condition used by GroupedAggregator,
// Iterable, and the existing Searcher. Used by the memory plugin's Iterate
// to apply filters inside Next() and by the streaming-tally path when a
// pushdown leaves a residual.
//
// A zero-value filter (no Op) matches everything. An explicit empty AND
// (Op = FilterAnd with no children) is the AND identity (true). An explicit
// empty OR is the OR identity (false).
//
// Unlike Match, MatchFilter does not return an error. The pushdown contract
// guarantees ops are well-formed before they reach here; an unsupported op
// (which would only happen on a programmer error or SPI/plugin drift) is
// treated as a non-match.
func MatchFilter(f Filter, data []byte, meta EntityMeta) bool {
	// Zero-value filter (no Op) matches everything. We deliberately only
	// check Op: an explicit Op (even FilterAnd with no children) must reach
	// the group evaluator so the group identity is honored (empty AND → true,
	// empty OR → false). evalLeafFilter returns false when Source/Path are
	// also empty, so a non-empty Op with an unset Source/Path won't false-
	// positive into the "match everything" branch.
	if f.Op == "" {
		return true
	}
	return evalFilter(f, data, meta)
}

func evalFilter(f Filter, data []byte, meta EntityMeta) bool {
	switch f.Op {
	case FilterAnd:
		for _, c := range f.Children {
			if !evalFilter(c, data, meta) {
				return false
			}
		}
		return true
	case FilterOr:
		for _, c := range f.Children {
			if evalFilter(c, data, meta) {
				return true
			}
		}
		return false
	}
	return evalLeafFilter(f, data, meta)
}

// evalLeafFilter mirrors the sqlite plugin's evaluateLeaf (post_filter.go)
// but takes raw data + meta instead of *Entity so it can be called from
// inner loops without constructing an Entity wrapper.
func evalLeafFilter(f Filter, data []byte, meta EntityMeta) bool {
	// IsNull / NotNull are checked first because they care about presence,
	// not value extraction succeeding.
	switch f.Op {
	case FilterIsNull:
		_, found := extractFilterValue(f, data, meta)
		return !found
	case FilterNotNull:
		val, found := extractFilterValue(f, data, meta)
		return found && val != nil
	}

	val, found := extractFilterValue(f, data, meta)

	// For "negative" ops (Ne, INe, NotContains, NotStartsWith, NotEndsWith),
	// a missing-or-null field is vacuously true; for everything else, missing
	// short-circuits to false.
	isNegativeOp := f.Op == FilterNe ||
		f.Op == FilterINe ||
		f.Op == FilterINotContains ||
		f.Op == FilterINotStartsWith ||
		f.Op == FilterINotEndsWith
	if !found || val == nil {
		return isNegativeOp
	}

	if f.Coercion == CoerceTemporal {
		return evalTemporalLeaf(f, val) // val already extracted above; found/null handled by the earlier guard
	}

	switch f.Op {
	case FilterEq:
		return compareFilterValues(val, f.Value) == 0
	case FilterNe:
		return compareFilterValues(val, f.Value) != 0
	case FilterGt:
		return compareFilterValues(val, f.Value) > 0
	case FilterLt:
		return compareFilterValues(val, f.Value) < 0
	case FilterGte:
		return compareFilterValues(val, f.Value) >= 0
	case FilterLte:
		return compareFilterValues(val, f.Value) <= 0
	case FilterContains:
		return strings.Contains(fmt.Sprint(val), fmt.Sprint(f.Value))
	case FilterStartsWith:
		return strings.HasPrefix(fmt.Sprint(val), fmt.Sprint(f.Value))
	case FilterEndsWith:
		return strings.HasSuffix(fmt.Sprint(val), fmt.Sprint(f.Value))
	case FilterLike:
		return matchFilterLike(fmt.Sprint(val), fmt.Sprint(f.Value))
	case FilterBetween:
		if len(f.Values) < 2 {
			return false
		}
		return compareFilterValues(val, f.Values[0]) >= 0 &&
			compareFilterValues(val, f.Values[1]) <= 0
	case FilterMatchesRegex:
		ok, err := opMatchesPattern(toGjsonResult(val), f.Value)
		return err == nil && ok
	case FilterIEq:
		return strings.EqualFold(fmt.Sprint(val), fmt.Sprint(f.Value))
	case FilterINe:
		return !strings.EqualFold(fmt.Sprint(val), fmt.Sprint(f.Value))
	case FilterIContains:
		return strings.Contains(strings.ToLower(fmt.Sprint(val)), strings.ToLower(fmt.Sprint(f.Value)))
	case FilterINotContains:
		return !strings.Contains(strings.ToLower(fmt.Sprint(val)), strings.ToLower(fmt.Sprint(f.Value)))
	case FilterIStartsWith:
		return strings.HasPrefix(strings.ToLower(fmt.Sprint(val)), strings.ToLower(fmt.Sprint(f.Value)))
	case FilterINotStartsWith:
		return !strings.HasPrefix(strings.ToLower(fmt.Sprint(val)), strings.ToLower(fmt.Sprint(f.Value)))
	case FilterIEndsWith:
		return strings.HasSuffix(strings.ToLower(fmt.Sprint(val)), strings.ToLower(fmt.Sprint(f.Value)))
	case FilterINotEndsWith:
		return !strings.HasSuffix(strings.ToLower(fmt.Sprint(val)), strings.ToLower(fmt.Sprint(f.Value)))
	}
	return false
}

// extractFilterValue extracts the field value referenced by the filter.
// SourceData uses a gjson path on the entity's JSON data; SourceMeta uses
// a fixed set of metadata field names (matching the sqlite plugin's
// extractMetaValue, which is the canonical mapping for SourceMeta paths).
// Returns (value, found). found=false means the field is missing; found=true
// with value=nil means the field exists and is JSON null.
func extractFilterValue(f Filter, data []byte, meta EntityMeta) (any, bool) {
	if f.Source == SourceMeta {
		return extractFilterMetaValue(f.Path, meta)
	}
	return extractFilterDataValue(f.Path, data)
}

func extractFilterDataValue(path string, data []byte) (any, bool) {
	result := gjson.GetBytes(data, path)
	if !result.Exists() {
		return nil, false
	}
	if result.Type == gjson.Null {
		return nil, true
	}
	return result.Value(), true
}

// extractFilterMetaValue mirrors the sqlite plugin's extractMetaValue keyset
// (plugins/sqlite/post_filter.go). Keep this list in sync with that file —
// the two must agree on which meta paths are valid for a Filter.
func extractFilterMetaValue(path string, meta EntityMeta) (any, bool) {
	switch path {
	case "entity_id":
		return meta.ID, true
	case "state":
		return meta.State, true
	case "version":
		return meta.Version, true
	case "created_at":
		return timeToMicro(meta.CreationDate), true
	case "updated_at":
		return timeToMicro(meta.LastModifiedDate), true
	case "model_name":
		return meta.ModelRef.EntityName, true
	case "model_version":
		return meta.ModelRef.ModelVersion, true
	case "change_type":
		return meta.ChangeType, true
	case "transaction_id":
		return meta.TransactionID, true
	// Canonical client-name vocabulary (additive; #423). Keep the storage-key
	// cases above in sync with plugins/sqlite/post_filter.go — these new
	// cases are the client-facing names used by domain-layer Filter building.
	case "id":
		return meta.ID, true
	case "creationDate":
		return meta.CreationDate, true // time.Time (temporal)
	case "lastUpdateTime":
		return meta.LastModifiedDate, true // time.Time (temporal)
	case "transitionForLatestSave":
		return meta.TransitionForLatestSave, true
	case "transactionId":
		return meta.TransactionID, true
	default:
		return nil, false
	}
}

// evalTemporalLeaf evaluates a CoerceTemporal leaf: the stored value (already
// extracted) and the filter operand(s) are converted to floored epoch-ms and
// compared via the shared CompareTemporal dispatcher (#423 / #431 seed).
func evalTemporalLeaf(f Filter, val any) bool {
	storedMs, storedOK := toEpochMillis(val)
	if f.Op == FilterBetween {
		if len(f.Values) < 2 {
			return false
		}
		lo, lok := ParseTemporalMillis(fmt.Sprint(f.Values[0]))
		hi, hok := ParseTemporalMillis(fmt.Sprint(f.Values[1]))
		return CompareTemporal(FilterBetween, storedMs, storedOK, lo, hi, lok && hok)
	}
	op, ook := ParseTemporalMillis(fmt.Sprint(f.Value))
	return CompareTemporal(f.Op, storedMs, storedOK, op, 0, ook)
}

// toEpochMillis converts a stored leaf value to floored epoch-ms. time.Time →
// UnixMilli (meta path); RFC3339 string → ParseTemporalMillis (future #137 body
// text). Anything else is not a valid instant → ok=false (excluded per §7.1).
func toEpochMillis(v any) (int64, bool) {
	switch t := v.(type) {
	case time.Time:
		return t.UnixMilli(), true
	case string:
		return ParseTemporalMillis(t)
	}
	return 0, false
}

// timeToMicro converts a time.Time to microseconds since Unix epoch.
// Mirrors plugins/sqlite/post_filter.go timeToMicro.
//
// The t.IsZero() guard is intentional: a zero time.Time is year 1
// (0001-01-01 UTC), which UnixMicro() reports as a very large negative
// number (~-62,135,596,800,000,000), not 0. Without the guard, ordering
// ops against created_at/updated_at on a zero-time entity would silently
// classify it as "much earlier than any valid timestamp" rather than
// "unset/sentinel zero". The sqlite plugin handles this the same way.
func timeToMicro(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMicro()
}

// compareFilterValues orders two raw values. Returns <0, 0, >0 like strings.Compare.
//
// Numeric coercion intentionally does NOT parse strings — only float64/float32/
// int/int64/json.Number are treated as numeric (see the shared NumericFloat in
// temporal.go, #431 seed). This mirrors the sqlite plugin's compareValues +
// toFloat64 (plugins/sqlite/post_filter.go). The Match path (predicate.Condition)
// does parse strings via operators.go toFloat64 — keep the two helpers separate
// so the Filter path stays in lockstep with sqlite.
func compareFilterValues(a, b any) int {
	af, aok := NumericFloat(a)
	bf, bok := NumericFloat(b)
	if aok && bok {
		switch {
		case af < bf:
			return -1
		case af > bf:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
}

// matchFilterLike mirrors the sqlite plugin's matchLike (plugins/sqlite/
// post_filter.go) — byte-based, NOT rune-based. `_` matches a single byte;
// `%` matches any byte sequence; `\` escapes. Multibyte characters in the
// data string are spanned by multiple `_` pattern bytes, matching SQLite's
// default LIKE semantics. Keep in sync with the sqlite implementation —
// drift would silently disagree on LIKE patterns crossing multibyte chars.
func matchFilterLike(s, pattern string) bool {
	return matchFilterLikeHelper(s, 0, pattern, 0)
}

func matchFilterLikeHelper(s string, si int, pattern string, pi int) bool {
	for pi < len(pattern) {
		ch := pattern[pi]
		switch {
		case ch == '\\' && pi+1 < len(pattern):
			pi++
			if si >= len(s) || s[si] != pattern[pi] {
				return false
			}
			si++
			pi++
		case ch == '%':
			for pi < len(pattern) && pattern[pi] == '%' {
				pi++
			}
			if pi == len(pattern) {
				return true
			}
			for si <= len(s) {
				if matchFilterLikeHelper(s, si, pattern, pi) {
					return true
				}
				si++
			}
			return false
		case ch == '_':
			if si >= len(s) {
				return false
			}
			si++
			pi++
		default:
			if si >= len(s) || s[si] != ch {
				return false
			}
			si++
			pi++
		}
	}
	return si == len(s)
}

// toGjsonResult wraps a raw value in a gjson.Result for reuse of the
// opMatchesPattern regex helper (which takes gjson.Result). This is a thin
// shim — we encode the value as JSON, parse it, and let gjson surface it as
// a Result. Used only for regex leaf evaluation, where the per-entity cost
// is dominated by regex compile anyway.
func toGjsonResult(v any) gjson.Result {
	b, err := json.Marshal(v)
	if err != nil {
		// Fall back to a string-typed Result via fmt.Sprint.
		return gjson.Parse(fmt.Sprintf("%q", fmt.Sprint(v)))
	}
	return gjson.ParseBytes(b)
}

// opIsNull reports whether a gjson.Result represents a missing or JSON-null
// value. Ported from internal/match/operators.go — used only by
// opMatchesPattern below.
func opIsNull(actual gjson.Result) bool {
	return !actual.Exists() || actual.Type == gjson.Null
}

// opMatchesPattern reports whether actual's string representation matches
// the regex pattern in expected. Ported verbatim from
// internal/match/operators.go (the MATCHES_PATTERN operator), which
// evalLeafFilter's FilterMatchesRegex case delegates to.
func opMatchesPattern(actual gjson.Result, expected any) (bool, error) {
	if opIsNull(actual) {
		return false, nil
	}
	pattern := fmt.Sprintf("%v", expected)
	return regexp.MatchString(pattern, actual.String())
}
