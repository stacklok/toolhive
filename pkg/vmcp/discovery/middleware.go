// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package discovery provides lazy per-user capability discovery for vMCP servers.
//
// Capabilities are discovered at session initialization and cached in the session for
// its lifetime. This ensures deterministic behavior and prevents notification spam from
// redundant capability updates when backends haven't changed.
//
// For MultiSession requests, the middleware injects routing context from the session's
// routing table so that composite tool workflow steps can route backend tool calls correctly.
// Tool routing for non-composite tools is handled by session-scoped handlers registered
// with AddSessionTools.
//
// Future enhancement: Add manager-level capability cache to share discoveries across
// sessions, plus separate background refresh worker (not in middleware request path)
// that periodically rediscovers capabilities, detects changes via hash comparison, and
// pushes updates to active sessions via MCP tools/list_changed notifications. Middleware
// flow remains unchanged - still just retrieves from session cache on subsequent requests.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

const (
	// discoveryTimeout is the maximum time for capability discovery.
	discoveryTimeout = 15 * time.Second
)

// middlewareConfig holds optional configuration for Middleware.
type middlewareConfig struct {
	sessionScopedRouting bool
	timeout              time.Duration
}

// MiddlewareOption configures Middleware behaviour.
type MiddlewareOption func(*middlewareConfig)

// WithSessionScopedRouting disables backend capability discovery for any request
// that arrives without an Mcp-Session-Id header (i.e. initialize requests).
// Use this when tools are registered per-session via AddSessionTools rather
// than through the discovery pipeline.
func WithSessionScopedRouting() MiddlewareOption {
	return func(c *middlewareConfig) {
		c.sessionScopedRouting = true
	}
}

// WithDiscoveryTimeout overrides the default discovery timeout.
func WithDiscoveryTimeout(timeout time.Duration) MiddlewareOption {
	return func(c *middlewareConfig) {
		c.timeout = timeout
	}
}

