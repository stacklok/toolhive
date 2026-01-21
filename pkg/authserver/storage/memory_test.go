// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Tests use the withStorage helper which calls t.Parallel() internally,
// making all subtests parallel despite not having explicit t.Parallel() calls.
//
//nolint:paralleltest // parallel execution handled by withStorage helper
package storage

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock Types ---

type mockSession struct {
	subject   string
	expiresAt map[fosite.TokenType]time.Time
}

func newMockSession() *mockSession {
	return &mockSession{subject: "test-subject", expiresAt: make(map[fosite.TokenType]time.Time)}
}

func (s *mockSession) SetExpiresAt(key fosite.TokenType, exp time.Time) { s.expiresAt[key] = exp }
func (s *mockSession) GetExpiresAt(key fosite.TokenType) time.Time      { return s.expiresAt[key] }
func (*mockSession) GetUsername() string                                { return "" }
func (s *mockSession) GetSubject() string                               { return s.subject }
func (s *mockSession) Clone() fosite.Session {
	clone := &mockSession{subject: s.subject, expiresAt: make(map[fosite.TokenType]time.Time)}
	for k, v := range s.expiresAt {
		clone.expiresAt[k] = v
	}
	return clone
}

type mockClient struct {
	id            string
	secret        []byte
	redirectURIs  []string
	grantTypes    []string
	responseTypes []string
	scopes        []string
	public        bool
}

func (c *mockClient) GetID() string                      { return c.id }
func (c *mockClient) GetHashedSecret() []byte            { return c.secret }
func (c *mockClient) GetRedirectURIs() []string          { return c.redirectURIs }
func (c *mockClient) GetGrantTypes() fosite.Arguments    { return c.grantTypes }
func (c *mockClient) GetResponseTypes() fosite.Arguments { return c.responseTypes }
func (c *mockClient) GetScopes() fosite.Arguments        { return c.scopes }
func (c *mockClient) IsPublic() bool                     { return c.public }
func (*mockClient) GetAudience() fosite.Arguments        { return nil }

type mockRequester struct {
	id                string
	requestedAt       time.Time
	client            fosite.Client
	requestedScopes   fosite.Arguments
	requestedAudience fosite.Arguments
	grantedScopes     fosite.Arguments
	grantedAudience   fosite.Arguments
	form              url.Values
	session           fosite.Session
}

func newMockRequester(id string, client fosite.Client) *mockRequester {
	return &mockRequester{
		id: id, requestedAt: time.Now(), client: client,
		requestedScopes: fosite.Arguments{"openid", "profile"}, grantedScopes: fosite.Arguments{"openid"},
		requestedAudience: fosite.Arguments{}, grantedAudience: fosite.Arguments{},
		form: make(url.Values), session: newMockSession(),
	}
}

func newMockRequesterWithExpiration(id string, client fosite.Client, tokenType fosite.TokenType, expiresAt time.Time) *mockRequester {
	session := newMockSession()
	session.SetExpiresAt(tokenType, expiresAt)
	return &mockRequester{
		id: id, requestedAt: time.Now(), client: client,
		requestedScopes: fosite.Arguments{"openid", "profile"}, grantedScopes: fosite.Arguments{"openid"},
		requestedAudience: fosite.Arguments{}, grantedAudience: fosite.Arguments{},
		form: make(url.Values), session: session,
	}
}

func (r *mockRequester) SetID(id string)                           { r.id = id }
func (r *mockRequester) GetID() string                             { return r.id }
func (r *mockRequester) GetRequestedAt() time.Time                 { return r.requestedAt }
func (r *mockRequester) GetClient() fosite.Client                  { return r.client }
func (r *mockRequester) GetRequestedScopes() fosite.Arguments      { return r.requestedScopes }
func (r *mockRequester) GetRequestedAudience() fosite.Arguments    { return r.requestedAudience }
func (r *mockRequester) SetRequestedScopes(s fosite.Arguments)     { r.requestedScopes = s }
func (r *mockRequester) SetRequestedAudience(aud fosite.Arguments) { r.requestedAudience = aud }
func (r *mockRequester) AppendRequestedScope(scope string) {
	r.requestedScopes = append(r.requestedScopes, scope)
}
func (r *mockRequester) GetGrantedScopes() fosite.Arguments   { return r.grantedScopes }
func (r *mockRequester) GetGrantedAudience() fosite.Arguments { return r.grantedAudience }
func (r *mockRequester) GrantScope(scope string)              { r.grantedScopes = append(r.grantedScopes, scope) }
func (r *mockRequester) GrantAudience(aud string)             { r.grantedAudience = append(r.grantedAudience, aud) }
func (r *mockRequester) GetSession() fosite.Session           { return r.session }
func (r *mockRequester) SetSession(s fosite.Session)          { r.session = s }
func (r *mockRequester) GetRequestForm() url.Values           { return r.form }
func (*mockRequester) Merge(_ fosite.Requester)               {}
func (r *mockRequester) Sanitize(_ []string) fosite.Requester { return r }

// --- Test Helpers ---

func withStorage(t *testing.T, fn func(context.Context, *MemoryStorage)) {
	t.Helper()
	t.Parallel()
	storage := NewMemoryStorage()
	defer storage.Close()
	fn(context.Background(), storage)
}

func requireNotFoundError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound, "should match storage.ErrNotFound")
	assert.ErrorIs(t, err, fosite.ErrNotFound, "should match fosite.ErrNotFound")
}

func testClient() *mockClient { return &mockClient{id: "test-client"} }

// --- Basic Tests ---

