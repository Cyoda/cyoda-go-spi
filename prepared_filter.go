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

	// hops is the parsed form of path, for SourceData leaves only: parsed
	// once here in prepareNode so per-row Match does no parsing. A path that
	// fails to parse leaves hops nil and expanded false — the same "never
	// matches" a leaf that failed ExpandLeaf already produces, so a malformed
	// path never resolves to anything rather than falling back to some other
	// interpretation.
	hops []PathHop

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
// Prepare copies everything it needs out of f. It does not retain a
// reference to it, so mutating f afterwards does not affect the returned
// value.
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
	if f.Source == SourceData {
		if f.Path == "" {
			// An empty Path is legal ONLY for a tree operator (FilterAnd /
			// FilterOr, handled in the switch above and never reaching this
			// branch) — it is how Filter.Path spells "addresses no field at
			// all". A LEAF with an empty Path addresses no field either, so
			// it must never resolve to anything, exactly like a path that
			// fails to parse below: leaving hops nil and expanded false is
			// what makes that so. Without this guard, ParseFilterPath("")
			// succeeds with a nil hop slice — legal input, by design, for
			// the tree-operator case — and ResolvePath(data, nil) resolves
			// that nil hop slice to the parsed ROOT DOCUMENT, so a
			// SourceData leaf with an empty Path matched every entity via a
			// presence test and matched via equality whenever the operand
			// happened to compare equal to the document's own gjson.Result.
			return n
		}
		hops, err := ParseFilterPath(f.Path)
		if err != nil {
			// A path that fails to parse must never resolve to anything: leave
			// the node unexpanded, which is already a non-match. Do not fall
			// through to ExpandLeaf — an unexpanded node never reaches
			// storedAll either way, but leaving hops nil here keeps that
			// invariant explicit rather than incidental.
			return n
		}
		n.hops = hops
	}
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
	// A leaf holds when SOME addressed value satisfies it. A leaf addressing
	// no values (an empty slice) is a non-match for every operator, presence
	// tests included — see docs/cloud-parity/path-grammar.md section 5: a
	// wildcard path over an empty array, a null or an absent field presents
	// no elements, so IS_NULL and NOT_NULL both answer false there. Do not
	// special-case an empty slice to "true for IS_NULL" — that would make the
	// two presence tests complements on a wildcard path, which section 5
	// deliberately rejects.
	for _, r := range n.storedAll(data, meta) {
		if EvalLeaf(n.exp, r) {
			return true
		}
	}
	return false
}

// storedAll resolves every value this leaf's path addresses, keeping gjson's
// .Raw so the kernel can classify numerics and temporals precisely. A
// SourceData leaf resolves through ResolvePath against the hops parsed once
// in prepareNode, so the result set follows the path's syntax rather than the
// stored value's shape (docs/cloud-parity/path-grammar.md section 3). A
// SourceMeta leaf is not a data path and carries no subscript, so it keeps
// its single bridged result through metaGjsonResult, same contract as the
// pre-split filterStoredResult.
//
// KNOWN DIVERGENCE, deliberately not resolved here. A temporal meta field
// (creationDate / lastUpdateTime) bridges to an RFC3339 string, and this
// evaluator applies a text or pattern operator to it lexically — where the
// predicate-tree evaluator in the consuming service guards the same case to a
// non-match on field identity. The same request therefore answers differently
// depending on whether the query pushes down.
//
// Do NOT resolve this by aligning either evaluator. A text or pattern operator
// on a temporal field is not a supported predicate, and the resolution is to
// refuse it at the shared validation boundary, which makes both evaluators'
// behaviour unreachable. Aligning here would specify semantics for a predicate
// that is being withdrawn.
func (n *preparedNode) storedAll(data []byte, meta EntityMeta) []gjson.Result {
	if n.source == SourceMeta {
		r, _ := metaGjsonResult(n.path, meta)
		return []gjson.Result{r}
	}
	return ResolvePath(data, n.hops)
}
