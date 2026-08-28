package spi

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync/atomic"
)

// This file holds the READ side of a model's schema: the node tree, its JSON
// decoder, and the flattening that turns the tree into the JSONPath ->
// FieldDescriptor "fields map" that [ConditionToFilter] needs in order to
// stamp [Filter.Declared].
//
// It lives in the SPI so a storage plugin that self-executes a search can go
// from the bytes it already holds ([ModelDescriptor.Schema]) to a fields map
// without importing the engine. The mutation/discovery half of the model
// package — inference from sample documents, diff, merge, extend, validation,
// unique-key derivation — deliberately stays in the engine: only the engine
// may decide what a model's schema becomes, while every executor must agree
// on how to read it. The mutators below exist so the engine can build a tree
// out of this type; the split is a statement of responsibility, not a lock.
//
// Read it as a contract, not as a helper: the key convention below is shared
// with the engine, and a deviation does not fail loudly. A path that is
// spelled differently here simply misses in the map, which yields an empty
// Declared type set, which makes the leaf comparison match nothing. The
// search then returns fewer rows with no error at all.

// NodeKind names one branch a [ModelNode] can carry.
type NodeKind int

const (
	// KindLeaf is the scalar branch: primitive DataTypes observed at the path.
	KindLeaf NodeKind = iota
	// KindObject is the object branch: named children.
	KindObject
	// KindArray is the array branch: one element descriptor shared by every
	// position.
	KindArray
)

// String returns the canonical wire name of the NodeKind ("LEAF", "OBJECT",
// "ARRAY"). These names are the on-the-wire encoding written by the engine, so
// they are part of the persisted format and must not be re-spelled.
func (k NodeKind) String() string {
	switch k {
	case KindLeaf:
		return "LEAF"
	case KindObject:
		return "OBJECT"
	case KindArray:
		return "ARRAY"
	default:
		return "UNKNOWN"
	}
}

// allKinds is the iteration order for [ModelNode.Kinds] and for the encoder,
// so both are deterministic.
var allKinds = [...]NodeKind{KindLeaf, KindObject, KindArray}

// Branch is one kind a path has been observed as, together with what that
// observation recorded.
type Branch interface{ Kind() NodeKind }

// ScalarBranch records the primitive DataTypes a path was observed holding.
// It NEVER holds NULL: null is not a scalar observation, it is the nullable
// marker, and [ModelNode.Nullable] carries it.
type ScalarBranch struct{ types *TypeSet }

// Kind implements [Branch].
func (b *ScalarBranch) Kind() NodeKind { return KindLeaf }

// Types returns a sorted copy of the observed DataTypes.
func (b *ScalarBranch) Types() []DataType { return b.types.Types() }

// ObjectBranch records the named children a path was observed holding.
type ObjectBranch struct{ children map[string]*ModelNode }

// Kind implements [Branch].
func (b *ObjectBranch) Kind() NodeKind { return KindObject }

// Children returns a shallow copy of the children map (the map is copied, the
// child nodes are not).
func (b *ObjectBranch) Children() map[string]*ModelNode {
	result := make(map[string]*ModelNode, len(b.children))
	for k, v := range b.children {
		result[k] = v
	}
	return result
}

// Child returns the named child, or nil if there is none.
func (b *ObjectBranch) Child(name string) *ModelNode { return b.children[name] }

// Len reports how many children the branch carries, without copying the map.
func (b *ObjectBranch) Len() int { return len(b.children) }

// ArrayBranch records that a path was observed holding an array, together with
// the descriptor shared by every element.
type ArrayBranch struct {
	element  *ModelNode
	maxWidth int
}

// Kind implements [Branch].
func (b *ArrayBranch) Kind() NodeKind { return KindArray }

// Element returns the descriptor shared by every position, or nil when the
// array was observed but never with any content.
//
// A nil element is meaningful and must not be substituted with an empty leaf:
// that would declare a field with an empty type set — a leaf that matches
// nothing — where the truth is that nothing was declared at all.
func (b *ArrayBranch) Element() *ModelNode { return b.element }

