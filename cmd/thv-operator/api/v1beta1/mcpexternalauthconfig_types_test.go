// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

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
			name: "embeddedAuthServer with multiple providers - valid at CRD level",
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
			expectErr: false,
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
			name: "valid upstreamInject type",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-upstream-inject",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type:           ExternalAuthTypeUpstreamInject,
					UpstreamInject: &UpstreamInjectSpec{ProviderName: "github"},
				},
			},
			expectErr: false,
		},
		{
			name: "invalid upstreamInject with nil spec",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-upstream-inject-nil",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type:           ExternalAuthTypeUpstreamInject,
					UpstreamInject: nil,
				},
			},
			expectErr: true,
			errMsg:    "upstreamInject configuration must be set if and only if type is 'upstreamInject'",
		},
		{
			name: "invalid upstreamInject with empty providerName",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-upstream-inject-empty",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type:           ExternalAuthTypeUpstreamInject,
					UpstreamInject: &UpstreamInjectSpec{ProviderName: ""},
				},
			},
			expectErr: true,
			errMsg:    "upstreamInject requires a non-empty providerName",
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
			name: "multiple providers - valid at CRD level",
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
			expectErr: false,
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
		{
			name: "OIDC provider with valid additionalAuthorizationParams",
			provider: UpstreamProviderConfig{
				Name: "google",
				Type: UpstreamProviderTypeOIDC,
				OIDCConfig: &OIDCUpstreamConfig{
					IssuerURL: "https://accounts.google.com",
					ClientID:  "client-id",
					AdditionalAuthorizationParams: map[string]string{
						"access_type": "offline",
						"prompt":      "consent",
					},
				},
			},
			expectErr: false,
		},
		{
			name: "OIDC provider with reserved param client_id",
			provider: UpstreamProviderConfig{
				Name: "google",
				Type: UpstreamProviderTypeOIDC,
				OIDCConfig: &OIDCUpstreamConfig{
					IssuerURL: "https://accounts.google.com",
					ClientID:  "client-id",
					AdditionalAuthorizationParams: map[string]string{
						"client_id": "override-attempt",
					},
				},
			},
			expectErr: true,
			errMsg:    "reserved parameter \"client_id\" is managed by the framework",
		},
		{
			name: "OAuth2 provider with reserved param response_type",
			provider: UpstreamProviderConfig{
				Name: "custom",
				Type: UpstreamProviderTypeOAuth2,
				OAuth2Config: &OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://oauth.example.com/authorize",
					TokenEndpoint:         "https://oauth.example.com/token",
					ClientID:              "client-id",
					UserInfo:              &UserInfoConfig{EndpointURL: "https://oauth.example.com/userinfo"},
					AdditionalAuthorizationParams: map[string]string{
						"response_type": "token",
					},
				},
			},
			expectErr: true,
			errMsg:    "reserved parameter \"response_type\" is managed by the framework",
		},
		{
			name: "OAuth2 provider with valid additionalAuthorizationParams",
			provider: UpstreamProviderConfig{
				Name: "github",
				Type: UpstreamProviderTypeOAuth2,
				OAuth2Config: &OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
					TokenEndpoint:         "https://github.com/login/oauth/access_token",
					ClientID:              "client-id",
					UserInfo:              &UserInfoConfig{EndpointURL: "https://api.github.com/user"},
					AdditionalAuthorizationParams: map[string]string{
						"allow_signup": "false",
					},
				},
			},
			expectErr: false,
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

func TestEmbeddedAuthServerConfig_SyntheticIdentityUpstreams(t *testing.T) {
	t.Parallel()

	oidc := &UpstreamProviderConfig{
		Name:       "okta",
		Type:       UpstreamProviderTypeOIDC,
		OIDCConfig: &OIDCUpstreamConfig{IssuerURL: "https://okta.example.com", ClientID: "id"},
	}
	oauth2WithUserInfo := UpstreamProviderConfig{
		Name: "with-userinfo",
		Type: UpstreamProviderTypeOAuth2,
		OAuth2Config: &OAuth2UpstreamConfig{
			AuthorizationEndpoint: "https://idp/authorize",
			TokenEndpoint:         "https://idp/token",
			ClientID:              "client",
			UserInfo:              &UserInfoConfig{EndpointURL: "https://idp/userinfo"},
		},
	}
	oauth2NoUserInfo := UpstreamProviderConfig{
		Name: "no-userinfo",
		Type: UpstreamProviderTypeOAuth2,
		OAuth2Config: &OAuth2UpstreamConfig{
			AuthorizationEndpoint: "https://idp/authorize",
			TokenEndpoint:         "https://idp/token",
			ClientID:              "client",
		},
	}
	oauth2NoUserInfo2 := UpstreamProviderConfig{
		Name: "another-no-userinfo",
		Type: UpstreamProviderTypeOAuth2,
		OAuth2Config: &OAuth2UpstreamConfig{
			AuthorizationEndpoint: "https://idp/authorize",
			TokenEndpoint:         "https://idp/token",
			ClientID:              "client",
		},
	}

	tests := []struct {
		name string
		cfg  *EmbeddedAuthServerConfig
		want []string
	}{
		{
			name: "nil config returns nil",
			cfg:  nil,
			want: nil,
		},
		{
			name: "empty upstreams returns nil",
			cfg:  &EmbeddedAuthServerConfig{},
			want: nil,
		},
		{
			name: "OIDC-only is not synthesis-mode",
			cfg:  &EmbeddedAuthServerConfig{UpstreamProviders: []UpstreamProviderConfig{*oidc}},
			want: nil,
		},
		{
			name: "OAuth2 with userInfo is not synthesis-mode",
			cfg:  &EmbeddedAuthServerConfig{UpstreamProviders: []UpstreamProviderConfig{oauth2WithUserInfo}},
			want: nil,
		},
		{
			name: "single OAuth2 without userInfo is synthesis-mode",
			cfg:  &EmbeddedAuthServerConfig{UpstreamProviders: []UpstreamProviderConfig{oauth2NoUserInfo}},
			want: []string{"no-userinfo"},
		},
		{
			name: "multiple OAuth2 without userInfo returned in sorted order",
			cfg: &EmbeddedAuthServerConfig{UpstreamProviders: []UpstreamProviderConfig{
				oauth2NoUserInfo, oauth2NoUserInfo2,
			}},
			want: []string{"another-no-userinfo", "no-userinfo"},
		},
		{
			name: "mixed: only OAuth2-without-userInfo are returned",
			cfg: &EmbeddedAuthServerConfig{UpstreamProviders: []UpstreamProviderConfig{
				*oidc, oauth2WithUserInfo, oauth2NoUserInfo,
			}},
			want: []string{"no-userinfo"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.cfg.SyntheticIdentityUpstreams())
		})
	}
}
