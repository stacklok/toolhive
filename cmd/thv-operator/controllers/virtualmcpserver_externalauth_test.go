// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// TestConvertExternalAuthConfigToStrategy tests the conversion of MCPExternalAuthConfig to BackendAuthStrategy
func TestConvertExternalAuthConfigToStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		externalAuthConfig *mcpv1alpha1.MCPExternalAuthConfig
		expectError        bool
		validate           func(*testing.T, *authtypes.BackendAuthStrategy)
	}{
		{
			name: "token exchange with all fields",
			externalAuthConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL:                "https://oauth.example.com/token",
						ClientID:                "test-client-id",
						ClientSecretRef:         &mcpv1alpha1.SecretKeyRef{Name: "test-secret", Key: "client-secret"},
						Audience:                "backend-service",
						Scopes:                  []string{"read", "write"},
						SubjectTokenType:        "access_token",
						ExternalTokenHeaderName: "X-Upstream-Token",
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, strategy *authtypes.BackendAuthStrategy) {
				t.Helper()
				assert.Equal(t, "token_exchange", strategy.Type)
				assert.NotNil(t, strategy.TokenExchange)
				assert.Equal(t, "https://oauth.example.com/token", strategy.TokenExchange.TokenURL)
				assert.Equal(t, "test-client-id", strategy.TokenExchange.ClientID)
				// Env var name is unique per ExternalAuthConfig to avoid conflicts
				assert.Equal(t, "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET_TEST_AUTH_CONFIG", strategy.TokenExchange.ClientSecretEnv)
				assert.Equal(t, "backend-service", strategy.TokenExchange.Audience)
				assert.Equal(t, []string{"read", "write"}, strategy.TokenExchange.Scopes)
				assert.Equal(t, "urn:ietf:params:oauth:token-type:access_token", strategy.TokenExchange.SubjectTokenType)
			},
		},
		{
			name: "token exchange with minimal fields",
			externalAuthConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "minimal-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						Audience: "backend-service",
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, strategy *authtypes.BackendAuthStrategy) {
				t.Helper()
				assert.Equal(t, "token_exchange", strategy.Type)
				assert.NotNil(t, strategy.TokenExchange)
				assert.Equal(t, "https://oauth.example.com/token", strategy.TokenExchange.TokenURL)
				assert.Equal(t, "backend-service", strategy.TokenExchange.Audience)
				// Optional fields should not be present
				assert.Empty(t, strategy.TokenExchange.ClientID)
				assert.Empty(t, strategy.TokenExchange.ClientSecretEnv)
				assert.Nil(t, strategy.TokenExchange.Scopes)
				assert.Empty(t, strategy.TokenExchange.SubjectTokenType)
			},
		},
		{
			name: "token exchange with id_token type",
			externalAuthConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "id-token-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL:         "https://oauth.example.com/token",
						Audience:         "backend-service",
						SubjectTokenType: "id_token",
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, strategy *authtypes.BackendAuthStrategy) {
				t.Helper()
				assert.NotNil(t, strategy.TokenExchange)
				assert.Equal(t, "urn:ietf:params:oauth:token-type:id_token", strategy.TokenExchange.SubjectTokenType)
			},
		},
		{
			name: "token exchange with nil TokenExchange config",
			externalAuthConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nil-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					// TokenExchange is nil
				},
			},
			expectError: true,
		},
		{
			name: "header injection",
			externalAuthConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "header-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
					HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
						HeaderName: "X-API-Key",
						ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "api-key-secret",
							Key:  "api-key",
						},
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, strategy *authtypes.BackendAuthStrategy) {
				t.Helper()
				assert.Equal(t, "header_injection", strategy.Type)
				assert.NotNil(t, strategy.HeaderInjection)
				assert.Equal(t, "X-API-Key", strategy.HeaderInjection.HeaderName)
				// Secrets are mounted as env vars, not resolved into ConfigMap
				// Env var name is unique per ExternalAuthConfig to avoid conflicts
				assert.Equal(t, "TOOLHIVE_HEADER_INJECTION_VALUE_HEADER_AUTH", strategy.HeaderInjection.HeaderValueEnv)
				assert.Empty(t, strategy.HeaderInjection.HeaderValue, "HeaderValue should not be set (secrets via env vars)")
			},
		},
		{
			name: "unsupported auth type",
			externalAuthConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unsupported",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: "unsupported_type",
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			_ = mcpv1alpha1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)

			// Set up fake client (no secrets needed - secrets are mounted as env vars, not resolved)
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

			r := &VirtualMCPServerReconciler{
				Client:           fakeClient,
				Scheme:           scheme,
				PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
			}

			strategy, err := r.convertExternalAuthConfigToStrategy(tt.externalAuthConfig)

			if tt.expectError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, strategy)
			if tt.validate != nil {
				tt.validate(t, strategy)
			}
		})
	}
}

