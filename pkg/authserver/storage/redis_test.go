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
		Session:           session.New("test-subject", "", "", session.UserClaims{}),
	}
}

// newRedisTestRequesterWithExpiration creates a fosite.Request with a real session.Session
// and a specific expiration time for the given token type.
func newRedisTestRequesterWithExpiration(id string, client fosite.Client, tokenType fosite.TokenType, expiresAt time.Time) fosite.Requester {
	sess := session.New("test-subject", "", "", session.UserClaims{})
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
			sess := session.New("user-123", "upstream-session-456", "test-client", session.UserClaims{})
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
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-123", "provider-a", tokens))

			retrieved, err := s.GetUpstreamTokens(ctx, "session-123", "provider-a")
			require.NoError(t, err)
			assert.Equal(t, tokens.AccessToken, retrieved.AccessToken)
			assert.Equal(t, tokens.RefreshToken, retrieved.RefreshToken)
			assert.Equal(t, tokens.UserID, retrieved.UserID)
			assert.Equal(t, tokens.ClientID, retrieved.ClientID)
		})
	})

	t.Run("get non-existent", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			_, err := s.GetUpstreamTokens(ctx, "non-existent", "provider-a")
			requireRedisNotFoundError(t, err)
		})
	})

	t.Run("delete", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "to-delete", "provider-a", &UpstreamTokens{AccessToken: "test", ExpiresAt: time.Now().Add(time.Hour)}))
			require.NoError(t, s.DeleteUpstreamTokens(ctx, "to-delete"))
			_, err := s.GetUpstreamTokens(ctx, "to-delete", "provider-a")
			requireRedisNotFoundError(t, err)
		})
	})

	t.Run("overwrite existing tokens", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session", "provider-a", &UpstreamTokens{AccessToken: "token-1", UserID: "user1", ExpiresAt: time.Now().Add(time.Hour)}))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session", "provider-a", &UpstreamTokens{AccessToken: "token-2", UserID: "user2", ExpiresAt: time.Now().Add(time.Hour)}))

			retrieved, err := s.GetUpstreamTokens(ctx, "session", "provider-a")
			require.NoError(t, err)
			assert.Equal(t, "token-2", retrieved.AccessToken)
			assert.Equal(t, "user2", retrieved.UserID)
		})
	})

	t.Run("get expired tokens returns ErrExpired with token data", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			// Store with an ExpiresAt that's already in the past.
			// The TTL includes DefaultRefreshTokenTTL so the key survives
			// past access token expiry, allowing refresh token retrieval.
			require.NoError(t, s.StoreUpstreamTokens(ctx, "expired", "provider-a", &UpstreamTokens{
				AccessToken:  "expired-token",
				RefreshToken: "refresh-token",
				ExpiresAt:    time.Now().Add(-time.Hour),
			}))

			retrieved, err := s.GetUpstreamTokens(ctx, "expired", "provider-a")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrExpired)
			// Tokens should be returned alongside ErrExpired for refresh purposes
			require.NotNil(t, retrieved)
			assert.Equal(t, "expired-token", retrieved.AccessToken)
			assert.Equal(t, "refresh-token", retrieved.RefreshToken)
		})
	})

	t.Run("zero ExpiresAt tokens are stored without Redis TTL", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
			// Non-expiring tokens (ExpiresAt zero) must be stored without a Redis TTL
			// so they are never automatically evicted.
			require.NoError(t, s.StoreUpstreamTokens(ctx, "no-expiry", "provider-a", &UpstreamTokens{
				AccessToken: "no-expiry-token",
				ProviderID:  "test-provider",
			}))

			retrieved, err := s.GetUpstreamTokens(ctx, "no-expiry", "provider-a")
			require.NoError(t, err)
			require.NotNil(t, retrieved)
			assert.Equal(t, "no-expiry-token", retrieved.AccessToken)
			assert.Equal(t, "test-provider", retrieved.ProviderID)
			assert.True(t, retrieved.ExpiresAt.IsZero())

			// Verify the key has no Redis TTL (miniredis returns 0 for keys without expiry).
			key := redisUpstreamKey(s.keyPrefix, "no-expiry", "provider-a")
			assert.Equal(t, time.Duration(0), mr.TTL(key), "non-expiring token must have no Redis TTL")
		})
	})

	t.Run("mixed-expiry session: non-expiring write removes index set TTL", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
			// First store an expiring token — this sets a TTL on the index set.
			err := s.StoreUpstreamTokens(ctx, "mixed-session", "provider-expiring", &UpstreamTokens{
				AccessToken: "expiring-token",
				ProviderID:  "provider-expiring",
				ExpiresAt:   time.Now().Add(time.Hour),
			})
			require.NoError(t, err)

			// Then store a non-expiring token for the same session.
			err = s.StoreUpstreamTokens(ctx, "mixed-session", "provider-nonexpiring", &UpstreamTokens{
				AccessToken: "non-expiring-token",
				ProviderID:  "provider-nonexpiring",
				// ExpiresAt intentionally zero
			})
			require.NoError(t, err)

			// The index set must now have no TTL (PERSIST removed it).
			idxKey := redisSetKey(s.keyPrefix, KeyTypeUpstreamIdx, "mixed-session")
			assert.Equal(t, time.Duration(0), mr.TTL(idxKey),
				"index set TTL must be removed when a non-expiring token is added to the session")
		})
	})

	t.Run("mixed-expiry session: expiring write after non-expiring keeps index persistent", func(t *testing.T) {
		// Regression test for the inverse ordering of the prior subtest. When a
		// non-expiring token is written first and an expiring token follows, the
		// Lua script must NOT re-apply a TTL to the index set — that would evict
		// the index and orphan the non-expiring token's per-provider key.
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
			// Non-expiring first: index gets PERSIST'd.
			err := s.StoreUpstreamTokens(ctx, "inverse-session", "provider-nonexpiring", &UpstreamTokens{
				AccessToken: "non-expiring-token",
				ProviderID:  "provider-nonexpiring",
				// ExpiresAt intentionally zero
			})
			require.NoError(t, err)

			// Expiring second: must NOT re-apply a TTL.
			err = s.StoreUpstreamTokens(ctx, "inverse-session", "provider-expiring", &UpstreamTokens{
				AccessToken: "expiring-token",
				ProviderID:  "provider-expiring",
				ExpiresAt:   time.Now().Add(time.Hour),
			})
			require.NoError(t, err)

			idxKey := redisSetKey(s.keyPrefix, KeyTypeUpstreamIdx, "inverse-session")
			assert.Equal(t, time.Duration(0), mr.TTL(idxKey),
				"index set TTL must remain unset after an expiring write follows a non-expiring one")

			// Fast-forward past the expiring token's TTL. The expiring per-provider
			// key evicts; the non-expiring one remains; the index stays intact.
			mr.FastForward(time.Hour + DefaultRefreshTokenTTL + time.Second)

			all, err := s.GetAllUpstreamTokens(ctx, "inverse-session")
			require.NoError(t, err)
			require.Contains(t, all, "provider-nonexpiring",
				"non-expiring token must remain reachable through the index")
			assert.NotContains(t, all, "provider-expiring",
				"expiring token must have been evicted by Redis TTL")
		})
	})

	t.Run("fresh expiring write applies index TTL", func(t *testing.T) {
		// Regression guard: the Lua script must apply a TTL on the very first
		// expiring write to a session (where the index set is being created
		// fresh by the SADD). Without this, the index would never expire.
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "fresh-session", "provider-a", &UpstreamTokens{
				AccessToken: "expiring-token",
				ProviderID:  "provider-a",
				ExpiresAt:   time.Now().Add(time.Hour),
			}))

			idxKey := redisSetKey(s.keyPrefix, KeyTypeUpstreamIdx, "fresh-session")
			ttl := mr.TTL(idxKey)
			assert.Greater(t, ttl, time.Duration(0),
				"index set must have a TTL after a fresh expiring write")
		})
	})

	t.Run("longer expiring write after shorter extends index TTL", func(t *testing.T) {
		// Locks down the idxTTL < ttlMs branch: when a member with longer TTL
		// is added, the index TTL must be extended to match.
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "extend-session", "provider-short", &UpstreamTokens{
				AccessToken: "short-token",
				ProviderID:  "provider-short",
				ExpiresAt:   time.Now().Add(15 * time.Minute),
			}))

			idxKey := redisSetKey(s.keyPrefix, KeyTypeUpstreamIdx, "extend-session")
			shortTTL := mr.TTL(idxKey)
			require.Greater(t, shortTTL, time.Duration(0))

			// Add a member with a much longer TTL.
			require.NoError(t, s.StoreUpstreamTokens(ctx, "extend-session", "provider-long", &UpstreamTokens{
				AccessToken: "long-token",
				ProviderID:  "provider-long",
				ExpiresAt:   time.Now().Add(24 * time.Hour),
			}))

			longTTL := mr.TTL(idxKey)
			assert.Greater(t, longTTL, shortTTL,
				"index TTL must be extended when a longer-lived member is added")
		})
	})

	t.Run("shorter expiring write after longer leaves index TTL unchanged", func(t *testing.T) {
		// Locks down the idxTTL >= ttlMs no-op branch: shorter-TTL members must
		// not shrink the index — the longest-lived member governs.
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "noshrink-session", "provider-long", &UpstreamTokens{
				AccessToken: "long-token",
				ProviderID:  "provider-long",
				ExpiresAt:   time.Now().Add(24 * time.Hour),
			}))

			idxKey := redisSetKey(s.keyPrefix, KeyTypeUpstreamIdx, "noshrink-session")
			longTTL := mr.TTL(idxKey)
			require.Greater(t, longTTL, time.Duration(0))

			// Add a member with a shorter TTL.
			require.NoError(t, s.StoreUpstreamTokens(ctx, "noshrink-session", "provider-short", &UpstreamTokens{
				AccessToken: "short-token",
				ProviderID:  "provider-short",
				ExpiresAt:   time.Now().Add(15 * time.Minute),
			}))

			afterTTL := mr.TTL(idxKey)
			// Allow tiny clock-drift tolerance: TTL must not have shrunk meaningfully.
			assert.GreaterOrEqual(t, afterTTL, longTTL-time.Second,
				"index TTL must not shrink when a shorter-lived member is added")
		})
	})

	t.Run("same provider rewrite from non-expiring to expiring keeps PERSIST'd until rewrite", func(t *testing.T) {
		// When the SAME provider rewrites from non-expiring to expiring, the
		// index set is no longer intentionally persistent (the only non-expiring
		// member is gone). With the current "leave PERSIST'd alone" rule, the
		// index stays without a TTL until something else evicts the entry. This
		// is acceptable for now — documented limitation, not a leak in practice
		// because DeleteUpstreamTokens cleans up the whole session.
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "rewrite-session", "provider-a", &UpstreamTokens{
				AccessToken: "non-expiring",
				ProviderID:  "provider-a",
			}))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "rewrite-session", "provider-a", &UpstreamTokens{
				AccessToken: "now-expiring",
				ProviderID:  "provider-a",
				ExpiresAt:   time.Now().Add(time.Hour),
			}))

			idxKey := redisSetKey(s.keyPrefix, KeyTypeUpstreamIdx, "rewrite-session")
			// Pre-existing PERSIST is preserved; we accept this trade-off rather
			// than tracking per-member TTL state in Lua. DeleteUpstreamTokens
			// remains the cleanup path.
			assert.Equal(t, time.Duration(0), mr.TTL(idxKey),
				"index TTL is left alone on same-provider rewrite (acceptable limitation)")

			// The new value is reachable.
			retrieved, err := s.GetUpstreamTokens(ctx, "rewrite-session", "provider-a")
			require.NoError(t, err)
			require.NotNil(t, retrieved)
			assert.Equal(t, "now-expiring", retrieved.AccessToken)
		})
	})

	t.Run("non-expiring token with SessionExpiresAt gets proper Redis TTL", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
			sessionExpiry := time.Now().Add(time.Hour)
			require.NoError(t, s.StoreUpstreamTokens(ctx, "sess-bound", "github", &UpstreamTokens{
				AccessToken:      "pat-token",
				ProviderID:       "github",
				SessionExpiresAt: sessionExpiry,
			}))

			retrieved, err := s.GetUpstreamTokens(ctx, "sess-bound", "github")
			require.NoError(t, err)
			require.NotNil(t, retrieved)
			assert.Equal(t, "pat-token", retrieved.AccessToken)
			assert.True(t, retrieved.ExpiresAt.IsZero(), "ExpiresAt must remain zero for non-expiring token")
			// Assert field survives JSON round-trip (Unix truncation → 1s tolerance).
			// RefreshAndStore carries SessionExpiresAt forward; silent zeroing here
			// would cause every token refresh to lose the session bound.
			assert.WithinDuration(t, sessionExpiry, retrieved.SessionExpiresAt, time.Second,
				"SessionExpiresAt must survive Store→Get round-trip")

			// Fast-forward past SessionExpiresAt + DefaultRefreshTokenTTL
			mr.FastForward(time.Hour + DefaultRefreshTokenTTL + time.Second)

			_, err = s.GetUpstreamTokens(ctx, "sess-bound", "github")
			requireRedisNotFoundError(t, err)
		})
	})

	t.Run("deeply stale ExpiresAt branch clamps TTL to one second", func(t *testing.T) {
		// Regression guard for the clamp introduced in marshalUpstreamTokensWithTTL.
		// Pre-fix, a token whose access expiry + DefaultRefreshTokenTTL had both
		// elapsed was stored with a full 30-day grace (DefaultRefreshTokenTTL),
		// retaining stale tokens far longer than necessary.  The fix clamps to
		// time.Second so deeply-stale rows evict promptly.  Cold-path callers
		// (refresher races, admin rewrites, legacy-row migrations) must observe
		// the 1-second lifetime — not the old 30-day grace — so this test pins
		// the behavior and will fail loudly if the clamp is reverted.
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
			stalePast := time.Now().Add(-(DefaultRefreshTokenTTL + time.Hour))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "stale-expires-session", "provider-a", &UpstreamTokens{
				AccessToken: "stale-token",
				ProviderID:  "provider-a",
				ExpiresAt:   stalePast,
			}))

			key := redisUpstreamKey(s.keyPrefix, "stale-expires-session", "provider-a")
			assert.LessOrEqual(t, mr.TTL(key), 2*time.Second,
				"deeply-stale ExpiresAt token must be stored with a 1s TTL, not the full DefaultRefreshTokenTTL grace")
		})
	})

	t.Run("deeply stale SessionExpiresAt branch clamps TTL to one second", func(t *testing.T) {
		// Mirror of the previous subtest for the SessionExpiresAt branch.
		// When ExpiresAt is zero but SessionExpiresAt + DefaultRefreshTokenTTL
		// have both elapsed, the same clamp must apply so that session-bound
		// non-expiring tokens (e.g. GitHub PATs) don't linger for 30 days after
		// the session itself has long expired.
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
			stalePast := time.Now().Add(-(DefaultRefreshTokenTTL + time.Hour))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "stale-session-expires-session", "github", &UpstreamTokens{
				AccessToken:      "stale-pat",
				ProviderID:       "github",
				SessionExpiresAt: stalePast,
				// ExpiresAt intentionally zero — exercises the SessionExpiresAt branch
			}))

			key := redisUpstreamKey(s.keyPrefix, "stale-session-expires-session", "github")
			assert.LessOrEqual(t, mr.TTL(key), 2*time.Second,
				"deeply-stale SessionExpiresAt token must be stored with a 1s TTL, not the full DefaultRefreshTokenTTL grace")
		})
	})

	t.Run("nil tokens is valid", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-id", "provider-a", nil))
			retrieved, err := s.GetUpstreamTokens(ctx, "session-id", "provider-a")
			require.NoError(t, err)
			assert.Nil(t, retrieved)
		})
	})

	t.Run("multi-provider store and get", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			tokensA := &UpstreamTokens{
				ProviderID:  "github",
				AccessToken: "github-access",
				UserID:      "user-1",
				ExpiresAt:   time.Now().Add(time.Hour),
			}
			tokensB := &UpstreamTokens{
				ProviderID:  "google",
				AccessToken: "google-access",
				UserID:      "user-1",
				ExpiresAt:   time.Now().Add(time.Hour),
			}
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-multi", "github", tokensA))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-multi", "google", tokensB))

			retrievedA, err := s.GetUpstreamTokens(ctx, "session-multi", "github")
			require.NoError(t, err)
			assert.Equal(t, "github-access", retrievedA.AccessToken)

			retrievedB, err := s.GetUpstreamTokens(ctx, "session-multi", "google")
			require.NoError(t, err)
			assert.Equal(t, "google-access", retrievedB.AccessToken)
		})
	})

	t.Run("GetAllUpstreamTokens with two providers", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			tokensA := &UpstreamTokens{
				ProviderID:  "github",
				AccessToken: "github-access",
				UserID:      "user-1",
				ExpiresAt:   time.Now().Add(time.Hour),
			}
			tokensB := &UpstreamTokens{
				ProviderID:  "google",
				AccessToken: "google-access",
				UserID:      "user-1",
				ExpiresAt:   time.Now().Add(time.Hour),
			}
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-all", "github", tokensA))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-all", "google", tokensB))

			allTokens, err := s.GetAllUpstreamTokens(ctx, "session-all")
			require.NoError(t, err)
			require.Len(t, allTokens, 2)

			assert.Equal(t, "github-access", allTokens["github"].AccessToken)
			assert.Equal(t, "google-access", allTokens["google"].AccessToken)
		})
	})

	t.Run("GetAllUpstreamTokens unknown session", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			allTokens, err := s.GetAllUpstreamTokens(ctx, "unknown-session")
			require.NoError(t, err)
			assert.Empty(t, allTokens)
		})
	})

	t.Run("session delete wipes all providers", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-wipe", "github", &UpstreamTokens{
				AccessToken: "gh-token", ExpiresAt: time.Now().Add(time.Hour),
			}))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-wipe", "google", &UpstreamTokens{
				AccessToken: "gl-token", ExpiresAt: time.Now().Add(time.Hour),
			}))

			require.NoError(t, s.DeleteUpstreamTokens(ctx, "session-wipe"))

			_, err := s.GetUpstreamTokens(ctx, "session-wipe", "github")
			requireRedisNotFoundError(t, err)

			_, err = s.GetUpstreamTokens(ctx, "session-wipe", "google")
			requireRedisNotFoundError(t, err)

			allTokens, err := s.GetAllUpstreamTokens(ctx, "session-wipe")
			require.NoError(t, err)
			assert.Empty(t, allTokens)
		})
	})

	t.Run("empty providerName returns error for Store and Get", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			err := s.StoreUpstreamTokens(ctx, "session-ep", "", &UpstreamTokens{AccessToken: "t"})
			require.Error(t, err)
			require.ErrorIs(t, err, fosite.ErrInvalidRequest)

			_, err = s.GetUpstreamTokens(ctx, "session-ep", "")
			require.Error(t, err)
			require.ErrorIs(t, err, fosite.ErrInvalidRequest)
		})
	})

	t.Run("legacy JSON without session_expires_at decodes with zero SessionExpiresAt", func(t *testing.T) {
		// Pin the wire-shape contract: pre-PR Redis data that has no
		// "session_expires_at" key must deserialise to SessionExpiresAt.IsZero().
		// A future DisallowUnknownFields flip or JSON tag rename would break this
		// without failing any other test in the suite.
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
			futureExpiry := time.Now().Add(time.Hour).Unix()
			legacyJSON := fmt.Sprintf(`{
				"provider_id": "github",
				"access_token": "legacy-access",
				"refresh_token": "legacy-refresh",
				"id_token": "legacy-id",
				"expires_at": %d,
				"user_id": "legacy-user-uuid",
				"upstream_subject": "github-sub-123",
				"client_id": "legacy-client"
			}`, futureExpiry)

			// Inject directly into miniredis, bypassing the Store path, to simulate
			// a pre-PR row written without "session_expires_at".
			key := redisUpstreamKey(s.keyPrefix, "legacy-session", "github")
			require.NoError(t, mr.Set(key, legacyJSON))

			retrieved, err := s.GetUpstreamTokens(ctx, "legacy-session", "github")
			require.NoError(t, err)
			require.NotNil(t, retrieved)

			assert.Equal(t, "github", retrieved.ProviderID)
			assert.Equal(t, "legacy-access", retrieved.AccessToken)
			assert.Equal(t, "legacy-refresh", retrieved.RefreshToken)
			assert.Equal(t, "legacy-id", retrieved.IDToken)
			assert.Equal(t, "legacy-user-uuid", retrieved.UserID)
			assert.Equal(t, "github-sub-123", retrieved.UpstreamSubject)
			assert.Equal(t, "legacy-client", retrieved.ClientID)
			assert.Equal(t, time.Unix(futureExpiry, 0), retrieved.ExpiresAt)
			assert.True(t, retrieved.SessionExpiresAt.IsZero(),
				"SessionExpiresAt must be zero when absent from legacy JSON")
		})
	})

}

