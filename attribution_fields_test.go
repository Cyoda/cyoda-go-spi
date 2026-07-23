package spi

import (
	"encoding/json"
	"testing"
)

func TestScheduledTask_ArmedBy_JSONRoundTrip(t *testing.T) {
	in := ScheduledTask{ID: "id1", TenantID: "t", Type: ScheduledTaskFireTransition,
		ArmedBy: Principal{ID: "u1", Kind: PrincipalUser}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out ScheduledTask
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.ArmedBy != in.ArmedBy {
		t.Fatalf("round-trip: %+v", out.ArmedBy)
	}
	// legacy JSON (no armedBy) → zero value
	var legacy ScheduledTask
	if err := json.Unmarshal([]byte(`{"id":"x","tenantId":"t"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.ArmedBy != (Principal{}) {
		t.Fatalf("legacy must be zero: %+v", legacy.ArmedBy)
	}
}
