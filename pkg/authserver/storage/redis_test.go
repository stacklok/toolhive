// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Tests use the withRedisStorage helper which calls t.Parallel() internally,
// making all subtests parallel despite not having explicit t.Parallel() calls.
//
//nolint:paralleltest // parallel execution handled by withRedisStorage helper
package storage

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ory/fosite"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/session"
)

// --- Test Helpers ---

func newTestRedisStorage(t *testing.T) (*RedisStorage, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	storage := NewRedisStorageWithClient(client, "test:auth:")
	return storage, mr
}

func withRedisStorage(t *testing.T, fn func(context.Context, *RedisStorage, *miniredis.Miniredis)) {
	t.Helper()
	t.Parallel()
	storage, mr := newTestRedisStorage(t)
	defer func() {
		_ = storage.Close()
		mr.Close()
	}()
	fn(context.Background(), storage, mr)
}

// newRedisTestRequester creates a fosite.Request with a real session.Session
// that can be properly serialized/deserialized through JSON for Redis storage.
func newRedisTestRequester(id string, client fosite.Client) fosite.Requester {
	return &fosite.Request{
		ID:                id,
		RequestedAt:       time.Now(),
		Client:            client,
		RequestedScope:    fosite.Arguments{"openid", "profile"},
		GrantedScope:      fosite.Arguments{"openid"},
		RequestedAudience: fosite.Arguments{},
		GrantedAudience:   fosite.Arguments{},
		Form:              make(url.Values),
		Session:           session.New("test-subject", "", ""),
	}
}

// newRedisTestRequesterWithExpiration creates a fosite.Request with a real session.Session
// and a specific expiration time for the given token type.
func newRedisTestRequesterWithExpiration(id string, client fosite.Client, tokenType fosite.TokenType, expiresAt time.Time) fosite.Requester {
	sess := session.New("test-subject", "", "")
	sess.SetExpiresAt(tokenType, expiresAt)
	return &fosite.Request{
		ID:                id,
		RequestedAt:       time.Now(),
		Client:            client,
		RequestedScope:    fosite.Arguments{"openid", "profile"},
		GrantedScope:      fosite.Arguments{"openid"},
		RequestedAudience: fosite.Arguments{},
		GrantedAudience:   fosite.Arguments{},
		Form:              make(url.Values),
		Session:           sess,
	}
}

func requireRedisNotFoundError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound, "should match storage.ErrNotFound")
	assert.ErrorIs(t, err, fosite.ErrNotFound, "should match fosite.ErrNotFound")
}

// --- Configuration Tests ---

func TestRedisConfig_Validation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     RedisConfig
		wantErr string
	}{
		{
			name:    "missing sentinel config",
			cfg:     RedisConfig{ACLUserConfig: &ACLUserConfig{}, KeyPrefix: "test:"},
			wantErr: "sentinel configuration is required",
		},
		{
			name:    "missing sentinel master name",
			cfg:     RedisConfig{SentinelConfig: &SentinelConfig{SentinelAddrs: []string{"localhost:26379"}}, ACLUserConfig: &ACLUserConfig{}, KeyPrefix: "test:"},
			wantErr: "sentinel master name is required",
		},
		{
			name:    "missing sentinel addresses",
			cfg:     RedisConfig{SentinelConfig: &SentinelConfig{MasterName: "mymaster"}, ACLUserConfig: &ACLUserConfig{}, KeyPrefix: "test:"},
			wantErr: "at least one sentinel address is required",
		},
		{
			name:    "missing ACL user config",
			cfg:     RedisConfig{SentinelConfig: &SentinelConfig{MasterName: "mymaster", SentinelAddrs: []string{"localhost:26379"}}, KeyPrefix: "test:"},
			wantErr: "ACL user configuration is required",
		},
		{
			name:    "missing ACL username",
			cfg:     RedisConfig{SentinelConfig: &SentinelConfig{MasterName: "mymaster", SentinelAddrs: []string{"localhost:26379"}}, ACLUserConfig: &ACLUserConfig{Password: "pass"}, KeyPrefix: "test:"},
			wantErr: "ACL username is required",
		},
		{
			name:    "missing ACL password",
			cfg:     RedisConfig{SentinelConfig: &SentinelConfig{MasterName: "mymaster", SentinelAddrs: []string{"localhost:26379"}}, ACLUserConfig: &ACLUserConfig{Username: "user"}, KeyPrefix: "test:"},
			wantErr: "ACL password is required",
		},
		{
			name:    "missing key prefix",
			cfg:     RedisConfig{SentinelConfig: &SentinelConfig{MasterName: "mymaster", SentinelAddrs: []string{"localhost:26379"}}, ACLUserConfig: &ACLUserConfig{Username: "user", Password: "pass"}},
			wantErr: "key prefix is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateConfig(&tt.cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestNewRedisStorage_ConnectionFailure(t *testing.T) {
	t.Parallel()

	cfg := RedisConfig{
		SentinelConfig: &SentinelConfig{
			MasterName:    "mymaster",
			SentinelAddrs: []string{"localhost:99999"}, // Invalid port
		},
		ACLUserConfig: &ACLUserConfig{
			Username: "user",
			Password: "pass",
		},
		KeyPrefix:   "test:",
		DialTimeout: 100 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_, err := NewRedisStorage(ctx, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to connect to redis")
}

// --- Client Tests ---

func TestRedisStorage_Client(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		clientID string
		setup    func(context.Context, *RedisStorage)
		wantErr  bool
	}{
		{"existing client", "test-client", func(ctx context.Context, s *RedisStorage) {
			_ = s.RegisterClient(ctx, &mockClient{id: "test-client"})
		}, false},
		{"non-existent client", "non-existent", func(_ context.Context, _ *RedisStorage) {}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
				tt.setup(ctx, s)
				client, err := s.GetClient(ctx, tt.clientID)
				if tt.wantErr {
					requireRedisNotFoundError(t, err)
					assert.Nil(t, client)
				} else {
					require.NoError(t, err)
					assert.Equal(t, tt.clientID, client.GetID())
				}
			})
		})
	}
}

