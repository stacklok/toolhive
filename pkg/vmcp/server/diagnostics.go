// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/stacklok/toolhive/pkg/diagnostics"
)

// defaultDiagnosticsHost is used when Config.Host is unset. Defined locally
// rather than imported from pkg/transport, which this package does not depend
// on and which would pull in the whole transport tree for one constant.
const defaultDiagnosticsHost = "127.0.0.1"

// DiagnosticsAddress returns the address the diagnostics listener is bound to,
// or an empty string when metrics are disabled or the server is not started.
//
// The resolved port is not always the configured one — the listener falls back
// to an available port when the requested one is taken — so this is the only
// reliable way to find the /metrics endpoint programmatically.
func (s *Server) DiagnosticsAddress() string {
	if s.diagnosticsServer == nil {
		return ""
	}
	return s.diagnosticsServer.Addr()
}

// startDiagnostics starts the diagnostics listener when the telemetry provider
// exposes a Prometheus handler, and is a no-op otherwise.
//
// The metrics endpoint is kept off the port that serves MCP traffic so access
// can be governed by port: NetworkPolicy matches on pods, ports, and protocols
// and cannot filter on HTTP path, so a shared port makes "allow MCP, deny
// scraping" unexpressible. This does not make the endpoint authenticated — the
// diagnostics listener carries no middleware. See pkg/diagnostics for the full
// rationale and its limits.
func (s *Server) startDiagnostics() error {
	if s.config.TelemetryProvider == nil {
		return nil
	}
	metricsHandler := s.config.TelemetryProvider.PrometheusHandler()
	if metricsHandler == nil {
		return nil
	}

	// Bind the same host as the MCP listener so anything that can reach the
	// workload can reach its metrics, but on a port deployments are not expected
	// to route publicly. In Kubernetes that host is 0.0.0.0, so the endpoint stays
	// reachable from other pods and restricting it is a NetworkPolicy job — which
	// is what the separate port makes possible.
	host := s.config.Host
	if host == "" {
		host = defaultDiagnosticsHost
	}

	server, err := diagnostics.New(
		host,
		diagnostics.ResolvePort(s.config.TelemetryProvider.PrometheusPort()),
		metricsHandler,
	)
	if err != nil {
		return fmt.Errorf("failed to create diagnostics server: %w", err)
	}
	if err := server.Start(); err != nil {
		return fmt.Errorf("failed to start diagnostics server: %w", err)
	}

	s.diagnosticsServer = server

	if s.transportPortMetricsHandler() != nil {
		slog.Warn("serving prometheus metrics on the MCP port as well as the diagnostics port; "+
			"this is deprecated and the MCP-port copy will be removed. Move scrapers to the "+
			"diagnostics address logged above, then set metricsOnTransportPort: false to verify",
			"mcp_port", s.config.Port)
	}

	return nil
}

// transportPortMetricsHandler returns the Prometheus handler when /metrics should
// also be served on the port carrying MCP traffic, and nil otherwise.
//
// Nil is the end state: metrics live only on the diagnostics listener. Non-nil is
// the migration window, kept so an existing scrape configuration does not break
// the moment the diagnostics listener appears.
func (s *Server) transportPortMetricsHandler() http.Handler {
	if s.config.TelemetryProvider == nil {
		return nil
	}
	if !s.config.TelemetryProvider.ServeMetricsOnTransportPort() {
		return nil
	}
	return s.config.TelemetryProvider.PrometheusHandler()
}

// stopDiagnostics shuts the diagnostics listener down. It is safe to call when
// no diagnostics server was started, and safe to call more than once.
func (s *Server) stopDiagnostics(ctx context.Context) error {
	if s.diagnosticsServer == nil {
		return nil
	}

	server := s.diagnosticsServer
	s.diagnosticsServer = nil
	if err := server.Stop(ctx); err != nil {
		return fmt.Errorf("failed to stop diagnostics server: %w", err)
	}
	return nil
}
