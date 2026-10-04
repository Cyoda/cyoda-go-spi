package spitest

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

func runKeyValueSuite(t *testing.T, h Harness, tracker *skipTracker) {
	runSubtest(t, h, tracker, "PutAndGet", testKVPutAndGet)
	runSubtest(t, h, tracker, "Get/NotFound", testKVGetNotFound)
	runSubtest(t, h, tracker, "Overwrite", testKVOverwrite)
	runSubtest(t, h, tracker, "Delete", testKVDelete)
	runSubtest(t, h, tracker, "DeleteAbsent", testKVDeleteAbsent)
	runSubtest(t, h, tracker, "List/Namespace", testKVListNamespace)
	runSubtest(t, h, tracker, "TenantIsolation", testKVTenantIsolation)
	runSubtest(t, h, tracker, "Value/BinarySafe", testKVBinarySafe)
	runSubtest(t, h, tracker, "Conditional/PutIfAbsent", testKVPutIfAbsent)
	runSubtest(t, h, tracker, "Conditional/CompareAndPut", testKVCompareAndPut)
	runSubtest(t, h, tracker, "Conditional/DeleteIfEqual", testKVDeleteIfEqual)
	runSubtest(t, h, tracker, "Conditional/DeletedKeyIsAbsent", testKVConditionalDeletedKey)
	runSubtest(t, h, tracker, "Conditional/ConcurrentPutIfAbsent", testKVConcurrentPutIfAbsent)
	runSubtest(t, h, tracker, "Conditional/ConcurrentCompareAndPut", testKVConcurrentCompareAndPut)
	runSubtest(t, h, tracker, "Conditional/AtomicAgainstPlainWrites", testKVConditionalAgainstPlain)
	runSubtest(t, h, tracker, "Conditional/Isolation", testKVConditionalIsolation)
	runSubtest(t, h, tracker, "Conditional/EmptyValue", testKVConditionalEmptyValue)
	runSubtest(t, h, tracker, "Conditional/ByteForByte", testKVConditionalByteForByte)
	runSubtest(t, h, tracker, "NoTransactionJoin", testKVNoTransactionJoin)
}

func testKVPutAndGet(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	kv, err := h.Factory.KeyValueStore(ctx)
	require.NoError(t, err)
	require.NoError(t, kv.Put(ctx, "ns1", "k1", []byte("v1")))
	got, err := kv.Get(ctx, "ns1", "k1")
	require.NoError(t, err)
	require.Equal(t, []byte("v1"), got)
}

func testKVGetNotFound(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	kv, _ := h.Factory.KeyValueStore(ctx)
	_, err := kv.Get(ctx, "ns", "missing")
	require.ErrorIs(t, err, spi.ErrNotFound)
}

func testKVOverwrite(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	kv, _ := h.Factory.KeyValueStore(ctx)
	require.NoError(t, kv.Put(ctx, "ns", "k", []byte("old")))
	require.NoError(t, kv.Put(ctx, "ns", "k", []byte("new")))
	got, err := kv.Get(ctx, "ns", "k")
	require.NoError(t, err)
	require.Equal(t, []byte("new"), got)
}

func testKVDelete(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	kv, _ := h.Factory.KeyValueStore(ctx)
	require.NoError(t, kv.Put(ctx, "ns", "k", []byte("v")))
	require.NoError(t, kv.Delete(ctx, "ns", "k"))
	_, err := kv.Get(ctx, "ns", "k")
	require.ErrorIs(t, err, spi.ErrNotFound)
}

// Deleting a key that is absent — never written, or already deleted — is
// not an error.
func testKVDeleteAbsent(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	kv, _ := h.Factory.KeyValueStore(ctx)
	require.NoError(t, kv.Delete(ctx, "ns", "never-written"))
	require.NoError(t, kv.Put(ctx, "ns", "k", []byte("v")))
	require.NoError(t, kv.Delete(ctx, "ns", "k"))
	require.NoError(t, kv.Delete(ctx, "ns", "k"))
}