// TestBuildOutgoingAuthConfig tests the buildOutgoingAuthConfig function
func TestBuildOutgoingAuthConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		vmcp          *mcpv1alpha1.VirtualMCPServer
		mcpServers    []mcpv1alpha1.MCPServer
		authConfigs   []mcpv1alpha1.MCPExternalAuthConfig
		workloadNames []string
		expectError   bool
		validate      func(*testing.T, *vmcpconfig.OutgoingAuthConfig)
	}{
		{
			name: "discovered mode with external auth config",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{Name: "test-group"},
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Source: "discovered",
					},
				},
			},
			mcpServers: []mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
							Name: "auth-config-1",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-2",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						// No ExternalAuthConfigRef
					},
				},
			},
			authConfigs: []mcpv1alpha1.MCPExternalAuthConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "auth-config-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
						Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
						TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
							TokenURL: "https://oauth.example.com/token",
							Audience: "backend-service",
						},
					},
				},
			},
			workloadNames: []string{"backend-1", "backend-2"},
			expectError:   false,
			validate: func(t *testing.T, config *vmcpconfig.OutgoingAuthConfig) {
				t.Helper()
				assert.Equal(t, "discovered", config.Source)
				// backend-1 should have auth config
				assert.Contains(t, config.Backends, "backend-1")
				assert.Equal(t, "token_exchange", config.Backends["backend-1"].Type)
				// backend-2 should not have auth config (no ExternalAuthConfigRef)
				assert.NotContains(t, config.Backends, "backend-2")
			},
		},
		{
			name: "discovered mode with inline overrides",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{Name: "test-group"},
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Source: "discovered",
						Backends: map[string]mcpv1alpha1.BackendAuthConfig{
							"backend-1": {
								Type: mcpv1alpha1.BackendAuthTypeExternalAuthConfigRef,
								ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
									Name: "auth-config-override",
								},
							},
						},
					},
				},
			},
			mcpServers: []mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
							Name: "auth-config-1",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-2",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
							Name: "auth-config-2",
						},
					},
				},
			},
			authConfigs: []mcpv1alpha1.MCPExternalAuthConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "auth-config-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
						Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
						TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
							TokenURL: "https://oauth.example.com/token",
							Audience: "backend-service",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "auth-config-2",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
						Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
						TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
							TokenURL: "https://oauth2.example.com/token",
							Audience: "backend-service-2",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "auth-config-override",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
						Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
						TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
							TokenURL: "https://oauth-override.example.com/token",
							Audience: "backend-service-override",
						},
					},
				},
			},
			workloadNames: []string{"backend-1", "backend-2"},
			expectError:   false,
			validate: func(t *testing.T, config *vmcpconfig.OutgoingAuthConfig) {
				t.Helper()
				assert.Equal(t, "discovered", config.Source)
				// backend-1 should use inline override, not discovered
				assert.Contains(t, config.Backends, "backend-1")
				assert.Equal(t, "token_exchange", config.Backends["backend-1"].Type)
				assert.NotNil(t, config.Backends["backend-1"].TokenExchange)
				assert.Equal(t, "https://oauth-override.example.com/token", config.Backends["backend-1"].TokenExchange.TokenURL)
				// backend-2 should use discovered config
				assert.Contains(t, config.Backends, "backend-2")
				assert.Equal(t, "token_exchange", config.Backends["backend-2"].Type)
			},
		},
		{
			name: "inline mode ignores discovered configs",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{Name: "test-group"},
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Source: "inline",
						Backends: map[string]mcpv1alpha1.BackendAuthConfig{
							"backend-1": {
								Type: mcpv1alpha1.BackendAuthTypeExternalAuthConfigRef,
								ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
									Name: "auth-config-1",
								},
							},
						},
					},
				},
			},
			mcpServers: []mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
							Name: "auth-config-1",
						},
					},
				},
			},
			authConfigs: []mcpv1alpha1.MCPExternalAuthConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "auth-config-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
						Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
						TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
							TokenURL: "https://oauth.example.com/token",
							Audience: "backend-service",
						},
					},
				},
			},
			workloadNames: []string{"backend-1"},
			expectError:   false,
			validate: func(t *testing.T, config *vmcpconfig.OutgoingAuthConfig) {
				t.Helper()
				assert.Equal(t, "inline", config.Source)
				// Only inline config should be present
				assert.Contains(t, config.Backends, "backend-1")
				assert.Equal(t, "token_exchange", config.Backends["backend-1"].Type)
			},
		},
		{
			name: "default auth config",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{Name: "test-group"},
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Source: "discovered",
						Default: &mcpv1alpha1.BackendAuthConfig{
							Type: mcpv1alpha1.BackendAuthTypeExternalAuthConfigRef,
							ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
								Name: "default-auth-config",
							},
						},
					},
				},
			},
			authConfigs: []mcpv1alpha1.MCPExternalAuthConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "default-auth-config",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
						Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
						TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
							TokenURL: "https://oauth.example.com/token",
							Audience: "backend-service",
						},
					},
				},
			},
			workloadNames: []string{},
			expectError:   false,
			validate: func(t *testing.T, config *vmcpconfig.OutgoingAuthConfig) {
				t.Helper()
				assert.NotNil(t, config.Default)
				assert.Equal(t, "token_exchange", config.Default.Type)
			},
		},
		{
			name: "inline mode with ExternalAuthConfigRef",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{Name: "test-group"},
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Source: "inline",
						Backends: map[string]mcpv1alpha1.BackendAuthConfig{
							"backend-1": {
								Type: mcpv1alpha1.BackendAuthTypeExternalAuthConfigRef,
								ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
									Name: "auth-config-1",
								},
							},
						},
					},
				},
			},
			authConfigs: []mcpv1alpha1.MCPExternalAuthConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "auth-config-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
						Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
						TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
							TokenURL: "https://oauth.example.com/token",
							Audience: "backend-service",
							ClientID: "test-client",
						},
					},
				},
			},
			workloadNames: []string{},
			expectError:   false,
			validate: func(t *testing.T, config *vmcpconfig.OutgoingAuthConfig) {
				t.Helper()
				assert.Contains(t, config.Backends, "backend-1")
				assert.Equal(t, "token_exchange", config.Backends["backend-1"].Type)
				assert.NotNil(t, config.Backends["backend-1"].TokenExchange)
				assert.Equal(t, "https://oauth.example.com/token", config.Backends["backend-1"].TokenExchange.TokenURL)
				assert.Equal(t, "test-client", config.Backends["backend-1"].TokenExchange.ClientID)
			},
		},
		{
			name: "missing ExternalAuthConfig should be skipped gracefully",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{Name: "test-group"},
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Source: "discovered",
					},
				},
			},
			mcpServers: []mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "backend-1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
							Name: "missing-auth-config",
						},
					},
				},
			},
			workloadNames: []string{"backend-1"},
			expectError:   false,
			validate: func(t *testing.T, config *vmcpconfig.OutgoingAuthConfig) {
				t.Helper()
				// Should not have backend-1 in config since ExternalAuthConfig is missing
				assert.NotContains(t, config.Backends, "backend-1")
			},
		},
		{
			name: "defaults to discovered mode when source not specified",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{Name: "test-group"},
					// No OutgoingAuth specified
				},
			},
			workloadNames: []string{},
			expectError:   false,
			validate: func(t *testing.T, config *vmcpconfig.OutgoingAuthConfig) {
				t.Helper()
				assert.Equal(t, "discovered", config.Source)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			_ = mcpv1alpha1.AddToScheme(scheme)

			// Build objects list for fake client
			objects := []client.Object{tt.vmcp}
			for i := range tt.mcpServers {
				objects = append(objects, &tt.mcpServers[i])
			}
			for i := range tt.authConfigs {
				objects = append(objects, &tt.authConfigs[i])
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				Build()

			r := &VirtualMCPServerReconciler{
				Client:           fakeClient,
				Scheme:           scheme,
				PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
			}

			ctx := context.Background()
			config, err := r.buildOutgoingAuthConfig(ctx, tt.vmcp, tt.workloadNames)

			if tt.expectError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, config)
			if tt.validate != nil {
				tt.validate(t, config)
			}
		})
	}
}

