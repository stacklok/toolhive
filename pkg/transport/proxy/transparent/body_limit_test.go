// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/bodylimit"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

//nolint:paralleltest // captures the process-global slog logger
func TestRoundTripBodyReadDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		err          error
		status       int
	}{
		{"reader_error", "read_error", errors.New("reader-detail-marker"), http.StatusBadRequest},
		{"wrapped_read_limit", "body_too_large", fmt.Errorf("reader-detail-marker: %w", &http.MaxBytesError{Limit: 8}),
			http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewTransparentProxy("127.0.0.1", 0, "http://backend", nil, nil, nil, false, false,
				"", nil, nil, "", false)
			t.Cleanup(func() { require.NoError(t, p.sessionManager.Stop()) })
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
			t.Cleanup(func() { slog.SetDefault(previous) })

			backendCalls := 0
			spy := roundTripFunc(func(*http.Request) (*http.Response, error) {
				backendCalls++
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
			})
			const body = `{"jsonrpc":"2.0","id":"id-marker","method":"body-marker"}`
			req := httptest.NewRequest(http.MethodPost, "/custom", nil)
			req.Body = io.NopCloser(io.MultiReader(strings.NewReader(body), iotest.ErrReader(tc.err)))
			resp, err := newTracingTransport(spy, p).RoundTrip(req)
			require.NoError(t, err)
			require.NotNil(t, resp)
			t.Cleanup(func() { drainAndClose(t, resp) })
			assert.Equal(t, tc.status, resp.StatusCode)
			assert.Zero(t, backendCalls)
			responseBody, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			for _, marker := range []string{"reader-detail-marker", "id-marker", "body-marker"} {
				assert.NotContains(t, string(responseBody), marker)
				assert.NotContains(t, logs.String(), marker)
			}
			var entry map[string]any
			require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
			assert.NotEmpty(t, entry["time"])
			delete(entry, "time")
			assert.Equal(t, map[string]any{
				"level": "WARN", "msg": "rejected unreadable MCP request body", "reason": tc.reason,
			}, entry)
		})
	}
}

// TestTransparentProxy_RejectsOversizedBody verifies the body-limit middleware
// in the transparent proxy's chain rejects an oversized request body with 413
// before forwarding it to the backend. This guards the transparent transport's
// independent middleware mounting.
//
// The request is sent WITHOUT a Content-Length (chunked transfer), which
// bypasses the middleware's early Content-Length rejection and instead trips
// http.MaxBytesReader inside the transport's readRequestBody, which must still
// report 413 (not forward a truncated body to the backend). This is the
// per-transport code path: the early Content-Length reject lives entirely in
// bodylimit.Middleware and is covered by pkg/bodylimit. A body that flows far
// enough to be capped also proves the middleware is mounted on the chain.
//
//nolint:paralleltest // starts an HTTP server
func TestTransparentProxy_RejectsOversizedBody(t *testing.T) {
	const limit = 1024

	// Backend that would return 200 if the request were ever forwarded; the
	// body-limit middleware must reject the oversized request before this runs.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	chain := []types.NamedMiddleware{
		{Name: bodylimit.MiddlewareType, Function: bodylimit.Middleware(limit)},
	}

	proxy := NewTransparentProxyWithOptions(
		"127.0.0.1", 0, backend.URL,
		nil, nil, nil,
		false, false, "sse",
		nil, nil, "", false,
		chain,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = proxy.Stop(stopCtx)
	})
	require.NoError(t, proxy.Start(ctx))

	url := fmt.Sprintf("http://%s/", proxy.listener.Addr().String())

	// io.NopCloser hides the concrete reader type so net/http omits
	// Content-Length and uses chunked transfer, bypassing the middleware's
	// early-reject path.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url,
		io.NopCloser(bytes.NewReader(make([]byte, limit*8))))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
	assert.NotEqual(t, http.StatusOK, resp.StatusCode)
	assert.NotEqual(t, http.StatusInternalServerError, resp.StatusCode)
}
