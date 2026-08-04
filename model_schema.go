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
// on how to read it.
//
// Read it as a contract, not as a helper: the key convention below is shared
// with the engine, and a deviation does not fail loudly. A path that is
// spelled differently here simply misses in the map, which yields an empty
// Declared type set, which makes the leaf comparison match nothing. The
// search then returns fewer rows with no error at all.

// NodeKind indicates whether a [ModelNode] represents an object, an array, or
// a leaf carrying primitive types.
type NodeKind int

const (
	// KindLeaf represents a leaf node holding one or more primitive DataTypes.
	KindLeaf NodeKind = iota
	// KindObject represents an object node with named children. An object node
	// may ALSO carry types of its own; see [ModelNode.Fields].
	KindObject
	// KindArray represents an array node with an element descriptor.
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

// ModelNode is a node in a model's schema tree — the decoded form of
// [ModelDescriptor.Schema].
//
// A node is one of three kinds. A KindObject node has named children and may
// additionally carry a non-empty TypeSet, which is how a path observed as both
// an object and a bare scalar is represented (see [ModelNode.Fields] for why
// that case must not be dropped). A KindArray node has a single element
// descriptor shared by every position. A KindLeaf node carries only types.
//
// Nodes are not safe for concurrent mutation. Build (or decode) a tree fully,
// then treat it as read-only; the flattening in [ModelNode.Fields] is cached
// and safe to call concurrently once the tree has stopped changing.
type ModelNode struct {
	kind     NodeKind
	types    *TypeSet
	children map[string]*ModelNode
	element  *ModelNode

	fieldCache atomic.Pointer[cachedFields]
}

// NewObjectNode returns an object node with an empty children map and an empty
// TypeSet.
func NewObjectNode() *ModelNode {
	return &ModelNode{
		kind:     KindObject,
		types:    NewTypeSet(),
		children: make(map[string]*ModelNode),
	}
}

// NewLeafNode returns a leaf node seeded with the given DataType.
func NewLeafNode(dt DataType) *ModelNode {
	ts := NewTypeSet()
	ts.Add(dt)
	return &ModelNode{
		kind:  KindLeaf,
		types: ts,
	}
}

// NewArrayNode returns an array node whose elements are described by element.
//
// A nil element is legal and meaningful: it is the "array observed, but never
// with any content" shape, which declares no leaf at all. Callers must not
// substitute an empty leaf for it, because that would declare a field with an
// empty type set — a leaf that matches nothing.
func NewArrayNode(element *ModelNode) *ModelNode {
	return &ModelNode{
		kind:    KindArray,
		types:   NewTypeSet(),
		element: element,
	}
}

// Kind returns the NodeKind of this node.
func (n *ModelNode) Kind() NodeKind { return n.kind }

// Types returns the TypeSet associated with this node. The TypeSet is the
// node's own, not a copy: mutating it mutates the node.
func (n *ModelNode) Types() *TypeSet { return n.types }

// Element returns the element descriptor for an array node, or nil — both for
// a non-array node and for an array whose content was never observed.
func (n *ModelNode) Element() *ModelNode { return n.element }

// Children returns a shallow copy of the children map (the map is copied, the
// child nodes are not), or nil when the node has no children map.
func (n *ModelNode) Children() map[string]*ModelNode {
	if n.children == nil {
		return nil
	}
	result := make(map[string]*ModelNode, len(n.children))
	for k, v := range n.children {
		result[k] = v
	}
	return result
}

// Child returns the named child, or nil if there is none.
func (n *ModelNode) Child(name string) *ModelNode {
	if n.children == nil {
		return nil
	}
	return n.children[name]
}

// SetChild adds or replaces a named child on this node and drops any cached
// flattening, so a later [ModelNode.Fields] reflects the new shape. Dropping
// the cache does not make concurrent build-and-read safe; it only makes
// build-then-read correct.
func (n *ModelNode) SetChild(name string, child *ModelNode) {
	if n.children == nil {
		n.children = make(map[string]*ModelNode)
	}
	n.children[name] = child
	n.fieldCache.Store(nil)
}

// wireNode is the JSON representation of a [ModelNode]. It is the persisted
// format written by the engine; field names and the Kind spellings are fixed
// by that format and are not free to change here.
type wireNode struct {
	Kind     string               `json:"kind"`
	Types    []string             `json:"types,omitempty"`
	Children map[string]*wireNode `json:"children,omitempty"`
	Element  *wireNode            `json:"element,omitempty"`
}

// fromWire converts a decoded wireNode tree into a ModelNode tree.
//
// Unknown kinds and unknown type names are errors rather than skipped entries.
// Silently dropping either would produce a structurally valid tree that
// under-declares types, and an under-declared leaf comparison matches nothing
// — a wrong answer with no error, which is worse than refusing to search.
//
// One inherited gap, preserved deliberately for parity rather than fixed
// here: a "children" object on a LEAF or ARRAY wire node is IGNORED, not
// rejected. The engine's decoder consults w.Children only in the OBJECT case,
// so tightening it here would make this decoder reject schemas the engine
// accepts. That divergence is the more dangerous of the two, so it is tracked
// upstream (Cyoda/cyoda-go#464) rather than fixed unilaterally.
func fromWire(w *wireNode) (*ModelNode, error) {
	var n *ModelNode

	switch w.Kind {
	case "OBJECT":
		n = NewObjectNode()
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

	case "ARRAY":
		var elem *ModelNode
		if w.Element != nil {
			var err error
			elem, err = fromWire(w.Element)
			if err != nil {
				return nil, fmt.Errorf("array element: %w", err)
			}
		}
		// An ARRAY with no element in the wire form is preserved as an ARRAY
		// with Element()==nil — the unobserved-element seed shape. It must not
		// be normalised into an empty leaf; see [NewArrayNode].
		n = NewArrayNode(elem)

	case "LEAF":
		n = &ModelNode{
			kind:  KindLeaf,
			types: NewTypeSet(),
		}

	default:
		return nil, fmt.Errorf("unknown node kind %q", w.Kind)
	}

	for _, name := range w.Types {
		dt, ok := ParseDataType(name)
		if !ok {
			return nil, fmt.Errorf("unknown data type %q", name)
		}
		n.types.Add(dt)
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
//     type (an ARRAY whose element is itself a KindLeaf). It is deliberately
//     narrower than "anything under an array": neither "$.items[*].price"
//     nor the self-descriptor of an object-or-scalar array element carries
//     it. This matches the engine's flattening exactly — do not "generalise"
//     it, because consumers key off the narrow meaning.
//   - A KindObject node that also carries concrete (non-NULL) types emits a
//     descriptor for its OWN path IN ADDITION to its children. This is the
//     object-or-scalar shape, and dropping the self-descriptor turns every
//     scalar comparison against such a path into a non-match.
//   - A KindObject node whose only type is NULL emits no self-descriptor: NULL
//     is the nullable marker, not a scalar observation, so the path stays a
//     pure container.
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
	// A concurrent SetChild cleared the cache again; the freshly built value
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

// concreteTypes returns ts's DataTypes with the NULL marker removed. A TypeSet
// is either the NULL-only marker or a set of concrete types ([TypeSet.Add]
// drops NULL once a concrete type is present), so this yields nil for a
// nil/empty set or a NULL-only set, and the concrete types otherwise.
//
// It backs the object-node self-leaf emit: only a genuine object-or-scalar
// polymorphism (a concrete scalar observed at an object path) becomes a
// searchable leaf; a merely nullable object stays a pure container.
func concreteTypes(ts *TypeSet) []DataType {
	if ts == nil || ts.IsEmpty() {
		return nil
	}
	all := ts.Types()
	out := all[:0:0]
	for _, dt := range all {
		if dt == Null {
			continue
		}
		out = append(out, dt)
	}
	return out
}

// collectFields walks the tree depth-first, appending a descriptor for every
// searchable leaf. prefix is the JSONPath accumulated so far; inArray reports
// whether the node's values are elements of an array.
func collectFields(n *ModelNode, prefix string, inArray bool, out *[]FieldDescriptor) {
	switch n.kind {
	case KindLeaf:
		*out = append(*out, FieldDescriptor{
			Path:    prefix,
			Types:   n.types.Types(), // Types() returns a copy
			IsArray: inArray,
		})

	case KindObject:
		// A node observed as BOTH an object and a bare CONCRETE scalar (the
		// polymorphic object-or-scalar shape the engine unions onto a
		// KindObject node) carries a concrete TypeSet alongside its children.
		// Emit a leaf for the object node's OWN path so the scalar-valued
		// observations are directly searchable with a scalar operand — IN
		// ADDITION to recursing into the children below, which is how the
		// object-valued observations are reached. A PURE object (children
		// only, empty TypeSet) emits nothing for itself: it has no scalar to
		// compare against. A NULL-only TypeSet is the nullable marker, not a
		// concrete observation, and likewise emits nothing.
		if concrete := concreteTypes(n.types); len(concrete) > 0 {
			*out = append(*out, FieldDescriptor{
				Path:    prefix,
				Types:   concrete,
				IsArray: inArray,
			})
		}
		keys := make([]string, 0, len(n.children))
		for k := range n.children {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			collectFields(n.children[k], prefix+"."+k, false, out)
		}

	case KindArray:
		// An array whose element was never observed declares nothing. It must
		// not emit an empty-typed leaf, which would match nothing while
		// looking like a declared field.
		if n.element == nil {
			return
		}
		arrayPath := prefix + "[*]"
		if n.element.kind == KindLeaf {
			*out = append(*out, FieldDescriptor{
				Path:    arrayPath,
				Types:   n.element.types.Types(),
				IsArray: true,
			})
			return
		}
		// Arrays of objects/arrays recurse under the "[*]" prefix. inArray is
		// false for the nested fields: "$.items[*].price" is a scalar per
		// item, not an array-valued field.
		collectFields(n.element, arrayPath, false, out)
	}
}
