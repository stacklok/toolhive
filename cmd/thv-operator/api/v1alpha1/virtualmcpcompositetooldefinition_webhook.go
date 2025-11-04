package v1alpha1

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"github.com/santhosh-tekuri/jsonschema/v6"
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
			"abort":       true,
			"continue":    true,
			"best_effort": true,
		}
		if !validModes[r.Spec.FailureMode] {
			errors = append(errors, "spec.failureMode must be one of: abort, continue, best_effort")
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("validation failed: %v", errors)
	}

	return nil
}

// validateParameters validates the parameter schema using JSON Schema validation
func (r *VirtualMCPCompositeToolDefinition) validateParameters() error {
	if len(r.Spec.Parameters) == 0 {
		return nil // No parameters to validate
	}

	// Build a JSON Schema object from the parameters
	// Parameters map to a JSON Schema "properties" object
	properties := make(map[string]interface{})
	var required []string

	for paramName, param := range r.Spec.Parameters {
		if param.Type == "" {
			return fmt.Errorf("spec.parameters[%s].type is required", paramName)
		}

		// Build a JSON Schema property definition
		property := map[string]interface{}{
			"type": param.Type,
		}

		if param.Description != "" {
			property["description"] = param.Description
		}

		if param.Default != "" {
			// Parse default value based on type
			property["default"] = param.Default
		}

		if param.Required {
			required = append(required, paramName)
		}

		properties[paramName] = property
	}

	// Construct a full JSON Schema document
	schemaDoc := map[string]interface{}{
		"type":       "object",
		"properties": properties,
	}

	if len(required) > 0 {
		schemaDoc["required"] = required
	}

	// Marshal to JSON
	schemaJSON, err := json.Marshal(schemaDoc)
	if err != nil {
		return fmt.Errorf("spec.parameters: failed to marshal schema: %v", err)
	}

	// Validate using JSON Schema validator
	if err := validateJSONSchema(schemaJSON); err != nil {
		return fmt.Errorf("spec.parameters: invalid JSON Schema: %v", err)
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
	return r.validateDependencyCycles()
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
			return fmt.Errorf("spec.steps[%d].timeout: %v", index, err)
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
		return fmt.Errorf("spec.steps[%d].type must be one of: tool_call, elicitation", index)
	}

	if stepType == WorkflowStepTypeToolCall {
		if step.Tool == "" {
			return fmt.Errorf("spec.steps[%d].tool is required when type is tool_call", index)
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
	for argName, argValue := range step.Arguments {
		if err := validateTemplate(argValue); err != nil {
			return fmt.Errorf("spec.steps[%d].arguments[%s]: invalid template: %v", index, argName, err)
		}
	}

	if step.Condition != "" {
		if err := validateTemplate(step.Condition); err != nil {
			return fmt.Errorf("spec.steps[%d].condition: invalid template: %v", index, err)
		}
	}

	if step.Message != "" {
		if err := validateTemplate(step.Message); err != nil {
			return fmt.Errorf("spec.steps[%d].message: invalid template: %v", index, err)
		}
	}

	// Validate JSON Schema for elicitation steps
	if step.Schema != nil {
		if err := validateJSONSchema(step.Schema.Raw); err != nil {
			return fmt.Errorf("spec.steps[%d].schema: invalid JSON Schema: %v", index, err)
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
		return nil // Action is optional, defaults to "abort"
	}

	validActions := map[string]bool{
		"abort":    true,
		"continue": true,
		"retry":    true,
	}
	if !validActions[errorHandling.Action] {
		return fmt.Errorf("spec.steps[%d].onError.action must be one of: abort, continue, retry", stepIndex)
	}

	if errorHandling.Action == "retry" {
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
		return fmt.Errorf("invalid template syntax: %v", err)
	}
	return nil
}

// validateJSONSchema validates that the provided bytes contain a valid JSON Schema
func validateJSONSchema(schemaBytes []byte) error {
	if len(schemaBytes) == 0 {
		return nil // Empty schema is allowed
	}

	// Parse the schema JSON
	var schemaDoc interface{}
	if err := json.Unmarshal(schemaBytes, &schemaDoc); err != nil {
		return fmt.Errorf("failed to parse JSON: %v", err)
	}

	// Compile the schema to validate it's a valid JSON Schema
	compiler := jsonschema.NewCompiler()
	schemaID := "schema://validation"
	if err := compiler.AddResource(schemaID, schemaDoc); err != nil {
		return formatJSONSchemaError(err)
	}

	if _, err := compiler.Compile(schemaID); err != nil {
		return formatJSONSchemaError(err)
	}

	return nil
}

// formatJSONSchemaError formats JSON Schema validation errors for better readability
func formatJSONSchemaError(err error) error {
	if validationErr, ok := err.(*jsonschema.ValidationError); ok {
		var errorMessages []string
		collectJSONSchemaErrors(validationErr, &errorMessages)
		if len(errorMessages) > 0 {
			return fmt.Errorf("%s", strings.Join(errorMessages, "; "))
		}
	}
	return err
}

// collectJSONSchemaErrors recursively collects all validation error messages
func collectJSONSchemaErrors(err *jsonschema.ValidationError, messages *[]string) {
	if err == nil {
		return
	}

	// If this error has causes, recurse into them
	if len(err.Causes) > 0 {
		for _, cause := range err.Causes {
			collectJSONSchemaErrors(cause, messages)
		}
		return
	}

	// This is a leaf error - format it
	output := err.BasicOutput()
	if output != nil && output.Error != nil {
		errorMsg := output.Error.String()
		if output.InstanceLocation != "" {
			errorMsg = fmt.Sprintf("%s at '%s'", errorMsg, output.InstanceLocation)
		}
		*messages = append(*messages, errorMsg)
	}
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
