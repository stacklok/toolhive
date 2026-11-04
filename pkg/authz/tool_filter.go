// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"context"
	"log/slog"
	"time"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
)

// authorizedToolIndexes returns the positions, in tools, of the tools the caller
// (identified by JWT claims in ctx) is authorized to read, so a filter can keep
// the matching raw descriptors. Visibility is decided by tool:read; the
// authorizer decorator makes tool:call imply tool:read. If authorizer is nil,
// every position is returned.
func authorizedToolIndexes(ctx context.Context, a authorizers.Authorizer, tools []mcp.Tool) []int {
	return authorizedToolIndexesWithin(ctx, a, tools, maxDerivedAuthzBudget)
}

// authorizedToolIndexesWithin is authorizedToolIndexes with an explicit shared
// deadline, so tests can exercise the deadline without waiting 30 seconds.
func authorizedToolIndexesWithin(
	ctx context.Context, a authorizers.Authorizer, tools []mcp.Tool, budget time.Duration,
) []int {
	if a == nil {
		indexes := make([]int, len(tools))
		for i := range tools {
			indexes[i] = i
		}
		return indexes
	}

	// One deadline covers the whole list, so a slow PDP cannot stretch tools/list by
	// the tool count. Tools checked after it expires are hidden.
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

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

		authorized, err := authorizeToolRead(toolCtx, a, tool.Name, nil)
		if err != nil {
			if ctx.Err() != nil {
				// The shared deadline expired or the request was cancelled. The rest of
				// the list is hidden, so one warning is enough.
				slog.Warn("Authorization deadline reached while filtering tools, hiding the rest",
					"tool", tool.Name, "error", err)
				break
			}
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

// authorizeToolRead checks whether the caller is authorized to read (see) a specific
// tool. Returns true if authorized, false if denied. If authorizer is nil, returns
// true (no-op).
func authorizeToolRead(
	ctx context.Context, a authorizers.Authorizer, toolName string, arguments map[string]interface{},
) (bool, error) {
	if a == nil {
		return true, nil
	}

	return a.AuthorizeWithJWTClaims(
		ctx,
		authorizers.MCPFeatureTool,
		authorizers.MCPOperationRead,
		toolName,
		arguments,
	)
}
