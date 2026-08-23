package spi

// MergeOrdered merges an already-ordered committed pull-stream with a sorted
// buffered overlay, excluding deleted ids, yielding the merged order.
// cmp is the total order both inputs are sorted by (final key: canonical
// ID), so cmp(a, b) == 0 implies a and b share an entity ID.
//
// On an equal-ID collision the overlay entity wins — it is yielded in place
// of the committed row, which is silently consumed (exactly one yield for
// that ID), matching read-your-own-writes overlay semantics elsewhere in
// this module (transaction write-set shadows the committed value).
//
// The returned function is a pull-stream: it does nothing, and calls next
// nothing, until first invoked. An error from next is propagated once all
// entities already fetched from the committed stream (and any adds that
// sort no later than them) have been yielded, and is sticky thereafter —
// no further entity is yielded once the committed stream has failed, since
// the true merge order past that point cannot be known.
func MergeOrdered(
	next func() (*Entity, bool, error),
	adds []*Entity,
	isDeleted func(entityID string) bool,
	cmp func(a, b *Entity) int,
) func() (*Entity, bool, error) {
	ai := 0
	var cur *Entity
	var pendingErr error
	started := false

	// advance pulls the next non-deleted committed row into cur (buffered
	// one-ahead). On error it stashes it in pendingErr and clears cur; the
	// error surfaces on the next call to the returned pull-function rather
	// than losing whatever this call already committed to yielding.
	advance := func() {
		for {
			e, ok, err := next()
			if err != nil {
				cur = nil
				pendingErr = err
				return
			}
			if !ok {
				cur = nil
				return
			}
			if isDeleted != nil && isDeleted(e.Meta.ID) {
				continue
			}
			cur = e
			return
		}
	}

	return func() (*Entity, bool, error) {
		if !started {
			started = true
			advance()
		}
		if pendingErr != nil {
			return nil, false, pendingErr
		}
		haveC := cur != nil
		haveA := ai < len(adds)
		switch {
		case haveC && haveA:
			c := cmp(adds[ai], cur)
			if c <= 0 {
				take := adds[ai]
				ai++
				if c == 0 {
					// Overlay wins the collision; consume the shadowed
					// committed duplicate without yielding it.
					advance()
				}
				return take, true, nil
			}
			take := cur
			advance()
			return take, true, nil
		case haveA:
			take := adds[ai]
			ai++
			return take, true, nil
		case haveC:
			take := cur
			advance()
			return take, true, nil
		default:
			return nil, false, nil
		}
	}
}
