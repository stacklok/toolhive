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
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	"github.com/stacklok/toolhive/pkg/runner"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
)

// TestCreateRunConfigFromMCPRemoteProxy tests the conversion from MCPRemoteProxy to RunConfig
func TestCreateRunConfigFromMCPRemoteProxy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		proxy       *mcpv1alpha1.MCPRemoteProxy
		toolConfig  *mcpv1alpha1.MCPToolConfig
		expectError bool
		validate    func(*testing.T, *runner.RunConfig)
	}{
		{
			name: "basic remote proxy",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "salesforce-proxy",
					Namespace: "mcp-proxies",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.salesforce.com",
					Port:      8080,
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://login.salesforce.com",
							Audience: "mcp.salesforce.com",
						},
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, config *runner.RunConfig) {
				t.Helper()
				assert.Equal(t, "salesforce-proxy", config.Name)
				assert.Equal(t, "https://mcp.salesforce.com", config.RemoteURL)
				assert.Empty(t, config.Image, "Image should be empty for remote proxy")
				assert.Equal(t, transporttypes.TransportTypeStreamableHTTP, config.Transport, "Should default to streamable-http")
				assert.Equal(t, 8080, config.Port)
				assert.NotNil(t, config.OIDCConfig)
				assert.Equal(t, "https://login.salesforce.com", config.OIDCConfig.Issuer)
				assert.Equal(t, "mcp.salesforce.com", config.OIDCConfig.Audience)
			},
		},
		{
			name: "with tool filtering",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "filtered-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Port:      8080,
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://auth.example.com",
							Audience: "mcp-proxy",
						},
					},
					ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
						Name: "filter-config",
					},
				},
			},
			toolConfig: &mcpv1alpha1.MCPToolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "filter-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPToolConfigSpec{
					ToolsFilter: []string{"read_data", "list_resources"},
					ToolsOverride: map[string]mcpv1alpha1.ToolOverride{
						"read_data": {
							Name:        "read-customer-data",
							Description: "Read customer data from Salesforce",
						},
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, config *runner.RunConfig) {
				t.Helper()
				assert.Equal(t, "filtered-proxy", config.Name)
				assert.Equal(t, "https://mcp.example.com", config.RemoteURL)
				assert.Equal(t, []string{"read_data", "list_resources"}, config.ToolsFilter)
				assert.NotNil(t, config.ToolsOverride)
				assert.Contains(t, config.ToolsOverride, "read_data")
				assert.Equal(t, "read-customer-data", config.ToolsOverride["read_data"].Name)
			},
		},
		{
			name: "with inline authorization",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "authz-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Port:      8080,
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://auth.example.com",
							Audience: "mcp-proxy",
						},
					},
					AuthzConfig: &mcpv1alpha1.AuthzConfigRef{
						Type: mcpv1alpha1.AuthzConfigTypeInline,
						Inline: &mcpv1alpha1.InlineAuthzConfig{
							Policies: []string{
								`permit(principal, action == Action::"tools/list", resource);`,
								`forbid(principal, action == Action::"tools/call", resource) when { resource.tool == "delete_resource" };`,
							},
							EntitiesJSON: `[]`,
						},
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, config *runner.RunConfig) {
				t.Helper()
				assert.Equal(t, "authz-proxy", config.Name)
				assert.NotNil(t, config.AuthzConfig)
				assert.Equal(t, authz.ConfigType(cedar.ConfigType), config.AuthzConfig.Type)

				cedarCfg, err := cedar.ExtractConfig(config.AuthzConfig)
				require.NoError(t, err)
				assert.Len(t, cedarCfg.Options.Policies, 2)
				assert.Contains(t, cedarCfg.Options.Policies[0], "tools/list")
			},
		},
		{
			name: "with trust proxy headers",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "trust-headers-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Port:      8080,
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://auth.example.com",
							Audience: "mcp-proxy",
						},
					},
					TrustProxyHeaders: true,
				},
			},
			expectError: false,
			validate: func(t *testing.T, config *runner.RunConfig) {
				t.Helper()
				assert.Equal(t, "trust-headers-proxy", config.Name)
				assert.True(t, config.TrustProxyHeaders)
			},
		},
		{
			name: "with header forward plaintext only",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "plaintext-headers-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Port:      8080,
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://auth.example.com",
							Audience: "mcp-proxy",
						},
					},
					HeaderForward: &mcpv1alpha1.HeaderForwardConfig{
						AddPlaintextHeaders: map[string]string{
							"X-Tenant-ID":   "tenant-123",
							"X-Correlation": "corr-abc",
						},
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, config *runner.RunConfig) {
				t.Helper()
				assert.Equal(t, "plaintext-headers-proxy", config.Name)
				require.NotNil(t, config.HeaderForward)
				assert.Equal(t, "tenant-123", config.HeaderForward.AddPlaintextHeaders["X-Tenant-ID"])
				assert.Equal(t, "corr-abc", config.HeaderForward.AddPlaintextHeaders["X-Correlation"])
				assert.Empty(t, config.HeaderForward.AddHeadersFromSecret)
			},
		},
		{
			name: "with header forward secrets",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "secret-headers-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Port:      8080,
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://auth.example.com",
							Audience: "mcp-proxy",
						},
					},
					HeaderForward: &mcpv1alpha1.HeaderForwardConfig{
						AddHeadersFromSecret: []mcpv1alpha1.HeaderFromSecret{
							{
								HeaderName: "X-API-Key",
								ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
									Name: "api-secret",
									Key:  "key",
								},
							},
							{
								HeaderName: "Authorization",
								ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
									Name: "auth-secret",
									Key:  "token",
								},
							},
						},
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, config *runner.RunConfig) {
				t.Helper()
				assert.Equal(t, "secret-headers-proxy", config.Name)
				require.NotNil(t, config.HeaderForward)
				assert.Empty(t, config.HeaderForward.AddPlaintextHeaders)
				// Verify secret identifiers (not actual secrets)
				require.Len(t, config.HeaderForward.AddHeadersFromSecret, 2)
				assert.Equal(t, "HEADER_FORWARD_X_API_KEY_SECRET_HEADERS_PROXY", config.HeaderForward.AddHeadersFromSecret["X-API-Key"])
				assert.Equal(t, "HEADER_FORWARD_AUTHORIZATION_SECRET_HEADERS_PROXY", config.HeaderForward.AddHeadersFromSecret["Authorization"])
			},
		},
		{
			name: "with header forward mixed plaintext and secrets",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mixed-headers-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Port:      8080,
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://auth.example.com",
							Audience: "mcp-proxy",
						},
					},
					HeaderForward: &mcpv1alpha1.HeaderForwardConfig{
						AddPlaintextHeaders: map[string]string{
							"X-Tenant-ID": "tenant-456",
						},
						AddHeadersFromSecret: []mcpv1alpha1.HeaderFromSecret{
							{
								HeaderName: "X-API-Key",
								ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
									Name: "api-secret",
									Key:  "key",
								},
							},
						},
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, config *runner.RunConfig) {
				t.Helper()
				assert.Equal(t, "mixed-headers-proxy", config.Name)
				require.NotNil(t, config.HeaderForward)
				// Verify plaintext header
				assert.Equal(t, "tenant-456", config.HeaderForward.AddPlaintextHeaders["X-Tenant-ID"])
				// Verify secret identifier (not actual secret)
				assert.Equal(t, "HEADER_FORWARD_X_API_KEY_MIXED_HEADERS_PROXY", config.HeaderForward.AddHeadersFromSecret["X-API-Key"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := createRunConfigTestScheme()
			objects := []runtime.Object{tt.proxy}
			if tt.toolConfig != nil {
				objects = append(objects, tt.toolConfig)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			reconciler := &MCPRemoteProxyReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			config, err := reconciler.createRunConfigFromMCPRemoteProxy(tt.proxy)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, config)
				assert.Equal(t, runner.CurrentSchemaVersion, config.SchemaVersion)
				if tt.validate != nil {
					tt.validate(t, config)
				}
			}
		})
	}
}