// --- Bulk Migration Tests ---

func TestRedisStorage_MigrateLegacyUpstreamData(t *testing.T) {
	t.Parallel()

	t.Run("migrates legacy token key to new format", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			// Write directly to the legacy key format (upstream:{sessionID})
			legacyKey := redisKey(s.keyPrefix, KeyTypeUpstream, "legacy-session")
			legacyData := `{"provider_id":"oidc","access_token":"legacy-at","refresh_token":"legacy-rt","expires_at":0,"user_id":"user-1","upstream_subject":"sub-1","client_id":"client-1"}`
			require.NoError(t, s.client.Set(ctx, legacyKey, legacyData, time.Hour).Err())

			require.NoError(t, s.MigrateLegacyUpstreamData(ctx, "default", "oidc"))

			// Legacy key should be deleted
			exists, err := s.client.Exists(ctx, legacyKey).Result()
			require.NoError(t, err)
			assert.Equal(t, int64(0), exists, "legacy key should be deleted after migration")

			// New key should be readable
			tokens, err := s.GetUpstreamTokens(ctx, "legacy-session", "default")
			require.NoError(t, err)
			require.NotNil(t, tokens)
			assert.Equal(t, "legacy-at", tokens.AccessToken)
			assert.Equal(t, "legacy-rt", tokens.RefreshToken)
			assert.Equal(t, "default", tokens.ProviderID)
			assert.Equal(t, "user-1", tokens.UserID)
		})
	})

	t.Run("skips legacy token with logical provider name", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			// Write a token under the legacy key with a logical provider name
			legacyKey := redisKey(s.keyPrefix, KeyTypeUpstream, "logical-session")
			legacyData := `{"provider_id":"some-logical-name","access_token":"logical-at","expires_at":0}`
			require.NoError(t, s.client.Set(ctx, legacyKey, legacyData, time.Hour).Err())

			require.NoError(t, s.MigrateLegacyUpstreamData(ctx, "default", "oidc"))

			// Legacy key should still exist (not migrated)
			exists, err := s.client.Exists(ctx, legacyKey).Result()
			require.NoError(t, err)
			assert.Equal(t, int64(1), exists, "legacy key should not be deleted for non-legacy provider ID")

			// New key should not exist
			_, err = s.GetUpstreamTokens(ctx, "logical-session", "default")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	})

	t.Run("idempotent: skips when new key already exists", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			// Write legacy key
			legacyKey := redisKey(s.keyPrefix, KeyTypeUpstream, "both-keys")
			legacyData := `{"provider_id":"oidc","access_token":"old-token","expires_at":0}`
			require.NoError(t, s.client.Set(ctx, legacyKey, legacyData, time.Hour).Err())

			// Write new-format key
			newTokens := &UpstreamTokens{ProviderID: "default", AccessToken: "new-token", ExpiresAt: time.Now().Add(time.Hour)}
			require.NoError(t, s.StoreUpstreamTokens(ctx, "both-keys", "default", newTokens))

			require.NoError(t, s.MigrateLegacyUpstreamData(ctx, "default", "oidc"))

			// New key should have the original new-format data, not the legacy data
			tokens, err := s.GetUpstreamTokens(ctx, "both-keys", "default")
			require.NoError(t, err)
			assert.Equal(t, "new-token", tokens.AccessToken)
		})
	})

	t.Run("migrates provider identity under new name", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			now := time.Now()
			// Create a user first
			require.NoError(t, s.CreateUser(ctx, &User{ID: "user-migrate", CreatedAt: now, UpdatedAt: now}))

			// Create identity under legacy provider ID
			legacyIdentity := &ProviderIdentity{
				UserID: "user-migrate", ProviderID: "oidc", ProviderSubject: "sub-123",
				LinkedAt: now, LastUsedAt: now,
			}
			require.NoError(t, s.CreateProviderIdentity(ctx, legacyIdentity))

			require.NoError(t, s.MigrateLegacyUpstreamData(ctx, "my-upstream", "oidc"))

			// Identity should now be findable under the new provider name
			newIdentity, err := s.GetProviderIdentity(ctx, "my-upstream", "sub-123")
			require.NoError(t, err)
			assert.Equal(t, "user-migrate", newIdentity.UserID)
			assert.Equal(t, "my-upstream", newIdentity.ProviderID)

			// Legacy identity should still exist (not deleted for safe rollback)
			legacyRetrieved, err := s.GetProviderIdentity(ctx, "oidc", "sub-123")
			require.NoError(t, err)
			assert.Equal(t, "user-migrate", legacyRetrieved.UserID)
		})
	})

	t.Run("skips provider identity migration when names match", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			now := time.Now()
			require.NoError(t, s.CreateUser(ctx, &User{ID: "user-same", CreatedAt: now, UpdatedAt: now}))

			// Create identity where legacy ID equals provider name
			identity := &ProviderIdentity{
				UserID: "user-same", ProviderID: "oidc", ProviderSubject: "sub-same",
				LinkedAt: now, LastUsedAt: now,
			}
			require.NoError(t, s.CreateProviderIdentity(ctx, identity))

			// When legacyProviderID == providerName, nothing should be migrated
			require.NoError(t, s.MigrateLegacyUpstreamData(ctx, "oidc", "oidc"))

			// Only the original identity should exist
			retrieved, err := s.GetProviderIdentity(ctx, "oidc", "sub-same")
			require.NoError(t, err)
			assert.Equal(t, "user-same", retrieved.UserID)
		})
	})

	t.Run("no-op on empty database", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			require.NoError(t, s.MigrateLegacyUpstreamData(ctx, "default", "oidc"))
		})
	})

	t.Run("migrates multiple legacy token keys", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			// Write multiple legacy keys
			for i := 0; i < 3; i++ {
				legacyKey := redisKey(s.keyPrefix, KeyTypeUpstream, fmt.Sprintf("session-%d", i))
				legacyData := fmt.Sprintf(`{"provider_id":"oidc","access_token":"at-%d","refresh_token":"rt-%d","expires_at":0,"user_id":"user-%d"}`, i, i, i)
				require.NoError(t, s.client.Set(ctx, legacyKey, legacyData, time.Hour).Err())
			}

			require.NoError(t, s.MigrateLegacyUpstreamData(ctx, "default", "oidc"))

			// All should be readable under new format
			for i := 0; i < 3; i++ {
				tokens, err := s.GetUpstreamTokens(ctx, fmt.Sprintf("session-%d", i), "default")
				require.NoError(t, err)
				assert.Equal(t, fmt.Sprintf("at-%d", i), tokens.AccessToken)
				assert.Equal(t, "default", tokens.ProviderID)
			}
		})
	})

	t.Run("does not touch new-format keys during scan", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			// Write a new-format key directly
			newTokens := &UpstreamTokens{
				ProviderID: "default", AccessToken: "new-at", ExpiresAt: time.Now().Add(time.Hour),
			}
			require.NoError(t, s.StoreUpstreamTokens(ctx, "new-session", "default", newTokens))

			require.NoError(t, s.MigrateLegacyUpstreamData(ctx, "default", "oidc"))

			// New key should be unchanged
			tokens, err := s.GetUpstreamTokens(ctx, "new-session", "default")
			require.NoError(t, err)
			assert.Equal(t, "new-at", tokens.AccessToken)
		})
	})

	t.Run("DeleteUser removes migrated upstream tokens", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			// Create a user so DeleteUser has something to delete.
			userID := "migrate-delete-user"
			require.NoError(t, s.CreateUser(ctx, &User{
				ID: userID, CreatedAt: time.Now(), UpdatedAt: time.Now(),
			}))

			// Write a legacy token key with that user's ID.
			legacyKey := redisKey(s.keyPrefix, KeyTypeUpstream, "del-session")
			legacyData := fmt.Sprintf(
				`{"provider_id":"oidc","access_token":"del-at","refresh_token":"del-rt","expires_at":0,"user_id":"%s","upstream_subject":"sub-del","client_id":"client-del"}`,
				userID,
			)
			require.NoError(t, s.client.Set(ctx, legacyKey, legacyData, time.Hour).Err())

			// Migrate — this should populate user:upstream:{userID} with the new key.
			require.NoError(t, s.MigrateLegacyUpstreamData(ctx, "default", "oidc"))

			// Sanity: token is reachable under the new key.
			tokens, err := s.GetUpstreamTokens(ctx, "del-session", "default")
			require.NoError(t, err)
			assert.Equal(t, "del-at", tokens.AccessToken)

			// Delete the user — cascade should remove the migrated upstream token.
			require.NoError(t, s.DeleteUser(ctx, userID))

			// The upstream token must be gone.
			_, err = s.GetUpstreamTokens(ctx, "del-session", "default")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotFound, "migrated token should be removed by DeleteUser cascade")
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
		require.NoError(t, s.StoreUpstreamTokens(ctx, "session-user", "provider-a", userTokens))
		require.NoError(t, s.StoreUpstreamTokens(ctx, "session-other", "provider-a", otherTokens))

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
		_, err = s.GetUpstreamTokens(ctx, "session-user", "provider-a")
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
		otherRetrieved, err := s.GetUpstreamTokens(ctx, "session-other", "provider-a")
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
			return s.StoreUpstreamTokens(ctx, "", "provider-a", &UpstreamTokens{AccessToken: "t"})
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