// TestConvertBackendAuthConfigToVMCP tests the convertBackendAuthConfigToVMCP function
func TestConvertBackendAuthConfigToVMCP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		crdConfig   *mcpv1alpha1.BackendAuthConfig
		authConfigs []mcpv1alpha1.MCPExternalAuthConfig
		expectError bool
		validate    func(*testing.T, *authtypes.BackendAuthStrategy)
	}{
		{
			name: "external_auth_config_ref type",
			crdConfig: &mcpv1alpha1.BackendAuthConfig{
				Type: mcpv1alpha1.BackendAuthTypeExternalAuthConfigRef,
				ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
					Name: "test-auth-config",
				},
			},
			authConfigs: []mcpv1alpha1.MCPExternalAuthConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-auth-config",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
						Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
						TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
							TokenURL: "https://oauth.example.com/token",
							Audience: "backend-service",
							ClientID: "test-client",
						},
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, strategy *authtypes.BackendAuthStrategy) {
				t.Helper()
				assert.Equal(t, "token_exchange", strategy.Type)
				assert.NotNil(t, strategy.TokenExchange)
				assert.Equal(t, "https://oauth.example.com/token", strategy.TokenExchange.TokenURL)
				assert.Equal(t, "backend-service", strategy.TokenExchange.Audience)
				assert.Equal(t, "test-client", strategy.TokenExchange.ClientID)
			},
		},
		{
			name: "missing ExternalAuthConfig",
			crdConfig: &mcpv1alpha1.BackendAuthConfig{
				Type: mcpv1alpha1.BackendAuthTypeExternalAuthConfigRef,
				ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
					Name: "missing-config",
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			_ = mcpv1alpha1.AddToScheme(scheme)

			objects := []client.Object{}
			for i := range tt.authConfigs {
				objects = append(objects, &tt.authConfigs[i])
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				Build()

			r := &VirtualMCPServerReconciler{
				Client:           fakeClient,
				Scheme:           scheme,
				PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
			}

			ctx := context.Background()
			strategy, err := r.convertBackendAuthConfigToVMCP(ctx, "default", tt.crdConfig)

			if tt.expectError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, strategy)
			if tt.validate != nil {
				tt.validate(t, strategy)
			}
		})
	}
}

