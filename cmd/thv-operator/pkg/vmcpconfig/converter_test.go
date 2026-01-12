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
	thvjson "github.com/stacklok/toolhive/pkg/json"
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
			Config:       vmcpconfig.Config{Group: "test-group"},
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

// TestConverter_CompositeToolsPassThrough verifies that CompositeTools from spec.config.CompositeTools
// are correctly passed through during conversion and not dropped.
// It also verifies that Duration fields serialize to human-readable formats (e.g., "30s").
func TestConverter_CompositeToolsPassThrough(t *testing.T) {
	t.Parallel()

	vmcpServer := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			Config: vmcpconfig.Config{
				Group: "test-group",
				CompositeTools: []vmcpconfig.CompositeToolConfig{
					{
						Name:        "test-composite-tool",
						Description: "A test composite tool",
						Timeout:     vmcpconfig.Duration(30 * time.Second),
						Steps: []vmcpconfig.WorkflowStepConfig{
							{
								ID:   "step1",
								Type: "tool",
								Tool: "backend.some-tool",
							},
							{
								ID:        "step2",
								Type:      "tool",
								Tool:      "backend.other-tool",
								DependsOn: []string{"step1"},
							},
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
	require.Len(t, config.CompositeTools, 1, "CompositeTools should not be dropped during conversion")

	tool := config.CompositeTools[0]
	assert.Equal(t, "test-composite-tool", tool.Name)
	assert.Equal(t, "A test composite tool", tool.Description)
	assert.Equal(t, vmcpconfig.Duration(30*time.Second), tool.Timeout)
	require.Len(t, tool.Steps, 2)
	assert.Equal(t, "step1", tool.Steps[0].ID)
	assert.Equal(t, "step2", tool.Steps[1].ID)
	assert.Equal(t, []string{"step1"}, tool.Steps[1].DependsOn)

	// Verify that Duration serializes to a human-readable format (e.g., "30s")
	timeoutJSON, err := json.Marshal(tool.Timeout)
	require.NoError(t, err)
	assert.Equal(t, `"30s"`, string(timeoutJSON), "Duration should serialize to human-readable format")
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
		{
			name: "oidc auth with scopes",
			incomingAuth: &mcpv1alpha1.IncomingAuthConfig{
				Type: "oidc",
				OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
					Type: "inline",
					Inline: &mcpv1alpha1.InlineOIDCConfig{
						Issuer:   "https://accounts.google.com",
						ClientID: "google-client",
						Audience: "google-audience",
						Scopes:   []string{"https://www.googleapis.com/auth/drive.readonly", "openid"},
					},
				},
			},
			expectedAuthType: "oidc",
			expectedOIDCConfig: &vmcpconfig.OIDCConfig{
				Issuer:   "https://accounts.google.com",
				ClientID: "google-client",
				Audience: "google-audience",
				Scopes:   []string{"https://www.googleapis.com/auth/drive.readonly", "openid"},
			},
			description: "Should correctly convert OIDC auth config with scopes",
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
					Config:       vmcpconfig.Config{Group: "test-group"},
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
					Scopes:   tt.expectedOIDCConfig.Scopes,
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
					assert.Equal(t, tt.expectedOIDCConfig.Scopes, config.IncomingAuth.OIDC.Scopes, tt.description)
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
					Config: vmcpconfig.Config{
						Group: "test-group",
						CompositeToolRefs: []vmcpconfig.CompositeToolRef{
							{Name: "referenced-tool"},
						},
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
						CompositeToolConfig: vmcpconfig.CompositeToolConfig{
							Name:        "referenced-tool",
							Description: "A referenced composite tool",
							Steps: []vmcpconfig.WorkflowStepConfig{
								{
									ID:   "step1",
									Type: "tool",
									Tool: "backend.tool1",
								},
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
					Config: vmcpconfig.Config{
						Group: "test-group",
						CompositeTools: []vmcpconfig.CompositeToolConfig{
							{
								Name:        "inline-tool",
								Description: "An inline composite tool",
								Steps: []vmcpconfig.WorkflowStepConfig{
									{
										ID:   "step1",
										Type: "tool",
										Tool: "backend.inline-tool",
									},
								},
							},
						},
						CompositeToolRefs: []vmcpconfig.CompositeToolRef{
							{Name: "referenced-tool"},
						},
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
						CompositeToolConfig: vmcpconfig.CompositeToolConfig{
							Name:        "referenced-tool",
							Description: "A referenced composite tool",
							Steps: []vmcpconfig.WorkflowStepConfig{
								{
									ID:   "step1",
									Type: "tool",
									Tool: "backend.referenced-tool",
								},
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
					Config: vmcpconfig.Config{
						Group: "test-group",
						CompositeToolRefs: []vmcpconfig.CompositeToolRef{
							{Name: "non-existent-tool"},
						},
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
					Config: vmcpconfig.Config{
						Group: "test-group",
						CompositeTools: []vmcpconfig.CompositeToolConfig{
							{
								Name:        "duplicate-tool",
								Description: "An inline tool",
								Steps: []vmcpconfig.WorkflowStepConfig{
									{
										ID:   "step1",
										Type: "tool",
										Tool: "backend.tool1",
									},
								},
							},
						},
						CompositeToolRefs: []vmcpconfig.CompositeToolRef{
							{Name: "referenced-tool"},
						},
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
						CompositeToolConfig: vmcpconfig.CompositeToolConfig{
							Name:        "duplicate-tool", // Same name as inline tool
							Description: "A referenced tool with duplicate name",
							Steps: []vmcpconfig.WorkflowStepConfig{
								{
									ID:   "step1",
									Type: "tool",
									Tool: "backend.tool2",
								},
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
					Config: vmcpconfig.Config{Group: "test-group"},
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
					Config: vmcpconfig.Config{
						Group: "test-group",
						CompositeToolRefs: []vmcpconfig.CompositeToolRef{
							{Name: "tool1"},
							{Name: "tool2"},
						},
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
						CompositeToolConfig: vmcpconfig.CompositeToolConfig{
							Name:        "tool1",
							Description: "First referenced tool",
							Steps: []vmcpconfig.WorkflowStepConfig{
								{
									ID:   "step1",
									Type: "tool",
									Tool: "backend.tool1",
								},
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
						CompositeToolConfig: vmcpconfig.CompositeToolConfig{
							Name:        "tool2",
							Description: "Second referenced tool",
							Steps: []vmcpconfig.WorkflowStepConfig{
								{
									ID:   "step1",
									Type: "tool",
									Tool: "backend.tool2",
								},
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
					Config: vmcpconfig.Config{
						Group: "test-group",
						CompositeToolRefs: []vmcpconfig.CompositeToolRef{
							{Name: "referenced-tool"},
						},
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
						CompositeToolConfig: vmcpconfig.CompositeToolConfig{
							Name:        "referenced-tool",
							Description: "A referenced tool with parameters",
							Parameters: thvjson.NewMap(map[string]any{
								"type": "object",
								"properties": map[string]any{
									"param1": map[string]any{"type": "string"},
								},
							}),
							Timeout: vmcpconfig.Duration(5 * time.Minute),
							Steps: []vmcpconfig.WorkflowStepConfig{
								{
									ID:   "step1",
									Type: "tool",
									Tool: "backend.tool1",
								},
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
				params, err := tool.Parameters.ToMap()
				require.NoError(t, err)
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

// TestConverter_CompositeToolDefinitionFieldsPreserved verifies that all fields from a
// VirtualMCPCompositeToolDefinition CRD spec are correctly preserved through conversion.
func TestConverter_CompositeToolDefinitionFieldsPreserved(t *testing.T) {
	t.Parallel()

	// Create the expected CompositeToolConfig that will be embedded in the CRD spec
	expectedConfig := vmcpconfig.CompositeToolConfig{
		Name:        "comprehensive-tool",
		Description: "A comprehensive composite tool with all fields",
		Timeout:     vmcpconfig.Duration(2*time.Minute + 30*time.Second),
		Parameters: thvjson.NewMap(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"input": map[string]any{"type": "string"},
				"count": map[string]any{"type": "integer"},
			},
			"required": []any{"input"},
		}),
		Steps: []vmcpconfig.WorkflowStepConfig{
			{
				ID:        "step1",
				Type:      "tool",
				Tool:      "backend.first-tool",
				Arguments: thvjson.NewMap(map[string]any{"arg1": "{{ .params.input }}"}),
				Timeout:   vmcpconfig.Duration(30 * time.Second),
				OnError: &vmcpconfig.StepErrorHandling{
					Action:     "retry",
					RetryCount: 3,
					RetryDelay: vmcpconfig.Duration(5 * time.Second),
				},
			},
			{
				ID:        "step2",
				Type:      "tool",
				Tool:      "backend.second-tool",
				DependsOn: []string{"step1"},
				Condition: "{{ .steps.step1.success }}",
				Arguments: thvjson.NewMap(map[string]any{"data": "{{ .steps.step1.result }}"}),
				OnError: &vmcpconfig.StepErrorHandling{
					Action: "continue",
				},
			},
		},
		Output: &vmcpconfig.OutputConfig{
			Properties: map[string]vmcpconfig.OutputProperty{
				"result": {
					Type:        "string",
					Description: "The final result",
					Value:       "{{ .steps.step2.result }}",
				},
			},
			Required: []string{"result"},
		},
	}

	// Create a VirtualMCPCompositeToolDefinition with all fields populated
	compositeDef := &mcpv1alpha1.VirtualMCPCompositeToolDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "comprehensive-tool",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPCompositeToolDefinitionSpec{
			CompositeToolConfig: expectedConfig,
		},
	}

	vmcpServer := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			Config: vmcpconfig.Config{
				Group: "test-group",
				CompositeToolRefs: []vmcpconfig.CompositeToolRef{
					{Name: "comprehensive-tool"},
				},
			},
		},
	}

	// Setup fake Kubernetes client
	testScheme := createTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(vmcpServer, compositeDef).
		Build()

	resolver := newNoOpMockResolver(t)
	converter, err := NewConverter(resolver, fakeClient)
	require.NoError(t, err)

	ctx := log.IntoContext(context.Background(), logr.Discard())
	cfg, err := converter.Convert(ctx, vmcpServer)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Len(t, cfg.CompositeTools, 1)

	// Since the spec embeds CompositeToolConfig directly, the converted result should match
	require.Equal(t, expectedConfig, cfg.CompositeTools[0])
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

func TestResolveToolConfigRefs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		tools            []*vmcpconfig.WorkloadToolConfig
		existingConfig   *mcpv1alpha1.MCPToolConfig
		expectedWorkload string
		expectedFilter   []string
		expectedOverride map[string]*vmcpconfig.ToolOverride
	}{
		{
			name: "inline config only",
			tools: []*vmcpconfig.WorkloadToolConfig{{
				Workload:  "backend1",
				Filter:    []string{"tool1", "tool2"},
				Overrides: map[string]*vmcpconfig.ToolOverride{"tool1": vmcpToolOverride("renamed_tool1", "Renamed")},
			}},
			expectedWorkload: "backend1",
			expectedFilter:   []string{"tool1", "tool2"},
			expectedOverride: map[string]*vmcpconfig.ToolOverride{"tool1": vmcpToolOverride("renamed_tool1", "Renamed")},
		},
		{
			name: "with MCPToolConfig reference",
			tools: []*vmcpconfig.WorkloadToolConfig{{
				Workload:      "backend1",
				ToolConfigRef: &vmcpconfig.ToolConfigRef{Name: "test-config"},
			}},
			existingConfig: newMCPToolConfig("test-config", "default", []string{"fetch"},
				map[string]mcpv1alpha1.ToolOverride{"fetch": toolOverride("renamed_fetch", "Renamed fetch")}),
			expectedWorkload: "backend1",
			expectedFilter:   []string{"fetch"},
			expectedOverride: map[string]*vmcpconfig.ToolOverride{"fetch": vmcpToolOverride("renamed_fetch", "Renamed fetch")},
		},
		{
			name: "inline takes precedence",
			tools: []*vmcpconfig.WorkloadToolConfig{{
				Workload:      "backend1",
				Filter:        []string{"inline_tool"},
				ToolConfigRef: &vmcpconfig.ToolConfigRef{Name: "test-config"},
				Overrides:     map[string]*vmcpconfig.ToolOverride{"fetch": vmcpToolOverride("inline_fetch", "Inline override")},
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

			srcAgg := &vmcpconfig.AggregationConfig{Tools: tt.tools}
			vmcp := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
			}

			agg := &vmcpconfig.AggregationConfig{}
			err := converter.resolveToolConfigRefs(ctx, vmcp, srcAgg, agg)

			require.NoError(t, err)
			require.Len(t, agg.Tools, 1)
			assert.Equal(t, tt.expectedWorkload, agg.Tools[0].Workload)
			assert.Equal(t, tt.expectedFilter, agg.Tools[0].Filter)
			assert.Equal(t, tt.expectedOverride, agg.Tools[0].Overrides)
		})
	}
}

// TestResolveToolConfigRefs_FailClosed tests that MCPToolConfig resolution errors cause conversion to fail.
// This is a security feature: if a user explicitly references an MCPToolConfig (for tool filtering or
// security policy enforcement), we should fail rather than deploy without the intended configuration.
func TestResolveToolConfigRefs_FailClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		tools          []*vmcpconfig.WorkloadToolConfig
		existingConfig *mcpv1alpha1.MCPToolConfig
		expectError    bool
		expectedErrMsg string
	}{
		{
			name: "error when MCPToolConfig reference not found (fail closed)",
			tools: []*vmcpconfig.WorkloadToolConfig{{
				Workload:      "backend1",
				ToolConfigRef: &vmcpconfig.ToolConfigRef{Name: "nonexistent-config"},
			}},
			existingConfig: nil, // MCPToolConfig doesn't exist in cluster
			expectError:    true,
			expectedErrMsg: "MCPToolConfig resolution failed for \"nonexistent-config\"",
		},
		{
			name: "no error when no ToolConfigRef specified",
			tools: []*vmcpconfig.WorkloadToolConfig{{
				Workload: "backend1",
				Filter:   []string{"tool1"},
			}},
			existingConfig: nil,
			expectError:    false,
		},
		{
			name: "successful when MCPToolConfig exists",
			tools: []*vmcpconfig.WorkloadToolConfig{{
				Workload:      "backend1",
				ToolConfigRef: &vmcpconfig.ToolConfigRef{Name: "valid-config"},
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

			srcAgg := &vmcpconfig.AggregationConfig{Tools: tt.tools}
			vmcp := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
			}

			agg := &vmcpconfig.AggregationConfig{}
			err := converter.resolveToolConfigRefs(ctx, vmcp, srcAgg, agg)

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
					Config: vmcpconfig.Config{
						Group: "test-group",
						Aggregation: &vmcpconfig.AggregationConfig{
							Tools: []*vmcpconfig.WorkloadToolConfig{{
								Workload:      "backend1",
								ToolConfigRef: &vmcpconfig.ToolConfigRef{Name: "missing-config"},
							}},
						},
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
					Config: vmcpconfig.Config{
						Group: "test-group",
						Aggregation: &vmcpconfig.AggregationConfig{
							Tools: []*vmcpconfig.WorkloadToolConfig{{
								Workload:      "backend1",
								ToolConfigRef: &vmcpconfig.ToolConfigRef{Name: "valid-config"},
							}},
						},
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
					Config: vmcpconfig.Config{Group: "test-group"},
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
