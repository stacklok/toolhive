// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_SSEGetConnectionSurvivesWriteTimeout verifies that the full
// vMCP server — with writeTimeoutMiddleware wired in — keeps a qualifying SSE
// GET connection alive past the server-level WriteTimeout.
//
// The test uses httptest.NewUnstartedServer so it can set a very short
// WriteTimeout before starting the server. It then opens a GET /mcp request
// with Accept: text/event-stream and reads from the body inside a context whose
// deadline is 3× the WriteTimeout. Two outcomes are possible:
//
//   - context.DeadlineExceeded: the read was still pending when our observation
//     window ended — the connection was NOT killed. This is the expected result.
//   - io.EOF or connection error: the server closed the connection early — the
//     WriteTimeout fired on the SSE stream. This is the failure case.
func TestIntegration_SSEGetConnectionSurvivesWriteTimeout(t *testing.T) {
	t.Parallel()

	const shortTimeout = 200 * time.Millisecond

	backendURL := startRealMCPBackend(t)

	// Build the handler separately so we can wrap it in a server with a custom WriteTimeout.
	handler := newRealTestHandler(t, backendURL)

	ts := httptest.NewUnstartedServer(handler)
	ts.Config.WriteTimeout = shortTimeout
	ts.Start()
	t.Cleanup(ts.Close)

	// Initialize an MCP session so the server assigns us a valid Mcp-Session-Id.
	// The initialize POST completes well within the server WriteTimeout.
	client := NewMCPTestClient(t, ts.URL)
	sessionID := client.InitializeSession()

	// Open a qualifying SSE GET stream. The observation context lives 3× longer
	// than the WriteTimeout; if the middleware is absent (or broken) the server
	// will kill the TCP connection after ~shortTimeout and the read below will
	// return io.EOF instead of context.DeadlineExceeded.
	sseCtx, sseCancel := context.WithTimeout(context.Background(), 3*shortTimeout)
	defer sseCancel()

	req, err := http.NewRequestWithContext(sseCtx, http.MethodGet, ts.URL+"/mcp", nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", sessionID)

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Loop until the observation window closes. io.EOF or a connection error
	// before the deadline means WriteTimeout killed the stream (test failure);
	// context expiry with the connection intact is the expected outcome.
	buf := make([]byte, 64)
	for {
		_, readErr := resp.Body.Read(buf)
		if readErr == nil {
			continue // data received; connection still alive, keep reading
		}
		if errors.Is(readErr, context.DeadlineExceeded) || errors.Is(readErr, context.Canceled) {
			break // observation window expired with connection intact — test passes
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			assert.Fail(t, "SSE GET connection was closed by the server before the observation window expired; WriteTimeout may have fired", "error: %v", readErr)
			break
		}
		// Any other error (e.g. connection reset) is also a failure.
		assert.Fail(t, "unexpected error reading SSE stream", "error: %v", readErr)
		break
	}
}