func TestRedisStorage_RegisterClient(t *testing.T) {
	withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
		client := &mockClient{id: "test-client", scopes: []string{"openid", "profile"}}
		require.NoError(t, s.RegisterClient(ctx, client))

		retrieved, err := s.GetClient(ctx, "test-client")
		require.NoError(t, err)
		assert.Equal(t, client.GetID(), retrieved.GetID())
		assert.Equal(t, client.GetScopes(), retrieved.GetScopes())
	})
}

func TestRedisStorage_ClientAssertionJWT(t *testing.T) {
	t.Parallel()

	t.Run("unknown JTI is valid", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			err := s.ClientAssertionJWTValid(ctx, "unknown-jti")
			require.NoError(t, err)
		})
	})

	t.Run("known JTI is invalid", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			require.NoError(t, s.SetClientAssertionJWT(ctx, "test-jti", time.Now().Add(time.Hour)))
			err := s.ClientAssertionJWTValid(ctx, "test-jti")
			assert.ErrorIs(t, err, fosite.ErrJTIKnown)
		})
	})

	t.Run("expired JTI is not stored", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			require.NoError(t, s.SetClientAssertionJWT(ctx, "expired-jti", time.Now().Add(-time.Hour)))
			err := s.ClientAssertionJWTValid(ctx, "expired-jti")
			require.NoError(t, err) // Should be valid because expired JTI is not stored
		})
	})
}

// --- Authorization Code Tests ---

func TestRedisStorage_AuthorizeCode(t *testing.T) {
	t.Parallel()

	t.Run("create and get", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			// First register the client
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			request := newRedisTestRequester("req-1", client)
			require.NoError(t, s.CreateAuthorizeCodeSession(ctx, "code-123", request))

			retrieved, err := s.GetAuthorizeCodeSession(ctx, "code-123", nil)
			require.NoError(t, err)
			assert.Equal(t, request.GetID(), retrieved.GetID())
		})
	})

	t.Run("get non-existent", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			_, err := s.GetAuthorizeCodeSession(ctx, "non-existent", nil)
			requireRedisNotFoundError(t, err)
		})
	})

	t.Run("invalidate code", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			request := newRedisTestRequester("req-1", client)
			require.NoError(t, s.CreateAuthorizeCodeSession(ctx, "code-123", request))
			require.NoError(t, s.InvalidateAuthorizeCodeSession(ctx, "code-123"))

			retrieved, err := s.GetAuthorizeCodeSession(ctx, "code-123", nil)
			require.Error(t, err)
			assert.ErrorIs(t, err, fosite.ErrInvalidatedAuthorizeCode)
			assert.NotNil(t, retrieved, "must return request with invalidated error")
		})
	})

	t.Run("invalidation extends auth code TTL and returns requester", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			request := newRedisTestRequester("req-1", client)
			require.NoError(t, s.CreateAuthorizeCodeSession(ctx, "code-replay", request))

			// Record the initial TTL of the auth code key.
			codeKey := redisKey(s.keyPrefix, KeyTypeAuthCode, "code-replay")
			initialTTL := mr.TTL(codeKey)

			require.NoError(t, s.InvalidateAuthorizeCodeSession(ctx, "code-replay"))

			// Verify the auth code TTL was extended to match the invalidation marker.
			extendedTTL := mr.TTL(codeKey)
			assert.Greater(t, extendedTTL, initialTTL, "auth code TTL should be extended on invalidation")

			// Fast-forward past the original auth code TTL but within the extended TTL.
			// The auth code data must still be available for replay detection.
			mr.FastForward(initialTTL + time.Second)

			retrieved, err := s.GetAuthorizeCodeSession(ctx, "code-replay", nil)
			require.Error(t, err)
			assert.ErrorIs(t, err, fosite.ErrInvalidatedAuthorizeCode)
			assert.NotNil(t, retrieved, "must return request with invalidated error for replay detection")
		})
	})

	t.Run("invalidate non-existent code", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			err := s.InvalidateAuthorizeCodeSession(ctx, "non-existent")
			requireRedisNotFoundError(t, err)
		})
	})
}

// --- Access Token Tests ---