func TestNewMemoryStorage(t *testing.T) {
	t.Parallel()
	storage := NewMemoryStorage()
	defer storage.Close()

	require.NotNil(t, storage)
	assert.NotNil(t, storage.clients)
	assert.NotNil(t, storage.authCodes)
	assert.NotNil(t, storage.accessTokens)
	assert.NotNil(t, storage.refreshTokens)
	assert.NotNil(t, storage.pkceRequests)
	assert.NotNil(t, storage.upstreamTokens)
	assert.NotNil(t, storage.invalidatedCodes)
	assert.NotNil(t, storage.clientAssertionJWTs)
	assert.Equal(t, DefaultCleanupInterval, storage.cleanupInterval)
}

func TestNewMemoryStorage_WithCleanupInterval(t *testing.T) {
	t.Parallel()
	customInterval := 1 * time.Minute
	storage := NewMemoryStorage(WithCleanupInterval(customInterval))
	defer storage.Close()
	assert.Equal(t, customInterval, storage.cleanupInterval)
}

func TestMemoryStorage_ImplementsStorage(t *testing.T) {
	t.Parallel()
	var _ Storage = (*MemoryStorage)(nil)
}

// --- Client Tests ---

func TestMemoryStorage_Client(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		clientID string
		setup    func(*MemoryStorage)
		wantErr  bool
	}{
		{"existing client", "test-client", func(s *MemoryStorage) {
			_ = s.RegisterClient(context.Background(), &mockClient{id: "test-client"})
		}, false},
		{"non-existent client", "non-existent", func(_ *MemoryStorage) {}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withStorage(t, func(ctx context.Context, s *MemoryStorage) {
				tt.setup(s)
				client, err := s.GetClient(ctx, tt.clientID)
				if tt.wantErr {
					requireNotFoundError(t, err)
					assert.Nil(t, client)
				} else {
					require.NoError(t, err)
					assert.Equal(t, tt.clientID, client.GetID())
				}
			})
		})
	}
}

func TestMemoryStorage_RegisterClient(t *testing.T) {
	withStorage(t, func(ctx context.Context, s *MemoryStorage) {
		client := &mockClient{id: "test-client"}
		require.NoError(t, s.RegisterClient(ctx, client))
		retrieved, err := s.GetClient(ctx, "test-client")
		require.NoError(t, err)
		assert.Equal(t, client, retrieved)
	})
}

func TestMemoryStorage_ClientAssertionJWT(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		setup   func(context.Context, *MemoryStorage)
		jti     string
		wantErr error
	}{
		{"unknown JTI is valid", nil, "unknown-jti", nil},
		{"known JTI is invalid", func(ctx context.Context, s *MemoryStorage) {
			_ = s.SetClientAssertionJWT(ctx, "test-jti", time.Now().Add(time.Hour))
		}, "test-jti", fosite.ErrJTIKnown},
		{"expired JTI is valid", func(ctx context.Context, s *MemoryStorage) {
			_ = s.SetClientAssertionJWT(ctx, "expired-jti", time.Now().Add(-time.Hour))
		}, "expired-jti", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withStorage(t, func(ctx context.Context, s *MemoryStorage) {
				if tt.setup != nil {
					tt.setup(ctx, s)
				}
				err := s.ClientAssertionJWTValid(ctx, tt.jti)
				if tt.wantErr != nil {
					assert.ErrorIs(t, err, tt.wantErr)
				} else {
					require.NoError(t, err)
				}
			})
		})
	}

	t.Run("cleanup expired JTIs on set", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			s.mu.Lock()
			s.clientAssertionJWTs["old-jti"] = time.Now().Add(-time.Hour)
			s.mu.Unlock()

			require.NoError(t, s.SetClientAssertionJWT(ctx, "new-jti", time.Now().Add(time.Hour)))

			s.mu.RLock()
			_, exists := s.clientAssertionJWTs["old-jti"]
			s.mu.RUnlock()
			assert.False(t, exists, "expired JTI should have been cleaned up")
		})
	})
}

// --- Generic Token Session Tests ---

type tokenSessionOps struct {
	name      string
	tokenType fosite.TokenType
	create    func(context.Context, *MemoryStorage, string, fosite.Requester) error
	get       func(context.Context, *MemoryStorage, string) (fosite.Requester, error)
	delete    func(context.Context, *MemoryStorage, string) error
}

func getTokenSessionTestCases() []tokenSessionOps {
	return []tokenSessionOps{
		{
			name:      "AuthorizeCode",
			tokenType: fosite.AuthorizeCode,
			create: func(ctx context.Context, s *MemoryStorage, sig string, r fosite.Requester) error {
				return s.CreateAuthorizeCodeSession(ctx, sig, r)
			},
			get: func(ctx context.Context, s *MemoryStorage, sig string) (fosite.Requester, error) {
				return s.GetAuthorizeCodeSession(ctx, sig, nil)
			},
			delete: nil, // AuthorizeCode uses invalidation, not deletion
		},
		{
			name:      "AccessToken",
			tokenType: fosite.AccessToken,
			create: func(ctx context.Context, s *MemoryStorage, sig string, r fosite.Requester) error {
				return s.CreateAccessTokenSession(ctx, sig, r)
			},
			get: func(ctx context.Context, s *MemoryStorage, sig string) (fosite.Requester, error) {
				return s.GetAccessTokenSession(ctx, sig, nil)
			},
			delete: func(ctx context.Context, s *MemoryStorage, sig string) error {
				return s.DeleteAccessTokenSession(ctx, sig)
			},
		},
		{
			name:      "RefreshToken",
			tokenType: fosite.RefreshToken,
			create: func(ctx context.Context, s *MemoryStorage, sig string, r fosite.Requester) error {
				return s.CreateRefreshTokenSession(ctx, sig, "access-sig", r)
			},
			get: func(ctx context.Context, s *MemoryStorage, sig string) (fosite.Requester, error) {
				return s.GetRefreshTokenSession(ctx, sig, nil)
			},
			delete: func(ctx context.Context, s *MemoryStorage, sig string) error {
				return s.DeleteRefreshTokenSession(ctx, sig)
			},
		},
		{
			name:      "PKCE",
			tokenType: fosite.AuthorizeCode, // PKCE uses authorize code expiration
			create: func(ctx context.Context, s *MemoryStorage, sig string, r fosite.Requester) error {
				return s.CreatePKCERequestSession(ctx, sig, r)
			},
			get: func(ctx context.Context, s *MemoryStorage, sig string) (fosite.Requester, error) {
				return s.GetPKCERequestSession(ctx, sig, nil)
			},
			delete: func(ctx context.Context, s *MemoryStorage, sig string) error {
				return s.DeletePKCERequestSession(ctx, sig)
			},
		},
	}
}

