// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/bodylimit"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// TestHTTPProxy_RejectsOversizedBody verifies that a body-limit middleware in
// the proxy chain rejects an oversized request body with 413 (the chain wires
// it exactly as PopulateMiddlewareConfigs does for every proxy transport).
//
//nolint:paralleltest // starts an HTTP server
func TestHTTPProxy_RejectsOversizedBody(t *testing.T) {
	const limit = 1024

	chain := []types.NamedMiddleware{
		{Name: bodylimit.MiddlewareType, Function: bodylimit.Middleware(limit)},
	}

	port := getFreePort(t)
	proxy := NewHTTPProxy("127.0.0.1", port, nil, chain)
	ctx := t.Context()
	require.NoError(t, proxy.Start(ctx))
	defer proxy.Stop(ctx)
	time.Sleep(100 * time.Millisecond)

	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, StreamableHTTPEndpoint)
	resp, err := http.Post(url, "application/json", bytes.NewReader(make([]byte, limit+1)))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
}

// TestHTTPProxy_BodyLimitBoundsDownstreamReader verifies that the body-limit
// middleware, placed first in the chain (as PopulateMiddlewareConfigs does),
// caps the bytes a downstream middleware can read — proving it executes before
// (outside of) handlers that buffer the body via io.ReadAll, like the MCP
// parser. The request uses chunked transfer (no Content-Length) so the early
// Content-Length rejection is bypassed and the MaxBytesReader cap is exercised.
//
// This is the ground-truth regression guard for chain ordering: if body-limit
// is ever moved after the parser, the downstream reader sees the full body and
// this test fails.
//
//nolint:paralleltest // starts an HTTP server
func TestHTTPProxy_BodyLimitBoundsDownstreamReader(t *testing.T) {
	const limit = 1024
	const bodySize = limit * 8

	var observed int64
	recorder := func(_ http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Mimics pkg/mcp/parser.go:85 reading the whole body.
			b, _ := io.ReadAll(r.Body)
			atomic.StoreInt64(&observed, int64(len(b)))
			w.WriteHeader(http.StatusOK)
		})
	}

	// Order mirrors PopulateMiddlewareConfigs: body-limit first, parser-like next.
	chain := []types.NamedMiddleware{
		{Name: bodylimit.MiddlewareType, Function: bodylimit.Middleware(limit)},
		{Name: "parser-like-reader", Function: recorder},
	}

	port := getFreePort(t)
	proxy := NewHTTPProxy("127.0.0.1", port, nil, chain)
	ctx := t.Context()
	require.NoError(t, proxy.Start(ctx))
	defer proxy.Stop(ctx)
	time.Sleep(100 * time.Millisecond)

	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, StreamableHTTPEndpoint)

	// io.NopCloser hides the concrete reader type so net/http sends the request
	// with chunked transfer encoding (ContentLength unknown).
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url,
		io.NopCloser(bytes.NewReader(make([]byte, bodySize))))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	got := atomic.LoadInt64(&observed)
	assert.LessOrEqual(t, got, int64(limit),
		"downstream reader must be bounded by the body limit (body-limit ran outermost)")
	assert.Less(t, got, int64(bodySize),
		"downstream reader must not see the full oversized body")
}