func TestRedisStorage_AccessToken(t *testing.T) {
	t.Parallel()

	t.Run("create and get", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			request := newRedisTestRequester("req-1", client)
			require.NoError(t, s.CreateAccessTokenSession(ctx, "sig-123", request))

			retrieved, err := s.GetAccessTokenSession(ctx, "sig-123", nil)
			require.NoError(t, err)
			assert.Equal(t, request.GetID(), retrieved.GetID())
		})
	})

	t.Run("get non-existent", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			_, err := s.GetAccessTokenSession(ctx, "non-existent", nil)
			requireRedisNotFoundError(t, err)
		})
	})

	t.Run("delete", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			request := newRedisTestRequester("req-1", client)
			require.NoError(t, s.CreateAccessTokenSession(ctx, "to-delete", request))
			require.NoError(t, s.DeleteAccessTokenSession(ctx, "to-delete"))

			_, err := s.GetAccessTokenSession(ctx, "to-delete", nil)
			requireRedisNotFoundError(t, err)
		})
	})

	t.Run("delete non-existent returns error", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			err := s.DeleteAccessTokenSession(ctx, "non-existent")
			requireRedisNotFoundError(t, err)
		})
	})
}

// --- Session Round-Trip Tests ---

func TestRedisStorage_SessionRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("JWT claims survive serialization", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			// Create a session with JWT claims and upstream session ID
			sess := session.New("user-123", "upstream-session-456", "test-client")
			request := &fosite.Request{
				ID:             "req-jwt",
				RequestedAt:    time.Now(),
				Client:         client,
				RequestedScope: fosite.Arguments{"openid"},
				GrantedScope:   fosite.Arguments{"openid"},
				Form:           make(url.Values),
				Session:        sess,
			}

			require.NoError(t, s.CreateAccessTokenSession(ctx, "jwt-sig", request))

			retrieved, err := s.GetAccessTokenSession(ctx, "jwt-sig", nil)
			require.NoError(t, err)

			// Verify the session implements UpstreamSession (required for token refresh)
			upstreamSess, ok := retrieved.GetSession().(session.UpstreamSession)
			require.True(t, ok, "session must implement UpstreamSession for token refresh")

			// Verify JWT claims are preserved
			jwtClaims := upstreamSess.GetJWTClaims()
			require.NotNil(t, jwtClaims)
			claims := jwtClaims.ToMapClaims()
			assert.Equal(t, "user-123", claims["sub"])
			assert.Equal(t, "upstream-session-456", claims["tsid"])
			assert.Equal(t, "test-client", claims["client_id"])

			// Verify upstream session ID is preserved
			assert.Equal(t, "upstream-session-456", upstreamSess.GetIDPSessionID())
		})
	})
}

// --- Refresh Token Tests ---

func TestRedisStorage_RefreshToken(t *testing.T) {
	t.Parallel()

	t.Run("create and get", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			request := newRedisTestRequester("req-1", client)
			require.NoError(t, s.CreateRefreshTokenSession(ctx, "refresh-sig", "access-sig", request))

			retrieved, err := s.GetRefreshTokenSession(ctx, "refresh-sig", nil)
			require.NoError(t, err)
			assert.Equal(t, request.GetID(), retrieved.GetID())
		})
	})

	t.Run("get non-existent", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			_, err := s.GetRefreshTokenSession(ctx, "non-existent", nil)
			requireRedisNotFoundError(t, err)
		})
	})

	t.Run("delete", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			request := newRedisTestRequester("req-1", client)
			require.NoError(t, s.CreateRefreshTokenSession(ctx, "to-delete", "access-sig", request))
			require.NoError(t, s.DeleteRefreshTokenSession(ctx, "to-delete"))

			_, err := s.GetRefreshTokenSession(ctx, "to-delete", nil)
			requireRedisNotFoundError(t, err)
		})
	})
}

func TestRedisStorage_RotateRefreshToken(t *testing.T) {
	t.Parallel()

	t.Run("rotate deletes refresh and access tokens", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			request := newRedisTestRequester("request-123", client)

			require.NoError(t, s.CreateRefreshTokenSession(ctx, "refresh-sig", "access-sig", request))
			require.NoError(t, s.CreateAccessTokenSession(ctx, "access-sig", request))
			require.NoError(t, s.RotateRefreshToken(ctx, "request-123", "refresh-sig"))

			_, err := s.GetRefreshTokenSession(ctx, "refresh-sig", nil)
			requireRedisNotFoundError(t, err)
			_, err = s.GetAccessTokenSession(ctx, "access-sig", nil)
			requireRedisNotFoundError(t, err)
		})
	})

	t.Run("rotate non-existent token (no error)", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			require.NoError(t, s.RotateRefreshToken(ctx, "non-existent", "non-existent"))
		})
	})
}

// --- Token Revocation Tests ---

func TestRedisStorage_RevokeAccessToken(t *testing.T) {
	withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
		client := testClient()
		require.NoError(t, s.RegisterClient(ctx, client))

		request := newRedisTestRequester("request-123", client)

		// Create multiple access tokens with same request ID
		require.NoError(t, s.CreateAccessTokenSession(ctx, "access-1", request))
		require.NoError(t, s.CreateAccessTokenSession(ctx, "access-2", request))

		// Revoke by request ID
		require.NoError(t, s.RevokeAccessToken(ctx, "request-123"))

		// Both should be gone
		_, err := s.GetAccessTokenSession(ctx, "access-1", nil)
		requireRedisNotFoundError(t, err)
		_, err = s.GetAccessTokenSession(ctx, "access-2", nil)
		requireRedisNotFoundError(t, err)
	})
}

func TestRedisStorage_RevokeRefreshToken(t *testing.T) {
	withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
		client := testClient()
		require.NoError(t, s.RegisterClient(ctx, client))

		request := newRedisTestRequester("request-123", client)

		require.NoError(t, s.CreateRefreshTokenSession(ctx, "refresh-1", "access-1", request))

		require.NoError(t, s.RevokeRefreshToken(ctx, "request-123"))

		_, err := s.GetRefreshTokenSession(ctx, "refresh-1", nil)
		requireRedisNotFoundError(t, err)
	})
}

