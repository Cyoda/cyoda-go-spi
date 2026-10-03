package spi_test

import (
	"context"
	"testing"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

func TestTenantIDIsNamedType(t *testing.T) {
	var tid spi.TenantID = "test-tenant"
	if tid == "" {
		t.Fatal("expected non-empty tenant ID")
	}
}

func TestSystemTenantIDConstant(t *testing.T) {
	if spi.SystemTenantID == "" {
		t.Fatal("expected SystemTenantID to be defined")
	}
	if spi.SystemTenantID != "SYSTEM" {
		t.Errorf("expected SYSTEM, got %s", spi.SystemTenantID)
	}
}

func TestUserContextCarriesTenant(t *testing.T) {
	tenant := spi.Tenant{ID: "tenant-A", Name: "Tenant A"}
	uc := &spi.UserContext{
		UserID: "user-1",
		Tenant: tenant,
		Roles:  []string{"USER"},
	}
	ctx := spi.WithUserContext(context.Background(), uc)
	got := spi.MustGetUserContext(ctx)
	if got.Tenant.ID != "tenant-A" {
		t.Errorf("expected tenant-A, got %s", got.Tenant.ID)
	}
	if got.Tenant.Name != "Tenant A" {
		t.Errorf("expected Tenant A, got %s", got.Tenant.Name)
	}
}

func TestAttributionFor_ExecutorSet_AttributesToUserIgnoringTxOrigin(t *testing.T) {
	ctx := spi.WithUserContext(context.Background(), &spi.UserContext{
		UserID: "alice", Kind: spi.PrincipalUser, Tenant: spi.Tenant{ID: "t1"},
		Executor: &spi.Principal{ID: "OBOCLIENT0000001", Kind: spi.PrincipalService},
	})
	ctx = spi.WithTransaction(ctx, &spi.TransactionState{ID: "tx1", TenantID: "t1",
		Origin: spi.Principal{ID: "bob", Kind: spi.PrincipalUser}})

	att, exe := spi.AttributionFor(ctx)
	if want := (spi.Principal{ID: "alice", Kind: spi.PrincipalUser}); att != want {
		t.Fatalf("attributed = %+v, want %+v", att, want)
	}
	if want := (spi.Principal{ID: "OBOCLIENT0000001", Kind: spi.PrincipalService}); exe != want {
		t.Fatalf("executor = %+v, want %+v", exe, want)
	}
}

func TestAttributionFor_ExecutorSet_NoTransaction(t *testing.T) {
	ctx := spi.WithUserContext(context.Background(), &spi.UserContext{
		UserID: "alice", Kind: spi.PrincipalUser,
		Executor: &spi.Principal{ID: "OBOCLIENT0000001", Kind: spi.PrincipalService},
	})
	att, exe := spi.AttributionFor(ctx)
	if att.ID != "alice" || exe.ID != "OBOCLIENT0000001" {
		t.Fatalf("got (%+v, %+v)", att, exe)
	}
}

func TestAttributionFor_ExecutorNil_ServiceInTxInheritsOrigin(t *testing.T) {
	ctx := spi.WithUserContext(context.Background(), &spi.UserContext{UserID: "C1", Kind: spi.PrincipalService})
	ctx = spi.WithTransaction(ctx, &spi.TransactionState{ID: "tx1", Origin: spi.Principal{ID: "alice", Kind: spi.PrincipalUser}})
	att, exe := spi.AttributionFor(ctx)
	if att.ID != "alice" || exe.ID != "C1" {
		t.Fatalf("got (%+v, %+v)", att, exe)
	}
}

// Outside a transaction, a service/system executor inherits the ambient
// origin, with the same precedence as ResolveOrigin: tx origin > ambient >
// the executor itself.
func TestAttributionFor_AmbientOrigin(t *testing.T) {
	armer := spi.Principal{ID: "alice", Kind: spi.PrincipalUser}
	txOrigin := spi.Principal{ID: "bob", Kind: spi.PrincipalUser}
	sys := spi.Principal{ID: "system", Kind: spi.PrincipalSystem}
	svc := spi.Principal{ID: "C1", Kind: spi.PrincipalService}
	oboClient := spi.Principal{ID: "OBOCLIENT0000001", Kind: spi.PrincipalService}
	usr := spi.Principal{ID: "carol", Kind: spi.PrincipalUser}

	cases := []struct {
		name         string
		uc           *spi.UserContext
		ambient      spi.Principal
		tx           *spi.TransactionState
		wantAtt      spi.Principal
		wantExecutor spi.Principal
	}{
		{"system executor, ambient, no tx", &spi.UserContext{UserID: sys.ID, Kind: sys.Kind}, armer, nil, armer, sys},
		{"service executor, ambient, no tx", &spi.UserContext{UserID: svc.ID, Kind: svc.Kind}, armer, nil, armer, svc},
		{"tx origin beats ambient", &spi.UserContext{UserID: sys.ID, Kind: sys.Kind}, armer,
			&spi.TransactionState{ID: "tx1", Origin: txOrigin}, txOrigin, sys},
		{"tx without origin falls back to ambient", &spi.UserContext{UserID: sys.ID, Kind: sys.Kind}, armer,
			&spi.TransactionState{ID: "tx1"}, armer, sys},
		{"user-kind executor records itself", &spi.UserContext{UserID: usr.ID, Kind: usr.Kind}, armer, nil, usr, usr},
		{"OBO ignores ambient", &spi.UserContext{UserID: usr.ID, Kind: usr.Kind, Executor: &oboClient}, armer, nil, usr, oboClient},
		{"no ambient, no tx", &spi.UserContext{UserID: sys.ID, Kind: sys.Kind}, spi.Principal{}, nil, sys, sys},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := spi.WithUserContext(context.Background(), tc.uc)
			ctx = spi.WithAmbientOrigin(ctx, tc.ambient)
			if tc.tx != nil {
				ctx = spi.WithTransaction(ctx, tc.tx)
			}
			att, exe := spi.AttributionFor(ctx)
			if att != tc.wantAtt {
				t.Errorf("attributed = %+v, want %+v", att, tc.wantAtt)
			}
			if exe != tc.wantExecutor {
				t.Errorf("executor = %+v, want %+v", exe, tc.wantExecutor)
			}
		})
	}
}
