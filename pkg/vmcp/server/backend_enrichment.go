// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
)

// backendEnrichmentMiddleware wraps an HTTP handler to add backend routing information
// to audit events by parsing MCP requests and looking up backends in the routing table.
func (*Server) backendEnrichmentMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read and parse the request body to extract MCP method and parameters
		var requestBody []byte
		if r.Body != nil {
			var err error
			requestBody, err = io.ReadAll(r.Body)
			// Always restore body for next handler, even on error
			if err != nil {
				// Log the error and restore an empty body to ensure consistent behavior
				logger.Warnw("failed to read request body in backend enrichment middleware",
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
			// Get routing table from discovered capabilities in context
			caps, ok := discovery.DiscoveredCapabilitiesFromContext(r.Context())
			if ok && caps != nil && caps.RoutingTable != nil {
				backendName := lookupBackendName(mcpRequest.Method, mcpRequest.Params, caps.RoutingTable)

				// Mutate the existing BackendInfo from audit middleware
				if backendName != "" {
					if backendInfo, ok := audit.BackendInfoFromContext(r.Context()); ok && backendInfo != nil {
						backendInfo.BackendName = backendName
					}
				}
			}
		}

		// Call next handler
		next.ServeHTTP(w, r)
	})
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
