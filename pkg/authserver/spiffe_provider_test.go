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
		name        string
		registry    *SPIFFEAssociationRegistry
		wantHandler bool
	}{
		{name: "no SPIFFE registry", registry: nil},
		{name: "token exchange only association", registry: providerRegistry(t, []string{SPIFFEGrantTypeTokenExchange})},
		{name: "client credentials association", registry: providerRegistry(t, []string{SPIFFEGrantTypeClientCredentials}), wantHandler: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := storage.NewMemoryStorage()
			t.Cleanup(func() { _ = store.Close() })
			authServerConfig := providerAuthorizationServerConfig(t)
			_, err := buildProvider(
				Config{DelegationTokenLifespan: 15 * time.Minute},
				authServerConfig,
				store,
				tt.registry,
			)
			require.NoError(t, err)
			// Read the handlers from the config, not the provider: fosite
			// registers them on *fosite.Config (which AuthorizationServerConfig
			// embeds) and reads them back as f.Config.GetTokenEndpointHandlers.
			// *fosite.Fosite does not itself implement that interface.
			handlers := authServerConfig.GetTokenEndpointHandlers(context.Background())
			assert.Equal(t, tt.wantHandler, containsSPIFFEClientCredentialsHandler(handlers))
		})
	}
}

func providerRegistry(t *testing.T, grants []string) *SPIFFEAssociationRegistry {
	t.Helper()

	association := testSPIFFEAssociation("client", "openid")
	association.GrantTypes = grants
	if len(grants) == 1 && grants[0] == SPIFFEGrantTypeClientCredentials {
		association.TokenExchange = nil
	}
	return newTestSPIFFEAssociationRegistry(t, []SPIFFEClientAuthRunConfig{association})
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
