package spi

import (
	"bytes"
	"time"

	"github.com/tidwall/gjson"
)

// LessByOrder is the canonical strict-less-than comparator for OrderSpec
// sequences, shared by every backend (memory, sqlite, postgres, commercial)
// so query results sort identically regardless of which plugin executed
// them. It mirrors the SQL ORDER BY built by plugins/sqlite/searcher.go and
// plugins/postgres/searcher.go's orderByFieldExpr: each spec in precedence
// order, comparison fixed by Kind, missing/null last (both directions), with
// a final entity_id ascending tiebreaker. Ported from
// internal/domain/search/ordersort.go (sortEntities/lessByKey) — do not
// diverge from either the SQL or the in-memory semantics; drift here would
// silently change cross-backend ordering.
func LessByOrder(a, b *Entity, specs []OrderSpec) bool {
	for _, s := range specs {
		if decided, less := lessByOrderKey(a, b, s); decided {
			return less
		}
	}
	// Terminal fallback: entity_id ascending. When the last spec already
	// resolves to entity_id, the loop above always decides for distinct IDs
	// (ties only occur for the same entity), so this fallback is reached
	// only when it agrees with — or is moot alongside — that spec.
	return a.Meta.ID < b.Meta.ID
}

// lessByOrderKey decides ordering for a single key, fully applying direction
// and nulls-last. decided=false means the two are equal under this key
// (advance to the next key). A present value always precedes a missing/null
// one regardless of Desc.
func lessByOrderKey(a, b *Entity, s OrderSpec) (decided, less bool) {
	av, aok := orderLeafValue(a, s)
	bv, bok := orderLeafValue(b, s)
	if !aok || !bok {
		if aok == bok {
			return false, false // both missing ⇒ equal
		}
		return true, aok // present (aok) sorts first, irrespective of Desc
	}
	c := compareOrderValues(av, bv, s.Kind)
	if c == 0 {
		return false, false
	}
	if s.Desc {
		return true, c > 0
	}
	return true, c < 0
}

// compareOrderValues returns -1/0/1 for two present values under the
// ordering class.
func compareOrderValues(av, bv gjson.Result, kind OrderKind) int {
	switch kind {
	case OrderNumeric:
		return cmpOrderFloat(av.Float(), bv.Float())
	case OrderBool:
		return cmpOrderBool(av.Bool(), bv.Bool())
	case OrderTemporal:
		// unix-millis carried in Num (see orderTimeResult); floor to ms via
		// integer division matches the SQL backends' floor-to-ms ORDER BY.
		return cmpOrderFloat(av.Num, bv.Num)
	default: // OrderText
		return bytes.Compare([]byte(av.String()), []byte(bv.String()))
	}
}

func orderLeafValue(e *Entity, s OrderSpec) (gjson.Result, bool) {
	if s.Source == SourceMeta {
		return orderMetaLeaf(e, s.Path)
	}
	r := gjson.GetBytes(e.Data, s.Path)
	if !r.Exists() || r.Type == gjson.Null {
		return gjson.Result{}, false
	}
	return r, true
}

func orderMetaLeaf(e *Entity, path string) (gjson.Result, bool) {
	switch path {
	case "state":
		return gjson.Result{Type: gjson.String, Str: e.Meta.State}, e.Meta.State != ""
	case "transitionForLatestSave":
		return gjson.Result{Type: gjson.String, Str: e.Meta.TransitionForLatestSave}, e.Meta.TransitionForLatestSave != ""
	case "transactionId":
		return gjson.Result{Type: gjson.String, Str: e.Meta.TransactionID}, e.Meta.TransactionID != ""
	case "id":
		return gjson.Result{Type: gjson.String, Str: e.Meta.ID}, e.Meta.ID != ""
	case "creationDate":
		return orderTimeResult(e.Meta.CreationDate)
	case "lastUpdateTime":
		return orderTimeResult(e.Meta.LastModifiedDate)
	}
	return gjson.Result{}, false
}

// orderTimeResult carries a time.Time's canonical Temporal sort key:
// epoch-milliseconds, floored from timeToMicro's microsecond resolution so
// this comparator agrees with the SQL backends' floor-to-ms ORDER BY (the
// coarsest resolution common to every parity backend, incl. commercial
// Cassandra/HLC). Reuses timeToMicro (filter_match.go) rather than a second
// time->int conversion. Integer division truncates toward zero, matching
// sqlite's "/1000" and postgres's floor() for every post-1970 instant (the
// only case that occurs for engine meta dates).
func orderTimeResult(t time.Time) (gjson.Result, bool) {
	if t.IsZero() {
		return gjson.Result{}, false
	}
	ms := float64(timeToMicro(t) / 1000)
	return gjson.Result{Type: gjson.Number, Num: ms}, true
}

func cmpOrderFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func cmpOrderBool(a, b bool) int {
	switch {
	case !a && b:
		return -1
	case a && !b:
		return 1
	default:
		return 0
	}
}
