// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package composer

import (
	"strings"
	"testing"

	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestValidateOutputConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		output  *config.OutputConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "nil output config is valid",
			output:  nil,
			wantErr: false,
		},
		{
			name: "valid simple output config",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"result": {
						Type:        "string",
						Description: "The result",
						Value:       "{{.steps.step1.output.data}}",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid output with required fields",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"result": {
						Type:        "string",
						Description: "The result",
						Value:       "{{.steps.step1.output.data}}",
					},
					"count": {
						Type:        "integer",
						Description: "Item count",
						Value:       "{{.steps.step1.output.count}}",
					},
				},
				Required: []string{"result"},
			},
			wantErr: false,
		},
		{
			name: "valid nested object output",
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
			wantErr: false,
		},
		{
			name: "empty properties",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{},
			},
			wantErr: true,
			errMsg:  "output properties cannot be empty",
		},
		{
			name: "required field not in properties",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"result": {
						Type:        "string",
						Description: "The result",
						Value:       "{{.steps.step1.output.data}}",
					},
				},
				Required: []string{"missing_field"},
			},
			wantErr: true,
			errMsg:  "does not exist in properties",
		},
		{
			name: "missing type",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"result": {
						Description: "The result",
						Value:       "{{.steps.step1.output.data}}",
					},
				},
			},
			wantErr: true,
			errMsg:  "missing required field 'type'",
		},
		{
			name: "invalid type",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"result": {
						Type:        "invalid_type",
						Description: "The result",
						Value:       "{{.steps.step1.output.data}}",
					},
				},
			},
			wantErr: true,
			errMsg:  "invalid type",
		},
		{
			name: "missing description",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"result": {
						Type:  "string",
						Value: "{{.steps.step1.output.data}}",
					},
				},
			},
			wantErr: true,
			errMsg:  "missing required field 'description'",
		},
		{
			name: "both value and properties specified",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"result": {
						Type:        "object",
						Description: "The result",
						Value:       "{{.steps.step1.output.data}}",
						Properties: map[string]config.OutputProperty{
							"field": {
								Type:        "string",
								Description: "A field",
								Value:       "value",
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "cannot have both 'value' and 'properties'",
		},
		{
			name: "neither value nor properties",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"result": {
						Type:        "object",
						Description: "The result",
					},
				},
			},
			wantErr: true,
			errMsg:  "must have either 'value' or 'properties'",
		},
		{
			name: "non-object type with properties",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"result": {
						Type:        "string",
						Description: "The result",
						Properties: map[string]config.OutputProperty{
							"field": {
								Type:        "string",
								Description: "A field",
								Value:       "value",
							},
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "must have 'value' field",
		},
		{
			name: "non-object type without value",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"result": {
						Type:        "string",
						Description: "The result",
					},
				},
			},
			wantErr: true,
			errMsg:  "must have either 'value' or 'properties'",
		},
		{
			name: "deeply nested properties (valid)",
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
										Value:       "value",
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "exceeds maximum nesting depth",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"l1": genNestedProperty(11), // Exceeds max depth of 10
				},
			},
			wantErr: true,
			errMsg:  "exceeds maximum nesting depth",
		},
		{
			name: "invalid template syntax - unbalanced braces",
			output: &config.OutputConfig{
				Properties: map[string]config.OutputProperty{
					"result": {
						Type:        "string",
						Description: "The result",
						Value:       "{{.steps.step1.output}",
					},
				},
			},
			wantErr: true,
			errMsg:  "invalid template syntax",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateOutputConfig(tt.output)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateOutputConfig() expected error containing %q, got nil", tt.errMsg)
					return
				}
				if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateOutputConfig() error = %v, want error containing %q", err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateOutputConfig() unexpected error = %v", err)
				}
			}
		})
	}
}

// genNestedProperty generates a nested property structure of the specified depth.
// Used for testing maximum depth validation.
func genNestedProperty(depth int) config.OutputProperty {
	if depth == 0 {
		return config.OutputProperty{
			Type:        "string",
			Description: "Leaf node",
			Value:       "value",
		}
	}
	return config.OutputProperty{
		Type:        "object",
		Description: "Nested object",
		Properties: map[string]config.OutputProperty{
			"nested": genNestedProperty(depth - 1),
		},
	}
}
