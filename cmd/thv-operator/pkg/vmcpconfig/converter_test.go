// Package vmcpconfig provides conversion logic from VirtualMCPServer CRD to vmcp Config
package vmcpconfig

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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

// newTestK8sClient creates a fake Kubernetes client for testing.
func newTestK8sClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

// newTestConverter creates a Converter with the given resolver, failing the test if creation fails.
func newTestConverter(t *testing.T, resolver oidc.Resolver) *Converter {
	t.Helper()
	k8sClient := newTestK8sClient(t)
	converter, err := NewConverter(resolver, k8sClient)
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
			result, err := converter.convertCompositeTools(ctx, vmcpServer)
			require.NoError(t, err)

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
			name:            "invalid timeout format - should default to 30m",
			timeout:         "invalid",
			expectedTimeout: 30 * 60 * 1e9, // 30 minutes (default from CRD)
			description:     "Should handle invalid timeout format gracefully by using default 30m",
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

			result, err := converter.convertCompositeTools(ctx, vmcpServer)
			require.NoError(t, err)

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

func TestConverter_ConvertCompositeTools_ElicitationResponseHandlers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                    string
		onDecline               *mcpv1alpha1.ElicitationResponseHandler
		onCancel                *mcpv1alpha1.ElicitationResponseHandler
		expectedOnDeclineAction string
		expectedOnCancelAction  string
		expectOnDeclineNil      bool
		expectOnCancelNil       bool
	}{
		{
			name:                    "with OnDecline skip_remaining",
			onDecline:               &mcpv1alpha1.ElicitationResponseHandler{Action: "skip_remaining"},
			onCancel:                nil,
			expectedOnDeclineAction: "skip_remaining",
			expectOnDeclineNil:      false,
			expectOnCancelNil:       true,
		},
		{
			name:                    "with OnDecline abort",
			onDecline:               &mcpv1alpha1.ElicitationResponseHandler{Action: "abort"},
			onCancel:                nil,
			expectedOnDeclineAction: "abort",
			expectOnDeclineNil:      false,
			expectOnCancelNil:       true,
		},
		{
			name:                    "with OnDecline continue",
			onDecline:               &mcpv1alpha1.ElicitationResponseHandler{Action: "continue"},
			onCancel:                nil,
			expectedOnDeclineAction: "continue",
			expectOnDeclineNil:      false,
			expectOnCancelNil:       true,
		},
		{
			name:                   "with OnCancel skip_remaining",
			onDecline:              nil,
			onCancel:               &mcpv1alpha1.ElicitationResponseHandler{Action: "skip_remaining"},
			expectedOnCancelAction: "skip_remaining",
			expectOnDeclineNil:     true,
			expectOnCancelNil:      false,
		},
		{
			name:                   "with OnCancel abort",
			onDecline:              nil,
			onCancel:               &mcpv1alpha1.ElicitationResponseHandler{Action: "abort"},
			expectedOnCancelAction: "abort",
			expectOnDeclineNil:     true,
			expectOnCancelNil:      false,
		},
		{
			name:                   "with OnCancel continue",
			onDecline:              nil,
			onCancel:               &mcpv1alpha1.ElicitationResponseHandler{Action: "continue"},
			expectedOnCancelAction: "continue",
			expectOnDeclineNil:     true,
			expectOnCancelNil:      false,
		},
		{
			name:                    "with both OnDecline and OnCancel",
			onDecline:               &mcpv1alpha1.ElicitationResponseHandler{Action: "skip_remaining"},
			onCancel:                &mcpv1alpha1.ElicitationResponseHandler{Action: "abort"},
			expectedOnDeclineAction: "skip_remaining",
			expectedOnCancelAction:  "abort",
			expectOnDeclineNil:      false,
			expectOnCancelNil:       false,
		},
		{
			name:               "with neither OnDecline nor OnCancel",
			onDecline:          nil,
			onCancel:           nil,
			expectOnDeclineNil: true,
			expectOnCancelNil:  true,
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
									ID:        "step1",
									Type:      "elicitation",
									Message:   "Please provide input",
									OnDecline: tt.onDecline,
									OnCancel:  tt.onCancel,
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

			// Check OnDecline
			if tt.expectOnDeclineNil {
				assert.Nil(t, step.OnDecline)
			} else {
				require.NotNil(t, step.OnDecline)
				assert.Equal(t, tt.expectedOnDeclineAction, step.OnDecline.Action)
			}

			// Check OnCancel
			if tt.expectOnCancelNil {
				assert.Nil(t, step.OnCancel)
			} else {
				require.NotNil(t, step.OnCancel)
				assert.Equal(t, tt.expectedOnCancelAction, step.OnCancel.Action)
			}
		})
	}
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

