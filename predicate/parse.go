package predicate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cyoda-platform/cyoda-go-spi/strictjson"
)

// maxParseDepth limits recursive nesting of group conditions to prevent
// stack exhaustion from deeply nested payloads.
const maxParseDepth = 50

// ParseCondition parses a JSON-encoded condition into a Condition value.
// It inspects the "type" field to determine the concrete type and delegates
// to the appropriate parser. GroupCondition's "conditions" array is parsed
// recursively.
//
// A condition is a client's typed JSON, decoded by strictjson: each form
// declares its members, and a member name must spell one of them exactly,
// once. A refusal of that kind is a *strictjson.Error naming the location
// from the top of the condition.
func ParseCondition(body []byte) (Condition, error) {
	return parseConditionWithDepth(body, 0)
}

func parseConditionWithDepth(body []byte, depth int) (Condition, error) {
	if depth > maxParseDepth {
		return nil, fmt.Errorf("condition nesting exceeds maximum depth of %d", maxParseDepth)
	}

	var envelope struct {
		Type *string `json:"type"`
	}
	if err := strictjson.Decode(body, &envelope, strictjson.AllowUnknownMembers); err != nil {
		return nil, err
	}
	if envelope.Type == nil {
		return nil, errors.New(`the condition has no "type" member`)
	}

	switch *envelope.Type {
	case "simple":
		return parseSimple(body)
	case "lifecycle":
		return parseLifecycle(body)
	case "group":
		return parseGroupWithDepth(body, depth)
	case "array":
		return parseArray(body)
	case "function":
		return parseFunctionWithDepth(body, depth)
	default:
		return nil, fmt.Errorf("unknown condition type: %q", *envelope.Type)
	}
}

// under places err under the location at when it is a *strictjson.Error, so
// a refusal inside a group member or a pre-check names its whole path; any
// other error is returned as it is.
func under(err error, at string) (error, bool) {
	var se *strictjson.Error
	if errors.As(err, &se) {
		return se.Under(at), true
	}
	return err, false
}

// parseFunctionWithDepth keeps the function condition's JSON and parses its
// pre-check one level below it, so the depth limit covers the pre-check.
// The function's config is the caller's shape: it is kept in Raw, unread.
func parseFunctionWithDepth(body []byte, depth int) (*FunctionCondition, error) {
	var raw struct {
		Type     string `json:"type"`
		Function *struct {
			Name      string          `json:"name"`
			Config    json.RawMessage `json:"config"`
			Criterion json.RawMessage `json:"criterion"`
		} `json:"function"`
	}
	if err := strictjson.Decode(body, &raw); err != nil {
		return nil, err
	}
	fc := &FunctionCondition{Raw: append(json.RawMessage(nil), body...)}
	if raw.Function == nil {
		return fc, nil
	}
	pc := bytes.TrimSpace(raw.Function.Criterion)
	if len(pc) == 0 || bytes.Equal(pc, []byte("null")) {
		return fc, nil
	}
	pre, err := parseConditionWithDepth(pc, depth+1)
	if err != nil {
		if placed, ok := under(err, "/function/criterion"); ok {
			return nil, placed
		}
		return nil, fmt.Errorf("failed to parse function pre-check: %w", err)
	}
	fc.PreCheck = pre
	return fc, nil
}

// operatorFields holds the three spellings of a leaf's operator: the
// canonical operatorType, the "operator" alias, and Cloud's "operation". A
// leaf writes one of them; two name the operator twice, as a duplicate
// member does, and are refused.
type operatorFields struct {
	OperatorType *string `json:"operatorType"`
	Operator     *string `json:"operator"`  // alias per Cyoda schema
	Operation    *string `json:"operation"` // legacy alias (backwards compat)
}

func (o operatorFields) name() (string, error) {
	var name string
	var written []string
	for _, f := range []struct {
		member string
		value  *string
	}{{"operatorType", o.OperatorType}, {"operator", o.Operator}, {"operation", o.Operation}} {
		if f.value != nil {
			name = *f.value
			written = append(written, f.member)
		}
	}
	if len(written) > 1 {
		return "", fmt.Errorf("the operator is written twice, as %q and %q: write one", written[0], written[1])
	}
	return name, nil
}

func parseSimple(body []byte) (*SimpleCondition, error) {
	var raw struct {
		Type string `json:"type"`
		operatorFields
		JsonPath string `json:"jsonPath"`
		Value    any    `json:"value"`
	}
	if err := strictjson.Decode(body, &raw, strictjson.PreserveNumbers); err != nil {
		return nil, err
	}
	op, err := raw.name()
	if err != nil {
		return nil, fmt.Errorf("failed to parse simple condition: %w", err)
	}
	return &SimpleCondition{
		JsonPath:     raw.JsonPath,
		OperatorType: op,
		Value:        raw.Value,
	}, nil
}

func parseLifecycle(body []byte) (*LifecycleCondition, error) {
	var raw struct {
		Type string `json:"type"`
		operatorFields
		Field string `json:"field"`
		Value any    `json:"value"`
	}
	if err := strictjson.Decode(body, &raw, strictjson.PreserveNumbers); err != nil {
		return nil, err
	}
	op, err := raw.name()
	if err != nil {
		return nil, fmt.Errorf("failed to parse lifecycle condition: %w", err)
	}
	return &LifecycleCondition{
		Field:        raw.Field,
		OperatorType: op,
		Value:        raw.Value,
	}, nil
}

func parseGroupWithDepth(body []byte, depth int) (*GroupCondition, error) {
	var raw struct {
		Type       string            `json:"type"`
		Operator   string            `json:"operator"`
		Conditions []json.RawMessage `json:"conditions"`
	}
	if err := strictjson.Decode(body, &raw); err != nil {
		return nil, err
	}

	conditions := make([]Condition, 0, len(raw.Conditions))
	for i, c := range raw.Conditions {
		parsed, err := parseConditionWithDepth(c, depth+1)
		if err != nil {
			if placed, ok := under(err, fmt.Sprintf("/conditions/%d", i)); ok {
				return nil, placed
			}
			return nil, fmt.Errorf("failed to parse group condition[%d]: %w", i, err)
		}
		conditions = append(conditions, parsed)
	}
	return &GroupCondition{
		Operator:   raw.Operator,
		Conditions: conditions,
	}, nil
}

func parseArray(body []byte) (*ArrayCondition, error) {
	var raw struct {
		Type string `json:"type"`
		operatorFields
		JsonPath string `json:"jsonPath"`
		Values   []any  `json:"values"`
	}
	if err := strictjson.Decode(body, &raw, strictjson.PreserveNumbers); err != nil {
		return nil, err
	}
	op, err := raw.name()
	if err != nil {
		return nil, fmt.Errorf("failed to parse array condition: %w", err)
	}
	return &ArrayCondition{
		JsonPath:     raw.JsonPath,
		OperatorType: op,
		Values:       raw.Values,
	}, nil
}
