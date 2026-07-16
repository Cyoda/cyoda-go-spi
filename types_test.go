package spi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateChangeLevel(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want ChangeLevel
		err  bool
	}{
		{"ARRAY_LENGTH", ChangeLevelArrayLength, false},
		{"ARRAY_ELEMENTS", ChangeLevelArrayElements, false},
		{"TYPE", ChangeLevelType, false},
		{"STRUCTURAL", ChangeLevelStructural, false},
		{"", "", true},
		{"invalid", "", true},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ValidateChangeLevel(tc.in)
			if (err != nil) != tc.err {
				t.Fatalf("got err=%v, want err=%v", err, tc.err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProcessorConfig_StartNewTxOnDispatch_RoundTrips(t *testing.T) {
	tt := true
	cfg := ProcessorConfig{StartNewTxOnDispatch: &tt}
	bs, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bs), `"startNewTxOnDispatch":true`) {
		t.Errorf("missing field in marshalled JSON: %s", bs)
	}
	var back ProcessorConfig
	if err := json.Unmarshal(bs, &back); err != nil {
		t.Fatal(err)
	}
	if back.StartNewTxOnDispatch == nil || !*back.StartNewTxOnDispatch {
		t.Errorf("round-trip dropped the field: %+v", back)
	}

	// Default (nil) does NOT marshal because of omitempty.
	defaultCfg := ProcessorConfig{}
	bs2, _ := json.Marshal(defaultCfg)
	if strings.Contains(string(bs2), "startNewTxOnDispatch") {
		t.Errorf("nil pointer should be omitted, got %s", bs2)
	}
}

func TestTransitionSchedule_RoundTrips(t *testing.T) {
	// Non-nil positive TimeoutMs round-trips byte-equivalent.
	tm := int64(5000)
	sched := TransitionSchedule{DelayMs: 1000, TimeoutMs: &tm}
	bs, err := json.Marshal(sched)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bs), `"delayMs":1000`) {
		t.Errorf("missing delayMs: %s", bs)
	}
	if !strings.Contains(string(bs), `"timeoutMs":5000`) {
		t.Errorf("missing timeoutMs: %s", bs)
	}

	var back TransitionSchedule
	if err := json.Unmarshal(bs, &back); err != nil {
		t.Fatal(err)
	}
	if back.DelayMs != 1000 {
		t.Errorf("DelayMs round-trip lost value: got %d", back.DelayMs)
	}
	if back.TimeoutMs == nil || *back.TimeoutMs != 5000 {
		t.Errorf("TimeoutMs round-trip lost value: got %v", back.TimeoutMs)
	}

	// Non-nil zero TimeoutMs (strictest semantic) survives omitempty.
	zero := int64(0)
	schedZero := TransitionSchedule{DelayMs: 1000, TimeoutMs: &zero}
	bs2, err := json.Marshal(schedZero)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bs2), `"timeoutMs":0`) {
		t.Errorf("non-nil zero TimeoutMs should marshal as timeoutMs:0, got %s", bs2)
	}
	var back2 TransitionSchedule
	if err := json.Unmarshal(bs2, &back2); err != nil {
		t.Fatal(err)
	}
	if back2.TimeoutMs == nil || *back2.TimeoutMs != 0 {
		t.Errorf("non-nil zero TimeoutMs round-trip dropped distinction: %v", back2.TimeoutMs)
	}

	// Nil TimeoutMs (no-timeout semantic) does not marshal.
	schedNil := TransitionSchedule{DelayMs: 1000}
	bs3, err := json.Marshal(schedNil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bs3), "timeoutMs") {
		t.Errorf("nil TimeoutMs should be omitted, got %s", bs3)
	}
	var back3 TransitionSchedule
	if err := json.Unmarshal(bs3, &back3); err != nil {
		t.Fatal(err)
	}
	if back3.TimeoutMs != nil {
		t.Errorf("nil TimeoutMs round-trip surfaced a non-nil pointer: %v", back3.TimeoutMs)
	}
}