func TestConvertCompositeTools_NonStringArguments(t *testing.T) {
	t.Parallel()

	// Test cases with valid JSON - use round-trip testing where input JSON is parsed
	// and compared with output to verify types are preserved correctly
	validJSONTests := []struct {
		name          string
		argumentsJSON string
		description   string
	}{
		{
			name:          "integer arguments",
			argumentsJSON: `{"max_results": 5, "query": "test"}`,
			description:   "Should correctly parse integer arguments (as float64)",
		},
		{
			name:          "boolean arguments",
			argumentsJSON: `{"enabled": true, "verbose": false}`,
			description:   "Should correctly parse boolean arguments",
		},
		{
			name:          "array arguments",
			argumentsJSON: `{"tags": ["tag1", "tag2"], "ids": [1, 2, 3]}`,
			description:   "Should correctly parse array arguments",
		},
		{
			name:          "object arguments",
			argumentsJSON: `{"config": {"timeout": 30, "retries": 3}}`,
			description:   "Should correctly parse nested object arguments",
		},
		{
			name:          "mixed types with template strings",
			argumentsJSON: `{"input": "{{ .params.message }}", "count": 10, "enabled": true}`,
			description:   "Should preserve template strings alongside non-string types",
		},
	}

	for _, tt := range validJSONTests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Parse the expected args from the input JSON (round-trip test)
			var expectedArgs map[string]any
			require.NoError(t, json.Unmarshal([]byte(tt.argumentsJSON), &expectedArgs),
				"Test setup: input JSON should be valid")

			arguments := &runtime.RawExtension{Raw: []byte(tt.argumentsJSON)}

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
									ID:        "step1",
									Type:      "tool",
									Tool:      "backend.some-tool",
									Arguments: arguments,
								},
							},
						},
					},
				},
			}

			converter := newTestConverter(t, newNoOpMockResolver(t))
			ctx := log.IntoContext(context.Background(), logr.Discard())

			result, err := converter.convertCompositeTools(ctx, vmcpServer)
			require.NoError(t, err, tt.description)
			require.Len(t, result, 1, "Should have one composite tool")
			require.Len(t, result[0].Steps, 1, "Should have one step")

			stepArgs := result[0].Steps[0].Arguments
			require.NotNil(t, stepArgs, tt.description)
			assert.Equal(t, expectedArgs, stepArgs, tt.description)
		})
	}

	t.Run("nil arguments returns empty map", func(t *testing.T) {
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
								ID:        "step1",
								Type:      "tool",
								Tool:      "backend.some-tool",
								Arguments: nil, // No arguments
							},
						},
					},
				},
			},
		}

		converter := newTestConverter(t, newNoOpMockResolver(t))
		ctx := log.IntoContext(context.Background(), logr.Discard())

		result, err := converter.convertCompositeTools(ctx, vmcpServer)
		require.NoError(t, err)
		require.Len(t, result, 1)
		require.Len(t, result[0].Steps, 1)

		stepArgs := result[0].Steps[0].Arguments
		assert.NotNil(t, stepArgs, "Should return empty map, not nil")
		assert.Empty(t, stepArgs, "Should be empty map")
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
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
								ID:        "step1",
								Type:      "tool",
								Tool:      "backend.some-tool",
								Arguments: &runtime.RawExtension{Raw: []byte(`{invalid json}`)},
							},
						},
					},
				},
			},
		}

		converter := newTestConverter(t, newNoOpMockResolver(t))
		ctx := log.IntoContext(context.Background(), logr.Discard())

		_, err := converter.convertCompositeTools(ctx, vmcpServer)
		require.Error(t, err, "Should return error for invalid JSON")
		assert.Contains(t, err.Error(), "failed to unmarshal arguments")
	})
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

