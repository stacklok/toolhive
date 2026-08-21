// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

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
	"github.com/stacklok/toolhive/pkg/transport"
)

func testMetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("thv_requests_total 1"))
	})
}

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

// newMetricsRunner builds a Runner with the Prometheus metrics path enabled.
// prometheusPort is passed through to the telemetry config. Tests that bind a
// listener pass an explicit free port rather than relying on
// diagnostics.DefaultPort, so parallel tests never contend for 9464.
func newMetricsRunner(t *testing.T, host string, prometheusPort int) *Runner {
	t.Helper()

	return &Runner{
		Config: &RunConfig{
			Host: host,
			// Nothing binds this port; it exists so a test can assert the
			// diagnostics listener picked a different one.
			Port: freePort(t),
			TelemetryConfig: &telemetry.Config{
				EnablePrometheusMetricsPath: true,
				PrometheusPort:              prometheusPort,
			},
		},
		prometheusHandler: testMetricsHandler(),
	}
}

func stopRunnerDiagnostics(t *testing.T, r *Runner) {
	t.Helper()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, r.stopDiagnosticsServer(ctx))
	})
}

// TestDiagnosticsPort covers port resolution without binding anything, so the
// default-port case can be asserted without contending for 9464.
func TestDiagnosticsPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *telemetry.Config
		want int
	}{
		{name: "nil config falls back to default", cfg: nil, want: diagnostics.DefaultPort},
		{
			name: "unset port falls back to default",
			cfg:  &telemetry.Config{EnablePrometheusMetricsPath: true},
			want: diagnostics.DefaultPort,
		},
		{
			name: "explicit port is honoured",
			cfg:  &telemetry.Config{EnablePrometheusMetricsPath: true, PrometheusPort: 9999},
			want: 9999,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, diagnosticsPort(tt.cfg))
		})
	}
}

// TestStartDiagnosticsServerNoHandler covers the default case: with the
// Prometheus metrics path disabled the telemetry middleware never sets a
// handler, so no diagnostics listener is bound.
func TestStartDiagnosticsServerNoHandler(t *testing.T) {
	t.Parallel()

	r := &Runner{Config: &RunConfig{Host: transport.LocalhostIPv4, Port: freePort(t)}}

	require.NoError(t, r.startDiagnosticsServer())
	assert.Nil(t, r.diagnosticsServer)
}

// TestStartDiagnosticsServerUsesSeparatePort is the security property this
// change exists for: when metrics are enabled they are served on a listener
// distinct from the application port, so they never sit behind the ServeMux
// pattern that outranks the middleware chain.
func TestStartDiagnosticsServerUsesSeparatePort(t *testing.T) {
	t.Parallel()

	r := newMetricsRunner(t, transport.LocalhostIPv4, freePort(t))

	require.NoError(t, r.startDiagnosticsServer())
	stopRunnerDiagnostics(t, r)

	require.NotNil(t, r.diagnosticsServer)
	metricsPort := r.diagnosticsServer.Port()
	require.NotZero(t, metricsPort)
	assert.NotEqual(t, r.Config.Port, metricsPort, "metrics must not share the application port")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://"+r.diagnosticsServer.Addr()+diagnostics.MetricsPath, nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "thv_requests_total 1", string(body))
}

// TestStartDiagnosticsServerHonoursConfiguredPort covers the deployment case
// where a scraper needs a known port to target.
func TestStartDiagnosticsServerHonoursConfiguredPort(t *testing.T) {
	t.Parallel()

	wantPort := freePort(t)
	r := newMetricsRunner(t, transport.LocalhostIPv4, wantPort)

	require.NoError(t, r.startDiagnosticsServer())
	stopRunnerDiagnostics(t, r)

	require.NotNil(t, r.diagnosticsServer)
	assert.Equal(t, wantPort, r.diagnosticsServer.Port())
}

// TestStartDiagnosticsServerDefaultsHost guards the case where a RunConfig was
// assembled without WithHost: the diagnostics listener must fall back to
// loopback rather than binding every interface.
func TestStartDiagnosticsServerDefaultsHost(t *testing.T) {
	t.Parallel()

	r := newMetricsRunner(t, "", freePort(t))

	require.NoError(t, r.startDiagnosticsServer())
	stopRunnerDiagnostics(t, r)

	require.NotNil(t, r.diagnosticsServer)
	assert.Contains(t, r.diagnosticsServer.Addr(), transport.LocalhostIPv4+":")
}

// TestStartDiagnosticsServerWithoutTelemetryConfig covers a handler set while
// TelemetryConfig is nil; the port must fall back to auto-assignment rather
// than panicking.
func TestStartDiagnosticsServerWithoutTelemetryConfig(t *testing.T) {
	t.Parallel()

	r := &Runner{
		Config:            &RunConfig{Host: transport.LocalhostIPv4, Port: freePort(t)},
		prometheusHandler: testMetricsHandler(),
	}

	require.NoError(t, r.startDiagnosticsServer())
	stopRunnerDiagnostics(t, r)

	require.NotNil(t, r.diagnosticsServer)
	assert.NotZero(t, r.diagnosticsServer.Port())
}

func TestStopDiagnosticsServerIsIdempotent(t *testing.T) {
	t.Parallel()

	r := newMetricsRunner(t, transport.LocalhostIPv4, freePort(t))
	require.NoError(t, r.startDiagnosticsServer())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, r.stopDiagnosticsServer(ctx))
	assert.Nil(t, r.diagnosticsServer)
	// Stopping again, as Cleanup may, must be a no-op.
	require.NoError(t, r.stopDiagnosticsServer(ctx))
}

// TestMountPrometheusHandlerOnTransportPort covers the decision jhrozek's review on
// #6370 flagged as untested: the tri-state resolves correctly in isolation
// (TestDiagnosticsPort / TestServeMetricsOnTransportPort in pkg/telemetry), but
// nothing proved the resolved value actually reached transportConfig.PrometheusHandler
// for a standard (non-vMCP) workload. A regression here would leave the migration
// switch resolving correctly while silently never mounting the transport-port copy.
func TestMountPrometheusHandlerOnTransportPort(t *testing.T) {
	t.Parallel()

	handler := testMetricsHandler()

	tests := []struct {
		name    string
		handler http.Handler
		cfg     *telemetry.Config
		want    bool
	}{
		{
			name:    "no handler never mounts, regardless of the switch",
			handler: nil,
			cfg:     &telemetry.Config{MetricsOnTransportPort: boolPtr(true)},
			want:    false,
		},
		{
			name:    "unset switch follows the default (currently true)",
			handler: handler,
			cfg:     nil,
			want:    telemetry.DefaultMetricsOnTransportPort,
		},
		{
			name:    "explicitly enabled mounts",
			handler: handler,
			cfg:     &telemetry.Config{MetricsOnTransportPort: boolPtr(true)},
			want:    true,
		},
		{
			name:    "explicitly disabled does not mount",
			handler: handler,
			cfg:     &telemetry.Config{MetricsOnTransportPort: boolPtr(false)},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, mountPrometheusHandlerOnTransportPort(tt.handler, tt.cfg))
		})
	}
}

func boolPtr(b bool) *bool { return &b }