// TestDiscoverBackendsWithExternalAuthConfigIntegration tests the full integration
// of ExternalAuthConfig discovery and resolution in the discoverBackends flow
func TestDiscoverBackendsWithExternalAuthConfigIntegration(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)

	// Create a VirtualMCPServer with discovered mode
	vmcp := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			GroupRef: mcpv1alpha1.GroupRef{Name: "test-group"},
			OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
				Source: "discovered",
			},
		},
	}

	// Create MCPGroup
	mcpGroup := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-group",
			Namespace: "default",
		},
		Status: mcpv1alpha1.MCPGroupStatus{
			Phase:   mcpv1alpha1.MCPGroupPhaseReady,
			Servers: []string{"backend-1", "backend-2"},
		},
	}

	// Create MCPServers - one with ExternalAuthConfig, one without
	mcpServers := []mcpv1alpha1.MCPServer{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "backend-1",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				GroupRef:  "test-group",
				Image:     "test-image:latest",
				Transport: "streamable-http",
				ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
					Name: "auth-config-1",
				},
			},
			Status: mcpv1alpha1.MCPServerStatus{
				Phase: mcpv1alpha1.MCPServerPhaseRunning,
				URL:   "http://mcp-backend-1-proxy.default.svc.cluster.local:8080",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "backend-2",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				GroupRef:  "test-group",
				Image:     "test-image:latest",
				Transport: "streamable-http",
				// No ExternalAuthConfigRef
			},
			Status: mcpv1alpha1.MCPServerStatus{
				Phase: mcpv1alpha1.MCPServerPhaseRunning,
				URL:   "http://mcp-backend-2-proxy.default.svc.cluster.local:8080",
			},
		},
	}

	// Create ExternalAuthConfig
	authConfig := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "auth-config-1",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
				TokenURL:         "https://oauth.example.com/token",
				ClientID:         "test-client-id",
				Audience:         "backend-service",
				Scopes:           []string{"read", "write"},
				SubjectTokenType: "access_token",
			},
		},
	}

	// Build objects list for fake client
	objects := []client.Object{vmcp, mcpGroup, authConfig}
	for i := range mcpServers {
		objects = append(objects, &mcpServers[i])
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		Build()

	r := &VirtualMCPServerReconciler{
		Client:           fakeClient,
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	ctx := context.Background()

	// Test that buildOutgoingAuthConfig correctly discovers and converts ExternalAuthConfig
	workloadNames := []string{"backend-1", "backend-2"}
	authConfigResult, err := r.buildOutgoingAuthConfig(ctx, vmcp, workloadNames)
	require.NoError(t, err)
	require.NotNil(t, authConfigResult)

	// Verify that backend-1 has token_exchange auth configured
	assert.Contains(t, authConfigResult.Backends, "backend-1")
	assert.Equal(t, "token_exchange", authConfigResult.Backends["backend-1"].Type)
	assert.NotNil(t, authConfigResult.Backends["backend-1"].TokenExchange)
	assert.Equal(t, "https://oauth.example.com/token", authConfigResult.Backends["backend-1"].TokenExchange.TokenURL)
	assert.Equal(t, "test-client-id", authConfigResult.Backends["backend-1"].TokenExchange.ClientID)
	assert.Equal(t, "backend-service", authConfigResult.Backends["backend-1"].TokenExchange.Audience)

	// Verify that backend-2 does not have auth config (no ExternalAuthConfigRef)
	assert.NotContains(t, authConfigResult.Backends, "backend-2")

	// Test that discoverBackends uses the auth config
	discoveredBackends, err := r.discoverBackends(ctx, vmcp)
	require.NoError(t, err)

	// Verify that backend-1 has authConfigRef in status
	backend1Found := false
	for _, backend := range discoveredBackends {
		if backend.Name == "backend-1" {
			backend1Found = true
			assert.Equal(t, "auth-config-1", backend.AuthConfigRef)
			assert.Equal(t, mcpv1alpha1.BackendAuthTypeExternalAuthConfigRef, backend.AuthType)
			break
		}
	}
	assert.True(t, backend1Found, "backend-1 should be discovered")

	// Verify that backend-2 does not have authConfigRef
	backend2Found := false
	for _, backend := range discoveredBackends {
		if backend.Name == "backend-2" {
			backend2Found = true
			assert.Empty(t, backend.AuthConfigRef)
			assert.Empty(t, backend.AuthType)
			break
		}
	}
	assert.True(t, backend2Found, "backend-2 should be discovered")
}

// TestGenerateUniqueTokenExchangeEnvVarName tests the generateUniqueTokenExchangeEnvVarName function
func TestGenerateUniqueTokenExchangeEnvVarName(t *testing.T) {
	t.Parallel()

	expectedPrefix := "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET"
	tests := []struct {
		name       string
		configName string

		expectedSuffix string
	}{
		{
			name:           "simple config name",
			configName:     "test-auth",
			expectedSuffix: "TEST_AUTH",
		},
		{
			name:           "config name with hyphens",
			configName:     "my-oauth-config",
			expectedSuffix: "MY_OAUTH_CONFIG",
		},
		{
			name:           "config name with special characters",
			configName:     "test@auth#config",
			expectedSuffix: "TEST_AUTH_CONFIG",
		},
		{
			name:           "config name with numbers",
			configName:     "auth-config-123",
			expectedSuffix: "AUTH_CONFIG_123",
		},
		{
			name:           "config name with mixed case",
			configName:     "MyOAuthConfig",
			expectedSuffix: "MYOAUTHCONFIG",
		},
		{
			name:           "single character",
			configName:     "a",
			expectedSuffix: "A",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ctrlutil.GenerateUniqueTokenExchangeEnvVarName(tt.configName)
			assert.Contains(t, result, expectedPrefix)
			assert.Contains(t, result, tt.expectedSuffix)
			// Verify format: PREFIX_SUFFIX
			assert.Contains(t, result, "_")
			// Verify all characters are valid for env vars (uppercase, alphanumeric, underscore)
			envVarPattern := regexp.MustCompile(`^[A-Z0-9_]+$`)
			assert.Regexp(t, envVarPattern, result, "Result should be a valid environment variable name")
		})
	}
}

