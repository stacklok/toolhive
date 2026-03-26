// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"context"
	"log/slog"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/authz/authorizers"
)

// FilterToolsByPolicy filters tools based on Cedar authorization policies.
// For each tool, it checks whether the caller (identified by JWT claims in ctx)
// is authorized to call that tool. Only authorized tools are returned.
// If authorizer is nil, all tools are returned unmodified.
func FilterToolsByPolicy(ctx context.Context, a authorizers.Authorizer, tools []mcp.Tool) []mcp.Tool {
	if a == nil {
		return tools
	}

	// Note: instantiating the list ensures that no null value is sent over the wire.
	// This is basically defensive programming, but for clients.
	filtered := make([]mcp.Tool, 0, len(tools))
	for i, tool := range tools {
		// Inject this tool's annotations into the context so Cedar policies
		// that use when clauses on resource attributes (e.g. resource.readOnlyHint)
		// can evaluate correctly. Without this, the authorization check runs
		// against a context with no annotations and all when clauses fail.
		toolCtx := ctx
		ann := &tools[i].Annotations
		if hasAnyHint(ann) {
			toolCtx = authorizers.WithToolAnnotations(toolCtx, convertMCPAnnotation(ann))
		}

		authorized, err := AuthorizeToolCall(toolCtx, a, tool.Name, nil)
		if err != nil {
			slog.Warn("Authorization check failed for tool, skipping",
				"tool", tool.Name, "error", err)
			continue
		}

		if authorized {
			filtered = append(filtered, tool)
		}
	}

	return filtered
}

// AuthorizeToolCall checks whether the caller is authorized to call a specific tool
// with the given arguments. Returns true if authorized, false if denied.
// If authorizer is nil, returns true (no-op).
func AuthorizeToolCall(
	ctx context.Context, a authorizers.Authorizer, toolName string, arguments map[string]interface{},
) (bool, error) {
	if a == nil {
		return true, nil
	}

	return a.AuthorizeWithJWTClaims(
		ctx,
		authorizers.MCPFeatureTool,
		authorizers.MCPOperationCall,
		toolName,
		arguments,
	)
}