func testKVListNamespace(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	kv, _ := h.Factory.KeyValueStore(ctx)
	require.NoError(t, kv.Put(ctx, "ns1", "a", []byte("1")))
	require.NoError(t, kv.Put(ctx, "ns1", "b", []byte("2")))
	require.NoError(t, kv.Put(ctx, "ns2", "c", []byte("3")))
	ns1, err := kv.List(ctx, "ns1")
	require.NoError(t, err)
	require.Len(t, ns1, 2)
	require.Equal(t, []byte("1"), ns1["a"])
	require.Equal(t, []byte("2"), ns1["b"])
	ns2, err := kv.List(ctx, "ns2")
	require.NoError(t, err)
	require.Len(t, ns2, 1)
}

func testKVTenantIsolation(t *testing.T, h Harness) {
	tA, tB := h.NewTenant(), h.NewTenant()
	kvA, _ := h.Factory.KeyValueStore(tenantContext(tA))
	kvB, _ := h.Factory.KeyValueStore(tenantContext(tB))
	require.NoError(t, kvA.Put(tenantContext(tA), "ns", "shared-key", []byte("A")))
	require.NoError(t, kvB.Put(tenantContext(tB), "ns", "shared-key", []byte("B")))
	gotA, _ := kvA.Get(tenantContext(tA), "ns", "shared-key")
	gotB, _ := kvB.Get(tenantContext(tB), "ns", "shared-key")
	require.Equal(t, []byte("A"), gotA)
	require.Equal(t, []byte("B"), gotB)
}

func testKVBinarySafe(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	kv, _ := h.Factory.KeyValueStore(ctx)
	payload := []byte{0x00, 0xFF, 0x01, 0x7F, 0x80, 0xDE, 0xAD, 0xBE, 0xEF, 0x00}
	require.NoError(t, kv.Put(ctx, "ns", "bin", payload))
	got, err := kv.Get(ctx, "ns", "bin")
	require.NoError(t, err)
	require.Equal(t, payload, got)
}

