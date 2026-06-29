package spi

import (
	"errors"
	"testing"
)

func TestModelDescriptorUniqueKeys(t *testing.T) {
	d := ModelDescriptor{UniqueKeys: []UniqueKey{{ID: "byEmail", Fields: []string{"$.email"}}}}
	if d.UniqueKeys[0].ID != "byEmail" || d.UniqueKeys[0].Fields[0] != "$.email" {
		t.Fatalf("unique keys not carried: %+v", d.UniqueKeys)
	}
	_ = UniqueClaim{KeyID: "byEmail", Signature: "s5:Alice"}
}

func TestUniqueSentinels(t *testing.T) {
	if errors.Is(ErrUniqueViolation, ErrConflict) {
		t.Fatal("must not equal ErrConflict")
	}
	if errors.Is(ErrPartialUniqueKey, ErrUniqueViolation) {
		t.Fatal("partial != violation")
	}
}

type capYes struct{}

func (capYes) SupportsCompositeUniqueKeys() bool { return true }

func TestCapable(t *testing.T) {
	var v any = capYes{}
	if c, ok := v.(CompositeUniqueKeyCapable); !ok || !c.SupportsCompositeUniqueKeys() {
		t.Fatal("not capable")
	}
}