// createTestScheme creates a test scheme with required types
func createTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(s)
	return s
}

func TestConverter_CompositeToolRefs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		vmcp          *mcpv1alpha1.VirtualMCPServer
		compositeDefs []*mcpv1alpha1.VirtualMCPCompositeToolDefinition
		k8sClient     client.Client
		expectError   bool
		errorContains string
		validate      func(t *testing.T, config *vmcpconfig.Config)
	}{
		{
			name: "successfully fetch and merge referenced composite tool",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{Name: "test-group"},
					CompositeToolRefs: []mcpv1alpha1.CompositeToolDefinitionRef{
						{Name: "referenced-tool"},
					},
				},
			},
			compositeDefs: []*mcpv1alpha1.VirtualMCPCompositeToolDefinition{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "referenced-tool",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
						Name:        "referenced-tool",
						Description: "A referenced composite tool",
						Steps: []mcpv1alpha1.WorkflowStep{
							{
								ID:   "step1",
								Type: "tool",
								Tool: "backend.tool1",
							},
						},
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, config *vmcpconfig.Config) {
				t.Helper()
				require.Len(t, config.CompositeTools, 1)
				assert.Equal(t, "referenced-tool", config.CompositeTools[0].Name)
				assert.Equal(t, "A referenced composite tool", config.CompositeTools[0].Description)
				require.Len(t, config.CompositeTools[0].Steps, 1)
				assert.Equal(t, "step1", config.CompositeTools[0].Steps[0].ID)
				assert.Equal(t, "backend.tool1", config.CompositeTools[0].Steps[0].Tool)
			},
		},
		{
			name: "merge inline and referenced composite tools",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{Name: "test-group"},
					CompositeTools: []mcpv1alpha1.CompositeToolSpec{
						{
							Name:        "inline-tool",
							Description: "An inline composite tool",
							Steps: []mcpv1alpha1.WorkflowStep{
								{
									ID:   "step1",
									Type: "tool",
									Tool: "backend.inline-tool",
								},
							},
						},
					},
					CompositeToolRefs: []mcpv1alpha1.CompositeToolDefinitionRef{
						{Name: "referenced-tool"},
					},
				},
			},
			compositeDefs: []*mcpv1alpha1.VirtualMCPCompositeToolDefinition{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "referenced-tool",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
						Name:        "referenced-tool",
						Description: "A referenced composite tool",
						Steps: []mcpv1alpha1.WorkflowStep{
							{
								ID:   "step1",
								Type: "tool",
								Tool: "backend.referenced-tool",
							},
						},
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, config *vmcpconfig.Config) {
				t.Helper()
				require.Len(t, config.CompositeTools, 2)
				// Check that both tools are present
				toolNames := make(map[string]bool)
				for _, tool := range config.CompositeTools {
					toolNames[tool.Name] = true
				}
				assert.True(t, toolNames["inline-tool"], "inline-tool should be present")
				assert.True(t, toolNames["referenced-tool"], "referenced-tool should be present")
			},
		},
		{
			name: "error when referenced composite tool not found",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{Name: "test-group"},
					CompositeToolRefs: []mcpv1alpha1.CompositeToolDefinitionRef{
						{Name: "non-existent-tool"},
					},
				},
			},
			compositeDefs: []*mcpv1alpha1.VirtualMCPCompositeToolDefinition{},
			expectError:   true,
			errorContains: "not found",
		},
		{
			name: "error when duplicate tool names exist",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{Name: "test-group"},
					CompositeTools: []mcpv1alpha1.CompositeToolSpec{
						{
							Name:        "duplicate-tool",
							Description: "An inline tool",
							Steps: []mcpv1alpha1.WorkflowStep{
								{
									ID:   "step1",
									Type: "tool",
									Tool: "backend.tool1",
								},
							},
						},
					},
					CompositeToolRefs: []mcpv1alpha1.CompositeToolDefinitionRef{
						{Name: "referenced-tool"},
					},
				},
			},
			compositeDefs: []*mcpv1alpha1.VirtualMCPCompositeToolDefinition{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "referenced-tool",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
						Name:        "duplicate-tool", // Same name as inline tool
						Description: "A referenced tool with duplicate name",
						Steps: []mcpv1alpha1.WorkflowStep{
							{
								ID:   "step1",
								Type: "tool",
								Tool: "backend.tool2",
							},
						},
					},
				},
			},
			expectError:   true,
			errorContains: "duplicate composite tool name",
		},
		{
			name: "error when k8sClient is nil",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{Name: "test-group"},
				},
			},
			compositeDefs: []*mcpv1alpha1.VirtualMCPCompositeToolDefinition{},
			k8sClient:     nil, // No client provided
			expectError:   true,
			errorContains: "k8sClient is required",
		},
		{
			name: "handle multiple referenced tools",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{Name: "test-group"},
					CompositeToolRefs: []mcpv1alpha1.CompositeToolDefinitionRef{
						{Name: "tool1"},
						{Name: "tool2"},
					},
				},
			},
			compositeDefs: []*mcpv1alpha1.VirtualMCPCompositeToolDefinition{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "tool1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
						Name:        "tool1",
						Description: "First referenced tool",
						Steps: []mcpv1alpha1.WorkflowStep{
							{
								ID:   "step1",
								Type: "tool",
								Tool: "backend.tool1",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "tool2",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
						Name:        "tool2",
						Description: "Second referenced tool",
						Steps: []mcpv1alpha1.WorkflowStep{
							{
								ID:   "step1",
								Type: "tool",
								Tool: "backend.tool2",
							},
						},
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, config *vmcpconfig.Config) {
				t.Helper()
				require.Len(t, config.CompositeTools, 2)
				toolNames := make(map[string]bool)
				for _, tool := range config.CompositeTools {
					toolNames[tool.Name] = true
				}
				assert.True(t, toolNames["tool1"], "tool1 should be present")
				assert.True(t, toolNames["tool2"], "tool2 should be present")
			},
		},
		{
			name: "convert referenced tool with parameters and timeout",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{Name: "test-group"},
					CompositeToolRefs: []mcpv1alpha1.CompositeToolDefinitionRef{
						{Name: "referenced-tool"},
					},
				},
			},
			compositeDefs: []*mcpv1alpha1.VirtualMCPCompositeToolDefinition{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "referenced-tool",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
						Name:        "referenced-tool",
						Description: "A referenced tool with parameters",
						Parameters: &runtime.RawExtension{
							Raw: []byte(`{"type":"object","properties":{"param1":{"type":"string"}}}`),
						},
						Timeout: "5m",
						Steps: []mcpv1alpha1.WorkflowStep{
							{
								ID:   "step1",
								Type: "tool",
								Tool: "backend.tool1",
							},
						},
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, config *vmcpconfig.Config) {
				t.Helper()
				require.Len(t, config.CompositeTools, 1)
				tool := config.CompositeTools[0]
				assert.Equal(t, "referenced-tool", tool.Name)
				assert.Equal(t, vmcpconfig.Duration(5*time.Minute), tool.Timeout)
				require.NotNil(t, tool.Parameters)
				params := tool.Parameters
				assert.Equal(t, "object", params["type"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Setup fake Kubernetes client
			var fakeClient client.Client
			if tt.k8sClient != nil {
				// Use provided client
				fakeClient = tt.k8sClient
			} else {
				// Create fake client with objects (or nil if we want to test nil client behavior)
				testScheme := createTestScheme()
				objects := []client.Object{tt.vmcp}
				for _, def := range tt.compositeDefs {
					objects = append(objects, def)
				}
				fakeClient = fake.NewClientBuilder().
					WithScheme(testScheme).
					WithObjects(objects...).
					Build()
			}

			// Create converter with client
			resolver := newNoOpMockResolver(t)
			converter, err := NewConverter(resolver, fakeClient)
			if tt.name == "error when k8sClient is nil" {
				// For this test, we explicitly pass nil to test the error
				_, err = NewConverter(resolver, nil)
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				return
			}
			require.NoError(t, err)

			ctx := log.IntoContext(context.Background(), logr.Discard())
			config, err := converter.Convert(ctx, tt.vmcp)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, config)
				if tt.validate != nil {
					tt.validate(t, config)
				}
			}
		})
	}
}

