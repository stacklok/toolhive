// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/clientcredentials"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func TestBuildProviderRegistersSPIFFEClientCredentialsOnlyWhenPermitted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		grants      []string
		wantHandler bool
	}{
		{
			name: "no SPIFFE trust",
		},
		{
			name:   "token exchange only association",
			grants: []string{oauthproto.GrantTypeTokenExchange},
		},
		{
			name:        "client credentials association",
			grants:      []string{oauthproto.GrantTypeClientCredentials},
			wantHandler: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := storage.NewMemoryStorage()
			t.Cleanup(func() { _ = store.Close() })
			key, err := rsa.GenerateKey(rand.Reader, 2048)
			require.NoError(t, err)
			cfg := Config{
				Issuer:                  "https://auth.example.com",
				KeyProvider:             &testKeyProvider{key: key},
				HMACSecrets:             servercrypto.NewHMACSecrets([]byte("test-secret-with-32-bytes-long!!")),
				AccessTokenLifespan:     time.Hour,
				RefreshTokenLifespan:    24 * time.Hour,
				AuthCodeLifespan:        time.Minute,
				DelegationTokenLifespan: 15 * time.Minute,
				AllowedAudiences:        []string{"https://resource.example.com"},
			}
			if tt.grants != nil {
				cfg.SPIFFETrust = providerTrust(tt.grants)
			}
			provider, err := BuildProviderForTest(context.Background(), cfg, store)
			require.NoError(t, err)
			// Read the handlers from the constructed provider, not the config
			// passed in: NewAuthorizationServer works on its own independent
			// copy of *fosite.Config (so concurrent constructions from a shared
			// AuthorizationServerConfig cannot alias each other's handler
			// slices), so factories registered during construction are visible
			// only through the provider's own Config, not the caller's.
			f, ok := provider.(*fosite.Fosite)
			require.True(t, ok)
			handlers := f.Config.GetTokenEndpointHandlers(context.Background())
			assert.Equal(t, tt.wantHandler, containsSPIFFEClientCredentialsHandler(handlers))
		})
	}
}

// providerTrust builds the trust config directly: only the association's
// grant list matters to provider construction, and the run-config validation that
// would otherwise admit the association is not under test here.
func providerTrust(grants []string) *SPIFFETrustConfig {
	return &SPIFFETrustConfig{associations: []SPIFFEClientAuthConfig{{
		clientID:      "client",
		authorization: SPIFFEAuthorizationPolicy{grantTypes: grants, scopes: []string{"openid"}},
	}}}
}

func containsSPIFFEClientCredentialsHandler(handlers fosite.TokenEndpointHandlers) bool {
	for _, handler := range handlers {
		if _, ok := handler.(*clientcredentials.Handler); ok {
			return true
		}
	}
	return false
}
