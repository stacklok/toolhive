// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/oidc"
	oidcmocks "github.com/stacklok/toolhive/cmd/thv-operator/pkg/oidc/mocks"
	thvjson "github.com/stacklok/toolhive/pkg/json"
	"github.com/stacklok/toolhive/pkg/telemetry"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// newNoOpMockResolver creates a mock resolver that returns (nil, nil) for all calls.
// Use this in tests that don't care about OIDC configuration.
func newNoOpMockResolver(t *testing.T) *oidcmocks.MockResolver {
	t.Helper()
	ctrl := gomock.NewController(t)
	mockResolver := oidcmocks.NewMockResolver(ctrl)
	return mockResolver
}

// newTestK8sClient creates a fake Kubernetes client for testing.
func newTestK8sClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))
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

// newTestVMCPServer creates a VirtualMCPServer with an MCPOIDCConfigReference for testing.
func newTestVMCPServer(oidcConfigRef *mcpv1beta1.MCPOIDCConfigReference) *mcpv1beta1.VirtualMCPServer {
	return &mcpv1beta1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
		Spec: mcpv1beta1.VirtualMCPServerSpec{
			GroupRef:     &mcpv1beta1.MCPGroupRef{Name: "test-group"},
			IncomingAuth: &mcpv1beta1.IncomingAuthConfig{Type: "oidc", OIDCConfigRef: oidcConfigRef},
		},
	}
}

// newTestMCPOIDCConfig creates an MCPOIDCConfig resource for testing with the given spec type.
func newTestMCPOIDCConfig(specType mcpv1beta1.MCPOIDCConfigSourceType) *mcpv1beta1.MCPOIDCConfig {
	return &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-oidc", Namespace: "default"},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{
			Type: specType,
		},
	}
}

// newTestMCPOIDCConfigInline creates an MCPOIDCConfig resource with inline config for testing.
func newTestMCPOIDCConfigInline(inline *mcpv1beta1.InlineOIDCSharedConfig) *mcpv1beta1.MCPOIDCConfig {
	return &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-oidc", Namespace: "default"},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{
			Type:   mcpv1beta1.MCPOIDCConfigTypeInline,
			Inline: inline,
		},
	}
}

// newTestConverterWithObjects creates a Converter with the given resolver and k8s objects.
func newTestConverterWithObjects(t *testing.T, resolver oidc.Resolver, objects ...client.Object) *Converter {
	t.Helper()
	k8sClient := newTestK8sClient(t, objects...)
	converter, err := NewConverter(resolver, k8sClient)
	require.NoError(t, err)
	return converter
}

func TestConverter_RateLimitingFromTopLevelSpec(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1beta1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
		Spec: mcpv1beta1.VirtualMCPServerSpec{
			GroupRef:     &mcpv1beta1.MCPGroupRef{Name: "test-group"},
			IncomingAuth: &mcpv1beta1.IncomingAuthConfig{Type: "anonymous"},
			RateLimiting: &mcpv1beta1.RateLimitConfig{
				Global: &mcpv1beta1.RateLimitBucket{
					MaxTokens:    10,
					RefillPeriod: metav1.Duration{Duration: time.Minute},
				},
				Tools: []mcpv1beta1.ToolRateLimitConfig{
					{
						Name: "backend_a_echo",
						Global: &mcpv1beta1.RateLimitBucket{
							MaxTokens:    1,
							RefillPeriod: metav1.Duration{Duration: time.Minute},
						},
					},
				},
			},
		},
	}

	converter := newTestConverter(t, newNoOpMockResolver(t))
	cfg, _, err := converter.Convert(context.Background(), vmcp, nil)
	require.NoError(t, err)
	require.NotNil(t, cfg.RateLimiting)
	require.NotNil(t, cfg.RateLimiting.Global)
	assert.Equal(t, int32(10), cfg.RateLimiting.Global.MaxTokens)
	require.Len(t, cfg.RateLimiting.Tools, 1)
	assert.Equal(t, "backend_a_echo", cfg.RateLimiting.Tools[0].Name)
}