// Test helpers for MCPToolConfig tests
func newMCPToolConfig(name, namespace string, filter []string, overrides map[string]mcpv1alpha1.ToolOverride) *mcpv1alpha1.MCPToolConfig {
	return &mcpv1alpha1.MCPToolConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       mcpv1alpha1.MCPToolConfigSpec{ToolsFilter: filter, ToolsOverride: overrides},
	}
}

func toolOverride(name, desc string) mcpv1alpha1.ToolOverride {
	return mcpv1alpha1.ToolOverride{Name: name, Description: desc}
}

func vmcpToolOverride(name, desc string) *vmcpconfig.ToolOverride {
	return &vmcpconfig.ToolOverride{Name: name, Description: desc}
}

func TestResolveMCPToolConfig(t *testing.T) {
	t.Parallel()

	ns := "test-ns"
	tests := []struct {
		name        string
		configName  string
		existing    *mcpv1alpha1.MCPToolConfig
		expectError bool
	}{
		{
			name:       "successfully resolve existing MCPToolConfig",
			configName: "test-config",
			existing:   newMCPToolConfig("test-config", ns, []string{"tool1", "tool2"}, nil),
		},
		{
			name:        "error when MCPToolConfig not found",
			configName:  "nonexistent",
			expectError: true,
		},
		{
			name:       "successfully resolve with overrides",
			configName: "config-with-overrides",
			existing: newMCPToolConfig("config-with-overrides", ns, []string{"fetch"},
				map[string]mcpv1alpha1.ToolOverride{"fetch": toolOverride("renamed_fetch", "Renamed tool")}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var k8sClient client.Client
			if tt.existing != nil {
				k8sClient = newTestK8sClient(t, tt.existing)
			} else {
				k8sClient = newTestK8sClient(t)
			}

			converter := newTestConverter(t, newNoOpMockResolver(t))
			converter.k8sClient = k8sClient

			result, err := converter.resolveMCPToolConfig(context.Background(), ns, tt.configName)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				assert.Equal(t, tt.existing.Spec, result.Spec)
			}
		})
	}
}

func TestMergeToolConfigFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		existing []string
		config   *mcpv1alpha1.MCPToolConfig
		expected []string
	}{
		{
			name:     "merge when workload has none",
			existing: nil,
			config:   newMCPToolConfig("", "", []string{"tool1", "tool2"}, nil),
			expected: []string{"tool1", "tool2"},
		},
		{
			name:     "inline takes precedence",
			existing: []string{"inline_tool"},
			config:   newMCPToolConfig("", "", []string{"config_tool"}, nil),
			expected: []string{"inline_tool"},
		},
		{
			name:     "no change when config has no filter",
			existing: []string{"existing_tool"},
			config:   newMCPToolConfig("", "", nil, nil),
			expected: []string{"existing_tool"},
		},
		{
			name:     "empty filter from config",
			existing: nil,
			config:   newMCPToolConfig("", "", []string{}, nil),
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			wtc := &vmcpconfig.WorkloadToolConfig{Filter: tt.existing}
			(&Converter{}).mergeToolConfigFilter(wtc, tt.config)

			assert.Equal(t, tt.expected, wtc.Filter)
		})
	}
}

func TestMergeToolConfigOverrides(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		existing map[string]*vmcpconfig.ToolOverride
		config   *mcpv1alpha1.MCPToolConfig
		expected map[string]*vmcpconfig.ToolOverride
	}{
		{
			name:     "merge when workload has none",
			existing: nil,
			config:   newMCPToolConfig("", "", nil, map[string]mcpv1alpha1.ToolOverride{"tool1": toolOverride("renamed_tool1", "Renamed description")}),
			expected: map[string]*vmcpconfig.ToolOverride{"tool1": vmcpToolOverride("renamed_tool1", "Renamed description")},
		},
		{
			name:     "inline takes precedence",
			existing: map[string]*vmcpconfig.ToolOverride{"tool1": vmcpToolOverride("inline_name", "Inline description")},
			config:   newMCPToolConfig("", "", nil, map[string]mcpv1alpha1.ToolOverride{"tool1": toolOverride("config_name", "Config description")}),
			expected: map[string]*vmcpconfig.ToolOverride{"tool1": vmcpToolOverride("inline_name", "Inline description")},
		},
		{
			name:     "merge non-conflicting",
			existing: map[string]*vmcpconfig.ToolOverride{"tool1": vmcpToolOverride("inline_tool1", "Inline description")},
			config:   newMCPToolConfig("", "", nil, map[string]mcpv1alpha1.ToolOverride{"tool2": toolOverride("config_tool2", "Config description")}),
			expected: map[string]*vmcpconfig.ToolOverride{
				"tool1": vmcpToolOverride("inline_tool1", "Inline description"),
				"tool2": vmcpToolOverride("config_tool2", "Config description"),
			},
		},
		{
			name:     "no change when config has no overrides",
			existing: map[string]*vmcpconfig.ToolOverride{"tool1": vmcpToolOverride("existing_name", "")},
			config:   newMCPToolConfig("", "", nil, nil),
			expected: map[string]*vmcpconfig.ToolOverride{"tool1": vmcpToolOverride("existing_name", "")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			wtc := &vmcpconfig.WorkloadToolConfig{Overrides: tt.existing}
			(&Converter{}).mergeToolConfigOverrides(wtc, tt.config)

			assert.Equal(t, tt.expected, wtc.Overrides)
		})
	}
}

