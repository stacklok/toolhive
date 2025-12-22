// Package discovery provides lazy per-user capability discovery for vMCP servers.
//
// Capabilities are discovered at session initialization and cached in the session for
// its lifetime. This ensures deterministic behavior and prevents notification spam from
// redundant capability updates when backends haven't changed.
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
	"net/http"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/logger"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

const (
	// discoveryTimeout is the maximum time for capability discovery.
	discoveryTimeout = 15 * time.Second
)

// Middleware performs capability discovery on session initialization and retrieves
// cached capabilities for subsequent requests. Must be placed after auth middleware.
//
// Initialize requests (no session ID): discovers capabilities and stores in context.
// Subsequent requests: retrieves routing table from VMCPSession and reconstructs context.
//
// Returns HTTP 504 for timeouts, HTTP 503 for discovery errors.
func Middleware(
	manager Manager,
	backends []vmcp.Backend,
	sessionManager *transportsession.Manager,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			sessionID := r.Header.Get("Mcp-Session-Id")

			var err error
			if sessionID == "" {
				// Initialize request: discover and cache capabilities in session.
				ctx, err = handleInitializeRequest(ctx, r, manager, backends)
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

// handleInitializeRequest performs capability discovery for initialize requests.
// Returns updated context with discovered capabilities or an error.
func handleInitializeRequest(
	ctx context.Context,
	r *http.Request,
	manager Manager,
	backends []vmcp.Backend,
) (context.Context, error) {
	discoveryCtx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()

	logger.Debugw("starting capability discovery for initialize request",
		"method", r.Method,
		"path", r.URL.Path,
		"backend_count", len(backends))

	capabilities, err := manager.Discover(discoveryCtx, backends)
	if err != nil {
		logger.Errorw("capability discovery failed",
			"error", err,
			"method", r.Method,
			"path", r.URL.Path)
		return ctx, fmt.Errorf("discovery failed: %w", err)
	}

	logger.Debugw("capability discovery completed",
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
	logger.Debugw("retrieving capabilities from session for subsequent request",
		"session_id", sessionID,
		"method", r.Method,
		"path", r.URL.Path)

	// Retrieve and validate session
	vmcpSess, err := vmcpsession.GetVMCPSession(sessionID, sessionManager)
	if err != nil {
		logger.Errorw("failed to get VMCPSession",
			"error", err,
			"session_id", sessionID,
			"method", r.Method,
			"path", r.URL.Path)
		return ctx, err
	}

	// Get routing table from session
	routingTable := vmcpSess.GetRoutingTable()
	if routingTable == nil {
		logger.Errorw("routing table not initialized in VMCPSession",
			"session_id", sessionID,
			"method", r.Method,
			"path", r.URL.Path)
		return ctx, fmt.Errorf("routing table not initialized")
	}

	// Get tools from session (needed for type coercion in composite tool workflows)
	tools := vmcpSess.GetTools()

	// Reconstruct AggregatedCapabilities for routing and type coercion
	capabilities := &aggregator.AggregatedCapabilities{
		RoutingTable: routingTable,
		Tools:        tools,
	}

	logger.Debugw("capabilities retrieved from session",
		"session_id", sessionID,
		"tool_count", len(routingTable.Tools),
		"resource_count", len(routingTable.Resources),
		"prompt_count", len(routingTable.Prompts))

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

	if strings.Contains(errMsg, "invalid session type") ||
		strings.Contains(errMsg, "routing table not initialized") {
		http.Error(w, errMsg, http.StatusInternalServerError)
		return
	}

	// Default to service unavailable for other errors
	http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
}
