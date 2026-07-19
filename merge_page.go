package spi

// MergePage performs a bounded k-way merge of a sorted committed source
// (next, lazy pull) with a pre-sorted adds slice, skipping committed rows
// for which deleted(id) is true, ordered by LessByOrder(specs). It returns
// the [offset, offset+limit) window. limit<=0 means unbounded. Memory is
// bounded to ~offset+limit+len(adds) when limit>0: the committed source is
// pulled lazily and the merge early-stops once enough survivors are
// gathered. limit<=0 (unbounded) drains and materializes the entire
// surviving sequence.
func MergePage(next func() (*Entity, bool, error), adds []*Entity, deleted func(id string) bool, specs []OrderSpec, offset, limit int) ([]*Entity, error) {
	need := -1
	if limit > 0 {
		need = offset + limit
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
	if offset > 0 {
		if offset >= len(out) {
			return nil, nil
		}
		out = out[offset:]
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
