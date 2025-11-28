// Package server implements the Virtual MCP Server that aggregates
// multiple backend MCP servers into a unified interface.
package server

import (
	"fmt"
	"time"

	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// ConvertConfigToWorkflowDefinitions converts configuration composite tools to workflow definitions.
//
// This function performs the following transformations:
//  1. Convert config.CompositeToolConfig to composer.WorkflowDefinition
//  2. Validate workflow definitions (basic checks, not full validation)
//  3. Return map of workflow definitions keyed by workflow name
//
// Full validation (cycle detection, tool references) is performed later during server initialization
// via composer.ValidateWorkflow().
//
// Returns error if any composite tool configuration is invalid or duplicate names exist.
func ConvertConfigToWorkflowDefinitions(
	compositeTools []*config.CompositeToolConfig,
) (map[string]*composer.WorkflowDefinition, error) {
	if len(compositeTools) == 0 {
		return nil, nil
	}

	workflowDefs := make(map[string]*composer.WorkflowDefinition, len(compositeTools))

	for _, ct := range compositeTools {
		// Validate basic requirements
		if ct.Name == "" {
			return nil, fmt.Errorf("composite tool name is required")
		}

		// Check for duplicate names
		if _, exists := workflowDefs[ct.Name]; exists {
			return nil, fmt.Errorf("duplicate composite tool name: %s", ct.Name)
		}

		// Convert steps
		steps, err := convertSteps(ct.Steps)
		if err != nil {
			return nil, fmt.Errorf("failed to convert steps for composite tool %s: %w", ct.Name, err)
		}

		// Parameters are already in JSON Schema format, pass through directly
		params := ct.Parameters

		// Convert timeout
		var timeout time.Duration
		if ct.Timeout > 0 {
			timeout = time.Duration(ct.Timeout)
		}

		// Create workflow definition
		def := &composer.WorkflowDefinition{
			Name:        ct.Name,
			Description: ct.Description,
			Parameters:  params,
			Steps:       steps,
			Timeout:     timeout,
			Output:      ct.Output,
			Metadata:    make(map[string]string),
		}

		workflowDefs[ct.Name] = def
	}

	return workflowDefs, nil
}

// convertSteps converts configuration steps to workflow steps.
func convertSteps(configSteps []*config.WorkflowStepConfig) ([]composer.WorkflowStep, error) {
	if len(configSteps) == 0 {
		return nil, fmt.Errorf("workflow must have at least one step")
	}

	steps := make([]composer.WorkflowStep, 0, len(configSteps))

	for i, cs := range configSteps {
		step, err := convertSingleStep(i, cs)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}

	return steps, nil
}

// convertSingleStep converts a single configuration step to a workflow step.
func convertSingleStep(index int, cs *config.WorkflowStepConfig) (composer.WorkflowStep, error) {
	// Validate basic requirements
	if err := validateStepBasics(index, cs); err != nil {
		return composer.WorkflowStep{}, err
	}

	// Convert step type
	stepType, err := parseStepType(cs)
	if err != nil {
		return composer.WorkflowStep{}, err
	}

	// Convert optional fields
	onError := convertErrorHandler(cs.OnError)
	elicitation, err := convertElicitation(stepType, cs)
	if err != nil {
		return composer.WorkflowStep{}, err
	}

	stepTimeout := time.Duration(0)
	if cs.Timeout > 0 {
		stepTimeout = time.Duration(cs.Timeout)
	}

	// Create workflow step
	return composer.WorkflowStep{
		ID:          cs.ID,
		Type:        stepType,
		Tool:        cs.Tool,
		Arguments:   cs.Arguments,
		Condition:   cs.Condition,
		DependsOn:   cs.DependsOn,
		OnError:     onError,
		Elicitation: elicitation,
		Timeout:     stepTimeout,
		Metadata:    make(map[string]string),
	}, nil
}

// validateStepBasics validates basic step requirements.
func validateStepBasics(index int, cs *config.WorkflowStepConfig) error {
	if cs.ID == "" {
		return fmt.Errorf("step %d: step ID is required", index)
	}
	if cs.Type == "" {
		return fmt.Errorf("step %s: step type is required", cs.ID)
	}
	return nil
}

// parseStepType converts string step type to composer.StepType.
func parseStepType(cs *config.WorkflowStepConfig) (composer.StepType, error) {
	var stepType composer.StepType
	switch cs.Type {
	case "tool":
		stepType = composer.StepTypeTool
		if cs.Tool == "" {
			return "", fmt.Errorf("step %s: tool name is required for tool steps", cs.ID)
		}
	case "elicitation":
		stepType = composer.StepTypeElicitation
	default:
		return "", fmt.Errorf("step %s: invalid step type %s", cs.ID, cs.Type)
	}
	return stepType, nil
}

// convertErrorHandler converts configuration error handler to composer format.
func convertErrorHandler(cfgHandler *config.StepErrorHandling) *composer.ErrorHandler {
	if cfgHandler == nil {
		return nil
	}

	retryDelay := time.Duration(0)
	if cfgHandler.RetryDelay > 0 {
		retryDelay = time.Duration(cfgHandler.RetryDelay)
	}

	return &composer.ErrorHandler{
		Action:     cfgHandler.Action,
		RetryCount: cfgHandler.RetryCount,
		RetryDelay: retryDelay,
	}
}

// convertElicitation converts elicitation configuration if step type is elicitation.
func convertElicitation(
	stepType composer.StepType,
	cs *config.WorkflowStepConfig,
) (*composer.ElicitationConfig, error) {
	if stepType != composer.StepTypeElicitation {
		return nil, nil
	}

	if cs.Message == "" {
		return nil, fmt.Errorf("step %s: message is required for elicitation steps", cs.ID)
	}
	if cs.Schema == nil {
		return nil, fmt.Errorf("step %s: schema is required for elicitation steps", cs.ID)
	}

	timeout := time.Duration(0)
	if cs.Timeout > 0 {
		timeout = time.Duration(cs.Timeout)
	}

	elicitation := &composer.ElicitationConfig{
		Message: cs.Message,
		Schema:  cs.Schema,
		Timeout: timeout,
	}

	// Convert elicitation response handlers
	if cs.OnDecline != nil {
		elicitation.OnDecline = &composer.ElicitationHandler{
			Action: cs.OnDecline.Action,
		}
	}
	if cs.OnCancel != nil {
		elicitation.OnCancel = &composer.ElicitationHandler{
			Action: cs.OnCancel.Action,
		}
	}

	return elicitation, nil
}
