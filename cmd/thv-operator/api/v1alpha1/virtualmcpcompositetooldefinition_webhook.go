package v1alpha1

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"text/template"

	"github.com/xeipuuv/gojsonschema"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SetupWebhookWithManager registers the webhook with the manager
func (r *VirtualMCPCompositeToolDefinition) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

//nolint:lll // kubebuilder webhook marker cannot be split
// +kubebuilder:webhook:path=/validate-toolhive-stacklok-dev-v1alpha1-virtualmcpcompositetooldefinition,mutating=false,failurePolicy=fail,sideEffects=None,groups=toolhive.stacklok.dev,resources=virtualmcpcompositetooldefinitions,verbs=create;update,versions=v1alpha1,name=vvirtualmcpcompositetooldefinition.kb.io,admissionReviewVersions=v1

var _ webhook.CustomValidator = &VirtualMCPCompositeToolDefinition{}

// ValidateCreate implements webhook.CustomValidator
func (r *VirtualMCPCompositeToolDefinition) ValidateCreate(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, r.Validate()
}

// ValidateUpdate implements webhook.CustomValidator
//
//nolint:lll // function signature cannot be shortened
func (r *VirtualMCPCompositeToolDefinition) ValidateUpdate(_ context.Context, _ runtime.Object, _ runtime.Object) (admission.Warnings, error) {
	return nil, r.Validate()
}

// ValidateDelete implements webhook.CustomValidator
func (*VirtualMCPCompositeToolDefinition) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	// No validation needed on delete
	return nil, nil
}

