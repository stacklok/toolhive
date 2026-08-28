// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

func TestValidateIssuerURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		issuer            string
		insecureAllowHTTP bool
		wantErr           bool
		errMsg            string
	}{
		// Valid — strict mode (insecureAllowHTTP=false)
		{name: "https", issuer: "https://example.com"},
		{name: "https with port", issuer: "https://example.com:8443"},
		{name: "https with path", issuer: "https://example.com/auth"},
		{name: "http localhost", issuer: "http://localhost"},
		{name: "http localhost with port", issuer: "http://localhost:8080"},
		{name: "http 127.0.0.1", issuer: "http://127.0.0.1:8080"},
		{name: "http IPv6 loopback", issuer: "http://[::1]:8080"},

		// Valid — insecureAllowHTTP=true permits http for non-localhost
		{name: "http cluster-local insecure", issuer: "http://vmcp-foo.default.svc.cluster.local:4483", insecureAllowHTTP: true},
		{name: "http private IP insecure", issuer: "http://10.0.0.1:4483", insecureAllowHTTP: true},
		{name: "http non-localhost insecure", issuer: "http://example.com", insecureAllowHTTP: true},

		// Invalid — strict mode
		{name: "empty", issuer: "", wantErr: true, errMsg: "issuer is required"},
		{name: "missing scheme", issuer: "example.com", wantErr: true, errMsg: "scheme is required"},
		{name: "missing host", issuer: "https://", wantErr: true, errMsg: "host is required"},
		{name: "query component", issuer: "https://example.com?foo=bar", wantErr: true, errMsg: "must not contain query"},
		{name: "fragment component", issuer: "https://example.com#section", wantErr: true, errMsg: "must not contain fragment"},
		{
			name: "userinfo with password", issuer: "https://user:hunter2@example.com",
			wantErr: true, errMsg: "must not contain userinfo",
		},
		{
			// url.Parse populates User for a bare username too.
			name: "userinfo without password", issuer: "https://user@example.com",
			wantErr: true, errMsg: "must not contain userinfo",
		},
		{name: "http non-localhost", issuer: "http://example.com", wantErr: true, errMsg: "http scheme is only allowed for localhost"},
		{name: "ftp scheme", issuer: "ftp://example.com", wantErr: true, errMsg: "scheme must be https"},
		{name: "trailing slash", issuer: "https://example.com/", wantErr: true, errMsg: "must not have trailing slash"},

		// Valid — insecureAllowHTTP=true permits http for non-localhost
		{name: "http in-cluster insecure allowed", issuer: "http://vmcp-test.default.svc.cluster.local:4483", insecureAllowHTTP: true},
		{name: "http non-localhost insecure allowed", issuer: "http://example.com", insecureAllowHTTP: true},
		{name: "https still valid with insecure flag", issuer: "https://example.com", insecureAllowHTTP: true},
		{name: "http localhost still valid with insecure flag", issuer: "http://localhost:8080", insecureAllowHTTP: true},

		// Invalid — insecureAllowHTTP=true still enforces other rules
		{name: "trailing slash insecure", issuer: "http://example.com/", insecureAllowHTTP: true, wantErr: true, errMsg: "must not have trailing slash"},
		{name: "ftp scheme insecure", issuer: "ftp://example.com", insecureAllowHTTP: true, wantErr: true, errMsg: "scheme must be https"},
		{name: "empty insecure", issuer: "", insecureAllowHTTP: true, wantErr: true, errMsg: "issuer is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateIssuerURL(tt.issuer, tt.insecureAllowHTTP)
			assertError(t, err, tt.wantErr, tt.errMsg)
		})
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	validKeyProvider := keys.NewGeneratingProvider(keys.DefaultAlgorithm)
	validHMAC := &servercrypto.HMACSecrets{Current: make([]byte, 32)}
	shortHMAC := &servercrypto.HMACSecrets{Current: make([]byte, 16)}
	validUpstream := &upstream.OAuth2Config{
		CommonOAuthConfig:     upstream.CommonOAuthConfig{ClientID: "c", RedirectURI: "https://example.com/cb"},
		AuthorizationEndpoint: "https://idp.example.com/authorize",
		TokenEndpoint:         "https://idp.example.com/token",
	}
	validUpstreams := []UpstreamConfig{{Name: "default", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstream}}
	validOIDCUpstream := &upstream.OIDCConfig{
		CommonOAuthConfig: upstream.CommonOAuthConfig{ClientID: "c", RedirectURI: "https://example.com/cb"},
		Issuer:            "https://accounts.google.com",
	}
	validOIDCUpstreams := []UpstreamConfig{{Name: "default", Type: UpstreamProviderTypeOIDC, OIDCConfig: validOIDCUpstream}}

	tests := []struct {
		name    string
		config  Config
		wantErr bool
		errMsg  string
	}{
		{name: "missing issuer", config: Config{KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: validUpstreams}, wantErr: true, errMsg: "issuer is required"},
		{name: "nil HMAC secrets", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, Upstreams: validUpstreams}, wantErr: true, errMsg: "HMAC secrets are required"},
		{name: "HMAC too short", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: shortHMAC, Upstreams: validUpstreams}, wantErr: true, errMsg: "HMAC secret must be at least 32 bytes"},
		{name: "no upstreams", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC}, wantErr: true, errMsg: "at least one upstream is required"},
		{name: "nil upstream config", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: []UpstreamConfig{{Name: "test", Type: UpstreamProviderTypeOAuth2}}}, wantErr: true, errMsg: "oauth2_config is required"},
		{name: "multiple upstreams", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: []UpstreamConfig{{Name: "first", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstream}, {Name: "second", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstream}}, AllowedAudiences: []string{"https://mcp.example.com"}}},
		{name: "upstream_filter with single upstream rejected", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: validUpstreams, AllowedAudiences: []string{"https://mcp.example.com"}, UpstreamFilter: &stubChainFilter{}}, wantErr: true, errMsg: "upstream_filter is configured but has no effect"},
		{name: "upstream_filter with multiple upstreams allowed", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: []UpstreamConfig{{Name: "first", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstream}, {Name: "second", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstream}}, AllowedAudiences: []string{"https://mcp.example.com"}, UpstreamFilter: &stubChainFilter{}}},
		{name: "duplicate upstream names", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: []UpstreamConfig{{Name: "same", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstream}, {Name: "same", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstream}}}, wantErr: true, errMsg: "duplicate upstream name"},
		{name: "multi-upstream with empty name on second", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: []UpstreamConfig{{Name: "first", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstream}, {Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstream}}}, wantErr: true, errMsg: "upstream[1]: name must be explicitly set"},
		{name: "multi-upstream with empty name on first", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: []UpstreamConfig{{Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstream}, {Name: "second", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstream}}}, wantErr: true, errMsg: "upstream[0]: name must be explicitly set"},
		{name: "multi-upstream with default name", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: []UpstreamConfig{{Name: "first", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstream}, {Name: "default", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstream}}}, wantErr: true, errMsg: `reserved for single-upstream`},
		{name: "upstream name with uppercase", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: []UpstreamConfig{{Name: "GitHub", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstream}}, AllowedAudiences: []string{"https://mcp.example.com"}}, wantErr: true, errMsg: "must match"},
		{name: "upstream name with underscore", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: []UpstreamConfig{{Name: "my_provider", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstream}}, AllowedAudiences: []string{"https://mcp.example.com"}}, wantErr: true, errMsg: "must match"},
		{name: "upstream name with leading hyphen", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: []UpstreamConfig{{Name: "-github", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstream}}, AllowedAudiences: []string{"https://mcp.example.com"}}, wantErr: true, errMsg: "must match"},
		{name: "upstream name with trailing hyphen", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: []UpstreamConfig{{Name: "github-", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstream}}, AllowedAudiences: []string{"https://mcp.example.com"}}, wantErr: true, errMsg: "must match"},
		{name: "valid upstream name with hyphens", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: []UpstreamConfig{{Name: "my-provider", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstream}}, AllowedAudiences: []string{"https://mcp.example.com"}}},
		{name: "valid single-char upstream name", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: []UpstreamConfig{{Name: "a", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstream}}, AllowedAudiences: []string{"https://mcp.example.com"}}},
		{name: "missing allowed audiences", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: validUpstreams}, wantErr: true, errMsg: "at least one allowed audience is required"},
		{name: "empty allowed audiences slice", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: validUpstreams, AllowedAudiences: []string{}}, wantErr: true, errMsg: "at least one allowed audience is required"},

		// AuthorizationEndpointBaseURL validation
		{name: "invalid authorization_endpoint_base_url", config: Config{Issuer: "https://example.com", AuthorizationEndpointBaseURL: "ftp://bad.example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: validUpstreams, AllowedAudiences: []string{"https://mcp.example.com"}}, wantErr: true, errMsg: "authorization_endpoint_base_url"},
		{name: "authorization_endpoint_base_url with trailing slash", config: Config{Issuer: "https://example.com", AuthorizationEndpointBaseURL: "https://login.example.com/", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: validUpstreams, AllowedAudiences: []string{"https://mcp.example.com"}}, wantErr: true, errMsg: "authorization_endpoint_base_url"},
		{name: "valid authorization_endpoint_base_url", config: Config{Issuer: "https://example.com", AuthorizationEndpointBaseURL: "https://login.example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: validUpstreams, AllowedAudiences: []string{"https://mcp.example.com"}}},

		// OIDC upstream validation
		{name: "OIDC nil oidc_config", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: []UpstreamConfig{{Name: "test", Type: UpstreamProviderTypeOIDC}}, AllowedAudiences: []string{"https://mcp.example.com"}}, wantErr: true, errMsg: "oidc_config is required"},
		{name: "unsupported upstream type", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: []UpstreamConfig{{Name: "test", Type: UpstreamProviderType("saml")}}, AllowedAudiences: []string{"https://mcp.example.com"}}, wantErr: true, errMsg: "unsupported provider type"},
		{name: "OIDC with oauth2_config set rejects", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: []UpstreamConfig{{Name: "test", Type: UpstreamProviderTypeOIDC, OIDCConfig: validOIDCUpstream, OAuth2Config: validUpstream}}, AllowedAudiences: []string{"https://mcp.example.com"}}, wantErr: true, errMsg: "oauth2_config must not be set"},
		{name: "OAuth2 with oidc_config set rejects", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: []UpstreamConfig{{Name: "test", Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstream, OIDCConfig: validOIDCUpstream}}, AllowedAudiences: []string{"https://mcp.example.com"}}, wantErr: true, errMsg: "oidc_config must not be set"},

		{name: "OAuth2 HTTP endpoints require an upstream or global insecure allow flag", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: []UpstreamConfig{{Name: "default", Type: UpstreamProviderTypeOAuth2, OAuth2Config: &upstream.OAuth2Config{CommonOAuthConfig: upstream.CommonOAuthConfig{ClientID: "c", RedirectURI: "https://example.com/cb"}, AuthorizationEndpoint: "http://idp.default.svc.cluster.local/authorize", TokenEndpoint: "http://idp.default.svc.cluster.local/token", InsecureAllowHTTP: true}}}, AllowedAudiences: []string{"https://mcp.example.com"}}},
		{name: "OIDC HTTP issuer requires an upstream or global insecure allow flag", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: []UpstreamConfig{{Name: "default", Type: UpstreamProviderTypeOIDC, OIDCConfig: &upstream.OIDCConfig{CommonOAuthConfig: upstream.CommonOAuthConfig{ClientID: "c", RedirectURI: "https://example.com/cb"}, Issuer: "http://idp.default.svc.cluster.local", InsecureAllowHTTP: true}}}, AllowedAudiences: []string{"https://mcp.example.com"}}},

		// BaselineClientScopes subset gate (mirrors RunConfig.Validate but on the
		// runtime Config — catches direct constructors that bypass YAML loading).
		{name: "baseline scope not in scopes_supported", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: validUpstreams, AllowedAudiences: []string{"https://mcp.example.com"}, ScopesSupported: []string{"openid"}, BaselineClientScopes: []string{"offline_access"}}, wantErr: true, errMsg: `baseline_client_scopes contains "offline_access"`},
		{name: "nil supported with baseline in DefaultScopes passes", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: validUpstreams, AllowedAudiences: []string{"https://mcp.example.com"}, ScopesSupported: nil, BaselineClientScopes: []string{"offline_access"}}},

		// CIMD validation
		{name: "CIMD enabled zero cache_max_size rejected", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: validUpstreams, AllowedAudiences: []string{"https://mcp.example.com"}, CIMDEnabled: true, CIMDCacheMaxSize: 0}, wantErr: true, errMsg: "cache_max_size must be >= 1"},
		{name: "CIMD enabled negative cache_max_size rejected", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: validUpstreams, AllowedAudiences: []string{"https://mcp.example.com"}, CIMDEnabled: true, CIMDCacheMaxSize: -1}, wantErr: true, errMsg: "cache_max_size must be >= 1"},
		{name: "CIMD enabled negative cache_fallback_ttl rejected", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: validUpstreams, AllowedAudiences: []string{"https://mcp.example.com"}, CIMDEnabled: true, CIMDCacheMaxSize: 256, CIMDCacheFallbackTTL: -time.Second}, wantErr: true, errMsg: "cache_fallback_ttl must be non-negative"},
		{name: "CIMD disabled ignores invalid cache fields", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: validUpstreams, AllowedAudiences: []string{"https://mcp.example.com"}, CIMDEnabled: false, CIMDCacheMaxSize: -1, CIMDCacheFallbackTTL: -time.Second}},
		{name: "CIMD enabled with valid bounds passes", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: validUpstreams, AllowedAudiences: []string{"https://mcp.example.com"}, CIMDEnabled: true, CIMDCacheMaxSize: 256, CIMDCacheFallbackTTL: 5 * time.Minute}},

		// Confidential-client transport gate (same predicate RunConfig.Validate uses)
		{name: "confidential clients combined with insecure HTTP rejects", config: Config{Issuer: "http://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: validUpstreams, AllowedAudiences: []string{"https://mcp.example.com"}, AllowConfidentialClientRegistration: true, InsecureAllowHTTP: true}, wantErr: true, errMsg: "allow_confidential_client_registration cannot be combined with insecure_allow_http"},
		{name: "private-key JWT registration combined with insecure HTTP passes (no secret to protect)", config: Config{Issuer: "http://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: validUpstreams, AllowedAudiences: []string{"https://mcp.example.com"}, AllowPrivateKeyJWTRegistration: true, InsecureAllowHTTP: true}},

		// Valid configs
		{name: "valid minimal", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: validUpstreams, AllowedAudiences: []string{"https://mcp.example.com"}}},
		{name: "valid nil key provider", config: Config{Issuer: "https://example.com", HMACSecrets: validHMAC, Upstreams: validUpstreams, AllowedAudiences: []string{"https://mcp.example.com"}}},
		{name: "valid empty upstream name defaults", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: []UpstreamConfig{{Type: UpstreamProviderTypeOAuth2, OAuth2Config: validUpstream}}, AllowedAudiences: []string{"https://mcp.example.com"}}},
		{name: "valid OIDC upstream", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstreams: validOIDCUpstreams, AllowedAudiences: []string{"https://mcp.example.com"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.config.Validate()
			assertError(t, err, tt.wantErr, tt.errMsg)
		})
	}
}

