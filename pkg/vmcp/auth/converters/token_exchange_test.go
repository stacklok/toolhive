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
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

func TestTokenExchangeConverter_StrategyType(t *testing.T) {
	t.Parallel()

	converter := &TokenExchangeConverter{}
	assert.Equal(t, authtypes.StrategyTypeTokenExchange, converter.StrategyType())
}

func TestTokenExchangeConverter_ConvertToStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		externalAuth *mcpv1alpha1.MCPExternalAuthConfig
		wantStrategy *authtypes.BackendAuthStrategy
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
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL:         "https://auth.example.com/token",
					ClientID:         "test-client",
					ClientSecretEnv:  "", // Set by controller, not converter
					Audience:         "https://api.example.com",
					Scopes:           []string{"read", "write"},
					SubjectTokenType: "urn:ietf:params:oauth:token-type:access_token",
				},
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
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
				},
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
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
					ClientID: "test-client",
					Audience: "https://api.example.com",
				},
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
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL:         "https://auth.example.com/token",
					SubjectTokenType: "urn:ietf:params:oauth:token-type:id_token",
				},
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
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL:         "https://auth.example.com/token",
					SubjectTokenType: "urn:ietf:params:oauth:token-type:jwt",
				},
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
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL:         "https://auth.example.com/token",
					SubjectTokenType: "urn:ietf:params:oauth:token-type:access_token",
				},
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
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
					Scopes:   []string{"openid", "profile", "email"},
				},
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
			strategy, err := converter.ConvertToStrategy(tt.externalAuth)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantStrategy, strategy)
		})
	}
}

func TestTokenExchangeConverter_ResolveSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		externalAuth  *mcpv1alpha1.MCPExternalAuthConfig
		setupSecrets  func(client.Client) error
		inputStrategy *authtypes.BackendAuthStrategy
		wantStrategy  *authtypes.BackendAuthStrategy
		wantErr       bool
		errContains   string
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
			inputStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL:        "https://auth.example.com/token",
					ClientID:        "test-client",
					ClientSecretEnv: "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET",
				},
			},
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL:        "https://auth.example.com/token",
					ClientID:        "test-client",
					ClientSecret:    "my-secret-value",
					ClientSecretEnv: "",
				},
			},
			wantErr: false,
		},
		{
			name: "no-op when client_secret_ref not present",
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
						ClientSecretRef: nil, // No secret ref, so no-op
					},
				},
			},
			inputStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
					ClientID: "test-client",
				},
			},
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
					ClientID: "test-client",
				},
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
			inputStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
				},
			},
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
				},
			},
			wantErr: false,
		},
		{
			name: "nil strategy",
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
			inputStrategy: nil,
			wantErr:       true,
			errContains:   "token exchange strategy is nil",
		},
		{
			name: "nil token exchange config in external auth",
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
			inputStrategy: &authtypes.BackendAuthStrategy{
				Type:          authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{},
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
			inputStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					ClientSecretEnv: "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET",
				},
			},
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					ClientSecretEnv: "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET",
				},
			},
			wantErr: false, // No-op when ClientSecretRef is nil
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
			inputStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					ClientSecretEnv: "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET",
				},
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
			inputStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					ClientSecretEnv: "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET",
				},
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
			strategy, err := converter.ResolveSecrets(
				context.Background(),
				tt.externalAuth,
				fakeClient,
				tt.externalAuth.Namespace,
				tt.inputStrategy,
			)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantStrategy, strategy)
		})
	}
}
