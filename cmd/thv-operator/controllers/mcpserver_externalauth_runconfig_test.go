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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/oidc"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// TestAddExternalAuthConfigOptions tests the addExternalAuthConfigOptions function
func TestAddExternalAuthConfigOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		mcpServer      *mcpv1alpha1.MCPServer
		externalAuth   *mcpv1alpha1.MCPExternalAuthConfig
		clientSecret   *corev1.Secret
		oidcConfig     *oidc.OIDCConfig // OIDC config for embedded auth server
		expectError    bool
		errContains    string
		validateConfig func(*testing.T, []runner.RunConfigBuilderOption)
	}{
		{
			name: "no external auth config reference",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					// No ExternalAuthConfigRef
				},
			},
			expectError: false,
			validateConfig: func(t *testing.T, opts []runner.RunConfigBuilderOption) {
				t.Helper()
				// Should have no options added
				assert.Len(t, opts, 0)
			},
		},
		{
			name: "valid token exchange configuration with all fields",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "test-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "test-client-id",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "oauth-secret",
							Key:  "client-secret",
						},
						Audience:                "backend-service",
						Scopes:                  []string{"read", "write", "admin"},
						ExternalTokenHeaderName: "X-Original-Authorization",
					},
				},
			},
			clientSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "oauth-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"client-secret": []byte("super-secret-value"),
				},
			},
			expectError: false,
			validateConfig: func(t *testing.T, opts []runner.RunConfigBuilderOption) {
				t.Helper()
				assert.Len(t, opts, 1, "Should have one middleware config option")
			},
		},
		{
			name: "valid token exchange with minimal configuration",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "minimal-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "minimal-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "minimal-client",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "minimal-secret",
							Key:  "secret-key",
						},
						Audience: "api",
						// No scope, no external token header
					},
				},
			},
			clientSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "minimal-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"secret-key": []byte("secret"),
				},
			},
			expectError: false,
			validateConfig: func(t *testing.T, opts []runner.RunConfigBuilderOption) {
				t.Helper()
				assert.Len(t, opts, 1)
			},
		},
		{
			name: "external auth config not found",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "non-existent",
					},
				},
			},
			expectError: true,
			errContains: "failed to get MCPExternalAuthConfig",
		},
		{
			name: "unsupported external auth type",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "unsupported-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unsupported-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: "unsupported-type",
				},
			},
			expectError: true,
			errContains: "unsupported external auth type",
		},
		{
			name: "valid embedded auth server configuration",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "embedded-auth-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "embedded-auth-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &mcpv1alpha1.EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
							{Name: "signing-key", Key: "private.pem"},
						},
						HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
							{Name: "hmac-secret", Key: "hmac"},
						},
						UpstreamProviders: []mcpv1alpha1.UpstreamProviderConfig{
							{
								Name: "okta",
								Type: mcpv1alpha1.UpstreamProviderTypeOIDC,
								OIDCConfig: &mcpv1alpha1.OIDCUpstreamConfig{
									IssuerURL:   "https://okta.example.com",
									ClientID:    "client-id",
									RedirectURI: "https://auth.example.com/callback",
								},
							},
						},
					},
				},
			},
			oidcConfig: &oidc.OIDCConfig{
				ResourceURL: "http://test-server.default.svc.cluster.local:8080",
				Scopes:      []string{"openid", "offline_access"},
			},
			expectError: false,
			validateConfig: func(t *testing.T, opts []runner.RunConfigBuilderOption) {
				t.Helper()
				assert.Len(t, opts, 1, "Should have one embedded auth server config option")
			},
		},
		{
			name: "embedded auth server with nil embedded config",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "bad-embedded-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "bad-embedded-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type:               mcpv1alpha1.ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: nil, // Missing embedded config
				},
			},
			oidcConfig: &oidc.OIDCConfig{
				ResourceURL: "http://test-server.default.svc.cluster.local:8080",
				Scopes:      []string{"openid"},
			},
			expectError: true,
			errContains: "embedded auth server configuration is nil",
		},
		{
			name: "embedded auth server without OIDC config fails",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "embedded-auth-config-no-oidc",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "embedded-auth-config-no-oidc",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &mcpv1alpha1.EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
							{Name: "signing-key", Key: "private.pem"},
						},
						HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
							{Name: "hmac-secret", Key: "hmac"},
						},
					},
				},
			},
			oidcConfig:  nil, // No OIDC config
			expectError: true,
			errContains: "OIDC config is required for embedded auth server",
		},
		{
			name: "embedded auth server without resourceUrl fails",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "embedded-auth-config-no-resource",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "embedded-auth-config-no-resource",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &mcpv1alpha1.EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
							{Name: "signing-key", Key: "private.pem"},
						},
						HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
							{Name: "hmac-secret", Key: "hmac"},
						},
					},
				},
			},
			oidcConfig: &oidc.OIDCConfig{
				ResourceURL: "", // Empty resource URL
				Scopes:      []string{"openid"},
			},
			expectError: true,
			errContains: "OIDC config resourceUrl is required for embedded auth server",
		},
		{
			name: "token exchange spec is nil",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "nil-spec-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nil-spec-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type:          mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: nil,
				},
			},
			expectError: true,
			errContains: "token exchange configuration is nil",
		},
		{
			name: "client secret not found",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "no-secret-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "no-secret-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "client",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "non-existent-secret",
							Key:  "key",
						},
						Audience: "api",
					},
				},
			},
			expectError: true,
			errContains: "failed to get client secret",
		},
		{
			name: "secret missing required key",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "missing-key-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "missing-key-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "client",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "incomplete-secret",
							Key:  "missing-key",
						},
						Audience: "api",
					},
				},
			},
			clientSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "incomplete-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"other-key": []byte("value"),
				},
			},
			expectError: true,
			errContains: "is missing key",
		},
		{
			name: "empty scope string",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "empty-scope-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "empty-scope-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "client",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "secret",
							Key:  "key",
						},
						Audience: "api",
						Scopes:   []string{}, // Empty scopes
					},
				},
			},
			clientSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"key": []byte("secret"),
				},
			},
			expectError: false,
			validateConfig: func(t *testing.T, opts []runner.RunConfigBuilderOption) {
				t.Helper()
				assert.Len(t, opts, 1)
			},
		},
		{
			name: "token exchange without client credentials (GCP Workforce Identity)",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "gcp-workforce-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "gcp-workforce-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://sts.googleapis.com/v1/token",
						Audience: "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/pool/providers/provider",
						Scopes:   []string{"https://www.googleapis.com/auth/cloud-platform"},
						// No ClientID or ClientSecretRef - optional for Workforce Identity
					},
				},
			},
			expectError: false,
			validateConfig: func(t *testing.T, opts []runner.RunConfigBuilderOption) {
				t.Helper()
				assert.Len(t, opts, 1, "Should have one middleware config option")
			},
		},
		{
			name: "token exchange with empty client ID but no secret ref",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "empty-client-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "empty-client-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://sts.googleapis.com/v1/token",
						ClientID: "", // Empty string
						Audience: "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/pool/providers/provider",
						Scopes:   []string{"scope1"},
						// ClientSecretRef is nil
					},
				},
			},
			expectError: false,
			validateConfig: func(t *testing.T, opts []runner.RunConfigBuilderOption) {
				t.Helper()
				assert.Len(t, opts, 1)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := createRunConfigTestScheme()
			objects := []runtime.Object{tt.mcpServer}
			if tt.externalAuth != nil {
				objects = append(objects, tt.externalAuth)
			}
			if tt.clientSecret != nil {
				objects = append(objects, tt.clientSecret)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

			ctx := t.Context()
			var options []runner.RunConfigBuilderOption

			err := ctrlutil.AddExternalAuthConfigOptions(ctx, reconciler.Client, tt.mcpServer.Namespace, tt.mcpServer.Spec.ExternalAuthConfigRef, tt.oidcConfig, &options)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				if tt.validateConfig != nil {
					tt.validateConfig(t, options)
				}
			}
		})
	}
}

