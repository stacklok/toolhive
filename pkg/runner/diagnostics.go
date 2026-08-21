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

	// Bind to the same host as the proxy so a deployment that reaches the
	// workload can reach its metrics, but on a separate port that deployments
	// are not expected to route publicly. Note this means the operator's
	// 0.0.0.0 default applies here too: the endpoint stays reachable from other
	// pods, so restricting it is a NetworkPolicy job, which is what the separate
	// port makes possible. Mirror the builder's host default so a config
	// assembled without WithHost does not bind to every interface.
	host := r.Config.Host
	if host == "" {
		host = transport.LocalhostIPv4
	}

	server, err := diagnostics.New(host, diagnosticsPort(r.Config.TelemetryConfig), r.prometheusHandler)
	if err != nil {
		return fmt.Errorf("failed to create diagnostics server: %w", err)
	}
	if err := server.Start(); err != nil {
		return fmt.Errorf("failed to start diagnostics server: %w", err)
	}

	r.diagnosticsServer = server
	return nil
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
// telemetry.DefaultMetricsOnTransportPort). Extracted from Run so the decision is
// unit-testable without exercising the whole method: a bug here would resolve the
// migration switch correctly while silently failing to act on it for standard
// (non-vMCP) workloads.
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
