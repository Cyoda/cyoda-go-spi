package spitest

import (
	"testing"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

func runWorkflowSuite(t *testing.T, h Harness, tracker *skipTracker) {
	runSubtest(t, h, tracker, "SaveAndGet", testWfSaveAndGet)
	runSubtest(t, h, tracker, "Get/EmptyModel", testWfGetEmpty)
	runSubtest(t, h, tracker, "Overwrite", testWfOverwrite)
	runSubtest(t, h, tracker, "Delete", testWfDelete)
	runSubtest(t, h, tracker, "DeleteAbsent", testWfDeleteAbsent)
	runSubtest(t, h, tracker, "TenantIsolation", testWfTenantIsolation)
}

func newWorkflowDef(name string) spi.WorkflowDefinition {
	return spi.WorkflowDefinition{Name: name, Version: "1", InitialState: "initial", Active: true,
		States: map[string]spi.StateDefinition{"initial": {}}}
}

func testWfSaveAndGet(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ws, err := h.Factory.WorkflowStore(ctx)
	require.NoError(t, err)
	mref := spi.ModelRef{EntityName: "m1", ModelVersion: "1"}
	defs := []spi.WorkflowDefinition{newWorkflowDef("wf1"), newWorkflowDef("wf2")}
	require.NoError(t, ws.Save(ctx, mref, defs))
	got, err := ws.Get(ctx, mref)
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func testWfGetEmpty(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ws, _ := h.Factory.WorkflowStore(ctx)
	got, err := ws.Get(ctx, spi.ModelRef{EntityName: "never", ModelVersion: "1"})
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Len(t, got, 0)
}

func testWfOverwrite(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ws, _ := h.Factory.WorkflowStore(ctx)
	mref := spi.ModelRef{EntityName: "m1", ModelVersion: "1"}
	require.NoError(t, ws.Save(ctx, mref, []spi.WorkflowDefinition{newWorkflowDef("wf1")}))
	require.NoError(t, ws.Save(ctx, mref, []spi.WorkflowDefinition{newWorkflowDef("wf2"), newWorkflowDef("wf3")}))
	got, err := ws.Get(ctx, mref)
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func testWfDelete(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ws, _ := h.Factory.WorkflowStore(ctx)
	mref := spi.ModelRef{EntityName: "m1", ModelVersion: "1"}
	require.NoError(t, ws.Save(ctx, mref, []spi.WorkflowDefinition{newWorkflowDef("wf1")}))
	require.NoError(t, ws.Delete(ctx, mref))
	got, err := ws.Get(ctx, mref)
	require.NoError(t, err)
	require.Len(t, got, 0)
}

// Deleting a model ref that is absent — never written, or already deleted —
// is not an error.
func testWfDeleteAbsent(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ws, _ := h.Factory.WorkflowStore(ctx)
	require.NoError(t, ws.Delete(ctx, spi.ModelRef{EntityName: "never", ModelVersion: "1"}))

	mref := spi.ModelRef{EntityName: "m1", ModelVersion: "1"}
	require.NoError(t, ws.Save(ctx, mref, []spi.WorkflowDefinition{newWorkflowDef("wf1")}))
	require.NoError(t, ws.Delete(ctx, mref))
	require.NoError(t, ws.Delete(ctx, mref))
}

func testWfTenantIsolation(t *testing.T, h Harness) {
	tA, tB := h.NewTenant(), h.NewTenant()
	wsA, _ := h.Factory.WorkflowStore(tenantContext(tA))
	wsB, _ := h.Factory.WorkflowStore(tenantContext(tB))
	mref := spi.ModelRef{EntityName: "m1", ModelVersion: "1"}
	require.NoError(t, wsA.Save(tenantContext(tA), mref, []spi.WorkflowDefinition{newWorkflowDef("wf1")}))
	got, err := wsB.Get(tenantContext(tB), mref)
	require.NoError(t, err)
	require.Len(t, got, 0)
}
