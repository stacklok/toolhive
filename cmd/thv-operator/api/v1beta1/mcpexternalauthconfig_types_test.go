// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// makeStringOfLen returns a string of exactly n ASCII bytes.
func makeStringOfLen(n int) string {
	return strings.Repeat("a", n)
}

// makeURLOfLen returns an https URL whose total length is exactly n.
// The host is padded with 'a' characters; useful for boundary-length tests
// against +kubebuilder:validation:Pattern URL regexes that require a host.
func makeURLOfLen(n int) string {
	const prefix = "https://"
	if n <= len(prefix) {
		// Caller asked for something below the minimum URL shape. Fall back to
		// a plain string of length n so callers see the length they asked for.
		return strings.Repeat("a", n)
	}
	return prefix + strings.Repeat("a", n-len(prefix))
}

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
			name: "valid obo type with fully populated OBOConfig",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-obo-full",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeOBO,
					OBO: &OBOConfig{
						TenantID:        "72f988bf-86f1-41af-91ab-2d7cd011db47",
						ClientID:        "app-client-id",
						ClientSecretRef: &SecretKeyRef{Name: "entra-client", Key: "clientSecret"},
						Audience:        "api://backend",
					},
				},
			},
			expectErr: false,
		},
		{
			name: "valid xaa type",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-xaa",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeXAA,
					XAA: &XAASpec{
						IDPTokenURL:    "https://idp.example.com/token",
						TargetTokenURL: "https://target.example.com/token",
						TargetAudience: "https://target.example.com",
						TargetResource: "https://mcp.example.com",
					},
				},
			},
			expectErr: false,
		},
		{
			// Go Validate() intentionally does NOT check OBOConfig fields: the
			// required-field, pattern, and "at least one of audience or scopes"
			// rules are enforced by the kubebuilder markers + CEL at admission,
			// and the registered OBO handler validates semantics at reconcile.
			// So a minimal obo block passes the Go method even though the
			// apiserver would reject it (covered by the envtest CEL suite).
			name: "obo type with minimal OBOConfig passes Go Validate (field checks deferred)",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-obo",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeOBO,
					OBO:  &OBOConfig{},
				},
			},
			expectErr: false,
		},
		{
			name: "invalid obo type with nil obo config",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-obo-missing",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeOBO,
					OBO:  nil,
				},
			},
			expectErr: true,
			errMsg:    "obo configuration must be set if and only if type is 'obo'",
		},
		{
			name: "invalid obo config set on non-obo type",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-obo-on-tokenexchange",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type:          ExternalAuthTypeTokenExchange,
					TokenExchange: &TokenExchangeConfig{TokenURL: "https://example.com/token"},
					OBO:           &OBOConfig{},
				},
			},
			expectErr: true,
			errMsg:    "obo configuration must be set if and only if type is 'obo'",
		},
		{
			// Also intentional shape-parity coverage for the unauthenticated
			// guard's OBO != nil disjunct, even though the OBO biconditional
			// above intercepts first for this input.
			name: "invalid obo config on unauthenticated type (obo biconditional intercepts first)",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-obo-on-unauth",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeUnauthenticated,
					OBO:  &OBOConfig{},
				},
			},
			expectErr: true,
			errMsg:    "obo configuration must be set if and only if type is 'obo'",
		},
		{
			name: "invalid xaa with nil spec",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-xaa-nil",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeXAA,
					XAA:  nil,
				},
			},
			expectErr: true,
			errMsg:    "xaa configuration must be set if and only if type is 'xaa'",
		},
		{
			name: "invalid xaa config set on non-xaa type",
			config: &MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-xaa-on-tokenexchange",
					Namespace: "default",
				},
				Spec: MCPExternalAuthConfigSpec{
					Type:          ExternalAuthTypeTokenExchange,
					TokenExchange: &TokenExchangeConfig{TokenURL: "https://example.com/token"},
					XAA: &XAASpec{
						IDPTokenURL:    "https://idp.example.com/token",
						TargetTokenURL: "https://target.example.com/token",
						TargetAudience: "https://target.example.com",
						TargetResource: "https://mcp.example.com",
					},
				},
			},
			expectErr: true,
			errMsg:    "xaa configuration must be set if and only if type is 'xaa'",
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
		{
			name: "confidential clients combined with insecure HTTP - invalid",
			config: &MCPExternalAuthConfig{
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer:                              "http://auth.example.com",
						InsecureAllowHTTP:                   true,
						AllowConfidentialClientRegistration: true,
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
			expectErr: true,
			errMsg:    "allow_confidential_client_registration cannot be combined with insecure_allow_http",
		},
		{
			name: "confidential clients with plain-HTTP loopback issuer without opt-in - invalid",
			config: &MCPExternalAuthConfig{
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer:                              "http://localhost:8080",
						AllowConfidentialClientRegistration: true,
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
			expectErr: true,
			errMsg:    "insecure_allow_confidential_over_loopback_http",
		},
		{
			name: "confidential clients with plain-HTTP loopback issuer and opt-in - valid",
			config: &MCPExternalAuthConfig{
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer:                              "http://localhost:8080",
						AllowConfidentialClientRegistration: true,
						InsecureAllowConfidentialOverLoopbackHTTP: true,
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
			name: "confidential clients with https issuer unaffected by loopback opt-in default",
			config: &MCPExternalAuthConfig{
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer:                              "https://localhost:8080",
						AllowConfidentialClientRegistration: true,
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
			name: "forceConfidentialRedirectUris without allowConfidentialClientRegistration - invalid",
			config: &MCPExternalAuthConfig{
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer:                        "https://auth.example.com",
						ForceConfidentialRedirectURIs: []string{"https://app.example.com/cb"},
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
			expectErr: true,
			errMsg:    "requires allow_confidential_client_registration",
		},
		{
			name: "forceConfidentialRedirectUris with loopback entry - invalid",
			config: &MCPExternalAuthConfig{
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer:                              "https://auth.example.com",
						AllowConfidentialClientRegistration: true,
						ForceConfidentialRedirectURIs:       []string{"https://localhost/cb"},
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
			expectErr: true,
			errMsg:    "must not be a loopback redirect URI",
		},
		{
			name: "forceConfidentialRedirectUris with valid https entry and flag on - valid",
			config: &MCPExternalAuthConfig{
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer:                              "https://auth.example.com",
						AllowConfidentialClientRegistration: true,
						ForceConfidentialRedirectURIs:       []string{"https://app.example.com/cb"},
						UpstreamProviders: []UpstreamProviderConfig{{
							Name:       "github",
							Type:       UpstreamProviderTypeOIDC,
							OIDCConfig: &OIDCUpstreamConfig{IssuerURL: "https://github.com", ClientID: "client-id"},
						}},
					},
				},
			},
			expectErr: false,
		},
		{
			name: "trusted issuer matching embedded auth issuer is invalid",
			config: &MCPExternalAuthConfig{
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						UpstreamProviders: []UpstreamProviderConfig{{
							Name:       "github",
							Type:       UpstreamProviderTypeOIDC,
							OIDCConfig: &OIDCUpstreamConfig{IssuerURL: "https://github.com", ClientID: "client-id"},
						}},
						TrustedIssuers: []TrustedIssuerConfig{{
							IssuerURL:              "https://auth.example.com",
							ExpectedAudience:       "toolhive-authserver",
							AllowedDelegateClients: []string{"delegate-client"},
						}},
					},
				},
			},
			expectErr: true,
			errMsg:    "must not equal the authorization server's own issuer",
		},
		{
			// buildTrustedIssuerConfigs must carry actorMatcher through, or a
			// malformed CEL expression passes admission and only surfaces when
			// the auth server fails to start.
			name: "trusted issuer with malformed actorMatcher - invalid",
			config: &MCPExternalAuthConfig{
				Spec: MCPExternalAuthConfigSpec{
					Type: ExternalAuthTypeEmbeddedAuthServer,
					EmbeddedAuthServer: &EmbeddedAuthServerConfig{
						Issuer: "https://auth.example.com",
						UpstreamProviders: []UpstreamProviderConfig{
							{
								Name: "okta",
								Type: UpstreamProviderTypeOIDC,
								OIDCConfig: &OIDCUpstreamConfig{
									IssuerURL: "https://okta.example.com",
									ClientID:  "client",
								},
							},
						},
						TrustedIssuers: []TrustedIssuerConfig{
							{
								IssuerURL:              "https://external.example.com",
								ExpectedAudience:       "aud",
								ActorMatcher:           "this is not ( valid CEL",
								AllowedDelegateClients: []string{"*"},
							},
						},
					},
				},
			},
			expectErr: true,
			errMsg:    "actor_matcher",
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

func TestMCPExternalAuthConfig_ZeroUpstreamAlternatives(t *testing.T) {
	t.Parallel()

	jwtGrant := &JWTBearerGrantConfig{
		MaxAssertionAge: &metav1.Duration{Duration: time.Minute},
		SubjectBindings: []JWTBearerSubjectBinding{{
			Subject:          "service-account",
			AllowedResources: []string{"https://mcp.example.com"},
		}},
	}
	tests := []struct {
		name      string
		config    EmbeddedAuthServerConfig
		wantError string
	}{
		{
			name: "delegate client permits token-only operation",
			config: EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				DelegateClients: []DelegateClientConfig{{
					ClientID:        "delegate-client",
					ClientSecretRef: &SecretKeyRef{Name: "delegate-secret", Key: "client-secret"},
					Scopes:          []string{"openid"},
					Audiences:       []string{"https://mcp.example.com"},
				}},
			},
		},
		{
			name: "JWT bearer trusted issuer permits token-only operation",
			config: EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				TrustedIssuers: []TrustedIssuerConfig{{
					IssuerURL:      "https://issuer.example.com",
					JWTBearerGrant: jwtGrant,
				}},
			},
		},
		{
			name:      "no token-only alternative is rejected",
			config:    EmbeddedAuthServerConfig{Issuer: "https://auth.example.com"},
			wantError: "at least one upstream provider is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := (&MCPExternalAuthConfig{Spec: MCPExternalAuthConfigSpec{
				Type:               ExternalAuthTypeEmbeddedAuthServer,
				EmbeddedAuthServer: &tt.config,
			}}).Validate()
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
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
			// Pin the suffix that uniquely identifies the unified discriminator
			// message — distinguishes it from the per-config substring used in
			// the "missing oidcConfig" / "missing oauth2Config" cases above.
			errMsg: "(and the other must not be set)",
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
		{
			name: "OAuth2 provider with valid DCRConfig (discoveryUrl only)",
			provider: UpstreamProviderConfig{
				Name: "dcr-discovery",
				Type: UpstreamProviderTypeOAuth2,
				OAuth2Config: &OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://idp.example.com/authorize",
					TokenEndpoint:         "https://idp.example.com/token",
					UserInfo:              &UserInfoConfig{EndpointURL: "https://idp.example.com/userinfo"},
					DCRConfig: &DCRUpstreamConfig{
						DiscoveryURL: "https://idp.example.com/.well-known/openid-configuration",
					},
				},
			},
			expectErr: false,
		},
		{
			name: "OAuth2 provider with valid DCRConfig (registrationEndpoint only)",
			provider: UpstreamProviderConfig{
				Name: "dcr-endpoint",
				Type: UpstreamProviderTypeOAuth2,
				OAuth2Config: &OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://idp.example.com/authorize",
					TokenEndpoint:         "https://idp.example.com/token",
					UserInfo:              &UserInfoConfig{EndpointURL: "https://idp.example.com/userinfo"},
					Scopes:                []string{"openid"},
					DCRConfig: &DCRUpstreamConfig{
						RegistrationEndpoint: "https://idp.example.com/register",
					},
				},
			},
			expectErr: false,
		},
		{
			name: "OAuth2 provider with DCRConfig and non-empty ClientID",
			provider: UpstreamProviderConfig{
				Name: "dcr-with-clientid",
				Type: UpstreamProviderTypeOAuth2,
				OAuth2Config: &OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://idp.example.com/authorize",
					TokenEndpoint:         "https://idp.example.com/token",
					UserInfo:              &UserInfoConfig{EndpointURL: "https://idp.example.com/userinfo"},
					ClientID:              "pre-provisioned-id",
					DCRConfig: &DCRUpstreamConfig{
						DiscoveryURL: "https://idp.example.com/.well-known/openid-configuration",
					},
				},
			},
			expectErr: true,
			errMsg:    "exactly one of clientId or dcrConfig must be set",
		},
		{
			name: "OAuth2 provider with DCRConfig specifying both endpoints",
			provider: UpstreamProviderConfig{
				Name: "dcr-both-endpoints",
				Type: UpstreamProviderTypeOAuth2,
				OAuth2Config: &OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://idp.example.com/authorize",
					TokenEndpoint:         "https://idp.example.com/token",
					UserInfo:              &UserInfoConfig{EndpointURL: "https://idp.example.com/userinfo"},
					DCRConfig: &DCRUpstreamConfig{
						DiscoveryURL:         "https://idp.example.com/.well-known/openid-configuration",
						RegistrationEndpoint: "https://idp.example.com/register",
					},
				},
			},
			expectErr: true,
			errMsg:    "exactly one of discoveryUrl or registrationEndpoint must be set",
		},
		{
			name: "OAuth2 provider with DCRConfig specifying neither endpoint",
			provider: UpstreamProviderConfig{
				Name: "dcr-neither-endpoint",
				Type: UpstreamProviderTypeOAuth2,
				OAuth2Config: &OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://idp.example.com/authorize",
					TokenEndpoint:         "https://idp.example.com/token",
					UserInfo:              &UserInfoConfig{EndpointURL: "https://idp.example.com/userinfo"},
					DCRConfig:             &DCRUpstreamConfig{},
				},
			},
			expectErr: true,
			errMsg:    "exactly one of discoveryUrl or registrationEndpoint must be set",
		},
		{
			name: "OAuth2 provider with neither ClientID nor DCRConfig",
			provider: UpstreamProviderConfig{
				Name: "oauth2-missing-auth",
				Type: UpstreamProviderTypeOAuth2,
				OAuth2Config: &OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://idp.example.com/authorize",
					TokenEndpoint:         "https://idp.example.com/token",
					UserInfo:              &UserInfoConfig{EndpointURL: "https://idp.example.com/userinfo"},
				},
			},
			expectErr: true,
			// Pin the scoped prefix so a rename of the oauth2Config label surfaces as a test failure.
			errMsg: "oauth2Config: exactly one of clientId or dcrConfig must be set",
		},
		{
			// Regression guard for DCR leakage on OIDC-typed providers:
			// conversion only maps DCRConfig in the OAuth2 branch, so a stray
			// OAuth2Config/DCRConfig payload on an OIDC-typed provider must be
			// rejected at validate time rather than silently dropped. Set
			// OIDCConfig so the OIDC half of the discriminator passes; the
			// stray OAuth2Config is what trips the rule.
			name: "OIDC provider with stray OAuth2Config carrying DCRConfig is rejected",
			provider: UpstreamProviderConfig{
				Name:       "mismatched-oidc",
				Type:       UpstreamProviderTypeOIDC,
				OIDCConfig: &OIDCUpstreamConfig{IssuerURL: "https://oidc.example.com", ClientID: "client-id"},
				OAuth2Config: &OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://idp.example.com/authorize",
					TokenEndpoint:         "https://idp.example.com/token",
					UserInfo:              &UserInfoConfig{EndpointURL: "https://idp.example.com/userinfo"},
					DCRConfig: &DCRUpstreamConfig{
						DiscoveryURL: "https://idp.example.com/.well-known/openid-configuration",
					},
				},
			},
			expectErr: true,
			// The unified discriminator message is the same regardless of
			// which side trips it; pin the OAuth2-specific phrasing to
			// document that this case is exercising the OAuth2-leakage path.
			errMsg: "oauth2Config must be set when type is 'oauth2'",
		},
		{
			name: "OAuth2 provider with DCRConfig and ClientSecretRef is rejected",
			provider: UpstreamProviderConfig{
				Name: "dcr-with-client-secret",
				Type: UpstreamProviderTypeOAuth2,
				OAuth2Config: &OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://idp.example.com/authorize",
					TokenEndpoint:         "https://idp.example.com/token",
					UserInfo:              &UserInfoConfig{EndpointURL: "https://idp.example.com/userinfo"},
					ClientSecretRef:       &SecretKeyRef{Name: "stray", Key: "client-secret"},
					DCRConfig: &DCRUpstreamConfig{
						DiscoveryURL: "https://idp.example.com/.well-known/openid-configuration",
					},
				},
			},
			expectErr: true,
			errMsg:    "clientSecretRef must not be set when dcrConfig is set",
		},
		{
			name: "OAuth2 provider with DCRConfig discoveryUrl at MaxLength is accepted",
			provider: UpstreamProviderConfig{
				Name: "dcr-url-at-cap",
				Type: UpstreamProviderTypeOAuth2,
				OAuth2Config: &OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://idp.example.com/authorize",
					TokenEndpoint:         "https://idp.example.com/token",
					UserInfo:              &UserInfoConfig{EndpointURL: "https://idp.example.com/userinfo"},
					DCRConfig: &DCRUpstreamConfig{
						DiscoveryURL: makeURLOfLen(MaxDCRURLLength),
					},
				},
			},
			expectErr: false,
		},
		{
			name: "OAuth2 provider with DCRConfig discoveryUrl over MaxLength is rejected",
			provider: UpstreamProviderConfig{
				Name: "dcr-url-over-cap",
				Type: UpstreamProviderTypeOAuth2,
				OAuth2Config: &OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://idp.example.com/authorize",
					TokenEndpoint:         "https://idp.example.com/token",
					UserInfo:              &UserInfoConfig{EndpointURL: "https://idp.example.com/userinfo"},
					DCRConfig: &DCRUpstreamConfig{
						DiscoveryURL: makeURLOfLen(MaxDCRURLLength + 1),
					},
				},
			},
			expectErr: true,
			errMsg:    "dcrConfig.discoveryUrl: length",
		},
		{
			name: "OAuth2 provider with DCRConfig softwareStatement over MaxLength is rejected",
			provider: UpstreamProviderConfig{
				Name: "dcr-software-statement-over-cap",
				Type: UpstreamProviderTypeOAuth2,
				OAuth2Config: &OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://idp.example.com/authorize",
					TokenEndpoint:         "https://idp.example.com/token",
					UserInfo:              &UserInfoConfig{EndpointURL: "https://idp.example.com/userinfo"},
					DCRConfig: &DCRUpstreamConfig{
						DiscoveryURL:      "https://idp.example.com/.well-known/openid-configuration",
						SoftwareStatement: makeStringOfLen(MaxSoftwareStatementLength + 1),
					},
				},
			},
			expectErr: true,
			errMsg:    "dcrConfig.softwareStatement: length",
		},
		{
			name: "OAuth2 provider with identityFromToken empty subjectPath rejected",
			provider: UpstreamProviderConfig{
				Name: "snowflake",
				Type: UpstreamProviderTypeOAuth2,
				OAuth2Config: &OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://myaccount.snowflakecomputing.com/oauth/authorize",
					TokenEndpoint:         "https://myaccount.snowflakecomputing.com/oauth/token-request",
					ClientID:              "client-id",
					UserInfo:              &UserInfoConfig{EndpointURL: "https://myaccount.snowflakecomputing.com/api/v2/users/me"},
					IdentityFromToken:     &IdentityFromTokenConfig{SubjectPath: ""},
				},
			},
			expectErr: true,
			errMsg:    "identityFromToken.subjectPath must not be empty when identityFromToken is set",
		},
		{
			name: "OAuth2 provider with identityFromToken non-empty subjectPath accepted",
			provider: UpstreamProviderConfig{
				Name: "snowflake",
				Type: UpstreamProviderTypeOAuth2,
				OAuth2Config: &OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://myaccount.snowflakecomputing.com/oauth/authorize",
					TokenEndpoint:         "https://myaccount.snowflakecomputing.com/oauth/token-request",
					ClientID:              "client-id",
					UserInfo:              &UserInfoConfig{EndpointURL: "https://myaccount.snowflakecomputing.com/api/v2/users/me"},
					IdentityFromToken:     &IdentityFromTokenConfig{SubjectPath: "username"},
				},
			},
			expectErr: false,
		},
		{
			name: "OAuth2 provider with identityFromToken and userInfo both set — accepted",
			provider: UpstreamProviderConfig{
				Name: "snowflake",
				Type: UpstreamProviderTypeOAuth2,
				OAuth2Config: &OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://myaccount.snowflakecomputing.com/oauth/authorize",
					TokenEndpoint:         "https://myaccount.snowflakecomputing.com/oauth/token-request",
					ClientID:              "client-id",
					UserInfo:              &UserInfoConfig{EndpointURL: "https://myaccount.snowflakecomputing.com/api/v2/users/me"},
					IdentityFromToken:     &IdentityFromTokenConfig{SubjectPath: "username", NamePath: "display_name"},
				},
			},
			expectErr: false,
		},
		{
			name: "OAuth2 provider with identityFromToken and tokenResponseMapping both set — accepted",
			provider: UpstreamProviderConfig{
				Name: "slack",
				Type: UpstreamProviderTypeOAuth2,
				OAuth2Config: &OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://slack.com/oauth/v2/authorize",
					TokenEndpoint:         "https://slack.com/api/oauth.v2.access",
					ClientID:              "client-id",
					UserInfo:              &UserInfoConfig{EndpointURL: "https://slack.com/api/users.info"},
					TokenResponseMapping:  &TokenResponseMapping{AccessTokenPath: "authed_user.access_token"},
					IdentityFromToken:     &IdentityFromTokenConfig{SubjectPath: "authed_user.id"},
				},
			},
			expectErr: false,
		},
		{
			// UserInfo is +optional, and validateUpstreamProvider permits nil
			// UserInfo so identity can be resolved solely via IdentityFromToken
			// — the Snowflake / Slack shape where the userinfo endpoint is
			// absent or unusable.
			name: "OAuth2 provider with identityFromToken and nil userInfo — accepted at runtime",
			provider: UpstreamProviderConfig{
				Name: "snowflake",
				Type: UpstreamProviderTypeOAuth2,
				OAuth2Config: &OAuth2UpstreamConfig{
					AuthorizationEndpoint: "https://myaccount.snowflakecomputing.com/oauth/authorize",
					TokenEndpoint:         "https://myaccount.snowflakecomputing.com/oauth/token-request",
					ClientID:              "client-id",
					UserInfo:              nil,
					IdentityFromToken:     &IdentityFromTokenConfig{SubjectPath: "username"},
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

func TestDelegateClientConfig_JSON(t *testing.T) {
	t.Parallel()

	config := EmbeddedAuthServerConfig{
		Issuer: "https://auth.example.com",
		DelegateClients: []DelegateClientConfig{{
			ClientID:        "delegate-client",
			ClientSecretRef: &SecretKeyRef{Name: "delegate-secret", Key: "client-secret"},
			Scopes:          []string{"openid"},
			Audiences:       []string{"https://api.example.com"},
		}},
	}

	encoded, err := json.Marshal(config)
	require.NoError(t, err)

	var serialized map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &serialized))
	require.Contains(t, serialized, "delegateClients")
	require.JSONEq(t, `[
		{
			"clientId": "delegate-client",
			"clientSecretRef": {"name": "delegate-secret", "key": "client-secret"},
			"scopes": ["openid"],
			"audiences": ["https://api.example.com"]
		}
	]`, string(serialized["delegateClients"]))
	assert.NotContains(t, string(encoded), `"clientSecret":`)
	assert.NotContains(t, string(encoded), `"redirectUris":`)
	assert.NotContains(t, string(encoded), `"grantTypes":`)

	deepCopy := config.DeepCopy()
	require.NotNil(t, deepCopy)
	deepCopy.DelegateClients[0].ClientSecretRef.Name = "changed-secret"
	deepCopy.DelegateClients[0].Scopes[0] = "profile"
	deepCopy.DelegateClients[0].Audiences[0] = "https://changed.example.com"
	assert.Equal(t, "delegate-secret", config.DelegateClients[0].ClientSecretRef.Name)
	assert.Equal(t, "openid", config.DelegateClients[0].Scopes[0])
	assert.Equal(t, "https://api.example.com", config.DelegateClients[0].Audiences[0])

	var existing EmbeddedAuthServerConfig
	err = json.Unmarshal([]byte(`{"issuer":"https://auth.example.com","upstreamProviders":[]}`), &existing)
	require.NoError(t, err)
	assert.Nil(t, existing.DelegateClients)
	assert.False(t, existing.AllowConfidentialClientRegistration)
	assert.False(t, existing.AllowPrivateKeyJWTRegistration)
}

func TestEmbeddedAuthServerConfig_ValidateConfidentialClientTransport(t *testing.T) {
	t.Parallel()

	delegateClients := []DelegateClientConfig{{
		ClientID:        "delegate-client",
		ClientSecretRef: &SecretKeyRef{Name: "delegate-secret", Key: "client-secret"},
		Scopes:          []string{"openid"},
		Audiences:       []string{"https://api.example.com"},
	}}
	tests := []struct {
		name      string
		config    EmbeddedAuthServerConfig
		expectErr bool
	}{
		{
			name: "delegate clients remain independent of confidential DCR",
			config: EmbeddedAuthServerConfig{
				Issuer:          "https://auth.example.com",
				DelegateClients: delegateClients,
			},
		},
		{
			name: "delegate clients reject insecure HTTP",
			config: EmbeddedAuthServerConfig{
				Issuer:            "http://auth.example.com",
				InsecureAllowHTTP: true,
				DelegateClients:   delegateClients,
			},
			expectErr: true,
		},
		{
			name: "delegate clients reject HTTP loopback without explicit opt in",
			config: EmbeddedAuthServerConfig{
				Issuer:          "http://127.0.0.1:8080",
				DelegateClients: delegateClients,
			},
			expectErr: true,
		},
		{
			name: "delegate clients reject HTTP non-loopback without explicit opt in",
			config: EmbeddedAuthServerConfig{
				Issuer:          "http://auth.example.com",
				DelegateClients: delegateClients,
			},
			expectErr: true,
		},
		{
			name: "delegate clients reject HTTP non-loopback with loopback opt in",
			config: EmbeddedAuthServerConfig{
				Issuer: "http://auth.example.com",
				InsecureAllowConfidentialOverLoopbackHTTP: true,
				DelegateClients: delegateClients,
			},
			expectErr: true,
		},
		{
			name: "delegate clients allow HTTPS with loopback opt in",
			config: EmbeddedAuthServerConfig{
				Issuer: "https://auth.example.com",
				InsecureAllowConfidentialOverLoopbackHTTP: true,
				DelegateClients: delegateClients,
			},
		},
		{
			name: "private-key JWT registration passes with insecure HTTP (no secret to protect)",
			config: EmbeddedAuthServerConfig{
				Issuer:                         "http://auth.example.com",
				InsecureAllowHTTP:              true,
				AllowPrivateKeyJWTRegistration: true,
			},
		},
		{
			name: "private-key JWT registration does not enable confidential registration",
			config: EmbeddedAuthServerConfig{
				Issuer:                         "https://auth.example.com",
				AllowPrivateKeyJWTRegistration: true,
			},
		},
		{
			name: "private-key JWT registration passes with plain-HTTP loopback issuer (no secret to protect)",
			config: EmbeddedAuthServerConfig{
				Issuer:                         "http://localhost:8080",
				AllowPrivateKeyJWTRegistration: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.config.ValidateConfidentialClientTransport()
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestMCPExternalAuthConfig_DelegateClientsRejectUnsafeHTTP(t *testing.T) {
	t.Parallel()

	config := &MCPExternalAuthConfig{
		Spec: MCPExternalAuthConfigSpec{
			Type: ExternalAuthTypeEmbeddedAuthServer,
			EmbeddedAuthServer: &EmbeddedAuthServerConfig{
				Issuer:            "http://auth.example.com",
				InsecureAllowHTTP: true,
				DelegateClients: []DelegateClientConfig{{
					ClientID:        "delegate-client",
					ClientSecretRef: &SecretKeyRef{Name: "delegate-secret", Key: "client-secret"},
					Scopes:          []string{"openid"},
					Audiences:       []string{"https://api.example.com"},
				}},
				UpstreamProviders: []UpstreamProviderConfig{{
					Name:       "github",
					Type:       UpstreamProviderTypeOIDC,
					OIDCConfig: &OIDCUpstreamConfig{IssuerURL: "https://github.com", ClientID: "client-id"},
				}},
			},
		},
	}

	err := config.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insecure_allow_http")
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
	oauth2IdentityFromTokenOnly := UpstreamProviderConfig{
		Name: "snowflake",
		Type: UpstreamProviderTypeOAuth2,
		OAuth2Config: &OAuth2UpstreamConfig{
			AuthorizationEndpoint: "https://idp/authorize",
			TokenEndpoint:         "https://idp/token",
			ClientID:              "client",
			IdentityFromToken:     &IdentityFromTokenConfig{SubjectPath: "username"},
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
		{
			name: "OAuth2 with identityFromToken (no userInfo) is not synthesis-mode",
			cfg:  &EmbeddedAuthServerConfig{UpstreamProviders: []UpstreamProviderConfig{oauth2IdentityFromTokenOnly}},
			want: nil,
		},
		{
			name: "mixed: identityFromToken upstream excluded; synthesis-mode upstream included",
			cfg: &EmbeddedAuthServerConfig{UpstreamProviders: []UpstreamProviderConfig{
				oauth2IdentityFromTokenOnly, oauth2NoUserInfo,
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