// --- PKCE Tests ---

func TestRedisStorage_PKCE(t *testing.T) {
	t.Parallel()

	t.Run("create and get", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			request := newRedisTestRequester("req-1", client)
			require.NoError(t, s.CreatePKCERequestSession(ctx, "pkce-sig", request))

			retrieved, err := s.GetPKCERequestSession(ctx, "pkce-sig", nil)
			require.NoError(t, err)
			assert.Equal(t, request.GetID(), retrieved.GetID())
		})
	})

	t.Run("get non-existent", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			_, err := s.GetPKCERequestSession(ctx, "non-existent", nil)
			requireRedisNotFoundError(t, err)
		})
	})

	t.Run("delete", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			request := newRedisTestRequester("req-1", client)
			require.NoError(t, s.CreatePKCERequestSession(ctx, "to-delete", request))
			require.NoError(t, s.DeletePKCERequestSession(ctx, "to-delete"))

			_, err := s.GetPKCERequestSession(ctx, "to-delete", nil)
			requireRedisNotFoundError(t, err)
		})
	})
}

// --- Upstream Token Tests ---

func TestRedisStorage_UpstreamTokens(t *testing.T) {
	t.Parallel()

	t.Run("store and get", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			tokens := &UpstreamTokens{
				ProviderID:   "google",
				AccessToken:  "upstream-access",
				RefreshToken: "upstream-refresh",
				IDToken:      "upstream-id",
				ExpiresAt:    time.Now().Add(time.Hour),
				UserID:       "user-123",
				ClientID:     "test-client-id",
			}
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-123", tokens))

			retrieved, err := s.GetUpstreamTokens(ctx, "session-123")
			require.NoError(t, err)
			assert.Equal(t, tokens.AccessToken, retrieved.AccessToken)
			assert.Equal(t, tokens.RefreshToken, retrieved.RefreshToken)
			assert.Equal(t, tokens.UserID, retrieved.UserID)
			assert.Equal(t, tokens.ClientID, retrieved.ClientID)
		})
	})

	t.Run("get non-existent", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			_, err := s.GetUpstreamTokens(ctx, "non-existent")
			requireRedisNotFoundError(t, err)
		})
	})

	t.Run("delete", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "to-delete", &UpstreamTokens{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour)}))
			require.NoError(t, s.DeleteUpstreamTokens(ctx, "to-delete"))
			_, err := s.GetUpstreamTokens(ctx, "to-delete")
			requireRedisNotFoundError(t, err)
		})
	})

	t.Run("overwrite existing tokens", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session", &UpstreamTokens{AccessToken: "token-1", UserID: "user1", ExpiresAt: time.Now().Add(time.Hour)}))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session", &UpstreamTokens{AccessToken: "token-2", UserID: "user2", ExpiresAt: time.Now().Add(time.Hour)}))

			retrieved, err := s.GetUpstreamTokens(ctx, "session")
			require.NoError(t, err)
			assert.Equal(t, "token-2", retrieved.AccessToken)
			assert.Equal(t, "user2", retrieved.UserID)
		})
	})

	t.Run("get expired tokens returns ErrExpired", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			// Store with an ExpiresAt that's already in the past.
			// The TTL will be set to DefaultAccessTokenTTL, but the stored
			// ExpiresAt will be checked and return ErrExpired.
			require.NoError(t, s.StoreUpstreamTokens(ctx, "expired", &UpstreamTokens{
				AccessToken: "expired-token", ExpiresAt: time.Now().Add(-time.Hour),
			}))

			retrieved, err := s.GetUpstreamTokens(ctx, "expired")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrExpired)
			assert.Nil(t, retrieved)
		})
	})

	t.Run("zero ExpiresAt tokens are retrievable (no expiry)", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			// Store tokens with zero ExpiresAt (no expiry set).
			// These should be retrievable — Redis TTL handles actual expiration.
			require.NoError(t, s.StoreUpstreamTokens(ctx, "no-expiry", &UpstreamTokens{
				AccessToken: "no-expiry-token",
				ProviderID:  "test-provider",
			}))

			retrieved, err := s.GetUpstreamTokens(ctx, "no-expiry")
			require.NoError(t, err)
			require.NotNil(t, retrieved)
			assert.Equal(t, "no-expiry-token", retrieved.AccessToken)
			assert.Equal(t, "test-provider", retrieved.ProviderID)
			assert.True(t, retrieved.ExpiresAt.IsZero())
		})
	})

	t.Run("nil tokens is valid", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-id", nil))
			retrieved, err := s.GetUpstreamTokens(ctx, "session-id")
			require.NoError(t, err)
			assert.Nil(t, retrieved)
		})
	})
}

// --- Pending Authorization Tests ---

