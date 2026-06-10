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
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
)

// backendEnrichmentMiddleware wraps an HTTP handler to add backend routing information
// to audit events by parsing MCP requests and resolving the target backend.
//
// The resolution source depends on the path: on the Serve path (s.core != nil) it
// asks the core (LookupTool/LookupResource/LookupPrompt) and derives the backend from
// Tool.BackendID; on the legacy server.New path it reads the routing table the discovery
// middleware injected into the request context.
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
			backendName := s.resolveBackendName(r.Context(), mcpRequest.Method, mcpRequest.Params)

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
// whether the core is wired (Serve path) or not (legacy path).
func (s *Server) resolveBackendName(ctx context.Context, method string, params map[string]any) string {
	// Serve path: the core is the source of truth. Lookup* applies the same admission
	// filter as List*, so a denied or unadvertised capability resolves to nothing (the
	// backend name is never derived from a capability the caller cannot reach). The
	// discovery-into-context seam is not populated on this path.
	if s.core != nil {
		return s.coreLookupBackendName(ctx, method, params)
	}

	// Legacy path: read the routing table the discovery middleware injected into context.
	caps, ok := discovery.DiscoveredCapabilitiesFromContext(ctx)
	if !ok || caps == nil || caps.RoutingTable == nil {
		return ""
	}
	return lookupBackendName(method, params, caps.RoutingTable)
}

// coreLookupBackendName resolves the backend for an MCP request via the core's
// Lookup* methods, derives the backend from the resolved capability's BackendID,
// and returns the backend's human-readable name. Identity is read from the request
// context at this transport boundary and passed explicitly to the core. Returns ""
// when the capability is unknown, unadvertised, or denied to the caller.
//
// The name (not the raw BackendID) is returned for parity with the legacy path,
// which records BackendTarget.WorkloadName (= backend.Name); recording the same value
// on both paths keeps audit events correlatable across the Serve and server.New paths.
func (s *Server) coreLookupBackendName(ctx context.Context, method string, params map[string]any) string {
	identity, _ := auth.IdentityFromContext(ctx)
	switch method {
	case "tools/call":
		if name, ok := params["name"].(string); ok {
			if tool, err := s.core.LookupTool(ctx, identity, name); err == nil && tool != nil {
				return s.backendDisplayName(ctx, tool.BackendID)
			}
		}
	case "resources/read":
		if uri, ok := params["uri"].(string); ok {
			if res, err := s.core.LookupResource(ctx, identity, uri); err == nil && res != nil {
				return s.backendDisplayName(ctx, res.BackendID)
			}
		}
	case "prompts/get":
		if name, ok := params["name"].(string); ok {
			if prompt, err := s.core.LookupPrompt(ctx, identity, name); err == nil && prompt != nil {
				return s.backendDisplayName(ctx, prompt.BackendID)
			}
		}
	}
	return ""
}

// backendDisplayName resolves a logical backend ID to its human-readable name via
// the registry, so the Serve path records the same audit value (backend.Name) the
// legacy path's WorkloadName carries. It falls back to the ID when the backend is
// not in the registry (mirroring the aggregator's minimal-target fallback), so audit
// still records an identifier rather than dropping the backend entirely.
func (s *Server) backendDisplayName(ctx context.Context, backendID string) string {
	if backendID == "" {
		return ""
	}
	if backend := s.backendRegistry.Get(ctx, backendID); backend != nil {
		return backend.Name
	}
	return backendID
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
