// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package compositetools

import (
	"fmt"
	"log/slog"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
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
// resolved to an accessible backend tool in the given routing table. It is a
// thin nil-safe wrapper around the shared router.ResolveToolRef primitive so
// accessibility filtering and annotation resolution cannot drift.
func isToolStepAccessible(stepTool string, rt *vmcp.RoutingTable) bool {
	_, ok := router.ResolveToolRef(rt, stepTool)
	return ok
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
//   - Annotations: the safety floor derived from the step tools' annotations,
//     merged with the workflow's explicit annotations, if any. When a workflow
//     has at least one tool step the floor is always non-nil (fail-closed):
//     backends that declare no annotations produce a conservative floor rather
//     than no floor.
//
// stepResolver may be nil (or return nil for every step), in which case each
// tool step's annotations are treated as unknown and taint the floor
// conservatively. When a workflow's explicit annotations contradict the derived
// safety floor, the composite tool is DROPPED (not advertised) with a warning
// that names the offending step tool(s) — an explicit declaration must never
// make a tool look safer than its steps allow.
//
// Returns a slice of vmcp.Tool ready for aggregation and exposure to clients.
func ConvertWorkflowDefsToTools(
	defs map[string]*composer.WorkflowDefinition,
	stepResolver StepAnnotationResolver,
) []vmcp.Tool {
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

		// Derive the safety floor from the step tools, merge explicit
		// annotations over it, and drop the tool on a contradiction.
		if ann, ok := resolveCompositeAnnotations(def, stepResolver); ok {
			tool.Annotations = ann
		} else {
			continue
		}

		tools = append(tools, tool)
	}

	return tools
}

// FilterWorkflowDefsByAnnotations returns only the workflow definitions whose
// explicit annotations do not contradict the derived safety floor. Definitions
// that contradict are omitted (with the same warning resolveCompositeAnnotations
// already emits). This is the CallTool/ListTools shared gate — callers that
// execute composites must use this filtered set so a dropped tool is never callable.
func FilterWorkflowDefsByAnnotations(
	defs map[string]*composer.WorkflowDefinition,
	stepResolver StepAnnotationResolver,
) map[string]*composer.WorkflowDefinition {
	if len(defs) == 0 {
		return defs
	}

	filtered := make(map[string]*composer.WorkflowDefinition, len(defs))
	for name, def := range defs {
		if _, ok := resolveCompositeAnnotations(def, stepResolver); ok {
			filtered[name] = def
		}
	}
	return filtered
}

// resolveCompositeAnnotations computes the advertised annotations for a
// composite tool: the floor derived from the step tools merged with the
// workflow's explicit annotations. It returns ok=false when the explicit
// annotations contradict the floor, in which case the caller must not
// advertise the tool.
//
// stepRefs (the tool references of the steps that produced the floor) is
// threaded into the drop warning so the author can locate the offending step.
func resolveCompositeAnnotations(
	def *composer.WorkflowDefinition,
	stepResolver StepAnnotationResolver,
) (ann *vmcp.ToolAnnotations, ok bool) {
	// No resolver and no explicit annotations: nothing to derive or merge.
	// (A workflow whose backends declare no annotations but has explicit
	// annotations still runs through Derive + the contradiction guard below.)
	if stepResolver == nil && def.Annotations == nil {
		return nil, true
	}

	stepAnn, stepRefs := resolveStepAnnotations(def, stepResolver)
	floor := DeriveCompositeAnnotations(stepAnn)
	explicit := def.Annotations.ToAnnotations()
	if err := CheckAnnotationContradiction(explicit, floor); err != nil {
		slog.Warn("composite tool annotations contradict the safety floor; omitting composite tool",
			"tool", def.Name, "step_tools", stepRefs, "error", err)
		return nil, false
	}
	return MergeAnnotations(floor, explicit), true
}

// resolveStepAnnotations collects the annotations of every tool step in the
// workflow, including forEach inner steps, mirroring the traversal in
// allToolStepsAccessible. It returns the annotations and, in parallel, the
// step tool references that produced them — so a contradiction warning can name
// the offending step(s). A nil resolver (or an unknown step tool) yields a nil
// entry, which the derivation treats conservatively as "unknown". A forEach
// step with a nil InnerStep contributes nothing and never panics.
func resolveStepAnnotations(
	def *composer.WorkflowDefinition,
	stepResolver StepAnnotationResolver,
) (anns []*vmcp.ToolAnnotations, refs []string) {
	resolve := func(stepTool string) {
		var ann *vmcp.ToolAnnotations
		if stepResolver != nil {
			ann = stepResolver(stepTool)
		}
		anns = append(anns, ann)
		refs = append(refs, stepTool)
	}
	for i := range def.Steps {
		step := &def.Steps[i]
		if step.Type == composer.StepTypeTool {
			resolve(step.Tool)
		}
		// A forEach step whose InnerStep is nil is structurally invalid (caught
		// earlier by validation); guard against a nil deref here regardless.
		if step.Type == composer.StepTypeForEach && step.InnerStep != nil &&
			step.InnerStep.Type == composer.StepTypeTool {
			resolve(step.InnerStep.Tool)
		}
	}
	return anns, refs
}

// CompositeToolNames returns the names of the composite tools in defs.
// Order is undefined (map iteration). Used for name-conflict detection so
// annotation policy cannot drop a colliding name from the conflict check.
func CompositeToolNames(defs map[string]*composer.WorkflowDefinition) []string {
	if len(defs) == 0 {
		return nil
	}
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	return names
}

// ValidateNoToolConflicts validates that composite tool names don't conflict with backend tool names.
//
// Tool name conflicts would cause ambiguity in routing/execution:
//   - Which tool should be invoked when a client calls the name?
//   - Should it route to the backend or execute the workflow?
//
// This validation ensures clear separation and prevents runtime confusion.
// Returns an error listing all conflicting tool names if any conflicts are found.
//
// Prefer CompositeToolNames(defs) for the compositeNames argument so conflict
// detection never depends on annotation conversion (which can drop tools).
func ValidateNoToolConflicts(backendTools []vmcp.Tool, compositeNames []string) error {
	// Build set of backend tool names for O(1) lookups
	backendNames := make(map[string]bool, len(backendTools))
	for _, tool := range backendTools {
		backendNames[tool.Name] = true
	}

	// Check for conflicts
	var conflicts []string
	for _, name := range compositeNames {
		if backendNames[name] {
			conflicts = append(conflicts, name)
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
