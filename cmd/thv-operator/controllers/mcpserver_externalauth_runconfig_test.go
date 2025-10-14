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
	"github.com/stacklok/toolhive/pkg/runner"
)

// TestAddExternalAuthConfigOptions tests the addExternalAuthConfigOptions function
func TestAddExternalAuthConfigOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		mcpServer      *mcpv1alpha1.MCPServer
		externalAuth   *mcpv1alpha1.MCPExternalAuthConfig
		clientSecret   *corev1.Secret
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
						ClientSecretRef: mcpv1alpha1.SecretKeyRef{
							Name: "oauth-secret",
							Key:  "client-secret",
						},
						Audience:                "backend-service",
						Scope:                   "read write admin",
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
						ClientSecretRef: mcpv1alpha1.SecretKeyRef{
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
						ClientSecretRef: mcpv1alpha1.SecretKeyRef{
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
						ClientSecretRef: mcpv1alpha1.SecretKeyRef{
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
						ClientSecretRef: mcpv1alpha1.SecretKeyRef{
							Name: "secret",
							Key:  "key",
						},
						Audience: "api",
						Scope:    "", // Empty scope
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

			reconciler := &MCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			ctx := t.Context()
			var options []runner.RunConfigBuilderOption

			err := reconciler.addExternalAuthConfigOptions(ctx, tt.mcpServer, &options)

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
						ClientSecretRef: mcpv1alpha1.SecretKeyRef{
							Name: "oauth-creds",
							Key:  "client-secret",
						},
						Audience: "backend-api",
						Scope:    "read write",
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

				// Verify middleware config was added
				assert.NotNil(t, config.MiddlewareConfigs)
				assert.Len(t, config.MiddlewareConfigs, 1)
				assert.Equal(t, "tokenexchange", config.MiddlewareConfigs[0].Type)

				// Verify middleware parameters
				var params map[string]interface{}
				err := json.Unmarshal(config.MiddlewareConfigs[0].Parameters, &params)
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

			reconciler := &MCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

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
						ClientSecretRef: mcpv1alpha1.SecretKeyRef{
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

			reconciler := &MCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			ctx := t.Context()
			envVars, err := reconciler.generateTokenExchangeEnvVars(ctx, tt.mcpServer)

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