// MaxWidth returns the widest array observed at this level, or zero. It is a
// discovery-time statistic that the wire format does not carry, so it is zero
// on any tree decoded from persisted bytes.
func (b *ArrayBranch) MaxWidth() int { return b.maxWidth }

// ModelNode is a node in a model's schema tree — the decoded form of
// [ModelDescriptor.Schema].
//
// A node holds the SET of kinds the path has been observed as, and whether it
// has been observed as null. A field observed in more than one kind carries
// more than one branch, and every branch it carries is a kind the field
// declares: a single label could only name one of three independent
// observations, so every reader that dispatched on one lost the others.
//
// Nodes are not safe for concurrent mutation. Build (or decode) a tree fully,
// then treat it as read-only; the flattening in [ModelNode.Fields] is cached
// and safe to call concurrently once the tree has stopped changing.
type ModelNode struct {
	branches map[NodeKind]Branch
	nullable bool

	fieldCache atomic.Pointer[cachedFields]
}

// NewObjectNode returns a node carrying an empty object branch.
func NewObjectNode() *ModelNode {
	n := &ModelNode{branches: make(map[NodeKind]Branch, 1)}
	n.branches[KindObject] = &ObjectBranch{children: make(map[string]*ModelNode)}
	return n
}

// NewLeafNode returns a node carrying a scalar branch seeded with dt.
//
// NewLeafNode(Null) is the exception, and the reason nullability is a flag: a
// path observed only as null has been observed as no kind at all, so the node
// carries no branch and is merely nullable.
func NewLeafNode(dt DataType) *ModelNode {
	n := &ModelNode{branches: make(map[NodeKind]Branch, 1)}
	n.AddScalarTypes(dt)
	return n
}

// NewArrayNode returns a node carrying an array branch whose elements are
// described by element. A nil element is legal — see [ArrayBranch.Element].
func NewArrayNode(element *ModelNode) *ModelNode {
	n := &ModelNode{branches: make(map[NodeKind]Branch, 1)}
	n.branches[KindArray] = &ArrayBranch{element: element}
	return n
}

// Scalar returns the node's scalar branch, or nil when the path was never
// observed holding a primitive value.
func (n *ModelNode) Scalar() *ScalarBranch {
	b, ok := n.branches[KindLeaf]
	if !ok {
		return nil
	}
	return b.(*ScalarBranch)
}

// Object returns the node's object branch, or nil when the path was never
// observed holding an object.
func (n *ModelNode) Object() *ObjectBranch {
	b, ok := n.branches[KindObject]
	if !ok {
		return nil
	}
	return b.(*ObjectBranch)
}

// Array returns the node's array branch, or nil when the path was never
// observed holding an array.
func (n *ModelNode) Array() *ArrayBranch {
	b, ok := n.branches[KindArray]
	if !ok {
		return nil
	}
	return b.(*ArrayBranch)
}

// Branch returns the branch for k, or nil when the node does not carry it.
func (n *ModelNode) Branch(k NodeKind) Branch {
	b, ok := n.branches[k]
	if !ok {
		return nil
	}
	return b
}

// Kinds returns the kinds this node declares, in ascending NodeKind order.
// A node observed only as null declares none.
func (n *ModelNode) Kinds() []NodeKind {
	out := make([]NodeKind, 0, len(n.branches))
	for _, k := range allKinds {
		if _, ok := n.branches[k]; ok {
			out = append(out, k)
		}
	}
	return out
}

// IsPolymorphic reports whether the path was observed as more than one kind.
// A monomorphic field is a set of one; the answer is derived, never stored.
func (n *ModelNode) IsPolymorphic() bool { return len(n.branches) > 1 }

// Nullable reports whether the path has been observed holding null.
//
// It is recorded only while the node carries no scalar branch: a scalar
// declaration admits null anyway, which is the same collapse [TypeSet.Add]
// applies when it drops NULL in the presence of a concrete type.
func (n *ModelNode) Nullable() bool { return n.nullable }

