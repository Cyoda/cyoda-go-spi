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