func TestConfigValidate_ZeroUpstreamAlternatives(t *testing.T) {
	t.Parallel()

	secret := []byte("0123456789abcdef0123456789abcdef")
	configs := []struct {
		name string
		cfg  Config
	}{
		{
			name: "delegate client",
			cfg: Config{
				Issuer:           "https://example.com",
				HMACSecrets:      servercrypto.NewHMACSecrets(secret),
				AllowedAudiences: []string{"https://mcp.example.com"},
				DelegateClients: []DelegateClient{{
					ClientID:     "delegate",
					ClientSecret: strings.Repeat("a", minDelegateClientSecretLength),
					Scopes:       []string{"openid"},
					Audiences:    []string{"https://mcp.example.com"},
				}},
			},
		},
		{
			name: "JWT bearer trusted issuer",
			cfg: Config{
				Issuer:           "https://example.com",
				HMACSecrets:      servercrypto.NewHMACSecrets(secret),
				AllowedAudiences: []string{"https://mcp.example.com"},
				TrustedIssuers: []tokenexchange.TrustedIssuer{{
					IssuerURL: "https://idp.example.com",
					JWTBearerGrant: &tokenexchange.JWTBearerGrantPolicy{
						MaxAssertionAge: "1m",
						SubjectBindings: []tokenexchange.JWTBearerSubjectBinding{{
							Subject:          "workload",
							AllowedResources: []string{"https://mcp.example.com"},
						}},
						AcceptedAudiences: []string{"https://example.com/oauth/token"},
					},
				}},
			},
		},
	}

	for _, tt := range configs {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.NoError(t, tt.cfg.Validate())
		})
	}
}

