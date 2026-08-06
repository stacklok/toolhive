// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
)

func TestFactory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		delegationLifespan time.Duration
		wantErr            bool
	}{
		{
			name:               "zero delegationLifespan returns error",
			delegationLifespan: 0,
			wantErr:            true,
		},
		{
			name:               "negative delegationLifespan returns error",
			delegationLifespan: -time.Minute,
			wantErr:            true,
		},
		{
			name:               "positive delegationLifespan succeeds",
			delegationLifespan: 15 * time.Minute,
		},
		{
			name:               "delegationLifespan at max access token lifespan succeeds",
			delegationLifespan: server.MaxAccessTokenLifespan,
		},
		{
			name:               "delegationLifespan above max access token lifespan returns error",
			delegationLifespan: server.MaxAccessTokenLifespan + time.Hour,
			wantErr:            true,
		},
		{
			name:               "delegationLifespan of 48h returns error",
			delegationLifespan: 48 * time.Hour,
			wantErr:            true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, err := Factory(tt.delegationLifespan, nil)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "delegationLifespan must be between")
				assert.Nil(t, f)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, f)
		})
	}
}

// buildTestAuthServerConfig returns a minimally-valid
// *server.AuthorizationServerConfig for exercising the closure Factory
// returns. AllowedAudiences is non-empty so it doubles as a valid
// ExpectedAudience target for a TrustedIssuer in the tests below.
func buildTestAuthServerConfig(t *testing.T) *server.AuthorizationServerConfig {
	t.Helper()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	cfg, err := server.NewAuthorizationServerConfig(&server.AuthorizationServerParams{
		Issuer:               "https://auth.example.com",
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: time.Hour * 24,
		AuthCodeLifespan:     time.Minute * 10,
		HMACSecrets:          servercrypto.NewHMACSecrets([]byte("test-secret-with-32-bytes-long!!")),
		SigningKeyID:         "key-1",
		SigningKeyAlgorithm:  "RS256",
		SigningKey:           rsaKey,
		AllowedAudiences:     []string{"https://mcp.example.com"},
	})
	require.NoError(t, err)
	return cfg
}

// fakeClientManager is a no-op fosite.ClientManager (the entirety of
// fosite.Storage) so fakeFactoryStorage satisfies the Factory closure's
// storage fosite.Storage parameter without needing a real client store —
// the closure only type-asserts storage to oauth2.AccessTokenStorage, it
// never calls a ClientManager method.
type fakeClientManager struct{}

func (fakeClientManager) GetClient(context.Context, string) (fosite.Client, error) {
	return nil, fosite.ErrNotFound
}
func (fakeClientManager) ClientAssertionJWTValid(context.Context, string) error { return nil }
func (fakeClientManager) SetClientAssertionJWT(context.Context, string, time.Time) error {
	return nil
}

// fakeFactoryStorage combines the no-op ClientManager with the package's
// existing mockAccessTokenStorage so it satisfies both fosite.Storage (the
// closure's declared parameter type) and oauth2.AccessTokenStorage (what the
// closure actually type-asserts against).
type fakeFactoryStorage struct {
	fakeClientManager
	*mockAccessTokenStorage
}

// TestFactory_ValidatorSelection asserts which SubjectTokenValidator the
// closure returned by Factory builds into the Handler: the self-issued
// validator when trustedIssuers is empty, the multi-issuer validator when
// it isn't, and a hard error — not a silent downgrade to the self-issued
// validator — when a configured TrustedIssuer is itself invalid.
func TestFactory_ValidatorSelection(t *testing.T) {
	t.Parallel()

	validIssuer := TrustedIssuer{
		IssuerURL:        "https://idp.example.com",
		ExpectedAudience: "https://mcp.example.com",
	}
	invalidIssuer := TrustedIssuer{
		IssuerURL: "https://idp.example.com",
		// ExpectedAudience deliberately empty: invalid per validateTrustedIssuer.
	}

	tests := []struct {
		name           string
		trustedIssuers []TrustedIssuer
		wantErr        string
		wantValidator  any // nil when wantErr is set
	}{
		{
			name:           "no trusted issuers builds self-issued validator",
			trustedIssuers: nil,
			wantValidator:  &SelfIssuedTokenValidator{},
		},
		{
			name:           "valid trusted issuer builds multi-issuer validator",
			trustedIssuers: []TrustedIssuer{validIssuer},
			wantValidator:  &MultiIssuerTokenValidator{},
		},
		{
			name:           "invalid trusted issuer fails closed, not silently downgraded",
			trustedIssuers: []TrustedIssuer{invalidIssuer},
			wantErr:        "trusted_issuers",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, err := Factory(15*time.Minute, tt.trustedIssuers)
			require.NoError(t, err)

			cfg := buildTestAuthServerConfig(t)
			storage := &fakeFactoryStorage{mockAccessTokenStorage: &mockAccessTokenStorage{}}
			strategy := &mockAccessTokenStrategy{}

			result, err := f(cfg, storage, strategy)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Nil(t, result)
				return
			}
			require.NoError(t, err)

			handler, ok := result.(*Handler)
			require.True(t, ok, "Factory closure must return *Handler, got %T", result)
			assert.IsType(t, tt.wantValidator, handler.validator)
		})
	}
}