func TestRedisStorage_PendingAuthorization(t *testing.T) {
	t.Parallel()

	makePending := func(state string) *PendingAuthorization {
		return &PendingAuthorization{
			ClientID: "test-client", RedirectURI: "https://example.com/callback",
			State: "client-state", PKCEChallenge: "challenge", PKCEMethod: "S256",
			Scopes: []string{"openid", "profile"}, InternalState: state,
			UpstreamPKCEVerifier: "verifier", UpstreamNonce: "nonce", CreatedAt: time.Now(),
		}
	}

	t.Run("store and load", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			pending := makePending("internal-state")
			require.NoError(t, s.StorePendingAuthorization(ctx, "internal-state", pending))

			retrieved, err := s.LoadPendingAuthorization(ctx, "internal-state")
			require.NoError(t, err)
			assert.Equal(t, pending.ClientID, retrieved.ClientID)
			assert.Equal(t, pending.PKCEChallenge, retrieved.PKCEChallenge)
			assert.Equal(t, pending.Scopes, retrieved.Scopes)
		})
	})

	t.Run("load non-existent", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			_, err := s.LoadPendingAuthorization(ctx, "non-existent")
			requireRedisNotFoundError(t, err)
		})
	})

	t.Run("delete", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			require.NoError(t, s.StorePendingAuthorization(ctx, "to-delete", makePending("to-delete")))
			require.NoError(t, s.DeletePendingAuthorization(ctx, "to-delete"))
			_, err := s.LoadPendingAuthorization(ctx, "to-delete")
			requireRedisNotFoundError(t, err)
		})
	})

	t.Run("delete non-existent returns error", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			err := s.DeletePendingAuthorization(ctx, "non-existent")
			requireRedisNotFoundError(t, err)
		})
	})
}

// --- User Storage Tests ---

func TestRedisStorage_User(t *testing.T) {
	t.Parallel()

	makeUser := func(id string) *User {
		now := time.Now()
		return &User{ID: id, CreatedAt: now, UpdatedAt: now}
	}

	t.Run("create and get", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			user := makeUser("user-123")
			require.NoError(t, s.CreateUser(ctx, user))

			retrieved, err := s.GetUser(ctx, "user-123")
			require.NoError(t, err)
			assert.Equal(t, user.ID, retrieved.ID)
		})
	})

	t.Run("get non-existent", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			_, err := s.GetUser(ctx, "non-existent")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	})

	t.Run("create duplicate", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			user := makeUser("user-123")
			require.NoError(t, s.CreateUser(ctx, user))
			err := s.CreateUser(ctx, user)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrAlreadyExists)
		})
	})

	t.Run("delete", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			require.NoError(t, s.CreateUser(ctx, makeUser("to-delete")))
			require.NoError(t, s.DeleteUser(ctx, "to-delete"))
			_, err := s.GetUser(ctx, "to-delete")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	})

	t.Run("delete non-existent", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			err := s.DeleteUser(ctx, "non-existent")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	})
}

func TestRedisStorage_DeleteUser_CascadesAssociatedData(t *testing.T) {
	withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
		now := time.Now()
		user := &User{ID: "user-cascade", CreatedAt: now, UpdatedAt: now}
		require.NoError(t, s.CreateUser(ctx, user))

		// Create another user for comparison
		otherUser := &User{ID: "other-user", CreatedAt: now, UpdatedAt: now}
		require.NoError(t, s.CreateUser(ctx, otherUser))

		// Link multiple provider identities to the user
		identity1 := &ProviderIdentity{UserID: "user-cascade", ProviderID: "google", ProviderSubject: "google-sub", LinkedAt: now}
		identity2 := &ProviderIdentity{UserID: "user-cascade", ProviderID: "github", ProviderSubject: "github-sub", LinkedAt: now}
		require.NoError(t, s.CreateProviderIdentity(ctx, identity1))
		require.NoError(t, s.CreateProviderIdentity(ctx, identity2))

		// Also create an identity for a different user to ensure it is not deleted
		otherIdentity := &ProviderIdentity{UserID: "other-user", ProviderID: "google", ProviderSubject: "other-google-sub", LinkedAt: now}
		require.NoError(t, s.CreateProviderIdentity(ctx, otherIdentity))

		// Store upstream tokens for both users
		userTokens := &UpstreamTokens{
			ProviderID: "google", AccessToken: "user-token", UserID: "user-cascade",
			UpstreamSubject: "google-sub", ClientID: "client-1", ExpiresAt: now.Add(time.Hour),
		}
		otherTokens := &UpstreamTokens{
			ProviderID: "google", AccessToken: "other-token", UserID: "other-user",
			UpstreamSubject: "other-google-sub", ClientID: "client-1", ExpiresAt: now.Add(time.Hour),
		}
		require.NoError(t, s.StoreUpstreamTokens(ctx, "session-user", userTokens))
		require.NoError(t, s.StoreUpstreamTokens(ctx, "session-other", otherTokens))

		// Delete the user - should cascade delete associated data
		require.NoError(t, s.DeleteUser(ctx, "user-cascade"))

		// Verify the user is gone
		_, err := s.GetUser(ctx, "user-cascade")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNotFound)

		// Verify the user's identities are gone
		_, err = s.GetProviderIdentity(ctx, "google", "google-sub")
		assert.ErrorIs(t, err, ErrNotFound)
		_, err = s.GetProviderIdentity(ctx, "github", "github-sub")
		assert.ErrorIs(t, err, ErrNotFound)

		// Verify the user's upstream tokens are gone
		_, err = s.GetUpstreamTokens(ctx, "session-user")
		assert.ErrorIs(t, err, ErrNotFound)

		// Verify the other user still exists
		otherUserRetrieved, err := s.GetUser(ctx, "other-user")
		require.NoError(t, err)
		assert.Equal(t, "other-user", otherUserRetrieved.ID)

		// Verify the other user's identity is still there
		retrieved, err := s.GetProviderIdentity(ctx, "google", "other-google-sub")
		require.NoError(t, err)
		assert.Equal(t, "other-user", retrieved.UserID)

		// Verify the other user's upstream tokens are still there
		otherRetrieved, err := s.GetUpstreamTokens(ctx, "session-other")
		require.NoError(t, err)
		assert.Equal(t, "other-token", otherRetrieved.AccessToken)
	})
}

