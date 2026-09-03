package spi

import "fmt"

// MergeBounded performs a bounded k-way merge of a sorted committed source
// (next, lazy pull) with a pre-sorted adds slice, skipping committed rows for
// which deleted(id) is true, ordered by LessByOrder(specs).
//
// limit >= 1 is REQUIRED — a bounded-or-fail cap on the merged result, not a
// page size, matching EntityStore.Search's contract: if the number of survivors
// exceeds limit, MergeBounded returns ErrSearchResultLimitExceeded rather
// than a truncated prefix. The bound gates on TOTAL survivors, so the adds
// slice alone can trip it. Memory is bounded to ~limit+1+len(adds): the
// committed source is pulled lazily and the merge stops the moment the bound
// is exceeded.
//
// limit <= 0 is a contract violation: MergeBounded returns an error rather
// than treating it as "unbounded" or substituting a default. There is no
// unbounded mode — a caller that wants every surviving entity uses the
// EntityStore.Iterate streaming surface (see MergeOrdered) instead of asking
// for a materialized slice with no bound.
func MergeBounded(next func() (*Entity, bool, error), adds []*Entity, deleted func(id string) bool, specs []OrderSpec, limit int) ([]*Entity, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("MergeBounded: limit must be >= 1")
	}
	need := limit + 1
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
		if len(out) >= need {
			break
		}
	}
	if len(out) > limit {
		return nil, fmt.Errorf("merge: %d or more matches exceed the limit of %d: %w", len(out), limit, ErrSearchResultLimitExceeded)
	}
	return out, nil
}
