// Package vmcpconfig provides conversion logic from VirtualMCPServer CRD to vmcp Config
package vmcpconfig

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/oidc"
	oidcmocks "github.com/stacklok/toolhive/cmd/thv-operator/pkg/oidc/mocks"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// Compile-time interface assertion to ensure VirtualMCPServer implements OIDCConfigurable.
// This catches interface drift at compile time rather than runtime.
// Placed here because api/v1alpha1 cannot import pkg/oidc (circular dependency).
var _ oidc.OIDCConfigurable = (*mcpv1alpha1.VirtualMCPServer)(nil)

// newNoOpMockResolver creates a mock resolver that returns (nil, nil) for all calls.
// Use this in tests that don't care about OIDC configuration.
func newNoOpMockResolver(t *testing.T) *oidcmocks.MockResolver {
	t.Helper()
	ctrl := gomock.NewController(t)
	mockResolver := oidcmocks.NewMockResolver(ctrl)
	mockResolver.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	return mockResolver
}

// newTestConverter creates a Converter with the given resolver, failing the test if creation fails.
func newTestConverter(t *testing.T, resolver oidc.Resolver) *Converter {
	t.Helper()
	converter, err := NewConverter(resolver)
	require.NoError(t, err)
	return converter
}

// newTestVMCPServer creates a VirtualMCPServer with OIDC config for testing.
func newTestVMCPServer(oidcConfig *mcpv1alpha1.OIDCConfigRef) *mcpv1alpha1.VirtualMCPServer {
	return &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef:     mcpv1alpha1.GroupRef{Name: "test-group"},
			IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{Type: "oidc", OIDCConfig: oidcConfig},
		},
	}
}

func TestConverter_OIDCResolution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		oidcConfig *mcpv1alpha1.OIDCConfigRef
		mockReturn *oidc.OIDCConfig
		mockErr    error
		validate   func(t *testing.T, config *vmcpconfig.Config, err error)
	}{
		{
			name:       "successful resolution maps all fields",
			oidcConfig: &mcpv1alpha1.OIDCConfigRef{Type: mcpv1alpha1.OIDCConfigTypeKubernetes},
			mockReturn: &oidc.OIDCConfig{
				Issuer: "https://issuer.example.com", Audience: "my-audience",
				ResourceURL: "https://resource.example.com", JWKSAllowPrivateIP: true,
			},
			validate: func(t *testing.T, config *vmcpconfig.Config, err error) {
				t.Helper()
				require.NoError(t, err)
				require.NotNil(t, config.IncomingAuth.OIDC)
				assert.Equal(t, "https://issuer.example.com", config.IncomingAuth.OIDC.Issuer)
				assert.Equal(t, "my-audience", config.IncomingAuth.OIDC.Audience)
				assert.Equal(t, "https://resource.example.com", config.IncomingAuth.OIDC.Resource)
				assert.True(t, config.IncomingAuth.OIDC.ProtectedResourceAllowPrivateIP)
			},
		},
		{
			name:       "resolution error returns error (fail-closed)",
			oidcConfig: &mcpv1alpha1.OIDCConfigRef{Type: mcpv1alpha1.OIDCConfigTypeConfigMap},
			mockErr:    errors.New("configmap not found"),
			validate: func(t *testing.T, _ *vmcpconfig.Config, err error) {
				t.Helper()
				require.Error(t, err)
				assert.Contains(t, err.Error(), "OIDC resolution failed")
			},
		},
		{
			name:       "nil resolved config results in nil OIDC",
			oidcConfig: &mcpv1alpha1.OIDCConfigRef{Type: mcpv1alpha1.OIDCConfigTypeInline},
			mockReturn: nil,
			validate: func(t *testing.T, config *vmcpconfig.Config, err error) {
				t.Helper()
				require.NoError(t, err)
				assert.Nil(t, config.IncomingAuth.OIDC)
			},
		},
		{
			name: "inline with client secret sets ClientSecretEnv",
			oidcConfig: &mcpv1alpha1.OIDCConfigRef{
				Type:   mcpv1alpha1.OIDCConfigTypeInline,
				Inline: &mcpv1alpha1.InlineOIDCConfig{ClientSecret: "secret"},
			},
			mockReturn: &oidc.OIDCConfig{Issuer: "https://issuer.example.com"},
			validate: func(t *testing.T, config *vmcpconfig.Config, err error) {
				t.Helper()
				require.NoError(t, err)
				assert.Equal(t, "VMCP_OIDC_CLIENT_SECRET", config.IncomingAuth.OIDC.ClientSecretEnv)
			},
		},
		{
			name: "configmap with client secret sets ClientSecretEnv",
			oidcConfig: &mcpv1alpha1.OIDCConfigRef{
				Type:      mcpv1alpha1.OIDCConfigTypeConfigMap,
				ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{Name: "config"},
			},
			mockReturn: &oidc.OIDCConfig{Issuer: "https://issuer.example.com", ClientSecret: "secret"},
			validate: func(t *testing.T, config *vmcpconfig.Config, err error) {
				t.Helper()
				require.NoError(t, err)
				assert.Equal(t, "VMCP_OIDC_CLIENT_SECRET", config.IncomingAuth.OIDC.ClientSecretEnv)
			},
		},
		{
			name:       "kubernetes type does not set ClientSecretEnv",
			oidcConfig: &mcpv1alpha1.OIDCConfigRef{Type: mcpv1alpha1.OIDCConfigTypeKubernetes},
			mockReturn: &oidc.OIDCConfig{Issuer: "https://kubernetes.default.svc"},
			validate: func(t *testing.T, config *vmcpconfig.Config, err error) {
				t.Helper()
				require.NoError(t, err)
				assert.Empty(t, config.IncomingAuth.OIDC.ClientSecretEnv)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockResolver := oidcmocks.NewMockResolver(ctrl)
			mockResolver.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(tt.mockReturn, tt.mockErr)

			converter := newTestConverter(t, mockResolver)
			ctx := log.IntoContext(context.Background(), logr.Discard())
			config, err := converter.Convert(ctx, newTestVMCPServer(tt.oidcConfig))

			tt.validate(t, config, err)
		})
	}
}

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

			converter := newTestConverter(t, newNoOpMockResolver(t))
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

			converter := newTestConverter(t, newNoOpMockResolver(t))
			ctx := log.IntoContext(context.Background(), logr.Discard())

			result := converter.convertCompositeTools(ctx, vmcpServer)

			require.Len(t, result, 1)
			assert.Equal(t, tt.expectedTimeout, int64(result[0].Timeout), tt.description)
		})
	}
}