// DeclaredTypes returns the node's DataTypes in the spelling the field walk,
// the exporters and the persisted form use: the scalar branch's types, or the
// lone NULL marker when the node is nullable and carries no scalar branch, or
// nil. The slice is a copy.
func (n *ModelNode) DeclaredTypes() []DataType {
	if s := n.Scalar(); s != nil {
		return s.Types()
	}
	if n.nullable {
		return []DataType{Null}
	}
	return nil
}

// AddScalarTypes records primitive observations at this path, establishing the
// scalar branch if it does not exist. NULL among them is the nullable marker
// and is routed to [ModelNode.SetNullable]; a concrete type clears the marker,
// so the two orders agree.
func (n *ModelNode) AddScalarTypes(dts ...DataType) {
	for _, dt := range dts {
		if dt == Null {
			n.SetNullable()
			continue
		}
		s := n.Scalar()
		if s == nil {
			s = &ScalarBranch{types: NewTypeSet()}
			n.ensureBranches()
			n.branches[KindLeaf] = s
		}
		s.types.Add(dt)
		n.nullable = false
	}
	n.fieldCache.Store(nil)
}

// SetNullable records that this path has been observed holding null. It is a
// no-op when the node carries a scalar branch, which already admits null.
func (n *ModelNode) SetNullable() {
	if n.Scalar() != nil {
		return
	}
	n.nullable = true
	n.fieldCache.Store(nil)
}

// SetChild adds or replaces a named child, establishing the object branch if
// it does not exist, and drops the cached flattening so a later
// [ModelNode.Fields] reflects the new shape. Dropping the cache does not make
// concurrent build-and-read safe; it only makes build-then-read correct.
func (n *ModelNode) SetChild(name string, child *ModelNode) {
	o := n.Object()
	if o == nil {
		o = &ObjectBranch{children: make(map[string]*ModelNode)}
		n.ensureBranches()
		n.branches[KindObject] = o
	}
	o.children[name] = child
	n.fieldCache.Store(nil)
}

// SetElement sets the descriptor shared by every array position, establishing
// the array branch if it does not exist.
func (n *ModelNode) SetElement(element *ModelNode) {
	a := n.Array()
	if a == nil {
		a = &ArrayBranch{}
		n.ensureBranches()
		n.branches[KindArray] = a
	}
	a.element = element
	n.fieldCache.Store(nil)
}

// ObserveArrayWidth records an observed array width, keeping the widest, and
// establishes the array branch if it does not exist.
func (n *ModelNode) ObserveArrayWidth(width int) {
	a := n.Array()
	if a == nil {
		a = &ArrayBranch{}
		n.ensureBranches()
		n.branches[KindArray] = a
	}
	if width > a.maxWidth {
		a.maxWidth = width
	}
}

func (n *ModelNode) ensureBranches() {
	if n.branches == nil {
		n.branches = make(map[NodeKind]Branch, 1)
	}
}

// wireNode is the JSON representation of a [ModelNode]. It is the persisted
// format written by the engine; field names and the kind spellings are fixed
// by that format and are not free to change here.
//
// "kind" and "kinds" are the same record at two widths. A node with at most
// one branch writes "kind", which is what every model on disk already says,
// so nearly every node serialises byte-identically and there is no migration.
// A node declaring more than one kind writes "kinds", because a single label
// could only ever name one of them.
type wireNode struct {
	Kind     string               `json:"kind,omitempty"`
	Kinds    []string             `json:"kinds,omitempty"`
	Types    []string             `json:"types,omitempty"`
	Children map[string]*wireNode `json:"children,omitempty"`
	Element  *wireNode            `json:"element,omitempty"`
}

