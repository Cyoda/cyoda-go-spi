package spitest

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

func runMessageSuite(t *testing.T, h Harness, tracker *skipTracker) {
	runSubtest(t, h, tracker, "SaveAndGet", testMsgSaveAndGet)
	runSubtest(t, h, tracker, "Get/NotFound", testMsgGetNotFound)
	runSubtest(t, h, tracker, "Delete", testMsgDelete)
	runSubtest(t, h, tracker, "DeleteBatch", testMsgDeleteBatch)
	runSubtest(t, h, tracker, "Payload/Large", testMsgPayloadLarge)
	runSubtest(t, h, tracker, "Payload/StreamClosed", testMsgPayloadStreamClosed)
	runSubtest(t, h, tracker, "TenantIsolation", testMsgTenantIsolation)
}

func testMsgSaveAndGet(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ms, err := h.Factory.MessageStore(ctx)
	require.NoError(t, err)
	header := spi.MessageHeader{Subject: "type-a", ContentType: "text/plain"}
	meta := spi.MessageMetaData{}
	payload := []byte("hello")
	require.NoError(t, ms.Save(ctx, "msg-1", header, meta, bytes.NewReader(payload)))
	gotHeader, _, rc, err := ms.Get(ctx, "msg-1")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	require.Equal(t, "type-a", gotHeader.Subject)
	gotPayload, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, payload, gotPayload)
}

func testMsgGetNotFound(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ms, _ := h.Factory.MessageStore(ctx)
	_, _, _, err := ms.Get(ctx, "missing")
	require.ErrorIs(t, err, spi.ErrNotFound)
}

func testMsgDelete(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ms, _ := h.Factory.MessageStore(ctx)
	require.NoError(t, ms.Save(ctx, "m1", spi.MessageHeader{Subject: "t", ContentType: "text/plain"}, spi.MessageMetaData{}, strings.NewReader("x")))
	require.NoError(t, ms.Delete(ctx, "m1"))
	_, _, _, err := ms.Get(ctx, "m1")
	require.ErrorIs(t, err, spi.ErrNotFound)
}

func testMsgDeleteBatch(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ms, _ := h.Factory.MessageStore(ctx)
	require.NoError(t, ms.Save(ctx, "m1", spi.MessageHeader{Subject: "t", ContentType: "text/plain"}, spi.MessageMetaData{}, strings.NewReader("a")))
	require.NoError(t, ms.Save(ctx, "m2", spi.MessageHeader{Subject: "t", ContentType: "text/plain"}, spi.MessageMetaData{}, strings.NewReader("b")))
	require.NoError(t, ms.Save(ctx, "m3", spi.MessageHeader{Subject: "t", ContentType: "text/plain"}, spi.MessageMetaData{}, strings.NewReader("c")))
	require.NoError(t, ms.DeleteBatch(ctx, []string{"m1", "m3"}))
	_, _, _, err := ms.Get(ctx, "m1")
	require.ErrorIs(t, err, spi.ErrNotFound)
	_, _, _, err = ms.Get(ctx, "m3")
	require.ErrorIs(t, err, spi.ErrNotFound)
	_, _, rc, err := ms.Get(ctx, "m2")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
}

func testMsgPayloadLarge(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ms, _ := h.Factory.MessageStore(ctx)
	payload := bytes.Repeat([]byte{0xAA}, 4*1024*1024) // 4 MB
	require.NoError(t, ms.Save(ctx, "big", spi.MessageHeader{Subject: "big", ContentType: "application/octet-stream"}, spi.MessageMetaData{}, bytes.NewReader(payload)))
	_, _, rc, err := ms.Get(ctx, "big")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.Equal(t, len(payload), len(got))
	require.Equal(t, payload, got)
}

func testMsgPayloadStreamClosed(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	ms, _ := h.Factory.MessageStore(ctx)
	require.NoError(t, ms.Save(ctx, "m1", spi.MessageHeader{Subject: "t", ContentType: "text/plain"}, spi.MessageMetaData{}, strings.NewReader("x")))
	_, _, rc, err := ms.Get(ctx, "m1")
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	// io.Closer does not mandate idempotent Close; we only require no panic.
	require.NotPanics(t, func() { _ = rc.Close() }, "double-close must not panic")
}

func testMsgTenantIsolation(t *testing.T, h Harness) {
	tA, tB := h.NewTenant(), h.NewTenant()
	msA, _ := h.Factory.MessageStore(tenantContext(tA))
	msB, _ := h.Factory.MessageStore(tenantContext(tB))
	require.NoError(t, msA.Save(tenantContext(tA), "shared-id", spi.MessageHeader{Subject: "t", ContentType: "text/plain"}, spi.MessageMetaData{}, strings.NewReader("A")))
	_, _, _, err := msB.Get(tenantContext(tB), "shared-id")
	require.ErrorIs(t, err, spi.ErrNotFound)
}