// TestCreateRunConfigFromMCPRemoteProxy_WithTokenExchange tests RunConfig generation with token exchange
func TestCreateRunConfigFromMCPRemoteProxy_WithTokenExchange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		proxy        *mcpv1alpha1.MCPRemoteProxy
		externalAuth *mcpv1alpha1.MCPExternalAuthConfig
		clientSecret *corev1.Secret
		expectError  bool
		validate     func(*testing.T, *runner.RunConfig)
	}{
		{
			name: "with token exchange",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "exchange-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.salesforce.com",
					Port:      8080,
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://auth.company.com",
							Audience: "mcp-proxy",
						},
					},
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "salesforce-exchange",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "salesforce-exchange",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://keycloak.company.com/token",
						ClientID: "exchange-client",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "exchange-creds",
							Key:  "client-secret",
						},
						Audience: "mcp.salesforce.com",
						Scopes:   []string{"mcp:read", "mcp:write"},
					},
				},
			},
			clientSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "exchange-creds",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"client-secret": []byte("super-secret"),
				},
			},
			expectError: false,
			validate: func(t *testing.T, config *runner.RunConfig) {
				t.Helper()
				assert.Equal(t, "exchange-proxy", config.Name)
				assert.Equal(t, "https://mcp.salesforce.com", config.RemoteURL)

				// Verify middleware config includes token exchange
				assert.NotNil(t, config.MiddlewareConfigs)
				found := false
				for _, mw := range config.MiddlewareConfigs {
					if mw.Type == "tokenexchange" {
						found = true
						var params map[string]interface{}
						err := json.Unmarshal(mw.Parameters, &params)
						require.NoError(t, err)

						tokenExchangeConfig, ok := params["token_exchange_config"].(map[string]interface{})
						require.True(t, ok)
						assert.Equal(t, "https://keycloak.company.com/token", tokenExchangeConfig["token_url"])
						assert.Equal(t, "exchange-client", tokenExchangeConfig["client_id"])
						assert.Equal(t, "mcp.salesforce.com", tokenExchangeConfig["audience"])
					}
				}
				assert.True(t, found, "Token exchange middleware should be present")
			},
		},
		{
			name: "external auth config not found",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "broken-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Port:      8080,
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://auth.example.com",
							Audience: "mcp-proxy",
						},
					},
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
			objects := []runtime.Object{tt.proxy}
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

			reconciler := &MCPRemoteProxyReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			runConfig, err := reconciler.createRunConfigFromMCPRemoteProxy(tt.proxy)

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

