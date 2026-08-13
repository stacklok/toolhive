// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/diagnostics"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/transport"
)

// startDiagnosticsServer starts the diagnostics listener when the telemetry
// middleware enabled the Prometheus metrics path, and is a no-op otherwise.
//
// The metrics endpoint is deliberately kept off the application listener. Go's
// ServeMux resolves an explicitly registered "/metrics" ahead of the "/"
// catch-all that carries the middleware chain, so sharing the listener leaves
// the endpoint unauthenticated even on a fully OIDC-configured deployment.
// See pkg/diagnostics for the full rationale.
func (r *Runner) startDiagnosticsServer() error {
	if r.prometheusHandler == nil {
		return nil
	}

	// Bind to the same host as the proxy so a deployment that reaches the
	// workload can reach its metrics, but on a separate port that deployments
	// are not expected to route publicly. Mirror the builder's host default so
	// a config assembled without WithHost does not bind to every interface.
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
	if cfg == nil || cfg.PrometheusPort == 0 {
		return diagnostics.DefaultPort
	}
	return cfg.PrometheusPort
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
