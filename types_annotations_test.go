package spi

import (
	"encoding/json"
	"testing"
)

func TestProcessorAndCriterionAnnotations_RoundTrip(t *testing.T) {
	in := WorkflowDefinition{
		Name:                 "wf",
		Version:              "1.2",
		InitialState:         "S",
		CriterionAnnotations: json.RawMessage(`{"displayName":"wf-guard"}`),
		States: map[string]StateDefinition{
			"S": {Transitions: []TransitionDefinition{{
				Name:                 "t",
				Next:                 "S",
				CriterionAnnotations: json.RawMessage(`{"displayName":"t-guard"}`),
				Processors: []ProcessorDefinition{{
					Name:        "p",
					Type:        "externalized",
					Annotations: json.RawMessage(`{"displayName":"proc"}`),
				}},
			}}},
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out WorkflowDefinition
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(out.CriterionAnnotations) != `{"displayName":"wf-guard"}` {
		t.Errorf("wf criterionAnnotations lost: %s", out.CriterionAnnotations)
	}
	tr := out.States["S"].Transitions[0]
	if string(tr.CriterionAnnotations) != `{"displayName":"t-guard"}` {
		t.Errorf("transition criterionAnnotations lost: %s", tr.CriterionAnnotations)
	}
	if string(tr.Processors[0].Annotations) != `{"displayName":"proc"}` {
		t.Errorf("processor annotations lost: %s", tr.Processors[0].Annotations)
	}
}

func TestAnnotations_OmittedWhenAbsent(t *testing.T) {
	b, _ := json.Marshal(ProcessorDefinition{Name: "p", Type: "externalized"})
	if got := string(b); contains(got, "annotations") {
		t.Errorf("absent processor annotations should be omitted, got %s", got)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