func TestConverter_ConvertCompositeTools_ErrorHandling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		errorHandling  *mcpv1alpha1.ErrorHandling
		expectedAction string
		expectedRetry  int
		expectedDelay  vmcpconfig.Duration
	}{
		{
			name: "with retry delay",
			errorHandling: &mcpv1alpha1.ErrorHandling{
				Action:     mcpv1alpha1.ErrorActionRetry,
				MaxRetries: 3,
				RetryDelay: "5s",
			},
			expectedAction: mcpv1alpha1.ErrorActionRetry,
			expectedRetry:  3,
			expectedDelay:  vmcpconfig.Duration(5 * time.Second),
		},
		{
			name: "with millisecond retry delay",
			errorHandling: &mcpv1alpha1.ErrorHandling{
				Action:     mcpv1alpha1.ErrorActionRetry,
				MaxRetries: 5,
				RetryDelay: "500ms",
			},
			expectedAction: mcpv1alpha1.ErrorActionRetry,
			expectedRetry:  5,
			expectedDelay:  vmcpconfig.Duration(500 * time.Millisecond),
		},
		{
			name: "with minute retry delay",
			errorHandling: &mcpv1alpha1.ErrorHandling{
				Action:     mcpv1alpha1.ErrorActionRetry,
				MaxRetries: 2,
				RetryDelay: "1m",
			},
			expectedAction: mcpv1alpha1.ErrorActionRetry,
			expectedRetry:  2,
			expectedDelay:  vmcpconfig.Duration(1 * time.Minute),
		},
		{
			name: "without retry delay",
			errorHandling: &mcpv1alpha1.ErrorHandling{
				Action:     mcpv1alpha1.ErrorActionRetry,
				MaxRetries: 3,
			},
			expectedAction: mcpv1alpha1.ErrorActionRetry,
			expectedRetry:  3,
			expectedDelay:  vmcpconfig.Duration(0),
		},
		{
			name: "abort action",
			errorHandling: &mcpv1alpha1.ErrorHandling{
				Action: mcpv1alpha1.ErrorActionAbort,
			},
			expectedAction: mcpv1alpha1.ErrorActionAbort,
			expectedRetry:  0,
			expectedDelay:  vmcpconfig.Duration(0),
		},
		{
			name: "continue action",
			errorHandling: &mcpv1alpha1.ErrorHandling{
				Action: mcpv1alpha1.ErrorActionContinue,
			},
			expectedAction: mcpv1alpha1.ErrorActionContinue,
			expectedRetry:  0,
			expectedDelay:  vmcpconfig.Duration(0),
		},
		{
			name: "invalid retry delay format is ignored",
			errorHandling: &mcpv1alpha1.ErrorHandling{
				Action:     mcpv1alpha1.ErrorActionRetry,
				MaxRetries: 3,
				RetryDelay: "invalid",
			},
			expectedAction: mcpv1alpha1.ErrorActionRetry,
			expectedRetry:  3,
			expectedDelay:  vmcpconfig.Duration(0),
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
							Steps: []mcpv1alpha1.WorkflowStep{
								{
									ID:      "step1",
									Type:    "tool",
									Tool:    "backend/some-tool",
									OnError: tt.errorHandling,
								},
							},
						},
					},
				},
			}

			converter := newTestConverter(t, newNoOpMockResolver(t))
			ctx := log.IntoContext(context.Background(), logr.Discard())
			config, err := converter.Convert(ctx, vmcpServer)

			require.NoError(t, err)
			require.NotNil(t, config)
			require.Len(t, config.CompositeTools, 1)
			require.Len(t, config.CompositeTools[0].Steps, 1)

			step := config.CompositeTools[0].Steps[0]
			if tt.errorHandling != nil {
				require.NotNil(t, step.OnError)
				assert.Equal(t, tt.expectedAction, step.OnError.Action)
				assert.Equal(t, tt.expectedRetry, step.OnError.RetryCount)
				assert.Equal(t, tt.expectedDelay, step.OnError.RetryDelay)
			} else {
				assert.Nil(t, step.OnError)
			}
		})
	}
}