// toWire converts a ModelNode tree into a wireNode tree.
func toWire(n *ModelNode) *wireNode {
	w := &wireNode{}

	kinds := n.Kinds()
	if len(kinds) > 1 {
		w.Kinds = make([]string, 0, len(kinds))
		for _, k := range kinds {
			w.Kinds = append(w.Kinds, k.String())
		}
	} else if len(kinds) == 1 {
		w.Kind = kinds[0].String()
	} else {
		// A node that declares no kind is the nullable marker, and LEAF with a
		// lone NULL is how it has always been spelled.
		w.Kind = KindLeaf.String()
	}

	for _, dt := range n.DeclaredTypes() {
		w.Types = append(w.Types, dt.String())
	}
	if o := n.Object(); o != nil && o.Len() > 0 {
		w.Children = make(map[string]*wireNode, o.Len())
		for name, child := range o.children {
			w.Children[name] = toWire(child)
		}
	}
	if a := n.Array(); a != nil && a.element != nil {
		w.Element = toWire(a.element)
	}
	return w
}

// fromWire converts a decoded wireNode tree into a ModelNode tree.
//
// Decoding is payload-driven as well as label-driven: a branch is restored
// because the node names it OR because the payload carries it. That is what
// lets a model persisted under the old single-label spelling come back whole.
// A field observed as both an object and an array was written as
// {"kind":"OBJECT", …, "element":…} — the label named one branch and the
// payload held both — so reading the label alone dropped the array branch on
// the first read back, and a predicate on it then declared no type and matched
// nothing: fewer rows, no error.
//
// Unknown kinds and unknown type names are errors rather than skipped entries,
// and so is a node that ends up declaring nothing at all. Silently dropping
// any of them would produce a structurally valid tree that under-declares
// types, which is a wrong answer with no error — worse than refusing to search.
func fromWire(w *wireNode) (*ModelNode, error) {
	names := w.Kinds
	if len(names) == 0 && w.Kind != "" {
		names = []string{w.Kind}
	}
	if len(names) == 0 {
		// The payload can only ADD branches to the ones a node names; it
		// cannot stand in for the record entirely. A node that names no kind
		// was not written by the engine, and guessing its shape from whatever
		// keys happen to be present would accept a schema nobody defined.
		return nil, fmt.Errorf("unknown node kind %q", w.Kind)
	}

	var named [len(allKinds)]bool
	for _, name := range names {
		switch name {
		case KindLeaf.String():
			named[KindLeaf] = true
		case KindObject.String():
			named[KindObject] = true
		case KindArray.String():
			named[KindArray] = true
		default:
			return nil, fmt.Errorf("unknown node kind %q", name)
		}
	}

	nullable := false
	concrete := make([]DataType, 0, len(w.Types))
	for _, name := range w.Types {
		dt, ok := ParseDataType(name)
		if !ok {
			return nil, fmt.Errorf("unknown data type %q", name)
		}
		if dt == Null {
			nullable = true
			continue
		}
		concrete = append(concrete, dt)
	}

	n := &ModelNode{branches: make(map[NodeKind]Branch, len(names))}

	// The scalar branch. A named LEAF whose only type is the NULL marker is the
	// one ambiguous spelling, and it resolves to the branchless marker: a
	// scalar branch never holds NULL, so NULL standing alone cannot be one.
	if len(concrete) > 0 || (named[KindLeaf] && !nullable) {
		n.AddScalarTypes(concrete...)
		if n.Scalar() == nil {
			n.branches[KindLeaf] = &ScalarBranch{types: NewTypeSet()}
		}
	}
	if nullable {
		n.SetNullable()
	}

	if named[KindObject] || len(w.Children) > 0 {
		n.branches[KindObject] = &ObjectBranch{children: make(map[string]*ModelNode, len(w.Children))}
		for name, wChild := range w.Children {
			if wChild == nil {
				return nil, fmt.Errorf("child %q: null node", name)
			}
			child, err := fromWire(wChild)
			if err != nil {
				return nil, fmt.Errorf("child %q: %w", name, err)
			}
			n.SetChild(name, child)
		}
	}

	if named[KindArray] || w.Element != nil {
		// An array with no element in the wire form is preserved as an array
		// branch with Element()==nil — the unobserved-element seed shape. It
		// must not be normalised into an empty leaf; see [ArrayBranch.Element].
		var elem *ModelNode
		if w.Element != nil {
			var err error
			elem, err = fromWire(w.Element)
			if err != nil {
				return nil, fmt.Errorf("array element: %w", err)
			}
		}
		n.branches[KindArray] = &ArrayBranch{element: elem}
	}

	if len(n.branches) == 0 && !n.nullable {
		return nil, fmt.Errorf("node declares no kind and carries no payload")
	}
	return n, nil
}

