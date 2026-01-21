// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	thvjson "github.com/stacklok/toolhive/pkg/json"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestConvertConfigToWorkflowDefinitions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       []config.CompositeToolConfig
		wantCount   int
		wantError   bool
		errContains string
	}{
		{
			name:      "empty input",
			input:     nil,
			wantCount: 0,
		},
		{
			name: "valid tool step",
			input: []config.CompositeToolConfig{{
				Name: "simple",
				Steps: []config.WorkflowStepConfig{
					{ID: "s1", Type: "tool", Tool: "backend.tool"},
				},
			}},
			wantCount: 1,
		},
		{
			name: "valid elicitation step",
			input: []config.CompositeToolConfig{{
				Name: "confirm",
				Steps: []config.WorkflowStepConfig{{
					ID: "s1", Type: "elicitation",
					Message: "Confirm?",
					Schema:  thvjson.NewMap(map[string]any{"type": "object"}),
				}},
			}},
			wantCount: 1,
		},
		{
			name:        "missing name",
			input:       []config.CompositeToolConfig{{Name: "", Steps: []config.WorkflowStepConfig{{ID: "s1", Type: "tool", Tool: "t"}}}},
			wantError:   true,
			errContains: "name is required",
		},
		{
			name: "duplicate names",
			input: []config.CompositeToolConfig{
				{Name: "dup", Steps: []config.WorkflowStepConfig{{ID: "s1", Type: "tool", Tool: "t1"}}},
				{Name: "dup", Steps: []config.WorkflowStepConfig{{ID: "s2", Type: "tool", Tool: "t2"}}},
			},
			wantError:   true,
			errContains: "duplicate",
		},
		{
			name:        "no steps",
			input:       []config.CompositeToolConfig{{Name: "empty", Steps: []config.WorkflowStepConfig{}}},
			wantError:   true,
			errContains: "at least one step",
		},
		{
			name:        "missing step ID",
			input:       []config.CompositeToolConfig{{Name: "inv", Steps: []config.WorkflowStepConfig{{ID: "", Type: "tool", Tool: "t"}}}},
			wantError:   true,
			errContains: "step ID is required",
		},
		{
			name:        "invalid step type",
			input:       []config.CompositeToolConfig{{Name: "inv", Steps: []config.WorkflowStepConfig{{ID: "s1", Type: "invalid"}}}},
			wantError:   true,
			errContains: "invalid step type",
		},
		{
			name:        "tool step without tool name",
			input:       []config.CompositeToolConfig{{Name: "inv", Steps: []config.WorkflowStepConfig{{ID: "s1", Type: "tool"}}}},
			wantError:   true,
			errContains: "tool name is required",
		},
		{
			name:        "elicitation without message",
			input:       []config.CompositeToolConfig{{Name: "inv", Steps: []config.WorkflowStepConfig{{ID: "s1", Type: "elicitation", Schema: thvjson.NewMap(map[string]any{})}}}},
			wantError:   true,
			errContains: "message is required",
		},
		{
			name:        "elicitation without schema",
			input:       []config.CompositeToolConfig{{Name: "inv", Steps: []config.WorkflowStepConfig{{ID: "s1", Type: "elicitation", Message: "Test"}}}},
			wantError:   true,
			errContains: "schema is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := ConvertConfigToWorkflowDefinitions(tt.input)

			if tt.wantError {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				assert.Len(t, result, tt.wantCount)
			}
		})
	}
}

func TestConvertWorkflowDefsToTools(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input map[string]*composer.WorkflowDefinition
		want  int
	}{
		{name: "nil", input: nil, want: 0},
		{name: "empty", input: map[string]*composer.WorkflowDefinition{}, want: 0},
		{
			name:  "single",
			input: map[string]*composer.WorkflowDefinition{"w1": {Name: "w1", Description: "Test"}},
			want:  1,
		},
		{
			name: "multiple",
			input: map[string]*composer.WorkflowDefinition{
				"w1": {Name: "w1"},
				"w2": {Name: "w2"},
			},
			want: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := convertWorkflowDefsToTools(tt.input)

			if tt.want == 0 {
				assert.Nil(t, result)
			} else {
				require.Len(t, result, tt.want)
				for _, tool := range result {
					assert.NotEmpty(t, tool.Name)
				}
			}
		})
	}
}

func TestValidateNoToolConflicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		backend   []string
		composite []string
		wantError bool
		contains  string
	}{
		{name: "no conflicts", backend: []string{"b1", "b2"}, composite: []string{"c1", "c2"}},
		{name: "empty backend", backend: []string{}, composite: []string{"c1"}},
		{name: "empty composite", backend: []string{"b1"}, composite: []string{}},
		{name: "single conflict", backend: []string{"shared"}, composite: []string{"shared"}, wantError: true, contains: "shared"},
		{name: "multiple conflicts", backend: []string{"t1", "t2"}, composite: []string{"t1", "t2"}, wantError: true, contains: "t1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			backend := makeTools(tt.backend)
			composite := makeTools(tt.composite)

			err := validateNoToolConflicts(backend, composite)

			if tt.wantError {
				require.Error(t, err)
				if tt.contains != "" {
					assert.Contains(t, err.Error(), tt.contains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func makeTools(names []string) []vmcp.Tool {
	tools := make([]vmcp.Tool, len(names))
	for i, name := range names {
		tools[i] = vmcp.Tool{Name: name}
	}
	return tools
}

func TestConvertSteps_ComplexWorkflow(t *testing.T) {
	t.Parallel()

	input := []config.WorkflowStepConfig{
		{
			ID:   "merge",
			Type: "tool",
			Tool: "github.merge_pr",
			OnError: &config.StepErrorHandling{
				Action:     "retry",
				RetryCount: 3,
				RetryDelay: config.Duration(2 * time.Second),
			},
		},
		{
			ID:        "confirm",
			Type:      "elicitation",
			Message:   "Deploy?",
			Schema:    thvjson.NewMap(map[string]any{"type": "object"}),
			Timeout:   config.Duration(5 * time.Minute),
			DependsOn: []string{"merge"},
			OnDecline: &config.ElicitationResponseConfig{Action: "abort"},
		},
		{
			ID:        "deploy",
			Type:      "tool",
			Tool:      "k8s.deploy",
			Condition: "{{.steps.confirm.action == 'accept'}}",
			DependsOn: []string{"confirm"},
		},
	}

	result, err := convertSteps(input)

	require.NoError(t, err)
	require.Len(t, result, 3)

	// Verify step 1
	assert.Equal(t, "merge", result[0].ID)
	assert.Equal(t, composer.StepTypeTool, result[0].Type)
	assert.NotNil(t, result[0].OnError)
	assert.Equal(t, 3, result[0].OnError.RetryCount)

	// Verify step 2
	assert.Equal(t, "confirm", result[1].ID)
	assert.Equal(t, composer.StepTypeElicitation, result[1].Type)
	assert.NotNil(t, result[1].Elicitation)
	assert.Equal(t, "Deploy?", result[1].Elicitation.Message)

	// Verify step 3
	assert.Equal(t, "deploy", result[2].ID)
	assert.NotEmpty(t, result[2].Condition)
	assert.Equal(t, []string{"confirm"}, result[2].DependsOn)
}

// TestConvertConfigToWorkflowDefinitions_WithOutputConfig tests that output configuration
// is correctly copied from CompositeToolConfig to WorkflowDefinition.
func TestConvertConfigToWorkflowDefinitions_WithOutputConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  []config.CompositeToolConfig
		verify func(t *testing.T, defs map[string]*composer.WorkflowDefinition)
	}{
		{
			name: "composite tool with output config",
			input: []config.CompositeToolConfig{
				{
					Name:        "data_processor",
					Description: "Process data with typed output",
					Steps: []config.WorkflowStepConfig{
						{ID: "fetch", Type: "tool", Tool: "data.fetch"},
					},
					Output: &config.OutputConfig{
						Properties: map[string]config.OutputProperty{
							"message": {
								Type:        "string",
								Description: "Result message",
								Value:       "{{.steps.fetch.output.text}}",
							},
							"count": {
								Type:        "integer",
								Description: "Item count",
								Value:       "{{.steps.fetch.output.count}}",
							},
						},
						Required: []string{"message"},
					},
				},
			},
			verify: func(t *testing.T, defs map[string]*composer.WorkflowDefinition) {
				t.Helper()
				require.Len(t, defs, 1)

				def, exists := defs["data_processor"]
				require.True(t, exists)
				require.NotNil(t, def.Output, "Output should be set on WorkflowDefinition")

				assert.Len(t, def.Output.Properties, 2)
				assert.Equal(t, []string{"message"}, def.Output.Required)

				msgProp, exists := def.Output.Properties["message"]
				require.True(t, exists)
				assert.Equal(t, "string", msgProp.Type)
				assert.Equal(t, "Result message", msgProp.Description)
			},
		},
		{
			name: "composite tool without output config (backward compatible)",
			input: []config.CompositeToolConfig{
				{
					Name:   "simple_tool",
					Steps:  []config.WorkflowStepConfig{{ID: "step1", Type: "tool", Tool: "tool"}},
					Output: nil,
				},
			},
			verify: func(t *testing.T, defs map[string]*composer.WorkflowDefinition) {
				t.Helper()
				require.Len(t, defs, 1)

				def, exists := defs["simple_tool"]
				require.True(t, exists)
				assert.Nil(t, def.Output, "Output should be nil for backward compatibility")
			},
		},
		{
			name: "multiple tools with mixed output configs",
			input: []config.CompositeToolConfig{
				{
					Name:  "with_output",
					Steps: []config.WorkflowStepConfig{{ID: "s1", Type: "tool", Tool: "t1"}},
					Output: &config.OutputConfig{
						Properties: map[string]config.OutputProperty{
							"result": {Type: "string", Value: "{{.steps.s1.output.text}}"},
						},
					},
				},
				{
					Name:   "without_output",
					Steps:  []config.WorkflowStepConfig{{ID: "s2", Type: "tool", Tool: "t2"}},
					Output: nil,
				},
			},
			verify: func(t *testing.T, defs map[string]*composer.WorkflowDefinition) {
				t.Helper()
				require.Len(t, defs, 2)

				withOutput := defs["with_output"]
				require.NotNil(t, withOutput)
				assert.NotNil(t, withOutput.Output)

				withoutOutput := defs["without_output"]
				require.NotNil(t, withoutOutput)
				assert.Nil(t, withoutOutput.Output)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := ConvertConfigToWorkflowDefinitions(tt.input)
			require.NoError(t, err)
			require.NotNil(t, result)

			if tt.verify != nil {
				tt.verify(t, result)
			}
		})
	}
}
