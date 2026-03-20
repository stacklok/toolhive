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
				newTestToolWithAnnotations("annotated_tool", &vmcp.ToolAnnotations{
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
		{
			name:      "annotation override applies title while preserving other annotations",
			backendID: "backend1",
			tools: []vmcp.Tool{
				newTestToolWithAnnotations("tool1", &vmcp.ToolAnnotations{
					Title:        "Original Title",
					ReadOnlyHint: boolPtr(true),
				}),
			},
			workloadConfig: &config.WorkloadToolConfig{
				Workload: "backend1",
				Overrides: map[string]*config.ToolOverride{
					"tool1": {
						Name: "tool1_renamed",
						Annotations: &config.ToolAnnotationsOverride{
							Title: stringPtr("Overridden Title"),
						},
					},
				},
			},
			wantCount: 1,
			wantNames: []string{"tool1_renamed"},
			wantAnnotations: &vmcp.ToolAnnotations{
				Title:        "Overridden Title",
				ReadOnlyHint: boolPtr(true),
			},
			wantOutputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"result": map[string]any{"type": "string"},
				},
			},
		},
		{
			name:      "annotation override applies bool hint correctly",
			backendID: "backend1",
			tools: []vmcp.Tool{
				newTestToolWithAnnotations("tool1", &vmcp.ToolAnnotations{
					Title:           "My Tool",
					ReadOnlyHint:    boolPtr(true),
					DestructiveHint: boolPtr(false),
				}),
			},
			workloadConfig: &config.WorkloadToolConfig{
				Workload: "backend1",
				Overrides: map[string]*config.ToolOverride{
					"tool1": {
						Description: "Updated desc",
						Annotations: &config.ToolAnnotationsOverride{
							ReadOnlyHint: boolPtr(false),
						},
					},
				},
			},
			wantCount: 1,
			wantNames: []string{"tool1"},
			wantAnnotations: &vmcp.ToolAnnotations{
				Title:           "My Tool",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
			},
			wantOutputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"result": map[string]any{"type": "string"},
				},
			},
		},
		{
			name:      "nil base annotations with override creates new annotations",
			backendID: "backend1",
			tools: []vmcp.Tool{
				{
					Name:        "tool1",
					Description: "A tool",
					InputSchema: map[string]any{"type": "object"},
					BackendID:   "backend1",
				},
			},
			workloadConfig: &config.WorkloadToolConfig{
				Workload: "backend1",
				Overrides: map[string]*config.ToolOverride{
					"tool1": {
						Name: "tool1_new",
						Annotations: &config.ToolAnnotationsOverride{
							Title:        stringPtr("New Title"),
							ReadOnlyHint: boolPtr(true),
						},
					},
				},
			},
			wantCount: 1,
			wantNames: []string{"tool1_new"},
			wantAnnotations: &vmcp.ToolAnnotations{
				Title:        "New Title",
				ReadOnlyHint: boolPtr(true),
			},
			wantOutputSchema: nil,
		},
		{
			name:      "nil annotation override leaves annotations unchanged",
			backendID: "backend1",
			tools: []vmcp.Tool{
				newTestToolWithAnnotations("tool1", &vmcp.ToolAnnotations{
					Title:        "Keep Me",
					ReadOnlyHint: boolPtr(true),
				}),
			},
			workloadConfig: &config.WorkloadToolConfig{
				Workload: "backend1",
				Overrides: map[string]*config.ToolOverride{
					"tool1": {
						Name:        "renamed_tool1",
						Annotations: nil,
					},
				},
			},
			wantCount: 1,
			wantNames: []string{"renamed_tool1"},
			wantAnnotations: &vmcp.ToolAnnotations{
				Title:        "Keep Me",
				ReadOnlyHint: boolPtr(true),
			},
			wantOutputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"result": map[string]any{"type": "string"},
				},
			},
		},
		{
			name:      "title cleared to empty string via override",
			backendID: "backend1",
			tools: []vmcp.Tool{
				newTestToolWithAnnotations("tool1", &vmcp.ToolAnnotations{
					Title:        "Original Title",
					ReadOnlyHint: boolPtr(true),
				}),
			},
			workloadConfig: &config.WorkloadToolConfig{
				Workload: "backend1",
				Overrides: map[string]*config.ToolOverride{
					"tool1": {
						Name: "tool1_cleared",
						Annotations: &config.ToolAnnotationsOverride{
							Title: stringPtr(""),
						},
					},
				},
			},
			wantCount: 1,
			wantNames: []string{"tool1_cleared"},
			wantAnnotations: &vmcp.ToolAnnotations{
				Title:        "",
				ReadOnlyHint: boolPtr(true),
			},
			wantOutputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"result": map[string]any{"type": "string"},
				},
			},
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