func TestApplyInlineOverrides(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		inline   map[string]mcpv1alpha1.ToolOverride
		existing map[string]*vmcpconfig.ToolOverride
		expected map[string]*vmcpconfig.ToolOverride
	}{
		{
			name:     "apply to empty workload",
			inline:   map[string]mcpv1alpha1.ToolOverride{"tool1": toolOverride("renamed_tool1", "Inline description")},
			existing: nil,
			expected: map[string]*vmcpconfig.ToolOverride{"tool1": vmcpToolOverride("renamed_tool1", "Inline description")},
		},
		{
			name:     "replace existing",
			inline:   map[string]mcpv1alpha1.ToolOverride{"tool1": toolOverride("new_name", "New description")},
			existing: map[string]*vmcpconfig.ToolOverride{"tool1": vmcpToolOverride("old_name", "Old description")},
			expected: map[string]*vmcpconfig.ToolOverride{"tool1": vmcpToolOverride("new_name", "New description")},
		},
		{
			name:     "no change when no inline",
			inline:   nil,
			existing: map[string]*vmcpconfig.ToolOverride{"tool1": vmcpToolOverride("existing_name", "")},
			expected: map[string]*vmcpconfig.ToolOverride{"tool1": vmcpToolOverride("existing_name", "")},
		},
		{
			name: "multiple overrides",
			inline: map[string]mcpv1alpha1.ToolOverride{
				"tool1": toolOverride("renamed_tool1", "Description 1"),
				"tool2": toolOverride("renamed_tool2", "Description 2"),
			},
			existing: nil,
			expected: map[string]*vmcpconfig.ToolOverride{
				"tool1": vmcpToolOverride("renamed_tool1", "Description 1"),
				"tool2": vmcpToolOverride("renamed_tool2", "Description 2"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			wtc := &vmcpconfig.WorkloadToolConfig{Overrides: tt.existing}
			toolConfig := mcpv1alpha1.WorkloadToolConfig{Overrides: tt.inline}
			(&Converter{}).applyInlineOverrides(toolConfig, wtc)

			assert.Equal(t, tt.expected, wtc.Overrides)
		})
	}
}