// UnmarshalModelNode decodes the persisted schema bytes of a model
// ([ModelDescriptor.Schema]) into a [ModelNode] tree.
//
// Empty input is NOT special-cased here: it is a JSON syntax error like any
// other malformed input. Callers that treat "no schema bound" as "nothing to
// declare" must make that decision explicitly; [FieldsMapFromSchema] is the
// entry point that does.
func UnmarshalModelNode(data []byte) (*ModelNode, error) {
	var w wireNode
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("failed to unmarshal schema: %w", err)
	}
	return fromWire(&w)
}

// MarshalModelNode encodes a [ModelNode] tree into the persisted schema bytes.
func MarshalModelNode(n *ModelNode) ([]byte, error) {
	return json.Marshal(toWire(n))
}

// FieldsMapFromSchema derives the flattened JSONPath -> FieldDescriptor
// view from a model's stored schema ([ModelDescriptor.Schema]).
//
// Nil or empty schema bytes yield (nil, nil). That is not leniency: a model
// with no schema bound has no types to declare, and the engine's own
// schema-loading path makes the same distinction, so a plugin that reported an
// error here would reject searches the engine accepts. Callers must handle the
// nil map — with no fields map there is no declared type set, and validation
// or coercion that depends on one must degrade in whatever direction the
// caller has decided is safe, not silently treat "unknown" as "no match".
//
// Non-empty but unparseable bytes are a wrapped error. Those bytes were
// written by the engine, so failing to read them means this executor disagrees
// with the writer about the model; continuing would search against a schema
// nobody defined. Fail closed.
//
// The returned map is freshly built on every call and owned by the caller;
// mutating it affects nothing else. FieldDescriptor.MaxWidth is always zero:
// observed array widths are a discovery-time statistic that the wire format
// does not carry.
func FieldsMapFromSchema(schema []byte) (map[string]FieldDescriptor, error) {
	if len(schema) == 0 {
		return nil, nil
	}
	node, err := UnmarshalModelNode(schema)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal model schema: %w", err)
	}
	return node.FieldsMap(), nil
}

// cachedFields holds the lazily computed flat view of a tree.
type cachedFields struct {
	list   []FieldDescriptor
	byPath map[string]FieldDescriptor
}

// Fields returns the flat list of leaf descriptors for this tree, sorted by
// path and cached after the first call.
//
// Flattening rules — these are the contract, and every executor must agree on
// them, because a path spelled differently here simply misses at lookup time
// and silently narrows results:
//
//   - Paths are JSONPath-like and rooted at "$": "$.name", "$.address.city".
//   - An array hop renders as "[*]" on the array's own path segment, never as
//     an index: "$.tags[*]", "$.items[*].price".
//   - IsArray is set only for a leaf reached directly as an array's element
//     type (an array branch whose element carries only a scalar branch). It is
//     deliberately narrower than "anything under an array": neither
//     "$.items[*].price" nor the self-descriptor of an object-or-scalar array
//     element carries it. This matches the engine's flattening exactly — do not
//     "generalise" it, because consumers key off the narrow meaning.
//   - A node that carries a scalar branch ALONGSIDE a container branch emits a
//     descriptor for its OWN path IN ADDITION to the container's contents. This
//     is the object-or-scalar shape, and dropping the self-descriptor turns
//     every scalar comparison against such a path into a non-match.
//   - A node observed only as null declares NULL at its own path. A container
//     that is merely nullable emits no self-descriptor: null is the marker, not
//     a scalar observation, so the path stays a pure container.
//
// The returned slice ALIASES the cache and is shared by every caller holding
// this node, process-wide. Do not sort, append to, or otherwise mutate it;
// copy first if you need to.
func (n *ModelNode) Fields() []FieldDescriptor {
	return n.fields().list
}

