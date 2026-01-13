// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
)

func TestNewAuthorizationServerConfig(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	params := &AuthorizationServerParams{
		Issuer:               "https://auth.example.com",
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: time.Hour * 24,
		AuthCodeLifespan:     time.Minute * 10,
		HMACSecrets:          servercrypto.NewHMACSecrets([]byte("test-secret-with-32-bytes-long!!")),
		SigningKeyID:         "key-1",
		SigningKeyAlgorithm:  "RS256",
		SigningKey:           rsaKey,
	}

	authzServerConfig, err := NewAuthorizationServerConfig(params)
	require.NoError(t, err)
	require.NotNil(t, authzServerConfig)

	// Verify fosite config is set correctly
	assert.Equal(t, params.Issuer, authzServerConfig.AccessTokenIssuer)
	assert.Equal(t, params.AccessTokenLifespan, authzServerConfig.AccessTokenLifespan)
	assert.Equal(t, params.RefreshTokenLifespan, authzServerConfig.RefreshTokenLifespan)
	assert.Equal(t, params.AuthCodeLifespan, authzServerConfig.AuthorizeCodeLifespan)

	// Verify signing key is set
	require.NotNil(t, authzServerConfig.SigningKey)
	assert.Equal(t, "key-1", authzServerConfig.SigningKey.KeyID)
	assert.Equal(t, "RS256", authzServerConfig.SigningKey.Algorithm)

	// Verify JWKS contains the key
	require.NotNil(t, authzServerConfig.SigningJWKS)
	assert.Len(t, authzServerConfig.SigningJWKS.Keys, 1)
}