func TestConverter_OIDCResolution(t *testing.T) {
	t.Parallel()

	const oidcConfigName = "test-oidc"

	tests := []struct {
		name          string
		oidcConfigRef *mcpv1beta1.MCPOIDCConfigReference
		oidcConfig    *mcpv1beta1.MCPOIDCConfig // MCPOIDCConfig object to add to fake client
		mockReturn    *oidc.OIDCConfig
		mockErr       error
		validate      func(t *testing.T, config *vmcpconfig.Config, err error)
	}{
		{
			name:          "successful resolution maps all fields",
			oidcConfigRef: &mcpv1beta1.MCPOIDCConfigReference{Name: oidcConfigName, Audience: "my-audience"},
			oidcConfig:    newTestMCPOIDCConfig(mcpv1beta1.MCPOIDCConfigTypeKubernetesServiceAccount),
			mockReturn: &oidc.OIDCConfig{
				Issuer: "https://issuer.example.com", Audience: "my-audience",
				ResourceURL:        "https://resource.example.com",
				JWKSAllowPrivateIP: true, ProtectedResourceAllowPrivateIP: true,
				JWKSURL: "https://issuer.example.com/jwks", IntrospectionURL: "https://issuer.example.com/introspect",
			},
			validate: func(t *testing.T, config *vmcpconfig.Config, err error) {
				t.Helper()
				require.NoError(t, err)
				require.NotNil(t, config.IncomingAuth.OIDC)
				assert.Equal(t, "https://issuer.example.com", config.IncomingAuth.OIDC.Issuer)
				assert.Equal(t, "my-audience", config.IncomingAuth.OIDC.Audience)
				assert.Equal(t, "https://resource.example.com", config.IncomingAuth.OIDC.Resource)
				assert.Equal(t, "https://issuer.example.com/jwks", config.IncomingAuth.OIDC.JWKSURL)
				assert.Equal(t, "https://issuer.example.com/introspect", config.IncomingAuth.OIDC.IntrospectionURL)
				assert.True(t, config.IncomingAuth.OIDC.ProtectedResourceAllowPrivateIP)
				assert.True(t, config.IncomingAuth.OIDC.JwksAllowPrivateIP)
			},
		},
		{
			name:          "fields mapped independently - jwksAllowPrivateIP true, protectedResourceAllowPrivateIP false",
			oidcConfigRef: &mcpv1beta1.MCPOIDCConfigReference{Name: oidcConfigName, Audience: "my-audience"},
			oidcConfig:    newTestMCPOIDCConfig(mcpv1beta1.MCPOIDCConfigTypeKubernetesServiceAccount),
			mockReturn: &oidc.OIDCConfig{
				Issuer: "https://issuer.example.com", Audience: "my-audience",
				JWKSAllowPrivateIP: true, ProtectedResourceAllowPrivateIP: false,
			},
			validate: func(t *testing.T, config *vmcpconfig.Config, err error) {
				t.Helper()
				require.NoError(t, err)
				require.NotNil(t, config.IncomingAuth.OIDC)
				assert.True(t, config.IncomingAuth.OIDC.JwksAllowPrivateIP)
				assert.False(t, config.IncomingAuth.OIDC.ProtectedResourceAllowPrivateIP)
			},
		},
		{
			name:          "resolution error returns error (fail-closed)",
			oidcConfigRef: &mcpv1beta1.MCPOIDCConfigReference{Name: oidcConfigName, Audience: "test-audience"},
			oidcConfig:    newTestMCPOIDCConfig(mcpv1beta1.MCPOIDCConfigTypeInline),
			mockErr:       errors.New("configmap not found"),
			validate: func(t *testing.T, _ *vmcpconfig.Config, err error) {
				t.Helper()
				require.Error(t, err)
				assert.Contains(t, err.Error(), "OIDC resolution failed")
			},
		},
		{
			name:          "nil resolved config results in nil OIDC",
			oidcConfigRef: &mcpv1beta1.MCPOIDCConfigReference{Name: oidcConfigName, Audience: "test-audience"},
			oidcConfig:    newTestMCPOIDCConfig(mcpv1beta1.MCPOIDCConfigTypeInline),
			mockReturn:    nil,
			validate: func(t *testing.T, config *vmcpconfig.Config, err error) {
				t.Helper()
				require.NoError(t, err)
				assert.Nil(t, config.IncomingAuth.OIDC)
			},
		},
		{
			name:          "inline with client secret sets ClientSecretEnv",
			oidcConfigRef: &mcpv1beta1.MCPOIDCConfigReference{Name: oidcConfigName, Audience: "test-audience"},
			oidcConfig: newTestMCPOIDCConfigInline(&mcpv1beta1.InlineOIDCSharedConfig{
				Issuer: "https://issuer.example.com",
				ClientSecretRef: &mcpv1beta1.SecretKeyRef{
					Name: "oidc-secret",
					Key:  "client-secret",
				},
			}),
			mockReturn: &oidc.OIDCConfig{Issuer: "https://issuer.example.com"},
			validate: func(t *testing.T, config *vmcpconfig.Config, err error) {
				t.Helper()
				require.NoError(t, err)
				assert.Equal(t, "VMCP_OIDC_CLIENT_SECRET", config.IncomingAuth.OIDC.ClientSecretEnv)
			},
		},
		{
			name:          "non-inline type does not set ClientSecretEnv",
			oidcConfigRef: &mcpv1beta1.MCPOIDCConfigReference{Name: oidcConfigName, Audience: "test-audience"},
			oidcConfig:    newTestMCPOIDCConfig(mcpv1beta1.MCPOIDCConfigTypeKubernetesServiceAccount),
			mockReturn:    &oidc.OIDCConfig{Issuer: "https://kubernetes.default.svc"},
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
			mockResolver.EXPECT().ResolveFromConfigRef(
				gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
			).Return(tt.mockReturn, tt.mockErr)

			converter := newTestConverterWithObjects(t, mockResolver, tt.oidcConfig)
			ctx := log.IntoContext(context.Background(), logr.Discard())
			config, _, err := converter.Convert(ctx, newTestVMCPServer(tt.oidcConfigRef), nil)

			tt.validate(t, config, err)
		})
	}
}

