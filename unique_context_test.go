package spi

import (
	"context"
	"testing"
)

func TestUniqueKeysContext(t *testing.T) {
	ctx := WithUniqueKeys(context.Background(), []UniqueKey{{ID: "k", Fields: []string{"$.a"}}})
	if got := UniqueKeysFromContext(ctx); len(got) != 1 || got[0].ID != "k" {
		t.Fatalf("got %+v", got)
	}
	if UniqueKeysFromContext(context.Background()) != nil {
		t.Fatal("absent must be nil")
	}
}
