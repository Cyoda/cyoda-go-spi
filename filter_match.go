package spi

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/tidwall/gjson"
)

// --- Filter operand and meta-value plumbing ---
//
// The helpers below are what the prepared evaluator (prepared_filter.go)
// uses; the sqlite/postgres post-filter steps reach them transitively
// through spi.Prepare/Match rather than calling them directly. The one
// direct external sharer of OperandString is the consuming repo's
// internal/match/operators.go. Drift between them would silently change
// grouped-stats results across backends; TestSqliteEvaluateFilter_DelegatesToKernel
// in cyoda-go pins the contract across the module boundary.

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
