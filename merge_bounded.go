package spi

import "fmt"

// MergeBounded performs a bounded k-way merge of a sorted committed source
// (next, lazy pull) with a pre-sorted adds slice, skipping committed rows for
// which deleted(id) is true, ordered by LessByOrder(specs).
//
// limit > 0 is a bounded-or-fail cap on the merged result, not a page size:
// if the number of survivors exceeds limit, MergeBounded returns
// ErrSearchResultLimitExceeded rather than a truncated prefix. The bound gates
// on TOTAL survivors, so the adds slice alone can trip it. Memory is bounded
// to ~limit+1+len(adds): the committed source is pulled lazily and the merge
// stops the moment the bound is exceeded.
//
// limit <= 0 means unbounded — it drains and materializes the entire
// surviving sequence and never raises. Callers must not substitute a default
// for a non-positive limit; "unbounded" is a real, load-bearing request mode.
func MergeBounded(next func() (*Entity, bool, error), adds []*Entity, deleted func(id string) bool, specs []OrderSpec, limit int) ([]*Entity, error) {
	need := -1
	if limit > 0 {
		need = limit + 1
	}
	out := make([]*Entity, 0, 16)
	ai := 0
	// pull the next non-deleted committed row (buffered one-ahead)
	var cur *Entity
	advance := func() error {
		for {
			e, ok, err := next()
			if err != nil {
				return err
			}
			if !ok {
				cur = nil
				return nil
			}
			if deleted != nil && deleted(e.Meta.ID) {
				continue
			}
			cur = e
			return nil
		}
	}
	if err := advance(); err != nil {
		return nil, err
	}
	for {
		haveC := cur != nil
		haveA := ai < len(adds)
		if !haveC && !haveA {
			break
		}
		var take *Entity
		switch {
		case haveC && haveA:
			if LessByOrder(adds[ai], cur, specs) {
				take = adds[ai]
				ai++
			} else {
				take = cur
				if err := advance(); err != nil {
					return nil, err
				}
			}
		case haveA:
			take = adds[ai]
			ai++
		default:
			take = cur
			if err := advance(); err != nil {
				return nil, err
			}
		}
		out = append(out, take)
		if need >= 0 && len(out) >= need {
			break
		}
	}
	if limit > 0 && len(out) > limit {
		return nil, fmt.Errorf("merge: %d or more matches exceed the limit of %d: %w", len(out), limit, ErrSearchResultLimitExceeded)
	}
	return out, nil
}