func TestConverter_ConvertCompositeTools_NoErrorHandling(t *testing.T) {
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
					Steps: []mcpv1alpha1.WorkflowStep{
						{
							ID:   "step1",
							Type: "tool",
							Tool: "backend/some-tool",
							// No OnError specified
						},
					},
				},
			},
		},
	}

	converter := newTestConverter(t, newNoOpMockResolver(t))
	ctx := log.IntoContext(context.Background(), logr.Discard())
	config, err := converter.Convert(ctx, vmcpServer)

	require.NoError(t, err)
	require.NotNil(t, config)
	require.Len(t, config.CompositeTools, 1)
	require.Len(t, config.CompositeTools[0].Steps, 1)

	step := config.CompositeTools[0].Steps[0]
	assert.Nil(t, step.OnError)
}

func TestConverter_ConvertCompositeTools_StepTimeout(t *testing.T) {
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
					Timeout:     "30s",
					Steps: []mcpv1alpha1.WorkflowStep{
						{
							ID:      "step1",
							Type:    "tool",
							Tool:    "backend/some-tool",
							Timeout: "10s",
						},
					},
				},
			},
		},
	}

	converter := newTestConverter(t, newNoOpMockResolver(t))
	ctx := log.IntoContext(context.Background(), logr.Discard())
	config, err := converter.Convert(ctx, vmcpServer)

	require.NoError(t, err)
	require.NotNil(t, config)
	require.Len(t, config.CompositeTools, 1)

	tool := config.CompositeTools[0]
	assert.Equal(t, vmcpconfig.Duration(30*time.Second), tool.Timeout)

	require.Len(t, tool.Steps, 1)
	assert.Equal(t, vmcpconfig.Duration(10*time.Second), tool.Steps[0].Timeout)
}

// validateOutputProperties is a recursive helper function to validate output properties at any nesting level
func validateOutputProperties(t *testing.T, expected, actual map[string]vmcpconfig.OutputProperty, path string) {
	t.Helper()

	for propName, expectedProp := range expected {
		fullPath := propName
		if path != "" {
			fullPath = path + "." + propName
		}

		actualProp, exists := actual[propName]
		require.True(t, exists, "Property %s should exist", fullPath)
		assert.Equal(t, expectedProp.Type, actualProp.Type, "Property %s type mismatch", fullPath)
		assert.Equal(t, expectedProp.Description, actualProp.Description, "Property %s description mismatch", fullPath)
		assert.Equal(t, expectedProp.Value, actualProp.Value, "Property %s value mismatch", fullPath)
		assert.Equal(t, expectedProp.Default, actualProp.Default, "Property %s default mismatch", fullPath)

		// Recursively validate nested properties
		if len(expectedProp.Properties) > 0 {
			assert.Equal(t, len(expectedProp.Properties), len(actualProp.Properties),
				"Property %s nested properties count mismatch", fullPath)
			validateOutputProperties(t, expectedProp.Properties, actualProp.Properties, fullPath)
		}
	}
}