func TestTransitionDefinition_Schedule_RoundTrips(t *testing.T) {
	tm := int64(5000)
	tr := TransitionDefinition{
		Name:     "AutoClose",
		Next:     "Closed",
		Manual:   false,
		Schedule: &TransitionSchedule{DelayMs: 86400000, TimeoutMs: &tm},
	}
	bs, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bs), `"schedule":{"delayMs":86400000,"timeoutMs":5000}`) {
		t.Errorf("schedule field missing or wrong shape: %s", bs)
	}

	var back TransitionDefinition
	if err := json.Unmarshal(bs, &back); err != nil {
		t.Fatal(err)
	}
	if back.Schedule == nil {
		t.Fatalf("Schedule round-trip dropped the field: %+v", back)
	}
	if back.Schedule.DelayMs != 86400000 {
		t.Errorf("Schedule.DelayMs lost value: got %d", back.Schedule.DelayMs)
	}
	if back.Schedule.TimeoutMs == nil || *back.Schedule.TimeoutMs != 5000 {
		t.Errorf("Schedule.TimeoutMs lost value: got %v", back.Schedule.TimeoutMs)
	}

	// Schedule omitted is preserved as nil through round-trip.
	trNoSched := TransitionDefinition{Name: "Foo", Next: "Bar"}
	bs2, err := json.Marshal(trNoSched)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bs2), "schedule") {
		t.Errorf("nil Schedule should be omitted, got %s", bs2)
	}
	var back2 TransitionDefinition
	if err := json.Unmarshal(bs2, &back2); err != nil {
		t.Fatal(err)
	}
	if back2.Schedule != nil {
		t.Errorf("Schedule round-trip surfaced a non-nil pointer for absent field: %v", back2.Schedule)
	}
}