func TestConvertToolConfigs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		tools            []mcpv1alpha1.WorkloadToolConfig
		existingConfig   *mcpv1alpha1.MCPToolConfig
		expectedWorkload string
		expectedFilter   []string
		expectedOverride map[string]*vmcpconfig.ToolOverride
	}{
		{
			name: "inline config only",
			tools: []mcpv1alpha1.WorkloadToolConfig{{
				Workload:  "backend1",
				Filter:    []string{"tool1", "tool2"},
				Overrides: map[string]mcpv1alpha1.ToolOverride{"tool1": toolOverride("renamed_tool1", "Renamed")},
			}},
			expectedWorkload: "backend1",
			expectedFilter:   []string{"tool1", "tool2"},
			expectedOverride: map[string]*vmcpconfig.ToolOverride{"tool1": vmcpToolOverride("renamed_tool1", "Renamed")},
		},
		{
			name: "with MCPToolConfig reference",
			tools: []mcpv1alpha1.WorkloadToolConfig{{
				Workload:      "backend1",
				ToolConfigRef: &mcpv1alpha1.ToolConfigRef{Name: "test-config"},
			}},
			existingConfig: newMCPToolConfig("test-config", "default", []string{"fetch"},
				map[string]mcpv1alpha1.ToolOverride{"fetch": toolOverride("renamed_fetch", "Renamed fetch")}),
			expectedWorkload: "backend1",
			expectedFilter:   []string{"fetch"},
			expectedOverride: map[string]*vmcpconfig.ToolOverride{"fetch": vmcpToolOverride("renamed_fetch", "Renamed fetch")},
		},
		{
			name: "inline takes precedence",
			tools: []mcpv1alpha1.WorkloadToolConfig{{
				Workload:      "backend1",
				Filter:        []string{"inline_tool"},
				ToolConfigRef: &mcpv1alpha1.ToolConfigRef{Name: "test-config"},
				Overrides:     map[string]mcpv1alpha1.ToolOverride{"fetch": toolOverride("inline_fetch", "Inline override")},
			}},
			existingConfig: newMCPToolConfig("test-config", "default", []string{"config_tool"},
				map[string]mcpv1alpha1.ToolOverride{"fetch": toolOverride("config_fetch", "Config override")}),
			expectedWorkload: "backend1",
			expectedFilter:   []string{"inline_tool"},
			expectedOverride: map[string]*vmcpconfig.ToolOverride{"fetch": vmcpToolOverride("inline_fetch", "Inline override")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := log.IntoContext(context.Background(), logr.Discard())
			var k8sClient client.Client
			if tt.existingConfig != nil {
				k8sClient = newTestK8sClient(t, tt.existingConfig)
			} else {
				k8sClient = newTestK8sClient(t)
			}

			converter := newTestConverter(t, newNoOpMockResolver(t))
			converter.k8sClient = k8sClient

			vmcp := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
				Spec:       mcpv1alpha1.VirtualMCPServerSpec{Aggregation: &mcpv1alpha1.AggregationConfig{Tools: tt.tools}},
			}

			agg := &vmcpconfig.AggregationConfig{}
			err := converter.convertToolConfigs(ctx, vmcp, agg)

			require.NoError(t, err)
			require.Len(t, agg.Tools, 1)
			assert.Equal(t, tt.expectedWorkload, agg.Tools[0].Workload)
			assert.Equal(t, tt.expectedFilter, agg.Tools[0].Filter)
			assert.Equal(t, tt.expectedOverride, agg.Tools[0].Overrides)
		})
	}
}