// applied asserts a conditional write returned no error and the given applied.
func applied(t *testing.T, want bool, got bool, err error) {
	t.Helper()
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func testKVPutIfAbsent(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	kv, err := h.Factory.KeyValueStore(ctx)
	require.NoError(t, err)
	ok, err := kv.PutIfAbsent(ctx, "ns", "k", []byte("v1"))
	applied(t, true, ok, err)
	ok, err = kv.PutIfAbsent(ctx, "ns", "k", []byte("v2"))
	applied(t, false, ok, err)
	got, err := kv.Get(ctx, "ns", "k")
	require.NoError(t, err)
	require.Equal(t, []byte("v1"), got)
}

func testKVCompareAndPut(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	kv, _ := h.Factory.KeyValueStore(ctx)
	ok, err := kv.CompareAndPut(ctx, "ns", "k", []byte("x"), []byte("y"))
	applied(t, false, ok, err) // absent
	_, err = kv.Get(ctx, "ns", "k")
	require.ErrorIs(t, err, spi.ErrNotFound)
	require.NoError(t, kv.Put(ctx, "ns", "k", []byte("v1")))
	ok, err = kv.CompareAndPut(ctx, "ns", "k", []byte("other"), []byte("v2"))
	applied(t, false, ok, err) // different
	ok, err = kv.CompareAndPut(ctx, "ns", "k", []byte("v1"), []byte("v2"))
	applied(t, true, ok, err)
	got, _ := kv.Get(ctx, "ns", "k")
	require.Equal(t, []byte("v2"), got)
}

func testKVDeleteIfEqual(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	kv, _ := h.Factory.KeyValueStore(ctx)
	ok, err := kv.DeleteIfEqual(ctx, "ns", "k", []byte("v"))
	applied(t, false, ok, err) // absent
	require.NoError(t, kv.Put(ctx, "ns", "k", []byte("v1")))
	ok, err = kv.DeleteIfEqual(ctx, "ns", "k", []byte("stale"))
	applied(t, false, ok, err)
	got, err := kv.Get(ctx, "ns", "k")
	require.NoError(t, err)
	require.Equal(t, []byte("v1"), got)
	ok, err = kv.DeleteIfEqual(ctx, "ns", "k", []byte("v1"))
	applied(t, true, ok, err)
	_, err = kv.Get(ctx, "ns", "k")
	require.ErrorIs(t, err, spi.ErrNotFound)
}

// A key written and then deleted is absent to every conditional write; a
// backend that soft-deletes must not compare against the deleted value.
func testKVConditionalDeletedKey(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	kv, _ := h.Factory.KeyValueStore(ctx)
	require.NoError(t, kv.Put(ctx, "ns", "k", []byte("old")))
	require.NoError(t, kv.Delete(ctx, "ns", "k"))
	ok, err := kv.CompareAndPut(ctx, "ns", "k", []byte("old"), []byte("x"))
	applied(t, false, ok, err)
	ok, err = kv.DeleteIfEqual(ctx, "ns", "k", []byte("old"))
	applied(t, false, ok, err)
	ok, err = kv.PutIfAbsent(ctx, "ns", "k", []byte("new"))
	applied(t, true, ok, err)
	got, err := kv.Get(ctx, "ns", "k")
	require.NoError(t, err)
	require.Equal(t, []byte("new"), got)
	all, err := kv.List(ctx, "ns")
	require.NoError(t, err)
	require.Equal(t, map[string][]byte{"k": []byte("new")}, all)
}

const conditionalRacers = 8

// At most one of N concurrent PutIfAbsent calls with distinct values is
// applied, and the stored value is that caller's.
func testKVConcurrentPutIfAbsent(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	kv, _ := h.Factory.KeyValueStore(ctx)
	for round := 0; round < 5; round++ {
		key := fmt.Sprintf("k%d", round)
		var wg sync.WaitGroup
		results := make([]bool, conditionalRacers)
		errs := make([]error, conditionalRacers)
		for i := range conditionalRacers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i], errs[i] = kv.PutIfAbsent(ctx, "ns", key, []byte(fmt.Sprintf("v%d", i)))
			}()
		}
		wg.Wait()
		winner := -1
		for i := range conditionalRacers {
			if errs[i] == nil && results[i] {
				require.Equal(t, -1, winner, "two PutIfAbsent calls applied")
				winner = i
			}
		}
		anyErr := false
		for _, e := range errs {
			anyErr = anyErr || e != nil
		}
		if winner < 0 {
			require.True(t, anyErr, "round %d: no PutIfAbsent applied and none errored", round)
			continue // a racer errored: outcome unknown, nothing more to assert
		}
		want := []byte(fmt.Sprintf("v%d", winner))
		got, err := kv.Get(ctx, "ns", key)
		require.NoError(t, err)
		require.Equal(t, want, got)
		all, err := kv.List(ctx, "ns")
		require.NoError(t, err)
		require.Equal(t, want, all[key])
	}
}

// At most one of N concurrent CompareAndPut calls from the same expected
// value is applied, and the stored value is that caller's.
func testKVConcurrentCompareAndPut(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	kv, _ := h.Factory.KeyValueStore(ctx)
	for round := 0; round < 5; round++ {
		key := fmt.Sprintf("k%d", round)
		require.NoError(t, kv.Put(ctx, "ns", key, []byte("start")))
		var wg sync.WaitGroup
		results := make([]bool, conditionalRacers)
		errs := make([]error, conditionalRacers)
		for i := range conditionalRacers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i], errs[i] = kv.CompareAndPut(ctx, "ns", key, []byte("start"), []byte(fmt.Sprintf("v%d", i)))
			}()
		}
		wg.Wait()
		winner := -1
		for i := range conditionalRacers {
			if errs[i] == nil && results[i] {
				require.Equal(t, -1, winner, "two CompareAndPut calls applied")
				winner = i
			}
		}
		anyErr := false
		for _, e := range errs {
			anyErr = anyErr || e != nil
		}
		if winner < 0 {
			require.True(t, anyErr, "round %d: no CompareAndPut applied and none errored", round)
			continue
		}
		got, err := kv.Get(ctx, "ns", key)
		require.NoError(t, err)
		require.Equal(t, []byte(fmt.Sprintf("v%d", winner)), got)
	}
}

