package predicate

import "encoding/json"

// Condition is the common interface for all search predicate conditions.
type Condition interface {
	Type() string
}

// SimpleCondition matches a single JSON path against a value using an operator.
type SimpleCondition struct {
	JsonPath     string
	OperatorType string
	Value        any
}

func (c *SimpleCondition) Type() string { return "simple" }

// LifecycleCondition matches entity lifecycle metadata fields.
type LifecycleCondition struct {
	Field        string // "state", "creationDate", "previousTransition"
	OperatorType string
	Value        any
}

func (c *LifecycleCondition) Type() string { return "lifecycle" }

// GroupCondition combines multiple conditions with a logical operator.
type GroupCondition struct {
	Operator   string // "AND", "OR"
	Conditions []Condition
}

func (c *GroupCondition) Type() string { return "group" }

// ArrayCondition tests the elements of one array by position. OperatorType
// is applied at every position whose entry in Values is non-nil; a nil entry
// tests nothing at that position. [spi.DesugarCondition] defines the clause's
// meaning: an AND of one SimpleCondition per tested position.
type ArrayCondition struct {
	JsonPath     string
	OperatorType string
	Values       []any // positional operands, nil = skip
}

func (c *ArrayCondition) Type() string { return "array" }

// FunctionCondition is a condition evaluated by a callout to a compute member.
type FunctionCondition struct {
	// Raw is the function condition's JSON as the parser received it (a
	// copy). The engine dispatches each function condition with its own JSON.
	Raw json.RawMessage
	// PreCheck is the parsed `function.criterion` member: the condition the
	// engine evaluates before the callout. nil when the member is absent or
	// null. The parser does not restrict what it holds.
	PreCheck Condition
}

func (c *FunctionCondition) Type() string { return "function" }
