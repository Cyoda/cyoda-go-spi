package predicate

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestRoundTrip_PreservesType verifies that every concrete condition type
// can be marshaled to JSON and parsed back via ParseCondition without
// losing the type discriminator or any field values.
func TestRoundTrip_SimpleCondition(t *testing.T) {
	original := &SimpleCondition{
		JsonPath:     "$.price",
		OperatorType: "EQUALS",
		Value:        json.Number("100"),
	}
	roundTripCondition(t, original)
}

func TestRoundTrip_LifecycleCondition(t *testing.T) {
	original := &LifecycleCondition{
		Field:        "state",
		OperatorType: "EQUALS",
		Value:        "APPROVED",
	}
	roundTripCondition(t, original)
}

func TestRoundTrip_GroupCondition_Empty(t *testing.T) {
	original := &GroupCondition{
		Operator:   "AND",
		Conditions: []Condition{},
	}
	roundTripCondition(t, original)
}

func TestRoundTrip_GroupCondition_Nested(t *testing.T) {
	original := &GroupCondition{
		Operator: "AND",
		Conditions: []Condition{
			&SimpleCondition{JsonPath: "$.price", OperatorType: "EQUALS", Value: json.Number("100")},
			&LifecycleCondition{Field: "state", OperatorType: "EQUALS", Value: "APPROVED"},
			&GroupCondition{
				Operator: "OR",
				Conditions: []Condition{
					&SimpleCondition{JsonPath: "$.amount", OperatorType: "GREATER_THAN", Value: json.Number("50")},
				},
			},
		},
	}
	roundTripCondition(t, original)
}

func TestRoundTrip_ArrayCondition(t *testing.T) {
	original := &ArrayCondition{
		JsonPath:     "$.tags[*]",
		OperatorType: "EQUALS",
		Values:       []any{"red", nil, "blue"},
	}
	roundTripCondition(t, original)
}

func TestRoundTrip_ArrayCondition_LargeIntegerKeepsPrecision(t *testing.T) {
	original := &ArrayCondition{
		JsonPath:     "$.ids[*]",
		OperatorType: "GREATER_THAN",
		Values:       []any{json.Number("12345678901234567890"), nil},
	}
	roundTripCondition(t, original)
}

func TestRoundTrip_FunctionCondition(t *testing.T) {
	// A function condition round-trips through its Raw JSON; a bare
	// &FunctionCondition{} marshals to the discriminator alone and parses back
	// with Raw set, so the original carries both Raw and the parsed PreCheck.
	original := &FunctionCondition{
		Raw:      json.RawMessage(`{"type":"function","function":{"name":"f","criterion":{"type":"lifecycle","field":"state","operatorType":"EQUALS","value":"A"}}}`),
		PreCheck: &LifecycleCondition{Field: "state", OperatorType: "EQUALS", Value: "A"},
	}
	roundTripCondition(t, original)
}

// roundTripCondition marshals the condition to JSON, parses it back, and
// asserts the result is structurally equal to the original.
func roundTripCondition(t *testing.T, original Condition) {
	t.Helper()
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := ParseCondition(encoded)
	if err != nil {
		t.Fatalf("parse: %v\nencoded: %s", err, encoded)
	}
	if !reflect.DeepEqual(original, parsed) {
		t.Errorf("round-trip mismatch:\n  original: %#v\n  parsed:   %#v\n  encoded:  %s", original, parsed, encoded)
	}
}

// TestRoundTrip_GroupCondition_EmptyConditions_RegressionFromUserReport
// captures the exact payload that triggered the bug in production:
// a Java client sending {"type":"group","operator":"AND","conditions":[]}
// got round-tripped through SearchService.SubmitAsync, lost the "type"
// field, and the per-shard executor failed with 'unknown condition type ""'
// on every shard.
func TestRoundTrip_GroupCondition_EmptyConditions_RegressionFromUserReport(t *testing.T) {
	original, err := ParseCondition([]byte(`{"type":"group","operator":"AND","conditions":[]}`))
	if err != nil {
		t.Fatalf("initial parse: %v", err)
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := ParseCondition(encoded)
	if err != nil {
		t.Fatalf("re-parse failed (this is the production bug): %v\nencoded: %s", err, encoded)
	}
	if !reflect.DeepEqual(original, parsed) {
		t.Errorf("round-trip mismatch:\n  original: %#v\n  parsed:   %#v", original, parsed)
	}
}

func TestMarshal_FunctionConditionWritesRaw(t *testing.T) {
	body := `{"type":"function","function":{"name":"f","criterion":{"type":"lifecycle","field":"state","operatorType":"EQUALS","value":"A"}}}`
	cond, err := ParseCondition([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := json.Marshal(cond)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != body {
		t.Errorf("marshal = %s, want %s", out, body)
	}
}