// Middleware performs capability discovery on session initialization and injects
// routing context for subsequent requests. Must be placed after auth middleware.
//
// Initialize requests (no session ID): discovers capabilities and stores in context.
// Subsequent requests (MultiSession): injects routing table from session into context
// so composite tool workflow steps can route backend tool calls correctly.
//
// Returns HTTP 504 for timeouts, HTTP 503 for discovery errors.
//
// The registry parameter provides the current list of backends. For dynamic environments
// (Kubernetes with DynamicRegistry), backends are fetched on each initialize request to
// ensure the latest backend list is used for capability discovery.
//
// The healthStatusProvider parameter (optional, can be nil) enables filtering backends
// based on current health status from the health monitor. When provided, only healthy and
// degraded backends are included in capability aggregation; unhealthy, unknown, and
// unauthenticated backends are excluded (which includes backends with OPEN circuit breakers).
// When nil (health monitoring disabled), the initial health status from the registry is used.
func Middleware(
	manager Manager,
	registry vmcp.BackendRegistry,
	sessionManager *transportsession.Manager,
	healthStatusProvider health.StatusProvider,
	opts ...MiddlewareOption,
) func(http.Handler) http.Handler {
	cfg := middlewareConfig{
		timeout: discoveryTimeout,
	}
	for _, o := range opts {
		o(&cfg)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			sessionID := r.Header.Get("Mcp-Session-Id")

			var err error
			if sessionID == "" {
				if cfg.sessionScopedRouting {
					// Session-scoped routing registers capabilities via the OnRegisterSession
					// hook rather than through discovery. Skip discovery on initialize.
					next.ServeHTTP(w, r)
					return
				}
				// Initialize request: discover and cache capabilities in session.
				ctx, err = handleInitializeRequest(ctx, r, manager, registry, healthStatusProvider, cfg.timeout)
			} else {
				// Subsequent request: retrieve cached capabilities from session.
				ctx, err = handleSubsequentRequest(ctx, r, sessionID, sessionManager)
			}

			if err != nil {
				handleDiscoveryError(w, r, err)
				return
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// filterHealthyBackends filters backends to only include those that are healthy
// or degraded. Backends that are unhealthy, unknown, or unauthenticated are excluded
// from capability aggregation to prevent exposing tools from unavailable backends.
//
// Health status filtering:
//   - healthy: included (fully operational)
//   - degraded: included (slow but working)
//   - empty/zero-value: included (assume healthy when health monitoring is disabled)
//   - unhealthy: excluded (not responding, circuit breaker may be open)
//   - unknown: excluded (status not yet determined)
//   - unauthenticated: excluded (authentication failed)
//
// When healthStatusProvider is provided, the current health status from the health
// monitor is used (respects circuit breaker state). When nil, falls back to the
// initial health status from the backend registry.
func filterHealthyBackends(backends []vmcp.Backend, healthStatusProvider health.StatusProvider) []vmcp.Backend {
	if len(backends) == 0 {
		return backends
	}

	healthy := make([]vmcp.Backend, 0, len(backends))
	excluded := 0

	for i := range backends {
		backend := &backends[i]

		// Get current health status from health monitor if available
		// This ensures circuit breaker state is respected during capability aggregation
		var healthStatus vmcp.BackendHealthStatus
		if healthStatusProvider != nil {
			if status, exists := healthStatusProvider.QueryBackendStatus(backend.ID); exists {
				healthStatus = status
			} else {
				// Backend not tracked by health monitor - use registry status
				healthStatus = backend.HealthStatus
			}
		} else {
			// Health monitoring disabled - use registry status
			healthStatus = backend.HealthStatus
		}

		// Include healthy, degraded, and empty/zero-value (assume healthy) backends.
		// Explicitly exclude unhealthy, unknown, and unauthenticated backends.
		if healthStatus == "" ||
			healthStatus == vmcp.BackendHealthy ||
			healthStatus == vmcp.BackendDegraded {
			healthy = append(healthy, *backend)
		} else {
			excluded++
			//nolint:gosec // G706: backend fields are internal, not user-controlled
			slog.Debug("excluding backend from capability aggregation due to health status",
				"backend_name", backend.Name,
				"backend_id", backend.ID,
				"health_status", healthStatus,
				"source", func() string {
					if healthStatusProvider != nil {
						return "health_monitor"
					}
					return "registry"
				}())
		}
	}

	if excluded > 0 {
		//nolint:gosec // G706: values are internal counts, not user-controlled
		slog.Debug("filtered backends for capability aggregation",
			"total_backends", len(backends),
			"healthy_backends", len(healthy),
			"excluded_backends", excluded)
	}

	return healthy
}

// handleInitializeRequest performs capability discovery for initialize requests.
// Returns updated context with discovered capabilities or an error.
//
// For dynamic environments, backends are fetched from the registry on each request
// to ensure the latest backend list is used (e.g., when backends are added/removed).
//
// When healthStatusProvider is provided, backends are filtered based on current health
// status from the health monitor (respects circuit breaker state). When nil, the initial
// health status from the backend registry is used.
func handleInitializeRequest(
	ctx context.Context,
	r *http.Request,
	manager Manager,
	registry vmcp.BackendRegistry,
	healthStatusProvider health.StatusProvider,
	timeout time.Duration,
) (context.Context, error) {
	discoveryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Get current backend list from registry (supports dynamic backend changes)
	allBackends := registry.List(discoveryCtx)

	// Filter to only include healthy/degraded backends for capability aggregation
	// Uses current health status from health monitor when available
	backends := filterHealthyBackends(allBackends, healthStatusProvider)

	//nolint:gosec // G706: request method/path are standard HTTP fields, not injection vectors
	slog.Debug("starting capability discovery for initialize request",
		"method", r.Method,
		"path", r.URL.Path,
		"total_backend_count", len(allBackends),
		"healthy_backend_count", len(backends))

	capabilities, err := manager.Discover(discoveryCtx, backends)
	if err != nil {
		//nolint:gosec // G706: request method/path are standard HTTP fields, not injection vectors
		slog.Error("capability discovery failed",
			"error", err,
			"method", r.Method,
			"path", r.URL.Path)
		return ctx, fmt.Errorf("discovery failed: %w", err)
	}

	//nolint:gosec // G706: request method/path are standard HTTP fields, not injection vectors
	slog.Debug("capability discovery completed",
		"method", r.Method,
		"path", r.URL.Path,
		"tool_count", len(capabilities.Tools),
		"resource_count", len(capabilities.Resources),
		"prompt_count", len(capabilities.Prompts))

	return WithDiscoveredCapabilities(ctx, capabilities), nil
}

// handleSubsequentRequest retrieves cached capabilities from the session.
// Returns updated context with capabilities or an error.
func handleSubsequentRequest(
	ctx context.Context,
	r *http.Request,
	sessionID string,
	sessionManager *transportsession.Manager,
) (context.Context, error) {
	//nolint:gosec // G706: session ID and request fields are not injection vectors
	slog.Debug("retrieving capabilities from session for subsequent request",
		"session_id", sessionID,
		"method", r.Method,
		"path", r.URL.Path)

	// First, validate the session exists at all.
	rawSess, exists := sessionManager.Get(sessionID)
	if !exists {
		//nolint:gosec // G706: session ID is not an injection vector
		slog.Error("session not found",
			"session_id", sessionID,
			"method", r.Method,
			"path", r.URL.Path)
		return ctx, fmt.Errorf("session not found: %s", sessionID)
	}

	// Backend tool calls are routed by session-scoped handlers registered with the SDK.
	// However, composite tool workflow steps go through the shared router which requires
	// DiscoveredCapabilities in the context. Inject capabilities built from the session's
	// routing table so composite workflows can route backend tool calls correctly.
	multiSess, isMulti := rawSess.(vmcpsession.MultiSession)
	if !isMulti {
		// The session is still a StreamableSession placeholder — Phase 2
		// (OnRegisterSession / CreateSession) has not yet replaced it with a
		// MultiSession. This can happen if the client sends a request in the
		// brief window between receiving the session ID and the hook completing.
		// Skip capability injection and let the SDK respond (tools list will be
		// temporarily empty, but no 500 is returned to the client).
		//nolint:gosec // G706: session ID is not an injection vector
		slog.Debug("session initialisation in progress, skipping capability injection",
			"session_id", sessionID)
		return ctx, nil
	}

	routingTable := multiSess.GetRoutingTable()
	if routingTable == nil {
		// Session initialisation not yet complete; no capabilities to inject.
		// Composite tool calls will fail routing, but backend tool calls are
		// already registered with the SDK and will succeed.
		//nolint:gosec // G706: session ID is not an injection vector
		slog.Debug("multi-session routing table not yet initialised; skipping capability injection",
			"session_id", sessionID)
		return ctx, nil
	}
	//nolint:gosec // G706: session ID is not an injection vector
	slog.Debug("injecting capabilities from multi-session routing table for composite tool routing",
		"session_id", sessionID,
		"tool_count", len(routingTable.Tools))
	capabilities := &aggregator.AggregatedCapabilities{
		RoutingTable: routingTable,
		Tools:        multiSess.Tools(),
	}
	return WithDiscoveredCapabilities(ctx, capabilities), nil
}

// handleDiscoveryError writes appropriate HTTP error responses based on the error type.
func handleDiscoveryError(w http.ResponseWriter, _ *http.Request, err error) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		http.Error(w, http.StatusText(http.StatusGatewayTimeout), http.StatusGatewayTimeout)
		return
	}

	// Check for session-related errors
	errMsg := err.Error()
	if strings.Contains(errMsg, "session not found") {
		http.Error(w, "Session not found", http.StatusUnauthorized)
		return
	}

	// Default to service unavailable for other errors
	http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
}
