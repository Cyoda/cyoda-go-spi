package spi

import "testing"

func TestModelDescriptorUniqueKeys(t *testing.T) {
	d := ModelDescriptor{UniqueKeys: []UniqueKey{{ID: "byEmail", Fields: []string{"$.email"}}}}
	if d.UniqueKeys[0].ID != "byEmail" || d.UniqueKeys[0].Fields[0] != "$.email" {
		t.Fatalf("unique keys not carried: %+v", d.UniqueKeys)
	}
	_ = UniqueClaim{KeyID: "byEmail", Signature: "s5:Alice"}
}
