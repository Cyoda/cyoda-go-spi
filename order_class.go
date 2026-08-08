package spi

import (
	"fmt"
	"sort"
)

// ClassifyType returns the single canonical ordering class for a leaf's
// declared types, used by the FILTER path ([ConditionToFilter]) to route
// coercion.
//
// Null members are ignored — a nullable field is still orderable. The
// remaining members must all map to the same class, otherwise there is no
// deterministic order and the field is unsortable, which is an error rather
// than an arbitrary choice.
//
// Temporal data subtypes classify as [OrderTemporal] here so data-temporal
// comparisons route to the temporal-aware pushdown. A SORT path may want the
// opposite (ISO-8601 lexical order is already chronological and is
// byte-identical across backends); such callers use [ClassifyTypesFold] with a
// fold that maps OrderTemporal onto OrderText rather than changing this
// function.
func ClassifyType(types []DataType) (OrderKind, error) {
	return ClassifyTypesFold(types, nil)
}

// ClassifyTypesFold is the shared unification core behind [ClassifyType] and
// any caller needing a different per-class fold (notably the engine's ORDER BY
// classification, which folds temporal onto text). Each non-null member is
// classified, then optionally folded; all folded classes must agree.
//
// A nil fold classifies without folding, making ClassifyTypesFold(t, nil)
// identical to ClassifyType(t).
func ClassifyTypesFold(types []DataType, fold func(OrderKind) OrderKind) (OrderKind, error) {
	var (
		have bool
		kind OrderKind
	)
	for _, t := range types {
		if t == Null {
			continue
		}
		k, err := scalarClass(t)
		if err != nil {
			return 0, err
		}
		if fold != nil {
			k = fold(k)
		}
		if !have {
			kind, have = k, true
			continue
		}
		if k != kind {
			return 0, fmt.Errorf("field has mixed ordering classes and cannot be sorted")
		}
	}
	if !have {
		return 0, fmt.Errorf("field has no sortable scalar type")
	}
	return kind, nil
}

// scalarClass maps one DataType to its ordering class.
func scalarClass(t DataType) (OrderKind, error) {
	switch {
	case IsNumeric(t):
		return OrderNumeric, nil
	case t == Boolean:
		return OrderBool, nil
	case t == String, t == Character, t == UUIDType, t == TimeUUIDType:
		// Compared as their stored ISO/string form (text/byte order).
		return OrderText, nil
	case t == LocalDate, t == LocalDateTime, t == LocalTime,
		t == ZonedDateTime, t == Year, t == YearMonth:
		// Temporal subtypes compare chronologically (with cross-subtype
		// resolution), not lexically. Routing them to OrderTemporal makes the
		// filter path stamp CoerceTemporal for a temporal data field, lighting
		// up the temporal-aware pushdown, and matches how temporal meta fields
		// (creationDate/lastUpdateTime) are ordered.
		return OrderTemporal, nil
	default: // ByteArray and anything non-scalar
		return 0, fmt.Errorf("type %s is not sortable", t)
	}
}

// MetaField describes one entry of the closed meta-field vocabulary — the
// canonical client-facing names that address entity metadata rather than
// document data.
type MetaField struct {
	Source FieldSource
	Path   string
	Kind   OrderKind
}

// sortableMetaFields is the closed set of meta sort/filter keys (canonical
// client names from the result envelope). Storage plugins map these to their
// own physical columns. It is the single source of truth for the meta
// vocabulary — callers derive temporal routing and field validity from it
// rather than maintaining separate hardcoded sets.
var sortableMetaFields = map[string]MetaField{
	"state":                   {Source: SourceMeta, Path: "state", Kind: OrderText},
	"creationDate":            {Source: SourceMeta, Path: "creationDate", Kind: OrderTemporal},
	"lastUpdateTime":          {Source: SourceMeta, Path: "lastUpdateTime", Kind: OrderTemporal},
	"transitionForLatestSave": {Source: SourceMeta, Path: "transitionForLatestSave", Kind: OrderText},
	"transactionId":           {Source: SourceMeta, Path: "transactionId", Kind: OrderText},
	"id":                      {Source: SourceMeta, Path: "id", Kind: OrderText},
}

// ResolveMetaField looks up name in the meta vocabulary. The map-key lookup is
// what enforces "no nested meta paths": a dotted name (e.g. "a.b") is simply
// not a key and returns ok=false.
func ResolveMetaField(name string) (MetaField, bool) {
	mf, ok := sortableMetaFields[name]
	return mf, ok
}

// MetaFieldNames returns the sorted canonical meta-field names, as a fresh
// slice the caller may retain or mutate.
//
// Enumeration is published alongside the [ResolveMetaField] point lookup
// because the vocabulary is a CLOSED set whose membership other code must
// agree with — a runtime matcher deciding which names address metadata, a
// validator rejecting the rest, a diagnostic listing the valid ones. Each of
// those needs the set, not a single lookup, and without this they keep their
// own copy: a silent drift surface, since nothing would compare the copies.
//
// Note "previousTransition" is absent. It is a client-facing alias for
// "transitionForLatestSave", not a vocabulary member, and
// [ConditionToFilter] canonicalizes it before any lookup. A caller
// validating raw client input must admit the alias itself.
func MetaFieldNames() []string {
	out := make([]string, 0, len(sortableMetaFields))
	for name := range sortableMetaFields {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// IsTemporalMetaField reports whether the given already-canonicalized meta
// field name is classified as temporal. Note "already-canonicalized": the
// "previousTransition" alias must be resolved to "transitionForLatestSave"
// before calling — [ConditionToFilter] does that for lifecycle conditions.
func IsTemporalMetaField(field string) bool {
	mf, ok := ResolveMetaField(field)
	return ok && mf.Kind == OrderTemporal
}