func TestMemoryStorage_TokenSessions(t *testing.T) {
	t.Parallel()

	for _, tc := range getTokenSessionTestCases() {
		t.Run(tc.name+"/create and get", func(t *testing.T) {
			withStorage(t, func(ctx context.Context, s *MemoryStorage) {
				request := newMockRequester("req-1", testClient())
				require.NoError(t, tc.create(ctx, s, "sig-123", request))
				retrieved, err := tc.get(ctx, s, "sig-123")
				require.NoError(t, err)
				assert.Equal(t, request.GetID(), retrieved.GetID())
			})
		})

		t.Run(tc.name+"/get non-existent", func(t *testing.T) {
			withStorage(t, func(ctx context.Context, s *MemoryStorage) {
				_, err := tc.get(ctx, s, "non-existent")
				requireNotFoundError(t, err)
			})
		})

		if tc.delete != nil {
			t.Run(tc.name+"/delete", func(t *testing.T) {
				withStorage(t, func(ctx context.Context, s *MemoryStorage) {
					request := newMockRequester("req-1", testClient())
					require.NoError(t, tc.create(ctx, s, "to-delete", request))
					require.NoError(t, tc.delete(ctx, s, "to-delete"))
					_, err := tc.get(ctx, s, "to-delete")
					requireNotFoundError(t, err)
				})
			})
		}
	}
}

func TestMemoryStorage_AuthorizeCode_Invalidation(t *testing.T) {
	t.Parallel()
	t.Run("invalidate code", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			request := newMockRequester("req-1", testClient())
			require.NoError(t, s.CreateAuthorizeCodeSession(ctx, "code-123", request))
			require.NoError(t, s.InvalidateAuthorizeCodeSession(ctx, "code-123"))

			retrieved, err := s.GetAuthorizeCodeSession(ctx, "code-123", nil)
			require.Error(t, err)
			assert.ErrorIs(t, err, fosite.ErrInvalidatedAuthorizeCode)
			assert.NotNil(t, retrieved, "must return request with invalidated error")
		})
	})

	t.Run("invalidate non-existent code", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			err := s.InvalidateAuthorizeCodeSession(ctx, "non-existent")
			requireNotFoundError(t, err)
		})
	})
}

func TestMemoryStorage_AccessToken_DeleteNonExistent(t *testing.T) {
	withStorage(t, func(ctx context.Context, s *MemoryStorage) {
		err := s.DeleteAccessTokenSession(ctx, "non-existent")
		requireNotFoundError(t, err)
	})
}

func TestMemoryStorage_RotateRefreshToken(t *testing.T) {
	t.Parallel()
	t.Run("rotate deletes refresh and access tokens", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			client := testClient()
			request := newMockRequester("request-123", client)

			require.NoError(t, s.CreateRefreshTokenSession(ctx, "refresh-sig", "access-sig", request))
			require.NoError(t, s.CreateAccessTokenSession(ctx, "access-sig", request))
			require.NoError(t, s.RotateRefreshToken(ctx, "request-123", "refresh-sig"))

			_, err := s.GetRefreshTokenSession(ctx, "refresh-sig", nil)
			requireNotFoundError(t, err)
			_, err = s.GetAccessTokenSession(ctx, "access-sig", nil)
			requireNotFoundError(t, err)
		})
	})

	t.Run("rotate non-existent token (no error)", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			require.NoError(t, s.RotateRefreshToken(ctx, "non-existent", "non-existent"))
		})
	})
}

// --- Upstream Token Tests ---

func TestMemoryStorage_UpstreamTokens(t *testing.T) {
	t.Parallel()
	t.Run("store and get", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			tokens := &UpstreamTokens{
				AccessToken: "upstream-access", RefreshToken: "upstream-refresh", IDToken: "upstream-id",
				ExpiresAt: time.Now().Add(time.Hour), UserID: "user-123", ClientID: "test-client-id",
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
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			_, err := s.GetUpstreamTokens(ctx, "non-existent")
			requireNotFoundError(t, err)
		})
	})

	t.Run("delete", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "to-delete", &UpstreamTokens{AccessToken: "test"}))
			require.NoError(t, s.DeleteUpstreamTokens(ctx, "to-delete"))
			_, err := s.GetUpstreamTokens(ctx, "to-delete")
			requireNotFoundError(t, err)
		})
	})

	t.Run("overwrite existing tokens", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session", &UpstreamTokens{AccessToken: "token-1", UserID: "user1"}))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session", &UpstreamTokens{AccessToken: "token-2", UserID: "user2"}))

			retrieved, err := s.GetUpstreamTokens(ctx, "session")
			require.NoError(t, err)
			assert.Equal(t, "token-2", retrieved.AccessToken)
			assert.Equal(t, "user2", retrieved.UserID)
		})
	})

	t.Run("get expired tokens returns ErrExpired", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "expired", &UpstreamTokens{
				AccessToken: "expired-token", ExpiresAt: time.Now().Add(-time.Hour),
			}))
			assert.Equal(t, 1, s.Stats().UpstreamTokens)

			retrieved, err := s.GetUpstreamTokens(ctx, "expired")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrExpired)
			assert.Nil(t, retrieved)
		})
	})
}

