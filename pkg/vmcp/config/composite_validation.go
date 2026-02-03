// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package config provides configuration types and validation for VirtualMCP.
package config

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"github.com/xeipuuv/gojsonschema"

	thvjson "github.com/stacklok/toolhive/pkg/json"
	"github.com/stacklok/toolhive/pkg/templates"
)

// Constants for workflow step types
const (
	WorkflowStepTypeToolCall    = "tool"
	WorkflowStepTypeElicitation = "elicitation"
)

// Constants for error actions
const (
	ErrorActionAbort    = "abort"
	ErrorActionContinue = "continue"
	ErrorActionRetry    = "retry"
)

// Constants for elicitation response actions
const (
	ElicitationResponseActionAbort         = "abort"
	ElicitationResponseActionContinue      = "continue"
	ElicitationResponseActionSkipRemaining = "skip_remaining"
)

// ValidateCompositeToolConfig validates a CompositeToolConfig.
// This is the primary entry point for composite tool validation, used by both
// webhooks (VirtualMCPServer, VirtualMCPCompositeToolDefinition) and runtime validation.
func ValidateCompositeToolConfig(pathPrefix string, tool *CompositeToolConfig) error {
	var errors []string

	// Validate required fields
	if tool.Name == "" {
		errors = append(errors, fmt.Sprintf("%s.name is required", pathPrefix))
	}
	if tool.Description == "" {
		errors = append(errors, fmt.Sprintf("%s.description is required", pathPrefix))
	}
	if len(tool.Steps) == 0 {
		errors = append(errors, fmt.Sprintf("%s.steps must have at least one step", pathPrefix))
	}

	// Timeout validation: Duration handles parsing, but check for negative
	if tool.Timeout < 0 {
		errors = append(errors, fmt.Sprintf("%s.timeout cannot be negative", pathPrefix))
	}

	// Validate parameters if present
	if err := ValidateParameters(pathPrefix, tool.Parameters); err != nil {
		errors = append(errors, err.Error())
	}

	// Validate steps
	if len(tool.Steps) > 0 {
		if err := ValidateWorkflowSteps(pathPrefix+".steps", tool.Steps); err != nil {
			errors = append(errors, err.Error())
		}

		// Validate defaultResults for skippable steps
		if err := ValidateDefaultResultsForSteps(pathPrefix+".steps", tool.Steps, tool.Output); err != nil {
			errors = append(errors, err.Error())
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("validation failed: %s", strings.Join(errors, "; "))
	}

	return nil
}

// ValidateParameters validates the parameter schema (JSON Schema format).
func ValidateParameters(pathPrefix string, params thvjson.Map) error {
	if params.IsEmpty() {
		return nil
	}

	paramsMap, err := params.ToMap()
	if err != nil {
		return fmt.Errorf("%s.parameters: invalid JSON: %w", pathPrefix, err)
	}

	// Validate type field
	typeVal, hasType := paramsMap["type"]
	if !hasType {
		return fmt.Errorf("%s.parameters: must have 'type' field (should be 'object' for JSON Schema)", pathPrefix)
	}

	typeStr, ok := typeVal.(string)
	if !ok {
		return fmt.Errorf("%s.parameters: 'type' field must be a string", pathPrefix)
	}

	if typeStr != "object" {
		return fmt.Errorf("%s.parameters: 'type' must be 'object' (got '%s')", pathPrefix, typeStr)
	}

	// Validate using JSON Schema validator
	schemaBytes, err := params.MarshalJSON()
	if err != nil {
		return fmt.Errorf("%s.parameters: failed to marshal: %w", pathPrefix, err)
	}
	if err := ValidateJSONSchema(schemaBytes); err != nil {
		return fmt.Errorf("%s.parameters: invalid JSON Schema: %w", pathPrefix, err)
	}

	return nil
}

// ValidateWorkflowSteps validates all workflow steps.
func ValidateWorkflowSteps(pathPrefix string, steps []WorkflowStepConfig) error {
	stepIDs := make(map[string]bool)
	stepIndices := make(map[string]int)

	// First pass: collect step IDs
	for i, step := range steps {
		if step.ID == "" {
			return fmt.Errorf("%s[%d].id is required", pathPrefix, i)
		}
		if stepIDs[step.ID] {
			return fmt.Errorf("%s[%d].id %q is duplicated", pathPrefix, i, step.ID)
		}
		stepIDs[step.ID] = true
		stepIndices[step.ID] = i
	}

	// Second pass: validate each step
	for i := range steps {
		if err := ValidateWorkflowStep(pathPrefix, i, &steps[i], stepIDs); err != nil {
			return err
		}
	}

	// Third pass: validate no dependency cycles
	return ValidateDependencyCycles(pathPrefix, steps)
}

// ValidateWorkflowStep validates a single workflow step.
func ValidateWorkflowStep(pathPrefix string, index int, step *WorkflowStepConfig, stepIDs map[string]bool) error {
	// Validate step type
	if err := ValidateStepType(pathPrefix, index, step); err != nil {
		return err
	}

	// Validate templates
	if err := ValidateStepTemplates(pathPrefix, index, step); err != nil {
		return err
	}

	// Validate dependencies
	if err := ValidateStepDependencies(pathPrefix, index, step, stepIDs); err != nil {
		return err
	}

	// Validate error handling
	if step.OnError != nil {
		if err := ValidateStepErrorHandling(pathPrefix, index, step.OnError); err != nil {
			return err
		}
	}

	// Validate elicitation response handlers
	stepType := step.Type
	if stepType == "" {
		stepType = WorkflowStepTypeToolCall
	}
	if stepType == WorkflowStepTypeElicitation {
		if step.OnDecline != nil {
			if err := ValidateElicitationResponseHandler(pathPrefix, index, "onDecline", step.OnDecline); err != nil {
				return err
			}
		}
		if step.OnCancel != nil {
			if err := ValidateElicitationResponseHandler(pathPrefix, index, "onCancel", step.OnCancel); err != nil {
				return err
			}
		}
	}

	return nil
}

// ValidateStepType validates step type and type-specific required fields.
func ValidateStepType(pathPrefix string, index int, step *WorkflowStepConfig) error {
	// Check for ambiguous configuration: both tool and message fields present without explicit type
	if step.Type == "" && step.Tool != "" && step.Message != "" {
		return fmt.Errorf(
			"%s[%d] cannot have both tool and message fields - use explicit type to clarify intent",
			pathPrefix, index)
	}

	stepType := step.Type
	if stepType == "" {
		stepType = WorkflowStepTypeToolCall // default
	}

	validTypes := map[string]bool{
		WorkflowStepTypeToolCall:    true,
		WorkflowStepTypeElicitation: true,
	}
	if !validTypes[stepType] {
		return fmt.Errorf("%s[%d].type must be one of: tool, elicitation", pathPrefix, index)
	}

	if stepType == WorkflowStepTypeToolCall {
		if step.Tool == "" {
			return fmt.Errorf("%s[%d].tool is required when type is tool", pathPrefix, index)
		}
		if !IsValidToolReference(step.Tool) {
			return fmt.Errorf("%s[%d].tool must be a valid tool name", pathPrefix, index)
		}
	}

	if stepType == WorkflowStepTypeElicitation && step.Message == "" {
		return fmt.Errorf("%s[%d].message is required when type is elicitation", pathPrefix, index)
	}

	return nil
}

// ValidateStepTemplates validates all template fields in a step.
func ValidateStepTemplates(pathPrefix string, index int, step *WorkflowStepConfig) error {
	// Validate arguments
	if !step.Arguments.IsEmpty() {
		args, err := step.Arguments.ToMap()
		if err != nil {
			return fmt.Errorf("%s[%d].arguments: invalid JSON: %w", pathPrefix, index, err)
		}
		for argName, argValue := range args {
			if strValue, ok := argValue.(string); ok {
				if err := ValidateTemplate(strValue); err != nil {
					return fmt.Errorf("%s[%d].arguments[%s]: invalid template: %w", pathPrefix, index, argName, err)
				}
			}
		}
	}

	// Validate condition
	if step.Condition != "" {
		if err := ValidateTemplate(step.Condition); err != nil {
			return fmt.Errorf("%s[%d].condition: invalid template: %w", pathPrefix, index, err)
		}
	}

	// Validate message
	if step.Message != "" {
		if err := ValidateTemplate(step.Message); err != nil {
			return fmt.Errorf("%s[%d].message: invalid template: %w", pathPrefix, index, err)
		}
	}

	// Validate JSON Schema for elicitation steps
	if !step.Schema.IsEmpty() {
		schemaBytes, err := step.Schema.MarshalJSON()
		if err != nil {
			return fmt.Errorf("%s[%d].schema: failed to marshal: %w", pathPrefix, index, err)
		}
		if err := ValidateJSONSchema(schemaBytes); err != nil {
			return fmt.Errorf("%s[%d].schema: invalid JSON Schema: %w", pathPrefix, index, err)
		}
	}

	return nil
}

// ValidateStepDependencies validates step dependencies reference existing steps.
func ValidateStepDependencies(pathPrefix string, index int, step *WorkflowStepConfig, stepIDs map[string]bool) error {
	for _, depID := range step.DependsOn {
		if !stepIDs[depID] {
			return fmt.Errorf("%s[%d].dependsOn references unknown step %q", pathPrefix, index, depID)
		}
	}
	return nil
}

// ValidateStepErrorHandling validates error handling configuration.
func ValidateStepErrorHandling(pathPrefix string, index int, onError *StepErrorHandling) error {
	if onError.Action == "" {
		return nil // Action is optional, defaults to abort
	}

	validActions := map[string]bool{
		ErrorActionAbort:    true,
		ErrorActionContinue: true,
		ErrorActionRetry:    true,
	}
	if !validActions[onError.Action] {
		return fmt.Errorf("%s[%d].onError.action must be one of: abort, continue, retry", pathPrefix, index)
	}

	if onError.Action == ErrorActionRetry && onError.RetryCount < 1 {
		return fmt.Errorf("%s[%d].onError.retryCount must be at least 1 when action is retry", pathPrefix, index)
	}

	return nil
}

// ValidateElicitationResponseHandler validates elicitation response handlers.
func ValidateElicitationResponseHandler(
	pathPrefix string, index int, handlerName string, handler *ElicitationResponseConfig,
) error {
	if handler.Action == "" {
		return fmt.Errorf("%s[%d].%s.action is required", pathPrefix, index, handlerName)
	}

	validActions := map[string]bool{
		ElicitationResponseActionAbort:         true,
		ElicitationResponseActionContinue:      true,
		ElicitationResponseActionSkipRemaining: true,
	}
	if !validActions[handler.Action] {
		return fmt.Errorf(
			"%s[%d].%s.action must be one of: abort, continue, skip_remaining",
			pathPrefix, index, handlerName)
	}

	return nil
}

// ValidateDependencyCycles validates that step dependencies don't create cycles.
func ValidateDependencyCycles(pathPrefix string, steps []WorkflowStepConfig) error {
	// Build adjacency list
	graph := make(map[string][]string)
	for _, step := range steps {
		graph[step.ID] = step.DependsOn
	}

	// DFS cycle detection
	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	var hasCycle func(string) bool
	hasCycle = func(stepID string) bool {
		visited[stepID] = true
		recStack[stepID] = true

		for _, depID := range graph[stepID] {
			if !visited[depID] {
				if hasCycle(depID) {
					return true
				}
			} else if recStack[depID] {
				return true
			}
		}

		recStack[stepID] = false
		return false
	}

	for stepID := range graph {
		if !visited[stepID] {
			if hasCycle(stepID) {
				return fmt.Errorf("%s: dependency cycle detected involving step %q", pathPrefix, stepID)
			}
		}
	}

	return nil
}

// stepFieldRef represents a reference to a specific field on a step's output.
type stepFieldRef struct {
	stepID string
	field  string
}

// ValidateDefaultResultsForSteps validates that defaultResults is specified for steps that:
// 1. May be skipped (have a condition or onError.action == "continue")
// 2. Are referenced by downstream steps
//
// nolint:gocyclo // multiple passes of the workflow are required to validate references are safe.
func ValidateDefaultResultsForSteps(pathPrefix string, steps []WorkflowStepConfig, output *OutputConfig) error {
	// 1. Compute all skippable step IDs
	skippableStepIDs := make(map[string]struct{})
	for _, step := range steps {
		if stepMayBeSkipped(step) {
			skippableStepIDs[step.ID] = struct{}{}
		}
	}

	if len(skippableStepIDs) == 0 {
		return nil
	}

	// 2. Compute map from skippable step ID to set of fields with default values
	skippableStepDefaults := make(map[string]map[string]struct{})
	for _, step := range steps {
		if _, ok := skippableStepIDs[step.ID]; ok {
			skippableStepDefaults[step.ID] = make(map[string]struct{})
			if !step.DefaultResults.IsEmpty() {
				defaultsMap, err := step.DefaultResults.ToMap()
				if err == nil {
					for key := range defaultsMap {
						skippableStepDefaults[step.ID][key] = struct{}{}
					}
				}
			}
		}
	}

	// 3. Check references in steps
	for _, step := range steps {
		refs, err := extractStepFieldRefsFromStep(step)
		if err != nil {
			return fmt.Errorf("failed to extract step references from step %s: %w", step.ID, err)
		}

		for _, ref := range refs {
			defaultFields, isSkippable := skippableStepDefaults[ref.stepID]
			if !isSkippable {
				continue
			}
			if _, hasDefault := defaultFields[ref.field]; !hasDefault {
				return fmt.Errorf(
					"%s[%s].defaultResults[%s] is required: step %q may be skipped and field %q is referenced by step %s",
					pathPrefix, ref.stepID, ref.field, ref.stepID, ref.field, step.ID)
			}
		}
	}

	// 4. Check references in output
	if output != nil {
		outputRefs, err := extractStepFieldRefsFromOutput(output)
		if err != nil {
			return fmt.Errorf("failed to extract step references from output: %w", err)
		}

		for _, ref := range outputRefs {
			defaultFields, isSkippable := skippableStepDefaults[ref.stepID]
			if !isSkippable {
				continue
			}
			if _, hasDefault := defaultFields[ref.field]; !hasDefault {
				return fmt.Errorf(
					"%s[%s].defaultResults[%s] is required: step %q may be skipped and field %q is referenced by output",
					pathPrefix, ref.stepID, ref.field, ref.stepID, ref.field)
			}
		}
	}

	return nil
}

// stepMayBeSkipped returns true if a step may be skipped during execution.
func stepMayBeSkipped(step WorkflowStepConfig) bool {
	if step.Condition != "" {
		return true
	}
	if step.OnError != nil && step.OnError.Action == ErrorActionContinue {
		return true
	}
	return false
}

// extractStepFieldRefsFromStep extracts step field references from a step's templates.
func extractStepFieldRefsFromStep(step WorkflowStepConfig) ([]stepFieldRef, error) {
	var allRefs []stepFieldRef

	if step.Condition != "" {
		refs, err := extractStepFieldRefsFromTemplate(step.Condition)
		if err != nil {
			return nil, err
		}
		allRefs = append(allRefs, refs...)
	}

	if !step.Arguments.IsEmpty() {
		args, err := step.Arguments.ToMap()
		if err == nil {
			for _, argValue := range args {
				if strValue, ok := argValue.(string); ok {
					refs, err := extractStepFieldRefsFromTemplate(strValue)
					if err != nil {
						return nil, err
					}
					allRefs = append(allRefs, refs...)
				}
			}
		}
	}

	if step.Message != "" {
		refs, err := extractStepFieldRefsFromTemplate(step.Message)
		if err != nil {
			return nil, err
		}
		allRefs = append(allRefs, refs...)
	}

	return uniqueStepFieldRefs(allRefs), nil
}

// extractStepFieldRefsFromOutput extracts step field references from output templates.
func extractStepFieldRefsFromOutput(output *OutputConfig) ([]stepFieldRef, error) {
	if output == nil {
		return nil, nil
	}

	var allRefs []stepFieldRef

	for _, prop := range output.Properties {
		if prop.Value != "" {
			refs, err := extractStepFieldRefsFromTemplate(prop.Value)
			if err != nil {
				return nil, err
			}
			allRefs = append(allRefs, refs...)
		}

		if len(prop.Properties) > 0 {
			nestedOutput := &OutputConfig{Properties: prop.Properties}
			nestedRefs, err := extractStepFieldRefsFromOutput(nestedOutput)
			if err != nil {
				return nil, err
			}
			allRefs = append(allRefs, nestedRefs...)
		}
	}

	return uniqueStepFieldRefs(allRefs), nil
}

// extractStepFieldRefsFromTemplate extracts step output field references from a template string.
func extractStepFieldRefsFromTemplate(tmplStr string) ([]stepFieldRef, error) {
	refs, err := templates.ExtractReferences(tmplStr)
	if err != nil {
		return nil, err
	}

	var stepRefs []stepFieldRef
	for _, ref := range refs {
		if strings.HasPrefix(ref, ".steps.") {
			parts := strings.SplitN(ref, ".", 6)
			if len(parts) >= 5 && parts[3] == "output" {
				stepRefs = append(stepRefs, stepFieldRef{
					stepID: parts[2],
					field:  parts[4],
				})
			}
		}
	}

	return uniqueStepFieldRefs(stepRefs), nil
}

// uniqueStepFieldRefs returns a deduplicated slice of stepFieldRefs.
func uniqueStepFieldRefs(refs []stepFieldRef) []stepFieldRef {
	seen := make(map[stepFieldRef]struct{})
	result := make([]stepFieldRef, 0, len(refs))
	for _, r := range refs {
		if _, ok := seen[r]; !ok {
			seen[r] = struct{}{}
			result = append(result, r)
		}
	}
	return result
}

// ValidateTemplate validates Go template syntax including custom functions.
// It uses the same FuncMap as the runtime template expander to ensure
// templates using json, quote, or fromJson are validated correctly.
func ValidateTemplate(tmpl string) error {
	_, err := template.New("validation").Funcs(templates.FuncMap()).Parse(tmpl)
	if err != nil {
		return fmt.Errorf("invalid template syntax: %w", err)
	}
	return nil
}

// ValidateJSONSchema validates that bytes contain a valid JSON Schema.
func ValidateJSONSchema(schemaBytes []byte) error {
	if len(schemaBytes) == 0 {
		return nil
	}

	var schemaDoc interface{}
	if err := json.Unmarshal(schemaBytes, &schemaDoc); err != nil {
		return fmt.Errorf("failed to parse JSON: %w", err)
	}

	schemaLoader := gojsonschema.NewBytesLoader(schemaBytes)
	documentLoader := gojsonschema.NewStringLoader("{}")

	_, err := gojsonschema.Validate(schemaLoader, documentLoader)
	if err != nil {
		return fmt.Errorf("invalid JSON Schema: %w", err)
	}

	return nil
}

// IsValidToolReference validates tool reference format.
// Accepts multiple formats:
//   - "workload.tool_name" (semantic format specifying which backend's tool)
//   - "workload_toolname" (aggregated format used with prefix conflict resolution)
//   - "toolname" (simple format when there's no ambiguity)
func IsValidToolReference(tool string) bool {
	if tool == "" {
		return false
	}
	// Accept any reasonable tool name format: alphanumeric with dots, underscores, and hyphens
	pattern := `^[a-zA-Z0-9][a-zA-Z0-9._-]*$`
	matched, _ := regexp.MatchString(pattern, tool)
	return matched
}
