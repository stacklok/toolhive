// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
)

// backendEnrichmentMiddleware wraps an HTTP handler to add backend routing information
// to audit events by parsing MCP requests and resolving the target backend.
//
// The resolution source depends on the path: on the Serve path (s.core != nil) it
// reads the per-session advertised-set cache populated at session registration
// (keyed by the Mcp-Session-Id header), so it never re-aggregates backend
// capabilities on a request; on the legacy server.New path it reads the routing
// table the discovery middleware injected into the request context.
func (s *Server) backendEnrichmentMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read and parse the request body to extract MCP method and parameters
		var requestBody []byte
		if r.Body != nil {
			var err error
			requestBody, err = io.ReadAll(r.Body)
			// Always restore body for next handler, even on error
			if err != nil {
				// Log the error and restore an empty body to ensure consistent behavior
				slog.Warn("failed to read request body in backend enrichment middleware",
					"error", err)
				r.Body = io.NopCloser(bytes.NewReader([]byte{}))
			} else {
				// Restore body with the read content
				r.Body = io.NopCloser(bytes.NewReader(requestBody))
			}
		}

		// Parse MCP request to extract tool/resource name
		var mcpRequest struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}

		if len(requestBody) > 0 && json.Unmarshal(requestBody, &mcpRequest) == nil {
			// The session ID (Serve path) comes from the same header the SDK and the
			// legacy discovery middleware use; it is "" for initialize and on the
			// legacy path, where resolveBackendName ignores it.
			sessionID := r.Header.Get("Mcp-Session-Id")
			backendName := s.resolveBackendName(r.Context(), sessionID, mcpRequest.Method, mcpRequest.Params)

			// Mutate the existing BackendInfo from audit middleware
			if backendName != "" {
				if backendInfo, ok := audit.BackendInfoFromContext(r.Context()); ok && backendInfo != nil {
					backendInfo.BackendName = backendName
				}
			}
		}

		// Call next handler
		next.ServeHTTP(w, r)
	})
}

// resolveBackendName resolves the backend handling an MCP request, dispatching on
// whether the core is wired (Serve path) or not (legacy path). sessionID is the
// Mcp-Session-Id header value; it is used only on the Serve path.
func (s *Server) resolveBackendName(ctx context.Context, sessionID, method string, params map[string]any) string {
	// Serve path: read the per-session advertised-set cache built once at session
	// registration. The discovery-into-context seam is not populated on this path.
	if s.core != nil {
		return s.cachedBackendName(sessionID, method, params)
	}

	// Legacy path: read the routing table the discovery middleware injected into context.
	caps, ok := discovery.DiscoveredCapabilitiesFromContext(ctx)
	if !ok || caps == nil || caps.RoutingTable == nil {
		return ""
	}
	return lookupBackendName(method, params, caps.RoutingTable)
}

// cachedBackendName resolves the backend serving an MCP request from the
// per-session advertised-set cache populated at session registration
// (injectCoreSessionCapabilities) from the core's single per-session aggregation.
// It performs only map lookups — unlike a core.Lookup* call it does NOT
// re-aggregate backend capabilities, which is the whole point of the cache
// (issue #5493).
//
// The cached value is the backend's human-readable name (not the raw BackendID),
// for parity with the legacy path, which records BackendTarget.WorkloadName
// (= backend.Name); recording the same value on both paths keeps audit events
// correlatable across the Serve and server.New paths. Conflict resolution and the
// admission filter were already applied by the core when the set was built, so the
// cache holds the advertised (possibly renamed) name a client actually calls and
// never a denied capability.
//
// Returns "" when the session is unknown on this replica (a cross-pod request with
// no session affinity, or an already-evicted session) or the capability is not in
// the advertised set. A miss degrades the audit label gracefully and never triggers
// a second aggregation. prompts/get is not resolved: the Serve path does not
// advertise prompts, so there is no prompt to label (see advertisedSet).
func (s *Server) cachedBackendName(sessionID, method string, params map[string]any) string {
	set, ok := s.advertisedSets.get(sessionID)
	if !ok {
		return ""
	}
	return set.backendName(method, params)
}

// lookupBackendName looks up which backend handles a given MCP request.
func lookupBackendName(method string, params map[string]any, routingTable *vmcp.RoutingTable) string {
	switch method {
	case "tools/call":
		if toolName, ok := params["name"].(string); ok {
			if target, exists := routingTable.Tools[toolName]; exists {
				return target.WorkloadName
			}
		}
	case "resources/read":
		if uri, ok := params["uri"].(string); ok {
			if target, exists := routingTable.Resources[uri]; exists {
				return target.WorkloadName
			}
		}
	case "prompts/get":
		if promptName, ok := params["name"].(string); ok {
			if target, exists := routingTable.Prompts[promptName]; exists {
				return target.WorkloadName
			}
		}
	}
	return ""
}