func TestConfigApplyDefaults(t *testing.T) {
	t.Parallel()

	t.Run("HMAC secret generation", func(t *testing.T) {
		t.Parallel()
		cfg := Config{Issuer: "https://example.com"}

		if err := cfg.applyDefaults(); err != nil {
			t.Fatalf("applyDefaults failed: %v", err)
		}

		if cfg.HMACSecrets == nil || len(cfg.HMACSecrets.Current) < servercrypto.MinSecretLength {
			t.Errorf("expected HMAC secret >= %d bytes", servercrypto.MinSecretLength)
		}
	})

	t.Run("HMAC secret preservation", func(t *testing.T) {
		t.Parallel()
		secret := []byte("0123456789abcdef0123456789abcdef")
		cfg := Config{Issuer: "https://example.com", HMACSecrets: &servercrypto.HMACSecrets{Current: secret}}

		if err := cfg.applyDefaults(); err != nil {
			t.Fatalf("applyDefaults failed: %v", err)
		}

		if !bytes.Equal(cfg.HMACSecrets.Current, secret) {
			t.Error("HMAC secret was overwritten")
		}
	})

	t.Run("KeyProvider generation", func(t *testing.T) {
		t.Parallel()
		cfg := Config{Issuer: "https://example.com"}

		if err := cfg.applyDefaults(); err != nil {
			t.Fatalf("applyDefaults failed: %v", err)
		}

		if cfg.KeyProvider == nil {
			t.Fatal("expected KeyProvider to be generated")
		}
	})

	t.Run("KeyProvider preservation", func(t *testing.T) {
		t.Parallel()
		existingProvider := keys.NewGeneratingProvider("ES384")
		cfg := Config{Issuer: "https://example.com", KeyProvider: existingProvider}

		if err := cfg.applyDefaults(); err != nil {
			t.Fatalf("applyDefaults failed: %v", err)
		}

		if cfg.KeyProvider != existingProvider {
			t.Error("KeyProvider was overwritten")
		}
	})

	// Lifespan defaults - table-driven
	lifespanTests := []struct {
		name                                  string
		input                                 Config
		wantAccess, wantRefresh, wantAuthCode time.Duration
	}{
		{
			name:         "applies defaults",
			input:        Config{Issuer: "https://example.com"},
			wantAccess:   time.Hour,
			wantRefresh:  7 * 24 * time.Hour,
			wantAuthCode: 10 * time.Minute,
		},
		{
			name: "preserves custom values",
			input: Config{
				Issuer:               "https://example.com",
				AccessTokenLifespan:  5 * time.Minute,
				RefreshTokenLifespan: 24 * time.Hour,
				AuthCodeLifespan:     2 * time.Minute,
			},
			wantAccess:   5 * time.Minute,
			wantRefresh:  24 * time.Hour,
			wantAuthCode: 2 * time.Minute,
		},
	}

	for _, tt := range lifespanTests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := tt.input
			if err := cfg.applyDefaults(); err != nil {
				t.Fatalf("applyDefaults failed: %v", err)
			}
			if cfg.AccessTokenLifespan != tt.wantAccess {
				t.Errorf("AccessTokenLifespan = %v, want %v", cfg.AccessTokenLifespan, tt.wantAccess)
			}
			if cfg.RefreshTokenLifespan != tt.wantRefresh {
				t.Errorf("RefreshTokenLifespan = %v, want %v", cfg.RefreshTokenLifespan, tt.wantRefresh)
			}
			if cfg.AuthCodeLifespan != tt.wantAuthCode {
				t.Errorf("AuthCodeLifespan = %v, want %v", cfg.AuthCodeLifespan, tt.wantAuthCode)
			}
		})
	}
}

