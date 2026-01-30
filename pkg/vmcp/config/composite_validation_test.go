// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	thvjson "github.com/stacklok/toolhive/pkg/json"
)

func TestValidateDefaultResultsForSteps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		steps       []WorkflowStepConfig
		output      *OutputConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "no skippable steps - no validation needed",
			steps: []WorkflowStepConfig{
				{ID: "step1"},
				{ID: "step2", Arguments: thvjson.NewMap(map[string]any{"input": "{{.steps.step1.output.data}}"})},
			},
			expectError: false,
		},
		{
			name: "conditional step with defaultResults - valid",
			steps: []WorkflowStepConfig{
				{
					ID:             "step1",
					Condition:      "{{.params.runStep1}}",
					DefaultResults: thvjson.NewMap(map[string]any{"result": nil}),
				},
				{ID: "step2", Arguments: thvjson.NewMap(map[string]any{"input": "{{.steps.step1.output.result}}"})},
			},
			expectError: false,
		},
		{
			name: "conditional step without defaultResults - referenced downstream - invalid",
			steps: []WorkflowStepConfig{
				{
					ID:        "step1",
					Condition: "{{.params.runStep1}}",
				},
				{ID: "step2", Arguments: thvjson.NewMap(map[string]any{"input": "{{.steps.step1.output.data}}"})},
			},
			expectError: true,
			errorMsg:    "defaultResults[data] is required",
		},
		{
			name: "conditional step without defaultResults - not referenced - valid",
			steps: []WorkflowStepConfig{
				{
					ID:        "step1",
					Condition: "{{.params.runStep1}}",
				},
				{ID: "step2"},
			},
			expectError: false,
		},
		{
			name: "status reference does not require defaultResults",
			steps: []WorkflowStepConfig{
				{
					ID:        "step1",
					Condition: "{{.params.runStep1}}",
				},
				{ID: "step2", Condition: `{{eq .steps.step1.status "completed"}}`},
			},
			expectError: false,
		},
		{
			name: "continue-on-error step with defaultResults - valid",
			steps: []WorkflowStepConfig{
				{
					ID:             "step1",
					OnError:        &StepErrorHandling{Action: ErrorActionContinue},
					DefaultResults: thvjson.NewMap(map[string]any{"result": nil}),
				},
				{ID: "step2", Arguments: thvjson.NewMap(map[string]any{"input": "{{.steps.step1.output.result}}"})},
			},
			expectError: false,
		},
		{
			name: "continue-on-error step without defaultResults - referenced - invalid",
			steps: []WorkflowStepConfig{
				{
					ID:      "step1",
					OnError: &StepErrorHandling{Action: ErrorActionContinue},
				},
				{ID: "step2", Arguments: thvjson.NewMap(map[string]any{"input": "{{.steps.step1.output.data}}"})},
			},
			expectError: true,
			errorMsg:    "defaultResults[data] is required",
		},
		{
			name: "retry step without defaultResults - referenced - valid (retry is not skippable)",
			steps: []WorkflowStepConfig{
				{
					ID:      "step1",
					OnError: &StepErrorHandling{Action: ErrorActionRetry, RetryCount: 3},
				},
				{ID: "step2", Arguments: thvjson.NewMap(map[string]any{"input": "{{.steps.step1.output.data}}"})},
			},
			expectError: false,
		},
		{
			name: "conditional step referenced in output - valid with defaults",
			steps: []WorkflowStepConfig{
				{
					ID:             "step1",
					Condition:      "{{.params.runStep1}}",
					DefaultResults: thvjson.NewMap(map[string]any{"data": nil}),
				},
			},
			output: &OutputConfig{
				Properties: map[string]OutputProperty{
					"result": {Value: "{{.steps.step1.output.data}}"},
				},
			},
			expectError: false,
		},
		{
			name: "conditional step referenced in output - invalid without defaults",
			steps: []WorkflowStepConfig{
				{
					ID:        "step1",
					Condition: "{{.params.runStep1}}",
				},
			},
			output: &OutputConfig{
				Properties: map[string]OutputProperty{
					"result": {Value: "{{.steps.step1.output.data}}"},
				},
			},
			expectError: true,
			errorMsg:    "defaultResults[data] is required",
		},
		{
			name: "reference in condition - valid with defaults",
			steps: []WorkflowStepConfig{
				{
					ID:             "step1",
					Condition:      "{{.params.runStep1}}",
					DefaultResults: thvjson.NewMap(map[string]any{"success": nil}),
				},
				{
					ID:        "step2",
					Condition: "{{.steps.step1.output.success}}",
				},
			},
			expectError: false,
		},
		{
			name: "reference in message (elicitation) - valid with defaults",
			steps: []WorkflowStepConfig{
				{
					ID:             "step1",
					Condition:      "{{.params.runStep1}}",
					DefaultResults: thvjson.NewMap(map[string]any{"summary": nil}),
				},
				{
					ID:      "step2",
					Type:    WorkflowStepTypeElicitation,
					Message: "Result: {{.steps.step1.output.summary}}",
				},
			},
			expectError: false,
		},
		{
			name: "multiple skippable steps - all need defaults if referenced",
			steps: []WorkflowStepConfig{
				{
					ID:        "step1",
					Condition: "{{.params.a}}",
				},
				{
					ID:        "step2",
					Condition: "{{.params.b}}",
				},
				{
					ID:        "step3",
					Arguments: thvjson.NewMap(map[string]any{"a": "{{.steps.step1.output.data}}", "b": "{{.steps.step2.output.data}}"}),
				},
			},
			expectError: true,
			errorMsg:    "defaultResults[data] is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateDefaultResultsForSteps("spec.steps", tt.steps, tt.output)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestStepMayBeSkipped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		step     WorkflowStepConfig
		expected bool
	}{
		{
			name:     "step without condition or error handling",
			step:     WorkflowStepConfig{ID: "step1"},
			expected: false,
		},
		{
			name:     "step with condition",
			step:     WorkflowStepConfig{ID: "step1", Condition: "{{.params.run}}"},
			expected: true,
		},
		{
			name:     "step with continue-on-error",
			step:     WorkflowStepConfig{ID: "step1", OnError: &StepErrorHandling{Action: ErrorActionContinue}},
			expected: true,
		},
		{
			name:     "step with abort error handling",
			step:     WorkflowStepConfig{ID: "step1", OnError: &StepErrorHandling{Action: ErrorActionAbort}},
			expected: false,
		},
		{
			name:     "step with retry error handling",
			step:     WorkflowStepConfig{ID: "step1", OnError: &StepErrorHandling{Action: ErrorActionRetry, RetryCount: 3}},
			expected: false,
		},
		{
			name:     "step with both condition and continue-on-error",
			step:     WorkflowStepConfig{ID: "step1", Condition: "{{.params.run}}", OnError: &StepErrorHandling{Action: ErrorActionContinue}},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := stepMayBeSkipped(tt.step)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractStepFieldRefsFromTemplate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		template string
		expected []stepFieldRef
	}{
		{
			name:     "output field reference",
			template: "{{.steps.step1.output.data}}",
			expected: []stepFieldRef{{stepID: "step1", field: "data"}},
		},
		{
			name:     "multiple output field references",
			template: "{{.steps.step1.output.a}} and {{.steps.step2.output.b}}",
			expected: []stepFieldRef{{stepID: "step1", field: "a"}, {stepID: "step2", field: "b"}},
		},
		{
			name:     "duplicate output field references",
			template: "{{.steps.step1.output.a}} and {{.steps.step1.output.a}}",
			expected: []stepFieldRef{{stepID: "step1", field: "a"}},
		},
		{
			name:     "same step different output fields",
			template: "{{.steps.step1.output.a}} and {{.steps.step1.output.b}}",
			expected: []stepFieldRef{{stepID: "step1", field: "a"}, {stepID: "step1", field: "b"}},
		},
		{
			name:     "no step references",
			template: "{{.params.value}}",
			expected: []stepFieldRef{},
		},
		{
			name:     "status reference ignored",
			template: `{{eq .steps.step1.status "completed"}}`,
			expected: []stepFieldRef{},
		},
		{
			name:     "error reference ignored",
			template: "{{.steps.step1.error}}",
			expected: []stepFieldRef{},
		},
		{
			name:     "bare output reference ignored (no field)",
			template: "{{.steps.step1.output}}",
			expected: []stepFieldRef{},
		},
		{
			name:     "nested output field extracts first level only",
			template: "{{.steps.step1.output.data.nested.field}}",
			expected: []stepFieldRef{{stepID: "step1", field: "data"}},
		},
		{
			name:     "function with output field reference",
			template: `{{eq .steps.step1.output.count 5}}`,
			expected: []stepFieldRef{{stepID: "step1", field: "count"}},
		},
		{
			name:     "plain text",
			template: "just some text",
			expected: []stepFieldRef{},
		},
		{
			name:     "mixed output and status references",
			template: `{{if eq .steps.step1.status "completed"}}{{.steps.step1.output.result}}{{end}}`,
			expected: []stepFieldRef{{stepID: "step1", field: "result"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := extractStepFieldRefsFromTemplate(tt.template)
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.expected, result)
		})
	}
}

func TestValidateCompositeToolConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		tool        *CompositeToolConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid tool",
			tool: &CompositeToolConfig{
				Name:        "test-tool",
				Description: "A test tool",
				Steps: []WorkflowStepConfig{
					{ID: "step1", Type: "tool", Tool: "backend.echo"},
				},
			},
			expectError: false,
		},
		{
			name: "missing name",
			tool: &CompositeToolConfig{
				Description: "A test tool",
				Steps: []WorkflowStepConfig{
					{ID: "step1", Type: "tool", Tool: "backend.echo"},
				},
			},
			expectError: true,
			errorMsg:    "name is required",
		},
		{
			name: "missing description",
			tool: &CompositeToolConfig{
				Name: "test-tool",
				Steps: []WorkflowStepConfig{
					{ID: "step1", Type: "tool", Tool: "backend.echo"},
				},
			},
			expectError: true,
			errorMsg:    "description is required",
		},
		{
			name: "no steps",
			tool: &CompositeToolConfig{
				Name:        "test-tool",
				Description: "A test tool",
				Steps:       []WorkflowStepConfig{},
			},
			expectError: true,
			errorMsg:    "steps must have at least one step",
		},
		{
			name: "invalid tool reference with special characters",
			tool: &CompositeToolConfig{
				Name:        "test-tool",
				Description: "A test tool",
				Steps: []WorkflowStepConfig{
					{ID: "step1", Type: "tool", Tool: "invalid@tool!"},
				},
			},
			expectError: true,
			errorMsg:    "must be a valid tool name",
		},
		{
			name: "duplicate step IDs",
			tool: &CompositeToolConfig{
				Name:        "test-tool",
				Description: "A test tool",
				Steps: []WorkflowStepConfig{
					{ID: "step1", Type: "tool", Tool: "backend.echo"},
					{ID: "step1", Type: "tool", Tool: "backend.other"},
				},
			},
			expectError: true,
			errorMsg:    "duplicated",
		},
		{
			name: "dependency on unknown step",
			tool: &CompositeToolConfig{
				Name:        "test-tool",
				Description: "A test tool",
				Steps: []WorkflowStepConfig{
					{ID: "step1", Type: "tool", Tool: "backend.echo", DependsOn: []string{"unknown"}},
				},
			},
			expectError: true,
			errorMsg:    "references unknown step",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateCompositeToolConfig("spec", tt.tool)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateWorkflowStepTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		step        WorkflowStepConfig
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid tool step",
			step:        WorkflowStepConfig{ID: "step1", Type: "tool", Tool: "backend.echo"},
			expectError: false,
		},
		{
			name:        "valid elicitation step",
			step:        WorkflowStepConfig{ID: "step1", Type: "elicitation", Message: "Please confirm"},
			expectError: false,
		},
		{
			name:        "tool step missing tool field",
			step:        WorkflowStepConfig{ID: "step1", Type: "tool"},
			expectError: true,
			errorMsg:    "tool is required",
		},
		{
			name:        "elicitation step missing message",
			step:        WorkflowStepConfig{ID: "step1", Type: "elicitation"},
			expectError: true,
			errorMsg:    "message is required",
		},
		{
			name:        "invalid step type",
			step:        WorkflowStepConfig{ID: "step1", Type: "invalid"},
			expectError: true,
			errorMsg:    "must be one of: tool, elicitation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateStepType("spec.steps", 0, &tt.step)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateStepErrorHandling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		onError     *StepErrorHandling
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid abort action",
			onError:     &StepErrorHandling{Action: "abort"},
			expectError: false,
		},
		{
			name:        "valid continue action",
			onError:     &StepErrorHandling{Action: "continue"},
			expectError: false,
		},
		{
			name:        "valid retry action with count",
			onError:     &StepErrorHandling{Action: "retry", RetryCount: 3},
			expectError: false,
		},
		{
			name:        "retry without count",
			onError:     &StepErrorHandling{Action: "retry"},
			expectError: true,
			errorMsg:    "retryCount must be at least 1",
		},
		{
			name:        "invalid action",
			onError:     &StepErrorHandling{Action: "invalid"},
			expectError: true,
			errorMsg:    "must be one of: abort, continue, retry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateStepErrorHandling("spec.steps", 0, tt.onError)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateDependencyCycles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		steps       []WorkflowStepConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "no cycles - linear",
			steps: []WorkflowStepConfig{
				{ID: "step1"},
				{ID: "step2", DependsOn: []string{"step1"}},
				{ID: "step3", DependsOn: []string{"step2"}},
			},
			expectError: false,
		},
		{
			name: "no cycles - diamond",
			steps: []WorkflowStepConfig{
				{ID: "step1"},
				{ID: "step2", DependsOn: []string{"step1"}},
				{ID: "step3", DependsOn: []string{"step1"}},
				{ID: "step4", DependsOn: []string{"step2", "step3"}},
			},
			expectError: false,
		},
		{
			name: "self-cycle",
			steps: []WorkflowStepConfig{
				{ID: "step1", DependsOn: []string{"step1"}},
			},
			expectError: true,
			errorMsg:    "dependency cycle detected",
		},
		{
			name: "two-step cycle",
			steps: []WorkflowStepConfig{
				{ID: "step1", DependsOn: []string{"step2"}},
				{ID: "step2", DependsOn: []string{"step1"}},
			},
			expectError: true,
			errorMsg:    "dependency cycle detected",
		},
		{
			name: "three-step cycle",
			steps: []WorkflowStepConfig{
				{ID: "step1", DependsOn: []string{"step3"}},
				{ID: "step2", DependsOn: []string{"step1"}},
				{ID: "step3", DependsOn: []string{"step2"}},
			},
			expectError: true,
			errorMsg:    "dependency cycle detected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateDependencyCycles("spec.steps", tt.steps)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
