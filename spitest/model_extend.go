package spitest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// toyApply is the schema fold the ExtendSchema cases install. A schema is
// {"fields":[...]} (sorted, deduplicated); a delta is a JSON array of field
// names; applying a delta is the sorted union. apply(s, nil) re-encodes s
// identically.
func toyApply(base []byte, delta spi.SchemaDelta) ([]byte, error) {
	var s struct {
		Fields []string `json:"fields"`
	}
	if len(base) > 0 {
		if err := json.Unmarshal(base, &s); err != nil {
			return nil, fmt.Errorf("toy schema: %w", err)
		}
	}
	set := make(map[string]struct{}, len(s.Fields))
	for _, f := range s.Fields {
		set[f] = struct{}{}
	}
	if len(delta) > 0 {
		var d []string
		if err := json.Unmarshal(delta, &d); err != nil {
			return nil, fmt.Errorf("toy delta: %w", err)
		}
		for _, f := range d {
			set[f] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	sort.Strings(out)
	s.Fields = out
	return json.Marshal(s)
}

func toyDelta(fields ...string) spi.SchemaDelta {
	b, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return spi.SchemaDelta(b)
}

func toySchema(fields ...string) string {
	b, err := json.Marshal(map[string][]string{"fields": fields})
	if err != nil {
		panic(err)
	}
	return string(b)
}

type applyFuncSetter interface {
	SetApplyFunc(func(base []byte, delta spi.SchemaDelta) ([]byte, error))
}

func runModelExtendSuite(t *testing.T, h Harness, tracker *skipTracker) {
	setter, ok := h.Factory.(applyFuncSetter)
	if !ok {
		t.Skip("factory has no SetApplyFunc; ExtendSchema cases need the toy apply function installed")
	}
	// A factory accepts its apply function once, so it is wired once for the
	// whole suite.
	var once sync.Once
	wire := func() { once.Do(func() { setter.SetApplyFunc(toyApply) }) }
	wire()

	run := func(name string, fn func(*testing.T, Harness)) {
		runSubtest(t, h, tracker, name, fn)
	}
	run("ExtendSchema/VisibleOnReturn", testExtendVisibleOnReturn)
	run("ExtendSchema/IgnoresCallerTransaction", testExtendIgnoresCallerTx)
	run("ExtendSchema/GetIgnoresCallerTransaction", testExtendGetIgnoresCallerTx)
	run("ExtendSchema/NoOpWritesNothing", testExtendNoOp)
	run("ExtendSchema/ConcurrentExtendersAllSucceed", testExtendConcurrent)
	run("ExtendSchema/NotLockedRefused", testExtendNotLockedRefused)
	run("ExtendSchema/MissingModel", testExtendMissingModel)
}

// lockedToyModel saves a model with schema {"fields":["a"]} on a fresh
// tenant and locks it.
func lockedToyModel(t *testing.T, h Harness) (context.Context, spi.ModelStore, spi.ModelRef) {
	t.Helper()
	ctx := tenantContext(h.NewTenant())
	ms, err := h.Factory.ModelStore(ctx)
	require.NoError(t, err)
	md := newModelDescriptor("ext", "1")
	md.Schema = []byte(toySchema("a"))
	require.NoError(t, ms.Save(ctx, md))
	require.NoError(t, ms.Lock(ctx, md.Ref))
	return ctx, ms, md.Ref
}

func schemaOf(t *testing.T, ctx context.Context, ms spi.ModelStore, ref spi.ModelRef) string {
	t.Helper()
	got, err := ms.Get(ctx, ref)
	require.NoError(t, err)
	return string(got.Schema)
}

func testExtendVisibleOnReturn(t *testing.T, h Harness) {
	ctx, ms, ref := lockedToyModel(t, h)
	require.NoError(t, ms.ExtendSchema(ctx, ref, toyDelta("b")))
	require.Equal(t, toySchema("a", "b"), schemaOf(t, ctx, ms, ref))
}

func testExtendIgnoresCallerTx(t *testing.T, h Harness) {
	ctx, ms, ref := lockedToyModel(t, h)
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID, txCtx, err := tm.Begin(ctx)
	require.NoError(t, err)
	extendErr := ms.ExtendSchema(txCtx, ref, toyDelta("b"))
	require.NoError(t, tm.Rollback(txCtx, txID))
	require.NoError(t, extendErr)
	require.Contains(t, schemaOf(t, ctx, ms, ref), `"b"`,
		"an extension commits on its own and survives the caller's rollback")
}

func testExtendGetIgnoresCallerTx(t *testing.T, h Harness) {
	ctx, ms, ref := lockedToyModel(t, h)
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID, txCtx, err := tm.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tm.Rollback(txCtx, txID) }()

	require.NoError(t, ms.ExtendSchema(ctx, ref, toyDelta("c")))
	require.Contains(t, schemaOf(t, txCtx, ms, ref), `"c"`,
		"Get reads the latest committed model whatever transaction ctx carries")
}

func testExtendNoOp(t *testing.T, h Harness) {
	ctx, ms, ref := lockedToyModel(t, h)
	require.NoError(t, ms.ExtendSchema(ctx, ref, toyDelta("b")))
	require.NoError(t, ms.ExtendSchema(ctx, ref, toyDelta("b")))
	require.Equal(t, toySchema("a", "b"), schemaOf(t, ctx, ms, ref))
	require.NoError(t, ms.ExtendSchema(ctx, ref, toyDelta("c")))
	require.Equal(t, toySchema("a", "b", "c"), schemaOf(t, ctx, ms, ref))
}

func testExtendConcurrent(t *testing.T, h Harness) {
	ctx, ms, ref := lockedToyModel(t, h)
	const n = 16
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = ms.ExtendSchema(ctx, ref, toyDelta(fmt.Sprintf("f%d", i)))
		}()
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "extender %d", i)
		require.False(t, errors.Is(err, spi.ErrConflict) || errors.Is(err, spi.ErrRetryExhausted))
	}
	fields := []string{"a"}
	for i := 0; i < n; i++ {
		fields = append(fields, fmt.Sprintf("f%d", i))
	}
	want, err := toyApply(nil, toyDelta(fields...))
	require.NoError(t, err)
	require.Equal(t, string(want), schemaOf(t, ctx, ms, ref), "every extension that returned nil is in the schema")
}

func testExtendNotLockedRefused(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ms, err := h.Factory.ModelStore(ctx)
	require.NoError(t, err)
	md := newModelDescriptor("ext-unlocked", "1")
	md.Schema = []byte(toySchema("a"))
	require.NoError(t, ms.Save(ctx, md))

	err = ms.ExtendSchema(ctx, md.Ref, toyDelta("b"))
	require.ErrorIs(t, err, spi.ErrModelNotLocked)
	require.Equal(t, toySchema("a"), schemaOf(t, ctx, ms, md.Ref))
}

func testExtendMissingModel(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ms, err := h.Factory.ModelStore(ctx)
	require.NoError(t, err)
	err = ms.ExtendSchema(ctx, spi.ModelRef{EntityName: "never", ModelVersion: "1"}, toyDelta("b"))
	require.ErrorIs(t, err, spi.ErrNotFound)
}