// TestCreateRunConfigFromMCPServer_WithExternalAuth tests RunConfig generation with external auth
func TestCreateRunConfigFromMCPServer_WithExternalAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		mcpServer    *mcpv1alpha1.MCPServer
		externalAuth *mcpv1alpha1.MCPExternalAuthConfig
		clientSecret *corev1.Secret
		expectError  bool
		validate     func(*testing.T, *runner.RunConfig)
	}{
		{
			name: "with external auth token exchange",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "external-auth-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "test:v1",
					Transport: "stdio",
					Port:      8080,
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "oauth-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "oauth-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "my-client-id",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "oauth-creds",
							Key:  "client-secret",
						},
						Audience: "backend-api",
						Scopes:   []string{"read", "write"},
					},
				},
			},
			clientSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "oauth-creds",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"client-secret": []byte("secret123"),
				},
			},
			expectError: false,
			validate: func(t *testing.T, config *runner.RunConfig) {
				t.Helper()
				assert.Equal(t, "external-auth-server", config.Name)
				assert.Equal(t, "test:v1", config.Image)

				// Verify middleware configs are populated (auth, tokenexchange, mcp-parser, usagemetrics)
				assert.NotNil(t, config.MiddlewareConfigs)
				assert.GreaterOrEqual(t, len(config.MiddlewareConfigs), 1, "Should have at least tokenexchange middleware")

				// Find the tokenexchange middleware
				var tokenExchangeMw *types.MiddlewareConfig
				for i := range config.MiddlewareConfigs {
					if config.MiddlewareConfigs[i].Type == "tokenexchange" {
						tokenExchangeMw = &config.MiddlewareConfigs[i]
						break
					}
				}
				require.NotNil(t, tokenExchangeMw, "tokenexchange middleware should be present")

				// Verify middleware parameters
				var params map[string]interface{}
				err := json.Unmarshal(tokenExchangeMw.Parameters, &params)
				require.NoError(t, err)

				tokenExchangeConfig, ok := params["token_exchange_config"].(map[string]interface{})
				require.True(t, ok)
				assert.Equal(t, "https://oauth.example.com/token", tokenExchangeConfig["token_url"])
				assert.Equal(t, "my-client-id", tokenExchangeConfig["client_id"])
				assert.Equal(t, "backend-api", tokenExchangeConfig["audience"])
			},
		},
		{
			name: "external auth config not found returns error",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "broken-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "test:v1",
					Transport: "stdio",
					Port:      8080,
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "non-existent",
					},
				},
			},
			expectError: true,
		},
		{
			name: "with external auth embedded auth server",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "embedded-auth-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "test:v1",
					Transport: "stdio",
					Port:      8080,
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "embedded-auth-config",
					},
					// OIDCConfig is required for embedded auth server
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type:        mcpv1alpha1.OIDCConfigTypeInline,
						ResourceURL: "http://embedded-auth-server.default.svc.cluster.local:8080",
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://kubernetes.default.svc",
							Audience: "toolhive",
							Scopes:   []string{"openid", "offline_access", "mcp:tools"},
						},
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "embedded-auth-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &mcpv1alpha1.EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
							{Name: "signing-key", Key: "private.pem"},
						},
						HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
							{Name: "hmac-secret", Key: "hmac"},
						},
						TokenLifespans: &mcpv1alpha1.TokenLifespanConfig{
							AccessTokenLifespan:  "30m",
							RefreshTokenLifespan: "168h",
							AuthCodeLifespan:     "5m",
						},
						UpstreamProviders: []mcpv1alpha1.UpstreamProviderConfig{
							{
								Name: "okta",
								Type: mcpv1alpha1.UpstreamProviderTypeOIDC,
								OIDCConfig: &mcpv1alpha1.OIDCUpstreamConfig{
									IssuerURL:   "https://okta.example.com",
									ClientID:    "my-client-id",
									RedirectURI: "https://auth.example.com/callback",
									Scopes:      []string{"openid", "profile", "email"},
								},
							},
						},
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, config *runner.RunConfig) {
				t.Helper()
				assert.Equal(t, "embedded-auth-server", config.Name)
				assert.Equal(t, "test:v1", config.Image)

				// Verify embedded auth server config is present
				require.NotNil(t, config.EmbeddedAuthServerConfig, "embedded auth server config should be present")
				assert.Equal(t, "https://auth.example.com", config.EmbeddedAuthServerConfig.Issuer)

				// Verify signing key config
				require.NotNil(t, config.EmbeddedAuthServerConfig.SigningKeyConfig)
				assert.Equal(t, "/etc/toolhive/authserver/keys", config.EmbeddedAuthServerConfig.SigningKeyConfig.KeyDir)

				// Verify token lifespans
				require.NotNil(t, config.EmbeddedAuthServerConfig.TokenLifespans)
				assert.Equal(t, "30m", config.EmbeddedAuthServerConfig.TokenLifespans.AccessTokenLifespan)

				// Verify upstream provider
				require.Len(t, config.EmbeddedAuthServerConfig.Upstreams, 1)
				assert.Equal(t, "okta", config.EmbeddedAuthServerConfig.Upstreams[0].Name)

				// Verify AllowedAudiences and ScopesSupported from OIDC config
				assert.Equal(t, []string{"http://embedded-auth-server.default.svc.cluster.local:8080"},
					config.EmbeddedAuthServerConfig.AllowedAudiences)
				assert.Equal(t, []string{"openid", "offline_access", "mcp:tools"},
					config.EmbeddedAuthServerConfig.ScopesSupported)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := createRunConfigTestScheme()
			objects := []runtime.Object{tt.mcpServer}
			if tt.externalAuth != nil {
				objects = append(objects, tt.externalAuth)
			}
			if tt.clientSecret != nil {
				objects = append(objects, tt.clientSecret)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

			runConfig, err := reconciler.createRunConfigFromMCPServer(tt.mcpServer)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, runConfig)
				if tt.validate != nil {
					tt.validate(t, runConfig)
				}
			}
		})
	}
}

