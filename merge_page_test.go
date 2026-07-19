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

func TestMergePage_InterleavesAddsInOrder(t *testing.T) {
	specs := []spi.OrderSpec{{Path: "n", Source: spi.SourceData, Kind: spi.OrderNumeric}}
	committed := []*spi.Entity{ent("a", `{"n":1}`), ent("c", `{"n":3}`)}
	adds := []*spi.Entity{ent("b", `{"n":2}`)}
	got, err := spi.MergePage(slcNext(committed), adds, none, specs, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{got[0].Meta.ID, got[1].Meta.ID, got[2].Meta.ID}
	if ids[0] != "a" || ids[1] != "b" || ids[2] != "c" {
		t.Fatalf("want [a b c], got %v", ids)
	}
}

func TestMergePage_SkipsDeletedAndPagesWithOffsetLimit(t *testing.T) {
	specs := []spi.OrderSpec{{Path: "n", Source: spi.SourceData, Kind: spi.OrderNumeric}}
	committed := []*spi.Entity{ent("a", `{"n":1}`), ent("b", `{"n":2}`), ent("c", `{"n":3}`), ent("d", `{"n":4}`)}
	del := func(id string) bool { return id == "b" }
	got, err := spi.MergePage(slcNext(committed), nil, del, specs, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Meta.ID != "c" {
		t.Fatalf("want [c] after skipping b, offset 1 limit 1, got %v", got)
	}
}

// TestMergePage_UnboundedLimitDrainsAllAndAppliesOffset verifies limit<=0
// means unbounded: every surviving row (committed + adds, minus deleted) is
// gathered, and offset still trims from the front of that full merged
// sequence rather than being ignored.
func TestMergePage_UnboundedLimitDrainsAllAndAppliesOffset(t *testing.T) {
	specs := []spi.OrderSpec{{Path: "n", Source: spi.SourceData, Kind: spi.OrderNumeric}}
	committed := []*spi.Entity{ent("a", `{"n":1}`), ent("c", `{"n":3}`), ent("e", `{"n":5}`)}
	adds := []*spi.Entity{ent("b", `{"n":2}`), ent("d", `{"n":4}`)}
	got, err := spi.MergePage(slcNext(committed), adds, none, specs, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 survivors after offset 2 of 5 total, got %d: %v", len(got), got)
	}
	ids := []string{got[0].Meta.ID, got[1].Meta.ID, got[2].Meta.ID}
	if ids[0] != "c" || ids[1] != "d" || ids[2] != "e" {
		t.Fatalf("want [c d e], got %v", ids)
	}
}

// TestMergePage_AddBeforeOffsetIsCountedNotEmitted verifies an add whose
// key sorts before the offset window still consumes a paging slot (it is
// counted toward offset) rather than always being emitted regardless of
// offset — the merge treats adds and committed rows uniformly for paging.
func TestMergePage_AddBeforeOffsetIsCountedNotEmitted(t *testing.T) {
	specs := []spi.OrderSpec{{Path: "n", Source: spi.SourceData, Kind: spi.OrderNumeric}}
	committed := []*spi.Entity{ent("b", `{"n":2}`), ent("c", `{"n":3}`)}
	adds := []*spi.Entity{ent("a", `{"n":1}`)} // sorts before everything
	got, err := spi.MergePage(slcNext(committed), adds, none, specs, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Meta.ID != "b" || got[1].Meta.ID != "c" {
		t.Fatalf("want [b c] (a consumed by offset), got %v", got)
	}
}

// TestMergePage_EmptyCommittedSourceOnlyAdds verifies an empty committed
// source (next exhausted immediately) still returns the adds slice as-is,
// windowed by offset/limit — the merge must not require a non-empty
// committed side.
func TestMergePage_EmptyCommittedSourceOnlyAdds(t *testing.T) {
	specs := []spi.OrderSpec{{Path: "n", Source: spi.SourceData, Kind: spi.OrderNumeric}}
	adds := []*spi.Entity{ent("x", `{"n":1}`), ent("y", `{"n":2}`)}
	got, err := spi.MergePage(slcNext(nil), adds, none, specs, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Meta.ID != "x" || got[1].Meta.ID != "y" {
		t.Fatalf("want [x y], got %v", got)
	}
}

// TestMergePage_EarlyStopsWithoutDrainingSource is the core guarantee: with
// a small bounded limit and no adds/deletes, MergePage must pull only enough
// committed rows to fill offset+limit and then stop — never draining a large
// source. The committed rows are generated lazily inside the closure (no
// upfront allocation of the full source), and a counter proves the number of
// next() calls stays at offset+limit+1 (the +1 is the one-ahead buffered
// pull that overshoots by exactly one before the early-stop break).
func TestMergePage_EarlyStopsWithoutDrainingSource(t *testing.T) {
	specs := []spi.OrderSpec{{Path: "n", Source: spi.SourceData, Kind: spi.OrderNumeric}}
	const sourceSize = 100_000
	offset, limit := 0, 1
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
	got, err := spi.MergePage(next, nil, none, specs, offset, limit)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 row, got %d", len(got))
	}
	if maxCalls := offset + limit + 1; calls > maxCalls {
		t.Fatalf("MergePage drained source: %d next() calls, want <= %d (did not early-stop)", calls, maxCalls)
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

// TestMergePage_PropagatesNextError verifies an error from the lazy
// committed-source puller aborts the merge and is returned to the caller,
// rather than being swallowed or treated as exhaustion.
func TestMergePage_PropagatesNextError(t *testing.T) {
	specs := []spi.OrderSpec{{Path: "n", Source: spi.SourceData, Kind: spi.OrderNumeric}}
	wantErr := errors.New("boom")
	next := func() (*spi.Entity, bool, error) { return nil, false, wantErr }
	_, err := spi.MergePage(next, nil, none, specs, 0, 0)
	if !errors.Is(err, wantErr) {
		t.Fatalf("want error propagated, got %v", err)
	}
}
