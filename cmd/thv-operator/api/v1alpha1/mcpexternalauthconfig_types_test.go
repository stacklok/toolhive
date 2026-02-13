// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMCPExternalAuthConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		config    *MCPExternalAuthConfig
		expectErr bool
		errMsg    string
	}{
		{
			name: "valid unauthenticated type",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-unauth",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeUnauthenticated,
				},
			},
			expectErr: false,
		},
		{
			name: "valid tokenExchange type",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-token",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type:          ExternalAuthTypeTokenExchange,
					TokenExchange: &TokenExchangeConfig{TokenURL: "https://example.com/token"},
				},
			},
			expectErr: false,
		},
		{
			name: "valid headerInjection type",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-header",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type:            ExternalAuthTypeHeaderInjection,
					HeaderInjection: &HeaderInjectionConfig{HeaderName: "Authorization"},
				},
			},
			expectErr: false,
		},
		{
			name: "valid bearerToken type",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-bearer",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type:        ExternalAuthTypeBearerToken,
					BearerToken: &BearerTokenConfig{},
				},
			},
			expectErr: false,
		},
		{
			name: "valid embeddedAuthServer with single OIDC provider",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-embedded-oidc",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						UpstreamProviders: []UpstreamProviderConfig{
							{
								Name:       "github",
								Type:       UpstreamProviderTypeOIDC,
								OIDCConfig: &OIDCUpstreamConfig{IssuerURL: "https://github.com", ClientID: "client-id"},
							},
						},
					},
				},
			},
			expectErr: false,
		},
		{
			name: "valid embeddedAuthServer with single OAuth2 provider",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-embedded-oauth2",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						UpstreamProviders: []UpstreamProviderConfig{
							{
								Name: "custom-oauth",
								Type: UpstreamProviderTypeOAuth2,
								OAuth2Config: &OAuth2UpstreamConfig{
									AuthorizationEndpoint: "https://oauth.example.com/authorize",
									TokenEndpoint:         "https://oauth.example.com/token",
									ClientID:              "client-id",
									UserInfo:              &UserInfoConfig{EndpointURL: "https://oauth.example.com/userinfo"},
								},
							},
						},
					},
				},
			},
			expectErr: false,
		},
		{
			name: "invalid embeddedAuthServer with multiple providers",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-embedded-multi",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						UpstreamProviders: []UpstreamProviderConfig{
							{
								Name:       "github",
								Type:       UpstreamProviderTypeOIDC,
								OIDCConfig: &OIDCUpstreamConfig{IssuerURL: "https://github.com", ClientID: "id1"},
							},
							{
								Name:       "google",
								Type:       UpstreamProviderTypeOIDC,
								OIDCConfig: &OIDCUpstreamConfig{IssuerURL: "https://accounts.google.com", ClientID: "id2"},
							},
						},
					},
				},
			},
			expectErr: true,
			errMsg:    "currently only one upstream provider is supported (found 2)",
		},
		{
			name: "invalid embeddedAuthServer with no providers",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-embedded-empty",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer:            "https://auth.example.com",
						UpstreamProviders: []UpstreamProviderConfig{},
					},
				},
			},
			expectErr: true,
			errMsg:    "at least one upstream provider is required",
		},
		{
			name: "invalid OIDC provider without oidcConfig",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-oidc-missing-config",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						UpstreamProviders: []UpstreamProviderConfig{
							{Name: "github", Type: UpstreamProviderTypeOIDC},
						},
					},
				},
			},
			expectErr: true,
			errMsg:    "oidcConfig must be set when type is 'oidc'",
		},
		{
			name: "invalid OAuth2 provider without oauth2Config",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-oauth2-missing-config",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						UpstreamProviders: []UpstreamProviderConfig{
							{Name: "custom", Type: UpstreamProviderTypeOAuth2},
						},
					},
				},
			},
			expectErr: true,
			errMsg:    "oauth2Config must be set when type is 'oauth2'",
		},
		{
			name: "invalid OIDC provider with oauth2Config instead",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-oidc-wrong-config",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						UpstreamProviders: []UpstreamProviderConfig{
							{
								Name: "github",
								Type: UpstreamProviderTypeOIDC,
								OAuth2Config: &OAuth2UpstreamConfig{
									AuthorizationEndpoint: "https://github.com/authorize",
									TokenEndpoint:         "https://github.com/token",
									ClientID:              "client-id",
									UserInfo:              &UserInfoConfig{EndpointURL: "https://github.com/userinfo"},
								},
							},
						},
					},
				},
			},
			expectErr: true,
			errMsg:    "oidcConfig must be set when type is 'oidc'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.config.Validate()
			if tt.expectErr {
				require.Error(t, err, "expected validation to fail")
				assert.Contains(t, err.Error(), tt.errMsg, "error message should match")
			} else {
				assert.NoError(t, err, "expected validation to pass")
			}
		})
	}
}