// --- Provider Identity Tests ---

func TestRedisStorage_ProviderIdentity(t *testing.T) {
	t.Parallel()

	makeIdentity := func(providerID, providerSubject, userID string) *ProviderIdentity {
		now := time.Now()
		return &ProviderIdentity{
			UserID:          userID,
			ProviderID:      providerID,
			ProviderSubject: providerSubject,
			LinkedAt:        now,
			LastUsedAt:      now,
		}
	}

	t.Run("create and get", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			now := time.Now()
			require.NoError(t, s.CreateUser(ctx, &User{ID: "user-123", CreatedAt: now, UpdatedAt: now}))

			identity := makeIdentity("google", "google-sub-123", "user-123")
			require.NoError(t, s.CreateProviderIdentity(ctx, identity))

			retrieved, err := s.GetProviderIdentity(ctx, "google", "google-sub-123")
			require.NoError(t, err)
			assert.Equal(t, identity.UserID, retrieved.UserID)
			assert.Equal(t, identity.ProviderID, retrieved.ProviderID)
			assert.Equal(t, identity.ProviderSubject, retrieved.ProviderSubject)
		})
	})

	t.Run("create sets up reverse index for GetUserProviderIdentities", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			now := time.Now()
			require.NoError(t, s.CreateUser(ctx, &User{ID: "user-reverse-idx", CreatedAt: now, UpdatedAt: now}))

			// Create a provider identity
			identity := makeIdentity("github", "github-sub-456", "user-reverse-idx")
			require.NoError(t, s.CreateProviderIdentity(ctx, identity))

			// Verify the reverse index was set up by calling GetUserProviderIdentities
			// This confirms the user:providers:{userID} set was populated correctly
			identities, err := s.GetUserProviderIdentities(ctx, "user-reverse-idx")
			require.NoError(t, err)
			require.Len(t, identities, 1, "reverse index should contain exactly one identity")
			assert.Equal(t, "github", identities[0].ProviderID)
			assert.Equal(t, "github-sub-456", identities[0].ProviderSubject)
			assert.Equal(t, "user-reverse-idx", identities[0].UserID)
		})
	})

	t.Run("get non-existent", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			_, err := s.GetProviderIdentity(ctx, "github", "non-existent")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	})

	t.Run("create for non-existent user", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			identity := makeIdentity("google", "google-sub-123", "non-existent-user")
			err := s.CreateProviderIdentity(ctx, identity)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	})

	t.Run("create duplicate", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			now := time.Now()
			require.NoError(t, s.CreateUser(ctx, &User{ID: "user-123", CreatedAt: now, UpdatedAt: now}))

			identity := makeIdentity("google", "google-sub-123", "user-123")
			require.NoError(t, s.CreateProviderIdentity(ctx, identity))
			err := s.CreateProviderIdentity(ctx, identity)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrAlreadyExists)
		})
	})

	t.Run("update last used at", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			now := time.Now()
			require.NoError(t, s.CreateUser(ctx, &User{ID: "user-update", CreatedAt: now, UpdatedAt: now}))
			identity := makeIdentity("google", "google-sub-update", "user-update")
			require.NoError(t, s.CreateProviderIdentity(ctx, identity))

			newLastUsed := now.Add(time.Hour)
			require.NoError(t, s.UpdateProviderIdentityLastUsed(ctx, "google", "google-sub-update", newLastUsed))

			retrieved, err := s.GetProviderIdentity(ctx, "google", "google-sub-update")
			require.NoError(t, err)
			assert.WithinDuration(t, newLastUsed, retrieved.LastUsedAt, time.Second)
		})
	})

	t.Run("update last used at for non-existent identity", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			err := s.UpdateProviderIdentityLastUsed(ctx, "github", "non-existent", time.Now())
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	})
}

func TestRedisStorage_GetUserProviderIdentities(t *testing.T) {
	t.Parallel()

	t.Run("returns all identities for user", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			now := time.Now()
			require.NoError(t, s.CreateUser(ctx, &User{ID: "user-1", CreatedAt: now, UpdatedAt: now}))

			id1 := &ProviderIdentity{UserID: "user-1", ProviderID: "google", ProviderSubject: "google-sub", LinkedAt: now}
			id2 := &ProviderIdentity{UserID: "user-1", ProviderID: "github", ProviderSubject: "github-sub", LinkedAt: now}
			require.NoError(t, s.CreateProviderIdentity(ctx, id1))
			require.NoError(t, s.CreateProviderIdentity(ctx, id2))

			identities, err := s.GetUserProviderIdentities(ctx, "user-1")
			require.NoError(t, err)
			assert.Len(t, identities, 2)

			providers := make(map[string]bool)
			for _, id := range identities {
				providers[id.ProviderID] = true
				assert.Equal(t, "user-1", id.UserID)
			}
			assert.True(t, providers["google"])
			assert.True(t, providers["github"])
		})
	})

	t.Run("returns empty slice for user with no identities", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			now := time.Now()
			require.NoError(t, s.CreateUser(ctx, &User{ID: "lonely-user", CreatedAt: now, UpdatedAt: now}))

			identities, err := s.GetUserProviderIdentities(ctx, "lonely-user")
			require.NoError(t, err)
			assert.Empty(t, identities)
		})
	})

	t.Run("returns error for non-existent user", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			_, err := s.GetUserProviderIdentities(ctx, "non-existent")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	})
}

