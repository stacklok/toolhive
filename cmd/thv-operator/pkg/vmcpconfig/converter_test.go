package vmcpconfig

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestConverter_convertCompositeTools(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		compositeTools []mcpv1alpha1.CompositeToolSpec
		wantLen        int
		wantTool       *struct {
			name       string
			params     map[string]any
			stepsCount int
		}
	}{
		{
			name: "valid parameters as JSON Schema",
			compositeTools: []mcpv1alpha1.CompositeToolSpec{
				{
					Name:        "test-tool",
					Description: "A test tool",
					Parameters: &runtime.RawExtension{
						Raw: mustMarshalJSON(t, map[string]any{
							"type": "object",
							"properties": map[string]any{
								"param1": map[string]any{
									"type":    "string",
									"default": "value1",
								},
							},
							"required": []string{"param1"},
						}),
					},
					Steps: []mcpv1alpha1.WorkflowStep{
						{
							ID:   "step1",
							Type: "tool",
							Tool: "some-tool",
						},
					},
				},
			},
			wantLen: 1,
			wantTool: &struct {
				name       string
				params     map[string]any
				stepsCount int
			}{
				name: "test-tool",
				params: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"param1": map[string]any{
							"type":    "string",
							"default": "value1",
						},
					},
					"required": []any{"param1"},
				},
				stepsCount: 1,
			},
		},
		{
			name: "nil parameters",
			compositeTools: []mcpv1alpha1.CompositeToolSpec{
				{
					Name:        "test-tool-no-params",
					Description: "A test tool without params",
					Parameters:  nil,
					Steps: []mcpv1alpha1.WorkflowStep{
						{
							ID:   "step1",
							Type: "tool",
							Tool: "some-tool",
						},
					},
				},
			},
			wantLen: 1,
			wantTool: &struct {
				name       string
				params     map[string]any
				stepsCount int
			}{
				name:       "test-tool-no-params",
				params:     nil,
				stepsCount: 1,
			},
		},
		{
			name: "empty parameters",
			compositeTools: []mcpv1alpha1.CompositeToolSpec{
				{
					Name:        "test-tool-empty",
					Description: "A test tool with empty params",
					Parameters:  &runtime.RawExtension{Raw: []byte("{}")},
					Steps: []mcpv1alpha1.WorkflowStep{
						{
							ID:   "step1",
							Type: "tool",
							Tool: "some-tool",
						},
					},
				},
			},
			wantLen: 1,
			wantTool: &struct {
				name       string
				params     map[string]any
				stepsCount int
			}{
				name:       "test-tool-empty",
				params:     map[string]any{},
				stepsCount: 1,
			},
		},
		{
			name: "invalid JSON parameters - should be ignored (webhook validates before this)",
			compositeTools: []mcpv1alpha1.CompositeToolSpec{
				{
					Name:        "test-tool-invalid",
					Description: "A test tool with invalid JSON",
					Parameters:  &runtime.RawExtension{Raw: []byte("invalid json{")},
					Steps: []mcpv1alpha1.WorkflowStep{
						{
							ID:   "step1",
							Type: "tool",
							Tool: "some-tool",
						},
					},
				},
			},
			wantLen: 1,
			wantTool: &struct {
				name       string
				params     map[string]any
				stepsCount int
			}{
				name:       "test-tool-invalid",
				params:     nil, // Invalid JSON results in nil params
				stepsCount: 1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			converter := NewConverter()
			vmcp := &mcpv1alpha1.VirtualMCPServer{
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					CompositeTools: tt.compositeTools,
				},
			}

			result := converter.convertCompositeTools(context.Background(), vmcp)

			require.Len(t, result, tt.wantLen)
			if tt.wantTool != nil {
				assert.Equal(t, tt.wantTool.name, result[0].Name)
				assert.Equal(t, tt.wantTool.params, result[0].Parameters)
				assert.Len(t, result[0].Steps, tt.wantTool.stepsCount)
			}
		})
	}
}

func mustMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}