// --- Pending Authorization Tests ---

func TestMemoryStorage_PendingAuthorization(t *testing.T) {
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
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
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
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			_, err := s.LoadPendingAuthorization(ctx, "non-existent")
			requireNotFoundError(t, err)
		})
	})

	t.Run("load expired returns ErrExpired", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			require.NoError(t, s.StorePendingAuthorization(ctx, "expired-state", makePending("expired-state")))

			s.mu.Lock()
			if entry, ok := s.pendingAuthorizations["expired-state"]; ok {
				entry.expiresAt = time.Now().Add(-time.Hour)
			}
			s.mu.Unlock()

			retrieved, err := s.LoadPendingAuthorization(ctx, "expired-state")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrExpired)
			assert.Nil(t, retrieved)
		})
	})

	t.Run("delete", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			require.NoError(t, s.StorePendingAuthorization(ctx, "to-delete", makePending("to-delete")))
			require.NoError(t, s.DeletePendingAuthorization(ctx, "to-delete"))
			_, err := s.LoadPendingAuthorization(ctx, "to-delete")
			requireNotFoundError(t, err)
		})
	})

	t.Run("delete non-existent returns error", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			err := s.DeletePendingAuthorization(ctx, "non-existent")
			requireNotFoundError(t, err)
		})
	})
}

// --- Cleanup Tests ---

func TestMemoryStorage_CleanupExpired(t *testing.T) {
	t.Parallel()

	type cleanupTest struct {
		name       string
		setup      func(context.Context, *MemoryStorage)
		getStats   func(Stats) int
		verifyGone func(context.Context, *MemoryStorage) error
		verifyKeep func(context.Context, *MemoryStorage) error
	}

	client := testClient()
	tests := []cleanupTest{
		{
			name: "auth codes",
			setup: func(ctx context.Context, s *MemoryStorage) {
				_ = s.CreateAuthorizeCodeSession(ctx, "expired", newMockRequesterWithExpiration("exp", client, fosite.AuthorizeCode, time.Now().Add(-time.Hour)))
				_ = s.CreateAuthorizeCodeSession(ctx, "valid", newMockRequesterWithExpiration("val", client, fosite.AuthorizeCode, time.Now().Add(time.Hour)))
			},
			getStats: func(st Stats) int { return st.AuthCodes },
			verifyGone: func(ctx context.Context, s *MemoryStorage) error {
				_, err := s.GetAuthorizeCodeSession(ctx, "expired", nil)
				return err
			},
			verifyKeep: func(ctx context.Context, s *MemoryStorage) error {
				_, err := s.GetAuthorizeCodeSession(ctx, "valid", nil)
				return err
			},
		},
		{
			name: "access tokens",
			setup: func(ctx context.Context, s *MemoryStorage) {
				_ = s.CreateAccessTokenSession(ctx, "expired", newMockRequesterWithExpiration("exp", client, fosite.AccessToken, time.Now().Add(-time.Hour)))
				_ = s.CreateAccessTokenSession(ctx, "valid", newMockRequesterWithExpiration("val", client, fosite.AccessToken, time.Now().Add(time.Hour)))
			},
			getStats: func(st Stats) int { return st.AccessTokens },
			verifyGone: func(ctx context.Context, s *MemoryStorage) error {
				_, err := s.GetAccessTokenSession(ctx, "expired", nil)
				return err
			},
			verifyKeep: func(ctx context.Context, s *MemoryStorage) error {
				_, err := s.GetAccessTokenSession(ctx, "valid", nil)
				return err
			},
		},
		{
			name: "refresh tokens",
			setup: func(ctx context.Context, s *MemoryStorage) {
				_ = s.CreateRefreshTokenSession(ctx, "expired", "a", newMockRequesterWithExpiration("exp", client, fosite.RefreshToken, time.Now().Add(-time.Hour)))
				_ = s.CreateRefreshTokenSession(ctx, "valid", "a", newMockRequesterWithExpiration("val", client, fosite.RefreshToken, time.Now().Add(time.Hour)))
			},
			getStats: func(st Stats) int { return st.RefreshTokens },
			verifyGone: func(ctx context.Context, s *MemoryStorage) error {
				_, err := s.GetRefreshTokenSession(ctx, "expired", nil)
				return err
			},
			verifyKeep: func(ctx context.Context, s *MemoryStorage) error {
				_, err := s.GetRefreshTokenSession(ctx, "valid", nil)
				return err
			},
		},
		{
			name: "PKCE requests",
			setup: func(ctx context.Context, s *MemoryStorage) {
				_ = s.CreatePKCERequestSession(ctx, "expired", newMockRequesterWithExpiration("exp", client, fosite.AuthorizeCode, time.Now().Add(-time.Hour)))
				_ = s.CreatePKCERequestSession(ctx, "valid", newMockRequesterWithExpiration("val", client, fosite.AuthorizeCode, time.Now().Add(time.Hour)))
			},
			getStats: func(st Stats) int { return st.PKCERequests },
			verifyGone: func(ctx context.Context, s *MemoryStorage) error {
				_, err := s.GetPKCERequestSession(ctx, "expired", nil)
				return err
			},
			verifyKeep: func(ctx context.Context, s *MemoryStorage) error {
				_, err := s.GetPKCERequestSession(ctx, "valid", nil)
				return err
			},
		},
		{
			name: "upstream tokens",
			setup: func(ctx context.Context, s *MemoryStorage) {
				_ = s.StoreUpstreamTokens(ctx, "expired", &UpstreamTokens{AccessToken: "exp", ExpiresAt: time.Now().Add(-time.Hour)})
				_ = s.StoreUpstreamTokens(ctx, "valid", &UpstreamTokens{AccessToken: "val", ExpiresAt: time.Now().Add(time.Hour)})
			},
			getStats: func(st Stats) int { return st.UpstreamTokens },
			verifyGone: func(ctx context.Context, s *MemoryStorage) error {
				_, err := s.GetUpstreamTokens(ctx, "expired")
				return err
			},
			verifyKeep: func(ctx context.Context, s *MemoryStorage) error {
				_, err := s.GetUpstreamTokens(ctx, "valid")
				return err
			},
		},
		{
			name: "client assertion JWTs",
			setup: func(_ context.Context, s *MemoryStorage) {
				// Add directly to avoid cleanup-on-set behavior
				s.mu.Lock()
				s.clientAssertionJWTs["expired"] = time.Now().Add(-time.Hour)
				s.clientAssertionJWTs["valid"] = time.Now().Add(time.Hour)
				s.mu.Unlock()
			},
			getStats:   func(st Stats) int { return st.ClientAssertionJWTs },
			verifyGone: func(ctx context.Context, s *MemoryStorage) error { return s.ClientAssertionJWTValid(ctx, "expired") }, // Should be valid (no error) when cleaned
			verifyKeep: func(ctx context.Context, s *MemoryStorage) error { return s.ClientAssertionJWTValid(ctx, "valid") },   // Should return ErrJTIKnown
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withStorage(t, func(ctx context.Context, s *MemoryStorage) {
				tc.setup(ctx, s)
				assert.Equal(t, 2, tc.getStats(s.Stats()))

				s.cleanupExpired()

				assert.Equal(t, 1, tc.getStats(s.Stats()))
				if tc.name == "client assertion JWTs" {
					require.NoError(t, tc.verifyGone(ctx, s), "expired JTI should be valid (cleaned)")
					assert.ErrorIs(t, tc.verifyKeep(ctx, s), fosite.ErrJTIKnown, "valid JTI should still be known")
				} else {
					requireNotFoundError(t, tc.verifyGone(ctx, s))
					require.NoError(t, tc.verifyKeep(ctx, s))
				}
			})
		})
	}

	t.Run("cleanup expired invalidated codes", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			request := newMockRequesterWithExpiration("req-1", client, fosite.AuthorizeCode, time.Now().Add(time.Hour))
			require.NoError(t, s.CreateAuthorizeCodeSession(ctx, "code-1", request))
			require.NoError(t, s.InvalidateAuthorizeCodeSession(ctx, "code-1"))

			s.mu.Lock()
			if entry, ok := s.invalidatedCodes["code-1"]; ok {
				entry.expiresAt = time.Now().Add(-time.Hour)
			}
			s.mu.Unlock()

			assert.Equal(t, 1, s.Stats().InvalidatedCodes)
			s.cleanupExpired()
			assert.Equal(t, 0, s.Stats().InvalidatedCodes)
		})
	})
}