// --- Input Validation Tests ---

func TestRedisStorage_InputValidation(t *testing.T) {
	t.Parallel()

	client := testClient()
	tests := []struct {
		name    string
		fn      func(context.Context, *RedisStorage) error
		wantErr error
	}{
		{"CreateAuthorizeCodeSession empty code", func(ctx context.Context, s *RedisStorage) error {
			return s.CreateAuthorizeCodeSession(ctx, "", newRedisTestRequester("r", client))
		}, fosite.ErrInvalidRequest},
		{"CreateAuthorizeCodeSession nil request", func(ctx context.Context, s *RedisStorage) error {
			return s.CreateAuthorizeCodeSession(ctx, "code", nil)
		}, fosite.ErrInvalidRequest},
		{"CreateAccessTokenSession empty signature", func(ctx context.Context, s *RedisStorage) error {
			return s.CreateAccessTokenSession(ctx, "", newRedisTestRequester("r", client))
		}, fosite.ErrInvalidRequest},
		{"CreateAccessTokenSession nil request", func(ctx context.Context, s *RedisStorage) error {
			return s.CreateAccessTokenSession(ctx, "sig", nil)
		}, fosite.ErrInvalidRequest},
		{"CreateRefreshTokenSession empty signature", func(ctx context.Context, s *RedisStorage) error {
			return s.CreateRefreshTokenSession(ctx, "", "a", newRedisTestRequester("r", client))
		}, fosite.ErrInvalidRequest},
		{"CreateRefreshTokenSession nil request", func(ctx context.Context, s *RedisStorage) error {
			return s.CreateRefreshTokenSession(ctx, "sig", "a", nil)
		}, fosite.ErrInvalidRequest},
		{"CreatePKCERequestSession empty signature", func(ctx context.Context, s *RedisStorage) error {
			return s.CreatePKCERequestSession(ctx, "", newRedisTestRequester("r", client))
		}, fosite.ErrInvalidRequest},
		{"CreatePKCERequestSession nil request", func(ctx context.Context, s *RedisStorage) error {
			return s.CreatePKCERequestSession(ctx, "sig", nil)
		}, fosite.ErrInvalidRequest},
		{"StoreUpstreamTokens empty sessionID", func(ctx context.Context, s *RedisStorage) error {
			return s.StoreUpstreamTokens(ctx, "", &UpstreamTokens{AccessToken: "t"})
		}, fosite.ErrInvalidRequest},
		{"StorePendingAuthorization empty state", func(ctx context.Context, s *RedisStorage) error {
			return s.StorePendingAuthorization(ctx, "", &PendingAuthorization{ClientID: "c"})
		}, fosite.ErrInvalidRequest},
		{"StorePendingAuthorization nil pending", func(ctx context.Context, s *RedisStorage) error {
			return s.StorePendingAuthorization(ctx, "state", nil)
		}, fosite.ErrInvalidRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
				err := tt.fn(ctx, s)
				require.Error(t, err)
				require.ErrorIs(t, err, tt.wantErr)
			})
		})
	}
}

func TestRedisStorage_UserInputValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fn      func(context.Context, *RedisStorage) error
		wantErr error
	}{
		{"CreateUser nil user", func(ctx context.Context, s *RedisStorage) error {
			return s.CreateUser(ctx, nil)
		}, fosite.ErrInvalidRequest},
		{"CreateUser empty ID", func(ctx context.Context, s *RedisStorage) error {
			return s.CreateUser(ctx, &User{ID: ""})
		}, fosite.ErrInvalidRequest},
		{"CreateProviderIdentity nil identity", func(ctx context.Context, s *RedisStorage) error {
			return s.CreateProviderIdentity(ctx, nil)
		}, fosite.ErrInvalidRequest},
		{"CreateProviderIdentity empty user ID", func(ctx context.Context, s *RedisStorage) error {
			return s.CreateProviderIdentity(ctx, &ProviderIdentity{UserID: "", ProviderID: "google", ProviderSubject: "sub"})
		}, fosite.ErrInvalidRequest},
		{"CreateProviderIdentity empty provider ID", func(ctx context.Context, s *RedisStorage) error {
			return s.CreateProviderIdentity(ctx, &ProviderIdentity{UserID: "user-1", ProviderID: "", ProviderSubject: "sub"})
		}, fosite.ErrInvalidRequest},
		{"CreateProviderIdentity empty provider subject", func(ctx context.Context, s *RedisStorage) error {
			return s.CreateProviderIdentity(ctx, &ProviderIdentity{UserID: "user-1", ProviderID: "google", ProviderSubject: ""})
		}, fosite.ErrInvalidRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
				err := tt.fn(ctx, s)
				require.Error(t, err)
				require.ErrorIs(t, err, tt.wantErr)
			})
		})
	}
}

// --- TTL and Expiration Tests ---

