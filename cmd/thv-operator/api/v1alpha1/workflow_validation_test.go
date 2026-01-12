package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	thvjson "github.com/stacklok/toolhive/pkg/json"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestValidateDefaultResultsForSteps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		steps       []config.WorkflowStepConfig
		output      *config.OutputConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "no skippable steps - no validation needed",
			steps: []config.WorkflowStepConfig{
				{ID: "step1"},
				{ID: "step2", Arguments: thvjson.NewMap(map[string]any{"input": "{{.steps.step1.output.data}}"})},
			},
			expectError: false,
		},
		{
			name: "conditional step with defaultResults - valid",
			steps: []config.WorkflowStepConfig{
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
			steps: []config.WorkflowStepConfig{
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
			steps: []config.WorkflowStepConfig{
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
			steps: []config.WorkflowStepConfig{
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
			steps: []config.WorkflowStepConfig{
				{
					ID:             "step1",
					OnError:        &config.StepErrorHandling{Action: ErrorActionContinue},
					DefaultResults: thvjson.NewMap(map[string]any{"result": nil}),
				},
				{ID: "step2", Arguments: thvjson.NewMap(map[string]any{"input": "{{.steps.step1.output.result}}"})},
			},
			expectError: false,
		},
		{
			name: "continue-on-error step without defaultResults - referenced - invalid",
			steps: []config.WorkflowStepConfig{
				{
					ID:      "step1",
					OnError: &config.StepErrorHandling{Action: ErrorActionContinue},
				},
				{ID: "step2", Arguments: thvjson.NewMap(map[string]any{"input": "{{.steps.step1.output.data}}"})},
			},
			expectError: true,
			errorMsg:    "defaultResults[data] is required",
		},
		{
			name: "retry step without defaultResults - referenced - valid (retry is not skippable)",
			steps: []config.WorkflowStepConfig{
				{
					ID:      "step1",
					OnError: &config.StepErrorHandling{Action: ErrorActionRetry, RetryCount: 3},
				},
				{ID: "step2", Arguments: thvjson.NewMap(map[string]any{"input": "{{.steps.step1.output.data}}"})},
			},
			expectError: false,
		},
		{
			name: "conditional step referenced in output - valid with defaults",
			steps: []config.WorkflowStepConfig{
				{
					ID:             "step1",
					Condition:      "{{.params.runStep1}}",
					DefaultResults: thvjson.NewMap(map[string]any{"data": nil}),
				},
			},
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"result": {Value: "{{.steps.step1.output.data}}"},
				},
			},
			expectError: false,
		},
		{
			name: "conditional step referenced in output - invalid without defaults",
			steps: []config.WorkflowStepConfig{
				{
					ID:        "step1",
					Condition: "{{.params.runStep1}}",
				},
			},
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"result": {Value: "{{.steps.step1.output.data}}"},
				},
			},
			expectError: true,
			errorMsg:    "defaultResults[data] is required",
		},
		{
			name: "reference in condition - valid with defaults",
			steps: []config.WorkflowStepConfig{
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
			steps: []config.WorkflowStepConfig{
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
			steps: []config.WorkflowStepConfig{
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

			err := validateDefaultResultsForSteps("spec.steps", tt.steps, tt.output)

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
		step     config.WorkflowStepConfig
		expected bool
	}{
		{
			name:     "step without condition or error handling",
			step:     config.WorkflowStepConfig{ID: "step1"},
			expected: false,
		},
		{
			name:     "step with condition",
			step:     config.WorkflowStepConfig{ID: "step1", Condition: "{{.params.run}}"},
			expected: true,
		},
		{
			name:     "step with continue-on-error",
			step:     config.WorkflowStepConfig{ID: "step1", OnError: &config.StepErrorHandling{Action: ErrorActionContinue}},
			expected: true,
		},
		{
			name:     "step with abort error handling",
			step:     config.WorkflowStepConfig{ID: "step1", OnError: &config.StepErrorHandling{Action: ErrorActionAbort}},
			expected: false,
		},
		{
			name:     "step with retry error handling",
			step:     config.WorkflowStepConfig{ID: "step1", OnError: &config.StepErrorHandling{Action: ErrorActionRetry, RetryCount: 3}},
			expected: false,
		},
		{
			name:     "step with both condition and continue-on-error",
			step:     config.WorkflowStepConfig{ID: "step1", Condition: "{{.params.run}}", OnError: &config.StepErrorHandling{Action: ErrorActionContinue}},
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