func TestConverter_ConvertCompositeTools_OutputSpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		outputSpec     *mcpv1alpha1.OutputSpec
		expectedOutput *vmcpconfig.OutputConfig
		description    string
	}{
		{
			name:           "nil output spec",
			outputSpec:     nil,
			expectedOutput: nil,
			description:    "Should handle nil output spec",
		},
		{
			name: "simple output with string property",
			outputSpec: &mcpv1alpha1.OutputSpec{
				Properties: map[string]mcpv1alpha1.OutputPropertySpec{
					"result": {
						Type:        "string",
						Description: "The result of the workflow",
						Value:       "{{.steps.step1.output.data}}",
					},
				},
				Required: []string{"result"},
			},
			expectedOutput: &vmcpconfig.OutputConfig{
				Properties: map[string]vmcpconfig.OutputProperty{
					"result": {
						Type:        "string",
						Description: "The result of the workflow",
						Value:       "{{.steps.step1.output.data}}",
					},
				},
				Required: []string{"result"},
			},
			description: "Should correctly convert simple output spec",
		},
		{
			name: "output with multiple properties and scalar defaults",
			outputSpec: &mcpv1alpha1.OutputSpec{
				Properties: map[string]mcpv1alpha1.OutputPropertySpec{
					"status": {
						Type:        "string",
						Description: "Status of the operation",
						Value:       "{{.steps.step1.output.status}}",
						Default:     &runtime.RawExtension{Raw: []byte(`"pending"`)},
					},
					"count": {
						Type:        "integer",
						Description: "Number of items processed",
						Value:       "{{.steps.step1.output.count}}",
						Default:     &runtime.RawExtension{Raw: []byte(`0`)},
					},
					"enabled": {
						Type:        "boolean",
						Description: "Whether the feature is enabled",
						Value:       "{{.steps.step1.output.enabled}}",
						Default:     &runtime.RawExtension{Raw: []byte(`true`)},
					},
				},
				Required: []string{"status"},
			},
			expectedOutput: &vmcpconfig.OutputConfig{
				Properties: map[string]vmcpconfig.OutputProperty{
					"status": {
						Type:        "string",
						Description: "Status of the operation",
						Value:       "{{.steps.step1.output.status}}",
						Default:     "pending",
					},
					"count": {
						Type:        "integer",
						Description: "Number of items processed",
						Value:       "{{.steps.step1.output.count}}",
						Default:     float64(0), // JSON numbers unmarshal as float64
					},
					"enabled": {
						Type:        "boolean",
						Description: "Whether the feature is enabled",
						Value:       "{{.steps.step1.output.enabled}}",
						Default:     true,
					},
				},
				Required: []string{"status"},
			},
			description: "Should correctly convert output spec with scalar defaults",
		},
		{
			name: "output with object-typed default value",
			outputSpec: &mcpv1alpha1.OutputSpec{
				Properties: map[string]mcpv1alpha1.OutputPropertySpec{
					"config": {
						Type:        "object",
						Description: "Configuration object",
						Value:       "{{.steps.step1.output.config}}",
						Default:     &runtime.RawExtension{Raw: []byte(`{"timeout": 30, "retries": 3, "enabled": true}`)},
					},
					"tags": {
						Type:        "array",
						Description: "List of tags",
						Value:       "{{.steps.step1.output.tags}}",
						Default:     &runtime.RawExtension{Raw: []byte(`["default", "prod"]`)},
					},
				},
			},
			expectedOutput: &vmcpconfig.OutputConfig{
				Properties: map[string]vmcpconfig.OutputProperty{
					"config": {
						Type:        "object",
						Description: "Configuration object",
						Value:       "{{.steps.step1.output.config}}",
						Default: map[string]any{
							"timeout": float64(30),
							"retries": float64(3),
							"enabled": true,
						},
					},
					"tags": {
						Type:        "array",
						Description: "List of tags",
						Value:       "{{.steps.step1.output.tags}}",
						Default:     []any{"default", "prod"},
					},
				},
			},
			description: "Should correctly convert output spec with object and array default values",
		},
		{
			name: "output with nested object properties",
			outputSpec: &mcpv1alpha1.OutputSpec{
				Properties: map[string]mcpv1alpha1.OutputPropertySpec{
					"metadata": {
						Type:        "object",
						Description: "Metadata about the result",
						Properties: map[string]mcpv1alpha1.OutputPropertySpec{
							"timestamp": {
								Type:        "string",
								Description: "When the result was generated",
								Value:       "{{.steps.step1.output.timestamp}}",
							},
							"version": {
								Type:        "integer",
								Description: "Version of the result format",
								Value:       "{{.steps.step1.output.version}}",
								Default:     &runtime.RawExtension{Raw: []byte(`1`)},
							},
						},
					},
				},
			},
			expectedOutput: &vmcpconfig.OutputConfig{
				Properties: map[string]vmcpconfig.OutputProperty{
					"metadata": {
						Type:        "object",
						Description: "Metadata about the result",
						Properties: map[string]vmcpconfig.OutputProperty{
							"timestamp": {
								Type:        "string",
								Description: "When the result was generated",
								Value:       "{{.steps.step1.output.timestamp}}",
							},
							"version": {
								Type:        "integer",
								Description: "Version of the result format",
								Value:       "{{.steps.step1.output.version}}",
								Default:     float64(1),
							},
						},
					},
				},
			},
			description: "Should correctly convert output spec with nested objects",
		},
		{
			name: "output with deeply nested object properties (3+ levels)",
			outputSpec: &mcpv1alpha1.OutputSpec{
				Properties: map[string]mcpv1alpha1.OutputPropertySpec{
					"response": {
						Type:        "object",
						Description: "Top level response object",
						Properties: map[string]mcpv1alpha1.OutputPropertySpec{
							"data": {
								Type:        "object",
								Description: "Second level data object",
								Properties: map[string]mcpv1alpha1.OutputPropertySpec{
									"result": {
										Type:        "object",
										Description: "Third level result object",
										Properties: map[string]mcpv1alpha1.OutputPropertySpec{
											"value": {
												Type:        "string",
												Description: "Fourth level actual value",
												Value:       "{{.steps.step1.output.deep.value}}",
												Default:     &runtime.RawExtension{Raw: []byte(`"default_value"`)},
											},
											"count": {
												Type:        "integer",
												Description: "Fourth level count",
												Value:       "{{.steps.step1.output.deep.count}}",
												Default:     &runtime.RawExtension{Raw: []byte(`0`)},
											},
										},
									},
									"metadata": {
										Type:        "object",
										Description: "Third level metadata",
										Properties: map[string]mcpv1alpha1.OutputPropertySpec{
											"timestamp": {
												Type:        "string",
												Description: "Timestamp of operation",
												Value:       "{{.steps.step1.output.timestamp}}",
											},
										},
									},
								},
							},
							"status": {
								Type:        "string",
								Description: "Second level status",
								Value:       "{{.steps.step1.output.status}}",
								Default:     &runtime.RawExtension{Raw: []byte(`"success"`)},
							},
						},
					},
				},
			},
			expectedOutput: &vmcpconfig.OutputConfig{
				Properties: map[string]vmcpconfig.OutputProperty{
					"response": {
						Type:        "object",
						Description: "Top level response object",
						Properties: map[string]vmcpconfig.OutputProperty{
							"data": {
								Type:        "object",
								Description: "Second level data object",
								Properties: map[string]vmcpconfig.OutputProperty{
									"result": {
										Type:        "object",
										Description: "Third level result object",
										Properties: map[string]vmcpconfig.OutputProperty{
											"value": {
												Type:        "string",
												Description: "Fourth level actual value",
												Value:       "{{.steps.step1.output.deep.value}}",
												Default:     "default_value",
											},
											"count": {
												Type:        "integer",
												Description: "Fourth level count",
												Value:       "{{.steps.step1.output.deep.count}}",
												Default:     float64(0),
											},
										},
									},
									"metadata": {
										Type:        "object",
										Description: "Third level metadata",
										Properties: map[string]vmcpconfig.OutputProperty{
											"timestamp": {
												Type:        "string",
												Description: "Timestamp of operation",
												Value:       "{{.steps.step1.output.timestamp}}",
											},
										},
									},
								},
							},
							"status": {
								Type:        "string",
								Description: "Second level status",
								Value:       "{{.steps.step1.output.status}}",
								Default:     "success",
							},
						},
					},
				},
			},
			description: "Should correctly convert output spec with deeply nested objects (4 levels)",
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
							Steps: []mcpv1alpha1.WorkflowStep{
								{
									ID:   "step1",
									Type: "tool",
									Tool: "backend/some-tool",
								},
							},
							Output: tt.outputSpec,
						},
					},
				},
			}

			converter := newTestConverter(t, newNoOpMockResolver(t))
			ctx := log.IntoContext(context.Background(), logr.Discard())
			config, err := converter.Convert(ctx, vmcpServer)

			require.NoError(t, err)
			require.NotNil(t, config)
			require.Len(t, config.CompositeTools, 1)

			tool := config.CompositeTools[0]
			if tt.expectedOutput == nil {
				assert.Nil(t, tool.Output, tt.description)
			} else {
				require.NotNil(t, tool.Output, tt.description)
				assert.Equal(t, tt.expectedOutput.Required, tool.Output.Required, tt.description)
				assert.Equal(t, len(tt.expectedOutput.Properties), len(tool.Output.Properties), tt.description)

				// Use recursive helper to validate all nested levels
				validateOutputProperties(t, tt.expectedOutput.Properties, tool.Output.Properties, "")
			}
		})
	}
}

