package spi

import (
	"fmt"

	"github.com/tidwall/gjson"
)

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
	// fails to parse makes prepareNode return an error (ErrUnevaluableLeaf)
	// instead of a node, so hops is always populated whenever a SourceData
	// leaf's preparedNode exists at all.
	hops []PathHop

	// exp is the once-per-query expansion this leaf evaluates against at
	// Match time. Every preparedNode a successful Prepare/prepareNode call
	// returns has one — a leaf whose operand cannot be expanded makes
	// prepareNode return an error instead of a node, so there is no
	// "unexpanded leaf" state left to represent here.
	exp Expansion
}

// Prepare compiles f for repeated evaluation, or reports why it cannot: a
// leaf whose operand cannot be expanded, whose SourceData Path is empty or
// outside the documented path grammar, or whose pattern operand will not
// compile, makes the whole filter unevaluable. That is decided once, from the
// condition alone, before any entity is read — it is a property of the
// request, and Prepare rejects the request rather than silently building a
// leaf that never matches. A never-match leaf would be indistinguishable from
// a genuine non-match at Match time, and — the reason this is not merely
// cosmetic — a NOT would invert it into matches-everything.
//
// Prepare copies everything it needs out of f. It does not retain a
// reference to it, so mutating f afterwards does not affect the returned
// value.
func Prepare(f Filter) (PreparedFilter, error) {
	// Root-only match-all. This check must NOT move into prepareNode: a
	// zero-Op CHILD is an unevaluable leaf (ExpandLeaf's default arm rejects
	// it, same as any other unsupported operator), and hoisting the check
	// into the recursion would silently turn it into an identity element
	// instead of the rejection every other unevaluable leaf gets.
	if f.Op == "" {
		return PreparedFilter{}, nil
	}
	n, err := prepareNode(f)
	if err != nil {
		return PreparedFilter{}, err
	}
	return PreparedFilter{root: &n}, nil
}

func prepareNode(f Filter) (preparedNode, error) {
	switch f.Op {
	case FilterAnd, FilterOr:
		n := preparedNode{op: f.Op}
		if len(f.Children) > 0 {
			n.children = make([]preparedNode, len(f.Children))
			for i, c := range f.Children {
				child, err := prepareNode(c)
				if err != nil {
					return preparedNode{}, err
				}
				n.children[i] = child
			}
		}
		return n, nil
	}

	// Leaf — including a zero-Op child, which ExpandLeaf's default arm rejects.
	n := preparedNode{op: f.Op, source: f.Source, path: f.Path}
	if f.Source == SourceData {
		if f.Path == "" {
			// An empty Path is legal ONLY for a tree operator (FilterAnd /
			// FilterOr, handled in the switch above and never reaching this
			// branch) — it is how Filter.Path spells "addresses no field at
			// all". A LEAF with an empty Path addresses no field either, and
			// must be rejected rather than resolved: ParseFilterPath("")
			// succeeds with a nil hop slice — legal input, by design, for
			// the tree-operator case — and ResolvePath(data, nil) resolves
			// that nil hop slice to the parsed ROOT DOCUMENT, so a
			// SourceData leaf with an empty Path would match every entity
			// via a presence test and match via equality whenever the
			// operand happened to compare equal to the document's own
			// gjson.Result.
			return preparedNode{}, fmt.Errorf("%w: leaf addresses no field (empty path)", ErrUnevaluableLeaf)
		}
		hops, err := ParseFilterPath(f.Path)
		if err != nil {
			return preparedNode{}, fmt.Errorf("%w: path %q: %v", ErrUnevaluableLeaf, f.Path, err)
		}
		n.hops = hops
	}
	exp, err := ExpandLeaf(f.Op, OperandString(f.Value), valuesToStrings(f.Values), f.Declared)
	if err != nil {
		return preparedNode{}, fmt.Errorf("%w: operand %v for op %q: %v", ErrUnevaluableLeaf, f.Value, f.Op, err)
	}
	// The pattern-compile failure ExpandLeaf swallows (compileLeafPattern's
	// error is deliberately discarded there, leaving strMatch nil, for
	// callers that still want a never-match leaf) must be surfaced HERE
	// instead of by changing ExpandLeaf's contract, which other callers
	// depend on. ValidateLeafPattern re-derives the same compile ONLY on this
	// (rare) failure path — exp.strMatch != nil is the common case and never
	// pays this cost, so the once-per-query compile guarantee
	// (TestPrepare_CompilesRegexExactlyOncePerQuery /
	// TestPrepare_TokenisesLikeExactlyOncePerQuery) holds for every leaf that
	// actually prepares successfully.
	if (f.Op == FilterLike || f.Op == FilterMatchesRegex) && exp.strMatch == nil {
		err := ValidateLeafPattern(f.Op, f.Value)
		if err == nil {
			// Should not happen: ExpandLeaf and ValidateLeafPattern share the
			// same derivation (compileLeafPattern), so a nil matcher here
			// implies ValidateLeafPattern also errors. Guard against a future
			// divergence between them turning into a silently-accepted
			// never-match leaf instead of a loud bug.
			err = fmt.Errorf("pattern operand did not compile, but ValidateLeafPattern reported no error")
		}
		return preparedNode{}, fmt.Errorf("%w: %v", ErrUnevaluableLeaf, err)
	}
	n.exp = exp
	return n, nil
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
