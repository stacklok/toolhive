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

// withBackendEnrichment wraps h with the legacy audit backend-enrichment middleware
// when audit is enabled on the legacy (server.New) path. On the Serve path
// (s.core != nil) the per-session tool/resource handlers write the backend label
// directly into the audit BackendInfo (serve_handlers.go), so the middleware — with
// its per-request body re-read and routing-table lookup — is not wired and h is
// returned unchanged (#5512 review). The legacy wiring is retired with the rest of the
// discovery path in #5445.
func (s *Server) withBackendEnrichment(h http.Handler) http.Handler {
	if s.config.AuditConfig != nil && s.core == nil {
		slog.Info("backend enrichment middleware enabled for audit events")
		return backendEnrichmentMiddleware(h)
	}
	return h
}

// backendEnrichmentMiddleware wraps an HTTP handler to add backend routing information
// to audit events by parsing MCP requests and resolving the target backend.
//
// This is the legacy (server.New) path's audit labelling: it reads the routing table
// the discovery middleware injected into the request context. It is only wired when
// s.core == nil (see (*Server).Handler) — on the Serve path the per-session handlers
// label the audit event directly (serve_handlers.go), so this middleware, with its
// per-request body re-read, is not in the chain. Retired with the discovery path in #5445.
//
// It is a free function (not a *Server method): with the Serve branch gone it reads no
// Server state — resolution comes entirely from the request context.
func backendEnrichmentMiddleware(next http.Handler) http.Handler {
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
			backendName := resolveBackendName(r.Context(), mcpRequest.Method, mcpRequest.Params)

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

// resolveBackendName resolves the backend handling an MCP request from the routing
// table the discovery middleware injected into the request context (legacy path).
// The middleware is only wired when s.core == nil, so there is no Serve-path branch.
func resolveBackendName(ctx context.Context, method string, params map[string]any) string {
	caps, ok := discovery.DiscoveredCapabilitiesFromContext(ctx)
	if !ok || caps == nil || caps.RoutingTable == nil {
		return ""
	}
	return lookupBackendName(method, params, caps.RoutingTable)
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
