// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package compositetools

import (
	"fmt"
	"strings"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// FilterWorkflowDefsForSession returns only the workflow definitions whose every
// tool step references a backend tool that is present in the session routing table.
//
// If a session does not have access to a backend tool (e.g. due to identity-based
// filtering), any composite tool that depends on that backend tool is also excluded.
// This prevents a session from invoking a composite tool that would fail at runtime
// because one or more of its underlying tools are not routable for that session.
func FilterWorkflowDefsForSession(
	defs map[string]*composer.WorkflowDefinition,
	rt *vmcp.RoutingTable,
) map[string]*composer.WorkflowDefinition {
	if len(defs) == 0 {
		return defs
	}

	filtered := make(map[string]*composer.WorkflowDefinition, len(defs))
	for name, def := range defs {
		if allToolStepsAccessible(def, rt) {
			filtered[name] = def
		}
	}
	return filtered
}

// allToolStepsAccessible reports whether every tool step in the workflow
// references a backend tool that is present in the session routing table.
// Returns false if rt is nil and the workflow contains any tool steps,
// since a nil routing table means no tools are routable in this session.
//
// Composite tool step names use the convention "{workloadID}.{toolName}" where
// workloadID is a Kubernetes resource name (no dots). The routing table may store
// tools under resolved/prefixed names (e.g. "{workloadID}_echo" with prefix strategy),
// so we look up by BackendTarget.WorkloadID rather than the resolved key directly.
func allToolStepsAccessible(def *composer.WorkflowDefinition, rt *vmcp.RoutingTable) bool {
	for _, step := range def.Steps {
		if step.Type == composer.StepTypeTool {
			if rt == nil {
				return false
			}
			if !isToolStepAccessible(step.Tool, rt) {
				return false
			}
		}
		// For forEach steps, check the inner step's tool accessibility
		if step.Type == composer.StepTypeForEach && step.InnerStep != nil {
			if step.InnerStep.Type == composer.StepTypeTool {
				if rt == nil {
					return false
				}
				if !isToolStepAccessible(step.InnerStep.Tool, rt) {
					return false
				}
			}
		}
	}
	return true
}

// isToolStepAccessible reports whether a composite tool step's tool name can be
// resolved to an accessible backend tool in the given routing table.
//
// Step tool names use the "{workloadID}.{toolName}" convention. Since conflict
// resolution strategies (e.g. prefix) may rename tools in the routing table
// (e.g. "echo" → "yardstick-backend_echo"), we check for accessibility by
// matching on WorkloadID and the original backend capability name rather than
// the resolved routing table key.
func isToolStepAccessible(stepTool string, rt *vmcp.RoutingTable) bool {
	// Fast path: exact match in the routing table.
	if _, ok := rt.Tools[stepTool]; ok {
		return true
	}

	// Parse "{workloadID}.{toolName}" convention.
	// Workload IDs are Kubernetes resource names and cannot contain dots,
	// so the first dot separates the workload ID from the tool name.
	dotIdx := strings.Index(stepTool, ".")
	if dotIdx <= 0 {
		return false
	}
	workloadID := stepTool[:dotIdx]
	originalName := stepTool[dotIdx+1:]

	for resolvedName, target := range rt.Tools {
		if target.WorkloadID != workloadID {
			continue
		}
		if target.GetBackendCapabilityName(resolvedName) == originalName {
			return true
		}
	}
	return false
}

// ConvertWorkflowDefsToTools converts workflow definitions to vmcp.Tool format.
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
func ConvertWorkflowDefsToTools(defs map[string]*composer.WorkflowDefinition) []vmcp.Tool {
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

// ValidateNoToolConflicts validates that composite tool names don't conflict with backend tool names.
//
// Tool name conflicts would cause ambiguity in routing/execution:
//   - Which tool should be invoked when a client calls the name?
//   - Should it route to the backend or execute the workflow?
//
// This validation ensures clear separation and prevents runtime confusion.
// Returns an error listing all conflicting tool names if any conflicts are found.
func ValidateNoToolConflicts(backendTools, compositeTools []vmcp.Tool) error {
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
