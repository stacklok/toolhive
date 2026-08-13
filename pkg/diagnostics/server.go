// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package diagnostics serves operational endpoints on a listener that is
// separate from the application listener.
//
// Diagnostics endpoints must not share the application listener. Go's ServeMux
// resolves the most specific registered pattern first, so an explicitly
// registered "/metrics" always beats the "/" catch-all that carries the proxy
// middleware chain (auth, body limit, rate limiting, audit). Registering
// /metrics on the application mux therefore leaves it unauthenticated even on a
// fully OIDC-configured deployment. Binding it to its own port, which
// deployments are not expected to route publicly, keeps it reachable for
// in-cluster scrapers without exposing it to the internet.
//
// This mirrors the ToolHive operator, which already binds its own metrics
// endpoint to a separate address (--metrics-bind-address), and the wider
// convention for diagnostics ports: etcd's --listen-metrics-urls and
// controller-runtime's --metrics-bind-address.
//
// Note that /health deliberately stays on the application listener. Kubernetes
// liveness and readiness probes target the application port, and the proxy
// health response carries no sensitive fields.
package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/transport/proxy/socket"
)

// MetricsPath is the path the Prometheus metrics handler is served on.
const MetricsPath = "/metrics"

// DefaultPort is the port the diagnostics listener binds when no port is
// configured. 9464 is the OpenTelemetry specification's default for the
// Prometheus exporter (OTEL_EXPORTER_PROMETHEUS_PORT), so scrapers already
// expect metrics there.
//
// A fixed default rather than an arbitrary one matters for deployments: a
// scraper needs a predictable target. When the port is already taken — several
// CLI workloads on one machine, for instance — Start falls back to an available
// port and logs the resolved address.
const DefaultPort = 9464

const (
	// readHeaderTimeout bounds header reads to prevent Slowloris attacks.
	readHeaderTimeout = 10 * time.Second
	// idleTimeout stops idle keep-alive connections from blocking shutdown.
	idleTimeout = 60 * time.Second
)

// Server serves diagnostics endpoints on a dedicated listener.
//
// The zero value is not usable; construct one with New.
type Server struct {
	host           string
	port           int
	metricsHandler http.Handler

	// mu guards server and listener, which Start writes and Stop reads.
	mu       sync.Mutex
	server   *http.Server
	listener net.Listener
}

// New creates a diagnostics server that serves metricsHandler at MetricsPath.
//
// host is the bind address. port is the requested port; 0 requests an
// arbitrary available port, which is the default for CLI runs where several
// workloads share a machine and a fixed port would collide. Callers that need a
// deterministic port for a scraper to target (the Kubernetes operator, for
// example) must pass one explicitly.
//
// The port is not bound until Start is called.
func New(host string, port int, metricsHandler http.Handler) (*Server, error) {
	if host == "" {
		return nil, errors.New("diagnostics: host is required")
	}
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("diagnostics: port %d out of range", port)
	}
	if metricsHandler == nil {
		return nil, errors.New("diagnostics: metrics handler is required")
	}
	return &Server{
		host:           host,
		port:           port,
		metricsHandler: metricsHandler,
	}, nil
}

// Start binds the diagnostics listener and serves it in the background. It
// returns once the listener is bound, so a caller that observes no error can
// rely on Addr reporting the resolved address.
//
// Calling Start on an already-started Server returns an error.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.listener != nil {
		return errors.New("diagnostics: server already started")
	}

	// Resolve the port the same way the proxy listeners do, so a requested
	// port that is busy falls back to an available one instead of failing the
	// whole workload.
	port, err := networking.FindOrUsePort(s.port)
	if err != nil {
		return fmt.Errorf("diagnostics: failed to resolve port: %w", err)
	}

	addr := fmt.Sprintf("%s:%d", s.host, port)

	mux := http.NewServeMux()
	mux.Handle(MetricsPath, s.metricsHandler)

	// Use SO_REUSEADDR for parity with the proxy listeners, which allows port
	// reuse after an unclean shutdown left a zombie holding the port.
	lc := socket.ListenConfig()
	listener, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return fmt.Errorf("diagnostics: failed to listen on %s: %w", addr, err)
	}

	s.listener = listener
	s.server = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}

	// Capture the server locally so the goroutine does not race with Stop
	// clearing the field.
	server := s.server
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			var opErr *net.OpError
			if errors.As(err, &opErr) && opErr.Op == "accept" {
				// Expected once Stop closes the listener.
				return
			}
			slog.Warn("diagnostics server error", "error", err)
		}
	}()

	// Logged at INFO because the resolved port is not predictable when the
	// caller requested 0, and an operator needs it to point a scraper here.
	slog.Info("prometheus metrics endpoint enabled on diagnostics listener",
		"address", listener.Addr().String(), "path", MetricsPath)

	return nil
}

// Addr returns the resolved listen address, or an empty string before Start
// succeeds or after Stop.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Port returns the resolved port, or 0 before Start succeeds or after Stop.
func (s *Server) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.listener == nil {
		return 0
	}
	tcpAddr, ok := s.listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0
	}
	return tcpAddr.Port
}

// Stop gracefully shuts the diagnostics server down. It is safe to call on a
// Server that was never started, and safe to call more than once.
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	server := s.server
	s.server = nil
	s.listener = nil
	s.mu.Unlock()

	if server == nil {
		return nil
	}

	// Shutdown closes the listener, so it is not closed separately here.
	if err := server.Shutdown(ctx); err != nil {
		return fmt.Errorf("diagnostics: failed to shut down: %w", err)
	}
	return nil
}
