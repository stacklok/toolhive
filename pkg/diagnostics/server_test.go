// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package diagnostics

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// metricsHandler returns a handler that writes a recognizable body, standing in
// for the Prometheus exposition handler.
func metricsHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})
}

// startTestServer starts a diagnostics server on an arbitrary loopback port and
// registers its shutdown with the test.
func startTestServer(t *testing.T, handler http.Handler) *Server {
	t.Helper()

	server, err := New("127.0.0.1", 0, handler)
	require.NoError(t, err)
	require.NoError(t, server.Start())

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, server.Stop(ctx))
	})

	return server
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, string(body)
}

func TestNewValidation(t *testing.T) {
	t.Parallel()

	handler := metricsHandler("ok")

	tests := []struct {
		name          string
		host          string
		port          int
		handler       http.Handler
		wantErrSubstr string
	}{
		{name: "valid", host: "127.0.0.1", port: 0, handler: handler},
		{name: "valid with explicit port", host: "127.0.0.1", port: 9464, handler: handler},
		{name: "empty host", host: "", port: 0, handler: handler, wantErrSubstr: "host is required"},
		{name: "nil handler", host: "127.0.0.1", port: 0, handler: nil, wantErrSubstr: "metrics handler is required"},
		{name: "negative port", host: "127.0.0.1", port: -1, handler: handler, wantErrSubstr: "out of range"},
		{name: "port above range", host: "127.0.0.1", port: 65536, handler: handler, wantErrSubstr: "out of range"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, err := New(tt.host, tt.port, tt.handler)
			if tt.wantErrSubstr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrSubstr)
				assert.Nil(t, server)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, server)
			// New must not bind; the port is only claimed by Start.
			assert.Empty(t, server.Addr())
			assert.Zero(t, server.Port())
		})
	}
}

// TestServesMetricsOnOwnListener is the core guarantee: the metrics endpoint is
// reachable on the diagnostics listener, which is a different port from the
// application listener the caller passes elsewhere.
func TestServesMetricsOnOwnListener(t *testing.T) {
	t.Parallel()

	server := startTestServer(t, metricsHandler("thv_requests_total 1"))

	require.NotEmpty(t, server.Addr())
	require.NotZero(t, server.Port())

	status, body := get(t, fmt.Sprintf("http://%s%s", server.Addr(), MetricsPath))
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "thv_requests_total 1", body)
}

// TestOnlyMetricsPathIsServed confirms the diagnostics listener is not a general
// catch-all: it must not proxy or otherwise answer application paths.
func TestOnlyMetricsPathIsServed(t *testing.T) {
	t.Parallel()

	server := startTestServer(t, metricsHandler("metrics"))

	for _, path := range []string{"/", "/mcp", "/health", "/metrics/../mcp"} {
		status, _ := get(t, fmt.Sprintf("http://%s%s", server.Addr(), path))
		assert.Equal(t, http.StatusNotFound, status, "path %q must not be served", path)
	}
}

func TestStartTwiceFails(t *testing.T) {
	t.Parallel()

	server := startTestServer(t, metricsHandler("ok"))

	err := server.Start()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

func TestStopWithoutStart(t *testing.T) {
	t.Parallel()

	server, err := New("127.0.0.1", 0, metricsHandler("ok"))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	assert.NoError(t, server.Stop(ctx))
}

func TestStopIsIdempotentAndClosesListener(t *testing.T) {
	t.Parallel()

	server, err := New("127.0.0.1", 0, metricsHandler("ok"))
	require.NoError(t, err)
	require.NoError(t, server.Start())

	addr := server.Addr()
	require.NotEmpty(t, addr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, server.Stop(ctx))
	// A second Stop must not panic or error.
	require.NoError(t, server.Stop(ctx))

	assert.Empty(t, server.Addr())
	assert.Zero(t, server.Port())

	// The listener is closed, so the endpoint is no longer reachable.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+MetricsPath, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		t.Fatal("expected the diagnostics listener to be closed after Stop")
	}
}