// Validate performs validation for VirtualMCPCompositeToolDefinition
// This method can be called by the controller during reconciliation or by the webhook
func (r *VirtualMCPCompositeToolDefinition) Validate() error {
	var errors []string

	// Validate spec.name is set
	if r.Spec.Name == "" {
		errors = append(errors, "spec.name is required")
	}

	// Validate spec.description is set
	if r.Spec.Description == "" {
		errors = append(errors, "spec.description is required")
	}

	// Validate steps
	if len(r.Spec.Steps) == 0 {
		errors = append(errors, "spec.steps must have at least one step")
	}

	// Validate parameter schema
	if err := r.validateParameters(); err != nil {
		errors = append(errors, err.Error())
	}

	// Validate workflow steps
	if err := r.validateSteps(); err != nil {
		errors = append(errors, err.Error())
	}

	// Validate timeout format
	if r.Spec.Timeout != "" {
		if err := validateDuration(r.Spec.Timeout); err != nil {
			errors = append(errors, fmt.Sprintf("spec.timeout: %v", err))
		}
	}

	// Validate failure mode
	if r.Spec.FailureMode != "" {
		validModes := map[string]bool{
			ErrorActionAbort:    true,
			ErrorActionContinue: true,
		}
		if !validModes[r.Spec.FailureMode] {
			errors = append(errors, "spec.failureMode must be one of: abort, continue")
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("validation failed: %v", errors)
	}

	return nil
}

// validateParameters validates the parameter schema using JSON Schema validation
func (r *VirtualMCPCompositeToolDefinition) validateParameters() error {
	if r.Spec.Parameters == nil || len(r.Spec.Parameters.Raw) == 0 {
		return nil // No parameters to validate
	}

	// Parameters should be a JSON Schema object in RawExtension format
	// Unmarshal to validate structure
	var params map[string]interface{}
	if err := json.Unmarshal(r.Spec.Parameters.Raw, &params); err != nil {
		return fmt.Errorf("spec.parameters: invalid JSON: %w", err)
	}

	// Validate that it has "type" field
	typeVal, hasType := params["type"]
	if !hasType {
		return fmt.Errorf("spec.parameters: must have 'type' field (should be 'object' for JSON Schema)")
	}

	// Type must be a string
	typeStr, ok := typeVal.(string)
	if !ok {
		return fmt.Errorf("spec.parameters: 'type' field must be a string")
	}

	// Type should be "object" for parameter schemas
	if typeStr != "object" {
		return fmt.Errorf("spec.parameters: 'type' must be 'object' (got '%s')", typeStr)
	}

	// Validate using JSON Schema validator
	if err := validateJSONSchema(r.Spec.Parameters.Raw); err != nil {
		return fmt.Errorf("spec.parameters: invalid JSON Schema: %w", err)
	}

	return nil
}

// validateSteps validates all workflow steps
func (r *VirtualMCPCompositeToolDefinition) validateSteps() error {
	stepIDs := make(map[string]bool)
	stepIndices := make(map[string]int)

	// First pass: collect all step IDs
	for i, step := range r.Spec.Steps {
		if step.ID == "" {
			return fmt.Errorf("spec.steps[%d].id is required", i)
		}

		// Check for duplicate step IDs
		if stepIDs[step.ID] {
			return fmt.Errorf("spec.steps[%d].id %q is duplicated", i, step.ID)
		}
		stepIDs[step.ID] = true
		stepIndices[step.ID] = i
	}

	// Second pass: validate each step
	for i, step := range r.Spec.Steps {
		if err := r.validateStep(i, step, stepIDs); err != nil {
			return err
		}
	}

	// Third pass: validate dependencies don't create cycles
	if err := r.validateDependencyCycles(); err != nil {
		return err
	}

	// Fourth pass: validate defaultResults for skippable steps
	return validateDefaultResultsForSteps("spec.steps", r.Spec.Steps, r.Spec.Output)
}

// validateStep validates a single workflow step
func (r *VirtualMCPCompositeToolDefinition) validateStep(index int, step WorkflowStep, stepIDs map[string]bool) error {
	if err := r.validateStepType(index, step); err != nil {
		return err
	}

	if err := r.validateStepTemplates(index, step); err != nil {
		return err
	}

	if err := r.validateStepDependencies(index, step, stepIDs); err != nil {
		return err
	}

	if step.OnError != nil {
		if err := r.validateErrorHandling(index, step.OnError); err != nil {
			return err
		}
	}

	if step.Timeout != "" {
		if err := validateDuration(step.Timeout); err != nil {
			return fmt.Errorf("spec.steps[%d].timeout: %w", index, err)
		}
	}

	return nil
}

// validateStepType validates step type and type-specific required fields
func (*VirtualMCPCompositeToolDefinition) validateStepType(index int, step WorkflowStep) error {
	stepType := step.Type
	if stepType == "" {
		stepType = WorkflowStepTypeToolCall // default
	}

	validTypes := map[string]bool{
		WorkflowStepTypeToolCall:    true,
		WorkflowStepTypeElicitation: true,
	}
	if !validTypes[stepType] {
		return fmt.Errorf("spec.steps[%d].type must be one of: tool, elicitation", index)
	}

	if stepType == WorkflowStepTypeToolCall {
		if step.Tool == "" {
			return fmt.Errorf("spec.steps[%d].tool is required when type is tool", index)
		}
		if !isValidToolReference(step.Tool) {
			return fmt.Errorf("spec.steps[%d].tool must be in format 'workload.tool_name'", index)
		}
	}

	if stepType == WorkflowStepTypeElicitation && step.Message == "" {
		return fmt.Errorf("spec.steps[%d].message is required when type is elicitation", index)
	}

	return nil
}

// validateStepTemplates validates all template fields in a step
func (*VirtualMCPCompositeToolDefinition) validateStepTemplates(index int, step WorkflowStep) error {
	return validateWorkflowStepTemplates("spec.steps", index, step)
}

// validateWorkflowStepTemplates validates all template fields in a workflow step.
// This is a shared validation function used by both VirtualMCPServer and VirtualMCPCompositeToolDefinition webhooks.
// The pathPrefix parameter allows customizing error message paths (e.g., "spec.steps" or "spec.compositeTools[0].steps").
func validateWorkflowStepTemplates(pathPrefix string, index int, step WorkflowStep) error {
	// Validate template syntax in arguments (only for string values)
	if step.Arguments != nil && len(step.Arguments.Raw) > 0 {
		var args map[string]any
		if err := json.Unmarshal(step.Arguments.Raw, &args); err != nil {
			return fmt.Errorf("%s[%d].arguments: invalid JSON: %w", pathPrefix, index, err)
		}
		for argName, argValue := range args {
			// Only validate template syntax for string values
			if strValue, ok := argValue.(string); ok {
				if err := validateTemplate(strValue); err != nil {
					return fmt.Errorf("%s[%d].arguments[%s]: invalid template: %w", pathPrefix, index, argName, err)
				}
			}
		}
	}

	if step.Condition != "" {
		if err := validateTemplate(step.Condition); err != nil {
			return fmt.Errorf("%s[%d].condition: invalid template: %w", pathPrefix, index, err)
		}
	}

	if step.Message != "" {
		if err := validateTemplate(step.Message); err != nil {
			return fmt.Errorf("%s[%d].message: invalid template: %w", pathPrefix, index, err)
		}
	}

	// Validate JSON Schema for elicitation steps
	if step.Schema != nil {
		if err := validateJSONSchema(step.Schema.Raw); err != nil {
			return fmt.Errorf("%s[%d].schema: invalid JSON Schema: %w", pathPrefix, index, err)
		}
	}

	return nil
}

// validateStepDependencies validates step dependencies reference existing steps
func (*VirtualMCPCompositeToolDefinition) validateStepDependencies(index int, step WorkflowStep, stepIDs map[string]bool) error {
	for _, depID := range step.DependsOn {
		if !stepIDs[depID] {
			return fmt.Errorf("spec.steps[%d].dependsOn references unknown step %q", index, depID)
		}
	}
	return nil
}

// validateErrorHandling validates error handling configuration
func (*VirtualMCPCompositeToolDefinition) validateErrorHandling(stepIndex int, errorHandling *ErrorHandling) error {
	if errorHandling.Action == "" {
		return nil // Action is optional, defaults to ErrorActionAbort
	}

	validActions := map[string]bool{
		ErrorActionAbort:    true,
		ErrorActionContinue: true,
		ErrorActionRetry:    true,
	}
	if !validActions[errorHandling.Action] {
		return fmt.Errorf("spec.steps[%d].onError.action must be one of: abort, continue, retry", stepIndex)
	}

	if errorHandling.Action == ErrorActionRetry {
		if errorHandling.MaxRetries < 1 {
			return fmt.Errorf("spec.steps[%d].onError.maxRetries must be at least 1 when action is retry", stepIndex)
		}
	}

	return nil
}

// validateDependencyCycles validates that step dependencies don't create cycles
func (r *VirtualMCPCompositeToolDefinition) validateDependencyCycles() error {
	// Build adjacency list for dependency graph
	graph := make(map[string][]string)
	for _, step := range r.Spec.Steps {
		graph[step.ID] = step.DependsOn
	}

	// Check for cycles using DFS
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
				return fmt.Errorf("spec.steps: dependency cycle detected involving step %q", stepID)
			}
		}
	}

	return nil
}