func TestMCPExternalAuthConfig_validateEmbeddedAuthServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		config    *MCPExternalAuthConfig
		expectErr bool
		errMsg    string
	}{
		{
			name: "single OIDC provider - valid",
			config: &MCPExternalAuthConfig{
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						UpstreamProviders: []UpstreamProviderConfig{
							{
								Name:       "github",
								Type:       UpstreamProviderTypeOIDC,
								OIDCConfig: &OIDCUpstreamConfig{IssuerURL: "https://github.com", ClientID: "client-id"},
							},
						},
					},
				},
			},
			expectErr: false,
		},
		{
			name: "single OAuth2 provider - valid",
			config: &MCPExternalAuthConfig{
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						UpstreamProviders: []UpstreamProviderConfig{
							{
								Name: "custom",
								Type: UpstreamProviderTypeOAuth2,
								OAuth2Config: &OAuth2UpstreamConfig{
									AuthorizationEndpoint: "https://oauth.example.com/authorize",
									TokenEndpoint:         "https://oauth.example.com/token",
									ClientID:              "client-id",
									UserInfo:              &UserInfoConfig{EndpointURL: "https://oauth.example.com/userinfo"},
								},
							},
						},
					},
				},
			},
			expectErr: false,
		},
		{
			name: "multiple providers - invalid",
			config: &MCPExternalAuthConfig{
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						UpstreamProviders: []UpstreamProviderConfig{
							{
								Name:       "github",
								Type:       UpstreamProviderTypeOIDC,
								OIDCConfig: &OIDCUpstreamConfig{IssuerURL: "https://github.com", ClientID: "id1"},
							},
							{
								Name:       "google",
								Type:       UpstreamProviderTypeOIDC,
								OIDCConfig: &OIDCUpstreamConfig{IssuerURL: "https://accounts.google.com", ClientID: "id2"},
							},
							{
								Name: "custom",
								Type: UpstreamProviderTypeOAuth2,
								OAuth2Config: &OAuth2UpstreamConfig{
									AuthorizationEndpoint: "https://oauth.example.com/authorize",
									TokenEndpoint:         "https://oauth.example.com/token",
									ClientID:              "id3",
									UserInfo:              &UserInfoConfig{EndpointURL: "https://oauth.example.com/userinfo"},
								},
							},
						},
					},
				},
			},
			expectErr: true,
			errMsg:    "currently only one upstream provider is supported (found 3)",
		},
		{
			name: "empty providers array - invalid",
			config: &MCPExternalAuthConfig{
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer:            "https://auth.example.com",
						UpstreamProviders: []UpstreamProviderConfig{},
					},
				},
			},
			expectErr: true,
			errMsg:    "at least one upstream provider is required",
		},
		{
			name: "nil embedded auth server config",
			config: &MCPExternalAuthConfig{
				Spec: MCPExternalAuthConfigSpec{
					Type:               ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: nil,
				},
			},
			expectErr: false, // validateEmbeddedAuthServer returns nil if config is nil
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.config.validateEmbeddedAuthServer()
			if tt.expectErr {
				require.Error(t, err, "expected validation to fail")
				assert.Contains(t, err.Error(), tt.errMsg, "error message should match")
			} else {
				assert.NoError(t, err, "expected validation to pass")
			}
		})
	}
}

func TestMCPExternalAuthConfig_validateUpstreamProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		provider  UpstreamProviderConfig
		expectErr bool
		errMsg    string
	}{
		{
			name: "valid OIDC provider",
			provider: UpstreamProviderConfig{
				Name:       "github",
				Type:       UpstreamProviderTypeOIDC,
				OIDCConfig: &OIDCUpstreamConfig{IssuerURL: "https://github.com", ClientID: "client-id"},
			},
			expectErr: false,
		},
		{
			name: "valid OAuth2 provider",
			provider: UpstreamProviderConfig{
				Name: "custom",
				Type: UpstreamProviderTypeOAuth2,
				OAuth2Config: &OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://oauth.example.com/authorize",
					TokenEndpoint:         "https://oauth.example.com/token",
					ClientID:              "client-id",
					UserInfo:              &UserInfoConfig{EndpointURL: "https://oauth.example.com/userinfo"},
				},
			},
			expectErr: false,
		},
		{
			name: "OIDC provider missing oidcConfig",
			provider: UpstreamProviderConfig{
				Name: "github",
				Type: UpstreamProviderTypeOIDC,
			},
			expectErr: true,
			errMsg:    "oidcConfig must be set when type is 'oidc'",
		},
		{
			name: "OAuth2 provider missing oauth2Config",
			provider: UpstreamProviderConfig{
				Name: "custom",
				Type: UpstreamProviderTypeOAuth2,
			},
			expectErr: true,
			errMsg:    "oauth2Config must be set when type is 'oauth2'",
		},
		{
			name: "OIDC provider with oauth2Config instead",
			provider: UpstreamProviderConfig{
				Name: "github",
				Type: UpstreamProviderTypeOIDC,
				OAuth2Config: &OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://github.com/authorize",
					TokenEndpoint:         "https://github.com/token",
					ClientID:              "client-id",
					UserInfo:              &UserInfoConfig{EndpointURL: "https://github.com/userinfo"},
				},
			},
			expectErr: true,
			errMsg:    "oidcConfig must be set when type is 'oidc'",
		},
		{
			name: "OAuth2 provider with oidcConfig instead",
			provider: UpstreamProviderConfig{
				Name:       "custom",
				Type:       UpstreamProviderTypeOAuth2,
				OIDCConfig: &OIDCUpstreamConfig{IssuerURL: "https://oauth.example.com", ClientID: "client-id"},
			},
			expectErr: true,
			errMsg:    "oidcConfig must be set when type is 'oidc' and must not be set otherwise",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := &MCPExternalAuthConfig{
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer:            "https://auth.example.com",
						UpstreamProviders: []UpstreamProviderConfig{tt.provider},
					},
				},
			}

			err := config.validateUpstreamProvider(0, &tt.provider)
			if tt.expectErr {
				require.Error(t, err, "expected validation to fail")
				assert.Contains(t, err.Error(), tt.errMsg, "error message should match")
			} else {
				assert.NoError(t, err, "expected validation to pass")
			}
		})
	}
}
