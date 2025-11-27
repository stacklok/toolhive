package vmcpconfig

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestConvertCompositeTools_Parameters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		parameters      *runtime.RawExtension
		expectedParams  map[string]any
		expectNilParams bool
		description     string
	}{
		{
			name:       "valid JSON Schema parameters",
			parameters: &runtime.RawExtension{Raw: []byte(`{"type":"object","properties":{"name":{"type":"string"}}}`)},
			expectedParams: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type": "string",
					},
				},
			},
			expectNilParams: false,
			description:     "Should correctly parse valid JSON Schema parameters",
		},
		{
			name:            "nil parameters",
			parameters:      nil,
			expectedParams:  nil,
			expectNilParams: true,
			description:     "Should handle nil parameters",
		},
		{
			name:            "empty raw extension",
			parameters:      &runtime.RawExtension{Raw: []byte{}},
			expectedParams:  nil,
			expectNilParams: true,
			description:     "Should handle empty raw extension",
		},
		{
			name:            "invalid JSON - should be nil after error",
			parameters:      &runtime.RawExtension{Raw: []byte(`{invalid json}`)},
			expectedParams:  nil,
			expectNilParams: true,
			description:     "Should handle invalid JSON gracefully (log error, leave params nil)",
		},
		{
			name:       "complex parameters with required array",
			parameters: &runtime.RawExtension{Raw: []byte(`{"type":"object","properties":{"name":{"type":"string"},"age":{"type":"integer"}},"required":["name"]}`)},
			expectedParams: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
					"age":  map[string]any{"type": "integer"},
				},
				"required": []any{"name"},
			},
			expectNilParams: false,
			description:     "Should correctly parse complex JSON Schema with required array",
		},
		{
			// This test case explicitly verifies that description and default fields
			// at the property level are preserved, addressing issue #2775
			name: "parameters with description and default fields (issue #2775)",
			parameters: &runtime.RawExtension{Raw: []byte(`{
				"type": "object",
				"properties": {
					"environment": {
						"type": "string",
						"description": "Target deployment environment",
						"default": "staging"
					},
					"replicas": {
						"type": "integer",
						"description": "Number of pod replicas",
						"default": 3
					}
				},
				"required": ["environment"]
			}`)},
			expectedParams: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"environment": map[string]any{
						"type":        "string",
						"description": "Target deployment environment",
						"default":     "staging",
					},
					"replicas": map[string]any{
						"type":        "integer",
						"description": "Number of pod replicas",
						"default":     float64(3), // JSON numbers unmarshal as float64
					},
				},
				"required": []any{"environment"},
			},
			expectNilParams: false,
			description:     "Should preserve description and default fields per issue #2775",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a VirtualMCPServer with the test parameters
			vmcpServer := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{Name: "test-group"},
					CompositeTools: []mcpv1alpha1.CompositeToolSpec{
						{
							Name:        "test-tool",
							Description: "A test composite tool",
							Parameters:  tt.parameters,
							Steps: []mcpv1alpha1.WorkflowStep{
								{
									ID:   "step1",
									Type: "tool",
									Tool: "some-tool",
								},
							},
						},
					},
				},
			}

			// Create converter and context with logger
			converter := NewConverter()
			ctx := log.IntoContext(context.Background(), logr.Discard())

			// Convert
			result := converter.convertCompositeTools(ctx, vmcpServer)

			// Assertions
			require.Len(t, result, 1, "Should have one composite tool")

			if tt.expectNilParams {
				assert.Nil(t, result[0].Parameters, tt.description)
			} else {
				require.NotNil(t, result[0].Parameters, tt.description)
				assert.Equal(t, tt.expectedParams, result[0].Parameters, tt.description)
			}
		})
	}
}

func TestConvertCompositeTools_Timeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		timeout         string
		expectedTimeout int64 // in nanoseconds (Duration)
		description     string
	}{
		{
			name:            "valid timeout",
			timeout:         "5m",
			expectedTimeout: 5 * 60 * 1e9,
			description:     "Should correctly parse valid timeout",
		},
		{
			name:            "empty timeout",
			timeout:         "",
			expectedTimeout: 0,
			description:     "Should handle empty timeout",
		},
		{
			name:            "invalid timeout format - should default to zero",
			timeout:         "invalid",
			expectedTimeout: 0,
			description:     "Should handle invalid timeout format gracefully",
		},
		{
			name:            "timeout in seconds",
			timeout:         "30s",
			expectedTimeout: 30 * 1e9,
			description:     "Should correctly parse seconds timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcpServer := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{Name: "test-group"},
					CompositeTools: []mcpv1alpha1.CompositeToolSpec{
						{
							Name:        "test-tool",
							Description: "A test composite tool",
							Timeout:     tt.timeout,
							Steps: []mcpv1alpha1.WorkflowStep{
								{
									ID:   "step1",
									Type: "tool",
									Tool: "some-tool",
								},
							},
						},
					},
				},
			}

			converter := NewConverter()
			ctx := log.IntoContext(context.Background(), logr.Discard())

			result := converter.convertCompositeTools(ctx, vmcpServer)

			require.Len(t, result, 1)
			assert.Equal(t, tt.expectedTimeout, int64(result[0].Timeout), tt.description)
		})
	}
}
