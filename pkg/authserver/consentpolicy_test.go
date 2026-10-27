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

	t.Run("empty list preserves existing behavior", func(t *testing.T) {
		t.Parallel()
		cfg := validConsentPolicyConfig()
		require.NoError(t, cfg.Validate())
	})
	t.Run("shared validator rejects URL-shaped IDs", func(t *testing.T) {
		t.Parallel()
		cfg := validConsentPolicyConfig()
		cfg.ConsentExemptClientIDs = []string{"https://client.example.com"}
		require.ErrorContains(t, cfg.Validate(), "URL-shaped")
	})
}
