// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package diagnostics

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// TestStartFallsBackWhenPortTaken covers the case a diagnostics listener must
// survive: the requested port is already in use. Losing the port must not fail
// the caller — the workload it accompanies is more important than the metrics
// endpoint keeping a specific port.
func TestStartFallsBackWhenPortTaken(t *testing.T) {
	t.Parallel()

	// Hold a port for the whole test so the requested one is genuinely occupied.
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = occupied.Close() })

	takenPort := occupied.Addr().(*net.TCPAddr).Port

	server, err := New("127.0.0.1", takenPort, metricsHandler("thv_requests_total 1"))
	require.NoError(t, err)
	require.NoError(t, server.Start())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, server.Stop(ctx))
	})

	require.NotZero(t, server.Port())
	assert.NotEqual(t, takenPort, server.Port(), "must not claim the occupied port")

	// The fallback listener still serves metrics.
	status, body := get(t, fmt.Sprintf("http://%s%s", server.Addr(), MetricsPath))
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "thv_requests_total 1", body)
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

// TestNotServedHereHandler covers the discoverability half of the split. The
// status alone is indistinguishable from a typo, so the body has to name where
// metrics actually live — this is the only client-side signal an operator gets
// when a scrape configuration still points at the application port.
func TestNotServedHereHandler(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	NotServedHereHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, MetricsPath, nil))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))

	body := rec.Body.String()
	assert.Contains(t, body, MetricsPath, "body must name the path being asked for")
	assert.Contains(t, body, startupLogMessage, "body must tell the reader what to grep for")
	assert.Contains(t, body, "docs/observability.md", "body must point at the docs")
}

// TestNotServedHereHandlerNamesNoPort is a regression guard, not a style check.
// The listener honours a configured port and falls back to another when it is
// taken, so a hardcoded number here is wrong for both of those configurations and
// sends someone who is already lost to a second dead endpoint. The startup log is
// the only address that is always right.
func TestNotServedHereHandlerNamesNoPort(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	NotServedHereHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, MetricsPath, nil))

	assert.NotContains(t, rec.Body.String(), strconv.Itoa(DefaultPort),
		"body must not name a port; the resolved one can differ from the default")
}

// TestResolvedPortIsDiscoverable covers the two configurations the 404 body cannot
// name — an explicitly configured port, and the fallback when it is occupied — and
// asserts the address the startup log reports is the one actually serving metrics.
// That address is what the 404 body directs the reader to, so it has to be right.
func TestResolvedPortIsDiscoverable(t *testing.T) {
	t.Parallel()

	t.Run("explicitly configured port is used verbatim", func(t *testing.T) {
		t.Parallel()

		want := freeTestPort(t)
		server, err := New("127.0.0.1", want, metricsHandler("thv_requests_total 1"))
		require.NoError(t, err)
		require.NoError(t, server.Start())
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			assert.NoError(t, server.Stop(ctx))
		})

		assert.Equal(t, want, server.Port())
		assertServesMetrics(t, server)
	})

	t.Run("fallback port is reported and serving", func(t *testing.T) {
		t.Parallel()

		occupied, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = occupied.Close() })
		taken := occupied.Addr().(*net.TCPAddr).Port

		server, err := New("127.0.0.1", taken, metricsHandler("thv_requests_total 1"))
		require.NoError(t, err)
		require.NoError(t, server.Start())
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			assert.NoError(t, server.Stop(ctx))
		})

		require.NotEqual(t, taken, server.Port(), "must have fallen back")
		// Addr is what the startup log reports, so it must be the live one.
		assertServesMetrics(t, server)
	})
}

// assertServesMetrics checks the address the server reports is genuinely serving.
func assertServesMetrics(t *testing.T, server *Server) {
	t.Helper()

	status, body := get(t, fmt.Sprintf("http://%s%s", server.Addr(), MetricsPath))
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "thv_requests_total 1", body)
}

// freeTestPort reserves and releases a port so an explicit-port test can name one
// without hardcoding it.
func freeTestPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)
	require.NoError(t, listener.Close())

	return addr.Port
}