func TestMemoryStorage_CleanupLoop(t *testing.T) {
	t.Parallel()

	t.Run("cleanup runs periodically", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		storage := NewMemoryStorage(WithCleanupInterval(50 * time.Millisecond))
		defer storage.Close()

		client := testClient()
		expiredRequest := newMockRequesterWithExpiration("exp", client, fosite.AuthorizeCode, time.Now().Add(-time.Hour))
		require.NoError(t, storage.CreateAuthorizeCodeSession(ctx, "expired", expiredRequest))
		assert.Equal(t, 1, storage.Stats().AuthCodes)

		time.Sleep(100 * time.Millisecond)
		assert.Equal(t, 0, storage.Stats().AuthCodes)
	})

	t.Run("close stops cleanup goroutine", func(t *testing.T) {
		t.Parallel()
		storage := NewMemoryStorage(WithCleanupInterval(10 * time.Millisecond))

		done := make(chan struct{})
		go func() {
			storage.Close()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(1 * time.Second):
			t.Fatal("Close did not return in time")
		}
	})
}

// --- Stats Test ---

func TestMemoryStorage_Stats(t *testing.T) {
	withStorage(t, func(ctx context.Context, s *MemoryStorage) {
		stats := s.Stats()
		assert.Equal(t, 0, stats.Clients)
		assert.Equal(t, 0, stats.AuthCodes)
		assert.Equal(t, 0, stats.AccessTokens)
		assert.Equal(t, 0, stats.RefreshTokens)
		assert.Equal(t, 0, stats.PKCERequests)
		assert.Equal(t, 0, stats.UpstreamTokens)
		assert.Equal(t, 0, stats.InvalidatedCodes)
		assert.Equal(t, 0, stats.ClientAssertionJWTs)
		assert.Equal(t, 0, stats.Users)
		assert.Equal(t, 0, stats.ProviderIdentities)

		client := testClient()
		_ = s.RegisterClient(ctx, client)
		request := newMockRequester("req-1", client)
		_ = s.CreateAuthorizeCodeSession(ctx, "code-1", request)
		_ = s.CreateAccessTokenSession(ctx, "access-1", request)
		_ = s.CreateRefreshTokenSession(ctx, "refresh-1", "access-1", request)
		_ = s.CreatePKCERequestSession(ctx, "pkce-1", request)
		_ = s.StoreUpstreamTokens(ctx, "upstream-1", &UpstreamTokens{AccessToken: "test"})
		_ = s.InvalidateAuthorizeCodeSession(ctx, "code-1")
		_ = s.SetClientAssertionJWT(ctx, "jti-1", time.Now().Add(time.Hour))

		now := time.Now()
		_ = s.CreateUser(ctx, &User{ID: "user-1", CreatedAt: now, UpdatedAt: now})
		_ = s.CreateProviderIdentity(ctx, &ProviderIdentity{
			UserID: "user-1", ProviderID: "google", ProviderSubject: "google-sub-1", LinkedAt: now,
		})

		stats = s.Stats()
		assert.Equal(t, 1, stats.Clients)
		assert.Equal(t, 1, stats.AuthCodes)
		assert.Equal(t, 1, stats.AccessTokens)
		assert.Equal(t, 1, stats.RefreshTokens)
		assert.Equal(t, 1, stats.PKCERequests)
		assert.Equal(t, 1, stats.UpstreamTokens)
		assert.Equal(t, 1, stats.InvalidatedCodes)
		assert.Equal(t, 1, stats.ClientAssertionJWTs)
		assert.Equal(t, 1, stats.Users)
		assert.Equal(t, 1, stats.ProviderIdentities)
	})
}