func TestNewAuthorizationServerConfig_InvalidConfig(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	tests := []struct {
		name    string
		params  *AuthorizationServerParams
		wantErr string
	}{
		{
			name:    "nil config",
			params:  nil,
			wantErr: "config is required",
		},
		{
			name: "missing issuer",
			params: &AuthorizationServerParams{
				Issuer:               "",
				AccessTokenLifespan:  time.Hour,
				RefreshTokenLifespan: time.Hour * 24,
				AuthCodeLifespan:     time.Minute * 10,
				HMACSecrets:          servercrypto.NewHMACSecrets([]byte("test-secret-with-32-bytes-long!!")),
				SigningKeyID:         "key-1",
				SigningKeyAlgorithm:  "RS256",
				SigningKey:           rsaKey,
			},
			wantErr: "issuer is required",
		},
		{
			name: "issuer with invalid scheme",
			params: &AuthorizationServerParams{
				Issuer:               "ftp://auth.example.com",
				AccessTokenLifespan:  time.Hour,
				RefreshTokenLifespan: time.Hour * 24,
				AuthCodeLifespan:     time.Minute * 10,
				HMACSecrets:          servercrypto.NewHMACSecrets([]byte("test-secret-with-32-bytes-long!!")),
				SigningKeyID:         "key-1",
				SigningKeyAlgorithm:  "RS256",
				SigningKey:           rsaKey,
			},
			wantErr: "issuer must use http or https scheme",
		},
		{
			name: "issuer without host",
			params: &AuthorizationServerParams{
				Issuer:               "https://",
				AccessTokenLifespan:  time.Hour,
				RefreshTokenLifespan: time.Hour * 24,
				AuthCodeLifespan:     time.Minute * 10,
				HMACSecrets:          servercrypto.NewHMACSecrets([]byte("test-secret-with-32-bytes-long!!")),
				SigningKeyID:         "key-1",
				SigningKeyAlgorithm:  "RS256",
				SigningKey:           rsaKey,
			},
			wantErr: "issuer must have a host",
		},
		{
			name: "issuer with trailing slash",
			params: &AuthorizationServerParams{
				Issuer:               "https://auth.example.com/",
				AccessTokenLifespan:  time.Hour,
				RefreshTokenLifespan: time.Hour * 24,
				AuthCodeLifespan:     time.Minute * 10,
				HMACSecrets:          servercrypto.NewHMACSecrets([]byte("test-secret-with-32-bytes-long!!")),
				SigningKeyID:         "key-1",
				SigningKeyAlgorithm:  "RS256",
				SigningKey:           rsaKey,
			},
			wantErr: "issuer must not have a trailing slash",
		},
		{
			name: "missing key ID",
			params: &AuthorizationServerParams{
				Issuer:               "https://auth.example.com",
				AccessTokenLifespan:  time.Hour,
				RefreshTokenLifespan: time.Hour * 24,
				AuthCodeLifespan:     time.Minute * 10,
				HMACSecrets:          servercrypto.NewHMACSecrets([]byte("test-secret-with-32-bytes-long!!")),
				SigningKeyID:         "",
				SigningKeyAlgorithm:  "RS256",
				SigningKey:           rsaKey,
			},
			wantErr: "signing key ID is required",
		},
		{
			name: "missing algorithm",
			params: &AuthorizationServerParams{
				Issuer:               "https://auth.example.com",
				AccessTokenLifespan:  time.Hour,
				RefreshTokenLifespan: time.Hour * 24,
				AuthCodeLifespan:     time.Minute * 10,
				HMACSecrets:          servercrypto.NewHMACSecrets([]byte("test-secret-with-32-bytes-long!!")),
				SigningKeyID:         "key-1",
				SigningKeyAlgorithm:  "",
				SigningKey:           rsaKey,
			},
			wantErr: "signing key algorithm is required",
		},
		{
			name: "missing signing key",
			params: &AuthorizationServerParams{
				Issuer:               "https://auth.example.com",
				AccessTokenLifespan:  time.Hour,
				RefreshTokenLifespan: time.Hour * 24,
				AuthCodeLifespan:     time.Minute * 10,
				HMACSecrets:          servercrypto.NewHMACSecrets([]byte("test-secret-with-32-bytes-long!!")),
				SigningKeyID:         "key-1",
				SigningKeyAlgorithm:  "RS256",
				SigningKey:           nil,
			},
			wantErr: "signing key is required",
		},
		{
			name: "HMAC secret too short",
			params: &AuthorizationServerParams{
				Issuer:               "https://auth.example.com",
				AccessTokenLifespan:  time.Hour,
				RefreshTokenLifespan: time.Hour * 24,
				AuthCodeLifespan:     time.Minute * 10,
				HMACSecrets:          servercrypto.NewHMACSecrets([]byte("too-short")),
				SigningKeyID:         "key-1",
				SigningKeyAlgorithm:  "RS256",
				SigningKey:           rsaKey,
			},
			wantErr: "current HMAC secret must be at least 32 bytes",
		},
		{
			name: "nil HMAC secrets",
			params: &AuthorizationServerParams{
				Issuer:               "https://auth.example.com",
				AccessTokenLifespan:  time.Hour,
				RefreshTokenLifespan: time.Hour * 24,
				AuthCodeLifespan:     time.Minute * 10,
				HMACSecrets:          nil,
				SigningKeyID:         "key-1",
				SigningKeyAlgorithm:  "RS256",
				SigningKey:           rsaKey,
			},
			wantErr: "HMAC secrets are required",
		},
		{
			name: "empty current HMAC secret",
			params: &AuthorizationServerParams{
				Issuer:               "https://auth.example.com",
				AccessTokenLifespan:  time.Hour,
				RefreshTokenLifespan: time.Hour * 24,
				AuthCodeLifespan:     time.Minute * 10,
				HMACSecrets:          &servercrypto.HMACSecrets{Current: nil},
				SigningKeyID:         "key-1",
				SigningKeyAlgorithm:  "RS256",
				SigningKey:           rsaKey,
			},
			wantErr: "current HMAC secret must be at least 32 bytes",
		},
		{
			name: "algorithm incompatible with key type",
			params: &AuthorizationServerParams{
				Issuer:               "https://auth.example.com",
				AccessTokenLifespan:  time.Hour,
				RefreshTokenLifespan: time.Hour * 24,
				AuthCodeLifespan:     time.Minute * 10,
				HMACSecrets:          servercrypto.NewHMACSecrets([]byte("test-secret-with-32-bytes-long!!")),
				SigningKeyID:         "key-1",
				SigningKeyAlgorithm:  "ES256", // EC algorithm with RSA key
				SigningKey:           rsaKey,
			},
			wantErr: "invalid signing configuration",
		},
		{
			name: "access token lifespan too short",
			params: &AuthorizationServerParams{
				Issuer:               "https://auth.example.com",
				AccessTokenLifespan:  time.Second, // Below minimum of 1 minute
				RefreshTokenLifespan: time.Hour * 24,
				AuthCodeLifespan:     time.Minute * 10,
				HMACSecrets:          servercrypto.NewHMACSecrets([]byte("test-secret-with-32-bytes-long!!")),
				SigningKeyID:         "key-1",
				SigningKeyAlgorithm:  "RS256",
				SigningKey:           rsaKey,
			},
			wantErr: "access token lifespan must be between",
		},
		{
			name: "access token lifespan too long",
			params: &AuthorizationServerParams{
				Issuer:               "https://auth.example.com",
				AccessTokenLifespan:  time.Hour * 48, // Above maximum of 24 hours
				RefreshTokenLifespan: time.Hour * 24,
				AuthCodeLifespan:     time.Minute * 10,
				HMACSecrets:          servercrypto.NewHMACSecrets([]byte("test-secret-with-32-bytes-long!!")),
				SigningKeyID:         "key-1",
				SigningKeyAlgorithm:  "RS256",
				SigningKey:           rsaKey,
			},
			wantErr: "access token lifespan must be between",
		},
		{
			name: "refresh token lifespan too short",
			params: &AuthorizationServerParams{
				Issuer:               "https://auth.example.com",
				AccessTokenLifespan:  time.Hour,
				RefreshTokenLifespan: time.Minute, // Below minimum of 1 hour
				AuthCodeLifespan:     time.Minute * 10,
				HMACSecrets:          servercrypto.NewHMACSecrets([]byte("test-secret-with-32-bytes-long!!")),
				SigningKeyID:         "key-1",
				SigningKeyAlgorithm:  "RS256",
				SigningKey:           rsaKey,
			},
			wantErr: "refresh token lifespan must be between",
		},
		{
			name: "auth code lifespan too long",
			params: &AuthorizationServerParams{
				Issuer:               "https://auth.example.com",
				AccessTokenLifespan:  time.Hour,
				RefreshTokenLifespan: time.Hour * 24,
				AuthCodeLifespan:     time.Hour, // Above maximum of 10 minutes
				HMACSecrets:          servercrypto.NewHMACSecrets([]byte("test-secret-with-32-bytes-long!!")),
				SigningKeyID:         "key-1",
				SigningKeyAlgorithm:  "RS256",
				SigningKey:           rsaKey,
			},
			wantErr: "authorization code lifespan must be between",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewAuthorizationServerConfig(tt.params)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestNewAuthorizationServerConfig_WithRotatedSecrets(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	currentSecret := []byte("current-secret-with-32-bytes-ok!")
	rotatedSecret1 := []byte("rotated-secret1-with-32-bytes!!")
	rotatedSecret2 := []byte("rotated-secret2-with-32-bytes!!")

	params := &AuthorizationServerParams{
		Issuer:               "https://auth.example.com",
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: time.Hour * 24,
		AuthCodeLifespan:     time.Minute * 10,
		HMACSecrets: &servercrypto.HMACSecrets{
			Current: currentSecret,
			Rotated: [][]byte{rotatedSecret1, rotatedSecret2},
		},
		SigningKeyID:        "key-1",
		SigningKeyAlgorithm: "RS256",
		SigningKey:          rsaKey,
	}

	authzServerConfig, err := NewAuthorizationServerConfig(params)
	require.NoError(t, err)
	require.NotNil(t, authzServerConfig)

	// Verify fosite config has both current and rotated secrets
	assert.Equal(t, currentSecret, authzServerConfig.GlobalSecret)
	require.Len(t, authzServerConfig.RotatedGlobalSecrets, 2)
	assert.Equal(t, rotatedSecret1, authzServerConfig.RotatedGlobalSecrets[0])
	assert.Equal(t, rotatedSecret2, authzServerConfig.RotatedGlobalSecrets[1])
}

func TestNewAuthorizationServerConfig_WithoutRotatedSecrets(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	currentSecret := []byte("current-secret-with-32-bytes-ok!")

	params := &AuthorizationServerParams{
		Issuer:               "https://auth.example.com",
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: time.Hour * 24,
		AuthCodeLifespan:     time.Minute * 10,
		HMACSecrets: &servercrypto.HMACSecrets{
			Current: currentSecret,
			Rotated: nil,
		},
		SigningKeyID:        "key-1",
		SigningKeyAlgorithm: "RS256",
		SigningKey:          rsaKey,
	}

	authzServerConfig, err := NewAuthorizationServerConfig(params)
	require.NoError(t, err)
	require.NotNil(t, authzServerConfig)

	// Verify fosite config has only current secret, no rotated
	assert.Equal(t, currentSecret, authzServerConfig.GlobalSecret)
	assert.Nil(t, authzServerConfig.RotatedGlobalSecrets)
}

func TestAuthorizationServerConfig_PublicJWKS(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	params := &AuthorizationServerParams{
		Issuer:               "https://auth.example.com",
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: time.Hour * 24,
		AuthCodeLifespan:     time.Minute * 10,
		HMACSecrets:          servercrypto.NewHMACSecrets([]byte("test-secret-with-32-bytes-long!!")),
		SigningKeyID:         "key-1",
		SigningKeyAlgorithm:  "RS256",
		SigningKey:           rsaKey,
	}

	authzServerConfig, err := NewAuthorizationServerConfig(params)
	require.NoError(t, err)

	publicJWKS := authzServerConfig.PublicJWKS()
	require.NotNil(t, publicJWKS)
	require.Len(t, publicJWKS.Keys, 1)

	// Verify it's a public key (not private)
	_, ok := publicJWKS.Keys[0].Key.(*rsa.PublicKey)
	assert.True(t, ok, "expected public key, got %T", publicJWKS.Keys[0].Key)
}

// mockStorage is a minimal fosite.Storage implementation for testing.
type mockStorage struct{}

func (*mockStorage) GetClient(_ context.Context, _ string) (fosite.Client, error) {
	return nil, fosite.ErrNotFound
}

func (*mockStorage) ClientAssertionJWTValid(_ context.Context, _ string) error {
	return nil
}

func (*mockStorage) SetClientAssertionJWT(_ context.Context, _ string, _ time.Time) error {
	return nil
}

// mockAuthorizeHandler implements fosite.AuthorizeEndpointHandler for testing.
type mockAuthorizeHandler struct{}

func (*mockAuthorizeHandler) HandleAuthorizeEndpointRequest(_ context.Context, _ fosite.AuthorizeRequester, _ fosite.AuthorizeResponder) error {
	return nil
}

// mockTokenHandler implements fosite.TokenEndpointHandler for testing.
type mockTokenHandler struct{}

func (*mockTokenHandler) PopulateTokenEndpointResponse(_ context.Context, _ fosite.AccessRequester, _ fosite.AccessResponder) error {
	return nil
}

func (*mockTokenHandler) CanSkipClientAuth(_ context.Context, _ fosite.AccessRequester) bool {
	return false
}

func (*mockTokenHandler) CanHandleTokenEndpointRequest(_ context.Context, _ fosite.AccessRequester) bool {
	return true
}

func (*mockTokenHandler) HandleTokenEndpointRequest(_ context.Context, _ fosite.AccessRequester) error {
	return nil
}

// mockTokenIntrospector implements fosite.TokenIntrospector for testing.
type mockTokenIntrospector struct{}

func (*mockTokenIntrospector) IntrospectToken(_ context.Context, _ string, _ fosite.TokenType, _ fosite.AccessRequester, _ []string) (fosite.TokenType, error) {
	return fosite.AccessToken, nil
}

// mockRevocationHandler implements fosite.RevocationHandler for testing.
type mockRevocationHandler struct{}

func (*mockRevocationHandler) RevokeToken(_ context.Context, _ string, _ string, _ fosite.Client) error {
	return nil
}

func TestNewAuthorizationServer(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	params := &AuthorizationServerParams{
		Issuer:               "https://auth.example.com",
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: time.Hour * 24,
		AuthCodeLifespan:     time.Minute * 10,
		HMACSecrets:          servercrypto.NewHMACSecrets([]byte("test-secret-with-32-bytes-long!!")),
		SigningKeyID:         "key-1",
		SigningKeyAlgorithm:  "RS256",
		SigningKey:           rsaKey,
	}

	authzServerConfig, err := NewAuthorizationServerConfig(params)
	require.NoError(t, err)

	storage := &mockStorage{}

	t.Run("creates provider with no factories", func(t *testing.T) {
		t.Parallel()

		provider := NewAuthorizationServer(authzServerConfig, storage, nil)
		require.NotNil(t, provider)
	})

	t.Run("creates provider with authorize handler factory", func(t *testing.T) {
		t.Parallel()

		factory := func(_ *AuthorizationServerConfig, _ fosite.Storage, _ any) any {
			return &mockAuthorizeHandler{}
		}

		provider := NewAuthorizationServer(authzServerConfig, storage, nil, factory)
		require.NotNil(t, provider)
	})

	t.Run("creates provider with token handler factory", func(t *testing.T) {
		t.Parallel()

		factory := func(_ *AuthorizationServerConfig, _ fosite.Storage, _ any) any {
			return &mockTokenHandler{}
		}

		provider := NewAuthorizationServer(authzServerConfig, storage, nil, factory)
		require.NotNil(t, provider)
	})

	t.Run("creates provider with multiple factories", func(t *testing.T) {
		t.Parallel()

		authorizeFactory := func(_ *AuthorizationServerConfig, _ fosite.Storage, _ any) any {
			return &mockAuthorizeHandler{}
		}
		tokenFactory := func(_ *AuthorizationServerConfig, _ fosite.Storage, _ any) any {
			return &mockTokenHandler{}
		}
		introspectorFactory := func(_ *AuthorizationServerConfig, _ fosite.Storage, _ any) any {
			return &mockTokenIntrospector{}
		}
		revocationFactory := func(_ *AuthorizationServerConfig, _ fosite.Storage, _ any) any {
			return &mockRevocationHandler{}
		}

		provider := NewAuthorizationServer(authzServerConfig, storage, nil,
			authorizeFactory, tokenFactory, introspectorFactory, revocationFactory)
		require.NotNil(t, provider)
	})

	t.Run("handles factory returning nil", func(t *testing.T) {
		t.Parallel()

		factory := func(_ *AuthorizationServerConfig, _ fosite.Storage, _ any) any {
			return nil
		}

		provider := NewAuthorizationServer(authzServerConfig, storage, nil, factory)
		require.NotNil(t, provider)
	})

	t.Run("handles factory returning non-handler type", func(t *testing.T) {
		t.Parallel()

		factory := func(_ *AuthorizationServerConfig, _ fosite.Storage, _ any) any {
			return "not a handler"
		}

		provider := NewAuthorizationServer(authzServerConfig, storage, nil, factory)
		require.NotNil(t, provider)
	})
}
