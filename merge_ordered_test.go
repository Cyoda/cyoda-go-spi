package spi_test

import (
	"bytes"
	"errors"
	"testing"

	spi "github.com/cyoda-platform/cyoda-go-spi"
	"github.com/tidwall/gjson"
)

// idCmp is the total order test cases sort adds/committed by: byte-wise
// entity-ID comparison, mirroring the "final key: canonical ID" contract
// MergeOrdered documents.
func idCmp(a, b *spi.Entity) int {
	return bytes.Compare([]byte(a.Meta.ID), []byte(b.Meta.ID))
}

// drain pulls every entity out of a MergeOrdered stream, returning the ids in
// yield order and the terminal error (nil on clean exhaustion).
func drain(next func() (*spi.Entity, bool, error)) ([]string, error) {
	var ids []string
	for {
		e, ok, err := next()
		if err != nil {
			return ids, err
		}
		if !ok {
			return ids, nil
		}
		ids = append(ids, e.Meta.ID)
	}
}

func idsEqual(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestMergeOrdered_InterleavesInOrder(t *testing.T) {
	committed := []*spi.Entity{ent("a", `{}`), ent("c", `{}`), ent("e", `{}`)}
	adds := []*spi.Entity{ent("b", `{}`), ent("d", `{}`)}
	next := spi.MergeOrdered(slcNext(committed), adds, none, idCmp)
	ids, err := drain(next)
	if err != nil {
		t.Fatal(err)
	}
	idsEqual(t, ids, []string{"a", "b", "c", "d", "e"})
}

func TestMergeOrdered_AddReplacesCommittedSameID(t *testing.T) {
	// The overlay add and a committed row share id "b"; the overlay must win
	// and the committed duplicate must be consumed without a second yield.
	committed := []*spi.Entity{ent("a", `{}`), ent("b", `{"src":"committed"}`), ent("c", `{}`)}
	adds := []*spi.Entity{ent("b", `{"src":"overlay"}`)}
	next := spi.MergeOrdered(slcNext(committed), adds, none, idCmp)
	var got []*spi.Entity
	for {
		e, ok, err := next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		got = append(got, e)
	}
	idsEqual(t, []string{got[0].Meta.ID, got[1].Meta.ID, got[2].Meta.ID}, []string{"a", "b", "c"})
	if len(got) != 3 {
		t.Fatalf("id \"b\" must yield exactly once (overlay wins), got %d entities: %v", len(got), got)
	}
	src := gjson.GetBytes(got[1].Data, "src").String()
	if src != "overlay" {
		t.Fatalf("overlay must win the collision, got src=%q", src)
	}
}

func TestMergeOrdered_DeletedCommittedIDSkipped(t *testing.T) {
	committed := []*spi.Entity{ent("a", `{}`), ent("b", `{}`), ent("c", `{}`)}
	deleted := func(id string) bool { return id == "b" }
	next := spi.MergeOrdered(slcNext(committed), nil, deleted, idCmp)
	ids, err := drain(next)
	if err != nil {
		t.Fatal(err)
	}
	idsEqual(t, ids, []string{"a", "c"})
}

func TestMergeOrdered_CommittedStreamErrorPropagates(t *testing.T) {
	wantErr := errors.New("boom")
	calls := 0
	next := func() (*spi.Entity, bool, error) {
		calls++
		if calls == 1 {
			return ent("a", `{}`), true, nil
		}
		return nil, false, wantErr
	}
	adds := []*spi.Entity{ent("z", `{}`)}
	merged := spi.MergeOrdered(next, adds, none, idCmp)

	// First yield is the committed "a" (fetched successfully before the
	// error occurred on the priming pull for the next committed row).
	e, ok, err := merged()
	if err != nil || !ok || e.Meta.ID != "a" {
		t.Fatalf("want (a, true, nil), got (%v, %v, %v)", e, ok, err)
	}
	// The error surfaces before any further (possibly out-of-order) adds
	// are yielded, and is sticky on repeated calls.
	if _, _, err := merged(); !errors.Is(err, wantErr) {
		t.Fatalf("want error propagated, got %v", err)
	}
	if _, ok, err := merged(); ok || !errors.Is(err, wantErr) {
		t.Fatalf("error must be sticky: want (false, wantErr), got (%v, %v)", ok, err)
	}
}

func TestMergeOrdered_EmptyAdds(t *testing.T) {
	committed := []*spi.Entity{ent("a", `{}`), ent("b", `{}`)}
	next := spi.MergeOrdered(slcNext(committed), nil, none, idCmp)
	ids, err := drain(next)
	if err != nil {
		t.Fatal(err)
	}
	idsEqual(t, ids, []string{"a", "b"})
}

func TestMergeOrdered_EmptyCommitted(t *testing.T) {
	adds := []*spi.Entity{ent("a", `{}`), ent("b", `{}`)}
	next := spi.MergeOrdered(slcNext(nil), adds, none, idCmp)
	ids, err := drain(next)
	if err != nil {
		t.Fatal(err)
	}
	idsEqual(t, ids, []string{"a", "b"})
}

func TestMergeOrdered_BothEmpty(t *testing.T) {
	next := spi.MergeOrdered(slcNext(nil), nil, none, idCmp)
	ids, err := drain(next)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("want no yields, got %v", ids)
	}
}
