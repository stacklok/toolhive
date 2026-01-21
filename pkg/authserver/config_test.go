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

func TestClientConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		client  ClientConfig
		wantErr bool
		errMsg  string
	}{
		// Config-level validation
		{name: "missing client ID", client: ClientConfig{RedirectURIs: []string{"http://localhost/cb"}}, wantErr: true, errMsg: "client id is required"},
		{name: "missing redirect URIs", client: ClientConfig{ID: "c"}, wantErr: true, errMsg: "at least one redirect_uri is required"},
		{name: "empty redirect URIs", client: ClientConfig{ID: "c", RedirectURIs: []string{}}, wantErr: true, errMsg: "at least one redirect_uri is required"},
		{name: "confidential without secret", client: ClientConfig{ID: "c", RedirectURIs: []string{"http://localhost/cb"}, Public: false}, wantErr: true, errMsg: "secret is required"},

		// Valid clients
		{name: "valid confidential", client: ClientConfig{ID: "c", Secret: "s", RedirectURIs: []string{"http://localhost/cb"}}},
		{name: "valid public", client: ClientConfig{ID: "c", RedirectURIs: []string{"http://localhost/cb"}, Public: true}},
		{name: "valid custom scheme", client: ClientConfig{ID: "c", RedirectURIs: []string{"cursor://cb"}, Public: true}},

		// Redirect URI validation (one case to verify delegation to oauth package)
		{name: "invalid redirect URI", client: ClientConfig{ID: "c", RedirectURIs: []string{"http://evil.com/cb"}, Public: true}, wantErr: true, errMsg: "redirect_uri[0]:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.client.Validate()
			assertError(t, err, tt.wantErr, tt.errMsg)
		})
	}
}

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

	tests := []struct {
		name    string
		config  Config
		wantErr bool
		errMsg  string
	}{
		{name: "missing issuer", config: Config{KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstream: validUpstream}, wantErr: true, errMsg: "issuer is required"},
		{name: "nil HMAC secrets", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, Upstream: validUpstream}, wantErr: true, errMsg: "HMAC secrets are required"},
		{name: "HMAC too short", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: shortHMAC, Upstream: validUpstream}, wantErr: true, errMsg: "HMAC secret must be at least 32 bytes"},
		{name: "nil upstream", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC}, wantErr: true, errMsg: "upstream config is required"},
		{name: "invalid client", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstream: validUpstream, Clients: []ClientConfig{{}}}, wantErr: true, errMsg: "client 0:"},

		// Valid configs
		{name: "valid minimal", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstream: validUpstream}},
		{name: "valid with client", config: Config{Issuer: "https://example.com", KeyProvider: validKeyProvider, HMACSecrets: validHMAC, Upstream: validUpstream, Clients: []ClientConfig{{ID: "c", Secret: "s", RedirectURIs: []string{"http://localhost/cb"}}}}},
		{name: "valid nil key provider", config: Config{Issuer: "https://example.com", HMACSecrets: validHMAC, Upstream: validUpstream}},
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
