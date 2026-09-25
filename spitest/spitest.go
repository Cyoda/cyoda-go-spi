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

	// IDOrder is the engine's canonical entity-ID comparator: a strict
	// three-way compare (a<b: negative, a==b: zero, a>b: positive) over two
	// entity ID strings, matching this backend's canonical entity-ID order
	// (see OrderSpec's doc comment — Path="id" ordering is per-engine, not
	// guaranteed identical across backends). Ordering conformance subtests
	// use this to assert Iterate's Ordered/EntityID output rather than
	// assuming byte-wise comparison. Optional; defaults to byte-wise
	// strings.Compare.
	IDOrder func(a, b string) int

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
	// StoreFactoryConformance fails the test if a key in Skip goes unmatched
	// while its top-level group actually ran. A key's group is its first
	// "/"-separated segment (e.g. "Transaction" for
	// "Transaction/TxStateErrors/OpAfterRollback"); the group is
	// considered to have run when some executed subtest starts with that
	// same segment. Matching itself is by exact string equality against
	// the running subtest's full path (skipIfRegistered's map lookup) —
	// a key that only ever names a path prefix, never a real subtest,
	// cannot be matched and is reported once its group runs.
	//
	// The check is group-grained, not deeper, because `-run` filtering in
	// this codebase is applied per group (e.g.
	// `go test -run 'TestConformance/AsyncSearch'`): a key's group not
	// running means the run excluded it and it gets no chance to match, so
	// it is not reported. A finer-grained check (matching a key's full
	// parent path) was tried and rejected: a typo in any segment before
	// the last — the common shape for this suite's multi-segment keys,
	// e.g. "Entity/CompareAndSave/Conflict" — would make the corrupted
	// parent path match nothing and silently swallow the typo, which
	// defeats the check for the key shapes actually in use. Group-only
	// still catches such typos, since the group segment is unaffected.
	// See the check in StoreFactoryConformance and skipTracker.unusedKeys
	// for the exact rule.
	Skip map[string]string
}

// skipTracker is a run-scoped record of every subtest that actually ran
// (recordRan) and which of those matched a Skip key (recordMatch). It is
// populated by skipIfRegistered and validated at the end of
// StoreFactoryConformance.
type skipTracker struct {
	mu      sync.Mutex
	ran     map[string]bool
	matched map[string]bool
}

func newSkipTracker() *skipTracker {
	return &skipTracker{ran: make(map[string]bool), matched: make(map[string]bool)}
}

// recordRan marks that the subtest at path name actually executed —
// skipIfRegistered was reached for it, so it was not excluded by `-run`
// filtering (whether or not it matched a Skip key).
func (st *skipTracker) recordRan(name string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.ran[name] = true
}

// recordMatch marks that the subtest at path name matched a Skip key and
// was skipped by it.
func (st *skipTracker) recordMatch(name string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.matched[name] = true
}

// unusedKeys returns the Skip keys that went unmatched despite their
// top-level group having run. A key is reported when it was not matched
// (no subtest's full path equalled the key exactly) AND some recorded ran
// path starts with the key's own top-level group — see the Skip field's
// doc comment for the exact rule and its rationale. A key whose group
// never ran (excluded by `-run` filtering) is not reported, since the run
// gave it no chance to match.
func (st *skipTracker) unusedKeys(skip map[string]string) []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []string
	for k := range skip {
		if st.matched[k] {
			continue
		}
		if st.groupRanLocked(k) {
			out = append(out, k)
		}
	}
	return out
}

// groupRanLocked reports whether some recorded ran path belongs to key's
// top-level group — its first "/"-separated segment. Callers must hold
// st.mu.
func (st *skipTracker) groupRanLocked(key string) bool {
	group, _, _ := strings.Cut(key, "/")
	for ran := range st.ran {
		if ranGroup, _, _ := strings.Cut(ran, "/"); ranGroup == group {
			return true
		}
	}
	return false
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
	tracker.recordRan(name)
	if reason, ok := h.Skip[name]; ok {
		tracker.recordMatch(name)
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
	if h.IDOrder == nil {
		h.IDOrder = strings.Compare
	}
	t.Cleanup(func() { _ = h.Factory.Close() })

	tracker := newSkipTracker()

	// Validate that every registered Skip key was actually hit, but only
	// among keys whose top-level group ran: a `-run`-filtered invocation
	// (e.g. `go test -run 'TestConformance/AsyncSearch'`) never executes
	// other groups' subtests, so their Skip keys get no chance to match and
	// must not fail the run. unusedKeys reports a key only when some
	// subtest in the same group did run and nothing matched the key
	// exactly — that's a real typo or stale entry, not filtering. The
	// check deliberately stops at the group segment rather than the key's
	// full parent path: a parent-path check would let a typo in any
	// segment before the last (the shape most of this suite's keys have,
	// e.g. "Entity/CompareAndSave/Conflict") pass unnoticed, because a
	// corrupted parent path matches no ran subtest and looks exactly like
	// "the group never ran". See skipTracker.
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
	t.Run("ScheduledTasks", func(t *testing.T) { runScheduledTasksSuite(t, h, tracker) })
	t.Run("Searcher", func(t *testing.T) { runSearcherSuite(t, h, tracker) })
	t.Run("Iterable", func(t *testing.T) { runIterableSuite(t, h, tracker) })
	t.Run("GroupedAggregator", func(t *testing.T) { runGroupedAggregatorSuite(t, h, tracker) })
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