// TestConvertToolConfigs_FailClosed tests that MCPToolConfig resolution errors cause conversion to fail.
// This is a security feature: if a user explicitly references an MCPToolConfig (for tool filtering or
// security policy enforcement), we should fail rather than deploy without the intended configuration.
func TestConvertToolConfigs_FailClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		tools          []mcpv1alpha1.WorkloadToolConfig
		existingConfig *mcpv1alpha1.MCPToolConfig
		expectError    bool
		expectedErrMsg string
	}{
		{
			name: "error when MCPToolConfig reference not found (fail closed)",
			tools: []mcpv1alpha1.WorkloadToolConfig{{
				Workload:      "backend1",
				ToolConfigRef: &mcpv1alpha1.ToolConfigRef{Name: "nonexistent-config"},
			}},
			existingConfig: nil, // MCPToolConfig doesn't exist in cluster
			expectError:    true,
			expectedErrMsg: "MCPToolConfig resolution failed for \"nonexistent-config\"",
		},
		{
			name: "no error when no ToolConfigRef specified",
			tools: []mcpv1alpha1.WorkloadToolConfig{{
				Workload: "backend1",
				Filter:   []string{"tool1"},
			}},
			existingConfig: nil,
			expectError:    false,
		},
		{
			name: "successful when MCPToolConfig exists",
			tools: []mcpv1alpha1.WorkloadToolConfig{{
				Workload:      "backend1",
				ToolConfigRef: &mcpv1alpha1.ToolConfigRef{Name: "valid-config"},
			}},
			existingConfig: newMCPToolConfig("valid-config", "default", []string{"fetch"}, nil),
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := log.IntoContext(context.Background(), logr.Discard())
			var k8sClient client.Client
			if tt.existingConfig != nil {
				k8sClient = newTestK8sClient(t, tt.existingConfig)
			} else {
				k8sClient = newTestK8sClient(t)
			}

			converter := newTestConverter(t, newNoOpMockResolver(t))
			converter.k8sClient = k8sClient

			vmcp := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
				Spec:       mcpv1alpha1.VirtualMCPServerSpec{Aggregation: &mcpv1alpha1.AggregationConfig{Tools: tt.tools}},
			}

			agg := &vmcpconfig.AggregationConfig{}
			err := converter.convertToolConfigs(ctx, vmcp, agg)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErrMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestConvert_MCPToolConfigFailClosed tests that MCPToolConfig resolution errors propagate through
// the full Convert() method and prevent VirtualMCPServer deployment.
func TestConvert_MCPToolConfigFailClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		vmcp           *mcpv1alpha1.VirtualMCPServer
		existingConfig *mcpv1alpha1.MCPToolConfig
		expectError    bool
		expectedErrMsg string
	}{
		{
			name: "Convert fails when MCPToolConfig not found",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{Name: "test-group"},
					Aggregation: &mcpv1alpha1.AggregationConfig{
						Tools: []mcpv1alpha1.WorkloadToolConfig{{
							Workload:      "backend1",
							ToolConfigRef: &mcpv1alpha1.ToolConfigRef{Name: "missing-config"},
						}},
					},
				},
			},
			existingConfig: nil,
			expectError:    true,
			expectedErrMsg: "failed to convert aggregation config",
		},
		{
			name: "Convert succeeds when MCPToolConfig exists",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{Name: "test-group"},
					Aggregation: &mcpv1alpha1.AggregationConfig{
						Tools: []mcpv1alpha1.WorkloadToolConfig{{
							Workload:      "backend1",
							ToolConfigRef: &mcpv1alpha1.ToolConfigRef{Name: "valid-config"},
						}},
					},
				},
			},
			existingConfig: newMCPToolConfig("valid-config", "default", []string{"fetch"}, nil),
			expectError:    false,
		},
		{
			name: "Convert succeeds when no Aggregation specified",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{Name: "test-group"},
				},
			},
			existingConfig: nil,
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := log.IntoContext(context.Background(), logr.Discard())
			var k8sClient client.Client
			if tt.existingConfig != nil {
				k8sClient = newTestK8sClient(t, tt.existingConfig)
			} else {
				k8sClient = newTestK8sClient(t)
			}

			converter := newTestConverter(t, newNoOpMockResolver(t))
			converter.k8sClient = k8sClient

			config, err := converter.Convert(ctx, tt.vmcp)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErrMsg)
				assert.Nil(t, config)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, config)
			}
		})
	}
}