// TestValidateRunConfigForRemoteProxy tests the validation logic for remote proxy RunConfigs
func TestValidateRunConfigForRemoteProxy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		config    *runner.RunConfig
		expectErr bool
		errMsg    string
	}{
		{
			name: "valid remote proxy config with streamable-http",
			config: &runner.RunConfig{
				Name:      "valid-proxy",
				RemoteURL: "https://mcp.salesforce.com",
				Transport: transporttypes.TransportTypeStreamableHTTP,
				Port:      8080,
				Host:      "0.0.0.0",
			},
			expectErr: false,
		},
		{
			name: "valid remote proxy config with sse",
			config: &runner.RunConfig{
				Name:      "sse-proxy",
				RemoteURL: "https://mcp.salesforce.com",
				Transport: transporttypes.TransportTypeSSE,
				Port:      8080,
				Host:      "0.0.0.0",
			},
			expectErr: false,
		},
		{
			name:      "nil config",
			config:    nil,
			expectErr: true,
			errMsg:    "RunConfig cannot be nil",
		},
		{
			name: "missing remote URL",
			config: &runner.RunConfig{
				Name:      "no-url-proxy",
				Transport: transporttypes.TransportTypeStreamableHTTP,
				Port:      8080,
				Host:      "0.0.0.0",
			},
			expectErr: true,
			errMsg:    "remoteURL is required",
		},
		{
			name: "missing name",
			config: &runner.RunConfig{
				RemoteURL: "https://mcp.example.com",
				Transport: transporttypes.TransportTypeStreamableHTTP,
				Port:      8080,
				Host:      "0.0.0.0",
			},
			expectErr: true,
			errMsg:    "name is required",
		},
		{
			name: "wrong transport type - stdio not allowed",
			config: &runner.RunConfig{
				Name:      "wrong-transport",
				RemoteURL: "https://mcp.example.com",
				Transport: transporttypes.TransportTypeStdio,
				Port:      8080,
				Host:      "0.0.0.0",
			},
			expectErr: true,
			errMsg:    "transport must be SSE or StreamableHTTP",
		},
		{
			name: "missing port",
			config: &runner.RunConfig{
				Name:      "no-port",
				RemoteURL: "https://mcp.example.com",
				Transport: transporttypes.TransportTypeStreamableHTTP,
				Host:      "0.0.0.0",
			},
			expectErr: true,
			errMsg:    "port is required",
		},
		{
			name: "missing host",
			config: &runner.RunConfig{
				Name:      "no-host",
				RemoteURL: "https://mcp.example.com",
				Transport: transporttypes.TransportTypeStreamableHTTP,
				Port:      8080,
			},
			expectErr: true,
			errMsg:    "host is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &MCPRemoteProxyReconciler{}
			err := r.validateRunConfigForRemoteProxy(context.TODO(), tt.config)

			if tt.expectErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestEnsureRunConfigConfigMapForRemoteProxy tests the ConfigMap creation and update logic
func TestEnsureRunConfigConfigMapForRemoteProxy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		proxy           *mcpv1alpha1.MCPRemoteProxy
		existingCM      *corev1.ConfigMap
		expectError     bool
		validateContent func(*testing.T, *corev1.ConfigMap)
	}{
		{
			name: "create new configmap for remote proxy",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "new-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Port:      8080,
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://auth.example.com",
							Audience: "mcp-proxy",
						},
					},
				},
			},
			existingCM:  nil,
			expectError: false,
			validateContent: func(t *testing.T, cm *corev1.ConfigMap) {
				t.Helper()
				assert.Equal(t, "new-proxy-runconfig", cm.Name)
				assert.Equal(t, "default", cm.Namespace)
				assert.Contains(t, cm.Data, "runconfig.json")
				assert.Contains(t, cm.Annotations, "toolhive.stacklok.dev/content-checksum")

				var runConfig runner.RunConfig
				err := json.Unmarshal([]byte(cm.Data["runconfig.json"]), &runConfig)
				require.NoError(t, err)
				assert.Equal(t, "new-proxy", runConfig.Name)
				assert.Equal(t, "https://mcp.example.com", runConfig.RemoteURL)
				assert.Empty(t, runConfig.Image, "Image should be empty for remote proxy")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			testScheme := createRunConfigTestScheme()
			objects := []runtime.Object{tt.proxy}
			if tt.existingCM != nil {
				objects = append(objects, tt.existingCM)
			}
			fakeClient := fake.NewClientBuilder().WithScheme(testScheme).WithRuntimeObjects(objects...).Build()

			reconciler := &MCPRemoteProxyReconciler{
				Client: fakeClient,
				Scheme: testScheme,
			}

			err := reconciler.ensureRunConfigConfigMap(context.TODO(), tt.proxy)
			if tt.expectError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)

			// Verify the ConfigMap exists
			configMapName := fmt.Sprintf("%s-runconfig", tt.proxy.Name)
			configMap := &corev1.ConfigMap{}
			err = fakeClient.Get(context.TODO(), types.NamespacedName{
				Name:      configMapName,
				Namespace: tt.proxy.Namespace,
			}, configMap)
			require.NoError(t, err)

			if tt.validateContent != nil {
				tt.validateContent(t, configMap)
			}
		})
	}
}

// TestLabelsForRunConfigRemoteProxy tests the label generation for remote proxy
func TestLabelsForRunConfigRemoteProxy(t *testing.T) {
	t.Parallel()
	expected := map[string]string{
		"toolhive.stacklok.io/component":        "run-config",
		"toolhive.stacklok.io/mcp-remote-proxy": "test-proxy",
		"toolhive.stacklok.io/managed-by":       "toolhive-operator",
	}

	result := labelsForRunConfigRemoteProxy("test-proxy")
	assert.Equal(t, expected, result)
}
