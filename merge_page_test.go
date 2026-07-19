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
