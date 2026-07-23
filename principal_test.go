package spi

import (
	"context"
	"testing"
)

func TestResolveOrigin_Precedence(t *testing.T) {
	user := Principal{ID: "u1", Kind: PrincipalUser}
	svc := Principal{ID: "svc", Kind: PrincipalService}
	origin := Principal{ID: "root", Kind: PrincipalUser}

	// UserContext fallback (root direct caller)
	ctx := WithUserContext(context.Background(), &UserContext{UserID: "u1", Kind: PrincipalUser, Tenant: Tenant{ID: "t"}})
	if got := ResolveOrigin(ctx); got != user {
		t.Fatalf("uc branch: got %+v", got)
	}
	// Ambient beats UserContext (scheduled fire seed)
	ctx = WithAmbientOrigin(ctx, origin)
	if got := ResolveOrigin(ctx); got != origin {
		t.Fatalf("ambient branch: got %+v", got)
	}
	// Zero ambient is absent
	ctx2 := WithAmbientOrigin(WithUserContext(context.Background(), &UserContext{UserID: "u1", Kind: PrincipalUser}), Principal{})
	if got := ResolveOrigin(ctx2); got != user {
		t.Fatalf("zero ambient must be absent: got %+v", got)
	}
	// Parent tx beats ambient
	ctx = WithTransaction(ctx, &TransactionState{ID: "tx1", Origin: Principal{ID: "parent", Kind: PrincipalUser}})
	if got := ResolveOrigin(ctx); got.ID != "parent" {
		t.Fatalf("parent-tx branch: got %+v", got)
	}
	_ = svc
}

func TestAttributionFor_StampRule(t *testing.T) {
	origin := Principal{ID: "root", Kind: PrincipalUser}
	// user-kind executor records itself even inside a foreign tx (D3)
	ctx := WithTransaction(
		WithUserContext(context.Background(), &UserContext{UserID: "obo", Kind: PrincipalUser}),
		&TransactionState{ID: "tx", Origin: origin})
	a, e := AttributionFor(ctx)
	if a.ID != "obo" || e.ID != "obo" {
		t.Fatalf("D3: got a=%+v e=%+v", a, e)
	}
	// service executor inherits tx origin
	ctx = WithTransaction(
		WithUserContext(context.Background(), &UserContext{UserID: "svc", Kind: PrincipalService}),
		&TransactionState{ID: "tx", Origin: origin})
	a, e = AttributionFor(ctx)
	if a != origin || e.ID != "svc" || e.Kind != PrincipalService {
		t.Fatalf("inherit: got a=%+v e=%+v", a, e)
	}
	// service executor, no tx → attributed = executor (non-joined)
	ctx = WithUserContext(context.Background(), &UserContext{UserID: "svc", Kind: PrincipalService})
	a, e = AttributionFor(ctx)
	if a != e || a.ID != "svc" {
		t.Fatalf("non-joined: got a=%+v e=%+v", a, e)
	}
	// unset kind treated as user (conservative)
	ctx = WithTransaction(
		WithUserContext(context.Background(), &UserContext{UserID: "legacy"}),
		&TransactionState{ID: "tx", Origin: origin})
	a, _ = AttributionFor(ctx)
	if a.ID != "legacy" {
		t.Fatalf("unset kind: got %+v", a)
	}
}
