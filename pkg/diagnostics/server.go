// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package diagnostics serves operational endpoints on a listener that is
// separate from the application listener.
//
// The reason to separate is enforceability, not middleware coverage. Be precise
// about what this does and does not buy, because the distinction is easy to
// overstate:
//
//   - It does NOT authenticate, rate limit, or audit the endpoints. This
//     listener carries no middleware at all (see the warning below). An endpoint
//     served here is exactly as unauthenticated as it was on the application
//     mux.
//   - It DOES make the endpoints governable by port. Kubernetes NetworkPolicy is
//     L3/L4: it selects pods, ports, and protocols, and cannot filter on HTTP
//     path. While /metrics shares the application port, no policy can permit MCP
//     traffic while denying metrics scraping. On its own port, it can. Route-level
//     controls (Gateway API, Ingress path rules) govern only what reaches the
//     gateway, so they address north-south exposure but leave pod-to-pod traffic
//     untouched.
//   - It DOES keep the endpoints off the port deployments route publicly, so the
//     safe outcome does not depend on every operator getting route rules right.
//
// Serving them on the application mux is what made the port-level control
// impossible: Go's ServeMux resolves the most specific registered pattern first,
// so an explicitly registered "/metrics" always beats the "/" catch-all that
// carries the middleware chain.
//
// This mirrors the ToolHive operator, which already binds its own metrics
// endpoint to a separate address (--metrics-bind-address), and the wider
// convention for diagnostics ports: etcd's --listen-metrics-urls and
// controller-runtime's --metrics-bind-address.
//
// Note that /health deliberately stays on the application listener. Kubernetes
// liveness and readiness probes target the application port, and the proxy
// health response carries no sensitive fields.
//
// Anything registered on this listener is served WITHOUT authentication,
// authorization, rate limiting, or audit — that is the whole point of moving it
// off the application listener, and it is why the listener must not be routed
// publicly. Only add read-only, non-sensitive endpoints here. In particular, do
// not register pprof or any other debug handler that exposes process memory,
// goroutine state, or configuration.
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

// Timeouts mirror the proxy and vMCP listeners. They matter more here, not less:
// this listener carries no middleware, so nothing else bounds a slow or
// abandoned client.
const (
	// readHeaderTimeout bounds header reads to prevent Slowloris attacks.
	readHeaderTimeout = 10 * time.Second
	// readTimeout bounds reading the entire request, including a body that a
	// client trickles in. The metrics handler ignores request bodies, so without
	// this a connection could be held open indefinitely past the header phase.
	readTimeout = 30 * time.Second
	// writeTimeout bounds writing the response. There are no long-lived streams
	// on this listener, so it applies unconditionally.
	writeTimeout = 30 * time.Second
	// idleTimeout stops idle keep-alive connections from blocking shutdown.
	idleTimeout = 60 * time.Second
	// maxHeaderBytes caps request header size.
	maxHeaderBytes = 1 << 20 // 1 MiB
)

// startupLogMessage is the message logged when the diagnostics listener binds. It
// is referenced by NotServedHereHandler, which tells the reader to grep for it, so
// the two must not drift apart.
const startupLogMessage = "prometheus metrics are served on a dedicated diagnostics port, not the application port"

// bindAttempts bounds how many times Start re-resolves and re-binds when the
// port is claimed between the availability check and the bind. See Server.bind.
const bindAttempts = 3

// NotServedHereHandler answers requests for MetricsPath on an application
// listener, where metrics are deliberately not served.
//
// It exists so the response explains itself. A bare 404 is indistinguishable
// from a typo, and the failure is otherwise silent from the server side: an
// upgrade moves the endpoint and the operator sees only a Prometheus target
// going down.
//
// The body deliberately names no port. The diagnostics listener honours a
// configured port and falls back to an available one when that is taken (see
// Server.bind), so any number written here would be wrong for both of those
// supported configurations — and a confidently wrong address is worse than none
// when the whole point is to redirect someone who is already lost. The startup
// log carries the resolved address and is the one source that is always correct.
//
// The status stays 404 rather than 410 Gone: this handler is also registered on
// deployments that never served metrics on this port, where claiming the
// resource was removed would be untrue.
func NotServedHereHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusNotFound)
		// Best effort: the client has already disconnected if this fails, and
		// there is nothing useful to do about it on a 404.
		_, _ = fmt.Fprintf(w, "%s is not served on this port.\n\n"+
			"When Prometheus metrics are enabled they are served on a dedicated diagnostics\n"+
			"listener, so that access can be restricted separately from MCP traffic.\n\n"+
			"Its address is logged at startup; look for %q.\n"+
			"See docs/observability.md for how to point a scraper at it.\n",
			MetricsPath, startupLogMessage)
	})
}

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
// host is the bind address. port is the requested port; pass DefaultPort unless
// a deployment needs a different one, since a predictable port is what lets a
// scraper find the endpoint. Passing 0 asks the OS for an arbitrary available
// port, which is useful in tests but leaves nothing for a scraper to target.
// Either way, Start falls back to an available port if the requested one is
// taken, so the resolved port is only known after Start — read it from Addr or
// Port.
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

	mux := http.NewServeMux()
	mux.Handle(MetricsPath, s.metricsHandler)

	listener, err := s.bind()
	if err != nil {
		return err
	}

	s.listener = listener
	s.server = &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
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

	// Logged at WARN, not INFO, because this is the only server-side signal that
	// the endpoint is not on the application port. Metrics are opt-in, so this
	// fires only for deployments that enabled them — the exact population whose
	// scrape configuration has to point here. Without it, an upgrade moves the
	// endpoint and the operator's only clue is a Prometheus target going down.
	slog.Warn(startupLogMessage+"; update scrape configuration to target this address",
		"address", listener.Addr().String(), "path", MetricsPath)

	return nil
}

// bind resolves a port and binds it, retrying a bounded number of times.
//
// networking.FindOrUsePort only *checks* availability, so another process can
// claim the port in the window before the actual bind. Retrying rather than
// failing keeps a lost race from taking down the whole workload over a
// diagnostics listener; each retry re-resolves, so a genuinely occupied port
// converges on an alternative. Exhausting the attempts is reported as an error,
// because silently serving nothing would hide the metrics endpoint.
//
// Callers must hold s.mu.
func (s *Server) bind() (net.Listener, error) {
	// Use SO_REUSEADDR for parity with the proxy listeners, which allows port
	// reuse after an unclean shutdown left a zombie holding the port.
	lc := socket.ListenConfig()

	var lastErr error
	for attempt := 1; attempt <= bindAttempts; attempt++ {
		port, err := networking.FindOrUsePort(s.port)
		if err != nil {
			return nil, fmt.Errorf("diagnostics: failed to resolve port: %w", err)
		}

		addr := fmt.Sprintf("%s:%d", s.host, port)
		listener, err := lc.Listen(context.Background(), "tcp", addr)
		if err == nil {
			return listener, nil
		}

		lastErr = err
		slog.Debug("diagnostics port was claimed between availability check and bind, retrying",
			"address", addr, "attempt", attempt, "error", err)
	}

	return nil, fmt.Errorf("diagnostics: failed to bind a listener on %s after %d attempts: %w",
		s.host, bindAttempts, lastErr)
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