func TestWorkflowAnnotations_RoundTrip(t *testing.T) {
	wf := WorkflowDefinition{
		Name:         "wf",
		Version:      "1.1",
		InitialState: "S",
		Active:       true,
		Annotations:  json.RawMessage(`{"roles":["admin"]}`),
		States: map[string]StateDefinition{
			"S": {
				Annotations: json.RawMessage(`{"label":"Start"}`),
				Transitions: []TransitionDefinition{
					{Name: "t", Next: "S", Annotations: json.RawMessage(`{"icon":"x"}`)},
				},
			},
		},
	}
	bs, err := json.Marshal(wf)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"annotations":{"roles":["admin"]}`, `"annotations":{"label":"Start"}`, `"annotations":{"icon":"x"}`} {
		if !strings.Contains(string(bs), want) {
			t.Errorf("marshalled JSON missing %s: %s", want, bs)
		}
	}

	var back WorkflowDefinition
	if err := json.Unmarshal(bs, &back); err != nil {
		t.Fatal(err)
	}
	if string(back.Annotations) != `{"roles":["admin"]}` {
		t.Errorf("workflow annotations round-trip: got %s", back.Annotations)
	}
	if string(back.States["S"].Annotations) != `{"label":"Start"}` {
		t.Errorf("state annotations round-trip: got %s", back.States["S"].Annotations)
	}
	if string(back.States["S"].Transitions[0].Annotations) != `{"icon":"x"}` {
		t.Errorf("transition annotations round-trip: got %s", back.States["S"].Transitions[0].Annotations)
	}

	// Absent annotations are omitted (omitempty) — state carries a
	// transition with nil Annotations to exercise all three levels.
	plain, _ := json.Marshal(WorkflowDefinition{Name: "p", Version: "1.1", InitialState: "S", States: map[string]StateDefinition{
		"S": {Transitions: []TransitionDefinition{{Name: "t", Next: "S"}}},
	}})
	if strings.Contains(string(plain), "annotations") {
		t.Errorf("nil annotations should be omitted, got %s", plain)
	}
}

func TestProcessorConfig_AsyncResultAndCrossover_RoundTrips(t *testing.T) {
	// Helper to make pointer literals readable.
	boolPtr := func(b bool) *bool { return &b }
	i64Ptr := func(i int64) *int64 { return &i }

	cases := []struct {
		name          string
		cfg           ProcessorConfig
		wantInJSON    []string // substrings that MUST appear
		wantNotInJSON []string // substrings that MUST NOT appear
	}{
		{
			name:          "both_nil_omitted",
			cfg:           ProcessorConfig{},
			wantNotInJSON: []string{"asyncResult", "crossoverToAsyncMs"},
		},
		{
			name:          "async_true_only",
			cfg:           ProcessorConfig{AsyncResult: boolPtr(true)},
			wantInJSON:    []string{`"asyncResult":true`},
			wantNotInJSON: []string{"crossoverToAsyncMs"},
		},
		{
			name:          "async_false_only",
			cfg:           ProcessorConfig{AsyncResult: boolPtr(false)},
			wantInJSON:    []string{`"asyncResult":false`},
			wantNotInJSON: []string{"crossoverToAsyncMs"},
		},
		{
			name:          "crossover_zero_only",
			cfg:           ProcessorConfig{CrossoverToAsyncMs: i64Ptr(0)},
			wantInJSON:    []string{`"crossoverToAsyncMs":0`},
			wantNotInJSON: []string{"asyncResult"},
		},
		{
			name:          "crossover_positive_only",
			cfg:           ProcessorConfig{CrossoverToAsyncMs: i64Ptr(5000)},
			wantInJSON:    []string{`"crossoverToAsyncMs":5000`},
			wantNotInJSON: []string{"asyncResult"},
		},
		{
			name:       "both_set",
			cfg:        ProcessorConfig{AsyncResult: boolPtr(true), CrossoverToAsyncMs: i64Ptr(5000)},
			wantInJSON: []string{`"asyncResult":true`, `"crossoverToAsyncMs":5000`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bs, err := json.Marshal(tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.wantInJSON {
				if !strings.Contains(string(bs), want) {
					t.Errorf("expected JSON to contain %q; got %s", want, bs)
				}
			}
			for _, notWant := range tc.wantNotInJSON {
				if strings.Contains(string(bs), notWant) {
					t.Errorf("expected JSON NOT to contain %q; got %s", notWant, bs)
				}
			}

			var back ProcessorConfig
			if err := json.Unmarshal(bs, &back); err != nil {
				t.Fatal(err)
			}
			// Pointer-state-preserving equality for AsyncResult.
			if (back.AsyncResult == nil) != (tc.cfg.AsyncResult == nil) {
				t.Errorf("AsyncResult pointer-presence mismatched: got %v, want %v",
					back.AsyncResult, tc.cfg.AsyncResult)
			}
			if back.AsyncResult != nil && tc.cfg.AsyncResult != nil &&
				*back.AsyncResult != *tc.cfg.AsyncResult {
				t.Errorf("AsyncResult value mismatched: got %v, want %v",
					*back.AsyncResult, *tc.cfg.AsyncResult)
			}
			// Pointer-state-preserving equality for CrossoverToAsyncMs.
			if (back.CrossoverToAsyncMs == nil) != (tc.cfg.CrossoverToAsyncMs == nil) {
				t.Errorf("CrossoverToAsyncMs pointer-presence mismatched: got %v, want %v",
					back.CrossoverToAsyncMs, tc.cfg.CrossoverToAsyncMs)
			}
			if back.CrossoverToAsyncMs != nil && tc.cfg.CrossoverToAsyncMs != nil &&
				*back.CrossoverToAsyncMs != *tc.cfg.CrossoverToAsyncMs {
				t.Errorf("CrossoverToAsyncMs value mismatched: got %d, want %d",
					*back.CrossoverToAsyncMs, *tc.cfg.CrossoverToAsyncMs)
			}
		})
	}
}

func TestScheduledTask_RoundTrips(t *testing.T) {
	to := int64(5000)
	rd := int64(1_700_000_030_000)
	task := ScheduledTask{
		ID:              "e1:S:T",
		TenantID:        "t1",
		Type:            ScheduledTaskFireTransition,
		ScheduledTime:   1_700_000_000_000,
		TimeoutMs:       &to,
		RedispatchAfter: &rd,
		EntityID:        "e1",
		ModelName:       "order",
		ModelVersion:    2,
		Transition:      "AutoClose",
		SourceState:     "OPEN",
		ArmedAt:         1_699_999_999_000,
		AttemptCount:    1,
	}
	b, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	var back ScheduledTask
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Type != ScheduledTaskFireTransition || back.ScheduledTime != task.ScheduledTime ||
		back.TimeoutMs == nil || *back.TimeoutMs != 5000 || back.SourceState != "OPEN" {
		t.Fatalf("round-trip lost fields: %+v", back)
	}
	// nil TimeoutMs (no timeout) must round-trip as absent.
	task.TimeoutMs = nil
	b2, _ := json.Marshal(task)
	if strings.Contains(string(b2), "timeoutMs") {
		t.Errorf("nil TimeoutMs must be omitted: %s", b2)
	}
}
