package converters

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestExternalAuthConfigToStrategyMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		externalAuth     *mcpv1alpha1.MCPExternalAuthConfig
		wantStrategyType string
		wantMetadata     map[string]any
		wantErr          bool
		errContains      string
	}{
		{
			name:         "nil config returns error",
			externalAuth: nil,
			wantErr:      true,
			errContains:  "external auth config is nil",
		},
		{
			name: "token exchange with all fields",
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
							Name: "test-secret",
							Key:  "client-secret",
						},
						Audience:         "https://api.example.com",
						Scopes:           []string{"read", "write"},
						SubjectTokenType: "access_token",
					},
				},
			},
			wantStrategyType: "token_exchange",
			wantMetadata: map[string]any{
				"token_url":          "https://auth.example.com/token",
				"client_id":          "test-client",
				"client_secret_env":  "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET",
				"audience":           "https://api.example.com",
				"scopes":             []string{"read", "write"},
				"subject_token_type": "urn:ietf:params:oauth:token-type:access_token",
			},
			wantErr: false,
		},
		{
			name: "token exchange with minimal fields",
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
			wantStrategyType: "token_exchange",
			wantMetadata: map[string]any{
				"token_url": "https://auth.example.com/token",
			},
			wantErr: false,
		},
		{
			name: "token exchange with no client secret",
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
					},
				},
			},
			wantStrategyType: "token_exchange",
			wantMetadata: map[string]any{
				"token_url": "https://auth.example.com/token",
				"client_id": "test-client",
			},
			wantErr: false,
		},
		{
			name: "unsupported auth type",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unsupported-auth",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: "unsupported-type",
				},
			},
			wantErr:     true,
			errContains: "unsupported auth type",
		},
		{
			name: "token exchange type with nil config",
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nil-token-exchange",
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

			strategyType, metadata, err := ExternalAuthConfigToStrategyMetadata(tt.externalAuth)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantStrategyType, strategyType)
			assert.Equal(t, tt.wantMetadata, metadata)
		})
	}
}
