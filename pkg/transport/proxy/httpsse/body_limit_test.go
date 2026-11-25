// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package httpsse

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/bodylimit"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// TestHTTPSSEProxy_RejectsOversizedChunkedBody verifies that an oversized
// request body sent WITHOUT a Content-Length (chunked transfer) is rejected
// with 413 by handlePostRequest's IsRequestTooLarge branch.
//
// This is the per-transport code path that no other test covers: the early
// Content-Length reject lives entirely in bodylimit.Middleware (covered by
// pkg/bodylimit). handlePostRequest's IsRequestTooLarge branch is gated behind
// session-exists (else 404) and a held local SSE connection (else 503), so the
// request must establish a live SSE session and keep it open before the chunked
// body trips http.MaxBytesReader inside io.ReadAll. Asserting 413 and NOT
// 404/503/500 proves the request reached that branch rather than being rejected
// by an earlier gate.
//
//nolint:paralleltest // starts an HTTP server
func TestHTTPSSEProxy_RejectsOversizedChunkedBody(t *testing.T) {
	const limit = 1024

	chain := []types.NamedMiddleware{
		{Name: bodylimit.MiddlewareType, Function: bodylimit.Middleware(limit)},
	}

	proxy := NewHTTPSSEProxy("127.0.0.1", 0, false, nil, chain)
	ctx := t.Context()
	require.NoError(t, proxy.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = proxy.Stop(stopCtx)
	})

	// Drain the message channel so the SSE handler is not blocked while the
	// session is held open (mirrors the integration tests).
	go func() {
		for msg := range proxy.GetMessageChannel() {
			_ = proxy.ForwardResponseToClients(ctx, msg)
		}
	}()

	proxyURL := fmt.Sprintf("http://%s", proxy.server.Addr)

	// Open the SSE connection and keep its body open: handlePostRequest's
	// liveSSESessions check requires the local connection to still be held.
	var sseResp *http.Response
	require.Eventually(t, func() bool {
		var err error
		sseResp, err = http.Get(proxyURL + "/sse") //nolint:gosec // test-only URL construction
		return err == nil
	}, 2*time.Second, 10*time.Millisecond, "SSE endpoint did not become ready")
	t.Cleanup(func() { _ = sseResp.Body.Close() })

	sessionID, err := extractSessionID(sseResp.Body)
	require.NoError(t, err, "failed to extract session ID from SSE endpoint event")
	require.NotEmpty(t, sessionID)

	// POST an oversized body WITHOUT a Content-Length. io.NopCloser hides the
	// concrete reader type so net/http uses chunked transfer, bypassing the
	// middleware's early Content-Length reject and forcing the body through
	// io.ReadAll's MaxBytesReader inside handlePostRequest.
	msgURL := fmt.Sprintf("%s/messages?session_id=%s", proxyURL, sessionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, msgURL,
		io.NopCloser(bytes.NewReader(make([]byte, limit*8))))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Must be 413 from the IsRequestTooLarge branch. A 404 would mean the
	// session was not found, 503 that the live SSE connection was not held, and
	// 500 a generic read error — any of these would mean an earlier gate
	// rejected the request before the body-size check.
	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
	assert.NotEqual(t, http.StatusNotFound, resp.StatusCode)
	assert.NotEqual(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.NotEqual(t, http.StatusInternalServerError, resp.StatusCode)
}
