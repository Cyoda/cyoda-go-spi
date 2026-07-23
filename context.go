package spi

import "context"

// TenantID is a named type for tenant identifiers, preventing accidental use of bare strings.
type TenantID string

// SystemTenantID is the well-known tenant for system-level data.
const SystemTenantID TenantID = "SYSTEM"

// Tenant is a first-class domain entity representing a tenant.
type Tenant struct {
	ID   TenantID
	Name string
}

// UserContext carries the authenticated user's identity through the request lifecycle.
type UserContext struct {
	UserID   string
	UserName string
	Kind     PrincipalKind
	Tenant   Tenant
	Roles    []string
}

type contextKey string

const userContextKey contextKey = "userContext"

func WithUserContext(ctx context.Context, uc *UserContext) context.Context {
	return context.WithValue(ctx, userContextKey, uc)
}

func GetUserContext(ctx context.Context) *UserContext {
	uc, _ := ctx.Value(userContextKey).(*UserContext)
	return uc
}

func MustGetUserContext(ctx context.Context) *UserContext {
	uc := GetUserContext(ctx)
	if uc == nil {
		panic("UserContext not found in context — auth middleware not applied")
	}
	return uc
}

// HasRole checks whether the target role is present in the roles slice.
func HasRole(roles []string, role string) bool {
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

// PrincipalKind classifies the actor identified by a Principal.
type PrincipalKind string

const (
	PrincipalUser    PrincipalKind = "user"
	PrincipalService PrincipalKind = "service"
	PrincipalSystem  PrincipalKind = "system"
)

// Principal identifies an actor and its explicit kind. The zero value means "absent".
type Principal struct {
	ID   string        `json:"id"`
	Kind PrincipalKind `json:"kind"`
}

const ambientOriginKey contextKey = "ambientOrigin"

// WithAmbientOrigin seeds the origin for a causal-chain root that has no
// transaction yet. Single legitimate seed site: the scheduled fire, from the
// durable task row. A zero Principal is never seeded.
func WithAmbientOrigin(ctx context.Context, p Principal) context.Context {
	if p == (Principal{}) {
		return ctx
	}
	return context.WithValue(ctx, ambientOriginKey, p)
}

// GetAmbientOrigin returns the ambient origin seeded via WithAmbientOrigin,
// or the zero Principal if none was seeded.
func GetAmbientOrigin(ctx context.Context) Principal {
	p, _ := ctx.Value(ambientOriginKey).(Principal)
	return p
}

// ResolveOrigin is the single shared origin-precedence implementation:
// parent-tx > ambient > UserContext. All backends MUST use it at Begin —
// divergence here is an attribution bug.
func ResolveOrigin(ctx context.Context) Principal {
	if tx := GetTransaction(ctx); tx != nil && tx.Origin != (Principal{}) {
		return tx.Origin
	}
	if amb := GetAmbientOrigin(ctx); amb != (Principal{}) {
		return amb
	}
	if uc := GetUserContext(ctx); uc != nil {
		return Principal{ID: uc.UserID, Kind: uc.Kind}
	}
	return Principal{}
}

// AttributionFor returns (attributed, executor) for a durable write staged
// under ctx. Origin inheritance engages only for service/system executors
// inside a transaction; a user-kind (or legacy unset-kind) executor records
// itself. Never elevates a non-joined write to a claimed user.
func AttributionFor(ctx context.Context) (attributed, executor Principal) {
	if uc := GetUserContext(ctx); uc != nil {
		executor = Principal{ID: uc.UserID, Kind: uc.Kind}
	}
	if executor.Kind == PrincipalService || executor.Kind == PrincipalSystem {
		if tx := GetTransaction(ctx); tx != nil && tx.Origin != (Principal{}) {
			return tx.Origin, executor
		}
	}
	return executor, executor
}
