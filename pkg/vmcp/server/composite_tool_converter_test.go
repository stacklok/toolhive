// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestBuildOutputSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output *config.OutputConfig
		want   map[string]any
	}{
		{
			name:   "nil output config",
			output: nil,
			want:   nil,
		},
		{
			name: "simple string property",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"result": {
						Type:        "string",
						Description: "The result",
						Value:       "{{.steps.step1.output.data}}",
					},
				},
			},
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"result": map[string]any{
						"type":        "string",
						"description": "The result",
					},
				},
			},
		},
		{
			name: "multiple properties with different types",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"name": {
						Type:        "string",
						Description: "Name",
						Value:       "{{.params.name}}",
					},
					"count": {
						Type:        "integer",
						Description: "Count",
						Value:       "{{.steps.step1.output.count}}",
					},
					"active": {
						Type:        "boolean",
						Description: "Active flag",
						Value:       "{{.steps.step1.output.active}}",
					},
				},
			},
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Name",
					},
					"count": map[string]any{
						"type":        "integer",
						"description": "Count",
					},
					"active": map[string]any{
						"type":        "boolean",
						"description": "Active flag",
					},
				},
			},
		},
		{
			name: "nested object properties",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"metadata": {
						Type:        "object",
						Description: "Metadata",
						Properties: map[string]config.OutputProperty{
							"version": {
								Type:        "string",
								Description: "Version",
								Value:       "{{.steps.step1.output.version}}",
							},
							"timestamp": {
								Type:        "integer",
								Description: "Timestamp",
								Value:       "{{.steps.step1.output.ts}}",
							},
						},
					},
				},
			},
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"metadata": map[string]any{
						"type":        "object",
						"description": "Metadata",
						"properties": map[string]any{
							"version": map[string]any{
								"type":        "string",
								"description": "Version",
							},
							"timestamp": map[string]any{
								"type":        "integer",
								"description": "Timestamp",
							},
						},
					},
				},
			},
		},
		{
			name: "with required fields",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"required_field": {
						Type:        "string",
						Description: "Required",
						Value:       "value",
					},
					"optional_field": {
						Type:        "string",
						Description: "Optional",
						Value:       "value",
					},
				},
				Required: []string{"required_field"},
			},
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"required_field": map[string]any{
						"type":        "string",
						"description": "Required",
					},
					"optional_field": map[string]any{
						"type":        "string",
						"description": "Optional",
					},
				},
				"required": []string{"required_field"},
			},
		},
		{
			name: "deeply nested structure",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"level1": {
						Type:        "object",
						Description: "Level 1",
						Properties: map[string]config.OutputProperty{
							"level2": {
								Type:        "object",
								Description: "Level 2",
								Properties: map[string]config.OutputProperty{
									"level3": {
										Type:        "string",
										Description: "Level 3",
										Value:       "deep_value",
									},
								},
							},
						},
					},
				},
			},
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"level1": map[string]any{
						"type":        "object",
						"description": "Level 1",
						"properties": map[string]any{
							"level2": map[string]any{
								"type":        "object",
								"description": "Level 2",
								"properties": map[string]any{
									"level3": map[string]any{
										"type":        "string",
										"description": "Level 3",
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "object with value (not properties)",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"data": {
						Type:        "object",
						Description: "Data object",
						Value:       "{{.steps.step1.output.json_data}}",
					},
				},
			},
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"data": map[string]any{
						"type":        "object",
						"description": "Data object",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := buildOutputSchema(tt.output)

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("buildOutputSchema() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestConvertWorkflowDefsToToolsWithOutputSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		defs         map[string]*composer.WorkflowDefinition
		want         int // number of tools expected
		validateTool func(*testing.T, map[string]*composer.WorkflowDefinition, []any)
	}{
		{
			name: "empty definitions",
			defs: map[string]*composer.WorkflowDefinition{},
			want: 0,
		},
		{
			name: "workflow without output schema",
			defs: map[string]*composer.WorkflowDefinition{
				"test": {
					Name:        "test_workflow",
					Description: "Test workflow",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"param1": map[string]any{
								"type": "string",
							},
						},
					},
					Output: nil,
				},
			},
			want: 1,
			validateTool: func(t *testing.T, _ map[string]*composer.WorkflowDefinition, tools []any) {
				t.Helper()
				if len(tools) != 1 {
					t.Fatalf("expected 1 tool, got %d", len(tools))
				}
				// Tool should not have OutputSchema field set
			},
		},
		{
			name: "workflow with output schema",
			defs: map[string]*composer.WorkflowDefinition{
				"test": {
					Name:        "test_workflow",
					Description: "Test workflow",
					Parameters: map[string]any{
						"type": "object",
					},
					Output: &config.OutputConfig{
						Properties: map[string]config.OutputProperty{
							"result": {
								Type:        "string",
								Description: "Result",
								Value:       "{{.steps.step1.output}}",
							},
						},
					},
				},
			},
			want: 1,
			validateTool: func(t *testing.T, _ map[string]*composer.WorkflowDefinition, tools []any) {
				t.Helper()
				if len(tools) != 1 {
					t.Fatalf("expected 1 tool, got %d", len(tools))
				}
				// Tool should have OutputSchema field set
			},
		},
		{
			name: "multiple workflows",
			defs: map[string]*composer.WorkflowDefinition{
				"workflow1": {
					Name:        "workflow1",
					Description: "First workflow",
					Output: &config.OutputConfig{
						Properties: map[string]config.OutputProperty{
							"result1": {
								Type:        "string",
								Description: "Result 1",
								Value:       "value",
							},
						},
					},
				},
				"workflow2": {
					Name:        "workflow2",
					Description: "Second workflow",
					Output: &config.OutputConfig{
						Properties: map[string]config.OutputProperty{
							"result2": {
								Type:        "integer",
								Description: "Result 2",
								Value:       "42",
							},
						},
					},
				},
			},
			want: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tools := convertWorkflowDefsToTools(tt.defs)

			if len(tools) != tt.want {
				t.Errorf("convertWorkflowDefsToTools() returned %d tools, want %d", len(tools), tt.want)
			}

			if tt.validateTool != nil {
				// Convert tools to []any for validation function
				toolsAny := make([]any, len(tools))
				for i, tool := range tools {
					toolsAny[i] = tool
				}
				tt.validateTool(t, tt.defs, toolsAny)
			}

			// Verify all tools have required fields
			for _, tool := range tools {
				if tool.Name == "" {
					t.Error("Tool missing name")
				}
				if tool.Description == "" {
					t.Error("Tool missing description")
				}
			}
		})
	}
}

func TestBuildOutputPropertySchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		prop config.OutputProperty
		want map[string]any
	}{
		{
			name: "simple string property",
			prop: config.OutputProperty{
				Type:        "string",
				Description: "A string",
				Value:       "{{.steps.step1.output}}",
			},
			want: map[string]any{
				"type":        "string",
				"description": "A string",
			},
		},
		{
			name: "integer property",
			prop: config.OutputProperty{
				Type:        "integer",
				Description: "An integer",
				Value:       "{{.steps.step1.output.count}}",
			},
			want: map[string]any{
				"type":        "integer",
				"description": "An integer",
			},
		},
		{
			name: "object with nested properties",
			prop: config.OutputProperty{
				Type:        "object",
				Description: "An object",
				Properties: map[string]config.OutputProperty{
					"field1": {
						Type:        "string",
						Description: "Field 1",
						Value:       "value",
					},
					"field2": {
						Type:        "integer",
						Description: "Field 2",
						Value:       "42",
					},
				},
			},
			want: map[string]any{
				"type":        "object",
				"description": "An object",
				"properties": map[string]any{
					"field1": map[string]any{
						"type":        "string",
						"description": "Field 1",
					},
					"field2": map[string]any{
						"type":        "integer",
						"description": "Field 2",
					},
				},
			},
		},
		{
			name: "array property",
			prop: config.OutputProperty{
				Type:        "array",
				Description: "An array",
				Value:       "{{.steps.step1.output.items}}",
			},
			want: map[string]any{
				"type":        "array",
				"description": "An array",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := buildOutputPropertySchema(tt.prop)

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("buildOutputPropertySchema() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