// --- Expiration Helper Test ---

func TestGetExpirationFromRequester(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		requester fosite.Requester
		tokenType fosite.TokenType
		expected  time.Duration // 0 means use default
	}{
		{"nil requester", nil, fosite.AccessToken, 0},
		{"nil session", &mockRequester{session: nil}, fosite.AccessToken, 0},
		{"zero expiration", &mockRequester{session: newMockSession()}, fosite.AccessToken, 0},
	}

	defaultTTL := time.Hour
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			before := time.Now()
			exp := getExpirationFromRequester(tt.requester, tt.tokenType, defaultTTL)
			after := time.Now()
			assert.True(t, exp.After(before.Add(defaultTTL-time.Second)))
			assert.True(t, exp.Before(after.Add(defaultTTL+time.Second)))
		})
	}

	t.Run("valid expiration is returned", func(t *testing.T) {
		t.Parallel()
		expectedExp := time.Now().Add(2 * time.Hour)
		session := newMockSession()
		session.SetExpiresAt(fosite.AccessToken, expectedExp)
		exp := getExpirationFromRequester(&mockRequester{session: session}, fosite.AccessToken, time.Hour)
		assert.WithinDuration(t, expectedExp, exp, time.Second)
	})
}

// --- Input Validation Tests ---

func TestMemoryStorage_InputValidation(t *testing.T) {
	t.Parallel()

	client := testClient()
	tests := []struct {
		name    string
		fn      func(context.Context, *MemoryStorage) error
		wantErr error
	}{
		{"CreateAuthorizeCodeSession empty code", func(ctx context.Context, s *MemoryStorage) error {
			return s.CreateAuthorizeCodeSession(ctx, "", newMockRequester("r", client))
		}, fosite.ErrInvalidRequest},
		{"CreateAuthorizeCodeSession nil request", func(ctx context.Context, s *MemoryStorage) error {
			return s.CreateAuthorizeCodeSession(ctx, "code", nil)
		}, fosite.ErrInvalidRequest},
		{"CreateAccessTokenSession empty signature", func(ctx context.Context, s *MemoryStorage) error {
			return s.CreateAccessTokenSession(ctx, "", newMockRequester("r", client))
		}, fosite.ErrInvalidRequest},
		{"CreateAccessTokenSession nil request", func(ctx context.Context, s *MemoryStorage) error {
			return s.CreateAccessTokenSession(ctx, "sig", nil)
		}, fosite.ErrInvalidRequest},
		{"CreateRefreshTokenSession empty signature", func(ctx context.Context, s *MemoryStorage) error {
			return s.CreateRefreshTokenSession(ctx, "", "a", newMockRequester("r", client))
		}, fosite.ErrInvalidRequest},
		{"CreateRefreshTokenSession nil request", func(ctx context.Context, s *MemoryStorage) error {
			return s.CreateRefreshTokenSession(ctx, "sig", "a", nil)
		}, fosite.ErrInvalidRequest},
		{"CreatePKCERequestSession empty signature", func(ctx context.Context, s *MemoryStorage) error {
			return s.CreatePKCERequestSession(ctx, "", newMockRequester("r", client))
		}, fosite.ErrInvalidRequest},
		{"CreatePKCERequestSession nil request", func(ctx context.Context, s *MemoryStorage) error {
			return s.CreatePKCERequestSession(ctx, "sig", nil)
		}, fosite.ErrInvalidRequest},
		{"StoreUpstreamTokens empty sessionID", func(ctx context.Context, s *MemoryStorage) error {
			return s.StoreUpstreamTokens(ctx, "", &UpstreamTokens{AccessToken: "t"})
		}, fosite.ErrInvalidRequest},
		{"StorePendingAuthorization empty state", func(ctx context.Context, s *MemoryStorage) error {
			return s.StorePendingAuthorization(ctx, "", &PendingAuthorization{ClientID: "c"})
		}, fosite.ErrInvalidRequest},
		{"StorePendingAuthorization nil pending", func(ctx context.Context, s *MemoryStorage) error {
			return s.StorePendingAuthorization(ctx, "state", nil)
		}, fosite.ErrInvalidRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withStorage(t, func(ctx context.Context, s *MemoryStorage) {
				err := tt.fn(ctx, s)
				require.Error(t, err)
				require.ErrorIs(t, err, tt.wantErr)
			})
		})
	}

	t.Run("StoreUpstreamTokens nil tokens is valid", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-id", nil))
			retrieved, err := s.GetUpstreamTokens(ctx, "session-id")
			require.NoError(t, err)
			assert.Nil(t, retrieved)
		})
	})
}