// Conditional and plain writes are atomic against each other. In either
// order of the two racing calls the end state is the same, so it is asserted
// directly.
func testKVConditionalAgainstPlain(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	kv, _ := h.Factory.KeyValueStore(ctx)
	for round := 0; round < 20; round++ {
		key := fmt.Sprintf("d%d", round)
		require.NoError(t, kv.Put(ctx, "ns", key, []byte("prev")))
		var wg sync.WaitGroup
		var delErr error
		wg.Add(2)
		go func() { defer wg.Done(); delErr = kv.Delete(ctx, "ns", key) }()
		go func() { defer wg.Done(); _, _ = kv.CompareAndPut(ctx, "ns", key, []byte("prev"), []byte("x")) }()
		wg.Wait()
		require.NoError(t, delErr, "round %d: Delete", round)
		_, err := kv.Get(ctx, "ns", key)
		require.ErrorIs(t, err, spi.ErrNotFound, "round %d: a CompareAndPut outlived a Delete", round)
	}
	for round := 0; round < 20; round++ {
		key := fmt.Sprintf("p%d", round)
		var wg sync.WaitGroup
		var putErr error
		wg.Add(2)
		go func() { defer wg.Done(); putErr = kv.Put(ctx, "ns", key, []byte("v")) }()
		go func() { defer wg.Done(); _, _ = kv.PutIfAbsent(ctx, "ns", key, []byte("w")) }()
		wg.Wait()
		require.NoError(t, putErr, "round %d: Put", round)
		got, err := kv.Get(ctx, "ns", key)
		require.NoError(t, err)
		require.Equal(t, []byte("v"), got, "round %d: PutIfAbsent overwrote a Put", round)
	}
}

// A conditional write never sees or changes the same key in another tenant
// or another namespace.
func testKVConditionalIsolation(t *testing.T, h Harness) {
	tA, tB := h.NewTenant(), h.NewTenant()
	ctxA, ctxB := tenantContext(tA), tenantContext(tB)
	kvA, _ := h.Factory.KeyValueStore(ctxA)
	kvB, _ := h.Factory.KeyValueStore(ctxB)
	require.NoError(t, kvB.Put(ctxB, "ns", "k", []byte("B")))
	require.NoError(t, kvA.Put(ctxA, "other", "k", []byte("A-other")))
	ok, err := kvA.PutIfAbsent(ctxA, "ns", "k", []byte("A"))
	applied(t, true, ok, err)
	ok, err = kvA.CompareAndPut(ctxA, "ns", "k", []byte("B"), []byte("x"))
	applied(t, false, ok, err)
	ok, err = kvA.DeleteIfEqual(ctxA, "ns", "k", []byte("A-other"))
	applied(t, false, ok, err)
	gotB, _ := kvB.Get(ctxB, "ns", "k")
	require.Equal(t, []byte("B"), gotB)
	gotOther, _ := kvA.Get(ctxA, "other", "k")
	require.Equal(t, []byte("A-other"), gotOther)
}

func testKVConditionalEmptyValue(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	kv, _ := h.Factory.KeyValueStore(ctx)
	ok, err := kv.PutIfAbsent(ctx, "ns", "k", nil)
	applied(t, true, ok, err)
	ok, err = kv.CompareAndPut(ctx, "ns", "k", []byte{}, []byte("v"))
	applied(t, true, ok, err)

	// An empty expected value never matches an absent key.
	for _, expected := range [][]byte{nil, {}} {
		ok, err = kv.CompareAndPut(ctx, "ns", "absent", expected, []byte("v"))
		applied(t, false, ok, err)
		_, err = kv.Get(ctx, "ns", "absent")
		require.ErrorIs(t, err, spi.ErrNotFound)
	}
	ok, err = kv.DeleteIfEqual(ctx, "ns", "absent", nil)
	applied(t, false, ok, err)

	// A stored empty value reads back as empty, not as absent, and is
	// deletable by an empty expected value.
	ok, err = kv.PutIfAbsent(ctx, "ns", "empty", nil)
	applied(t, true, ok, err)
	got, err := kv.Get(ctx, "ns", "empty")
	require.NoError(t, err)
	require.Len(t, got, 0)
	ok, err = kv.DeleteIfEqual(ctx, "ns", "empty", []byte{})
	applied(t, true, ok, err)
	_, err = kv.Get(ctx, "ns", "empty")
	require.ErrorIs(t, err, spi.ErrNotFound)
}

