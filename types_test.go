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
