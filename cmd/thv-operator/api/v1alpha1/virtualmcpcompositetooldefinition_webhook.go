package v1alpha1

import (
	"fmt"
	"regexp"
	"text/template"
)

// Validate performs validation for VirtualMCPCompositeToolDefinition
// This method can be called by the controller during reconciliation
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

// validateParameters validates the parameter schema
func (r *VirtualMCPCompositeToolDefinition) validateParameters() error {
	for paramName, param := range r.Spec.Parameters {
		if param.Type == "" {
			return fmt.Errorf("spec.parameters[%s].type is required", paramName)
		}

		// Validate parameter type
		validTypes := map[string]bool{
			"string":  true,
			"integer": true,
			"number":  true,
			"boolean": true,
			"array":   true,
			"object":  true,
		}
		if !validTypes[param.Type] {
			return fmt.Errorf("spec.parameters[%s].type must be one of: string, integer, number, boolean, array, object", paramName)
		}
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
	if err := r.validateDependencyCycles(stepIndices); err != nil {
		return err
	}

	return nil
}

// validateStep validates a single workflow step
func (r *VirtualMCPCompositeToolDefinition) validateStep(index int, step WorkflowStep, stepIDs map[string]bool) error {
	// Validate step type
	stepType := step.Type
	if stepType == "" {
		stepType = WorkflowStepTypeToolCall // default
	}

	validTypes := map[string]bool{
		WorkflowStepTypeToolCall:     true,
		WorkflowStepTypeElicitation:  true,
	}
	if !validTypes[stepType] {
		return fmt.Errorf("spec.steps[%d].type must be one of: tool_call, elicitation", index)
	}

	// Validate type-specific fields
	if stepType == WorkflowStepTypeToolCall {
		if step.Tool == "" {
			return fmt.Errorf("spec.steps[%d].tool is required when type is tool_call", index)
		}

		// Validate tool name format (workload.tool_name)
		if !isValidToolReference(step.Tool) {
			return fmt.Errorf("spec.steps[%d].tool must be in format 'workload.tool_name'", index)
		}
	}

	if stepType == WorkflowStepTypeElicitation {
		if step.Message == "" {
			return fmt.Errorf("spec.steps[%d].message is required when type is elicitation", index)
		}
	}

	// Validate arguments contain valid templates
	for argName, argValue := range step.Arguments {
		if err := validateTemplate(argValue); err != nil {
			return fmt.Errorf("spec.steps[%d].arguments[%s]: invalid template: %v", index, argName, err)
		}
	}

	// Validate condition template
	if step.Condition != "" {
		if err := validateTemplate(step.Condition); err != nil {
			return fmt.Errorf("spec.steps[%d].condition: invalid template: %v", index, err)
		}
	}

	// Validate message template for elicitation
	if step.Message != "" {
		if err := validateTemplate(step.Message); err != nil {
			return fmt.Errorf("spec.steps[%d].message: invalid template: %v", index, err)
		}
	}

	// Validate dependsOn references exist
	for _, depID := range step.DependsOn {
		if !stepIDs[depID] {
			return fmt.Errorf("spec.steps[%d].dependsOn references unknown step %q", index, depID)
		}
	}

	// Validate error handling
	if step.OnError != nil {
		if err := r.validateErrorHandling(index, step.OnError); err != nil {
			return err
		}
	}

	// Validate timeout format
	if step.Timeout != "" {
		if err := validateDuration(step.Timeout); err != nil {
			return fmt.Errorf("spec.steps[%d].timeout: %v", index, err)
		}
	}

	return nil
}

// validateErrorHandling validates error handling configuration
func (r *VirtualMCPCompositeToolDefinition) validateErrorHandling(stepIndex int, errorHandling *ErrorHandling) error {
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
func (r *VirtualMCPCompositeToolDefinition) validateDependencyCycles(stepIndices map[string]int) error {
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
