package controllerutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
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

			ctx := t.Context()
			var options []runner.RunConfigBuilderOption

			err := AddExternalAuthConfigOptions(ctx, fakeClient, tt.mcpServer.Namespace, tt.mcpServer.Spec.ExternalAuthConfigRef, &options)

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
