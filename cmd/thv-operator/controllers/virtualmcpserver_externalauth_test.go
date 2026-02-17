// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
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
		name             string
		vmcp             *mcpv1alpha1.VirtualMCPServer
		mcpServers       []mcpv1alpha1.MCPServer
		authConfigs      []mcpv1alpha1.MCPExternalAuthConfig
		workloadNames    []workloads.TypedWorkload
		expectAuthErrors bool // Set to true if test expects auth config errors (non-fatal)
		validate         func(*testing.T, *vmcpconfig.OutgoingAuthConfig)
		validateErrors   func(*testing.T, []AuthConfigError) // Validate all auth errors (default, backend-specific, discovered)
	}{
		{
			name: "discovered mode with external auth config",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{Group: "test-group"},
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
			workloadNames: []workloads.TypedWorkload{
				{
					Name: "backend-1",
					Type: workloads.WorkloadTypeMCPServer,
				},
				{
					Name: "backend-2",
					Type: workloads.WorkloadTypeMCPServer,
				},
			},
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
					Config: vmcpconfig.Config{Group: "test-group"},
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
			workloadNames: []workloads.TypedWorkload{
				{
					Name: "backend-1",
					Type: workloads.WorkloadTypeMCPServer,
				},
				{
					Name: "backend-2",
					Type: workloads.WorkloadTypeMCPServer,
				},
			},
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
					Config: vmcpconfig.Config{Group: "test-group"},
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
			workloadNames: []workloads.TypedWorkload{
				{
					Name: "backend-1",
					Type: workloads.WorkloadTypeMCPServer,
				},
			},
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
					Config: vmcpconfig.Config{Group: "test-group"},
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
			workloadNames: []workloads.TypedWorkload{},
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
					Config: vmcpconfig.Config{Group: "test-group"},
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
			workloadNames: []workloads.TypedWorkload{},
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
					Config: vmcpconfig.Config{Group: "test-group"},
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
			workloadNames: []workloads.TypedWorkload{
				{
					Name: "backend-1",
					Type: workloads.WorkloadTypeMCPServer,
				},
			},
			expectAuthErrors: true, // New behavior: discovered errors are returned
			validate: func(t *testing.T, config *vmcpconfig.OutgoingAuthConfig) {
				t.Helper()
				// Should not have backend-1 in config since ExternalAuthConfig is missing
				assert.NotContains(t, config.Backends, "backend-1")
			},
			validateErrors: func(t *testing.T, errors []AuthConfigError) {
				t.Helper()
				require.Len(t, errors, 1, "expected exactly one discovered auth error")
				authErr := errors[0]
				assert.Equal(t, "discovered:backend-1", authErr.Context)
				assert.Equal(t, "backend-1", authErr.BackendName)
				assert.Error(t, authErr.Error)
				assert.Contains(t, authErr.Error.Error(), "missing-auth-config")
				assert.Contains(t, authErr.Error.Error(), "not found")
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
					Config: vmcpconfig.Config{Group: "test-group"},
					// No OutgoingAuth specified
				},
			},
			workloadNames: []workloads.TypedWorkload{},
			validate: func(t *testing.T, config *vmcpconfig.OutgoingAuthConfig) {
				t.Helper()
				assert.Equal(t, "discovered", config.Source)
			},
		},
		{
			name: "default auth config error is collected but doesn't fail reconciliation",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{Group: "test-group"},
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Source: "discovered",
						Default: &mcpv1alpha1.BackendAuthConfig{
							Type: "external_auth_config_ref",
							ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
								Name: "missing-default-auth", // Auth config doesn't exist
							},
						},
					},
				},
			},
			workloadNames:    []workloads.TypedWorkload{},
			expectAuthErrors: true, // Should collect default auth error
			validateErrors: func(t *testing.T, errors []AuthConfigError) {
				t.Helper()
				require.Len(t, errors, 1, "expected exactly one auth error")
				authErr := errors[0]
				assert.Equal(t, "default", authErr.Context)
				assert.Empty(t, authErr.BackendName)
				assert.Error(t, authErr.Error)
				assert.Contains(t, authErr.Error.Error(), "failed to convert default auth config")
			},
			validate: func(t *testing.T, config *vmcpconfig.OutgoingAuthConfig) {
				t.Helper()
				// Default auth should not be set due to error
				assert.Nil(t, config.Default)
			},
		},
		{
			name: "backend-specific auth config error is collected but doesn't fail reconciliation",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vmcp",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					Config: vmcpconfig.Config{Group: "test-group"},
					OutgoingAuth: &mcpv1alpha1.OutgoingAuthConfig{
						Source: "discovered",
						Backends: map[string]mcpv1alpha1.BackendAuthConfig{
							"api-backend": {
								Type: "external_auth_config_ref",
								ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
									Name: "missing-backend-auth",
								},
							},
						},
					},
				},
			},
			workloadNames:    []workloads.TypedWorkload{},
			expectAuthErrors: true, // Should collect backend-specific auth error
			validateErrors: func(t *testing.T, errors []AuthConfigError) {
				t.Helper()
				require.Len(t, errors, 1, "expected exactly one auth error")
				authErr := errors[0]
				assert.Equal(t, "backend:api-backend", authErr.Context)
				assert.Equal(t, "api-backend", authErr.BackendName)
				assert.Error(t, authErr.Error)
				assert.Contains(t, authErr.Error.Error(), "failed to convert backend auth config")
			},
			validate: func(t *testing.T, config *vmcpconfig.OutgoingAuthConfig) {
				t.Helper()
				// Backend-specific auth should not be set due to error
				assert.NotContains(t, config.Backends, "api-backend")
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
			config, _, allAuthErrors := r.buildOutgoingAuthConfig(ctx, tt.vmcp, tt.workloadNames)

			require.NotNil(t, config)

			// Check auth config errors (default, backend-specific, discovered)
			if tt.expectAuthErrors {
				require.NotEmpty(t, allAuthErrors, "expected auth config errors but got none")
				if tt.validateErrors != nil {
					tt.validateErrors(t, allAuthErrors)
				}
			} else {
				require.Empty(t, allAuthErrors, "unexpected auth config errors")
			}

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
