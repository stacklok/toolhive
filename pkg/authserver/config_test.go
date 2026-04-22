// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.config.Validate()
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
