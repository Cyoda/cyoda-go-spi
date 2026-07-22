package spi

import (
	"testing"
	"time"
)

// These tests live in package spi (not spi_test) because
// TestExtractFilterMetaValue_CanonicalKeys exercises the unexported
// extractFilterMetaValue directly.

func TestMatchFilter_TemporalMeta(t *testing.T) {
	meta := EntityMeta{CreationDate: time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)}
	// stored 2021-01-01T00:00:00Z; operand 2021-01-01T00:00:00.000Z → same instant
	eq := Filter{Op: FilterEq, Source: SourceMeta, Path: "creationDate",
		Coercion: CoerceTemporal, Value: "2021-01-01T00:00:00.000Z"}
	if !MatchFilter(eq, nil, meta) {
		t.Error("EQUALS same-instant (mixed precision) should match")
	}
	gt := Filter{Op: FilterGt, Source: SourceMeta, Path: "creationDate",
		Coercion: CoerceTemporal, Value: "2020-12-31T23:59:59Z"}
	if !MatchFilter(gt, nil, meta) {
		t.Error("GREATER_THAN earlier instant should match")
	}
	// offset operand: 2021-01-01T01:00:00+01:00 == 00:00:00Z → equal
	eqOff := Filter{Op: FilterEq, Source: SourceMeta, Path: "creationDate",
		Coercion: CoerceTemporal, Value: "2021-01-01T01:00:00+01:00"}
	if !MatchFilter(eqOff, nil, meta) {
		t.Error("EQUALS with offset operand denoting same instant should match")
	}
}

// TestMatchFilter_TemporalMeta_ZeroTimeExcluded is the M1 regression test:
// toEpochMillis previously returned (t.UnixMilli(), true) for a zero
// time.Time, treating a present-but-unset stored value as a valid ~year-1
// instant rather than "no value". This diverges from
// internal/match.matchTemporalMeta, which checks !stored.IsZero() and
// excludes a zero-value stored field entirely (storedOK=false).
//
// LESS_THAN is where the divergence is externally observable: a year-1
// instant is (numerically) less than essentially every real-world date, so
// the buggy toEpochMillis makes a zero-value creationDate incorrectly match
// "LESS_THAN 2000-01-01" — a row that should be excluded (unset value) is
// returned. Once toEpochMillis treats a zero time.Time as storedOK=false,
// CompareTemporal's vacuous-false-except-NE rule takes over and LT
// correctly excludes it.
func TestMatchFilter_TemporalMeta_ZeroTimeExcluded(t *testing.T) {
	meta := EntityMeta{CreationDate: time.Time{}} // zero value: unset

	lt := Filter{Op: FilterLt, Source: SourceMeta, Path: "creationDate",
		Coercion: CoerceTemporal, Value: "2000-01-01T00:00:00Z"}
	if MatchFilter(lt, nil, meta) {
		t.Error("LESS_THAN against a zero-value stored creationDate should exclude, not match")
	}

	ne := Filter{Op: FilterNe, Source: SourceMeta, Path: "creationDate",
		Coercion: CoerceTemporal, Value: "2000-01-01T00:00:00Z"}
	if !MatchFilter(ne, nil, meta) {
		t.Error("NOT_EQUAL against a zero-value stored creationDate should vacuously match")
	}
}

func TestExtractFilterMetaValue_CanonicalKeys(t *testing.T) {
	meta := EntityMeta{ID: "e1", State: "S", TransitionForLatestSave: "t",
		TransactionID: "tx", CreationDate: time.Unix(1, 0)}
	for _, k := range []string{"id", "state", "transitionForLatestSave", "transactionId", "creationDate", "lastUpdateTime"} {
		if _, ok := extractFilterMetaValue(k, meta); !ok {
			t.Errorf("extractFilterMetaValue(%q) not found", k)
		}
	}
}
