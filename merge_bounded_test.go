package spi_test

import (
	"errors"
	"testing"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

func slcNext(es []*spi.Entity) func() (*spi.Entity, bool, error) {
	i := 0
	return func() (*spi.Entity, bool, error) {
		if i >= len(es) {
			return nil, false, nil
		}
		e := es[i]
		i++
		return e, true, nil
	}
}
func none(string) bool { return false }

func TestMergeBounded_InterleavesAddsInOrder(t *testing.T) {
	specs := []spi.OrderSpec{{Path: "n", Source: spi.SourceData, Kind: spi.OrderNumeric}}
	committed := []*spi.Entity{ent("a", `{"n":1}`), ent("c", `{"n":3}`)}
	adds := []*spi.Entity{ent("b", `{"n":2}`)}
	got, err := spi.MergeBounded(slcNext(committed), adds, none, specs, 0)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{got[0].Meta.ID, got[1].Meta.ID, got[2].Meta.ID}
	if ids[0] != "a" || ids[1] != "b" || ids[2] != "c" {
		t.Fatalf("want [a b c], got %v", ids)
	}
}

// TestMergeBounded_EmptyCommittedSourceOnlyAdds verifies an empty committed
// source (next exhausted immediately) still returns the adds slice as-is —
// the merge must not require a non-empty committed side.
func TestMergeBounded_EmptyCommittedSourceOnlyAdds(t *testing.T) {
	specs := []spi.OrderSpec{{Path: "n", Source: spi.SourceData, Kind: spi.OrderNumeric}}
	adds := []*spi.Entity{ent("x", `{"n":1}`), ent("y", `{"n":2}`)}
	got, err := spi.MergeBounded(slcNext(nil), adds, none, specs, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Meta.ID != "x" || got[1].Meta.ID != "y" {
		t.Fatalf("want [x y], got %v", got)
	}
}

// TestMergeBounded_EarlyStopsWithoutDrainingSource is the core guarantee:
// with a small bounded limit and no adds/deletes, MergeBounded must pull only
// enough committed rows to detect an over-limit condition and then stop —
// never draining a large source. The committed rows are generated lazily
// inside the closure (no upfront allocation of the full source), and a
// counter proves the number of next() calls stays at limit+1 (the +1 is the
// one-ahead buffered pull that overshoots by exactly one before the
// early-stop break).
func TestMergeBounded_EarlyStopsWithoutDrainingSource(t *testing.T) {
	specs := []spi.OrderSpec{{Path: "n", Source: spi.SourceData, Kind: spi.OrderNumeric}}
	const sourceSize = 100_000
	limit := 1
	calls := 0
	i := 0
	next := func() (*spi.Entity, bool, error) {
		calls++
		if i >= sourceSize {
			return nil, false, nil
		}
		e := &spi.Entity{Data: []byte(`{"n":` + itoa(i) + `}`), Meta: spi.EntityMeta{ID: "id" + itoa(i)}}
		i++
		return e, true, nil
	}
	_, err := spi.MergeBounded(next, nil, none, specs, limit)
	if !errors.Is(err, spi.ErrSearchResultLimitExceeded) {
		t.Fatalf("100000 survivors over limit 1: got err %v, want ErrSearchResultLimitExceeded", err)
	}
	if maxCalls := limit + 1 + 1; calls > maxCalls {
		t.Fatalf("MergeBounded drained source: %d next() calls, want <= %d (did not early-stop)", calls, maxCalls)
	}
}

// itoa is a tiny allocation-light int->string for building lazy test payloads
// without pulling in strconv at every call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// TestMergeBounded_PropagatesNextError verifies an error from the lazy
// committed-source puller aborts the merge and is returned to the caller,
// rather than being swallowed or treated as exhaustion.
func TestMergeBounded_PropagatesNextError(t *testing.T) {
	specs := []spi.OrderSpec{{Path: "n", Source: spi.SourceData, Kind: spi.OrderNumeric}}
	wantErr := errors.New("boom")
	next := func() (*spi.Entity, bool, error) { return nil, false, wantErr }
	_, err := spi.MergeBounded(next, nil, none, specs, 0)
	if !errors.Is(err, wantErr) {
		t.Fatalf("want error propagated, got %v", err)
	}
}

func TestMergeBounded_OverLimitRaises(t *testing.T) {
	committed := []*spi.Entity{ent("a", `{}`), ent("b", `{}`), ent("c", `{}`)}
	_, err := spi.MergeBounded(slcNext(committed), nil, nil, nil, 2)
	if !errors.Is(err, spi.ErrSearchResultLimitExceeded) {
		t.Fatalf("3 survivors over limit 2: got err %v, want ErrSearchResultLimitExceeded", err)
	}
}

func TestMergeBounded_ExactlyAtLimitSucceeds(t *testing.T) {
	committed := []*spi.Entity{ent("a", `{}`), ent("b", `{}`)}
	got, err := spi.MergeBounded(slcNext(committed), nil, nil, nil, 2)
	if err != nil {
		t.Fatalf("2 survivors at limit 2: unexpected err %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entities, want 2", len(got))
	}
}

func TestMergeBounded_UnboundedDrains(t *testing.T) {
	committed := []*spi.Entity{ent("a", `{}`), ent("b", `{}`), ent("c", `{}`)}
	got, err := spi.MergeBounded(slcNext(committed), nil, nil, nil, 0)
	if err != nil {
		t.Fatalf("limit 0 must be unbounded: unexpected err %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entities, want 3", len(got))
	}
}

// The bound gates on TOTAL survivors, so adds alone can exceed it even when
// the committed stream is empty.
func TestMergeBounded_AddsAloneExceedLimit(t *testing.T) {
	adds := []*spi.Entity{ent("a", `{}`), ent("b", `{}`), ent("c", `{}`)}
	_, err := spi.MergeBounded(slcNext(nil), adds, nil, nil, 2)
	if !errors.Is(err, spi.ErrSearchResultLimitExceeded) {
		t.Fatalf("3 adds over limit 2: got err %v, want ErrSearchResultLimitExceeded", err)
	}
}

// A deleted committed row is not a survivor and must not count toward the bound.
func TestMergeBounded_DeletedRowsDoNotCount(t *testing.T) {
	committed := []*spi.Entity{ent("a", `{}`), ent("b", `{}`), ent("c", `{}`)}
	deleted := func(id string) bool { return id == "b" }
	got, err := spi.MergeBounded(slcNext(committed), nil, deleted, nil, 2)
	if err != nil {
		t.Fatalf("2 survivors after suppression at limit 2: unexpected err %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entities, want 2", len(got))
	}
}
