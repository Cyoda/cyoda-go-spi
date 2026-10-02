package spi

import (
	"encoding/json"
	"strings"
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

func TestStateMachineEvent_AttributionFields_JSONRoundTrip(t *testing.T) {
	in := StateMachineEvent{
		EventType:  SMEventTransitionMade,
		EntityID:   "e1",
		Attributed: Principal{ID: "alice", Kind: PrincipalUser},
		Executor:   Principal{ID: "OBOCLIENT0000001", Kind: PrincipalService},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"attributed"`) || !strings.Contains(s, `"executor"`) {
		t.Fatalf("expected attributed and executor keys present, got %s", s)
	}
	var out StateMachineEvent
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Attributed != in.Attributed {
		t.Fatalf("attributed round-trip: %+v", out.Attributed)
	}
	if out.Executor != in.Executor {
		t.Fatalf("executor round-trip: %+v", out.Executor)
	}

	// zero value: both keys omitted (omitzero)
	zero := StateMachineEvent{EventType: SMEventTransitionMade, EntityID: "e2"}
	zb, err := json.Marshal(zero)
	if err != nil {
		t.Fatal(err)
	}
	zs := string(zb)
	if strings.Contains(zs, `"attributed"`) || strings.Contains(zs, `"executor"`) {
		t.Fatalf("expected attributed and executor keys absent on zero value, got %s", zs)
	}
}