// TestGenerateTokenExchangeEnvVars tests the generateTokenExchangeEnvVars function
func TestGenerateTokenExchangeEnvVars(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		mcpServer    *mcpv1alpha1.MCPServer
		externalAuth *mcpv1alpha1.MCPExternalAuthConfig
		expectError  bool
		errContains  string
		validate     func(*testing.T, []corev1.EnvVar)
	}{
		{
			name: "no external auth config reference",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
				},
			},
			expectError: false,
			validate: func(t *testing.T, envVars []corev1.EnvVar) {
				t.Helper()
				assert.Len(t, envVars, 0)
			},
		},
		{
			name: "valid token exchange config generates env var",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "oauth-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "oauth-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "client-id",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "oauth-secret",
							Key:  "client-secret",
						},
						Audience: "api",
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, envVars []corev1.EnvVar) {
				t.Helper()
				require.Len(t, envVars, 1)
				assert.Equal(t, "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET", envVars[0].Name)
				require.NotNil(t, envVars[0].ValueFrom)
				require.NotNil(t, envVars[0].ValueFrom.SecretKeyRef)
				assert.Equal(t, "oauth-secret", envVars[0].ValueFrom.SecretKeyRef.Name)
				assert.Equal(t, "client-secret", envVars[0].ValueFrom.SecretKeyRef.Key)
			},
		},
		{
			name: "unsupported auth type returns empty env vars",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "unsupported-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unsupported-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: "unsupported",
				},
			},
			expectError: false,
			validate: func(t *testing.T, envVars []corev1.EnvVar) {
				t.Helper()
				assert.Len(t, envVars, 0)
			},
		},
		{
			name: "nil token exchange spec returns empty env vars",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "nil-spec-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nil-spec-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type:          mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: nil,
				},
			},
			expectError: false,
			validate: func(t *testing.T, envVars []corev1.EnvVar) {
				t.Helper()
				assert.Len(t, envVars, 0)
			},
		},
		{
			name: "external auth config not found returns error",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "non-existent",
					},
				},
			},
			expectError: true,
			errContains: "failed to get MCPExternalAuthConfig",
		},
		{
			name: "token exchange without client secret ref (GCP Workforce Identity)",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "gcp-workforce-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "gcp-workforce-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://sts.googleapis.com/v1/token",
						Audience: "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/pool/providers/provider",
						Scopes:   []string{"https://www.googleapis.com/auth/cloud-platform"},
						// No ClientID or ClientSecretRef
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, envVars []corev1.EnvVar) {
				t.Helper()
				// Should not generate any env vars since ClientSecretRef is nil
				assert.Len(t, envVars, 0)
			},
		},
		{
			name: "token exchange with nil client secret ref returns no env vars",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "nil-secret-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nil-secret-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL:        "https://oauth.example.com/token",
						ClientID:        "client-id",
						ClientSecretRef: nil, // Explicitly nil
						Audience:        "api",
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, envVars []corev1.EnvVar) {
				t.Helper()
				assert.Len(t, envVars, 0)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := createRunConfigTestScheme()
			objects := []runtime.Object{tt.mcpServer}
			if tt.externalAuth != nil {
				objects = append(objects, tt.externalAuth)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

			ctx := t.Context()
			envVars, err := ctrlutil.GenerateTokenExchangeEnvVars(ctx, reconciler.Client, tt.mcpServer.Namespace, tt.mcpServer.Spec.ExternalAuthConfigRef, ctrlutil.GetExternalAuthConfigByName)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
				if tt.validate != nil {
					tt.validate(t, envVars)
				}
			}
		})
	}
}
