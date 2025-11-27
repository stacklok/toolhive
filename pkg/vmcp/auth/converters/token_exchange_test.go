package converters

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestTokenExchangeConverter_StrategyType(t *testing.T) {
	t.Parallel()

	converter := &TokenExchangeConverter{}
	assert.Equal(t, "token_exchange", converter.StrategyType())
}

func TestTokenExchangeConverter_ConvertToMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		externalAuth *mcpv1alpha1.MCPExternalAuthConfig
		wantMetadata map[string]any
		wantErr      bool
		errContains  string
	}{
		{
			name: "full token exchange config",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://auth.example.com/token",
						ClientID: "test-client",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "client-secret",
							Key:  "secret",
						},
						Audience:                "https://api.example.com",
						Scopes:                  []string{"read", "write"},
						SubjectTokenType:        "access_token",
						ExternalTokenHeaderName: "X-Upstream-Token",
					},
				},
			},
			wantMetadata: map[string]any{
				"token_url":                  "https://auth.example.com/token",
				"client_id":                  "test-client",
				"client_secret_env":          "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET",
				"audience":                   "https://api.example.com",
				"scopes":                     []string{"read", "write"},
				"subject_token_type":         "urn:ietf:params:oauth:token-type:access_token",
				"external_token_header_name": "X-Upstream-Token",
			},
			wantErr: false,
		},
		{
			name: "minimal token exchange config",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "minimal-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://auth.example.com/token",
					},
				},
			},
			wantMetadata: map[string]any{
				"token_url": "https://auth.example.com/token",
			},
			wantErr: false,
		},
		{
			name: "token exchange without client secret",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "no-secret-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://auth.example.com/token",
						ClientID: "test-client",
						Audience: "https://api.example.com",
					},
				},
			},
			wantMetadata: map[string]any{
				"token_url": "https://auth.example.com/token",
				"client_id": "test-client",
				"audience":  "https://api.example.com",
			},
			wantErr: false,
		},
		{
			name: "subject token type id_token short form",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "id-token-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL:         "https://auth.example.com/token",
						SubjectTokenType: "id_token",
					},
				},
			},
			wantMetadata: map[string]any{
				"token_url":          "https://auth.example.com/token",
				"subject_token_type": "urn:ietf:params:oauth:token-type:id_token",
			},
			wantErr: false,
		},
		{
			name: "subject token type jwt short form",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "jwt-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL:         "https://auth.example.com/token",
						SubjectTokenType: "jwt",
					},
				},
			},
			wantMetadata: map[string]any{
				"token_url":          "https://auth.example.com/token",
				"subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
			},
			wantErr: false,
		},
		{
			name: "subject token type already full URN",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "urn-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL:         "https://auth.example.com/token",
						SubjectTokenType: "urn:ietf:params:oauth:token-type:access_token",
					},
				},
			},
			wantMetadata: map[string]any{
				"token_url":          "https://auth.example.com/token",
				"subject_token_type": "urn:ietf:params:oauth:token-type:access_token",
			},
			wantErr: false,
		},
		{
			name: "with scopes array",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "scopes-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://auth.example.com/token",
						Scopes:   []string{"openid", "profile", "email"},
					},
				},
			},
			wantMetadata: map[string]any{
				"token_url": "https://auth.example.com/token",
				"scopes":    []string{"openid", "profile", "email"},
			},
			wantErr: false,
		},
		{
			name: "nil token exchange config",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nil-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type:          mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: nil,
				},
			},
			wantErr:     true,
			errContains: "token exchange config is nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			converter := &TokenExchangeConverter{}
			metadata, err := converter.ConvertToMetadata(tt.externalAuth)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantMetadata, metadata)
		})
	}
}

func TestTokenExchangeConverter_ResolveSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		externalAuth *mcpv1alpha1.MCPExternalAuthConfig
		setupSecrets func(client.Client) error
		inputMeta    map[string]any
		wantMetadata map[string]any
		wantErr      bool
		errContains  string
	}{
		{
			name: "successful secret resolution",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://auth.example.com/token",
						ClientID: "test-client",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "client-secret",
							Key:  "secret",
						},
					},
				},
			},
			setupSecrets: func(k8sClient client.Client) error {
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "client-secret",
						Namespace: "default",
					},
					Data: map[string][]byte{
						"secret": []byte("my-secret-value"),
					},
				}
				return k8sClient.Create(context.Background(), secret)
			},
			inputMeta: map[string]any{
				"token_url":         "https://auth.example.com/token",
				"client_id":         "test-client",
				"client_secret_env": "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET",
			},
			wantMetadata: map[string]any{
				"token_url":     "https://auth.example.com/token",
				"client_id":     "test-client",
				"client_secret": "my-secret-value",
			},
			wantErr: false,
		},
		{
			name: "no-op when client_secret_env not present",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://auth.example.com/token",
						ClientID: "test-client",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "client-secret",
							Key:  "secret",
						},
					},
				},
			},
			inputMeta: map[string]any{
				"token_url": "https://auth.example.com/token",
				"client_id": "test-client",
			},
			wantMetadata: map[string]any{
				"token_url": "https://auth.example.com/token",
				"client_id": "test-client",
			},
			wantErr: false,
		},
		{
			name: "minimal config no-op",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "minimal-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://auth.example.com/token",
					},
				},
			},
			inputMeta: map[string]any{
				"token_url": "https://auth.example.com/token",
			},
			wantMetadata: map[string]any{
				"token_url": "https://auth.example.com/token",
			},
			wantErr: false,
		},
		{
			name: "nil token exchange config",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type:          mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: nil,
				},
			},
			inputMeta: map[string]any{
				"client_secret_env": "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET",
			},
			wantErr:     true,
			errContains: "token exchange config is nil",
		},
		{
			name: "nil clientSecretRef",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL:        "https://auth.example.com/token",
						ClientID:        "test-client",
						ClientSecretRef: nil,
					},
				},
			},
			inputMeta: map[string]any{
				"client_secret_env": "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET",
			},
			wantErr:     true,
			errContains: "clientSecretRef is nil",
		},
		{
			name: "missing secret",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://auth.example.com/token",
						ClientID: "test-client",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "nonexistent-secret",
							Key:  "secret",
						},
					},
				},
			},
			inputMeta: map[string]any{
				"client_secret_env": "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET",
			},
			wantErr:     true,
			errContains: "failed to get secret",
		},
		{
			name: "missing key in secret",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://auth.example.com/token",
						ClientID: "test-client",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "client-secret",
							Key:  "wrong-key",
						},
					},
				},
			},
			setupSecrets: func(k8sClient client.Client) error {
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "client-secret",
						Namespace: "default",
					},
					Data: map[string][]byte{
						"secret": []byte("my-secret-value"),
					},
				}
				return k8sClient.Create(context.Background(), secret)
			},
			inputMeta: map[string]any{
				"client_secret_env": "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET",
			},
			wantErr:     true,
			errContains: "does not contain key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create fake client with schemes
			scheme := runtime.NewScheme()
			_ = mcpv1alpha1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

			// Setup secrets if provided
			if tt.setupSecrets != nil {
				err := tt.setupSecrets(fakeClient)
				require.NoError(t, err, "failed to setup secrets")
			}

			converter := &TokenExchangeConverter{}
			metadata, err := converter.ResolveSecrets(
				context.Background(),
				tt.externalAuth,
				fakeClient,
				tt.externalAuth.Namespace,
				tt.inputMeta,
			)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantMetadata, metadata)
		})
	}
}