// validateTemplate validates Go template syntax
func validateTemplate(tmpl string) error {
	// Try to parse as Go template
	_, err := template.New("validation").Parse(tmpl)
	if err != nil {
		return fmt.Errorf("invalid template syntax: %w", err)
	}
	return nil
}

// validateJSONSchema validates that the provided bytes contain a valid JSON Schema
func validateJSONSchema(schemaBytes []byte) error {
	if len(schemaBytes) == 0 {
		return nil // Empty schema is allowed
	}

	// Parse the schema JSON to verify it's valid JSON
	var schemaDoc interface{}
	if err := json.Unmarshal(schemaBytes, &schemaDoc); err != nil {
		return fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Use gojsonschema to validate the schema by attempting to use it as a schema
	// We validate an empty object against the schema - if the schema itself is invalid,
	// gojsonschema will return an error during schema loading
	schemaLoader := gojsonschema.NewBytesLoader(schemaBytes)
	documentLoader := gojsonschema.NewStringLoader("{}")

	// If this succeeds, the schema is valid JSON Schema
	// If it fails during schema loading, the schema is invalid
	_, err := gojsonschema.Validate(schemaLoader, documentLoader)
	if err != nil {
		// Check if error is about the schema itself (not validation failure)
		return fmt.Errorf("invalid JSON Schema: %w", err)
	}

	return nil
}

// validateDuration validates duration format (e.g., "30s", "5m", "1h")
func validateDuration(duration string) error {
	// Pattern: one or more segments of number + unit (ms, s, m, h)
	pattern := `^([0-9]+(\.[0-9]+)?(ms|s|m|h))+$`
	matched, err := regexp.MatchString(pattern, duration)
	if err != nil {
		return err
	}
	if !matched {
		return fmt.Errorf("invalid duration format %q, expected format like '30s', '5m', '1h', '1h30m'", duration)
	}
	return nil
}

// isValidToolReference validates tool reference format (workload.tool_name)
func isValidToolReference(tool string) bool {
	// Tool reference must be in format: workload.tool_name
	// Both parts must be non-empty
	pattern := `^[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+$`
	matched, _ := regexp.MatchString(pattern, tool)
	return matched
}

// GetValidationErrors returns a list of validation errors
// This is a helper method for the controller to populate status.validationErrors
func (r *VirtualMCPCompositeToolDefinition) GetValidationErrors() []string {
	var errors []string

	if r.Spec.Name == "" {
		errors = append(errors, "spec.name is required")
	}

	if r.Spec.Description == "" {
		errors = append(errors, "spec.description is required")
	}

	if len(r.Spec.Steps) == 0 {
		errors = append(errors, "spec.steps must have at least one step")
	} else {
		// Validate steps and collect errors
		if err := r.validateSteps(); err != nil {
			errors = append(errors, err.Error())
		}
	}

	if err := r.validateParameters(); err != nil {
		errors = append(errors, err.Error())
	}

	if r.Spec.Timeout != "" {
		if err := validateDuration(r.Spec.Timeout); err != nil {
			errors = append(errors, fmt.Sprintf("spec.timeout: %v", err))
		}
	}

	return errors
}