// Values are compared byte for byte: no case folding, no trimming, no prefix
// match.
func testKVConditionalByteForByte(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	kv, _ := h.Factory.KeyValueStore(ctx)
	require.NoError(t, kv.Put(ctx, "ns", "k", []byte("v1")))
	for _, expected := range []string{"V1", "v1\x00", "v", "v12"} {
		ok, err := kv.CompareAndPut(ctx, "ns", "k", []byte(expected), []byte("x"))
		applied(t, false, ok, err)
	}
	ok, err := kv.DeleteIfEqual(ctx, "ns", "k", []byte("V1"))
	applied(t, false, ok, err)
	got, err := kv.Get(ctx, "ns", "k")
	require.NoError(t, err)
	require.Equal(t, []byte("v1"), got)
}

// No key-value operation joins a transaction: a write made with a context
// that carries an open transaction is visible outside it at once and survives
// its rollback, and a read with such a context sees a value committed after
// the transaction began.
func testKVNoTransactionJoin(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	kv, _ := h.Factory.KeyValueStore(ctx)
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID, txCtx := beginGuarded(t, tm, ctx)

	require.NoError(t, kv.Put(txCtx, "ns", "put", []byte("v")))
	ok, err := kv.PutIfAbsent(txCtx, "ns", "pia", []byte("v"))
	applied(t, true, ok, err)
	require.NoError(t, kv.Put(ctx, "ns", "cap", []byte("a")))
	ok, err = kv.CompareAndPut(txCtx, "ns", "cap", []byte("a"), []byte("b"))
	applied(t, true, ok, err)
	require.NoError(t, kv.Put(ctx, "ns", "del", []byte("v")))
	require.NoError(t, kv.Delete(txCtx, "ns", "del"))
	require.NoError(t, kv.Put(ctx, "ns", "die", []byte("v")))
	ok, err = kv.DeleteIfEqual(txCtx, "ns", "die", []byte("v"))
	applied(t, true, ok, err)

	// Visible outside the transaction before it ends.
	for k, want := range map[string]string{"put": "v", "pia": "v", "cap": "b"} {
		got, err := kv.Get(ctx, "ns", k)
		require.NoError(t, err, k)
		require.Equal(t, []byte(want), got, k)
	}
	for _, k := range []string{"del", "die"} {
		_, err := kv.Get(ctx, "ns", k)
		require.ErrorIs(t, err, spi.ErrNotFound, k)
	}
	// A read inside sees a value committed after the transaction began.
	require.NoError(t, kv.Put(ctx, "ns", "late", []byte("late")))
	got, err := kv.Get(txCtx, "ns", "late")
	require.NoError(t, err)
	require.Equal(t, []byte("late"), got)
	all, err := kv.List(txCtx, "ns")
	require.NoError(t, err)
	require.Equal(t, []byte("late"), all["late"])

	require.NoError(t, tm.Rollback(txCtx, txID))
	for k, want := range map[string]string{"put": "v", "pia": "v", "cap": "b"} {
		got, err := kv.Get(ctx, "ns", k)
		require.NoError(t, err, k)
		require.Equal(t, []byte(want), got, k)
	}
	for _, k := range []string{"del", "die"} {
		_, err := kv.Get(ctx, "ns", k)
		require.ErrorIs(t, err, spi.ErrNotFound, k)
	}
}
