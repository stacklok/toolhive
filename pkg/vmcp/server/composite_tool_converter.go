// Package server implements the Virtual MCP Server that aggregates
// multiple backend MCP servers into a unified interface.
package server

import (
	"fmt"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
)

// convertWorkflowDefsToTools converts workflow definitions to vmcp.Tool format.
//
// This creates the tool metadata (name, description, schema) that gets exposed
// via the MCP tools/list endpoint. The actual workflow execution logic is handled
// by the workflow executor adapters created separately.
//
// Each workflow definition becomes a tool with:
//   - Name: workflow.Name
//   - Description: workflow.Description
//   - InputSchema: workflow.Parameters (JSON Schema format)
//
// Returns a slice of vmcp.Tool ready for aggregation and exposure to clients.
func convertWorkflowDefsToTools(defs map[string]*composer.WorkflowDefinition) []vmcp.Tool {
	if len(defs) == 0 {
		return nil // Idiomatic Go: nil slice for empty result
	}

	tools := make([]vmcp.Tool, 0, len(defs))
	for _, def := range defs {
		tool := vmcp.Tool{
			Name:        def.Name,
			Description: def.Description,
			InputSchema: def.Parameters,
		}
		tools = append(tools, tool)
	}

	return tools
}

// validateNoToolConflicts validates that composite tool names don't conflict with backend tool names.
//
// Tool name conflicts would cause ambiguity in routing/execution:
//   - Which tool should be invoked when a client calls the name?
//   - Should it route to the backend or execute the workflow?
//
// This validation ensures clear separation and prevents runtime confusion.
// Returns an error listing all conflicting tool names if any conflicts are found.
func validateNoToolConflicts(backendTools, compositeTools []vmcp.Tool) error {
	// Build set of backend tool names for O(1) lookups
	backendNames := make(map[string]bool, len(backendTools))
	for _, tool := range backendTools {
		backendNames[tool.Name] = true
	}

	// Check for conflicts
	var conflicts []string
	for _, compTool := range compositeTools {
		if backendNames[compTool.Name] {
			conflicts = append(conflicts, compTool.Name)
		}
	}

	if len(conflicts) > 0 {
		return fmt.Errorf("%w: composite tool names conflict with backend tools: %v",
			vmcp.ErrToolNameConflict, conflicts)
	}

	return nil
}
