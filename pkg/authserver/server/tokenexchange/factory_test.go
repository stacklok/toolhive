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
		name                      string
		delegationLifespan        time.Duration
		configuredDelegateClients []string
		wantErr                   string
	}{
		{
			name:               "zero delegationLifespan returns error",
			delegationLifespan: 0,
			wantErr:            "delegationLifespan must be between",
		},
		{
			name:               "negative delegationLifespan returns error",
			delegationLifespan: -time.Minute,
			wantErr:            "delegationLifespan must be between",
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
			wantErr:            "delegationLifespan must be between",
		},
		{
			name:               "delegationLifespan of 48h returns error",
			delegationLifespan: 48 * time.Hour,
			wantErr:            "delegationLifespan must be between",
		},
		{
			name:                      "empty-string configured delegate client returns error",
			delegationLifespan:        15 * time.Minute,
			configuredDelegateClients: []string{"agent-1", ""},
			wantErr:                   "configuredDelegateClients must not contain an empty client ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, err := Factory(tt.delegationLifespan, nil, tt.configuredDelegateClients)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
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

// TestFactory_ValidatorSelection asserts how a SubjectTokenValidator reaches
// the Handler: the bare Factory uses the self-issued validator when there are
// no trusted issuers, and fails closed when trusted issuers are configured
// (it cannot own a MultiIssuerTokenValidator's JWKS workers for shutdown);
// trusted-issuer setups must supply a shared, closeable validator via
// FactoryWithSharedTrustedIssuerValidator, which is then the exact instance the
// Handler uses.
func TestFactory_ValidatorSelection(t *testing.T) {
	t.Parallel()

	validIssuer := TrustedIssuer{
		IssuerURL:              "https://idp.example.com",
		ExpectedAudience:       "https://mcp.example.com",
		AllowedDelegateClients: []string{anyDelegateClient},
	}

	t.Run("no trusted issuers builds self-issued validator", func(t *testing.T) {
		t.Parallel()

		f, err := Factory(15*time.Minute, nil, nil)
		require.NoError(t, err)

		cfg := buildTestAuthServerConfig(t)
		result, err := f(cfg, &fakeFactoryStorage{mockAccessTokenStorage: &mockAccessTokenStorage{}}, &mockAccessTokenStrategy{})
		require.NoError(t, err)

		handler, ok := result.(*Handler)
		require.True(t, ok, "Factory closure must return *Handler, got %T", result)
		assert.IsType(t, &SelfIssuedTokenValidator{}, handler.validator)
	})

	t.Run("bare Factory with trusted issuers fails closed", func(t *testing.T) {
		t.Parallel()

		// The bare Factory has no way to release a MultiIssuerTokenValidator's
		// JWKS workers, so it must reject trusted issuers outright rather than
		// build a leaked validator inside the compose-time closure.
		_, err := Factory(15*time.Minute, []TrustedIssuer{validIssuer}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "require a shared validator")
	})

	t.Run("shared validator is the instance the handler uses", func(t *testing.T) {
		t.Parallel()

		cfg := buildTestAuthServerConfig(t)
		shared, err := NewSharedTrustedIssuerValidator(cfg, []TrustedIssuer{validIssuer})
		require.NoError(t, err)
		t.Cleanup(func() { _ = shared.Close() })

		f, err := FactoryWithSharedTrustedIssuerValidator(15*time.Minute, []TrustedIssuer{validIssuer}, nil, shared)
		require.NoError(t, err)

		result, err := f(cfg, &fakeFactoryStorage{mockAccessTokenStorage: &mockAccessTokenStorage{}}, &mockAccessTokenStrategy{})
		require.NoError(t, err)

		handler, ok := result.(*Handler)
		require.True(t, ok, "closure must return *Handler, got %T", result)
		assert.Same(t, shared, handler.validator, "the caller-owned shared validator must be used verbatim")
	})
}
