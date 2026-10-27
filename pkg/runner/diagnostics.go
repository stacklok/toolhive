// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/stacklok/toolhive/pkg/diagnostics"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/transport"
)

// diagnosticsStopTimeout bounds the graceful shutdown of the diagnostics
// listener on Run's error path, where no caller-supplied context is available.
const diagnosticsStopTimeout = 10 * time.Second

// startDiagnosticsServer starts the diagnostics listener when the telemetry
// middleware enabled the Prometheus metrics path, and is a no-op otherwise.
//
// The metrics endpoint is deliberately kept off the application listener so it
// can be governed by port: NetworkPolicy cannot filter on HTTP path, so a shared
// port makes "allow MCP, deny scraping" unexpressible. This does not make the
// endpoint authenticated — the diagnostics listener carries no middleware. See
// pkg/diagnostics for the full rationale and its limits.
func (r *Runner) startDiagnosticsServer() error {
	if r.prometheusHandler == nil {
		return nil
	}

	server, err := diagnostics.New(r.bindHost(), diagnosticsPort(r.Config.TelemetryConfig), r.prometheusHandler)
	if err != nil {
		return fmt.Errorf("failed to create diagnostics server: %w", err)
	}
	if err := server.Start(); err != nil {
		return fmt.Errorf("failed to start diagnostics server: %w", err)
	}

	r.diagnosticsServer = server
	return nil
}

// bindHost returns the host the runner's side listeners (diagnostics, auth
// server TLS) bind to.
//
// They bind to the same host as the proxy so a deployment that reaches the
// workload can reach them, but on separate ports that deployments are not
// expected to route publicly. Note this means the operator's 0.0.0.0 default
// applies here too: the endpoints stay reachable from other pods, so
// restricting them is a NetworkPolicy job, which is what the separate ports
// make possible. Mirror the builder's host default so a config assembled
// without WithHost does not bind to every interface.
func (r *Runner) bindHost() string {
	if r.Config.Host == "" {
		return transport.LocalhostIPv4
	}
	return r.Config.Host
}

// diagnosticsPort resolves the port the diagnostics listener should request.
// An unset port falls back to diagnostics.DefaultPort so a scraper has a
// predictable target rather than an arbitrary one.
func diagnosticsPort(cfg *telemetry.Config) int {
	if cfg == nil {
		return diagnostics.DefaultPort
	}
	return diagnostics.ResolvePort(cfg.PrometheusPort)
}

// mountPrometheusHandlerOnTransportPort reports whether Run should hand the
// Prometheus handler to the transport, which is what makes the proxies also serve
// /metrics on the transport port during the migration window (see
// telemetry.DefaultMetricsOnTransportPort).
func mountPrometheusHandlerOnTransportPort(handler http.Handler, cfg *telemetry.Config) bool {
	return handler != nil && cfg.ServeMetricsOnTransportPort()
}

// stopDiagnosticsServer shuts the diagnostics listener down. It is safe to call
// when no diagnostics server was started.
func (r *Runner) stopDiagnosticsServer(ctx context.Context) error {
	if r.diagnosticsServer == nil {
		return nil
	}

	server := r.diagnosticsServer
	r.diagnosticsServer = nil
	if err := server.Stop(ctx); err != nil {
		return fmt.Errorf("failed to stop diagnostics server: %w", err)
	}
	return nil
}
