// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMCPExternalAuthConfig_ValidateCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		config        *MCPExternalAuthConfig
		expectError   bool
		errorMsg      string
		expectWarning bool
		warningMsg    string
	}{
		{
			name: "valid unauthenticated",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-unauthenticated",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeUnauthenticated,
				},
			},
			expectError:   false,
			expectWarning: true,
			warningMsg:    "'unauthenticated' type disables authentication to the backend. Only use for backends on trusted networks or when authentication is handled by network-level security.",
		},
		{
			name: "unauthenticated with tokenExchange should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeUnauthenticated,
					TokenExchange: &TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
					},
				},
			},
			expectError:   true,
			errorMsg:      "tokenExchange configuration must be set if and only if type is 'tokenExchange'",
			expectWarning: true,
			warningMsg:    "'unauthenticated' type disables authentication to the backend. Only use for backends on trusted networks or when authentication is handled by network-level security.",
		},
		{
			name: "unauthenticated with headerInjection should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeUnauthenticated,
					HeaderInjection: &HeaderInjectionConfig{
						HeaderName: "Authorization",
						ValueSecretRef: &SecretKeyRef{
							Name: "secret",
							Key:  "key",
						},
					},
				},
			},
			expectError:   true,
			errorMsg:      "headerInjection configuration must be set if and only if type is 'headerInjection'",
			expectWarning: true,
			warningMsg:    "'unauthenticated' type disables authentication to the backend. Only use for backends on trusted networks or when authentication is handled by network-level security.",
		},
		{
			name: "unauthenticated with bearerToken should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeUnauthenticated,
					BearerToken: &BearerTokenConfig{
						TokenSecretRef: &SecretKeyRef{
							Name: "bearer-token-secret",
							Key:  "token",
						},
					},
				},
			},
			expectError:   true,
			errorMsg:      "bearerToken configuration must be set if and only if type is 'bearerToken'",
			expectWarning: true,
			warningMsg:    "'unauthenticated' type disables authentication to the backend. Only use for backends on trusted networks or when authentication is handled by network-level security.",
		},
		{
			name: "valid tokenExchange",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-tokenexchange",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeTokenExchange,
					TokenExchange: &TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						Audience: "backend-service",
					},
				},
			},
			expectError: false,
		},
		{
			name: "tokenExchange without config should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeTokenExchange,
				},
			},
			expectError: true,
			errorMsg:    "tokenExchange configuration must be set if and only if type is 'tokenExchange'",
		},
		{
			name: "tokenExchange with headerInjection should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeTokenExchange,
					TokenExchange: &TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						Audience: "backend-service",
					},
					HeaderInjection: &HeaderInjectionConfig{
						HeaderName: "Authorization",
						ValueSecretRef: &SecretKeyRef{
							Name: "secret",
							Key:  "key",
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "headerInjection configuration must be set if and only if type is 'headerInjection'",
		},
		{
			name: "tokenExchange with bearerToken should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeTokenExchange,
					TokenExchange: &TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						Audience: "backend-service",
					},
					BearerToken: &BearerTokenConfig{
						TokenSecretRef: &SecretKeyRef{
							Name: "bearer-token-secret",
							Key:  "token",
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "bearerToken configuration must be set if and only if type is 'bearerToken'",
		},
		{
			name: "valid headerInjection",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-headerinjection",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeHeaderInjection,
					HeaderInjection: &HeaderInjectionConfig{
						HeaderName: "X-API-Key",
						ValueSecretRef: &SecretKeyRef{
							Name: "api-key-secret",
							Key:  "api-key",
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "headerInjection without config should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeHeaderInjection,
				},
			},
			expectError: true,
			errorMsg:    "headerInjection configuration must be set if and only if type is 'headerInjection'",
		},
		{
			name: "headerInjection with tokenExchange should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeHeaderInjection,
					HeaderInjection: &HeaderInjectionConfig{
						HeaderName: "X-API-Key",
						ValueSecretRef: &SecretKeyRef{
							Name: "secret",
							Key:  "key",
						},
					},
					TokenExchange: &TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
					},
				},
			},
			expectError: true,
			errorMsg:    "tokenExchange configuration must be set if and only if type is 'tokenExchange'",
		},
		{
			name: "headerInjection with bearerToken should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeHeaderInjection,
					HeaderInjection: &HeaderInjectionConfig{
						HeaderName: "X-API-Key",
						ValueSecretRef: &SecretKeyRef{
							Name: "secret",
							Key:  "key",
						},
					},
					BearerToken: &BearerTokenConfig{
						TokenSecretRef: &SecretKeyRef{
							Name: "bearer-token-secret",
							Key:  "token",
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "bearerToken configuration must be set if and only if type is 'bearerToken'",
		},
		{
			name: "valid bearerToken",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-bearertoken",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeBearerToken,
					BearerToken: &BearerTokenConfig{
						TokenSecretRef: &SecretKeyRef{
							Name: "bearer-token-secret",
							Key:  "token",
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "bearerToken without config should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeBearerToken,
				},
			},
			expectError: true,
			errorMsg:    "bearerToken configuration must be set if and only if type is 'bearerToken'",
		},
		{
			name: "bearerToken with tokenExchange should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeBearerToken,
					BearerToken: &BearerTokenConfig{
						TokenSecretRef: &SecretKeyRef{
							Name: "bearer-token-secret",
							Key:  "token",
						},
					},
					TokenExchange: &TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						Audience: "backend-service",
					},
				},
			},
			expectError: true,
			errorMsg:    "tokenExchange configuration must be set if and only if type is 'tokenExchange'",
		},
		{
			name: "bearerToken with headerInjection should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeBearerToken,
					BearerToken: &BearerTokenConfig{
						TokenSecretRef: &SecretKeyRef{
							Name: "bearer-token-secret",
							Key:  "token",
						},
					},
					HeaderInjection: &HeaderInjectionConfig{
						HeaderName: "X-API-Key",
						ValueSecretRef: &SecretKeyRef{
							Name: "secret",
							Key:  "key",
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "headerInjection configuration must be set if and only if type is 'headerInjection'",
		},
		// embeddedAuthServer test cases
		{
			name: "valid embeddedAuthServer with OIDC upstream",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-embedded-oidc",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						SigningKeySecretRefs: []SecretKeyRef{
							{Name: "signing-key-secret", Key: "private-key"},
						},
						HMACSecretRefs: []SecretKeyRef{
							{Name: "hmac-secret", Key: "secret"},
						},
						UpstreamProviders: []UpstreamProviderConfig{
							{
								Name: "okta",
								Type: UpstreamProviderTypeOIDC,
								OIDCConfig: &OIDCUpstreamConfig{
									IssuerURL:   "https://okta.example.com",
									ClientID:    "client-id",
									RedirectURI: "https://auth.example.com/callback",
								},
							},
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "valid embeddedAuthServer with OAuth2 upstream",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-embedded-oauth2",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						SigningKeySecretRefs: []SecretKeyRef{
							{Name: "signing-key-secret", Key: "private-key"},
						},
						HMACSecretRefs: []SecretKeyRef{
							{Name: "hmac-secret", Key: "secret"},
						},
						UpstreamProviders: []UpstreamProviderConfig{
							{
								Name: "github",
								Type: UpstreamProviderTypeOAuth2,
								OAuth2Config: &OAuth2UpstreamConfig{
									AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
									TokenEndpoint:         "https://github.com/login/oauth/access_token",
									UserInfo:              &UserInfoConfig{EndpointURL: "https://api.github.com/user"},
									ClientID:              "github-client-id",
									RedirectURI:           "https://auth.example.com/callback",
								},
							},
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "embeddedAuthServer without config should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
				},
			},
			expectError: true,
			errorMsg:    "embeddedAuthServer configuration must be set if and only if type is 'embeddedAuthServer'",
		},
		{
			name: "embeddedAuthServer with tokenExchange should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						SigningKeySecretRefs: []SecretKeyRef{
							{Name: "signing-key-secret", Key: "private-key"},
						},
						HMACSecretRefs: []SecretKeyRef{
							{Name: "hmac-secret", Key: "secret"},
						},
						UpstreamProviders: []UpstreamProviderConfig{
							{
								Name: "okta",
								Type: UpstreamProviderTypeOIDC,
								OIDCConfig: &OIDCUpstreamConfig{
									IssuerURL:   "https://okta.example.com",
									ClientID:    "client-id",
									RedirectURI: "https://auth.example.com/callback",
								},
							},
						},
					},
					TokenExchange: &TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						Audience: "backend-service",
					},
				},
			},
			expectError: true,
			errorMsg:    "tokenExchange configuration must be set if and only if type is 'tokenExchange'",
		},
		{
			name: "embeddedAuthServer with headerInjection should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						SigningKeySecretRefs: []SecretKeyRef{
							{Name: "signing-key-secret", Key: "private-key"},
						},
						HMACSecretRefs: []SecretKeyRef{
							{Name: "hmac-secret", Key: "secret"},
						},
						UpstreamProviders: []UpstreamProviderConfig{
							{
								Name: "okta",
								Type: UpstreamProviderTypeOIDC,
								OIDCConfig: &OIDCUpstreamConfig{
									IssuerURL:   "https://okta.example.com",
									ClientID:    "client-id",
									RedirectURI: "https://auth.example.com/callback",
								},
							},
						},
					},
					HeaderInjection: &HeaderInjectionConfig{
						HeaderName: "X-API-Key",
						ValueSecretRef: &SecretKeyRef{
							Name: "secret",
							Key:  "key",
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "headerInjection configuration must be set if and only if type is 'headerInjection'",
		},
		{
			name: "embeddedAuthServer with bearerToken should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						SigningKeySecretRefs: []SecretKeyRef{
							{Name: "signing-key-secret", Key: "private-key"},
						},
						HMACSecretRefs: []SecretKeyRef{
							{Name: "hmac-secret", Key: "secret"},
						},
						UpstreamProviders: []UpstreamProviderConfig{
							{
								Name: "okta",
								Type: UpstreamProviderTypeOIDC,
								OIDCConfig: &OIDCUpstreamConfig{
									IssuerURL:   "https://okta.example.com",
									ClientID:    "client-id",
									RedirectURI: "https://auth.example.com/callback",
								},
							},
						},
					},
					BearerToken: &BearerTokenConfig{
						TokenSecretRef: &SecretKeyRef{
							Name: "bearer-token-secret",
							Key:  "token",
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "bearerToken configuration must be set if and only if type is 'bearerToken'",
		},
		{
			name: "OIDC upstream with oauth2Config set should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						SigningKeySecretRefs: []SecretKeyRef{
							{Name: "signing-key-secret", Key: "private-key"},
						},
						HMACSecretRefs: []SecretKeyRef{
							{Name: "hmac-secret", Key: "secret"},
						},
						UpstreamProviders: []UpstreamProviderConfig{
							{
								Name: "mixed",
								Type: UpstreamProviderTypeOIDC,
								OIDCConfig: &OIDCUpstreamConfig{
									IssuerURL:   "https://okta.example.com",
									ClientID:    "client-id",
									RedirectURI: "https://auth.example.com/callback",
								},
								OAuth2Config: &OAuth2UpstreamConfig{
									AuthorizationEndpoint: "https://example.com/auth",
									TokenEndpoint:         "https://example.com/token",
									UserInfo:              &UserInfoConfig{EndpointURL: "https://example.com/userinfo"},
									ClientID:              "client-id",
									RedirectURI:           "https://auth.example.com/callback",
								},
							},
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "oauth2Config must be set when type is 'oauth2' and must not be set otherwise",
		},
		{
			name: "OAuth2 upstream with oidcConfig set should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						SigningKeySecretRefs: []SecretKeyRef{
							{Name: "signing-key-secret", Key: "private-key"},
						},
						HMACSecretRefs: []SecretKeyRef{
							{Name: "hmac-secret", Key: "secret"},
						},
						UpstreamProviders: []UpstreamProviderConfig{
							{
								Name: "mixed",
								Type: UpstreamProviderTypeOAuth2,
								OAuth2Config: &OAuth2UpstreamConfig{
									AuthorizationEndpoint: "https://example.com/auth",
									TokenEndpoint:         "https://example.com/token",
									UserInfo:              &UserInfoConfig{EndpointURL: "https://example.com/userinfo"},
									ClientID:              "client-id",
									RedirectURI:           "https://auth.example.com/callback",
								},
								OIDCConfig: &OIDCUpstreamConfig{
									IssuerURL:   "https://okta.example.com",
									ClientID:    "client-id",
									RedirectURI: "https://auth.example.com/callback",
								},
							},
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "oidcConfig must be set when type is 'oidc' and must not be set otherwise",
		},
		{
			name: "OIDC upstream missing oidcConfig should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						SigningKeySecretRefs: []SecretKeyRef{
							{Name: "signing-key-secret", Key: "private-key"},
						},
						HMACSecretRefs: []SecretKeyRef{
							{Name: "hmac-secret", Key: "secret"},
						},
						UpstreamProviders: []UpstreamProviderConfig{
							{
								Name: "missing-config",
								Type: UpstreamProviderTypeOIDC,
							},
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "oidcConfig must be set when type is 'oidc' and must not be set otherwise",
		},
		{
			name: "OAuth2 upstream missing oauth2Config should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						SigningKeySecretRefs: []SecretKeyRef{
							{Name: "signing-key-secret", Key: "private-key"},
						},
						HMACSecretRefs: []SecretKeyRef{
							{Name: "hmac-secret", Key: "secret"},
						},
						UpstreamProviders: []UpstreamProviderConfig{
							{
								Name: "missing-config",
								Type: UpstreamProviderTypeOAuth2,
							},
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "oauth2Config must be set when type is 'oauth2' and must not be set otherwise",
		},
		{
			name: "tokenExchange with embeddedAuthServer should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeTokenExchange,
					TokenExchange: &TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						Audience: "backend-service",
					},
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						SigningKeySecretRefs: []SecretKeyRef{
							{Name: "signing-key-secret", Key: "private-key"},
						},
						HMACSecretRefs: []SecretKeyRef{
							{Name: "hmac-secret", Key: "secret"},
						},
						UpstreamProviders: []UpstreamProviderConfig{
							{
								Name: "okta",
								Type: UpstreamProviderTypeOIDC,
								OIDCConfig: &OIDCUpstreamConfig{
									IssuerURL:   "https://okta.example.com",
									ClientID:    "client-id",
									RedirectURI: "https://auth.example.com/callback",
								},
							},
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "embeddedAuthServer configuration must be set if and only if type is 'embeddedAuthServer'",
		},
		{
			name: "headerInjection with embeddedAuthServer should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeHeaderInjection,
					HeaderInjection: &HeaderInjectionConfig{
						HeaderName: "X-API-Key",
						ValueSecretRef: &SecretKeyRef{
							Name: "secret",
							Key:  "key",
						},
					},
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						SigningKeySecretRefs: []SecretKeyRef{
							{Name: "signing-key-secret", Key: "private-key"},
						},
						HMACSecretRefs: []SecretKeyRef{
							{Name: "hmac-secret", Key: "secret"},
						},
						UpstreamProviders: []UpstreamProviderConfig{
							{
								Name: "okta",
								Type: UpstreamProviderTypeOIDC,
								OIDCConfig: &OIDCUpstreamConfig{
									IssuerURL:   "https://okta.example.com",
									ClientID:    "client-id",
									RedirectURI: "https://auth.example.com/callback",
								},
							},
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "embeddedAuthServer configuration must be set if and only if type is 'embeddedAuthServer'",
		},
		{
			name: "bearerToken with embeddedAuthServer should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeBearerToken,
					BearerToken: &BearerTokenConfig{
						TokenSecretRef: &SecretKeyRef{
							Name: "bearer-token-secret",
							Key:  "token",
						},
					},
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						SigningKeySecretRefs: []SecretKeyRef{
							{Name: "signing-key-secret", Key: "private-key"},
						},
						HMACSecretRefs: []SecretKeyRef{
							{Name: "hmac-secret", Key: "secret"},
						},
						UpstreamProviders: []UpstreamProviderConfig{
							{
								Name: "okta",
								Type: UpstreamProviderTypeOIDC,
								OIDCConfig: &OIDCUpstreamConfig{
									IssuerURL:   "https://okta.example.com",
									ClientID:    "client-id",
									RedirectURI: "https://auth.example.com/callback",
								},
							},
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "embeddedAuthServer configuration must be set if and only if type is 'embeddedAuthServer'",
		},
		{
			name: "unauthenticated with embeddedAuthServer should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeUnauthenticated,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						SigningKeySecretRefs: []SecretKeyRef{
							{Name: "signing-key-secret", Key: "private-key"},
						},
						HMACSecretRefs: []SecretKeyRef{
							{Name: "hmac-secret", Key: "secret"},
						},
						UpstreamProviders: []UpstreamProviderConfig{
							{
								Name: "okta",
								Type: UpstreamProviderTypeOIDC,
								OIDCConfig: &OIDCUpstreamConfig{
									IssuerURL:   "https://okta.example.com",
									ClientID:    "client-id",
									RedirectURI: "https://auth.example.com/callback",
								},
							},
						},
					},
				},
			},
			expectError:   true,
			errorMsg:      "embeddedAuthServer configuration must be set if and only if type is 'embeddedAuthServer'",
			expectWarning: true,
			warningMsg:    "'unauthenticated' type disables authentication to the backend. Only use for backends on trusted networks or when authentication is handled by network-level security.",
		},
		// awsSts test cases
		{
			name: "valid awsSts with fallbackRoleArn only",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-awssts-rolearn",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeAWSSts,
					AWSSts: &AWSStsConfig{
						Region:          "us-east-1",
						FallbackRoleArn: "arn:aws:iam::123456789012:role/TestRole",
					},
				},
			},
			expectError: false,
		},
		{
			name: "valid awsSts with roleMappings only (claim-based)",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-awssts-mappings",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeAWSSts,
					AWSSts: &AWSStsConfig{
						Region: "us-west-2",
						RoleMappings: []RoleMapping{
							{
								Claim:   "admins",
								RoleArn: "arn:aws:iam::123456789012:role/AdminRole",
							},
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "valid awsSts with fallbackRoleArn and roleMappings",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-awssts-fallback",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeAWSSts,
					AWSSts: &AWSStsConfig{
						Region:          "eu-west-1",
						FallbackRoleArn: "arn:aws:iam::123456789012:role/DefaultRole",
						RoleMappings: []RoleMapping{
							{
								Claim:   "admins",
								RoleArn: "arn:aws:iam::123456789012:role/AdminRole",
							},
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "valid awsSts with matcher-based role mapping",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-awssts-matcher",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeAWSSts,
					AWSSts: &AWSStsConfig{
						Region: "us-east-1",
						RoleMappings: []RoleMapping{
							{
								Matcher: `"admins" in claims["groups"]`,
								RoleArn: "arn:aws:iam::123456789012:role/AdminRole",
							},
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "awsSts without config should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeAWSSts,
				},
			},
			expectError: true,
			errorMsg:    "awsSts configuration must be set if and only if type is 'awsSts'",
		},
		{
			name: "awsSts missing region should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeAWSSts,
					AWSSts: &AWSStsConfig{
						Region:          "",
						FallbackRoleArn: "arn:aws:iam::123456789012:role/TestRole",
					},
				},
			},
			expectError: true,
			errorMsg:    "awsSts.region is required",
		},
		{
			name: "awsSts neither fallbackRoleArn nor roleMappings should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeAWSSts,
					AWSSts: &AWSStsConfig{
						Region: "us-east-1",
					},
				},
			},
			expectError: true,
			errorMsg:    "at least one of fallbackRoleArn or roleMappings must be configured",
		},
		{
			name: "awsSts roleMapping with neither claim nor matcher should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeAWSSts,
					AWSSts: &AWSStsConfig{
						Region: "us-east-1",
						RoleMappings: []RoleMapping{
							{
								RoleArn: "arn:aws:iam::123456789012:role/TestRole",
							},
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "exactly one of claim or matcher must be set",
		},
		{
			name: "awsSts roleMapping with both claim and matcher should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeAWSSts,
					AWSSts: &AWSStsConfig{
						Region: "us-east-1",
						RoleMappings: []RoleMapping{
							{
								Claim:   "admins",
								Matcher: `"admins" in claims["groups"]`,
								RoleArn: "arn:aws:iam::123456789012:role/TestRole",
							},
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "claim and matcher are mutually exclusive",
		},
		{
			name: "awsSts roleMapping missing roleArn should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeAWSSts,
					AWSSts: &AWSStsConfig{
						Region: "us-east-1",
						RoleMappings: []RoleMapping{
							{
								Claim: "admins",
							},
						},
					},
				},
			},
			expectError: true,
			errorMsg:    "awsSts.roleMappings[0].roleArn is required",
		},
		{
			name: "awsSts invalid session duration too low should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeAWSSts,
					AWSSts: &AWSStsConfig{
						Region:          "us-east-1",
						FallbackRoleArn: "arn:aws:iam::123456789012:role/TestRole",
						SessionDuration: int32Ptr(100),
					},
				},
			},
			expectError: true,
			errorMsg:    "awsSts.sessionDuration must be between 900 and 43200 seconds",
		},
		{
			name: "awsSts invalid session duration too high should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeAWSSts,
					AWSSts: &AWSStsConfig{
						Region:          "us-east-1",
						FallbackRoleArn: "arn:aws:iam::123456789012:role/TestRole",
						SessionDuration: int32Ptr(50000),
					},
				},
			},
			expectError: true,
			errorMsg:    "awsSts.sessionDuration must be between 900 and 43200 seconds",
		},
		{
			name: "awsSts with tokenExchange should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeAWSSts,
					AWSSts: &AWSStsConfig{
						Region:          "us-east-1",
						FallbackRoleArn: "arn:aws:iam::123456789012:role/TestRole",
					},
					TokenExchange: &TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						Audience: "backend-service",
					},
				},
			},
			expectError: true,
			errorMsg:    "tokenExchange configuration must be set if and only if type is 'tokenExchange'",
		},
		{
			name: "unauthenticated with awsSts should fail",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-invalid",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeUnauthenticated,
					AWSSts: &AWSStsConfig{
						Region:          "us-east-1",
						FallbackRoleArn: "arn:aws:iam::123456789012:role/TestRole",
					},
				},
			},
			expectError:   true,
			errorMsg:      "awsSts configuration must be set if and only if type is 'awsSts'",
			expectWarning: true,
			warningMsg:    "'unauthenticated' type disables authentication to the backend. Only use for backends on trusted networks or when authentication is handled by network-level security.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			warnings, err := tt.config.ValidateCreate(context.Background(), tt.config)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
			}

			// Check warnings
			if tt.expectWarning {
				require.Len(t, warnings, 1, "expected exactly one warning")
				assert.Equal(t, tt.warningMsg, string(warnings[0]))
			} else {
				assert.Nil(t, warnings, "expected no warnings")
			}
		})
	}
}

