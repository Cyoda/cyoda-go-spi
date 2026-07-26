package spi

import (
	"encoding/json"
	"fmt"
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

// evalLeafFilter routes a single leaf through the type-directed EvalLeaf kernel
// (eval_leaf.go), the one authoritative comparator shared with the search
// boundary. It resolves the stored value as a gjson.Result (preserving its
// precise numeric/temporal Raw form), normalizes the operand(s) to Cloud
// .asText() string form, and stamps the leaf's declared model types so the
// kernel can do type-directed comparison.
//
// IS_NULL / NOT_NULL are routed through the kernel too — it decides them purely
// from the stored Result's presence, uniformly for data and (bridged) meta.
//
// The operand was already validated at the search boundary, so a per-row expand
// error (which only happens on a genuinely malformed operand) is treated as a
// non-match rather than propagated: matched && err == nil.
func evalLeafFilter(f Filter, data []byte, meta EntityMeta) bool {
	stored := filterStoredResult(f, data, meta)
	matched, err := EvalLeafString(f.Op, OperandString(f.Value), valuesToStrings(f.Values), f.Declared, stored)
	return matched && err == nil
}

// filterStoredResult resolves the stored value referenced by the leaf as a
// gjson.Result, keeping its .Raw so the kernel can classify numerics/temporals
// precisely. A missing data path yields a non-existent Result (Exists()==false);
// SourceMeta values are bridged through metaGjsonResult. In both cases the
// kernel handles absent/null uniformly.
func filterStoredResult(f Filter, data []byte, meta EntityMeta) gjson.Result {
	if f.Source == SourceMeta {
		r, _ := metaGjsonResult(f.Path, meta)
		return r
	}
	return gjson.GetBytes(data, f.Path)
}

// metaGjsonResult bridges a SourceMeta value into a gjson.Result so the kernel
// classifies it uniformly with data values. It reuses extractFilterMetaValue for
// the canonical meta keyset, then JSON-encodes the value and parses it: a meta
// string becomes a gjson String, a numeric meta a gjson Number, and a time.Time
// an RFC3339 String the kernel's temporal branch parses (the domain stamps
// Declared=[ZonedDateTime] for temporal meta leaves).
//
// An absent meta path — or a present-but-unset zero time.Time — yields a
// non-existent Result so the kernel treats it as absent/null (IS_NULL true;
// every binary op, negatives included, a non-match). The zero-time exclusion
// preserves the invariant that an unset creationDate/lastUpdateTime is "no
// value", not a comparable ~year-1 instant.
func metaGjsonResult(path string, meta EntityMeta) (gjson.Result, bool) {
	v, found := extractFilterMetaValue(path, meta)
	if !found || v == nil {
		return gjson.Result{}, false
	}
	if t, ok := v.(time.Time); ok && t.IsZero() {
		return gjson.Result{}, false
	}
	b, err := json.Marshal(v)
	if err != nil {
		return gjson.Result{}, false
	}
	return gjson.ParseBytes(b), true
}

// OperandString normalizes a filter operand to its Cloud .asText() string form,
// the shape ExpandLeaf parses. A json.Number keeps its exact lexical form; a
// nil operand becomes the empty string (a genuinely-null binary operand was
// rejected at the search boundary).
func OperandString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case json.Number:
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}

// valuesToStrings maps each range/list operand through OperandString.
func valuesToStrings(vs []any) []string {
	if len(vs) == 0 {
		return nil
	}
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = OperandString(v)
	}
	return out
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
	// Canonical client-name vocabulary (additive). Keep the storage-key
	// cases above in sync with plugins/sqlite/post_filter.go — these
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