// TestConverter_CompositeToolsPassThrough verifies that CompositeTools from spec.config.CompositeTools
// are correctly passed through during conversion and not dropped.
// It also verifies that Duration fields serialize to human-readable formats (e.g., "30s").
func TestConverter_CompositeToolsPassThrough(t *testing.T) {
	t.Parallel()

	vmcpServer := &mcpv1beta1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1beta1.VirtualMCPServerSpec{
			GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
			Config: vmcpconfig.Config{
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
	config, _, err := converter.Convert(ctx, vmcpServer, nil)

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

	const oidcConfigName = "test-oidc"

	tests := []struct {
		name               string
		incomingAuth       *mcpv1beta1.IncomingAuthConfig
		oidcConfig         *mcpv1beta1.MCPOIDCConfig // MCPOIDCConfig object to add to fake client
		expectedAuthType   string
		expectedOIDCConfig *vmcpconfig.OIDCConfig
		expectNilAuth      bool
		mockReturn         *oidc.OIDCConfig
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
			incomingAuth: &mcpv1beta1.IncomingAuthConfig{
				Type: "anonymous",
			},
			expectedAuthType: "anonymous",
			description:      "Should use anonymous auth when explicitly specified",
		},
		{
			name: "explicit oidc auth via MCPOIDCConfigRef",
			incomingAuth: &mcpv1beta1.IncomingAuthConfig{
				Type:          "oidc",
				OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{Name: oidcConfigName, Audience: "test-audience"},
			},
			oidcConfig: newTestMCPOIDCConfigInline(&mcpv1beta1.InlineOIDCSharedConfig{
				Issuer:   "https://example.com",
				ClientID: "test-client",
			}),
			mockReturn: &oidc.OIDCConfig{
				Issuer:   "https://example.com",
				ClientID: "test-client",
				Audience: "test-audience",
			},
			expectedAuthType: "oidc",
			expectedOIDCConfig: &vmcpconfig.OIDCConfig{
				Issuer:   "https://example.com",
				ClientID: "test-client",
				Audience: "test-audience",
			},
			description: "Should correctly convert OIDC auth config via MCPOIDCConfigRef",
		},
		{
			name: "oidc auth with scopes",
			incomingAuth: &mcpv1beta1.IncomingAuthConfig{
				Type:          "oidc",
				OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{Name: oidcConfigName, Audience: "google-audience"},
			},
			oidcConfig: newTestMCPOIDCConfigInline(&mcpv1beta1.InlineOIDCSharedConfig{
				Issuer:   "https://accounts.google.com",
				ClientID: "google-client",
			}),
			mockReturn: &oidc.OIDCConfig{
				Issuer:   "https://accounts.google.com",
				ClientID: "google-client",
				Audience: "google-audience",
				Scopes:   []string{"https://www.googleapis.com/auth/drive.readonly", "openid"},
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
		{
			name: "oidc auth with jwksUrl and introspectionUrl",
			incomingAuth: &mcpv1beta1.IncomingAuthConfig{
				Type:          "oidc",
				OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{Name: oidcConfigName, Audience: "test-audience"},
			},
			oidcConfig: newTestMCPOIDCConfigInline(&mcpv1beta1.InlineOIDCSharedConfig{
				Issuer:           "https://auth.example.com",
				ClientID:         "test-client",
				JWKSURL:          "https://auth.example.com/custom/jwks",
				IntrospectionURL: "https://auth.example.com/custom/introspect",
			}),
			mockReturn: &oidc.OIDCConfig{
				Issuer:           "https://auth.example.com",
				ClientID:         "test-client",
				Audience:         "test-audience",
				JWKSURL:          "https://auth.example.com/custom/jwks",
				IntrospectionURL: "https://auth.example.com/custom/introspect",
			},
			expectedAuthType: "oidc",
			expectedOIDCConfig: &vmcpconfig.OIDCConfig{
				Issuer:           "https://auth.example.com",
				ClientID:         "test-client",
				Audience:         "test-audience",
				JWKSURL:          "https://auth.example.com/custom/jwks",
				IntrospectionURL: "https://auth.example.com/custom/introspect",
			},
			description: "Should correctly convert OIDC auth config with jwksUrl and introspectionUrl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcpServer := &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					GroupRef:     &mcpv1beta1.MCPGroupRef{Name: "test-group"},
					IncomingAuth: tt.incomingAuth,
				},
			}

			// Set up mock resolver based on test expectations
			ctrl := gomock.NewController(t)
			mockResolver := oidcmocks.NewMockResolver(ctrl)

			// Build k8s client objects
			var objects []client.Object
			if tt.oidcConfig != nil {
				objects = append(objects, tt.oidcConfig)
			}

			// Configure mock to return expected OIDC config
			if tt.mockReturn != nil {
				mockResolver.EXPECT().ResolveFromConfigRef(
					gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(tt.mockReturn, nil)
			} else {
				mockResolver.EXPECT().ResolveFromConfigRef(
					gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
				).Return(nil, nil).AnyTimes()
			}

			converter := newTestConverterWithObjects(t, mockResolver, objects...)
			ctx := log.IntoContext(context.Background(), logr.Discard())
			config, _, err := converter.Convert(ctx, vmcpServer, nil)

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
					assert.Equal(t, tt.expectedOIDCConfig.JWKSURL, config.IncomingAuth.OIDC.JWKSURL, tt.description)
					assert.Equal(t, tt.expectedOIDCConfig.IntrospectionURL, config.IncomingAuth.OIDC.IntrospectionURL, tt.description)
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
	_ = mcpv1beta1.AddToScheme(s)
	return s
}

func TestConverter_CompositeToolRefs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		vmcp          *mcpv1beta1.VirtualMCPServer
		compositeDefs []*mcpv1beta1.VirtualMCPCompositeToolDefinition
		k8sClient     client.Client
		expectError   bool
		errorContains string
		validate      func(t *testing.T, config *vmcpconfig.Config)
	}{
		{
			name: "successfully fetch and merge referenced composite tool",
			vmcp: &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
					Config: vmcpconfig.Config{
						CompositeToolRefs: []vmcpconfig.CompositeToolRef{
							{Name: "referenced-tool"},
						},
					},
				},
			},
			compositeDefs: []*mcpv1beta1.VirtualMCPCompositeToolDefinition{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "referenced-tool",
						Namespace: "default",
					},
					Spec: mcpv1beta1.VirtualMCPCompositeToolDefinitionSpec{
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
			vmcp: &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
					Config: vmcpconfig.Config{
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
			compositeDefs: []*mcpv1beta1.VirtualMCPCompositeToolDefinition{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "referenced-tool",
						Namespace: "default",
					},
					Spec: mcpv1beta1.VirtualMCPCompositeToolDefinitionSpec{
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
			vmcp: &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
					Config: vmcpconfig.Config{
						CompositeToolRefs: []vmcpconfig.CompositeToolRef{
							{Name: "non-existent-tool"},
						},
					},
				},
			},
			compositeDefs: []*mcpv1beta1.VirtualMCPCompositeToolDefinition{},
			expectError:   true,
			errorContains: "not found",
		},
		{
			name: "error when duplicate tool names exist",
			vmcp: &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
					Config: vmcpconfig.Config{
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
			compositeDefs: []*mcpv1beta1.VirtualMCPCompositeToolDefinition{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "referenced-tool",
						Namespace: "default",
					},
					Spec: mcpv1beta1.VirtualMCPCompositeToolDefinitionSpec{
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
			vmcp: &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
				},
			},
			compositeDefs: []*mcpv1beta1.VirtualMCPCompositeToolDefinition{},
			k8sClient:     nil, // No client provided
			expectError:   true,
			errorContains: "k8sClient is required",
		},
		{
			name: "handle multiple referenced tools",
			vmcp: &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
					Config: vmcpconfig.Config{
						CompositeToolRefs: []vmcpconfig.CompositeToolRef{
							{Name: "tool1"},
							{Name: "tool2"},
						},
					},
				},
			},
			compositeDefs: []*mcpv1beta1.VirtualMCPCompositeToolDefinition{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "tool1",
						Namespace: "default",
					},
					Spec: mcpv1beta1.VirtualMCPCompositeToolDefinitionSpec{
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
					Spec: mcpv1beta1.VirtualMCPCompositeToolDefinitionSpec{
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
			vmcp: &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
					Config: vmcpconfig.Config{
						CompositeToolRefs: []vmcpconfig.CompositeToolRef{
							{Name: "referenced-tool"},
						},
					},
				},
			},
			compositeDefs: []*mcpv1beta1.VirtualMCPCompositeToolDefinition{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "referenced-tool",
						Namespace: "default",
					},
					Spec: mcpv1beta1.VirtualMCPCompositeToolDefinitionSpec{
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
			config, _, err := converter.Convert(ctx, tt.vmcp, nil)

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
	compositeDef := &mcpv1beta1.VirtualMCPCompositeToolDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "comprehensive-tool",
			Namespace: "default",
		},
		Spec: mcpv1beta1.VirtualMCPCompositeToolDefinitionSpec{
			CompositeToolConfig: expectedConfig,
		},
	}

	vmcpServer := &mcpv1beta1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1beta1.VirtualMCPServerSpec{
			GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
			Config: vmcpconfig.Config{
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
	cfg, _, err := converter.Convert(ctx, vmcpServer, nil)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Len(t, cfg.CompositeTools, 1)

	// Since the spec embeds CompositeToolConfig directly, the converted result should match
	require.Equal(t, expectedConfig, cfg.CompositeTools[0])
}

// Test helpers for MCPToolConfig tests
func newMCPToolConfig(name, namespace string, filter []string, overrides map[string]mcpv1beta1.ToolOverride) *mcpv1beta1.MCPToolConfig {
	return &mcpv1beta1.MCPToolConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       mcpv1beta1.MCPToolConfigSpec{ToolsFilter: filter, ToolsOverride: overrides},
	}
}

func toolOverride(name, desc string) mcpv1beta1.ToolOverride {
	return mcpv1beta1.ToolOverride{Name: name, Description: desc}
}

func toolOverrideWithAnnotations(name, desc string, ann *mcpv1beta1.ToolAnnotationsOverride) mcpv1beta1.ToolOverride {
	return mcpv1beta1.ToolOverride{Name: name, Description: desc, Annotations: ann}
}

func vmcpToolOverride(name, desc string) *vmcpconfig.ToolOverride {
	return &vmcpconfig.ToolOverride{Name: name, Description: desc}
}

func vmcpToolOverrideWithAnnotations(name, desc string, ann *vmcpconfig.ToolAnnotationsOverride) *vmcpconfig.ToolOverride {
	return &vmcpconfig.ToolOverride{Name: name, Description: desc, Annotations: ann}
}

func stringPtr(s string) *string { return &s }
func boolPtr(b bool) *bool       { return &b }

func TestResolveMCPToolConfig(t *testing.T) {
	t.Parallel()

	ns := "test-ns"
	tests := []struct {
		name        string
		configName  string
		existing    *mcpv1beta1.MCPToolConfig
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
				map[string]mcpv1beta1.ToolOverride{"fetch": toolOverride("renamed_fetch", "Renamed tool")}),
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
		config   *mcpv1beta1.MCPToolConfig
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
		config   *mcpv1beta1.MCPToolConfig
		expected map[string]*vmcpconfig.ToolOverride
	}{
		{
			name:     "merge when workload has none",
			existing: nil,
			config:   newMCPToolConfig("", "", nil, map[string]mcpv1beta1.ToolOverride{"tool1": toolOverride("renamed_tool1", "Renamed description")}),
			expected: map[string]*vmcpconfig.ToolOverride{"tool1": vmcpToolOverride("renamed_tool1", "Renamed description")},
		},
		{
			name:     "inline takes precedence",
			existing: map[string]*vmcpconfig.ToolOverride{"tool1": vmcpToolOverride("inline_name", "Inline description")},
			config:   newMCPToolConfig("", "", nil, map[string]mcpv1beta1.ToolOverride{"tool1": toolOverride("config_name", "Config description")}),
			expected: map[string]*vmcpconfig.ToolOverride{"tool1": vmcpToolOverride("inline_name", "Inline description")},
		},
		{
			name:     "merge non-conflicting",
			existing: map[string]*vmcpconfig.ToolOverride{"tool1": vmcpToolOverride("inline_tool1", "Inline description")},
			config:   newMCPToolConfig("", "", nil, map[string]mcpv1beta1.ToolOverride{"tool2": toolOverride("config_tool2", "Config description")}),
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
		{
			name:     "merge preserves annotation overrides from CRD",
			existing: nil,
			config: newMCPToolConfig("", "", nil, map[string]mcpv1beta1.ToolOverride{
				"tool1": toolOverrideWithAnnotations("renamed", "desc", &mcpv1beta1.ToolAnnotationsOverride{
					Title:        stringPtr("Custom Title"),
					ReadOnlyHint: boolPtr(true),
				}),
			}),
			expected: map[string]*vmcpconfig.ToolOverride{
				"tool1": vmcpToolOverrideWithAnnotations("renamed", "desc", &vmcpconfig.ToolAnnotationsOverride{
					Title:        stringPtr("Custom Title"),
					ReadOnlyHint: boolPtr(true),
				}),
			},
		},
		{
			name:     "merge preserves nil annotations",
			existing: nil,
			config: newMCPToolConfig("", "", nil, map[string]mcpv1beta1.ToolOverride{
				"tool1": toolOverride("renamed", "desc"),
			}),
			expected: map[string]*vmcpconfig.ToolOverride{
				"tool1": vmcpToolOverride("renamed", "desc"),
			},
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

func TestConvertCRDToolOverride(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    mcpv1beta1.ToolOverride
		expected *vmcpconfig.ToolOverride
	}{
		{
			name:     "name and description only",
			input:    toolOverride("renamed", "new desc"),
			expected: vmcpToolOverride("renamed", "new desc"),
		},
		{
			name: "all annotation fields converted",
			input: toolOverrideWithAnnotations("renamed", "desc", &mcpv1beta1.ToolAnnotationsOverride{
				Title:           stringPtr("My Title"),
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
			expected: vmcpToolOverrideWithAnnotations("renamed", "desc", &vmcpconfig.ToolAnnotationsOverride{
				Title:           stringPtr("My Title"),
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			name:  "title annotation only",
			input: toolOverrideWithAnnotations("renamed", "desc", &mcpv1beta1.ToolAnnotationsOverride{Title: stringPtr("Just Title")}),
			expected: vmcpToolOverrideWithAnnotations("renamed", "desc", &vmcpconfig.ToolAnnotationsOverride{
				Title: stringPtr("Just Title"),
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := convertCRDToolOverride(&tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestResolveToolConfigRefs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		tools            []*vmcpconfig.WorkloadToolConfig
		existingConfig   *mcpv1beta1.MCPToolConfig
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
				map[string]mcpv1beta1.ToolOverride{"fetch": toolOverride("renamed_fetch", "Renamed fetch")}),
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
				map[string]mcpv1beta1.ToolOverride{"fetch": toolOverride("config_fetch", "Config override")}),
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
			vmcp := &mcpv1beta1.VirtualMCPServer{
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
		existingConfig *mcpv1beta1.MCPToolConfig
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
			vmcp := &mcpv1beta1.VirtualMCPServer{
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
		vmcp           *mcpv1beta1.VirtualMCPServer
		existingConfig *mcpv1beta1.MCPToolConfig
		expectError    bool
		expectedErrMsg string
	}{
		{
			name: "Convert fails when MCPToolConfig not found",
			vmcp: &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
					Config: vmcpconfig.Config{
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
			vmcp: &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
					Config: vmcpconfig.Config{
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
			vmcp: &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
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

			config, _, err := converter.Convert(ctx, tt.vmcp, nil)

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

// TestConverter_InlineTelemetryIgnored verifies that the operator-side converter
// ignores Config.Telemetry (the standalone CLI field) and only uses TelemetryConfigRef.
func TestConverter_InlineTelemetryIgnored(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1beta1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1beta1.VirtualMCPServerSpec{
			GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
			IncomingAuth: &mcpv1beta1.IncomingAuthConfig{
				Type: "anonymous",
			},
			Config: vmcpconfig.Config{
				Telemetry: &telemetry.Config{
					Endpoint:    "otlp-collector:4317",
					ServiceName: "should-be-ignored",
				},
			},
		},
	}

	converter := newTestConverter(t, newNoOpMockResolver(t))
	ctx := log.IntoContext(context.Background(), logr.Discard())

	config, _, err := converter.Convert(ctx, vmcp, nil)
	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Nil(t, config.Telemetry, "Config.Telemetry should be ignored by the operator; use TelemetryConfigRef")
}

// TestConverter_TelemetryNil tests that nil telemetry config is handled correctly.
func TestConverter_TelemetryNil(t *testing.T) {
	t.Parallel()

	vmcp := &mcpv1beta1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1beta1.VirtualMCPServerSpec{
			GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
			IncomingAuth: &mcpv1beta1.IncomingAuthConfig{
				Type: "anonymous",
			},
			Config: vmcpconfig.Config{
				Telemetry: nil, // No telemetry config
			},
		},
	}

	converter := newTestConverter(t, newNoOpMockResolver(t))
	ctx := log.IntoContext(context.Background(), logr.Discard())

	config, _, err := converter.Convert(ctx, vmcp, nil)
	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Nil(t, config.Telemetry, "Telemetry should be nil when not configured")
}

func TestConverter_SessionStorage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		sessionStorage  *mcpv1beta1.SessionStorageConfig
		inlineConfig    *vmcpconfig.SessionStorageConfig
		expectedStorage *vmcpconfig.SessionStorageConfig
	}{
		{
			name: "redis provider populates SessionStorage",
			sessionStorage: &mcpv1beta1.SessionStorageConfig{
				Provider:  mcpv1beta1.SessionStorageProviderRedis,
				Address:   "redis:6379",
				DB:        2,
				KeyPrefix: "thv:",
			},
			expectedStorage: &vmcpconfig.SessionStorageConfig{
				Provider:  "redis",
				Address:   "redis:6379",
				DB:        2,
				KeyPrefix: "thv:",
			},
		},
		{
			name: "memory provider results in nil SessionStorage",
			sessionStorage: &mcpv1beta1.SessionStorageConfig{
				Provider: "memory",
			},
			expectedStorage: nil,
		},
		{
			name:            "nil spec.sessionStorage results in nil SessionStorage",
			sessionStorage:  nil,
			expectedStorage: nil,
		},
		{
			name:           "spec.config.sessionStorage is overwritten when spec.sessionStorage is nil",
			sessionStorage: nil,
			inlineConfig: &vmcpconfig.SessionStorageConfig{
				Provider: "redis",
				Address:  "sneaky:6379",
			},
			expectedStorage: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vmcpServer := &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
					Config: vmcpconfig.Config{
						SessionStorage: tt.inlineConfig,
					},
					SessionStorage: tt.sessionStorage,
				},
			}

			converter := newTestConverter(t, newNoOpMockResolver(t))
			ctx := log.IntoContext(context.Background(), logr.Discard())

			config, _, err := converter.Convert(ctx, vmcpServer, nil)
			require.NoError(t, err)
			require.NotNil(t, config)

			assert.Equal(t, tt.expectedStorage, config.SessionStorage)
		})
	}
}

func TestDeriveAllowedAudiences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   *vmcpconfig.Config
		expected []string
	}{
		{
			name:     "nil IncomingAuth returns nil",
			config:   &vmcpconfig.Config{},
			expected: nil,
		},
		{
			name: "nil OIDC returns nil",
			config: &vmcpconfig.Config{
				IncomingAuth: &vmcpconfig.IncomingAuthConfig{Type: "oidc"},
			},
			expected: nil,
		},
		{
			name: "Resource is used even when Audience is also set",
			config: &vmcpconfig.Config{
				IncomingAuth: &vmcpconfig.IncomingAuthConfig{
					Type: "oidc",
					OIDC: &vmcpconfig.OIDCConfig{
						Resource: "https://resource.example.com",
						Audience: "https://audience.example.com",
					},
				},
			},
			expected: []string{"https://resource.example.com"},
		},
		{
			name: "Audience alone returns nil (only Resource is used)",
			config: &vmcpconfig.Config{
				IncomingAuth: &vmcpconfig.IncomingAuthConfig{
					Type: "oidc",
					OIDC: &vmcpconfig.OIDCConfig{
						Audience: "https://audience.example.com",
					},
				},
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := deriveAllowedAudiences(tt.config)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDeriveScopesSupported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   *vmcpconfig.Config
		expected []string
	}{
		{
			name:     "nil IncomingAuth returns nil",
			config:   &vmcpconfig.Config{},
			expected: nil,
		},
		{
			name: "nil OIDC returns nil",
			config: &vmcpconfig.Config{
				IncomingAuth: &vmcpconfig.IncomingAuthConfig{Type: "oidc"},
			},
			expected: nil,
		},
		{
			name: "empty scopes returns nil (triggers auth server defaults)",
			config: &vmcpconfig.Config{
				IncomingAuth: &vmcpconfig.IncomingAuthConfig{
					Type: "oidc",
					OIDC: &vmcpconfig.OIDCConfig{Scopes: []string{}},
				},
			},
			expected: nil,
		},
		{
			name: "populated scopes are returned as-is",
			config: &vmcpconfig.Config{
				IncomingAuth: &vmcpconfig.IncomingAuthConfig{
					Type: "oidc",
					OIDC: &vmcpconfig.OIDCConfig{Scopes: []string{"openid", "upstream:github"}},
				},
			},
			expected: []string{"openid", "upstream:github"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := deriveScopesSupported(tt.config)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDeriveResourceURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   *vmcpconfig.Config
		expected string
	}{
		{
			name:     "nil IncomingAuth returns empty",
			config:   &vmcpconfig.Config{},
			expected: "",
		},
		{
			name: "nil OIDC returns empty",
			config: &vmcpconfig.Config{
				IncomingAuth: &vmcpconfig.IncomingAuthConfig{Type: "oidc"},
			},
			expected: "",
		},
		{
			name: "empty Resource returns empty",
			config: &vmcpconfig.Config{
				IncomingAuth: &vmcpconfig.IncomingAuthConfig{
					Type: "oidc",
					OIDC: &vmcpconfig.OIDCConfig{},
				},
			},
			expected: "",
		},
		{
			name: "populated Resource is returned",
			config: &vmcpconfig.Config{
				IncomingAuth: &vmcpconfig.IncomingAuthConfig{
					Type: "oidc",
					OIDC: &vmcpconfig.OIDCConfig{
						Resource: "https://resource.example.com",
					},
				},
			},
			expected: "https://resource.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := deriveResourceURL(tt.config)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestConvert_AuthServerConfigIntegration is an integration-level test that exercises the
// full Convert() path with an AuthServerConfig set on the VirtualMCPServer. It verifies that
// the returned RunConfig has the correct Issuer, Upstreams, and AllowedAudiences derived
// from the IncomingAuth OIDC audience, and that no secret values leak into the RunConfig.
func TestConvert_AuthServerConfigIntegration(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockResolver := oidcmocks.NewMockResolver(ctrl)
	mockResolver.EXPECT().ResolveFromConfigRef(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).Return(&oidc.OIDCConfig{
		Issuer:      "https://incoming-issuer.example.com",
		Audience:    "https://my-vmcp.example.com",
		ResourceURL: "https://resource.example.com",
	}, nil)

	oidcCfg := newTestMCPOIDCConfigInline(&mcpv1beta1.InlineOIDCSharedConfig{
		Issuer: "https://incoming-issuer.example.com",
	})
	k8sClient := newTestK8sClient(t, oidcCfg)
	converter, err := NewConverter(mockResolver, k8sClient)
	require.NoError(t, err)

	vmcp := &mcpv1beta1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
		Spec: mcpv1beta1.VirtualMCPServerSpec{
			GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
			IncomingAuth: &mcpv1beta1.IncomingAuthConfig{
				Type:          "oidc",
				OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{Name: "test-oidc", Audience: "https://my-vmcp.example.com"},
			},
			AuthServerConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://authserver.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{
						Name: "corp-idp",
						Type: mcpv1beta1.UpstreamProviderTypeOIDC,
						OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{
							IssuerURL: "https://corp.example.com",
							ClientID:  "corp-client-id",
							ClientSecretRef: &mcpv1beta1.SecretKeyRef{
								Name: "corp-secret",
								Key:  "client-secret",
							},
						},
					},
				},
			},
		},
	}

	ctx := log.IntoContext(context.Background(), logr.Discard())
	config, runConfig, err := converter.Convert(ctx, vmcp, nil)

	require.NoError(t, err)
	require.NotNil(t, config)
	require.NotNil(t, runConfig, "RunConfig should be non-nil when AuthServerConfig is present")

	// Verify Issuer comes from AuthServerConfig, not IncomingAuth
	assert.Equal(t, "https://authserver.example.com", runConfig.Issuer)

	// Verify AllowedAudiences derived from IncomingAuth OIDC Resource (takes precedence over Audience)
	assert.Equal(t, []string{"https://resource.example.com"}, runConfig.AllowedAudiences)

	// Verify upstream is present and uses env var, not file path
	require.Len(t, runConfig.Upstreams, 1)
	assert.Equal(t, "corp-idp", runConfig.Upstreams[0].Name)
	require.NotNil(t, runConfig.Upstreams[0].OIDCConfig)
	assert.Empty(t, runConfig.Upstreams[0].OIDCConfig.ClientSecretFile,
		"No file path for secret should be present; env var is used")
	assert.Equal(t, controllerutil.UpstreamClientSecretEnvVar+"_CORP_IDP",
		runConfig.Upstreams[0].OIDCConfig.ClientSecretEnvVar)
}

// TestConverter_TelemetryConfigRef tests that Convert uses MCPTelemetryConfig when TelemetryConfigRef is set.
// The telemetry config is now passed directly by the controller (no longer fetched by the converter).
func TestConverter_TelemetryConfigRef(t *testing.T) {
	t.Parallel()

	telemetryCfg := &mcpv1beta1.MCPTelemetryConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-telemetry", Namespace: "default"},
		Spec: mcpv1beta1.MCPTelemetryConfigSpec{
			OpenTelemetry: &mcpv1beta1.MCPTelemetryOTelConfig{
				Enabled:  true,
				Endpoint: "https://otel-collector:4317",
				Tracing: &mcpv1beta1.OpenTelemetryTracingConfig{
					Enabled:      true,
					SamplingRate: "0.5",
				},
				Metrics: &mcpv1beta1.OpenTelemetryMetricsConfig{
					Enabled: true,
				},
			},
		},
	}

	k8sClient := newTestK8sClient(t)
	converter, err := NewConverter(newNoOpMockResolver(t), k8sClient)
	require.NoError(t, err)

	vmcp := &mcpv1beta1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
		Spec: mcpv1beta1.VirtualMCPServerSpec{
			GroupRef:     &mcpv1beta1.MCPGroupRef{Name: "test-group"},
			IncomingAuth: &mcpv1beta1.IncomingAuthConfig{Type: "anonymous"},
			TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{
				Name:        "shared-telemetry",
				ServiceName: "custom-svc",
			},
		},
	}

	ctx := log.IntoContext(context.Background(), logr.Discard())
	config, _, err := converter.Convert(ctx, vmcp, telemetryCfg)
	require.NoError(t, err)
	require.NotNil(t, config)
	require.NotNil(t, config.Telemetry)

	assert.Equal(t, "custom-svc", config.Telemetry.ServiceName,
		"ServiceName should come from TelemetryConfigRef.ServiceName override")
	assert.Equal(t, "otel-collector:4317", config.Telemetry.Endpoint,
		"Endpoint should be normalized (https:// prefix stripped)")
	assert.True(t, config.Telemetry.TracingEnabled, "Tracing should be enabled from MCPTelemetryConfig")
	assert.True(t, config.Telemetry.MetricsEnabled, "Metrics should be enabled from MCPTelemetryConfig")
}

// TestConvertIncomingAuth_PrimaryUpstreamProvider verifies that convertIncomingAuth
// propagates the first configured upstream provider name into AuthzConfig so Cedar
// evaluates claims from the upstream IDP token rather than the ToolHive-issued
// AS token. Without this, policies referencing upstream claims (e.g. "department")
// fail at runtime because Cedar reads the wrong token.
func TestConvertIncomingAuth_PrimaryUpstreamProvider(t *testing.T) {
	t.Parallel()

	inlineAuthzRef := &mcpv1beta1.AuthzConfigRef{
		Type: "inline",
		Inline: &mcpv1beta1.InlineAuthzConfig{
			Policies: []string{`permit(principal, action, resource);`},
		},
	}

	tests := []struct {
		name             string
		authServerConfig *mcpv1beta1.EmbeddedAuthServerConfig
		authzConfig      *mcpv1beta1.AuthzConfigRef
		expectAuthzNil   bool
		expectedProvider string
	}{
		{
			name:             "no auth server leaves provider unset",
			authServerConfig: nil,
			authzConfig:      inlineAuthzRef,
			expectedProvider: "",
		},
		{
			name: "auth server with empty upstream list leaves provider unset",
			authServerConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer:            "https://authserver.example.com",
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{},
			},
			authzConfig:      inlineAuthzRef,
			expectedProvider: "",
		},
		{
			name: "single named upstream becomes primary",
			authServerConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://authserver.example.com",
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{Name: "okta", Type: mcpv1beta1.UpstreamProviderTypeOIDC},
				},
			},
			authzConfig:      inlineAuthzRef,
			expectedProvider: "okta",
		},
		{
			name: "empty upstream name resolves to default",
			authServerConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://authserver.example.com",
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{Name: "", Type: mcpv1beta1.UpstreamProviderTypeOIDC},
				},
			},
			authzConfig:      inlineAuthzRef,
			expectedProvider: "default",
		},
		{
			name: "first upstream wins with multiple providers",
			authServerConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://authserver.example.com",
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{Name: "okta", Type: mcpv1beta1.UpstreamProviderTypeOIDC},
					{Name: "github", Type: mcpv1beta1.UpstreamProviderTypeOAuth2},
					{Name: "google", Type: mcpv1beta1.UpstreamProviderTypeOIDC},
				},
			},
			authzConfig:      inlineAuthzRef,
			expectedProvider: "okta",
		},
		{
			name: "no authz config leaves Authz nil without panic",
			authServerConfig: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer: "https://authserver.example.com",
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{Name: "okta", Type: mcpv1beta1.UpstreamProviderTypeOIDC},
				},
			},
			authzConfig:    nil,
			expectAuthzNil: true,
		},
		{
			// Direct-IdP flow with anonymous incoming auth: neither the embedded
			// AS nor authz is configured. Converter must not panic and must leave
			// Authz unset.
			name:             "both auth server and authz nil leaves Authz nil without panic",
			authServerConfig: nil,
			authzConfig:      nil,
			expectAuthzNil:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			converter := newTestConverter(t, newNoOpMockResolver(t))

			vmcp := &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					GroupRef: &mcpv1beta1.MCPGroupRef{Name: "test-group"},
					IncomingAuth: &mcpv1beta1.IncomingAuthConfig{
						Type:        "anonymous",
						AuthzConfig: tt.authzConfig,
					},
					AuthServerConfig: tt.authServerConfig,
				},
			}

			ctx := log.IntoContext(t.Context(), logr.Discard())
			incoming, err := converter.convertIncomingAuth(ctx, vmcp)
			require.NoError(t, err)
			require.NotNil(t, incoming)

			if tt.expectAuthzNil {
				assert.Nil(t, incoming.Authz)
				return
			}

			require.NotNil(t, incoming.Authz)
			assert.Equal(t, tt.expectedProvider, incoming.Authz.PrimaryUpstreamProvider)
		})
	}
}