func TestRedisStorage_TTLHandling(t *testing.T) {
	t.Parallel()

	t.Run("access tokens expire automatically", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			// Create request with short expiration
			request := newRedisTestRequesterWithExpiration("req-1", client, fosite.AccessToken, time.Now().Add(time.Second))
			require.NoError(t, s.CreateAccessTokenSession(ctx, "short-lived", request))

			// Should exist initially
			_, err := s.GetAccessTokenSession(ctx, "short-lived", nil)
			require.NoError(t, err)

			// Fast-forward time
			mr.FastForward(2 * time.Second)

			// Should be gone after TTL
			_, err = s.GetAccessTokenSession(ctx, "short-lived", nil)
			requireRedisNotFoundError(t, err)
		})
	})

	t.Run("pending authorizations expire automatically", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
			pending := &PendingAuthorization{
				ClientID: "test", State: "state", CreatedAt: time.Now(),
			}
			require.NoError(t, s.StorePendingAuthorization(ctx, "expire-me", pending))

			// Should exist initially
			_, err := s.LoadPendingAuthorization(ctx, "expire-me")
			require.NoError(t, err)

			// Fast-forward past default TTL (10 minutes)
			mr.FastForward(15 * time.Minute)

			// Should be gone after TTL
			_, err = s.LoadPendingAuthorization(ctx, "expire-me")
			requireRedisNotFoundError(t, err)
		})
	})
}

// --- Concurrent Access Tests ---

func TestRedisStorage_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	t.Run("concurrent writes", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			var wg sync.WaitGroup
			for i := 0; i < 50; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					request := newRedisTestRequester(fmt.Sprintf("req-%d", idx), client)
					_ = s.CreateAccessTokenSession(ctx, fmt.Sprintf("token-%d", idx), request)
				}(i)
			}
			wg.Wait()
		})
	})

	t.Run("concurrent reads and writes", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			client := testClient()
			require.NoError(t, s.RegisterClient(ctx, client))

			// Preload some data
			for i := 0; i < 10; i++ {
				request := newRedisTestRequester(fmt.Sprintf("preload-%d", i), client)
				_ = s.CreateAccessTokenSession(ctx, fmt.Sprintf("preload-%d", i), request)
			}

			var wg sync.WaitGroup
			for i := 0; i < 50; i++ {
				wg.Add(2)
				go func(idx int) {
					defer wg.Done()
					request := newRedisTestRequester(fmt.Sprintf("req-%d", idx), client)
					_ = s.CreateAccessTokenSession(ctx, fmt.Sprintf("token-%d", idx), request)
				}(i)
				go func(idx int) {
					defer wg.Done()
					_, _ = s.GetAccessTokenSession(ctx, fmt.Sprintf("preload-%d", idx%10), nil)
				}(i)
			}
			wg.Wait()
		})
	})

	t.Run("concurrent client registration and lookup", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			var wg sync.WaitGroup
			numGoroutines := 25
			for i := 0; i < numGoroutines; i++ {
				wg.Add(2)
				go func(idx int) {
					defer wg.Done()
					_ = s.RegisterClient(ctx, &mockClient{id: fmt.Sprintf("client-%d", idx)})
				}(i)
				go func(idx int) {
					defer wg.Done()
					_, _ = s.GetClient(ctx, fmt.Sprintf("client-%d", idx))
				}(i)
			}
			wg.Wait()

			// Verify all clients exist
			for i := 0; i < numGoroutines; i++ {
				client, err := s.GetClient(ctx, fmt.Sprintf("client-%d", i))
				require.NoError(t, err, "client-%d should exist", i)
				assert.Equal(t, fmt.Sprintf("client-%d", i), client.GetID())
			}
		})
	})
}

// --- Interface Compliance Tests ---

func TestRedisStorage_ImplementsStorage(t *testing.T) {
	t.Parallel()
	var _ Storage = (*RedisStorage)(nil)
}

// --- Key Generation Tests ---

func TestDeriveKeyPrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		namespace string
		name      string
		expected  string
	}{
		{"default", "my-server", "thv:auth:{default:my-server}:"},
		{"prod", "auth-server", "thv:auth:{prod:auth-server}:"},
		{"", "test", "thv:auth:{:test}:"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/%s", tt.namespace, tt.name), func(t *testing.T) {
			t.Parallel()
			result := DeriveKeyPrefix(tt.namespace, tt.name)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRedisKeyGeneration(t *testing.T) {
	t.Parallel()

	t.Run("redisKey", func(t *testing.T) {
		t.Parallel()
		result := redisKey("test:auth:", KeyTypeAccess, "sig-123")
		assert.Equal(t, "test:auth:access:sig-123", result)
	})

	t.Run("redisProviderKey", func(t *testing.T) {
		t.Parallel()
		result := redisProviderKey("test:auth:", "google", "sub-123")
		assert.Equal(t, "test:auth:provider:6:google:sub-123", result)
	})

	t.Run("redisSetKey", func(t *testing.T) {
		t.Parallel()
		result := redisSetKey("test:auth:", KeyTypeReqIDAccess, "req-123")
		assert.Equal(t, "test:auth:reqid:access:req-123", result)
	})
}

// --- Health Check Tests ---

func TestRedisStorage_Ping(t *testing.T) {
	withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
		err := s.Ping(ctx)
		require.NoError(t, err)
	})
}

func TestRedisStorage_Ping_ConnectionFailure(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	storage := NewRedisStorageWithClient(client, "test:auth:")

	// Close the miniredis server to simulate connection failure
	mr.Close()

	err := storage.Ping(context.Background())
	require.Error(t, err)
}