func TestRedisStorage_Health(t *testing.T) {
	withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
		err := s.Health(ctx)
		require.NoError(t, err)
	})
}

func TestRedisStorage_Health_ConnectionFailure(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	storage := NewRedisStorageWithClient(client, "test:auth:")

	// Close the miniredis server to simulate connection failure
	mr.Close()

	err := storage.Health(context.Background())
	require.Error(t, err)
}

// --- GetLatestUpstreamTokensForUser Tests ---

func TestRedisStorage_GetLatestUpstreamTokensForUser(t *testing.T) {
	t.Parallel()

	t.Run("no_match", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			_, err := s.GetLatestUpstreamTokensForUser(ctx, "user-A", "prov-X")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	})

	t.Run("one_match_returns_record", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-1", "prov-X", &UpstreamTokens{
				ProviderID:   "prov-X",
				UserID:       "user-A",
				RefreshToken: "rt-1",
				ExpiresAt:    time.Now().Add(time.Hour),
			}))

			got, err := s.GetLatestUpstreamTokensForUser(ctx, "user-A", "prov-X")
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, "rt-1", got.RefreshToken)
		})
	})

	t.Run("multiple_sessions_pick_latest_expires_at", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			now := time.Now()
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-1", "prov-X", &UpstreamTokens{
				ProviderID:   "prov-X",
				UserID:       "user-A",
				RefreshToken: "rt-1h",
				ExpiresAt:    now.Add(time.Hour),
			}))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-2", "prov-X", &UpstreamTokens{
				ProviderID:   "prov-X",
				UserID:       "user-A",
				RefreshToken: "rt-2h",
				ExpiresAt:    now.Add(2 * time.Hour),
			}))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-3", "prov-X", &UpstreamTokens{
				ProviderID:   "prov-X",
				UserID:       "user-A",
				RefreshToken: "rt-3h",
				ExpiresAt:    now.Add(3 * time.Hour),
			}))

			got, err := s.GetLatestUpstreamTokensForUser(ctx, "user-A", "prov-X")
			require.NoError(t, err)
			assert.Equal(t, "rt-3h", got.RefreshToken)
		})
	})

	t.Run("different_user_not_matched", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			// This case exits via the empty-SMEMBERS short-circuit (no user-upstream
			// index for the queried userID), not the per-row ProviderID filter. The
			// different_provider_not_matched case below exercises the row-level filter.
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-1", "prov-X", &UpstreamTokens{
				ProviderID: "prov-X",
				UserID:     "user-B",
				ExpiresAt:  time.Now().Add(time.Hour),
			}))

			_, err := s.GetLatestUpstreamTokensForUser(ctx, "user-A", "prov-X")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	})

	t.Run("different_provider_not_matched", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-1", "prov-Y", &UpstreamTokens{
				ProviderID: "prov-Y",
				UserID:     "user-A",
				ExpiresAt:  time.Now().Add(time.Hour),
			}))

			_, err := s.GetLatestUpstreamTokensForUser(ctx, "user-A", "prov-X")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	})

	t.Run("tolerate_access_token_expired", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			// ExpiresAt is 1 minute in the past. TTL = time.Until(-1min) + DefaultRefreshTokenTTL
			// which is still large and positive, so Redis keeps the key.
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-1", "prov-X", &UpstreamTokens{
				ProviderID:   "prov-X",
				UserID:       "user-A",
				RefreshToken: "rt-expired-at",
				ExpiresAt:    time.Now().Add(-time.Minute),
			}))

			got, err := s.GetLatestUpstreamTokensForUser(ctx, "user-A", "prov-X")
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, "rt-expired-at", got.RefreshToken)
		})
	})

	t.Run("zero_expires_at_loses_to_nonzero", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-zero", "prov-X", &UpstreamTokens{
				ProviderID:   "prov-X",
				UserID:       "user-A",
				RefreshToken: "rt-zero",
				ExpiresAt:    time.Time{},
			}))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-nonzero", "prov-X", &UpstreamTokens{
				ProviderID:   "prov-X",
				UserID:       "user-A",
				RefreshToken: "rt-nonzero",
				ExpiresAt:    time.Now().Add(time.Hour),
			}))

			got, err := s.GetLatestUpstreamTokensForUser(ctx, "user-A", "prov-X")
			require.NoError(t, err)
			assert.Equal(t, "rt-nonzero", got.RefreshToken)
		})
	})

	t.Run("two_zero_expires_at_rows", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			// Both rows have zero ExpiresAt. The winner is whichever is encountered
			// first during iteration — iteration order over Redis set members is
			// non-deterministic, so we assert only that one of them is returned.
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-zero-1", "prov-X", &UpstreamTokens{
				ProviderID:   "prov-X",
				UserID:       "user-A",
				RefreshToken: "rt-zero-1",
				ExpiresAt:    time.Time{},
			}))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-zero-2", "prov-X", &UpstreamTokens{
				ProviderID:   "prov-X",
				UserID:       "user-A",
				RefreshToken: "rt-zero-2",
				ExpiresAt:    time.Time{},
			}))

			got, err := s.GetLatestUpstreamTokensForUser(ctx, "user-A", "prov-X")
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Contains(t, []string{"rt-zero-1", "rt-zero-2"}, got.RefreshToken)
		})
	})

	// stale_index_entry is Redis-specific: a SMEMBER entry pointing at a key that
	// was never written (simulating a TTL-evicted per-provider key). The real row
	// must still be returned; the dangling member must not cause an error.
	t.Run("stale_index_entry", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, mr *miniredis.Miniredis) {
			// Store a real row for (user-A, prov-X).
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-real", "prov-X", &UpstreamTokens{
				ProviderID:   "prov-X",
				UserID:       "user-A",
				RefreshToken: "rt-real",
				ExpiresAt:    time.Now().Add(time.Hour),
			}))

			// Inject a dangling member directly into the user reverse-index set.
			// The key "test:auth:upstream:phantom-session:prov-X" was never written.
			setKey := redisSetKey("test:auth:", KeyTypeUserUpstream, "user-A")
			phantomKey := redisUpstreamKey("test:auth:", "phantom-session", "prov-X")
			mr.SAdd(setKey, phantomKey)

			got, err := s.GetLatestUpstreamTokensForUser(ctx, "user-A", "prov-X")
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, "rt-real", got.RefreshToken)
		})
	})

	t.Run("returns_all_fields_round_trip", func(t *testing.T) {
		withRedisStorage(t, func(ctx context.Context, s *RedisStorage, _ *miniredis.Miniredis) {
			// Truncate to second precision: Redis stores time as int64 unix seconds.
			now := time.Now().Truncate(time.Second)
			fixture := UpstreamTokens{
				ProviderID:       "prov-X",
				AccessToken:      "access-tok",
				RefreshToken:     "refresh-tok",
				IDToken:          "id-tok",
				ExpiresAt:        now.Add(time.Hour),
				SessionExpiresAt: now.Add(2 * time.Hour),
				UserID:           "user-A",
				UpstreamSubject:  "sub-upstream",
				ClientID:         "client-1",
			}

			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-rt", "prov-X", &fixture))

			got, err := s.GetLatestUpstreamTokensForUser(ctx, "user-A", "prov-X")
			require.NoError(t, err)
			require.NotNil(t, got)
			require.Equal(t, fixture, *got)
		})
	})
}
