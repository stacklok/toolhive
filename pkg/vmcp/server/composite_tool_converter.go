// Package server implements the Virtual MCP Server that aggregates
// multiple backend MCP servers into a unified interface.
package server

import (
	"fmt"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
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
//   - OutputSchema: workflow.Output (JSON Schema format, if defined)
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

		// Include output schema if defined
		if def.Output != nil {
			tool.OutputSchema = buildOutputSchema(def.Output)
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

// buildOutputSchema converts an OutputConfig to MCP-compliant JSON Schema format.
//
// This builds the output schema that is exposed to MCP clients via tools/list.
// The schema follows the MCP specification for output schemas, which uses
// standard JSON Schema format with type="object" and properties.
//
// Per MCP spec: https://modelcontextprotocol.io/specification/2025-06-18/server/tools#output-schema
//
// The returned schema has the format:
//
//	{
//	  "type": "object",
//	  "properties": {
//	    "property_name": {
//	      "type": "string",
//	      "description": "Property description"
//	    }
//	  },
//	  "required": ["property_name"]
//	}
//
// Note: The Value field (used for runtime template expansion) is NOT included
// in the schema exposed to clients. Only type and description metadata are included.
func buildOutputSchema(output *config.OutputConfig) map[string]any {
	if output == nil {
		return nil
	}

	properties := make(map[string]any)

	// Convert each output property to JSON Schema format
	for name, prop := range output.Properties {
		properties[name] = buildOutputPropertySchema(prop)
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}

	// Include required fields if specified
	if len(output.Required) > 0 {
		schema["required"] = output.Required
	}

	return schema
}

// buildOutputPropertySchema converts an OutputProperty to JSON Schema format.
// This recursively handles nested properties for object types.
func buildOutputPropertySchema(prop config.OutputProperty) map[string]any {
	schema := map[string]any{
		"type":        prop.Type,
		"description": prop.Description,
	}

	// For object types with nested properties, recursively build the schema
	if prop.Type == "object" && len(prop.Properties) > 0 {
		nestedProps := make(map[string]any)
		for nestedName, nestedProp := range prop.Properties {
			nestedProps[nestedName] = buildOutputPropertySchema(nestedProp)
		}
		schema["properties"] = nestedProps
	}

	return schema
}
