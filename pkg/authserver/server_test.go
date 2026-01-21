// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"crypto/rand"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	storagemocks "github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	upstreammocks "github.com/stacklok/toolhive/pkg/authserver/upstream/mocks"
)

// validUpstreamConfig returns a valid upstream config for tests.
func validUpstreamConfig() *upstream.OAuth2Config {
	return &upstream.OAuth2Config{
		CommonOAuthConfig: upstream.CommonOAuthConfig{
			ClientID:    "test-client",
			RedirectURI: "https://example.com/callback",
		},
		AuthorizationEndpoint: "https://idp.example.com/auth",
		TokenEndpoint:         "https://idp.example.com/token",
	}
}

// validHMACSecret returns a valid HMAC secret for tests.
func validHMACSecret() []byte {
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	return secret
}

func TestNew(t *testing.T) {
	t.Parallel()

	validKeyProvider := keys.NewGeneratingProvider(keys.DefaultAlgorithm)
	validHMAC := &servercrypto.HMACSecrets{Current: validHMACSecret()}
	validUpstreams := []UpstreamConfig{{Name: "default", Config: validUpstreamConfig()}}

	tests := []struct {
		name        string
		cfg         Config
		storageNil  bool
		wantErr     bool
		errContains string
	}{
		{
			name:        "nil storage returns error",
			cfg:         Config{},
			storageNil:  true,
			wantErr:     true,
			errContains: "invalid config",
		},
		{
			name:        "empty issuer returns error",
			cfg:         Config{},
			storageNil:  false,
			wantErr:     true,
			errContains: "issuer is required",
		},
		// Note: "missing HMAC secrets" no longer returns an error because
		// applyDefaults() auto-generates them when nil
		{
			name: "HMAC secret too short returns error",
			cfg: Config{
				Issuer:           "https://example.com",
				KeyProvider:      validKeyProvider,
				HMACSecrets:      &servercrypto.HMACSecrets{Current: []byte("short")},
				Upstreams:        validUpstreams,
				AllowedAudiences: []string{"https://mcp.example.com"},
			},
			storageNil:  false,
			wantErr:     true,
			errContains: "HMAC secret must be at least 32 bytes",
		},
		{
			name: "missing upstreams returns error",
			cfg: Config{
				Issuer:           "https://example.com",
				KeyProvider:      validKeyProvider,
				HMACSecrets:      validHMAC,
				AllowedAudiences: []string{"https://mcp.example.com"},
			},
			storageNil:  false,
			wantErr:     true,
			errContains: "at least one upstream is required",
		},
		{
			name: "missing allowed audiences returns error",
			cfg: Config{
				Issuer:      "https://example.com",
				KeyProvider: validKeyProvider,
				HMACSecrets: validHMAC,
				Upstreams:   validUpstreams,
			},
			storageNil:  false,
			wantErr:     true,
			errContains: "at least one allowed audience is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			var stor *storagemocks.MockStorage
			if !tt.storageNil {
				stor = storagemocks.NewMockStorage(ctrl)
			}

			ctx := context.Background()
			_, err := New(ctx, tt.cfg, stor)

			if tt.wantErr {
				if err == nil {
					t.Errorf("New() error = nil, wantErr %v", tt.wantErr)
					return
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("New() error = %q, want error containing %q", err.Error(), tt.errContains)
				}
			} else {
				if err != nil {
					t.Errorf("New() unexpected error = %v", err)
				}
			}
		})
	}
}

// TestNewServer_Success tests the success path with mocked dependencies.
func TestNewServer_Success(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mocks
	mockStorage := storagemocks.NewMockStorage(ctrl)
	mockUpstream := upstreammocks.NewMockOAuth2Provider(ctrl)

	// Create valid config
	cfg := Config{
		Issuer:           "https://example.com",
		KeyProvider:      keys.NewGeneratingProvider(keys.DefaultAlgorithm),
		HMACSecrets:      &servercrypto.HMACSecrets{Current: validHMACSecret()},
		Upstreams:        []UpstreamConfig{{Name: "default", Config: validUpstreamConfig()}},
		AllowedAudiences: []string{"https://mcp.example.com"},
	}

	// Create factory that returns our mock
	mockFactory := func(_ *upstream.OAuth2Config) (upstream.OAuth2Provider, error) {
		return mockUpstream, nil
	}

	// Call newServer with the mock factory
	ctx := context.Background()
	srv, err := newServer(ctx, cfg, mockStorage, withUpstreamFactory(mockFactory))

	if err != nil {
		t.Fatalf("newServer() unexpected error: %v", err)
	}
	if srv == nil {
		t.Fatal("newServer() returned nil server")
	}
	if srv.Handler() == nil {
		t.Error("server.Handler() returned nil")
	}
	if srv.IDPTokenStorage() != mockStorage {
		t.Error("server.IDPTokenStorage() did not return expected storage")
	}
}
