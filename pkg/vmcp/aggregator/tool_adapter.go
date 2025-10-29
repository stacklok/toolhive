// Package aggregator provides capability aggregation for Virtual MCP Server.
package aggregator

import (
	"context"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// processBackendTools applies per-backend filtering and overrides to tools.
// This is called during capability discovery, before conflict resolution.
//
// This function reuses the battle-tested logic from pkg/mcp/tool_filter.go
// by converting vmcp.Tool to mcp.SimpleTool, applying the middleware logic,
// and converting back.
func processBackendTools(
	_ context.Context,
	backendID string,
	tools []vmcp.Tool,
	workloadConfig *config.WorkloadToolConfig,
) []vmcp.Tool {
	if workloadConfig == nil {
		return tools // No configuration for this backend
	}

	// If no filter or overrides configured, return tools as-is
	if len(workloadConfig.Filter) == 0 && len(workloadConfig.Overrides) == 0 {
		return tools
	}

	// Build middleware options from workload config
	var opts []mcp.ToolMiddlewareOption

	// Add filter if configured
	if len(workloadConfig.Filter) > 0 {
		opts = append(opts, mcp.WithToolsFilter(workloadConfig.Filter...))
	}

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
		logger.Warnf("Failed to apply tool filtering for backend %s: %v", backendID, err)
		return tools // Return original tools if processing fails
	}

	// Convert back to vmcp.Tool, preserving InputSchema and BackendID
	result := make([]vmcp.Tool, len(processed))
	for i, simpleTool := range processed {
		// Find the original tool name (before any override)
		originalName := simpleTool.Name
		if revName, wasOverridden := reverseOverrideMap[simpleTool.Name]; wasOverridden {
			originalName = revName
		}

		// Look up the original tool to preserve InputSchema and BackendID
		originalTool := originalToolsByName[originalName]

		// Construct the result tool with processed name/description but original schema
		result[i] = vmcp.Tool{
			Name:        simpleTool.Name,        // Use the processed (potentially overridden) name
			Description: simpleTool.Description, // Use the processed (potentially overridden) description
			InputSchema: originalTool.InputSchema,
			BackendID:   backendID, // Use the backendID parameter (source of truth)
		}
	}

	return result
}
