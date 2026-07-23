package spitest

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// Harness bundles a StoreFactory under test with the hooks the conformance
// suite needs. Plugin authors construct one in their test function and
// pass it to StoreFactoryConformance.
type Harness struct {
	// Factory is the StoreFactory under test. StoreFactoryConformance
	// calls Factory.Close when the suite finishes.
	Factory spi.StoreFactory

	// AdvanceClock moves the plugin's virtual clock forward by d.
	// Contract: after AdvanceClock returns, every subsequent timestamp
	// the plugin assigns strictly dominates every timestamp assigned
	// before the call. d must be > 0; d <= 0 panics.
	AdvanceClock func(d time.Duration)

	// Now returns the plugin's current clock time. Temporal tests use this
	// to capture "asAt" markers that are consistent with the plugin's clock.
	// Optional; defaults to time.Now.
	Now func() time.Time

	// NewTenant returns a fresh tenant ID unique within this process.
	// The harness invokes this at the start of every subtest; no subtest
	// reuses another's tenant. Optional; defaults to a uuid-based generator.
	NewTenant func() spi.TenantID

	// Skip is an optional map from subtest path suffix to skip reason.
	// Keys must be the path below the root test name, e.g.:
	//
	//   "Transaction/Join"
	//   "Entity/CompareAndSave/Conflict"
	//   "AsyncSearch/UpdateStatus/Succeeded"
	//   "AsyncSearch/SaveAndGetResults/Pagination"
	//   "AsyncSearch/Cancel"
	//   "AsyncSearch/ReapExpired"
	//
	// When a running subtest's name (stripped of the root prefix) matches a
	// key, the harness calls t.Skipf(reason) at the start of that subtest.
	// Backends with known structural incompatibilities populate this to
	// prevent false failures while documenting the open issues.
	//
	// StoreFactoryConformance fails the test if any key in Skip was never
	// matched — this catches typos in key names.
	Skip map[string]string
}

// skipTracker is a run-scoped set of which Skip keys were actually hit.
// It is populated by runSubtest / skipIfRegistered and validated at the
// end of StoreFactoryConformance.
type skipTracker struct {
	mu   sync.Mutex
	used map[string]bool
}

func newSkipTracker() *skipTracker { return &skipTracker{used: make(map[string]bool)} }

func (st *skipTracker) record(key string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.used[key] = true
}

func (st *skipTracker) unusedKeys(skip map[string]string) []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []string
	for k := range skip {
		if !st.used[k] {
			out = append(out, k)
		}
	}
	return out
}

// skipIfRegistered calls t.Skipf if the current subtest's path suffix
// appears in h.Skip. The suffix is t.Name() with the root test prefix
// (everything up to and including the first '/') stripped.
func skipIfRegistered(t *testing.T, h Harness, tracker *skipTracker) {
	t.Helper()
	name := t.Name()
	if idx := strings.Index(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	if reason, ok := h.Skip[name]; ok {
		tracker.record(name)
		t.Skipf("skipped by plugin: %s", reason)
	}
}

// runSubtest wraps t.Run so every subtest automatically receives a skip
// check before fn runs. This replaces per-subtest skipIfRegistered calls.
func runSubtest(t *testing.T, h Harness, tracker *skipTracker, name string, fn func(*testing.T, Harness)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		skipIfRegistered(t, h, tracker)
		fn(t, h)
	})
}

// StoreFactoryConformance runs the full conformance suite against h.
// Plugin authors call this from a single top-level test function.
func StoreFactoryConformance(t *testing.T, h Harness) {
	t.Helper()
	mustBeSet(t, h.Factory != nil, "Harness.Factory must be set")
	mustBeSet(t, h.AdvanceClock != nil, "Harness.AdvanceClock must be set")
	if h.Now == nil {
		h.Now = time.Now
	}
	if h.NewTenant == nil {
		h.NewTenant = defaultNewTenant
	}
	t.Cleanup(func() { _ = h.Factory.Close() })

	tracker := newSkipTracker()

	// Validate that every registered Skip key was actually hit during the run.
	// Unmatched keys indicate typos or stale entries.
	t.Cleanup(func() {
		for _, key := range tracker.unusedKeys(h.Skip) {
			t.Errorf("Harness.Skip key %q was never matched — possible typo or stale entry", key)
		}
	})

	t.Run("Transaction", func(t *testing.T) { runTransactionSuite(t, h, tracker) })
	t.Run("Entity", func(t *testing.T) { runEntitySuite(t, h, tracker) })
	t.Run("Model", func(t *testing.T) { runModelSuite(t, h, tracker) })
	t.Run("KeyValue", func(t *testing.T) { runKeyValueSuite(t, h, tracker) })
	t.Run("Message", func(t *testing.T) { runMessageSuite(t, h, tracker) })
	t.Run("Workflow", func(t *testing.T) { runWorkflowSuite(t, h, tracker) })
	t.Run("Audit", func(t *testing.T) { runAuditSuite(t, h, tracker) })
	t.Run("AsyncSearch", func(t *testing.T) { runAsyncSearchSuite(t, h, tracker) })
}

func defaultNewTenant() spi.TenantID {
	return spi.TenantID("conformance-" + uuid.NewString())
}

// tenantContext returns a background context carrying a synthetic
// UserContext for the given tenant, sufficient for plugin tenant
// resolution. Kind is left at its zero value; tests that care about
// attribution use tenantContextAs instead.
func tenantContext(tenant spi.TenantID) context.Context {
	return spi.WithUserContext(context.Background(), &spi.UserContext{
		UserID:   "conformance-test",
		UserName: "conformance",
		Tenant:   spi.Tenant{ID: tenant, Name: string(tenant)},
	})
}

// tenantContextAs returns a background context carrying a synthetic
// UserContext for the given tenant with an explicit userID and
// PrincipalKind. Used by attribution conformance tests (origin capture,
// executor round-trip) that need deterministic control over Kind —
// tenantContext's fixed "conformance-test" user with zero-value Kind
// isn't distinguishable across actors and isn't attribution-shaped.
func tenantContextAs(tenant spi.TenantID, userID string, kind spi.PrincipalKind) context.Context {
	return spi.WithUserContext(context.Background(), &spi.UserContext{
		UserID:   userID,
		UserName: userID,
		Kind:     kind,
		Tenant:   spi.Tenant{ID: tenant, Name: string(tenant)},
	})
}

// mustBeSet is a local tiny assertion so this file doesn't depend on testify
// at the entry-point level. Per-subtest files may use testify.
func mustBeSet(t *testing.T, cond bool, msg string) {
	t.Helper()
	if !cond {
		t.Fatal(msg)
	}
}