// assertError is a test helper for consistent error checking.
func assertError(t *testing.T, err error, wantErr bool, errMsg string) {
	t.Helper()
	if wantErr {
		if errMsg == "" {
			t.Fatal("wantErr is true but errMsg is empty: strings.Contains(x, \"\") is always true, so this case would pass unconditionally")
		}
		if err == nil {
			t.Errorf("expected error containing %q, got nil", errMsg)
		} else if !strings.Contains(err.Error(), errMsg) {
			t.Errorf("expected error containing %q, got %q", errMsg, err.Error())
		}
	} else if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestOAuth2UpstreamRunConfigValidate(t *testing.T) {
	t.Parallel()

	validDCR := &DCRUpstreamConfig{
		DiscoveryURL: "https://idp.example.com/.well-known/oauth-authorization-server",
	}

	tests := []struct {
		name    string
		config  OAuth2UpstreamRunConfig
		wantErr bool
		errMsg  string
	}{
		// Four ClientID x DCRConfig combinations.
		{
			name:    "empty ClientID and nil DCRConfig rejects",
			config:  OAuth2UpstreamRunConfig{},
			wantErr: true,
			errMsg:  "either client_id or dcr_config is required",
		},
		{
			name:    "non-empty ClientID and non-nil DCRConfig rejects",
			config:  OAuth2UpstreamRunConfig{ClientID: "c", DCRConfig: validDCR},
			wantErr: true,
			errMsg:  "client_id and dcr_config are mutually exclusive",
		},
		{
			name:   "non-empty ClientID and nil DCRConfig is valid",
			config: OAuth2UpstreamRunConfig{ClientID: "c"},
		},
		{
			name:   "empty ClientID and non-nil DCRConfig is valid",
			config: OAuth2UpstreamRunConfig{DCRConfig: validDCR},
		},

		// DCRConfig exactly-one-of rule propagates.
		{
			name: "DCRConfig with both discovery_url and registration_endpoint rejects",
			config: OAuth2UpstreamRunConfig{
				DCRConfig: &DCRUpstreamConfig{
					DiscoveryURL:         "https://idp.example.com/.well-known/oauth-authorization-server",
					RegistrationEndpoint: "https://idp.example.com/register",
				},
			},
			wantErr: true,
			errMsg:  "discovery_url and registration_endpoint are mutually exclusive",
		},
		{
			name: "DCRConfig with neither discovery_url nor registration_endpoint rejects",
			config: OAuth2UpstreamRunConfig{
				DCRConfig: &DCRUpstreamConfig{},
			},
			wantErr: true,
			errMsg:  "either discovery_url or registration_endpoint is required",
		},
		{
			name: "DCRConfig with only registration_endpoint is valid when authorization_endpoint and token_endpoint are also set",
			config: OAuth2UpstreamRunConfig{
				AuthorizationEndpoint: "https://idp.example.com/authorize",
				TokenEndpoint:         "https://idp.example.com/token",
				DCRConfig: &DCRUpstreamConfig{
					RegistrationEndpoint: "https://idp.example.com/register",
				},
			},
		},

		// registration_endpoint requires explicit authorize/token endpoints.
		// Discovery would have populated them; bypassing discovery means the
		// run-config must supply them or the upstream is unusable.
		{
			name: "DCRConfig.registration_endpoint without authorization_endpoint rejects",
			config: OAuth2UpstreamRunConfig{
				TokenEndpoint: "https://idp.example.com/token",
				DCRConfig: &DCRUpstreamConfig{
					RegistrationEndpoint: "https://idp.example.com/register",
				},
			},
			wantErr: true,
			errMsg:  "authorization_endpoint and token_endpoint are required",
		},
		{
			name: "DCRConfig.registration_endpoint without token_endpoint rejects",
			config: OAuth2UpstreamRunConfig{
				AuthorizationEndpoint: "https://idp.example.com/authorize",
				DCRConfig: &DCRUpstreamConfig{
					RegistrationEndpoint: "https://idp.example.com/register",
				},
			},
			wantErr: true,
			errMsg:  "authorization_endpoint and token_endpoint are required",
		},
		{
			name: "DCRConfig.discovery_url is valid without explicit endpoints (discovery populates them)",
			config: OAuth2UpstreamRunConfig{
				DCRConfig: &DCRUpstreamConfig{
					DiscoveryURL: "https://idp.example.com/.well-known/oauth-authorization-server",
				},
			},
		},

		// IdentityFromToken subject_path requirement.
		{
			name: "IdentityFromToken with empty SubjectPath rejects",
			config: OAuth2UpstreamRunConfig{
				ClientID:          "c",
				IdentityFromToken: &IdentityFromTokenRunConfig{},
			},
			wantErr: true,
			errMsg:  "identity_from_token.subject_path must not be empty",
		},
		{
			name: "IdentityFromToken with non-empty SubjectPath is valid",
			config: OAuth2UpstreamRunConfig{
				ClientID: "c",
				IdentityFromToken: &IdentityFromTokenRunConfig{
					SubjectPath: "username",
				},
			},
		},
		{
			name: "nil IdentityFromToken is valid",
			config: OAuth2UpstreamRunConfig{
				ClientID: "c",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.config.Validate()
			assertError(t, err, tt.wantErr, tt.errMsg)
		})
	}
}

func TestRunConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  RunConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:   "nil baseline scopes passes",
			config: RunConfig{ScopesSupported: []string{"openid", "profile"}, BaselineClientScopes: nil},
		},
		{
			name:   "empty baseline scopes passes",
			config: RunConfig{ScopesSupported: []string{"openid", "profile"}, BaselineClientScopes: []string{}},
		},
		{
			name:   "single baseline entry in supported set passes",
			config: RunConfig{ScopesSupported: []string{"openid", "profile", "email"}, BaselineClientScopes: []string{"openid"}},
		},
		{
			name:   "all baseline entries in supported set passes",
			config: RunConfig{ScopesSupported: []string{"openid", "profile", "email", "offline_access"}, BaselineClientScopes: []string{"openid", "offline_access"}},
		},
		{
			name:    "baseline contains scope not in supported rejects with specific error",
			config:  RunConfig{ScopesSupported: []string{"openid"}, BaselineClientScopes: []string{"openid", "offline_access"}},
			wantErr: true,
			errMsg:  `"offline_access" which is not in scopes_supported`,
		},
		{
			name:   "nil supported with baseline in DefaultScopes passes",
			config: RunConfig{ScopesSupported: nil, BaselineClientScopes: []string{"offline_access"}},
		},
		{
			name:    "nil supported with baseline outside DefaultScopes rejects",
			config:  RunConfig{ScopesSupported: nil, BaselineClientScopes: []string{"custom_scope"}},
			wantErr: true,
			errMsg:  `"custom_scope"`,
		},
		{
			name:    "first missing scope is reported when multiple are missing",
			config:  RunConfig{ScopesSupported: []string{"openid"}, BaselineClientScopes: []string{"foo", "bar"}},
			wantErr: true,
			errMsg:  "foo",
		},
		// CIMD RunConfig validation
		{name: "CIMD nil passes", config: RunConfig{CIMD: nil}},
		{name: "CIMD disabled passes even with invalid fields", config: RunConfig{CIMD: &CIMDRunConfig{Enabled: false, CacheMaxSize: -1, CacheFallbackTTL: "-1s"}}},
		{name: "CIMD enabled negative cache_max_size rejected", config: RunConfig{CIMD: &CIMDRunConfig{Enabled: true, CacheMaxSize: -1}}, wantErr: true, errMsg: "cache_max_size"},
		{name: "CIMD enabled invalid TTL string rejected", config: RunConfig{CIMD: &CIMDRunConfig{Enabled: true, CacheFallbackTTL: "not-a-duration"}}, wantErr: true, errMsg: "cache_fallback_ttl"},
		{name: "CIMD enabled negative TTL rejected", config: RunConfig{CIMD: &CIMDRunConfig{Enabled: true, CacheFallbackTTL: "-5m"}}, wantErr: true, errMsg: "cache_fallback_ttl"},
		{name: "CIMD enabled valid passes", config: RunConfig{CIMD: &CIMDRunConfig{Enabled: true, CacheMaxSize: 64, CacheFallbackTTL: "5m"}}},
		{name: "CIMD enabled omitted optional fields pass", config: RunConfig{CIMD: &CIMDRunConfig{Enabled: true}}},
		// Confidential-client transport gate
		{name: "confidential clients without insecure HTTP passes", config: RunConfig{AllowConfidentialClientRegistration: true}},
		{name: "private-key JWT registration defaults to false", config: RunConfig{}},
		{name: "private-key JWT registration without insecure HTTP passes", config: RunConfig{AllowPrivateKeyJWTRegistration: true}},
		{name: "insecure HTTP without confidential clients passes", config: RunConfig{InsecureAllowHTTP: true}},
		{
			name:    "confidential clients with malformed issuer reject before startup",
			config:  RunConfig{Issuer: "http://[::1", AllowConfidentialClientRegistration: true},
			wantErr: true,
			errMsg:  "confidential clients require a valid issuer URL",
		},
		{
			name: "loopback confidential opt-in rejects structurally invalid issuer before startup",
			config: RunConfig{
				Issuer:                              "http://user@localhost",
				AllowConfidentialClientRegistration: true,
				InsecureAllowConfidentialOverLoopbackHTTP: true,
			},
			wantErr: true,
			errMsg:  "confidential clients require a valid issuer URL",
		},
		{
			name:   "private-key JWT registration combined with insecure HTTP passes (no secret to protect)",
			config: RunConfig{AllowPrivateKeyJWTRegistration: true, InsecureAllowHTTP: true},
		},
		{
			name: "confidential clients with plain-HTTP loopback issuer rejects without the opt-in",
			config: RunConfig{
				Issuer:                              "http://localhost:8080",
				AllowConfidentialClientRegistration: true,
			},
			wantErr: true,
			errMsg:  "insecure_allow_confidential_over_loopback_http",
		},
		{
			name: "confidential clients with plain-HTTP loopback issuer passes with the opt-in",
			config: RunConfig{
				Issuer:                              "http://localhost:8080",
				AllowConfidentialClientRegistration: true,
				InsecureAllowConfidentialOverLoopbackHTTP: true,
			},
		},
		{
			name: "confidential clients with https loopback issuer is unaffected",
			config: RunConfig{
				Issuer:                              "https://localhost:8080",
				AllowConfidentialClientRegistration: true,
			},
		},
		{
			name: "confidential clients disabled with plain-HTTP loopback issuer is unaffected",
			config: RunConfig{
				Issuer: "http://localhost:8080",
			},
		},
		{
			name: "private-key JWT registration with plain-HTTP loopback issuer is unaffected (no secret to protect)",
			config: RunConfig{
				Issuer:                         "http://localhost:8080",
				AllowPrivateKeyJWTRegistration: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.config.Validate()
			assertError(t, err, tt.wantErr, tt.errMsg)
		})
	}
}

func TestDelegateClientRunConfigValidate(t *testing.T) {
	t.Parallel()

	validClient := DelegateClientRunConfig{
		ClientID:           "delegate",
		ClientSecretEnvVar: "DELEGATE_CLIENT_SECRET",
		Scopes:             []string{"openid"},
		Audiences:          []string{"https://mcp.example.com"},
	}
	tests := []struct {
		name    string
		clients []DelegateClientRunConfig
		wantErr string
	}{
		{name: "valid", clients: []DelegateClientRunConfig{validClient}},
		{name: "empty ID", clients: []DelegateClientRunConfig{{ClientSecretEnvVar: "SECRET", Scopes: validClient.Scopes, Audiences: validClient.Audiences}}, wantErr: "client_id is required"},
		{name: "duplicate ID", clients: []DelegateClientRunConfig{validClient, validClient}, wantErr: "duplicate client_id"},
		{name: "missing secret reference", clients: []DelegateClientRunConfig{{ClientID: "delegate", Scopes: validClient.Scopes, Audiences: validClient.Audiences}}, wantErr: "client_secret_file or client_secret_env_var is required"},
		{name: "missing scopes", clients: []DelegateClientRunConfig{{ClientID: "delegate", ClientSecretEnvVar: "SECRET", Audiences: validClient.Audiences}}, wantErr: "scopes is required"},
		{name: "scope outside supported", clients: []DelegateClientRunConfig{{ClientID: "delegate", ClientSecretEnvVar: "SECRET", Scopes: []string{"admin"}, Audiences: validClient.Audiences}}, wantErr: `"admin" which is not in scopes_supported`},
		{name: "missing audiences", clients: []DelegateClientRunConfig{{ClientID: "delegate", ClientSecretEnvVar: "SECRET", Scopes: validClient.Scopes}}, wantErr: "audiences is required"},
		{name: "audience outside allowed", clients: []DelegateClientRunConfig{{ClientID: "delegate", ClientSecretEnvVar: "SECRET", Scopes: validClient.Scopes, Audiences: []string{"https://other.example.com"}}}, wantErr: "is not in allowed_audiences"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := RunConfig{
				Issuer:           "https://auth.example.com",
				ScopesSupported:  []string{"openid"},
				AllowedAudiences: []string{"https://mcp.example.com"},
				DelegateClients:  tt.clients,
			}
			assertError(t, cfg.Validate(), tt.wantErr != "", tt.wantErr)
		})
	}
}

func TestRunConfigValidateAllowedAudiences(t *testing.T) {
	t.Parallel()

	cfg := RunConfig{
		Issuer:           "https://auth.example.com",
		AllowedAudiences: []string{"ftp://mcp.example.com"},
	}

	require.ErrorContains(t, cfg.Validate(), "allowed_audiences contains invalid audience")
}