// FieldsMap returns the same flattening as [ModelNode.Fields], keyed by path.
//
// The returned map ALIASES the cache and is shared process-wide; treat it as
// read-only. Note that a FieldDescriptor's Types slice is likewise shared.
func (n *ModelNode) FieldsMap() map[string]FieldDescriptor {
	return n.fields().byPath
}

// fields returns the cached flattening, computing it once. Losing the
// compare-and-swap race is harmless: both results are equal by construction,
// and the winner is adopted so callers converge on one shared value.
func (n *ModelNode) fields() *cachedFields {
	if cached := n.fieldCache.Load(); cached != nil {
		return cached
	}
	cf := n.buildFieldCache()
	if n.fieldCache.CompareAndSwap(nil, cf) {
		return cf
	}
	if cached := n.fieldCache.Load(); cached != nil {
		return cached
	}
	// A concurrent mutation cleared the cache again; the freshly built value
	// is still a consistent snapshot, so return it rather than spin.
	return cf
}

func (n *ModelNode) buildFieldCache() *cachedFields {
	var list []FieldDescriptor
	collectFields(n, "$", false, &list)
	// Sort so the flattening is deterministic: callers compare, log, and hash
	// these lists, and map iteration order alone would make that unstable.
	sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })

	byPath := make(map[string]FieldDescriptor, len(list))
	for _, f := range list {
		byPath[f.Path] = f
	}
	return &cachedFields{list: list, byPath: byPath}
}

// collectFields walks the tree depth-first, appending a descriptor for every
// searchable leaf. prefix is the JSONPath accumulated so far; inArray reports
// whether the node's values are elements of an array.
// A node is walked by the branches it carries, not by one label: a field
// observed as several kinds declares each of them, and every branch a write
// may take must be reachable here, since this is where a search looks up a
// path's declared types.
func collectFields(n *ModelNode, prefix string, inArray bool, out *[]FieldDescriptor) {
	// Scalar branch. A container node that ALSO carries one was observed
	// holding a bare scalar, so its own path is a searchable leaf in addition
	// to the container's contents. A node that declares no kind at all is the
	// nullable marker: it declares NULL at its own path. A container that is
	// merely nullable declares no scalar and emits nothing here.
	if s := n.Scalar(); s != nil {
		*out = append(*out, FieldDescriptor{
			Path:    prefix,
			Types:   s.Types(),
			IsArray: inArray,
		})
	} else if n.nullable && len(n.branches) == 0 {
		*out = append(*out, FieldDescriptor{
			Path:    prefix,
			Types:   []DataType{Null},
			IsArray: inArray,
		})
	}

	// Object branch. Sorted keys, so the flattening is deterministic.
	if o := n.Object(); o != nil {
		keys := make([]string, 0, o.Len())
		for k := range o.children {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			collectFields(o.children[k], prefix+"."+k, false, out)
		}
	}

	// Array branch. An array whose element was never observed declares
	// nothing: it must not emit an empty-typed leaf, which would match nothing
	// while looking like a declared field.
	a := n.Array()
	if a == nil || a.element == nil {
		return
	}
	arrayPath := prefix + "[*]"
	if a.element.Object() == nil && a.element.Array() == nil {
		// The element is a scalar (or the nullable marker) and nothing else,
		// so it IS the leaf. This is the one place IsArray is set.
		*out = append(*out, FieldDescriptor{
			Path:     arrayPath,
			Types:    a.element.DeclaredTypes(),
			IsArray:  true,
			MaxWidth: a.maxWidth,
		})
		return
	}
	// Elements observed as objects or arrays recurse under the "[*]" prefix.
	// inArray is false for the nested fields: "$.items[*].price" is a scalar
	// per item, not an array-valued field.
	collectFields(a.element, arrayPath, false, out)
}