// TestGenerateUniqueHeaderInjectionEnvVarName tests the generateUniqueHeaderInjectionEnvVarName function
func TestGenerateUniqueHeaderInjectionEnvVarName(t *testing.T) {
	t.Parallel()

	expectedPrefix := "TOOLHIVE_HEADER_INJECTION_VALUE"
	tests := []struct {
		name           string
		configName     string
		expectedSuffix string
	}{
		{
			name:           "simple config name",
			configName:     "header-auth",
			expectedSuffix: "HEADER_AUTH",
		},
		{
			name:           "config name with hyphens",
			configName:     "my-api-key-config",
			expectedSuffix: "MY_API_KEY_CONFIG",
		},
		{
			name:           "config name with special characters",
			configName:     "test@header#config",
			expectedSuffix: "TEST_HEADER_CONFIG",
		},
		{
			name:           "config name with numbers",
			configName:     "header-config-456",
			expectedSuffix: "HEADER_CONFIG_456",
		},
		{
			name:           "config name with mixed case",
			configName:     "MyHeaderConfig",
			expectedSuffix: "MYHEADERCONFIG",
		},
		{
			name:           "single character",
			configName:     "x",
			expectedSuffix: "X",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ctrlutil.GenerateUniqueHeaderInjectionEnvVarName(tt.configName)
			assert.True(t, strings.HasPrefix(result, expectedPrefix+"_"), "Result should start with prefix")
			assert.True(t, strings.HasSuffix(result, tt.expectedSuffix), "Result should end with suffix")
			// Verify format: PREFIX_SUFFIX
			assert.Contains(t, result, "_")
			// Verify all characters are valid for env vars (uppercase, alphanumeric, underscore)
			envVarPattern := regexp.MustCompile(`^[A-Z0-9_]+$`)
			assert.Regexp(t, envVarPattern, result, "Result should be a valid environment variable name")
		})
	}
}