func TestConfigValidateDelegateClients(t *testing.T) {
	t.Parallel()

	base := func() Config {
		return Config{
			Issuer:      "https://auth.example.com",
			KeyProvider: keys.NewGeneratingProvider(keys.DefaultAlgorithm),
			HMACSecrets: &servercrypto.HMACSecrets{Current: make([]byte, 32)},
			Upstreams: []UpstreamConfig{{
				Type: UpstreamProviderTypeOAuth2,
				OAuth2Config: &upstream.OAuth2Config{
					CommonOAuthConfig:     upstream.CommonOAuthConfig{ClientID: "upstream", RedirectURI: "https://auth.example.com/callback"},
					AuthorizationEndpoint: "https://idp.example.com/authorize",
					TokenEndpoint:         "https://idp.example.com/token",
				},
			}},
			ScopesSupported:  []string{"openid"},
			AllowedAudiences: []string{"https://mcp.example.com"},
		}
	}
	validClient := DelegateClient{
		ClientID:     "delegate",
		ClientSecret: strings.Repeat("a", minDelegateClientSecretLength),
		Scopes:       []string{"openid"},
		Audiences:    []string{"https://mcp.example.com"},
	}
	tests := []struct {
		name    string
		clients []DelegateClient
		issuer  string
		wantErr string
	}{
		{name: "valid resolved client", clients: []DelegateClient{validClient}},
		{name: "missing resolved secret", clients: []DelegateClient{{ClientID: "delegate", Scopes: validClient.Scopes, Audiences: validClient.Audiences}}, wantErr: "resolved client secret is required"},
		{name: "resolved secret too short", clients: []DelegateClient{{ClientID: "delegate", ClientSecret: "short-secret", Scopes: validClient.Scopes, Audiences: validClient.Audiences}}, wantErr: "resolved client secret must be at least"},
		{name: "static client rejects insecure HTTP", clients: []DelegateClient{validClient}, issuer: "http://auth.example.com", wantErr: "confidential clients would send secrets over cleartext HTTP"},
		{name: "static client rejects loopback HTTP without opt-in", clients: []DelegateClient{validClient}, issuer: "http://localhost:8080", wantErr: "insecure_allow_confidential_over_loopback_http"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := base()
			cfg.DelegateClients = tt.clients
			if tt.issuer != "" {
				cfg.Issuer = tt.issuer
				cfg.InsecureAllowHTTP = tt.issuer == "http://auth.example.com"
			}
			err := cfg.Validate()
			assertError(t, err, tt.wantErr != "", tt.wantErr)
			if err != nil && tt.name == "resolved secret too short" {
				assert.NotContains(t, err.Error(), "short-secret")
			}
		})
	}
}

// TestValidateConfidentialClientTransport pins the shared predicate that both
// RunConfig.Validate and Config.Validate call, and that the operator's
// validateEmbeddedAuthServer reuses: confidential clients are rejected when
// combined with insecureAllowHTTP (unconditionally), or with a plain-HTTP
// loopback issuer unless insecureAllowConfidentialOverLoopbackHTTP opts in.
// private_key_jwt registration has no equivalent transport gate: it never
// returns a secret in the DCR response, so it is unaffected by any of this
// (see TestConfigValidate's "private-key JWT registration ... passes" cases).
func TestValidateConfidentialClientTransport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		allowConfidential     bool
		insecureAllowHTTP     bool
		issuer                string
		allowLoopbackOverride bool
		wantErr               bool
		errContains           string
		redacted              []string
	}{
		{name: "both false passes"},
		{name: "confidential only, https issuer passes", allowConfidential: true, issuer: "https://auth.example.com"},
		{name: "insecure HTTP only passes", insecureAllowHTTP: true},
		{
			name: "insecure HTTP combined with confidential rejects", allowConfidential: true, insecureAllowHTTP: true,
			wantErr: true, errContains: "insecure_allow_http",
		},
		{
			name:              "confidential with malformed issuer rejects without exposing credentials",
			allowConfidential: true, issuer: "http://user:supersecret@[::1",
			wantErr: true, errContains: "confidential clients require a valid issuer URL",
			redacted: []string{"user", "supersecret", "http://user:supersecret@[::1"},
		},
		{
			name:              "loopback confidential opt-in rejects structurally invalid issuer",
			allowConfidential: true, issuer: "http://user@localhost", allowLoopbackOverride: true,
			wantErr: true, errContains: "confidential clients require a valid issuer URL",
		},
		{
			name:              "confidential with plain-HTTP loopback issuer rejects without the opt-in",
			allowConfidential: true, issuer: "http://localhost:8080",
			wantErr: true, errContains: "insecure_allow_confidential_over_loopback_http",
		},
		{
			name:              "confidential with plain-HTTP loopback issuer passes with the opt-in",
			allowConfidential: true, issuer: "http://localhost:8080", allowLoopbackOverride: true,
		},
		{
			name:              "confidential with https loopback issuer passes without the opt-in",
			allowConfidential: true, issuer: "https://localhost:8080",
		},
		{
			name:   "confidential disabled with plain-HTTP loopback issuer passes",
			issuer: "http://localhost:8080",
		},
		{
			name:              "confidential with plain-HTTP non-loopback issuer rejects without opt-in",
			allowConfidential: true, issuer: "http://auth.example.com",
			wantErr: true, errContains: "plain-HTTP non-loopback",
		},
		{
			name:              "confidential with plain-HTTP non-loopback issuer rejects with loopback opt-in",
			allowConfidential: true, issuer: "http://auth.example.com", allowLoopbackOverride: true,
			wantErr: true, errContains: "non-loopback issuer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateConfidentialClientTransport(tt.allowConfidential, tt.insecureAllowHTTP, tt.issuer, tt.allowLoopbackOverride)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "confidential clients")
				assert.Contains(t, err.Error(), tt.errContains)
				for _, value := range tt.redacted {
					assert.NotContains(t, err.Error(), value)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestValidateForceConfidentialRedirectURIs pins the validation rules for the
// force-confidential-redirect-uris override: it requires
// allow_confidential_client_registration, and every entry must be an https
// non-loopback redirect URI.
func TestValidateForceConfidentialRedirectURIs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		uris              []string
		allowConfidential bool
		wantErr           bool
		errMsg            string
	}{
		{name: "empty list passes regardless of allowConfidential"},
		{
			name:    "empty list passes even with allowConfidential false",
			uris:    nil,
			wantErr: false,
		},
		{
			name:              "valid https non-loopback entry passes",
			uris:              []string{"https://client.example.com/callback"},
			allowConfidential: true,
		},
		{
			name:              "multiple valid entries pass",
			uris:              []string{"https://a.example.com/cb", "https://b.example.com/cb"},
			allowConfidential: true,
		},
		{
			name:    "non-empty list without allowConfidential rejects",
			uris:    []string{"https://client.example.com/callback"},
			wantErr: true,
			errMsg:  "requires allow_confidential_client_registration",
		},
		{
			name:              "loopback IP entry rejects",
			uris:              []string{"https://127.0.0.1/callback"},
			allowConfidential: true,
			wantErr:           true,
			errMsg:            "must not be a loopback redirect URI",
		},
		{
			name:              "loopback localhost entry rejects",
			uris:              []string{"https://localhost/callback"},
			allowConfidential: true,
			wantErr:           true,
			errMsg:            "must not be a loopback redirect URI",
		},
		{
			name:              "http loopback entry rejects (must be https)",
			uris:              []string{"http://localhost/callback"},
			allowConfidential: true,
			wantErr:           true,
			errMsg:            "must not be a loopback redirect URI",
		},
		{
			name:              "http non-loopback entry rejects",
			uris:              []string{"http://client.example.com/callback"},
			allowConfidential: true,
			wantErr:           true,
			errMsg:            "must use http (for loopback) or https scheme",
		},
		{
			name:              "one bad entry among good ones rejects",
			uris:              []string{"https://good.example.com/cb", "https://localhost/callback"},
			allowConfidential: true,
			wantErr:           true,
			errMsg:            "must not be a loopback redirect URI",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateForceConfidentialRedirectURIs(tt.uris, tt.allowConfidential)
			assertError(t, err, tt.wantErr, tt.errMsg)
		})
	}
}

func TestDCRUpstreamConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  DCRUpstreamConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "neither discovery_url nor registration_endpoint rejects",
			config:  DCRUpstreamConfig{},
			wantErr: true,
			errMsg:  "either discovery_url or registration_endpoint is required",
		},
		{
			name: "both discovery_url and registration_endpoint rejects",
			config: DCRUpstreamConfig{
				DiscoveryURL:         "https://idp.example.com/.well-known/oauth-authorization-server",
				RegistrationEndpoint: "https://idp.example.com/register",
			},
			wantErr: true,
			errMsg:  "discovery_url and registration_endpoint are mutually exclusive",
		},
		{
			name: "only discovery_url is valid",
			config: DCRUpstreamConfig{
				DiscoveryURL: "https://idp.example.com/.well-known/oauth-authorization-server",
			},
		},
		{
			name: "only registration_endpoint is valid",
			config: DCRUpstreamConfig{
				RegistrationEndpoint: "https://idp.example.com/register",
			},
		},
		{
			name: "software metadata and a single token source pass validation",
			config: DCRUpstreamConfig{
				RegistrationEndpoint:   "https://idp.example.com/register",
				InitialAccessTokenFile: "/var/run/secrets/dcr-token",
				SoftwareID:             "toolhive",
				SoftwareStatement:      "eyJhbGciOi...",
			},
		},
		{
			name: "both initial_access_token_file and initial_access_token_env_var rejects",
			config: DCRUpstreamConfig{
				RegistrationEndpoint:     "https://idp.example.com/register",
				InitialAccessTokenFile:   "/var/run/secrets/dcr-token",
				InitialAccessTokenEnvVar: "DCR_TOKEN",
			},
			wantErr: true,
			errMsg:  "initial_access_token_file and initial_access_token_env_var are mutually exclusive",
		},
		{
			name:    "malformed discovery_url rejects",
			config:  DCRUpstreamConfig{DiscoveryURL: "://broken"},
			wantErr: true,
			errMsg:  "invalid discovery_url",
		},
		{
			name:    "non-loopback http discovery_url rejects",
			config:  DCRUpstreamConfig{DiscoveryURL: "http://idp.example.com/.well-known/oauth-authorization-server"},
			wantErr: true,
			errMsg:  "invalid discovery_url",
		},
		{
			name:    "non-loopback http registration_endpoint rejects",
			config:  DCRUpstreamConfig{RegistrationEndpoint: "http://idp.example.com/register"},
			wantErr: true,
			errMsg:  "invalid registration_endpoint",
		},
		{
			name: "loopback http discovery_url is valid",
			config: DCRUpstreamConfig{
				DiscoveryURL: "http://localhost:8080/.well-known/oauth-authorization-server",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.config.Validate()
			assertError(t, err, tt.wantErr, tt.errMsg)
		})
	}
}

func TestConfigApplyDefaults_BaselineClientScopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		scopesSupported      []string
		baselineClientScopes []string
		wantErr              bool
		errMsg               string
		wantDefaultScopes    bool
	}{
		{
			name:              "empty scopes_supported and empty baseline — defaults substituted",
			wantDefaultScopes: true,
		},
		{
			name:            "scopes_supported set and empty baseline — no substitution",
			scopesSupported: []string{"openid", "profile"},
		},
		{
			name:                 "scopes_supported set and baseline non-empty — no substitution no error",
			scopesSupported:      []string{"openid", "profile"},
			baselineClientScopes: []string{"openid"},
		},
		{
			name:                 "empty scopes_supported with non-empty baseline applies DefaultScopes",
			baselineClientScopes: []string{"openid"},
			wantDefaultScopes:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &Config{
				ScopesSupported:      tt.scopesSupported,
				BaselineClientScopes: tt.baselineClientScopes,
			}

			err := cfg.applyDefaults()

			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errMsg)
				return
			}

			require.NoError(t, err)
			if tt.wantDefaultScopes {
				require.Equal(t, registration.DefaultScopes, cfg.ScopesSupported)
			} else {
				require.Equal(t, tt.scopesSupported, cfg.ScopesSupported)
			}
		})
	}
}

func TestConfigApplyDefaults_CIMD(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		cfg             Config
		wantMaxSize     int
		wantFallbackTTL time.Duration
	}{
		{
			name:            "CIMD enabled with zero fields applies defaults",
			cfg:             Config{Issuer: "https://example.com", CIMDEnabled: true},
			wantMaxSize:     256,
			wantFallbackTTL: 5 * time.Minute,
		},
		{
			name: "CIMD enabled preserves non-zero values",
			cfg: Config{
				Issuer:               "https://example.com",
				CIMDEnabled:          true,
				CIMDCacheMaxSize:     128,
				CIMDCacheFallbackTTL: 10 * time.Minute,
			},
			wantMaxSize:     128,
			wantFallbackTTL: 10 * time.Minute,
		},
		{
			name:            "CIMD disabled leaves zero fields unchanged",
			cfg:             Config{Issuer: "https://example.com", CIMDEnabled: false},
			wantMaxSize:     0,
			wantFallbackTTL: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := tt.cfg
			err := cfg.applyDefaults()
			require.NoError(t, err)
			require.Equal(t, tt.wantMaxSize, cfg.CIMDCacheMaxSize)
			require.Equal(t, tt.wantFallbackTTL, cfg.CIMDCacheFallbackTTL)
		})
	}
}

// TestConfigValidate_DelegationTokenLifespan covers the RFC 8693 delegation
// token lifespan bounds added to Config.Validate: zero is accepted (it is
// defaulted later by applyDefaults), values in (0, 24h] are accepted, and
// negative or over-24h values are rejected.
func TestConfigValidate_DelegationTokenLifespan(t *testing.T) {
	t.Parallel()

	// base returns a minimally-valid Config so each case isolates the
	// DelegationTokenLifespan check from unrelated validation failures.
	base := func() Config {
		return Config{
			Issuer:      "https://example.com",
			KeyProvider: keys.NewGeneratingProvider(keys.DefaultAlgorithm),
			HMACSecrets: &servercrypto.HMACSecrets{Current: make([]byte, 32)},
			Upstreams: []UpstreamConfig{{
				Name: "default",
				Type: UpstreamProviderTypeOAuth2,
				OAuth2Config: &upstream.OAuth2Config{
					CommonOAuthConfig:     upstream.CommonOAuthConfig{ClientID: "c", RedirectURI: "https://example.com/cb"},
					AuthorizationEndpoint: "https://idp.example.com/authorize",
					TokenEndpoint:         "https://idp.example.com/token",
				},
			}},
			AllowedAudiences: []string{"https://mcp.example.com"},
		}
	}

	tests := []struct {
		name     string
		lifespan time.Duration
		wantErr  bool
		errMsg   string
	}{
		{name: "zero accepted (defaulted later)", lifespan: 0},
		{name: "valid 15m", lifespan: 15 * time.Minute},
		{name: "valid 1h", lifespan: time.Hour},
		{name: "valid 24h boundary", lifespan: 24 * time.Hour},
		{name: "negative rejected", lifespan: -time.Second, wantErr: true, errMsg: "delegation token lifespan must not be negative"},
		{name: "over 24h rejected", lifespan: 24*time.Hour + time.Second, wantErr: true, errMsg: "delegation token lifespan must not exceed 24h"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := base()
			cfg.DelegationTokenLifespan = tt.lifespan
			assertError(t, cfg.Validate(), tt.wantErr, tt.errMsg)
		})
	}
}

// TestConfigApplyDefaults_DelegationTokenLifespan verifies that applyDefaults
// fills a zero DelegationTokenLifespan with the 15-minute default and preserves
// a caller-supplied value.
func TestConfigApplyDefaults_DelegationTokenLifespan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input time.Duration
		want  time.Duration
	}{
		{name: "zero gets 15m default", input: 0, want: 15 * time.Minute},
		{name: "custom value preserved", input: 5 * time.Minute, want: 5 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := Config{Issuer: "https://example.com", DelegationTokenLifespan: tt.input}
			require.NoError(t, cfg.applyDefaults())
			require.Equal(t, tt.want, cfg.DelegationTokenLifespan)
		})
	}
}

