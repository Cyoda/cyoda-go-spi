package spi

import "github.com/tidwall/gjson"

// prepared_filter.go is the prepare/execute split of the Filter evaluator.
// Prepare resolves everything that depends only on the query — operand
// normalisation, type bucketing, and regex compilation — into an immutable
// tree. Match then walks that tree per row and does no query-invariant work at
// all.
//
// The prepared value is safe for concurrent use by any number of goroutines
// after Prepare returns: nothing in it is written again, and nothing is
// resolved lazily. The commercial Cassandra direct-search fan-out depends on
// this, handing one prepared filter to N errgroup workers.

// PreparedFilter is a Filter compiled for repeated evaluation. Build it once
// per query with Prepare, then call Match once per candidate row.
//
// The zero PreparedFilter matches everything, mirroring Prepare(Filter{}) and
// the "no filter" convention every backend already relies on.
type PreparedFilter struct {
	// root is nil exactly for the match-all filter. A nil root is what makes
	// the zero value match-all without a separate flag.
	root *preparedNode
}

// preparedNode is one node of the prepared tree: a group with children, or a
// leaf carrying its addressing plus the expansion its operand produced.
type preparedNode struct {
	op       FilterOp
	children []preparedNode

	// Leaf addressing, mirroring Filter.Source / Filter.Path.
	source FieldSource
	path   string

	// exp is meaningful only when expanded is true. A leaf whose ExpandLeaf
	// failed is a leaf that never matches — the same answer evalLeafFilter
	// produced by absorbing the error into `matched && err == nil`, but stated
	// explicitly rather than relying on the zero Expansion happening to fall
	// through EvalLeaf's switch.
	exp      Expansion
	expanded bool
}

// Prepare compiles f for repeated evaluation. It returns no error: a leaf whose
// operand cannot be expanded becomes a leaf that never matches, which is
// exactly what the per-row evaluator did before. Promoting that to a hard
// rejection is a cross-backend contract change and is deliberately not done
// here.
//
// Prepare consumes f. It does not retain a reference to it, and it is not a
// defence against a caller mutating f afterwards — no such defence is owed.
func Prepare(f Filter) PreparedFilter {
	// Root-only match-all. This check must NOT move into prepareNode: a
	// zero-Op CHILD is a leaf that never matches, and hoisting the check into
	// the recursion would silently turn it into an identity element.
	if f.Op == "" {
		return PreparedFilter{}
	}
	n := prepareNode(f)
	return PreparedFilter{root: &n}
}

func prepareNode(f Filter) preparedNode {
	switch f.Op {
	case FilterAnd, FilterOr:
		n := preparedNode{op: f.Op}
		if len(f.Children) > 0 {
			n.children = make([]preparedNode, len(f.Children))
			for i, c := range f.Children {
				n.children[i] = prepareNode(c)
			}
		}
		return n
	}

	// Leaf — including a zero-Op child, which ExpandLeaf's default arm rejects.
	n := preparedNode{op: f.Op, source: f.Source, path: f.Path}
	exp, err := ExpandLeaf(f.Op, OperandString(f.Value), valuesToStrings(f.Values), f.Declared)
	if err == nil {
		n.exp = exp
		n.expanded = true
	}
	return n
}

// Match reports whether the entity satisfies the prepared filter. It performs
// no parsing, bucketing or regex compilation — all of that happened in Prepare.
func (p PreparedFilter) Match(data []byte, meta EntityMeta) bool {
	if p.root == nil {
		return true
	}
	return p.root.match(data, meta)
}

func (n *preparedNode) match(data []byte, meta EntityMeta) bool {
	switch n.op {
	case FilterAnd:
		for i := range n.children {
			if !n.children[i].match(data, meta) {
				return false
			}
		}
		return true
	case FilterOr:
		for i := range n.children {
			if n.children[i].match(data, meta) {
				return true
			}
		}
		return false
	}
	if !n.expanded {
		return false
	}
	return EvalLeaf(n.exp, n.stored(data, meta))
}

// stored resolves the value this leaf addresses, keeping gjson's .Raw so the
// kernel can classify numerics and temporals precisely. Same contract as the
// pre-split filterStoredResult: a missing data path yields a non-existent
// Result, and SourceMeta values are bridged through metaGjsonResult.
func (n *preparedNode) stored(data []byte, meta EntityMeta) gjson.Result {
	if n.source == SourceMeta {
		r, _ := metaGjsonResult(n.path, meta)
		return r
	}
	return gjson.GetBytes(data, n.path)
}
