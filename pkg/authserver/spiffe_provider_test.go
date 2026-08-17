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

	oauthserver "github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/clientcredentials"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

func TestBuildProviderRegistersSPIFFEClientCredentialsOnlyWhenPermitted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                           string
		supportsClientCredentialsGrant bool
		wantHandler                    bool
	}{
		{name: "client credentials capability disabled"},
		{name: "client credentials capability enabled", supportsClientCredentialsGrant: true, wantHandler: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := storage.NewMemoryStorage()
			t.Cleanup(func() { _ = store.Close() })
			authServerConfig := providerAuthorizationServerConfig(t)
			authServerConfig.SupportsSPIFFEClientCredentialsGrant = tt.supportsClientCredentialsGrant
			provider, _, err := buildProvider(
				Config{DelegationTokenLifespan: 15 * time.Minute},
				authServerConfig,
				store,
			)
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

func providerAuthorizationServerConfig(t *testing.T) *oauthserver.AuthorizationServerConfig {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	config, err := oauthserver.NewAuthorizationServerConfig(&oauthserver.AuthorizationServerParams{
		Issuer:               "https://auth.example.com",
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: 24 * time.Hour,
		AuthCodeLifespan:     time.Minute,
		HMACSecrets:          servercrypto.NewHMACSecrets([]byte("test-secret-with-32-bytes-long!!")),
		SigningKeyID:         "test-key",
		SigningKeyAlgorithm:  "RS256",
		SigningKey:           key,
		AllowedAudiences:     []string{"https://resource.example.com"},
	})
	require.NoError(t, err)
	return config
}

func containsSPIFFEClientCredentialsHandler(handlers fosite.TokenEndpointHandlers) bool {
	for _, handler := range handlers {
		if _, ok := handler.(*clientcredentials.Handler); ok {
			return true
		}
	}
	return false
}
