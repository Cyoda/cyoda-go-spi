package spitest

import (
	"context"
	"flag"
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
	// StoreFactoryConformance fails an UNFILTERED run (the `test.run` flag
	// empty) if any key in Skip was never matched — this catches typos and
	// stale entries by exact string equality, at any segment depth.
	//
	// The check does not run at all under any `-run` filter, at any
	// depth. This package documents focused invocations that filter below
	// the group level (e.g. `go test -run
	// 'TestConformance/Transaction/TxStateErrors/JoinAfterCommit'`), and
	// `-run` can filter at any segment depth — there is no fixed grain
	// (the whole key, its top-level group, ...) that tells "this key's
	// subtest was excluded by the filter" apart from "this key is a real
	// typo" in every case; matching at one grain mis-reports the other. A
	// full, unfiltered run (`make test`, CI) still catches every typo, at
	// every segment depth. See shouldCheckSkipKeys.
	Skip map[string]string
}

// skipTracker is a run-scoped set of which Skip keys were actually matched
// by a subtest (recordMatch). It is populated by skipIfRegistered and
// validated, on an unfiltered run only, at the end of
// StoreFactoryConformance. See shouldCheckSkipKeys.
type skipTracker struct {
	mu      sync.Mutex
	matched map[string]bool
}

func newSkipTracker() *skipTracker { return &skipTracker{matched: make(map[string]bool)} }

// recordMatch marks that the subtest at path name matched a Skip key and
// was skipped by it.
func (st *skipTracker) recordMatch(name string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.matched[name] = true
}

// unusedKeys returns the Skip keys that were never matched by any
// subtest. Callers gate this on shouldCheckSkipKeys: under a `-run`
// filter, a key can go unmatched simply because its subtest never ran, so
// the result is only meaningful for an unfiltered run.
func (st *skipTracker) unusedKeys(skip map[string]string) []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []string
	for k := range skip {
		if !st.matched[k] {
			out = append(out, k)
		}
	}
	return out
}

// shouldCheckSkipKeys reports whether the unmatched-Skip-key check should
// run, given the current value of the `test.run` flag (from
// flag.Lookup("test.run").Value.String()). It is true only for an
// unfiltered run (runFlag == ""). `-run` can filter at any segment depth,
// and no fixed grain of comparison can tell "excluded by the filter" apart
// from "a real typo" for every possible filter depth — see the Skip
// field's doc comment — so the check simply does not run under any
// filter, and always runs when there is none.
func shouldCheckSkipKeys(runFlag string) bool {
	return runFlag == ""
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

	// Validate that every registered Skip key was actually hit — but only
	// on an unfiltered run. `-run` can filter this suite at any segment
	// depth (this package documents invocations filtering below the group
	// level), and no fixed grain of "did this key's subtest have a chance
	// to run" comparison is correct at every depth: it either fails a
	// focused run over a key excluded by the filter, or lets a real typo
	// pass. So the check is skipped entirely under any `-run` filter and
	// only runs unfiltered, where every subtest ran and an unmatched key
	// is unambiguously a typo or a stale entry. Full runs (`make test`,
	// CI) are always unfiltered, so this loses no coverage.
	if runFlag := flag.Lookup("test.run"); runFlag == nil || shouldCheckSkipKeys(runFlag.Value.String()) {
		t.Cleanup(func() {
			for _, key := range tracker.unusedKeys(h.Skip) {
				t.Errorf("Harness.Skip key %q was never matched — possible typo or stale entry", key)
			}
		})
	}

	t.Run("Transaction", func(t *testing.T) { runTransactionSuite(t, h, tracker) })
	t.Run("Entity", func(t *testing.T) { runEntitySuite(t, h, tracker) })
	t.Run("ConsistencyTime", func(t *testing.T) { runConsistencyTimeSuite(t, h, tracker) })
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