// TestConfigValidate_TrustedIssuers covers validateTrustedIssuers as reached
// from Config.Validate: the URL-shape checks (validateTrustedIssuerURL on
// issuer_url, validateJWKSEndpointURL on jwks_url) and the structural checks
// delegated to tokenexchange.ValidateTrustedIssuers.
func TestConfigValidate_TrustedIssuers(t *testing.T) {
	t.Parallel()

	// base returns a minimally-valid Config (Issuer "https://example.com",
	// AllowedAudiences ["https://mcp.example.com"]) so each case isolates the
	// TrustedIssuers check from unrelated validation failures.
	base := func() Config {
		return Config{
			Issuer:      "https://example.com",
			KeyProvider: keys.NewGeneratingProvider(keys.DefaultAlgorithm),
			HMACSecrets: &servercrypto.HMACSecrets{Current: make([]byte, 32)},
			Upstreams: []UpstreamConfig{{
				Name: "default",
				Type: UpstreamProviderTypeOAuth2,
				OAuth2Config: &upstream.OAuth2Config{
					CommonOAuthConfig:     upstream.CommonOAuthConfig{ClientID: "c", RedirectURI: "https://example.com/cb"},
					AuthorizationEndpoint: "https://idp.example.com/authorize",
					TokenEndpoint:         "https://idp.example.com/token",
				},
			}},
			AllowedAudiences: []string{"https://mcp.example.com"},
		}
	}

	tests := []struct {
		name    string
		issuers []tokenexchange.TrustedIssuer
		wantErr bool
		errMsg  string
	}{
		{
			name:    "no trusted issuers is byte-identical to before",
			issuers: nil,
		},
		{
			name: "issuer_url bad scheme rejected",
			issuers: []tokenexchange.TrustedIssuer{
				{IssuerURL: "htps://idp.example.com", ExpectedAudience: "https://mcp.example.com", AllowedDelegateClients: []string{"*"}},
			},
			wantErr: true,
			errMsg:  "issuer_url",
		},
		{
			name: "issuer_url empty rejected",
			issuers: []tokenexchange.TrustedIssuer{
				{IssuerURL: "", ExpectedAudience: "https://mcp.example.com", AllowedDelegateClients: []string{"*"}},
			},
			wantErr: true,
			errMsg:  "issuer is required",
		},
		{
			name: "issuer_url http without per-issuer insecure_allow_http rejected",
			issuers: []tokenexchange.TrustedIssuer{
				{IssuerURL: "http://idp.example.com", ExpectedAudience: "https://mcp.example.com", AllowedDelegateClients: []string{"*"}},
			},
			wantErr: true,
			errMsg:  "http scheme is only allowed for localhost",
		},
		{
			name: "issuer_url http with per-issuer insecure_allow_http accepted",
			issuers: []tokenexchange.TrustedIssuer{
				{IssuerURL: "http://idp.example.com", ExpectedAudience: "https://mcp.example.com", InsecureAllowHTTP: true, AllowedDelegateClients: []string{"*"}},
			},
		},
		{
			// Unlike Config.Issuer, a trusted issuer gets no localhost
			// exemption: it isn't this server's own issuer, so the same
			// same-host development convenience doesn't apply — see
			// validateTrustedIssuerURL's doc comment. Without
			// insecure_allow_http, http://localhost must be rejected here
			// the same as any other http issuer_url.
			name: "issuer_url http localhost rejected without per-issuer insecure_allow_http",
			issuers: []tokenexchange.TrustedIssuer{
				{IssuerURL: "http://localhost:8080", ExpectedAudience: "https://mcp.example.com", AllowedDelegateClients: []string{"*"}},
			},
			wantErr: true,
			errMsg:  "http scheme is only allowed for localhost",
		},
		{
			name: "issuer_url http localhost accepted with per-issuer insecure_allow_http",
			issuers: []tokenexchange.TrustedIssuer{
				{IssuerURL: "http://localhost:8080", ExpectedAudience: "https://mcp.example.com", InsecureAllowHTTP: true, AllowedDelegateClients: []string{"*"}},
			},
		},
		{
			// Azure AD B2C's real jwks_uri carries a query string
			// (?p=B2C_1_...); jwks_url must be validated as an ordinary
			// endpoint URL, not an OIDC issuer identifier, or a legitimate
			// production IdP would be rejected.
			name: "jwks_url with query string accepted",
			issuers: []tokenexchange.TrustedIssuer{
				{
					IssuerURL:              "https://idp.example.com",
					ExpectedAudience:       "https://mcp.example.com",
					JWKSURL:                "https://idp.example.com/keys?p=B2C_1_signin",
					AllowedDelegateClients: []string{"*"},
				},
			},
		},
		{
			name: "jwks_url with trailing slash accepted",
			issuers: []tokenexchange.TrustedIssuer{
				{
					IssuerURL:              "https://idp.example.com",
					ExpectedAudience:       "https://mcp.example.com",
					JWKSURL:                "https://idp.example.com/keys/",
					AllowedDelegateClients: []string{"*"},
				},
			},
		},
		{
			name: "jwks_url bad scheme rejected",
			issuers: []tokenexchange.TrustedIssuer{
				{
					IssuerURL:              "https://idp.example.com",
					ExpectedAudience:       "https://mcp.example.com",
					JWKSURL:                "ftp://idp.example.com/keys",
					AllowedDelegateClients: []string{"*"},
				},
			},
			wantErr: true,
			errMsg:  "jwks_url",
		},
		{
			name: "jwks_url http rejected without per-issuer insecure_allow_http",
			issuers: []tokenexchange.TrustedIssuer{
				{
					IssuerURL:              "https://idp.example.com",
					ExpectedAudience:       "https://mcp.example.com",
					JWKSURL:                "http://idp.example.com/keys",
					AllowedDelegateClients: []string{"*"},
				},
			},
			wantErr: true,
			errMsg:  "jwks_url",
		},
		{
			name: "jwks_url http accepted with per-issuer insecure_allow_http",
			issuers: []tokenexchange.TrustedIssuer{
				{
					IssuerURL:              "https://idp.example.com",
					ExpectedAudience:       "https://mcp.example.com",
					JWKSURL:                "http://idp.example.com/keys",
					InsecureAllowHTTP:      true,
					AllowedDelegateClients: []string{"*"},
				},
			},
		},
		{
			name: "jwks_url private IP literal rejected without allow_private_ips",
			issuers: []tokenexchange.TrustedIssuer{
				{
					IssuerURL:              "https://idp.example.com",
					ExpectedAudience:       "https://mcp.example.com",
					JWKSURL:                "https://10.0.0.5/keys",
					AllowedDelegateClients: []string{"*"},
				},
			},
			wantErr: true,
			errMsg:  "private or loopback",
		},
		{
			name: "jwks_url private IP literal accepted with allow_private_ips",
			issuers: []tokenexchange.TrustedIssuer{
				{
					IssuerURL:              "https://idp.example.com",
					ExpectedAudience:       "https://mcp.example.com",
					JWKSURL:                "https://10.0.0.5/keys",
					AllowPrivateIPs:        true,
					AllowedDelegateClients: []string{"*"},
				},
			},
		},
		{
			// Mirrors the CRD's Kubebuilder CEL rule requiring jwksUrl
			// whenever allowPrivateIPs is set (mcpexternalauthconfig_types.go):
			// without a hand-configured jwks_url, OIDC discovery — a document
			// fetched from, and thus influenceable by, the external issuer
			// itself — would choose the private JWKS dial target. A
			// hand-written RunConfig must not be able to bypass what the CRD
			// path already guarantees.
			name: "allow_private_ips without jwks_url rejected",
			issuers: []tokenexchange.TrustedIssuer{
				{
					IssuerURL:              "https://idp.example.com",
					ExpectedAudience:       "https://mcp.example.com",
					AllowPrivateIPs:        true,
					AllowedDelegateClients: []string{"*"},
				},
			},
			wantErr: true,
			errMsg:  "allow_private_ips requires jwks_url",
		},
		{
			name: "missing expected_audience rejected",
			issuers: []tokenexchange.TrustedIssuer{
				{IssuerURL: "https://idp.example.com", AllowedDelegateClients: []string{"*"}},
			},
			wantErr: true,
			errMsg:  "expected_audience is required",
		},
		{
			name: "issuer_url equal to Config.Issuer rejected",
			issuers: []tokenexchange.TrustedIssuer{
				{IssuerURL: "https://example.com", ExpectedAudience: "https://mcp.example.com", AllowedDelegateClients: []string{"*"}},
			},
			wantErr: true,
			errMsg:  "must not equal the authorization server's own issuer",
		},
		{
			name: "duplicate issuer_url rejected",
			issuers: []tokenexchange.TrustedIssuer{
				{IssuerURL: "https://idp.example.com", ExpectedAudience: "https://mcp.example.com", AllowedDelegateClients: []string{"*"}},
				{IssuerURL: "https://idp.example.com", ExpectedAudience: "https://mcp.example.com", AllowedDelegateClients: []string{"*"}},
			},
			wantErr: true,
			errMsg:  "configured more than once",
		},
		{
			name: "actor_claim sub rejected",
			issuers: []tokenexchange.TrustedIssuer{
				{IssuerURL: "https://idp.example.com", ExpectedAudience: "https://mcp.example.com", ActorClaim: "sub", AllowedDelegateClients: []string{"*"}},
			},
			wantErr: true,
			errMsg:  "actor_claim",
		},
		{
			name: "actor_matcher malformed rejected",
			issuers: []tokenexchange.TrustedIssuer{
				{IssuerURL: "https://idp.example.com", ExpectedAudience: "https://mcp.example.com", ActorMatcher: "claims.", AllowedDelegateClients: []string{"*"}},
			},
			wantErr: true,
			errMsg:  "actor_matcher",
		},
		{
			// Unlike Config.Issuer, a trusted issuer_url permits a trailing
			// slash: Microsoft Entra ID v1 (the default for a newly
			// registered API) issues "iss": "https://sts.windows.net/{tenant}/"
			// with one, and OIDC Discovery has no rule against it — see
			// validateTrustedIssuerURL's doc comment.
			name: "issuer_url with trailing slash accepted",
			issuers: []tokenexchange.TrustedIssuer{
				{
					IssuerURL:              "https://sts.windows.net/11111111-2222-3333-4444-555555555555/",
					ExpectedAudience:       "https://mcp.example.com",
					AllowedDelegateClients: []string{"*"},
				},
			},
		},
		{
			// A may_act-only issuer is legitimate: an empty AllowedActors
			// means every non-may_act token from it is rejected at
			// validation time, not that the config itself is invalid.
			name: "empty allowed_actors accepted",
			issuers: []tokenexchange.TrustedIssuer{
				{IssuerURL: "https://idp.example.com", ExpectedAudience: "https://mcp.example.com", AllowedActors: nil, AllowedDelegateClients: []string{"*"}},
			},
		},
		{
			// #5989 hardening reaches Config.Validate through the same
			// shared validateTrustedIssuers -> tokenexchange.ValidateTrustedIssuers
			// path as the constructor-level check.
			name: "absent allowed_delegate_clients rejected",
			issuers: []tokenexchange.TrustedIssuer{
				{IssuerURL: "https://idp.example.com", ExpectedAudience: "https://mcp.example.com"},
			},
			wantErr: true,
			errMsg:  "allowed_delegate_clients is required",
		},
		{
			name: "allow_may_act with wildcard allowed_delegate_clients rejected",
			issuers: []tokenexchange.TrustedIssuer{
				{IssuerURL: "https://idp.example.com", ExpectedAudience: "https://mcp.example.com", AllowedDelegateClients: []string{"*"}, AllowMayAct: true},
			},
			wantErr: true,
			errMsg:  "allow_may_act",
		},
		{
			// This is a server runtime configuration invariant, not CRD
			// admission: the configured allowed audience is supplied by the
			// individual MCP server using the shared auth configuration.
			name: "JWT-bearer accepted audience overlapping allowed resource rejected",
			issuers: []tokenexchange.TrustedIssuer{{
				IssuerURL: "https://idp.example.com",
				JWTBearerGrant: &tokenexchange.JWTBearerGrantPolicy{
					MaxAssertionAge: "1m",
					SubjectBindings: []tokenexchange.JWTBearerSubjectBinding{{
						Subject:          "workload",
						AllowedResources: []string{"https://mcp.example.com"},
					}},
					AcceptedAudiences: []string{"https://mcp.example.com"},
				},
			}},
			wantErr: true,
			errMsg:  "must not also be a configured resource audience",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := base()
			cfg.TrustedIssuers = tt.issuers
			assertError(t, cfg.Validate(), tt.wantErr, tt.errMsg)
		})
	}
}