// --- User Storage Tests ---

func TestMemoryStorage_User(t *testing.T) {
	t.Parallel()

	makeUser := func(id string) *User {
		now := time.Now()
		return &User{ID: id, CreatedAt: now, UpdatedAt: now}
	}

	t.Run("create and get", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			user := makeUser("user-123")
			require.NoError(t, s.CreateUser(ctx, user))

			retrieved, err := s.GetUser(ctx, "user-123")
			require.NoError(t, err)
			assert.Equal(t, user.ID, retrieved.ID)
			assert.Equal(t, user.CreatedAt.Unix(), retrieved.CreatedAt.Unix())
		})
	})

	t.Run("get non-existent", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			_, err := s.GetUser(ctx, "non-existent")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	})

	t.Run("create duplicate", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			user := makeUser("user-123")
			require.NoError(t, s.CreateUser(ctx, user))
			err := s.CreateUser(ctx, user)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrAlreadyExists)
		})
	})

	t.Run("delete", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			require.NoError(t, s.CreateUser(ctx, makeUser("to-delete")))
			require.NoError(t, s.DeleteUser(ctx, "to-delete"))
			_, err := s.GetUser(ctx, "to-delete")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	})

	t.Run("delete non-existent", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			err := s.DeleteUser(ctx, "non-existent")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	})
}

func TestMemoryStorage_ProviderIdentity(t *testing.T) {
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
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			// First create the user
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

	t.Run("get non-existent", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			_, err := s.GetProviderIdentity(ctx, "github", "non-existent")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	})

	t.Run("create for non-existent user", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			identity := makeIdentity("google", "google-sub-123", "non-existent-user")
			err := s.CreateProviderIdentity(ctx, identity)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	})

	t.Run("create duplicate", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			// First create the user
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
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			// Create user and identity
			now := time.Now()
			require.NoError(t, s.CreateUser(ctx, &User{ID: "user-update", CreatedAt: now, UpdatedAt: now}))
			identity := makeIdentity("google", "google-sub-update", "user-update")
			require.NoError(t, s.CreateProviderIdentity(ctx, identity))

			// Update last used at
			newLastUsed := now.Add(time.Hour)
			require.NoError(t, s.UpdateProviderIdentityLastUsed(ctx, "google", "google-sub-update", newLastUsed))

			// Verify the update
			retrieved, err := s.GetProviderIdentity(ctx, "google", "google-sub-update")
			require.NoError(t, err)
			assert.WithinDuration(t, newLastUsed, retrieved.LastUsedAt, time.Second)
		})
	})

	t.Run("update last used at for non-existent identity", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			err := s.UpdateProviderIdentityLastUsed(ctx, "github", "non-existent", time.Now())
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	})
}

func TestMemoryStorage_GetUserProviderIdentities(t *testing.T) {
	t.Parallel()

	t.Run("returns all identities for user", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			now := time.Now()
			require.NoError(t, s.CreateUser(ctx, &User{ID: "user-1", CreatedAt: now, UpdatedAt: now}))

			// Create multiple identities for the user
			id1 := &ProviderIdentity{UserID: "user-1", ProviderID: "google", ProviderSubject: "google-sub", LinkedAt: now}
			id2 := &ProviderIdentity{UserID: "user-1", ProviderID: "github", ProviderSubject: "github-sub", LinkedAt: now}
			require.NoError(t, s.CreateProviderIdentity(ctx, id1))
			require.NoError(t, s.CreateProviderIdentity(ctx, id2))

			identities, err := s.GetUserProviderIdentities(ctx, "user-1")
			require.NoError(t, err)
			assert.Len(t, identities, 2)

			// Verify both providers are present
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
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			now := time.Now()
			require.NoError(t, s.CreateUser(ctx, &User{ID: "lonely-user", CreatedAt: now, UpdatedAt: now}))

			identities, err := s.GetUserProviderIdentities(ctx, "lonely-user")
			require.NoError(t, err)
			assert.Empty(t, identities)
		})
	})

	t.Run("returns error for non-existent user", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			_, err := s.GetUserProviderIdentities(ctx, "non-existent")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	})

	t.Run("does not return other users identities", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			now := time.Now()
			require.NoError(t, s.CreateUser(ctx, &User{ID: "user-a", CreatedAt: now, UpdatedAt: now}))
			require.NoError(t, s.CreateUser(ctx, &User{ID: "user-b", CreatedAt: now, UpdatedAt: now}))

			// Create identities for both users
			require.NoError(t, s.CreateProviderIdentity(ctx, &ProviderIdentity{
				UserID: "user-a", ProviderID: "google", ProviderSubject: "sub-a", LinkedAt: now,
			}))
			require.NoError(t, s.CreateProviderIdentity(ctx, &ProviderIdentity{
				UserID: "user-b", ProviderID: "google", ProviderSubject: "sub-b", LinkedAt: now,
			}))

			// Get only user-a's identities
			identities, err := s.GetUserProviderIdentities(ctx, "user-a")
			require.NoError(t, err)
			assert.Len(t, identities, 1)
			assert.Equal(t, "sub-a", identities[0].ProviderSubject)
		})
	})

	t.Run("returns defensive copies", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			now := time.Now()
			require.NoError(t, s.CreateUser(ctx, &User{ID: "user-copy", CreatedAt: now, UpdatedAt: now}))
			require.NoError(t, s.CreateProviderIdentity(ctx, &ProviderIdentity{
				UserID: "user-copy", ProviderID: "google", ProviderSubject: "sub-copy", LinkedAt: now,
			}))

			identities, err := s.GetUserProviderIdentities(ctx, "user-copy")
			require.NoError(t, err)
			require.Len(t, identities, 1)

			// Modify the returned identity
			identities[0].ProviderSubject = "modified"

			// Fetch again and verify original is unchanged
			identities2, err := s.GetUserProviderIdentities(ctx, "user-copy")
			require.NoError(t, err)
			assert.Equal(t, "sub-copy", identities2[0].ProviderSubject)
		})
	})
}

