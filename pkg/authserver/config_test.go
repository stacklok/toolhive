// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"bytes"
	"strings"
	"testing"
	"time"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

func TestValidateIssuerURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		issuer  string
		wantErr bool
		errMsg  string
	}{
		// Valid
		{name: "https", issuer: "https://example.com"},
		{name: "https with port", issuer: "https://example.com:8443"},
		{name: "https with path", issuer: "https://example.com/auth"},
		{name: "http localhost", issuer: "http://localhost"},
		{name: "http localhost with port", issuer: "http://localhost:8080"},
		{name: "http 127.0.0.1", issuer: "http://127.0.0.1:8080"},
		{name: "http IPv6 loopback", issuer: "http://[::1]:8080"},

		// Invalid
		{name: "empty", issuer: "", wantErr: true, errMsg: "issuer is required"},
		{name: "missing scheme", issuer: "example.com", wantErr: true, errMsg: "scheme is required"},
		{name: "missing host", issuer: "https://", wantErr: true, errMsg: "host is required"},
		{name: "query component", issuer: "https://example.com?foo=bar", wantErr: true, errMsg: "must not contain query"},
		{name: "fragment component", issuer: "https://example.com#section", wantErr: true, errMsg: "must not contain fragment"},
		{name: "http non-localhost", issuer: "http://example.com", wantErr: true, errMsg: "http scheme is only allowed for localhost"},
		{name: "ftp scheme", issuer: "ftp://example.com", wantErr: true, errMsg: "scheme must be https"},
		{name: "trailing slash", issuer: "https://example.com/", wantErr: true, errMsg: "must not have trailing slash"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateIssuerURL(tt.issuer)
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
