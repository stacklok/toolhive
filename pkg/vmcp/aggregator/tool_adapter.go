// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package aggregator provides capability aggregation for Virtual MCP Server.
package aggregator

import (
	"context"
	"log/slog"

	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// processBackendTools applies per-backend overrides to tools.
// This is called during capability discovery, before conflict resolution.
//
// This function reuses the battle-tested logic from pkg/mcp/tool_filter.go
// by converting vmcp.Tool to mcp.SimpleTool, applying the middleware logic,
// and converting back.
//
// NOTE: Neither ExcludeAll nor Filter are applied here. Both only affect which
// tools are advertised to MCP clients, not which tools are available for routing.
// This allows composite tools to call backend tools that are excluded from
// direct client access. ExcludeAll and Filter are applied later in MergeCapabilities
// via the shouldAdvertiseTool check.
func processBackendTools(
	_ context.Context,
	backendID string,
	tools []vmcp.Tool,
	workloadConfig *config.WorkloadToolConfig,
) []vmcp.Tool {
	if workloadConfig == nil {
		return tools // No configuration for this backend
	}

	// If no overrides configured, return tools as-is
	// NOTE: Filter is NOT applied here - it only affects advertising, not routing.
	// This ensures filtered tools remain in the routing table for composite tools.
	if len(workloadConfig.Overrides) == 0 {
		return tools
	}

	// Build middleware options from workload config (only overrides, not filter)
	var opts []mcp.ToolMiddlewareOption

	// NOTE: Filter is intentionally NOT applied here. Filter only affects which
	// tools are advertised to MCP clients (like ExcludeAll), not which tools are
	// available in the routing table. This allows composite tools to call
	// filtered backend tools. Filter is checked in shouldAdvertiseTool.

	// Build reverse map: overridden name -> original name (for lookup after processing)
	reverseOverrideMap := make(map[string]string)

	// Add overrides if configured
	if len(workloadConfig.Overrides) > 0 {
		for originalName, override := range workloadConfig.Overrides {
			if override != nil {
				opts = append(opts, mcp.WithToolsOverride(originalName, override.Name, override.Description))
				// Track the mapping from overridden name back to original name
				if override.Name != "" {
					reverseOverrideMap[override.Name] = originalName
				}
			}
		}
	}

	// Convert vmcp.Tool to mcp.SimpleTool
	simpleTools := make([]mcp.SimpleTool, len(tools))
	originalToolsByName := make(map[string]vmcp.Tool, len(tools))
	for i, tool := range tools {
		simpleTools[i] = mcp.SimpleTool{
			Name:        tool.Name,
			Description: tool.Description,
		}
		originalToolsByName[tool.Name] = tool
	}

	// Apply the shared filtering/override logic from pkg/mcp
	processed, err := mcp.ApplyToolFiltering(opts, simpleTools)
	if err != nil {
		slog.Warn("failed to apply tool filtering for backend", "backend", backendID, "error", err)
		return tools // Return original tools if processing fails
	}

	// Convert back to vmcp.Tool, preserving InputSchema and BackendID
	result := make([]vmcp.Tool, 0, len(processed))
	for _, simpleTool := range processed {
		// Find the original tool name (before any override)
		originalName := simpleTool.Name
		if revName, wasOverridden := reverseOverrideMap[simpleTool.Name]; wasOverridden {
			originalName = revName
		}

		// Look up the original tool to preserve InputSchema and BackendID
		originalTool, exists := originalToolsByName[originalName]
		if !exists {
			// This should not happen unless there's a bug in the filtering logic,
			// but skip the tool rather than panicking
			slog.Warn("tool not found in original tools map for backend, skipping", "tool", originalName, "backend", backendID)
			continue
		}

		// Construct the result tool with processed name/description but original schema
		result = append(result, vmcp.Tool{
			Name:        simpleTool.Name,        // Use the processed (potentially overridden) name
			Description: simpleTool.Description, // Use the processed (potentially overridden) description
			InputSchema: originalTool.InputSchema,
			BackendID:   backendID, // Use the backendID parameter (source of truth)
		})
	}

	return result
}