func TestMemoryStorage_DeleteUser_CascadesAssociatedData(t *testing.T) {
	withStorage(t, func(ctx context.Context, s *MemoryStorage) {
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
			UpstreamSubject: "google-sub", ClientID: "client-1",
		}
		otherTokens := &UpstreamTokens{
			ProviderID: "google", AccessToken: "other-token", UserID: "other-user",
			UpstreamSubject: "other-google-sub", ClientID: "client-1",
		}
		require.NoError(t, s.StoreUpstreamTokens(ctx, "session-user", userTokens))
		require.NoError(t, s.StoreUpstreamTokens(ctx, "session-other", otherTokens))

		assert.Equal(t, 2, s.Stats().Users)
		assert.Equal(t, 3, s.Stats().ProviderIdentities)
		assert.Equal(t, 2, s.Stats().UpstreamTokens)

		require.NoError(t, s.DeleteUser(ctx, "user-cascade"))

		assert.Equal(t, 1, s.Stats().Users) // other-user still exists
		assert.Equal(t, 1, s.Stats().ProviderIdentities)
		assert.Equal(t, 1, s.Stats().UpstreamTokens)

		// Verify the user's identities are gone
		_, err := s.GetProviderIdentity(ctx, "google", "google-sub")
		assert.ErrorIs(t, err, ErrNotFound)
		_, err = s.GetProviderIdentity(ctx, "github", "github-sub")
		assert.ErrorIs(t, err, ErrNotFound)

		// Verify the user's upstream tokens are gone
		_, err = s.GetUpstreamTokens(ctx, "session-user")
		assert.ErrorIs(t, err, ErrNotFound)

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

func TestMemoryStorage_UserInputValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fn      func(context.Context, *MemoryStorage) error
		wantErr error
	}{
		{"CreateUser nil user", func(ctx context.Context, s *MemoryStorage) error {
			return s.CreateUser(ctx, nil)
		}, fosite.ErrInvalidRequest},
		{"CreateUser empty ID", func(ctx context.Context, s *MemoryStorage) error {
			return s.CreateUser(ctx, &User{ID: ""})
		}, fosite.ErrInvalidRequest},
		{"CreateProviderIdentity nil identity", func(ctx context.Context, s *MemoryStorage) error {
			return s.CreateProviderIdentity(ctx, nil)
		}, fosite.ErrInvalidRequest},
		{"CreateProviderIdentity empty user ID", func(ctx context.Context, s *MemoryStorage) error {
			return s.CreateProviderIdentity(ctx, &ProviderIdentity{UserID: "", ProviderID: "google", ProviderSubject: "sub"})
		}, fosite.ErrInvalidRequest},
		{"CreateProviderIdentity empty provider ID", func(ctx context.Context, s *MemoryStorage) error {
			return s.CreateProviderIdentity(ctx, &ProviderIdentity{UserID: "user-1", ProviderID: "", ProviderSubject: "sub"})
		}, fosite.ErrInvalidRequest},
		{"CreateProviderIdentity empty provider subject", func(ctx context.Context, s *MemoryStorage) error {
			return s.CreateProviderIdentity(ctx, &ProviderIdentity{UserID: "user-1", ProviderID: "google", ProviderSubject: ""})
		}, fosite.ErrInvalidRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withStorage(t, func(ctx context.Context, s *MemoryStorage) {
				err := tt.fn(ctx, s)
				require.Error(t, err)
				require.ErrorIs(t, err, tt.wantErr)
			})
		})
	}
}

// --- Concurrent Access Tests ---

func TestMemoryStorage_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	t.Run("concurrent writes", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			client := testClient()
			var wg sync.WaitGroup
			for i := 0; i < 100; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					_ = s.CreateAuthorizeCodeSession(ctx, fmt.Sprintf("code-%d", idx), newMockRequester(fmt.Sprintf("req-%d", idx), client))
				}(i)
			}
			wg.Wait()
		})
	})

	t.Run("concurrent reads and writes", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			client := testClient()
			for i := 0; i < 10; i++ {
				_ = s.CreateAccessTokenSession(ctx, fmt.Sprintf("preload-%d", i), newMockRequester("preload", client))
			}

			var wg sync.WaitGroup
			for i := 0; i < 100; i++ {
				wg.Add(2)
				go func(idx int) {
					defer wg.Done()
					_ = s.CreateAccessTokenSession(ctx, fmt.Sprintf("token-%d", idx), newMockRequester(fmt.Sprintf("req-%d", idx), client))
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
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			var wg sync.WaitGroup
			numGoroutines := 50
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

			for i := 0; i < numGoroutines; i++ {
				client, err := s.GetClient(ctx, fmt.Sprintf("client-%d", i))
				require.NoError(t, err, "client-%d should exist", i)
				assert.Equal(t, fmt.Sprintf("client-%d", i), client.GetID())
			}
		})
	})

	t.Run("concurrent cleanup with writes", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			client := testClient()
			var wg sync.WaitGroup
			for i := 0; i < 50; i++ {
				wg.Add(2)
				go func(idx int) {
					defer wg.Done()
					_ = s.CreateAccessTokenSession(ctx, fmt.Sprintf("token-%d", idx), newMockRequester(fmt.Sprintf("req-%d", idx), client))
				}(i)
				go func(_ int) {
					defer wg.Done()
					s.cleanupExpired()
				}(i)
			}
			wg.Wait()
		})
	})
}
