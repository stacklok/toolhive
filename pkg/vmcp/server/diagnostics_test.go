// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/diagnostics"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// freePort asks the OS for an unused port and releases it, so a test can name a
// port without hardcoding one that may already be in use on the machine.
func freePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)
	require.NoError(t, listener.Close())

	return addr.Port
}

// newMetricsProvider builds a telemetry provider that exposes a Prometheus
// handler on the given port.
func newMetricsProvider(t *testing.T, port int) *telemetry.Provider {
	t.Helper()

	provider, err := telemetry.NewProvider(t.Context(), telemetry.Config{
		ServiceName:                 "vmcp-diagnostics-test",
		EnablePrometheusMetricsPath: true,
		PrometheusPort:              port,
	})
	require.NoError(t, err)
	require.NotNil(t, provider.PrometheusHandler(), "provider must expose a Prometheus handler")

	return provider
}

func stopDiagnosticsOnCleanup(t *testing.T, s *Server) {
	t.Helper()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, s.stopDiagnostics(ctx))
	})
}

// TestStartDiagnosticsNoTelemetryProvider covers the default case: with no
// telemetry configured there is nothing to serve and no listener is bound.
func TestStartDiagnosticsNoTelemetryProvider(t *testing.T) {
	t.Parallel()

	s := &Server{config: &Config{Host: "127.0.0.1", Port: freePort(t)}}

	require.NoError(t, s.startDiagnostics())
	assert.Nil(t, s.diagnosticsServer)
}

// TestStartDiagnosticsUsesSeparatePort is the property this change exists for:
// when metrics are enabled they bind a listener distinct from the port serving
// MCP traffic, so access to them can be governed by port.
func TestStartDiagnosticsUsesSeparatePort(t *testing.T) {
	t.Parallel()

	mcpPort := freePort(t)
	metricsPort := freePort(t)

	s := &Server{config: &Config{
		Host:              "127.0.0.1",
		Port:              mcpPort,
		TelemetryProvider: newMetricsProvider(t, metricsPort),
	}}

	require.NoError(t, s.startDiagnostics())
	stopDiagnosticsOnCleanup(t, s)

	require.NotNil(t, s.diagnosticsServer)
	require.NotZero(t, s.diagnosticsServer.Port())
	assert.NotEqual(t, mcpPort, s.diagnosticsServer.Port(),
		"metrics must not share the MCP port")

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://"+s.diagnosticsServer.Addr()+diagnostics.MetricsPath, nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestStartDiagnosticsHonoursConfiguredPort covers the deployment case where a
// scraper needs a known port to target.
func TestStartDiagnosticsHonoursConfiguredPort(t *testing.T) {
	t.Parallel()

	metricsPort := freePort(t)
	s := &Server{config: &Config{
		Host:              "127.0.0.1",
		Port:              freePort(t),
		TelemetryProvider: newMetricsProvider(t, metricsPort),
	}}

	require.NoError(t, s.startDiagnostics())
	stopDiagnosticsOnCleanup(t, s)

	require.NotNil(t, s.diagnosticsServer)
	assert.Equal(t, metricsPort, s.diagnosticsServer.Port())
}

// TestStartDiagnosticsDefaultsHost guards a Config assembled without Host: the
// listener must fall back to loopback rather than binding every interface.
func TestStartDiagnosticsDefaultsHost(t *testing.T) {
	t.Parallel()

	s := &Server{config: &Config{
		Port:              freePort(t),
		TelemetryProvider: newMetricsProvider(t, freePort(t)),
	}}

	require.NoError(t, s.startDiagnostics())
	stopDiagnosticsOnCleanup(t, s)

	require.NotNil(t, s.diagnosticsServer)
	assert.Contains(t, s.diagnosticsServer.Addr(), defaultDiagnosticsHost+":")
}

func TestStopDiagnosticsIsIdempotent(t *testing.T) {
	t.Parallel()

	s := &Server{config: &Config{
		Host:              "127.0.0.1",
		Port:              freePort(t),
		TelemetryProvider: newMetricsProvider(t, freePort(t)),
	}}
	require.NoError(t, s.startDiagnostics())

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	require.NoError(t, s.stopDiagnostics(ctx))
	assert.Nil(t, s.diagnosticsServer)
	// Stop runs this again on a server that already shut down.
	require.NoError(t, s.stopDiagnostics(ctx))
}

// TestStartFailsBeforeMCPListenerWhenDiagnosticsFails is a regression test for a
// review finding on #6368: startDiagnostics used to run after the MCP listener was
// already serving in a background goroutine, so a diagnostics failure returned an
// error from Start while MCP traffic kept flowing and Ready() never closed --
// anything blocked on it would hang forever.
//
// Forces startDiagnostics to fail deterministically with a host that cannot be
// listened on, and asserts Start fails before anything MCP-related is touched: no
// listener is bound, and Ready() stays open (matching "the server never became
// ready", not "ready and now broken").
func TestStartFailsBeforeMCPListenerWhenDiagnosticsFails(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	provider := newMetricsProvider(t, freePort(t))

	srv, err := Serve(ctx, &stubVMCP{}, &ServerConfig{
		// Cannot be resolved as a listen address, so diagnostics.Server.bind fails
		// all its retries quickly rather than needing a real occupied port, which
		// would be racy to arrange deterministically.
		Host:                 "invalid-host-that-cannot-be-resolved.invalid",
		Port:                 freePort(t),
		SessionTTL:           defaultSessionTTL,
		SessionManagerConfig: testMinimalSessionManagerConfig(),
		BackendRegistry:      vmcp.NewImmutableRegistry([]vmcp.Backend{}),
		TelemetryProvider:    provider,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	err = srv.Start(ctx)
	require.Error(t, err)

	assert.Nil(t, srv.diagnosticsServer, "a failed diagnostics start must not leave a server reference behind")
	srv.listenerMu.RLock()
	listener := srv.listener
	srv.listenerMu.RUnlock()
	assert.Nil(t, listener, "the MCP listener must not be created when diagnostics fails first")

	select {
	case <-srv.Ready():
		t.Fatal("Ready() must not close when Start returns an error")
	default:
	}
}