func validEmbeddedAuthServerConfig() *EmbeddedAuthServerConfig {
	return &EmbeddedAuthServerConfig{
		Issuer: "https://auth.example.com",
		SigningKeySecretRefs: []SecretKeyRef{
			{Name: "signing-key-secret", Key: "private-key"},
		},
		HMACSecretRefs: []SecretKeyRef{
			{Name: "hmac-secret", Key: "secret"},
		},
		UpstreamProviders: []UpstreamProviderConfig{
			{
				Name: "okta",
				Type: UpstreamProviderTypeOIDC,
				OIDCConfig: &OIDCUpstreamConfig{
					IssuerURL: "https://okta.example.com",
					ClientID:  "client-id",
				},
			},
		},
	}
}

func TestMCPExternalAuthConfig_ValidateStorageConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      *MCPExternalAuthConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid embedded auth server with no storage (defaults to memory)",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: MCPExternalAuthConfigSpec{
					Type:               ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: validEmbeddedAuthServerConfig(),
				},
			},
			expectError: false,
		},
		{
			name: "valid memory storage type",
			config: func() *MCPExternalAuthConfig {
				cfg := validEmbeddedAuthServerConfig()
				cfg.Storage = &AuthServerStorageConfig{Type: "memory"}
				return &MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
					Spec: MCPExternalAuthConfigSpec{
						Type:               ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: cfg,
					},
				}
			}(),
			expectError: false,
		},
		{
			name: "memory storage with redis config should fail",
			config: func() *MCPExternalAuthConfig {
				cfg := validEmbeddedAuthServerConfig()
				cfg.Storage = &AuthServerStorageConfig{
					Type:  "memory",
					Redis: &RedisStorageConfig{},
				}
				return &MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
					Spec: MCPExternalAuthConfigSpec{
						Type:               ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: cfg,
					},
				}
			}(),
			expectError: true,
			errorMsg:    "redis configuration must not be set when type is 'memory'",
		},
		{
			name: "redis storage without redis config should fail",
			config: func() *MCPExternalAuthConfig {
				cfg := validEmbeddedAuthServerConfig()
				cfg.Storage = &AuthServerStorageConfig{Type: "redis"}
				return &MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
					Spec: MCPExternalAuthConfigSpec{
						Type:               ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: cfg,
					},
				}
			}(),
			expectError: true,
			errorMsg:    "redis configuration is required when type is 'redis'",
		},
		{
			name: "valid redis storage with sentinel addrs",
			config: func() *MCPExternalAuthConfig {
				cfg := validEmbeddedAuthServerConfig()
				cfg.Storage = &AuthServerStorageConfig{
					Type: "redis",
					Redis: &RedisStorageConfig{
						SentinelConfig: &RedisSentinelConfig{
							MasterName:    "mymaster",
							SentinelAddrs: []string{"sentinel-0:26379", "sentinel-1:26379"},
						},
						ACLUserConfig: &RedisACLUserConfig{
							UsernameSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "username"},
							PasswordSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "password"},
						},
					},
				}
				return &MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
					Spec: MCPExternalAuthConfigSpec{
						Type:               ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: cfg,
					},
				}
			}(),
			expectError: false,
		},
		{
			name: "valid redis storage with sentinel service ref",
			config: func() *MCPExternalAuthConfig {
				cfg := validEmbeddedAuthServerConfig()
				cfg.Storage = &AuthServerStorageConfig{
					Type: "redis",
					Redis: &RedisStorageConfig{
						SentinelConfig: &RedisSentinelConfig{
							MasterName: "mymaster",
							SentinelService: &SentinelServiceRef{
								Name:      "redis-sentinel",
								Namespace: "redis-ns",
								Port:      26379,
							},
						},
						ACLUserConfig: &RedisACLUserConfig{
							UsernameSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "username"},
							PasswordSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "password"},
						},
					},
				}
				return &MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
					Spec: MCPExternalAuthConfigSpec{
						Type:               ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: cfg,
					},
				}
			}(),
			expectError: false,
		},
		{
			name: "redis storage missing sentinel config should fail",
			config: func() *MCPExternalAuthConfig {
				cfg := validEmbeddedAuthServerConfig()
				cfg.Storage = &AuthServerStorageConfig{
					Type: "redis",
					Redis: &RedisStorageConfig{
						ACLUserConfig: &RedisACLUserConfig{
							UsernameSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "username"},
							PasswordSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "password"},
						},
					},
				}
				return &MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
					Spec: MCPExternalAuthConfigSpec{
						Type:               ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: cfg,
					},
				}
			}(),
			expectError: true,
			errorMsg:    "sentinelConfig is required",
		},
		{
			name: "redis storage missing master name should fail",
			config: func() *MCPExternalAuthConfig {
				cfg := validEmbeddedAuthServerConfig()
				cfg.Storage = &AuthServerStorageConfig{
					Type: "redis",
					Redis: &RedisStorageConfig{
						SentinelConfig: &RedisSentinelConfig{
							SentinelAddrs: []string{"sentinel-0:26379"},
						},
						ACLUserConfig: &RedisACLUserConfig{
							UsernameSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "username"},
							PasswordSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "password"},
						},
					},
				}
				return &MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
					Spec: MCPExternalAuthConfigSpec{
						Type:               ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: cfg,
					},
				}
			}(),
			expectError: true,
			errorMsg:    "masterName is required",
		},
		{
			name: "both sentinelAddrs and sentinelService should fail",
			config: func() *MCPExternalAuthConfig {
				cfg := validEmbeddedAuthServerConfig()
				cfg.Storage = &AuthServerStorageConfig{
					Type: "redis",
					Redis: &RedisStorageConfig{
						SentinelConfig: &RedisSentinelConfig{
							MasterName:    "mymaster",
							SentinelAddrs: []string{"sentinel-0:26379"},
							SentinelService: &SentinelServiceRef{
								Name: "redis-sentinel",
							},
						},
						ACLUserConfig: &RedisACLUserConfig{
							UsernameSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "username"},
							PasswordSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "password"},
						},
					},
				}
				return &MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
					Spec: MCPExternalAuthConfigSpec{
						Type:               ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: cfg,
					},
				}
			}(),
			expectError: true,
			errorMsg:    "exactly one of sentinelAddrs or sentinelService must be specified, not both",
		},
		{
			name: "neither sentinelAddrs nor sentinelService should fail",
			config: func() *MCPExternalAuthConfig {
				cfg := validEmbeddedAuthServerConfig()
				cfg.Storage = &AuthServerStorageConfig{
					Type: "redis",
					Redis: &RedisStorageConfig{
						SentinelConfig: &RedisSentinelConfig{
							MasterName: "mymaster",
						},
						ACLUserConfig: &RedisACLUserConfig{
							UsernameSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "username"},
							PasswordSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "password"},
						},
					},
				}
				return &MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
					Spec: MCPExternalAuthConfigSpec{
						Type:               ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: cfg,
					},
				}
			}(),
			expectError: true,
			errorMsg:    "exactly one of sentinelAddrs or sentinelService must be specified",
		},
		{
			name: "sentinel service with empty name should fail",
			config: func() *MCPExternalAuthConfig {
				cfg := validEmbeddedAuthServerConfig()
				cfg.Storage = &AuthServerStorageConfig{
					Type: "redis",
					Redis: &RedisStorageConfig{
						SentinelConfig: &RedisSentinelConfig{
							MasterName:      "mymaster",
							SentinelService: &SentinelServiceRef{},
						},
						ACLUserConfig: &RedisACLUserConfig{
							UsernameSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "username"},
							PasswordSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "password"},
						},
					},
				}
				return &MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
					Spec: MCPExternalAuthConfigSpec{
						Type:               ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: cfg,
					},
				}
			}(),
			expectError: true,
			errorMsg:    "sentinelService: name is required",
		},
		{
			name: "redis storage missing acl user config should fail",
			config: func() *MCPExternalAuthConfig {
				cfg := validEmbeddedAuthServerConfig()
				cfg.Storage = &AuthServerStorageConfig{
					Type: "redis",
					Redis: &RedisStorageConfig{
						SentinelConfig: &RedisSentinelConfig{
							MasterName:    "mymaster",
							SentinelAddrs: []string{"sentinel-0:26379"},
						},
					},
				}
				return &MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
					Spec: MCPExternalAuthConfigSpec{
						Type:               ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: cfg,
					},
				}
			}(),
			expectError: true,
			errorMsg:    "aclUserConfig is required",
		},
		{
			name: "redis storage missing username secret ref should fail",
			config: func() *MCPExternalAuthConfig {
				cfg := validEmbeddedAuthServerConfig()
				cfg.Storage = &AuthServerStorageConfig{
					Type: "redis",
					Redis: &RedisStorageConfig{
						SentinelConfig: &RedisSentinelConfig{
							MasterName:    "mymaster",
							SentinelAddrs: []string{"sentinel-0:26379"},
						},
						ACLUserConfig: &RedisACLUserConfig{
							PasswordSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "password"},
						},
					},
				}
				return &MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
					Spec: MCPExternalAuthConfigSpec{
						Type:               ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: cfg,
					},
				}
			}(),
			expectError: true,
			errorMsg:    "usernameSecretRef is required",
		},
		{
			name: "redis storage missing password secret ref should fail",
			config: func() *MCPExternalAuthConfig {
				cfg := validEmbeddedAuthServerConfig()
				cfg.Storage = &AuthServerStorageConfig{
					Type: "redis",
					Redis: &RedisStorageConfig{
						SentinelConfig: &RedisSentinelConfig{
							MasterName:    "mymaster",
							SentinelAddrs: []string{"sentinel-0:26379"},
						},
						ACLUserConfig: &RedisACLUserConfig{
							UsernameSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "username"},
						},
					},
				}
				return &MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
					Spec: MCPExternalAuthConfigSpec{
						Type:               ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: cfg,
					},
				}
			}(),
			expectError: true,
			errorMsg:    "passwordSecretRef is required",
		},
		{
			name: "valid custom timeout durations",
			config: func() *MCPExternalAuthConfig {
				cfg := validEmbeddedAuthServerConfig()
				cfg.Storage = &AuthServerStorageConfig{
					Type: "redis",
					Redis: &RedisStorageConfig{
						SentinelConfig: &RedisSentinelConfig{
							MasterName:    "mymaster",
							SentinelAddrs: []string{"sentinel-0:26379"},
						},
						ACLUserConfig: &RedisACLUserConfig{
							UsernameSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "username"},
							PasswordSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "password"},
						},
						DialTimeout:  "10s",
						ReadTimeout:  "5s",
						WriteTimeout: "5s",
					},
				}
				return &MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
					Spec: MCPExternalAuthConfigSpec{
						Type:               ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: cfg,
					},
				}
			}(),
			expectError: false,
		},
		{
			name: "invalid dial timeout should fail",
			config: func() *MCPExternalAuthConfig {
				cfg := validEmbeddedAuthServerConfig()
				cfg.Storage = &AuthServerStorageConfig{
					Type: "redis",
					Redis: &RedisStorageConfig{
						SentinelConfig: &RedisSentinelConfig{
							MasterName:    "mymaster",
							SentinelAddrs: []string{"sentinel-0:26379"},
						},
						ACLUserConfig: &RedisACLUserConfig{
							UsernameSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "username"},
							PasswordSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "password"},
						},
						DialTimeout: "not-a-duration",
					},
				}
				return &MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
					Spec: MCPExternalAuthConfigSpec{
						Type:               ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: cfg,
					},
				}
			}(),
			expectError: true,
			errorMsg:    "invalid dialTimeout",
		},
		{
			name: "invalid read timeout should fail",
			config: func() *MCPExternalAuthConfig {
				cfg := validEmbeddedAuthServerConfig()
				cfg.Storage = &AuthServerStorageConfig{
					Type: "redis",
					Redis: &RedisStorageConfig{
						SentinelConfig: &RedisSentinelConfig{
							MasterName:    "mymaster",
							SentinelAddrs: []string{"sentinel-0:26379"},
						},
						ACLUserConfig: &RedisACLUserConfig{
							UsernameSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "username"},
							PasswordSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "password"},
						},
						ReadTimeout: "invalid",
					},
				}
				return &MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
					Spec: MCPExternalAuthConfigSpec{
						Type:               ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: cfg,
					},
				}
			}(),
			expectError: true,
			errorMsg:    "invalid readTimeout",
		},
		{
			name: "invalid write timeout should fail",
			config: func() *MCPExternalAuthConfig {
				cfg := validEmbeddedAuthServerConfig()
				cfg.Storage = &AuthServerStorageConfig{
					Type: "redis",
					Redis: &RedisStorageConfig{
						SentinelConfig: &RedisSentinelConfig{
							MasterName:    "mymaster",
							SentinelAddrs: []string{"sentinel-0:26379"},
						},
						ACLUserConfig: &RedisACLUserConfig{
							UsernameSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "username"},
							PasswordSecretRef: &SecretKeyRef{Name: "redis-secret", Key: "password"},
						},
						WriteTimeout: "abc",
					},
				}
				return &MCPExternalAuthConfig{
					ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
					Spec: MCPExternalAuthConfigSpec{
						Type:               ExternalAuthTypeEmbeddedAuthServer,
						EmbeddedAuthServer: cfg,
					},
				}
			}(),
			expectError: true,
			errorMsg:    "invalid writeTimeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := tt.config.ValidateCreate(context.Background(), tt.config)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestMCPExternalAuthConfig_ValidateUpdate(t *testing.T) {
	t.Parallel()

	config := &MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-unauthenticated",
			Namespace: "default",
		},
		Spec: MCPExternalAuthConfigSpec{
			Type: ExternalAuthTypeUnauthenticated,
		},
	}

	// ValidateUpdate should use the same logic as ValidateCreate
	warnings, err := config.ValidateUpdate(context.Background(), nil, config)
	require.NoError(t, err)
	// Should have warning for unauthenticated type
	require.Len(t, warnings, 1, "expected exactly one warning")
	assert.Equal(t, "'unauthenticated' type disables authentication to the backend. Only use for backends on trusted networks or when authentication is handled by network-level security.", string(warnings[0]))

	// Test invalid update
	invalidConfig := &MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-invalid",
			Namespace: "default",
		},
		Spec: MCPExternalAuthConfigSpec{
			Type: ExternalAuthTypeUnauthenticated,
			TokenExchange: &TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
			},
		},
	}

	warnings, err = invalidConfig.ValidateUpdate(context.Background(), nil, invalidConfig)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tokenExchange configuration must be set if and only if type is 'tokenExchange'")
	// Should still have warning for unauthenticated type even when validation fails
	require.Len(t, warnings, 1, "expected exactly one warning")
	assert.Equal(t, "'unauthenticated' type disables authentication to the backend. Only use for backends on trusted networks or when authentication is handled by network-level security.", string(warnings[0]))
}

func TestMCPExternalAuthConfig_ValidateDelete(t *testing.T) {
	t.Parallel()

	config := &MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-unauthenticated",
			Namespace: "default",
		},
		Spec: MCPExternalAuthConfigSpec{
			Type: ExternalAuthTypeUnauthenticated,
		},
	}

	// ValidateDelete should always succeed
	warnings, err := config.ValidateDelete(context.Background(), config)
	require.NoError(t, err)
	assert.Nil(t, warnings)
}

func int32Ptr(v int32) *int32 {
	return &v
}
