// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"context"
	"log/slog"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
)

// authorizedToolIndexes returns the positions, in tools, of the tools the caller
// (identified by JWT claims in ctx) is authorized to call, so a filter can keep
// the matching raw descriptors. If authorizer is nil, every position is returned.
func authorizedToolIndexes(ctx context.Context, a authorizers.Authorizer, tools []mcp.Tool) []int {
	if a == nil {
		indexes := make([]int, len(tools))
		for i := range tools {
			indexes[i] = i
		}
		return indexes
	}

	authorizedIndexes := make([]int, 0, len(tools))
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

		authorized, err := authorizeToolCall(toolCtx, a, tool.Name, nil)
		if err != nil {
			slog.Warn("Authorization check failed for tool, skipping",
				"tool", tool.Name, "error", err)
			continue
		}

		if authorized {
			authorizedIndexes = append(authorizedIndexes, i)
		} else {
			slog.Debug("Tool denied by authorization policy",
				"tool", tool.Name)
		}
	}

	if denied := len(tools) - len(authorizedIndexes); denied > 0 {
		slog.Debug("Authorization policy filtered tools",
			"total", len(tools), "allowed", len(authorizedIndexes), "denied", denied)
	}

	return authorizedIndexes
}

// authorizeToolCall checks whether the caller is authorized to call a specific tool
// with the given arguments. Returns true if authorized, false if denied.
// If authorizer is nil, returns true (no-op).
func authorizeToolCall(
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