// TestRunConfigValidate_TrustedIssuers asserts that RunConfig.Validate
// itself catches every TrustedIssuers failure mode reachable through
// validateTrustedIssuers — the four structural checks, the issuer_url shape
// check, and (mirroring TestConfigValidate_TrustedIssuers) the jwks_url
// private-IP guard — not only Config.Validate, because it's the same
// shared function both call. This matters because buildUpstreamConfigs
// performs live RFC 7591 registration against upstream IdPs before
// Config.Validate is ever reached (see the comment on RunConfig.Validate).
// A bad trusted issuer caught only at the Config layer would orphan an
// upstream client registration on every restart of the resulting crash loop.
func TestRunConfigValidate_TrustedIssuers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		issuers []tokenexchange.TrustedIssuer
		wantErr bool
		errMsg  string
	}{
		{
			name:    "no trusted issuers passes",
			issuers: nil,
		},
		{
			name: "malformed issuer_url rejected",
			issuers: []tokenexchange.TrustedIssuer{
				{IssuerURL: "htps://idp.example.com", ExpectedAudience: "https://mcp.example.com", AllowedDelegateClients: []string{"*"}},
			},
			wantErr: true,
			errMsg:  "issuer_url",
		},
		{
			name: "missing expected_audience rejected",
			issuers: []tokenexchange.TrustedIssuer{
				{IssuerURL: "https://idp.example.com", AllowedDelegateClients: []string{"*"}},
			},
			wantErr: true,
			errMsg:  "expected_audience is required",
		},
		{
			name: "issuer_url equal to RunConfig.Issuer rejected",
			issuers: []tokenexchange.TrustedIssuer{
				{IssuerURL: "https://example.com", ExpectedAudience: "https://mcp.example.com", AllowedDelegateClients: []string{"*"}},
			},
			wantErr: true,
			errMsg:  "must not equal the authorization server's own issuer",
		},
		{
			name: "duplicate issuer_url rejected",
			issuers: []tokenexchange.TrustedIssuer{
				{IssuerURL: "https://idp.example.com", ExpectedAudience: "https://mcp.example.com", AllowedDelegateClients: []string{"*"}},
				{IssuerURL: "https://idp.example.com", ExpectedAudience: "https://mcp.example.com", AllowedDelegateClients: []string{"*"}},
			},
			wantErr: true,
			errMsg:  "configured more than once",
		},
		{
			name: "actor_claim sub rejected",
			issuers: []tokenexchange.TrustedIssuer{
				{IssuerURL: "https://idp.example.com", ExpectedAudience: "https://mcp.example.com", ActorClaim: "sub", AllowedDelegateClients: []string{"*"}},
			},
			wantErr: true,
			errMsg:  "actor_claim",
		},
		{
			name: "actor_matcher malformed rejected",
			issuers: []tokenexchange.TrustedIssuer{
				{IssuerURL: "https://idp.example.com", ExpectedAudience: "https://mcp.example.com", ActorMatcher: "claims.", AllowedDelegateClients: []string{"*"}},
			},
			wantErr: true,
			errMsg:  "actor_matcher",
		},
		{
			name: "jwks_url private IP literal rejected without allow_private_ips",
			issuers: []tokenexchange.TrustedIssuer{
				{
					IssuerURL:              "https://idp.example.com",
					ExpectedAudience:       "https://mcp.example.com",
					JWKSURL:                "https://10.0.0.5/keys",
					AllowedDelegateClients: []string{"*"},
				},
			},
			wantErr: true,
			errMsg:  "private or loopback",
		},
		{
			name: "allow_may_act with wildcard allowed_delegate_clients rejected",
			issuers: []tokenexchange.TrustedIssuer{
				{IssuerURL: "https://idp.example.com", ExpectedAudience: "https://mcp.example.com", AllowedDelegateClients: []string{"*"}, AllowMayAct: true},
			},
			wantErr: true,
			errMsg:  "allow_may_act",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := RunConfig{Issuer: "https://example.com", TrustedIssuers: tt.issuers}
			assertError(t, cfg.Validate(), tt.wantErr, tt.errMsg)
		})
	}
}

// TestConfig_WarnTrustedIssuerAudiences pins the invalid_target footgun
// warning: it must fire at startup for an ExpectedAudience absent from
// AllowedAudiences, and stay silent when the audience is present.
//
// See TestNewEmbeddedAuthServer_TrustedIssuers in
// runner/embeddedauthserver_test.go for the same pattern with the rationale
// spelled out.
//
//nolint:paralleltest // captures the package-global slog.Default()
func TestConfig_WarnTrustedIssuerAudiences(t *testing.T) {
	tests := []struct {
		name      string
		audiences []string
		wantWarn  bool
	}{
		{
			name:      "expected_audience absent from allowed_audiences warns",
			audiences: []string{"https://other.example.com"},
			wantWarn:  true,
		},
		{
			name:      "expected_audience present in allowed_audiences is silent",
			audiences: []string{"https://mcp.example.com"},
			wantWarn:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
			t.Cleanup(func() { slog.SetDefault(prev) })

			cfg := Config{
				AllowedAudiences: tt.audiences,
				TrustedIssuers: []tokenexchange.TrustedIssuer{
					{IssuerURL: "https://idp.example.com", ExpectedAudience: "https://mcp.example.com", AllowedDelegateClients: []string{"*"}},
				},
			}
			cfg.warnTrustedIssuerAudiences()

			if tt.wantWarn {
				require.Contains(t, buf.String(), "trusted issuer's expected_audience is not in allowed_audiences")
			} else {
				require.Empty(t, buf.String())
			}
		})
	}
}