func TestConverter_IncomingAuthRequired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		incomingAuth       *mcpv1alpha1.IncomingAuthConfig
		expectedAuthType   string
		expectedOIDCConfig *vmcpconfig.OIDCConfig
		expectNilAuth      bool
		description        string
	}{
		{
			name:          "nil incomingAuth results in nil config",
			incomingAuth:  nil,
			expectNilAuth: true,
			description:   "Should return nil IncomingAuth when not specified - CRD validation will reject this",
		},
		{
			name: "explicit anonymous auth",
			incomingAuth: &mcpv1alpha1.IncomingAuthConfig{
				Type: "anonymous",
			},
			expectedAuthType: "anonymous",
			description:      "Should use anonymous auth when explicitly specified",
		},
		{
			name: "explicit oidc auth with inline config",
			incomingAuth: &mcpv1alpha1.IncomingAuthConfig{
				Type: "oidc",
				OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
					Type: "inline",
					Inline: &mcpv1alpha1.InlineOIDCConfig{
						Issuer:   "https://example.com",
						ClientID: "test-client",
						Audience: "test-audience",
					},
				},
			},
			expectedAuthType: "oidc",
			expectedOIDCConfig: &vmcpconfig.OIDCConfig{
				Issuer:   "https://example.com",
				ClientID: "test-client",
				Audience: "test-audience",
			},
			description: "Should correctly convert OIDC auth config",
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
					GroupRef:     mcpv1alpha1.GroupRef{Name: "test-group"},
					IncomingAuth: tt.incomingAuth,
				},
			}

			// Set up mock resolver based on test expectations
			ctrl := gomock.NewController(t)
			mockResolver := oidcmocks.NewMockResolver(ctrl)

			// Configure mock to return expected OIDC config
			if tt.expectedOIDCConfig != nil {
				mockResolver.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(&oidc.OIDCConfig{
					Issuer:   tt.expectedOIDCConfig.Issuer,
					ClientID: tt.expectedOIDCConfig.ClientID,
					Audience: tt.expectedOIDCConfig.Audience,
				}, nil)
			} else {
				mockResolver.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
			}

			converter := newTestConverter(t, mockResolver)
			ctx := log.IntoContext(context.Background(), logr.Discard())
			config, err := converter.Convert(ctx, vmcpServer)

			require.NoError(t, err, tt.description)
			require.NotNil(t, config, tt.description)

			if tt.expectNilAuth {
				assert.Nil(t, config.IncomingAuth, tt.description)
			} else {
				require.NotNil(t, config.IncomingAuth, tt.description)
				assert.Equal(t, tt.expectedAuthType, config.IncomingAuth.Type, tt.description)

				if tt.expectedOIDCConfig != nil {
					require.NotNil(t, config.IncomingAuth.OIDC, tt.description)
					assert.Equal(t, tt.expectedOIDCConfig.Issuer, config.IncomingAuth.OIDC.Issuer, tt.description)
					assert.Equal(t, tt.expectedOIDCConfig.ClientID, config.IncomingAuth.OIDC.ClientID, tt.description)
					assert.Equal(t, tt.expectedOIDCConfig.Audience, config.IncomingAuth.OIDC.Audience, tt.description)
				} else {
					assert.Nil(t, config.IncomingAuth.OIDC, tt.description)
				}
			}
		})
	}
}
