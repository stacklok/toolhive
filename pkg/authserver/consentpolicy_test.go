// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"testing"

	"github.com/stretchr/testify/require"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

func validConsentPolicyConfig() Config {
	return Config{
		Issuer:      "https://auth.example.com",
		HMACSecrets: servercrypto.NewHMACSecrets([]byte("0123456789abcdef0123456789abcdef")),
		Upstreams: []UpstreamConfig{{
			Name: "default",
			Type: UpstreamProviderTypeOAuth2,
			OAuth2Config: &upstream.OAuth2Config{
				CommonOAuthConfig:     upstream.CommonOAuthConfig{ClientID: "upstream-client", RedirectURI: "https://auth.example.com/callback"},
				AuthorizationEndpoint: "https://idp.example.com/authorize",
				TokenEndpoint:         "https://idp.example.com/token",
			},
		}},
		AllowedAudiences: []string{"https://mcp.example.com"},
	}
}

func TestConfigValidateConsentExemptClientIDs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(*Config)
		wantErr string
	}{
		{name: "empty list preserves existing behavior"},
		{name: "valid opaque ID", setup: func(cfg *Config) { cfg.ConsentExemptClientIDs = []string{"desktop-client"} }},
		{name: "empty ID rejected", setup: func(cfg *Config) { cfg.ConsentExemptClientIDs = []string{""} }, wantErr: "empty ID"},
		{name: "duplicate ID rejected", setup: func(cfg *Config) { cfg.ConsentExemptClientIDs = []string{"desktop-client", "desktop-client"} }, wantErr: "duplicate ID"},
		{name: "absolute URI rejected", setup: func(cfg *Config) { cfg.ConsentExemptClientIDs = []string{"custom:desktop-client"} }, wantErr: "URL-shaped"},
		{name: "network path URL rejected", setup: func(cfg *Config) { cfg.ConsentExemptClientIDs = []string{"//client.example.com/id"} }, wantErr: "URL-shaped"},
		{name: "upstream required", setup: func(cfg *Config) { cfg.ConsentExemptClientIDs = []string{"desktop-client"}; cfg.Upstreams = nil }, wantErr: "at least one configured upstream"},
		{name: "insecure HTTP issuer does not relax HTTPS requirement", setup: func(cfg *Config) {
			cfg.ConsentExemptClientIDs = []string{"desktop-client"}
			cfg.Issuer = "http://auth.example.com"
			cfg.InsecureAllowHTTP = true
		}, wantErr: "HTTPS"},
		{name: "HTTP override rejected despite HTTPS issuer", setup: func(cfg *Config) {
			cfg.ConsentExemptClientIDs = []string{"desktop-client"}
			cfg.AuthorizationEndpointBaseURL = "http://login.example.com"
			cfg.InsecureAllowHTTP = true
		}, wantErr: "HTTPS"},
		{name: "HTTPS override is effective over HTTP issuer", setup: func(cfg *Config) {
			cfg.ConsentExemptClientIDs = []string{"desktop-client"}
			cfg.Issuer = "http://auth.example.com"
			cfg.InsecureAllowHTTP = true
			cfg.AuthorizationEndpointBaseURL = "https://login.example.com"
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConsentPolicyConfig()
			if tt.setup != nil {
				tt.setup(&cfg)
			}
			err := cfg.Validate()
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}
