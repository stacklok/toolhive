// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"log/slog"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
)

// AnnotationEnrichmentMiddleware creates middleware that reads tool annotations
// from the discovery context and injects them into the request context for
// the authz middleware to use.
//
// This middleware sits between discovery and authz in the middleware chain:
//
//	... -> discovery -> annotation-enrichment -> authz -> ...
//
// It only enriches context for tools/call requests. For all other request
// types, it passes through without modification.
func AnnotationEnrichmentMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Only enrich for tools/call requests where authz needs annotation data.
		parsedReq := mcpparser.GetParsedMCPRequest(ctx)
		if parsedReq == nil || parsedReq.Method != string(mcp.MethodToolsCall) {
			next.ServeHTTP(w, r)
			return
		}

		toolName := parsedReq.ResourceID
		if toolName == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Get discovered capabilities from context (set by discovery middleware).
		caps, ok := discovery.DiscoveredCapabilitiesFromContext(ctx)
		if !ok || caps == nil {
			next.ServeHTTP(w, r)
			return
		}

		// Search all tool lists (backend tools and composite tools) for a match.
		if ann := findToolAnnotations(toolName, caps); ann != nil {
			ctx = authorizers.WithToolAnnotations(ctx, ann)
			r = r.WithContext(ctx)
			slog.Debug("enriched request context with tool annotations",
				"tool", toolName,
				"readOnlyHint", ann.ReadOnlyHint,
				"destructiveHint", ann.DestructiveHint,
				"idempotentHint", ann.IdempotentHint,
				"openWorldHint", ann.OpenWorldHint,
			)
		}

		next.ServeHTTP(w, r)
	})
}

// findToolAnnotations searches for a tool by name in the aggregated capabilities
// and converts its vmcp.ToolAnnotations to the authorizers.ToolAnnotations format.
// Returns nil if the tool is not found or has no annotations.
func findToolAnnotations(toolName string, caps *aggregator.AggregatedCapabilities) *authorizers.ToolAnnotations {
	// Search backend tools first, then composite tools.
	for _, tool := range caps.Tools {
		if tool.Name == toolName && tool.Annotations != nil {
			return convertAnnotations(tool.Annotations)
		}
	}
	for _, tool := range caps.CompositeTools {
		if tool.Name == toolName && tool.Annotations != nil {
			return convertAnnotations(tool.Annotations)
		}
	}
	return nil
}

// convertAnnotations converts vmcp.ToolAnnotations to authorizers.ToolAnnotations.
// Only authorization-relevant hint fields are mapped; informational fields like
// Title are intentionally omitted since they are not used in policy evaluation.
// Returns nil if the source annotations contain no hint fields.
func convertAnnotations(ann *vmcp.ToolAnnotations) *authorizers.ToolAnnotations {
	if ann.ReadOnlyHint == nil && ann.DestructiveHint == nil &&
		ann.IdempotentHint == nil && ann.OpenWorldHint == nil {
		return nil
	}
	return &authorizers.ToolAnnotations{
		ReadOnlyHint:    ann.ReadOnlyHint,
		DestructiveHint: ann.DestructiveHint,
		IdempotentHint:  ann.IdempotentHint,
		OpenWorldHint:   ann.OpenWorldHint,
	}
}
