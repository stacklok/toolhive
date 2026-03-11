// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestProcessBackendTools_AnnotationsAndOutputSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		backendID        string
		tools            []vmcp.Tool
		workloadConfig   *config.WorkloadToolConfig
		wantCount        int
		wantNames        []string
		wantAnnotations  *vmcp.ToolAnnotations
		wantOutputSchema map[string]any
	}{
		{
			name:      "preserves Annotations and OutputSchema through overrides",
			backendID: "backend1",
			tools: []vmcp.Tool{
				{
					Name:        "tool1",
					Description: "Tool 1",
					InputSchema: map[string]any{"type": "object"},
					OutputSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"result": map[string]any{"type": "string"},
						},
					},
					Annotations: &vmcp.ToolAnnotations{
						ReadOnlyHint: boolPtr(true),
						Title:        "My Tool",
					},
					BackendID: "backend1",
				},
			},
			workloadConfig: &config.WorkloadToolConfig{
				Workload: "backend1",
				Overrides: map[string]*config.ToolOverride{
					"tool1": {Name: "renamed_tool1"},
				},
			},
			wantCount: 1,
			wantNames: []string{"renamed_tool1"},
			wantAnnotations: &vmcp.ToolAnnotations{
				ReadOnlyHint: boolPtr(true),
				Title:        "My Tool",
			},
			wantOutputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"result": map[string]any{"type": "string"},
				},
			},
		},
		{
			name:      "preserves Annotations without overrides",
			backendID: "backend1",
			tools: []vmcp.Tool{
				newTestToolWithAnnotations("annotated_tool", "backend1", &vmcp.ToolAnnotations{
					Title:           "Annotated",
					DestructiveHint: boolPtr(true),
					IdempotentHint:  boolPtr(false),
				}),
			},
			workloadConfig: nil,
			wantCount:      1,
			wantNames:      []string{"annotated_tool"},
			wantAnnotations: &vmcp.ToolAnnotations{
				Title:           "Annotated",
				DestructiveHint: boolPtr(true),
				IdempotentHint:  boolPtr(false),
			},
			wantOutputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"result": map[string]any{"type": "string"},
				},
			},
		},
		{
			name:      "nil Annotations preserved as nil",
			backendID: "backend1",
			tools: []vmcp.Tool{
				{
					Name:        "simple_tool",
					Description: "Simple tool",
					InputSchema: map[string]any{"type": "object"},
					BackendID:   "backend1",
				},
			},
			workloadConfig:   nil,
			wantCount:        1,
			wantNames:        []string{"simple_tool"},
			wantAnnotations:  nil,
			wantOutputSchema: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := processBackendTools(context.Background(), tt.backendID, tt.tools, tt.workloadConfig)

			require.Len(t, result, tt.wantCount)

			// Check expected tool names are present
			resultNames := make(map[string]bool)
			for _, tool := range result {
				resultNames[tool.Name] = true
			}
			for _, wantName := range tt.wantNames {
				assert.True(t, resultNames[wantName], "expected tool %q not found in results", wantName)
			}

			// Verify Annotations and OutputSchema on the first result tool
			if tt.wantCount > 0 {
				tool := result[0]

				if tt.wantAnnotations == nil {
					assert.Nil(t, tool.Annotations, "expected nil Annotations")
				} else {
					require.NotNil(t, tool.Annotations, "expected non-nil Annotations")
					assert.Equal(t, tt.wantAnnotations.Title, tool.Annotations.Title)
					assert.Equal(t, tt.wantAnnotations.ReadOnlyHint, tool.Annotations.ReadOnlyHint)
					assert.Equal(t, tt.wantAnnotations.DestructiveHint, tool.Annotations.DestructiveHint)
					assert.Equal(t, tt.wantAnnotations.IdempotentHint, tool.Annotations.IdempotentHint)
					assert.Equal(t, tt.wantAnnotations.OpenWorldHint, tool.Annotations.OpenWorldHint)
				}

				if tt.wantOutputSchema == nil {
					assert.Nil(t, tool.OutputSchema, "expected nil OutputSchema")
				} else {
					require.NotNil(t, tool.OutputSchema, "expected non-nil OutputSchema")
					assert.Equal(t, tt.wantOutputSchema["type"], tool.OutputSchema["type"])
				}
			}
		})
	}
}
