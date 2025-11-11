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

			// Initialize request: discover and cache capabilities in session.
			if sessionID == "" {
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

					if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
						http.Error(w, http.StatusText(http.StatusGatewayTimeout), http.StatusGatewayTimeout)
						return
					}

					http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
					return
				}

				logger.Debugw("capability discovery completed",
					"method", r.Method,
					"path", r.URL.Path,
					"tool_count", len(capabilities.Tools),
					"resource_count", len(capabilities.Resources),
					"prompt_count", len(capabilities.Prompts))

				ctx = WithDiscoveredCapabilities(ctx, capabilities)
			} else {
				// Retrieve cached capabilities from session
				logger.Debugw("retrieving capabilities from session for subsequent request",
					"session_id", sessionID,
					"method", r.Method,
					"path", r.URL.Path)

				sess, ok := sessionManager.Get(sessionID)
				if !ok {
					logger.Errorw("session not found",
						"session_id", sessionID,
						"method", r.Method,
						"path", r.URL.Path)
					http.Error(w, "Session not found", http.StatusUnauthorized)
					return
				}

				vmcpSess, ok := sess.(*vmcpsession.VMCPSession)
				if !ok {
					logger.Errorw("session is not a VMCPSession - factory misconfiguration",
						"session_id", sessionID,
						"actual_type", fmt.Sprintf("%T", sess),
						"method", r.Method,
						"path", r.URL.Path)
					http.Error(w, "Invalid session type", http.StatusInternalServerError)
					return
				}

				routingTable := vmcpSess.GetRoutingTable()
				if routingTable == nil {
					logger.Errorw("routing table not initialized in VMCPSession",
						"session_id", sessionID,
						"method", r.Method,
						"path", r.URL.Path)
					http.Error(w, "Session capabilities not initialized", http.StatusInternalServerError)
					return
				}

				// Reconstruct minimal AggregatedCapabilities for routing
				capabilities := &aggregator.AggregatedCapabilities{
					RoutingTable: routingTable,
				}

				logger.Debugw("capabilities retrieved from session",
					"session_id", sessionID,
					"tool_count", len(routingTable.Tools),
					"resource_count", len(routingTable.Resources),
					"prompt_count", len(routingTable.Prompts))

				ctx = WithDiscoveredCapabilities(ctx, capabilities)
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