func TestApplyAnnotationOverrides(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		base      *vmcp.ToolAnnotations
		overrides *config.ToolAnnotationsOverride
		want      *vmcp.ToolAnnotations
	}{
		{
			name: "nil overrides returns base unchanged",
			base: &vmcp.ToolAnnotations{
				Title:        "Original",
				ReadOnlyHint: boolPtr(true),
			},
			overrides: nil,
			want: &vmcp.ToolAnnotations{
				Title:        "Original",
				ReadOnlyHint: boolPtr(true),
			},
		},
		{
			name: "nil base with non-nil overrides creates new annotations",
			base: nil,
			overrides: &config.ToolAnnotationsOverride{
				Title:        stringPtr("Brand New"),
				ReadOnlyHint: boolPtr(false),
			},
			want: &vmcp.ToolAnnotations{
				Title:        "Brand New",
				ReadOnlyHint: boolPtr(false),
			},
		},
		{
			name: "title override only preserves other fields",
			base: &vmcp.ToolAnnotations{
				Title:           "Old Title",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			},
			overrides: &config.ToolAnnotationsOverride{
				Title: stringPtr("New Title"),
			},
			want: &vmcp.ToolAnnotations{
				Title:           "New Title",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			},
		},
		{
			name: "readOnlyHint override only",
			base: &vmcp.ToolAnnotations{
				Title:        "Keep",
				ReadOnlyHint: boolPtr(true),
			},
			overrides: &config.ToolAnnotationsOverride{
				ReadOnlyHint: boolPtr(false),
			},
			want: &vmcp.ToolAnnotations{
				Title:        "Keep",
				ReadOnlyHint: boolPtr(false),
			},
		},
		{
			name: "multiple fields overridden simultaneously",
			base: &vmcp.ToolAnnotations{
				Title:           "Original",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
			},
			overrides: &config.ToolAnnotationsOverride{
				Title:          stringPtr("Updated"),
				ReadOnlyHint:   boolPtr(false),
				OpenWorldHint:  boolPtr(true),
				IdempotentHint: boolPtr(true),
			},
			want: &vmcp.ToolAnnotations{
				Title:           "Updated",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(true),
			},
		},
		{
			name: "title set to empty string clears it",
			base: &vmcp.ToolAnnotations{
				Title:        "Has Title",
				ReadOnlyHint: boolPtr(true),
			},
			overrides: &config.ToolAnnotationsOverride{
				Title: stringPtr(""),
			},
			want: &vmcp.ToolAnnotations{
				Title:        "",
				ReadOnlyHint: boolPtr(true),
			},
		},
		{
			name: "bool hints set to false explicitly",
			base: &vmcp.ToolAnnotations{
				Title:           "Tool",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(true),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(true),
			},
			overrides: &config.ToolAnnotationsOverride{
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			},
			want: &vmcp.ToolAnnotations{
				Title:           "Tool",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			},
		},
		{
			name:      "nil base and nil overrides returns base",
			base:      nil,
			overrides: nil,
			want:      nil,
		},
		{
			name:      "nil base with empty overrides returns zero-value annotations",
			base:      nil,
			overrides: &config.ToolAnnotationsOverride{},
			want:      &vmcp.ToolAnnotations{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := applyAnnotationOverrides(tt.base, tt.overrides)

			if tt.want == nil {
				assert.Nil(t, got)
				return
			}

			require.NotNil(t, got)
			assert.Equal(t, tt.want.Title, got.Title)
			assert.Equal(t, tt.want.ReadOnlyHint, got.ReadOnlyHint)
			assert.Equal(t, tt.want.DestructiveHint, got.DestructiveHint)
			assert.Equal(t, tt.want.IdempotentHint, got.IdempotentHint)
			assert.Equal(t, tt.want.OpenWorldHint, got.OpenWorldHint)
		})
	}
}

func TestApplyAnnotationOverrides_DoesNotMutateInput(t *testing.T) {
	t.Parallel()

	base := &vmcp.ToolAnnotations{
		Title:        "Original",
		ReadOnlyHint: boolPtr(true),
	}
	overrides := &config.ToolAnnotationsOverride{
		Title: stringPtr("Changed"),
	}

	got := applyAnnotationOverrides(base, overrides)

	// The returned value should have the override applied
	assert.Equal(t, "Changed", got.Title)

	// The original base should not be mutated
	assert.Equal(t, "Original", base.Title)
	assert.Equal(t, boolPtr(true), base.ReadOnlyHint)
}
