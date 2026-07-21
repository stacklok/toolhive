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
	"sync/atomic"
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
	fn(t.Context(), storage)
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

func TestMemoryStorage_RenewClientTTL_NoOp(t *testing.T) {
	withStorage(t, func(ctx context.Context, s *MemoryStorage) {
		// In-memory clients have no TTL, so renewal is a documented no-op: it must
		// not error and must leave the client retrievable.
		client := &mockClient{id: "public-client", public: true}
		require.NoError(t, s.RegisterClient(ctx, client))
		require.NoError(t, s.RenewClientTTL(ctx, client))
		retrieved, err := s.GetClient(ctx, "public-client")
		require.NoError(t, err)
		assert.Equal(t, "public-client", retrieved.GetID())
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
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-123", "provider-a", tokens))

			retrieved, err := s.GetUpstreamTokens(ctx, "session-123", "provider-a", nil)
			require.NoError(t, err)
			assert.Equal(t, tokens.AccessToken, retrieved.AccessToken)
			assert.Equal(t, tokens.RefreshToken, retrieved.RefreshToken)
			assert.Equal(t, tokens.UserID, retrieved.UserID)
			assert.Equal(t, tokens.ClientID, retrieved.ClientID)
		})
	})

	t.Run("get non-existent", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			_, err := s.GetUpstreamTokens(ctx, "non-existent", "provider-a", nil)
			requireNotFoundError(t, err)
		})
	})

	t.Run("delete", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "to-delete", "provider-a", &UpstreamTokens{AccessToken: "test"}))
			require.NoError(t, s.DeleteUpstreamTokens(ctx, "to-delete"))
			_, err := s.GetUpstreamTokens(ctx, "to-delete", "provider-a", nil)
			requireNotFoundError(t, err)
		})
	})

	t.Run("overwrite existing tokens", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session", "provider-a", &UpstreamTokens{AccessToken: "token-1", UserID: "user1"}))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session", "provider-a", &UpstreamTokens{AccessToken: "token-2", UserID: "user2"}))

			retrieved, err := s.GetUpstreamTokens(ctx, "session", "provider-a", nil)
			require.NoError(t, err)
			assert.Equal(t, "token-2", retrieved.AccessToken)
			assert.Equal(t, "user2", retrieved.UserID)
		})
	})

	t.Run("get expired tokens returns ErrExpired with token data", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "expired", "provider-a", &UpstreamTokens{
				AccessToken:  "expired-token",
				RefreshToken: "refresh-token",
				ExpiresAt:    time.Now().Add(-time.Hour),
			}))
			assert.Equal(t, 1, s.Stats().UpstreamTokens)

			retrieved, err := s.GetUpstreamTokens(ctx, "expired", "provider-a", nil)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrExpired)
			// Tokens should be returned alongside ErrExpired for refresh purposes
			require.NotNil(t, retrieved)
			assert.Equal(t, "expired-token", retrieved.AccessToken)
			assert.Equal(t, "refresh-token", retrieved.RefreshToken)
		})
	})

	t.Run("multi-provider store and get", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			tokensA := &UpstreamTokens{AccessToken: "access-a", RefreshToken: "refresh-a", ExpiresAt: time.Now().Add(time.Hour)}
			tokensB := &UpstreamTokens{AccessToken: "access-b", RefreshToken: "refresh-b", ExpiresAt: time.Now().Add(time.Hour)}

			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-1", "provider-a", tokensA))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-1", "provider-b", tokensB))

			retrievedA, err := s.GetUpstreamTokens(ctx, "session-1", "provider-a", nil)
			require.NoError(t, err)
			assert.Equal(t, "access-a", retrievedA.AccessToken)

			retrievedB, err := s.GetUpstreamTokens(ctx, "session-1", "provider-b", nil)
			require.NoError(t, err)
			assert.Equal(t, "access-b", retrievedB.AccessToken)
		})
	})

	t.Run("cross-provider isolation", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			tokensA := &UpstreamTokens{AccessToken: "access-a", ExpiresAt: time.Now().Add(time.Hour)}
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-1", "provider-a", tokensA))

			// Provider B should not be affected by provider A's data
			_, err := s.GetUpstreamTokens(ctx, "session-1", "provider-b", nil)
			requireNotFoundError(t, err)

			// Store provider B and verify provider A is unchanged
			tokensB := &UpstreamTokens{AccessToken: "access-b", ExpiresAt: time.Now().Add(time.Hour)}
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-1", "provider-b", tokensB))

			retrievedA, err := s.GetUpstreamTokens(ctx, "session-1", "provider-a", nil)
			require.NoError(t, err)
			assert.Equal(t, "access-a", retrievedA.AccessToken)
		})
	})

	t.Run("GetAllUpstreamTokens with two providers", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			tokensA := &UpstreamTokens{AccessToken: "access-a", ExpiresAt: time.Now().Add(time.Hour)}
			tokensB := &UpstreamTokens{AccessToken: "access-b", ExpiresAt: time.Now().Add(time.Hour)}

			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-1", "provider-a", tokensA))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-1", "provider-b", tokensB))

			all, err := s.GetAllUpstreamTokens(ctx, "session-1", nil)
			require.NoError(t, err)
			assert.Len(t, all, 2)
			assert.Equal(t, "access-a", all["provider-a"].AccessToken)
			assert.Equal(t, "access-b", all["provider-b"].AccessToken)
		})
	})

	t.Run("GetAllUpstreamTokens unknown session", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			all, err := s.GetAllUpstreamTokens(ctx, "unknown-session", nil)
			require.NoError(t, err)
			assert.Empty(t, all)
		})
	})

	t.Run("GetAllUpstreamTokens includes expired tokens", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-1", "provider-a", &UpstreamTokens{
				AccessToken:  "expired-access",
				RefreshToken: "expired-refresh",
				ExpiresAt:    time.Now().Add(-time.Hour),
			}))

			all, err := s.GetAllUpstreamTokens(ctx, "session-1", nil)
			require.NoError(t, err)
			require.Len(t, all, 1)
			assert.Equal(t, "expired-access", all["provider-a"].AccessToken)
			assert.Equal(t, "expired-refresh", all["provider-a"].RefreshToken)
		})
	})

	t.Run("session delete wipes all providers", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-1", "provider-a", &UpstreamTokens{AccessToken: "a"}))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-1", "provider-b", &UpstreamTokens{AccessToken: "b"}))
			assert.Equal(t, 2, s.Stats().UpstreamTokens)

			require.NoError(t, s.DeleteUpstreamTokens(ctx, "session-1"))

			_, err := s.GetUpstreamTokens(ctx, "session-1", "provider-a", nil)
			requireNotFoundError(t, err)
			_, err = s.GetUpstreamTokens(ctx, "session-1", "provider-b", nil)
			requireNotFoundError(t, err)
			assert.Equal(t, 0, s.Stats().UpstreamTokens)
		})
	})

	t.Run("empty providerName returns error for Store", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			err := s.StoreUpstreamTokens(ctx, "session-1", "", &UpstreamTokens{AccessToken: "test"})
			require.Error(t, err)
			assert.ErrorIs(t, err, fosite.ErrInvalidRequest)
		})
	})

	t.Run("empty providerName returns error for Get", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			_, err := s.GetUpstreamTokens(ctx, "session-1", "", nil)
			require.Error(t, err)
			assert.ErrorIs(t, err, fosite.ErrInvalidRequest)
		})
	})

	t.Run("expired entry cleanup", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			// Store a single provider with an already-expired storage TTL
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-1", "provider-a", &UpstreamTokens{
				AccessToken: "test",
				ExpiresAt:   time.Now().Add(-DefaultRefreshTokenTTL - time.Hour),
			}))

			// Verify the entry exists
			assert.Equal(t, 1, s.Stats().UpstreamTokens, "entry should exist before cleanup")

			// Run cleanup to expire the entry
			s.cleanupExpired()

			// Verify the entry is cleaned up
			assert.Equal(t, 0, s.Stats().UpstreamTokens, "expired entry should be cleaned up")
		})
	})
}

func TestMemoryStorage_GetLatestUpstreamTokensForUser(t *testing.T) {
	t.Parallel()

	t.Run("no_match", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			_, err := s.GetLatestUpstreamTokensForUser(ctx, "user-A", "prov-X")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotFound)
		})
	})

	t.Run("one_match_returns_deep_copy", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
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

			// Mutate the returned copy and confirm the stored value is unchanged.
			got.RefreshToken = "mutated"
			got2, err := s.GetLatestUpstreamTokensForUser(ctx, "user-A", "prov-X")
			require.NoError(t, err)
			assert.Equal(t, "rt-1", got2.RefreshToken, "stored row must be unaffected by mutation of returned copy")
		})
	})

	t.Run("multiple_sessions_pick_latest_expires_at", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
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
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
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
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			// The lookup filters on the stored UpstreamTokens.ProviderID field,
			// not the map key. StoreUpstreamTokens copies the field from the
			// input struct, so they always match here.
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
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			// ExpiresAt is 1h in the past (access token expired), but the storage TTL
			// is ExpiresAt + DefaultRefreshTokenTTL which is still in the future,
			// so the cleanup loop has not swept this row yet.
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-1", "prov-X", &UpstreamTokens{
				ProviderID:   "prov-X",
				UserID:       "user-A",
				RefreshToken: "rt-expired-at",
				ExpiresAt:    time.Now().Add(-time.Hour),
			}))

			got, err := s.GetLatestUpstreamTokensForUser(ctx, "user-A", "prov-X")
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, "rt-expired-at", got.RefreshToken)
		})
	})

	t.Run("zero_expires_at_wins_over_nonzero", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			// Row with zero ExpiresAt — providers like Slack and GitHub OAuth Apps
			// genuinely never expire; treated as "alive forever" by IsExpired.
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-zero", "prov-X", &UpstreamTokens{
				ProviderID:   "prov-X",
				UserID:       "user-A",
				RefreshToken: "rt-zero",
				ExpiresAt:    time.Time{},
			}))
			// Row with a real ExpiresAt in the future.
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-nonzero", "prov-X", &UpstreamTokens{
				ProviderID:   "prov-X",
				UserID:       "user-A",
				RefreshToken: "rt-nonzero",
				ExpiresAt:    time.Now().Add(time.Hour),
			}))

			got, err := s.GetLatestUpstreamTokensForUser(ctx, "user-A", "prov-X")
			require.NoError(t, err)
			assert.Equal(t, "rt-zero", got.RefreshToken)
		})
	})

	t.Run("two_zero_expires_at_rows", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			// Both rows have zero ExpiresAt. Go's map iteration is randomized so
			// we only assert that one of the two is returned.
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

	t.Run("empty_user_id", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			_, err := s.GetLatestUpstreamTokensForUser(ctx, "", "prov-X")
			require.Error(t, err)
			require.ErrorIs(t, err, fosite.ErrInvalidRequest)
		})
	})

	t.Run("empty_provider_id", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			_, err := s.GetLatestUpstreamTokensForUser(ctx, "user-A", "")
			require.Error(t, err)
			require.ErrorIs(t, err, fosite.ErrInvalidRequest)
		})
	})

	t.Run("returns_all_fields_round_trip", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
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

	t.Run("DeleteUpstreamTokensForProvider leaves sibling intact", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-1", "provider-a", &UpstreamTokens{AccessToken: "a"}))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-1", "provider-b", &UpstreamTokens{AccessToken: "b"}))

			require.NoError(t, s.DeleteUpstreamTokensForProvider(ctx, "session-1", "provider-a"))

			_, err := s.GetUpstreamTokens(ctx, "session-1", "provider-a", nil)
			requireNotFoundError(t, err)

			got, err := s.GetUpstreamTokens(ctx, "session-1", "provider-b", nil)
			require.NoError(t, err)
			assert.Equal(t, "b", got.AccessToken)

			all, err := s.GetAllUpstreamTokens(ctx, "session-1", nil)
			require.NoError(t, err)
			assert.Len(t, all, 1)
		})
	})

	t.Run("DeleteUpstreamTokensForProvider absent row is non-fatal", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			require.NoError(t, s.DeleteUpstreamTokensForProvider(ctx, "no-such-session", "provider-a"))
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
			UpstreamPKCEVerifier: "verifier", UpstreamNonce: "nonce",
			SingleLeg: true, ChainUpstreams: []string{"provider-1", "provider-2"},
			CreatedAt: time.Now(),
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
			assert.Equal(t, pending.SingleLeg, retrieved.SingleLeg)
			assert.Equal(t, pending.ChainUpstreams, retrieved.ChainUpstreams)
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
				// Entry must be older than DefaultRefreshTokenTTL past access token expiry to be cleaned up
				_ = s.StoreUpstreamTokens(ctx, "expired", "provider-a", &UpstreamTokens{AccessToken: "exp", ExpiresAt: time.Now().Add(-DefaultRefreshTokenTTL - time.Hour)})
				_ = s.StoreUpstreamTokens(ctx, "valid", "provider-a", &UpstreamTokens{AccessToken: "val", ExpiresAt: time.Now().Add(time.Hour)})
			},
			getStats: func(st Stats) int { return st.UpstreamTokens },
			verifyGone: func(ctx context.Context, s *MemoryStorage) error {
				_, err := s.GetUpstreamTokens(ctx, "expired", "provider-a", nil)
				return err
			},
			verifyKeep: func(ctx context.Context, s *MemoryStorage) error {
				_, err := s.GetUpstreamTokens(ctx, "valid", "provider-a", nil)
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

	t.Run("upstream tokens with zero ExpiresAt are never evicted", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			// Non-expiring token (ExpiresAt zero) must survive cleanup runs.
			_ = s.StoreUpstreamTokens(ctx, "never-expiring", "provider-a", &UpstreamTokens{
				AccessToken: "non-expiring-token",
				// ExpiresAt intentionally zero
			})
			// Expiring token to confirm cleanup still works.
			_ = s.StoreUpstreamTokens(ctx, "expired", "provider-a", &UpstreamTokens{
				AccessToken: "expired-token",
				ExpiresAt:   time.Now().Add(-DefaultRefreshTokenTTL - time.Hour),
			})

			assert.Equal(t, 2, s.Stats().UpstreamTokens)

			s.cleanupExpired()

			assert.Equal(t, 1, s.Stats().UpstreamTokens, "only the expiring token should be removed")

			tokens, err := s.GetUpstreamTokens(ctx, "never-expiring", "provider-a", nil)
			require.NoError(t, err)
			require.NotNil(t, tokens)
			assert.Equal(t, "non-expiring-token", tokens.AccessToken)
			assert.True(t, tokens.ExpiresAt.IsZero())
		})
	})

	t.Run("non-expiring token with SessionExpiresAt is evicted after session bound passes", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			err := s.StoreUpstreamTokens(ctx, "sess-1", "github", &UpstreamTokens{
				AccessToken:      "pat-token",
				SessionExpiresAt: time.Now().Add(-DefaultRefreshTokenTTL - time.Second),
			})
			require.NoError(t, err)
			assert.Equal(t, 1, s.Stats().UpstreamTokens)

			s.cleanupExpired()

			assert.Equal(t, 0, s.Stats().UpstreamTokens)
			_, getErr := s.GetUpstreamTokens(ctx, "sess-1", "github", nil)
			requireNotFoundError(t, getErr)
		})
	})

	t.Run("non-expiring token with future SessionExpiresAt is kept", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			err := s.StoreUpstreamTokens(ctx, "sess-2", "github", &UpstreamTokens{
				AccessToken:      "pat-token",
				SessionExpiresAt: time.Now().Add(time.Hour),
			})
			require.NoError(t, err)
			assert.Equal(t, 1, s.Stats().UpstreamTokens)

			s.cleanupExpired()

			assert.Equal(t, 1, s.Stats().UpstreamTokens)
		})
	})

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
		ctx := t.Context()
		storage := NewMemoryStorage(WithCleanupInterval(50 * time.Millisecond))
		defer storage.Close()

		client := testClient()
		expiredRequest := newMockRequesterWithExpiration("exp", client, fosite.AuthorizeCode, time.Now().Add(-time.Hour))
		require.NoError(t, storage.CreateAuthorizeCodeSession(ctx, "expired", expiredRequest))
		assert.Equal(t, 1, storage.Stats().AuthCodes)

		require.Eventually(t, func() bool {
			return storage.Stats().AuthCodes == 0
		}, 2*time.Second, 25*time.Millisecond, "expired auth code should be cleaned up")
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
		_ = s.StoreUpstreamTokens(ctx, "upstream-1", "provider-a", &UpstreamTokens{AccessToken: "test"})
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
			return s.StoreUpstreamTokens(ctx, "", "provider-a", &UpstreamTokens{AccessToken: "t"})
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
			require.NoError(t, s.StoreUpstreamTokens(ctx, "session-id", "provider-a", nil))
			retrieved, err := s.GetUpstreamTokens(ctx, "session-id", "provider-a", nil)
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
		require.NoError(t, s.StoreUpstreamTokens(ctx, "session-user", "google", userTokens))
		require.NoError(t, s.StoreUpstreamTokens(ctx, "session-other", "google", otherTokens))

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
		_, err = s.GetUpstreamTokens(ctx, "session-user", "google", nil)
		assert.ErrorIs(t, err, ErrNotFound)

		// Verify the other user's identity is still there
		retrieved, err := s.GetProviderIdentity(ctx, "google", "other-google-sub")
		require.NoError(t, err)
		assert.Equal(t, "other-user", retrieved.UserID)

		// Verify the other user's upstream tokens are still there
		otherRetrieved, err := s.GetUpstreamTokens(ctx, "session-other", "google", nil)
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

func TestMemoryStorage_DCRCredentials_RoundTrip(t *testing.T) {
	withStorage(t, func(ctx context.Context, s *MemoryStorage) {
		key := DCRKey{
			Issuer:      "https://thv.example.com",
			UpstreamID:  "https://idp.example.com",
			RedirectURI: "https://thv.example.com/oauth/callback",
			ScopesHash:  ScopesHash([]string{"openid", "profile"}),
		}
		creds := &DCRCredentials{
			Key:                     key,
			ProviderName:            "atlassian",
			ClientID:                "client-abc",
			ClientSecret:            "secret-xyz",
			TokenEndpointAuthMethod: "client_secret_basic",
			RegistrationAccessToken: "rat-123",
			RegistrationClientURI:   "https://idp.example.com/register/client-abc",
			AuthorizationEndpoint:   "https://idp.example.com/authorize",
			TokenEndpoint:           "https://idp.example.com/token",
			CreatedAt:               time.Date(2025, 5, 1, 12, 0, 0, 0, time.UTC),
			ClientSecretExpiresAt:   time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		}

		require.NoError(t, s.StoreDCRCredentials(ctx, creds))

		got, err := s.GetDCRCredentials(ctx, key)
		require.NoError(t, err)
		require.NotNil(t, got)

		// Every field round-trips, including the embedded Key.
		assert.Equal(t, *creds, *got)
	})
}

func TestMemoryStorage_DCRCredentials_DistinctKeysDoNotCollide(t *testing.T) {
	withStorage(t, func(ctx context.Context, s *MemoryStorage) {
		mkKey := func(issuer, upstreamID, redirect string, scopes []string) DCRKey {
			return DCRKey{
				Issuer:      issuer,
				UpstreamID:  upstreamID,
				RedirectURI: redirect,
				ScopesHash:  ScopesHash(scopes),
			}
		}
		mkCreds := func(key DCRKey, clientID string) *DCRCredentials {
			return &DCRCredentials{
				Key:                   key,
				ClientID:              clientID,
				AuthorizationEndpoint: "https://idp.example.com/auth",
				TokenEndpoint:         "https://idp.example.com/token",
			}
		}
		entries := []*DCRCredentials{
			mkCreds(mkKey("https://idp-a.example.com", "https://up-a", "https://x/cb", []string{"openid"}), "a"),
			mkCreds(mkKey("https://idp-b.example.com", "https://up-a", "https://x/cb", []string{"openid"}), "b"),
			mkCreds(mkKey("https://idp-a.example.com", "https://up-a", "https://y/cb", []string{"openid"}), "c"),
			mkCreds(mkKey("https://idp-a.example.com", "https://up-a", "https://x/cb", []string{"openid", "email"}), "d"),
			// Differs from entry "a" only by UpstreamID — two OAuth2 upstreams
			// inside one embedded authserver sharing issuer, redirect, and
			// scopes. Before UpstreamID joined the key these collided (#5823).
			mkCreds(mkKey("https://idp-a.example.com", "https://up-b", "https://x/cb", []string{"openid"}), "e"),
		}
		for _, e := range entries {
			require.NoError(t, s.StoreDCRCredentials(ctx, e))
		}

		for _, want := range entries {
			got, err := s.GetDCRCredentials(ctx, want.Key)
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, want.ClientID, got.ClientID)
		}
	})
}

func TestMemoryStorage_DCRCredentials_OverwriteSemantics(t *testing.T) {
	withStorage(t, func(ctx context.Context, s *MemoryStorage) {
		key := DCRKey{
			Issuer:      "https://idp.example.com",
			UpstreamID:  "https://upstream.example.com",
			RedirectURI: "https://x/cb",
			ScopesHash:  ScopesHash([]string{"openid"}),
		}
		mkCreds := func(clientID string) *DCRCredentials {
			return &DCRCredentials{
				Key:                   key,
				ClientID:              clientID,
				AuthorizationEndpoint: "https://idp.example.com/auth",
				TokenEndpoint:         "https://idp.example.com/token",
			}
		}

		require.NoError(t, s.StoreDCRCredentials(ctx, mkCreds("first")))
		require.NoError(t, s.StoreDCRCredentials(ctx, mkCreds("second")))

		got, err := s.GetDCRCredentials(ctx, key)
		require.NoError(t, err)
		assert.Equal(t, "second", got.ClientID)
	})
}

func TestMemoryStorage_DCRCredentials_NotFound(t *testing.T) {
	withStorage(t, func(ctx context.Context, s *MemoryStorage) {
		_, err := s.GetDCRCredentials(ctx, DCRKey{Issuer: "https://unknown.example.com"})
		requireNotFoundError(t, err)
	})
}

// TestMemoryStorage_DCRCredentials_StoreInvalidInputRejected pins the
// fail-loud-on-invalid-input contract: nil creds, an unpopulated Key
// (empty Issuer, UpstreamID, RedirectURI, or ScopesHash), and missing RFC 7591
// mandatory response fields (ClientID, AuthorizationEndpoint,
// TokenEndpoint) must be rejected with fosite.ErrInvalidRequest rather
// than producing a working-looking write that fails downstream at the
// upstream's token endpoint.
func TestMemoryStorage_DCRCredentials_StoreInvalidInputRejected(t *testing.T) {
	t.Parallel()

	// validCreds returns a fully-populated DCRCredentials that subtests
	// mutate to isolate a single missing field. Keeping every other field
	// valid ensures the assertion proves which field was rejected.
	validCreds := func() *DCRCredentials {
		return &DCRCredentials{
			Key: DCRKey{
				Issuer:      "https://idp.example.com",
				UpstreamID:  "https://upstream.example.com",
				RedirectURI: "https://x/cb",
				ScopesHash:  ScopesHash([]string{"openid"}),
			},
			ClientID:              "abc",
			AuthorizationEndpoint: "https://idp.example.com/auth",
			TokenEndpoint:         "https://idp.example.com/token",
		}
	}

	tests := []struct {
		name    string
		mutator func(*DCRCredentials) *DCRCredentials
	}{
		{
			name:    "nil creds",
			mutator: func(*DCRCredentials) *DCRCredentials { return nil },
		},
		{
			name: "empty issuer",
			mutator: func(c *DCRCredentials) *DCRCredentials {
				c.Key.Issuer = ""
				return c
			},
		},
		{
			name: "empty upstream_id",
			mutator: func(c *DCRCredentials) *DCRCredentials {
				c.Key.UpstreamID = ""
				return c
			},
		},
		{
			name: "empty redirect_uri",
			mutator: func(c *DCRCredentials) *DCRCredentials {
				c.Key.RedirectURI = ""
				return c
			},
		},
		{
			name: "empty scopes_hash",
			mutator: func(c *DCRCredentials) *DCRCredentials {
				c.Key.ScopesHash = ""
				return c
			},
		},
		{
			name: "empty client_id",
			mutator: func(c *DCRCredentials) *DCRCredentials {
				c.ClientID = ""
				return c
			},
		},
		{
			name: "empty authorization_endpoint",
			mutator: func(c *DCRCredentials) *DCRCredentials {
				c.AuthorizationEndpoint = ""
				return c
			},
		},
		{
			name: "empty token_endpoint",
			mutator: func(c *DCRCredentials) *DCRCredentials {
				c.TokenEndpoint = ""
				return c
			},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			withStorage(t, func(ctx context.Context, s *MemoryStorage) {
				err := s.StoreDCRCredentials(ctx, tc.mutator(validCreds()))
				require.Error(t, err)
				assert.ErrorIs(t, err, fosite.ErrInvalidRequest)
				// Confirm the rejection did not partially populate the store.
				assert.Equal(t, 0, s.Stats().DCRCredentials,
					"rejected Store must not leave any entry behind")
			})
		})
	}
}

// TestMemoryStorage_DCRCredentials_GetReturnsDefensiveCopy pins the
// defensive-copy contract: a caller mutating the returned record must not
// affect persisted state.
func TestMemoryStorage_DCRCredentials_GetReturnsDefensiveCopy(t *testing.T) {
	withStorage(t, func(ctx context.Context, s *MemoryStorage) {
		key := DCRKey{
			Issuer:      "https://idp.example.com",
			UpstreamID:  "https://upstream.example.com",
			RedirectURI: "https://x/cb",
			ScopesHash:  ScopesHash([]string{"openid"}),
		}
		require.NoError(t, s.StoreDCRCredentials(ctx, &DCRCredentials{
			Key:                   key,
			ClientID:              "orig",
			AuthorizationEndpoint: "https://idp.example.com/auth",
			TokenEndpoint:         "https://idp.example.com/token",
		}))

		got, err := s.GetDCRCredentials(ctx, key)
		require.NoError(t, err)
		got.ClientID = "tampered-by-caller"

		refetched, err := s.GetDCRCredentials(ctx, key)
		require.NoError(t, err)
		assert.Equal(t, "orig", refetched.ClientID)
	})
}

// TestMemoryStorage_DCRCredentials_StoreCopyIsolatesCaller pins the
// store-side defensive-copy contract: a caller mutating the input *after*
// Store must not affect persisted state.
func TestMemoryStorage_DCRCredentials_StoreCopyIsolatesCaller(t *testing.T) {
	withStorage(t, func(ctx context.Context, s *MemoryStorage) {
		key := DCRKey{
			Issuer:      "https://idp.example.com",
			UpstreamID:  "https://upstream.example.com",
			RedirectURI: "https://x/cb",
			ScopesHash:  ScopesHash([]string{"openid"}),
		}
		input := &DCRCredentials{
			Key:                   key,
			ClientID:              "orig",
			AuthorizationEndpoint: "https://idp.example.com/auth",
			TokenEndpoint:         "https://idp.example.com/token",
		}
		require.NoError(t, s.StoreDCRCredentials(ctx, input))

		input.ClientID = "tampered-after-store"

		got, err := s.GetDCRCredentials(ctx, key)
		require.NoError(t, err)
		assert.Equal(t, "orig", got.ClientID)
	})
}

// TestMemoryStorage_DCRCredentials_ExcludedFromCleanupExpired pins the
// invariant that DCR entries are NOT swept by cleanupExpired. The Redis
// backend applies TTL via SetEX; the in-memory backend keeps entries for the
// process lifetime.
func TestMemoryStorage_DCRCredentials_ExcludedFromCleanupExpired(t *testing.T) {
	withStorage(t, func(ctx context.Context, s *MemoryStorage) {
		key := DCRKey{
			Issuer:      "https://idp.example.com",
			UpstreamID:  "https://upstream.example.com",
			RedirectURI: "https://x/cb",
			ScopesHash:  ScopesHash([]string{"openid"}),
		}
		require.NoError(t, s.StoreDCRCredentials(ctx, &DCRCredentials{
			Key:                   key,
			ClientID:              "abc",
			AuthorizationEndpoint: "https://idp.example.com/auth",
			TokenEndpoint:         "https://idp.example.com/token",
			CreatedAt:             time.Now().Add(-365 * 24 * time.Hour),
		}))

		s.cleanupExpired()

		got, err := s.GetDCRCredentials(ctx, key)
		require.NoError(t, err)
		assert.Equal(t, "abc", got.ClientID)
	})
}

// TestMemoryStorage_DCRCredentials_ConcurrentAccess fans out N goroutines
// performing alternating StoreDCRCredentials / GetDCRCredentials against
// overlapping and disjoint keys, exercising the sync.RWMutex guard
// advertised in the DCRCredentialStore contract. With go test -race this
// catches a future change that drops the lock or returns an internal
// pointer instead of a defensive copy.
//
// The test is bounded by a fail-fast deadline so a regression that
// deadlocks fails loudly rather than hanging until the global Go test
// timeout, per .claude/rules/testing.md.
func TestMemoryStorage_DCRCredentials_ConcurrentAccess(t *testing.T) {
	withStorage(t, func(ctx context.Context, s *MemoryStorage) {
		const (
			workers      = 16
			opsPerWorker = 200
		)

		// Two key spaces: overlapping (every worker writes the same keys, so
		// the lock must serialise their writes) and disjoint (each worker has
		// its own key space, so reads never see another worker's writes).
		overlappingKey := func(i int) DCRKey {
			return DCRKey{
				Issuer:      "https://idp.example.com",
				UpstreamID:  "https://upstream.example.com",
				RedirectURI: "https://thv.example.com/oauth/callback",
				ScopesHash:  fmt.Sprintf("overlap-%d", i%4),
			}
		}
		disjointKey := func(worker, i int) DCRKey {
			return DCRKey{
				Issuer:      fmt.Sprintf("https://idp-%d.example.com", worker),
				UpstreamID:  "https://upstream.example.com",
				RedirectURI: "https://thv.example.com/oauth/callback",
				ScopesHash:  fmt.Sprintf("disjoint-%d", i),
			}
		}
		mkCreds := func(key DCRKey, clientID string) *DCRCredentials {
			return &DCRCredentials{
				Key:                   key,
				ClientID:              clientID,
				AuthorizationEndpoint: "https://idp.example.com/auth",
				TokenEndpoint:         "https://idp.example.com/token",
			}
		}

		var errCount int32
		var wg sync.WaitGroup
		wg.Add(workers)
		for w := 0; w < workers; w++ {
			go func(worker int) {
				defer wg.Done()
				for i := 0; i < opsPerWorker; i++ {
					var key DCRKey
					if i%2 == 0 {
						key = overlappingKey(i)
					} else {
						key = disjointKey(worker, i)
					}
					if err := s.StoreDCRCredentials(ctx, mkCreds(key, fmt.Sprintf("worker-%d-op-%d", worker, i))); err != nil {
						atomic.AddInt32(&errCount, 1)
					}
					// The disjoint Get must always hit (the goroutine that
					// just wrote owns this key); the overlapping Get may
					// miss if another worker happens to be mid-Store, but
					// that's not an error.
					if _, err := s.GetDCRCredentials(ctx, key); err != nil && i%2 != 0 {
						atomic.AddInt32(&errCount, 1)
					}
				}
			}(w)
		}

		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()

		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("timeout waiting for concurrent DCR Store/Get to finish; possible deadlock")
		}

		assert.Zero(t, atomic.LoadInt32(&errCount),
			"no Store or disjoint-Get should have errored under concurrent access")
	})
}

// TestScopesHash_StableAcrossPermutationAndDuplicates pins the canonical-form
// invariants of ScopesHash.
func TestScopesHash_StableAcrossPermutationAndDuplicates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b []string
	}{
		{"two-element permutation", []string{"openid", "profile"}, []string{"profile", "openid"}},
		{"three-element permutation", []string{"openid", "profile", "email"}, []string{"email", "openid", "profile"}},
		// OAuth scope sets are sets, not multisets (RFC 6749 §3.3); duplicates
		// must canonicalise to the same hash.
		{"single equals double duplicate", []string{"openid"}, []string{"openid", "openid"}},
		{"three with duplicate equals two unique", []string{"openid", "profile", "openid"}, []string{"openid", "profile"}},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, ScopesHash(tc.a), ScopesHash(tc.b))
		})
	}
}

// TestScopesHash_NoCollisionFromBoundaryJoin guards against a regression in
// the canonical form: ["ab", "c"] and ["a", "bc"] must produce different
// hashes. The newline delimiter is what prevents the collision; this test
// exists to fail loudly if the canonical form is ever simplified.
func TestScopesHash_NoCollisionFromBoundaryJoin(t *testing.T) {
	t.Parallel()
	assert.NotEqual(t, ScopesHash([]string{"ab", "c"}), ScopesHash([]string{"a", "bc"}))
}

// TestScopesHash_DistinctForDistinctScopes pins that distinct non-empty
// scope sets hash to distinct values, while nil and empty slice canonicalise
// to the same value (both reduce to the empty canonical form).
func TestScopesHash_DistinctForDistinctScopes(t *testing.T) {
	t.Parallel()

	a := ScopesHash([]string{"openid"})
	b := ScopesHash([]string{"openid", "profile"})
	c := ScopesHash([]string{"profile"})
	d := ScopesHash(nil)
	e := ScopesHash([]string{})

	assert.NotEqual(t, a, b)
	assert.NotEqual(t, a, c)
	assert.NotEqual(t, b, c)
	assert.NotEqual(t, a, d)
	assert.Equal(t, d, e)
}

// seedBoundRow stores a live, fully-bound upstream token row for
// (sessionID, provider).
func seedBoundRow(ctx context.Context, t *testing.T, s *MemoryStorage, sessionID, provider string) {
	t.Helper()
	row := &UpstreamTokens{
		ProviderID:      provider,
		AccessToken:     "access-" + provider,
		RefreshToken:    "refresh-" + provider,
		ExpiresAt:       time.Now().Add(time.Hour),
		UserID:          "user-A",
		ClientID:        "client-A",
		UpstreamSubject: "subject-A",
	}
	require.NoError(t, s.StoreUpstreamTokens(ctx, sessionID, provider, row))
}

// TestMemoryStorage_UpstreamBinding covers the read-side binding matrix
// (scenarios 1-12) for the in-memory backend. Log-capturing cases live in
// TestMemoryStorage_UpstreamBinding_WarnLogs because slog.SetDefault is
// process-global.
func TestMemoryStorage_UpstreamBinding(t *testing.T) {
	t.Parallel()

	t.Run("scenario 1: matching ctx user, nil expected, row returned", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			seedBoundRow(ctx, t, s, "sess-1", "github")

			readCtx := ContextWithBindingUser(ctx, "user-A")
			got, err := s.GetUpstreamTokens(readCtx, "sess-1", "github", nil)
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, "user-A", got.UserID)
		})
	})

	t.Run("scenario 2: mismatched ctx user, nil expected, ErrInvalidBinding with nil tokens", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			seedBoundRow(ctx, t, s, "sess-2", "github")

			readCtx := ContextWithBindingUser(ctx, "user-B")
			got, err := s.GetUpstreamTokens(readCtx, "sess-2", "github", nil)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidBinding)
			assert.Nil(t, got, "mismatched row must not be released, even for refresh")
		})
	})

	t.Run("scenario 3: no identity in ctx, nil expected, row returned", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			seedBoundRow(ctx, t, s, "sess-3", "github")

			got, err := s.GetUpstreamTokens(ctx, "sess-3", "github", nil)
			require.NoError(t, err)
			require.NotNil(t, got)
		})
	})

	t.Run("scenario 4: expected UserID match and mismatch", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			seedBoundRow(ctx, t, s, "sess-4", "github")

			got, err := s.GetUpstreamTokens(ctx, "sess-4", "github", &ExpectedBinding{UserID: "user-A"})
			require.NoError(t, err)
			require.NotNil(t, got)

			got, err = s.GetUpstreamTokens(ctx, "sess-4", "github", &ExpectedBinding{UserID: "user-B"})
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidBinding)
			assert.Nil(t, got)
		})
	})

	t.Run("scenario 5: client ID binding variants", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			seedBoundRow(ctx, t, s, "sess-5", "github")

			// match
			got, err := s.GetUpstreamTokens(ctx, "sess-5", "github", &ExpectedBinding{ClientID: "client-A"})
			require.NoError(t, err)
			require.NotNil(t, got)

			// mismatch
			got, err = s.GetUpstreamTokens(ctx, "sess-5", "github", &ExpectedBinding{ClientID: "client-B"})
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidBinding)
			assert.Nil(t, got)

			// expected-empty skips the dimension
			got, err = s.GetUpstreamTokens(ctx, "sess-5", "github", &ExpectedBinding{UserID: "user-A"})
			require.NoError(t, err)
			require.NotNil(t, got)

			// stored-empty legacy row passes with an asserted client
			legacy := &UpstreamTokens{
				ProviderID: "github", AccessToken: "legacy-access",
				ExpiresAt: time.Now().Add(time.Hour), UserID: "user-A",
			}
			require.NoError(t, s.StoreUpstreamTokens(ctx, "sess-5b", "github", legacy))
			got, err = s.GetUpstreamTokens(ctx, "sess-5b", "github",
				&ExpectedBinding{UserID: "user-A", ClientID: "client-B"})
			require.NoError(t, err)
			require.NotNil(t, got)
		})
	})

	t.Run("scenario 6: upstream subject mismatch", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			seedBoundRow(ctx, t, s, "sess-6", "github")

			got, err := s.GetUpstreamTokens(ctx, "sess-6", "github", &ExpectedBinding{UpstreamSubject: "subject-B"})
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidBinding)
			assert.Nil(t, got)
		})
	})

	t.Run("scenario 7: expired + mismatched row yields ErrInvalidBinding, not ErrExpired", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			row := &UpstreamTokens{
				ProviderID: "github", AccessToken: "stale-access", RefreshToken: "stale-refresh",
				ExpiresAt: time.Now().Add(-time.Hour), UserID: "user-A",
			}
			require.NoError(t, s.StoreUpstreamTokens(ctx, "sess-7", "github", row))

			got, err := s.GetUpstreamTokens(ctx, "sess-7", "github", &ExpectedBinding{UserID: "user-B"})
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidBinding)
			assert.NotErrorIs(t, err, ErrExpired, "binding must win over expiry")
			assert.Nil(t, got, "no refresh material released for a mismatched row")
		})
	})

	t.Run("scenario 8: bulk read excludes only the mismatched provider", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			seedBoundRow(ctx, t, s, "sess-8", "github")
			other := &UpstreamTokens{
				ProviderID: "gitlab", AccessToken: "other-access",
				ExpiresAt: time.Now().Add(time.Hour), UserID: "user-B",
			}
			require.NoError(t, s.StoreUpstreamTokens(ctx, "sess-8", "gitlab", other))

			all, err := s.GetAllUpstreamTokens(ctx, "sess-8", &ExpectedBinding{UserID: "user-A"})
			require.NoError(t, err)
			require.Len(t, all, 1)
			assert.Contains(t, all, "github")
			assert.NotContains(t, all, "gitlab")
		})
	})

	t.Run("scenario 9: bulk read with all rows mismatched returns empty map, nil error", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			seedBoundRow(ctx, t, s, "sess-9", "github")
			seedBoundRow(ctx, t, s, "sess-9", "gitlab")

			all, err := s.GetAllUpstreamTokens(ctx, "sess-9", &ExpectedBinding{UserID: "user-B"})
			require.NoError(t, err)
			require.NotNil(t, all, "unknown-session semantics preserved: empty map, not nil")
			assert.Empty(t, all)
		})
	})

	t.Run("scenario 12: tombstone passes through the binding path", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			// Store nil tokens: the write path persists a deletion tombstone.
			require.NoError(t, s.StoreUpstreamTokens(ctx, "sess-12", "github", nil))

			got, err := s.GetUpstreamTokens(ctx, "sess-12", "github", &ExpectedBinding{UserID: "user-B"})
			require.NoError(t, err)
			assert.Nil(t, got, "tombstone semantics unchanged: (nil, nil), no panic")

			// Strict mode must not reject a tombstone either — there is no row
			// to bind, so the caller's tombstone handling applies.
			got, err = s.GetUpstreamTokens(ctx, "sess-12", "github", &ExpectedBinding{UserID: "user-B", Strict: true})
			require.NoError(t, err)
			assert.Nil(t, got)
		})
	})

	t.Run("strict mode: legacy row (empty stored UserID) fails closed, owned row passes", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			legacy := &UpstreamTokens{
				ProviderID: "github", AccessToken: "legacy-access", RefreshToken: "legacy-refresh",
				ExpiresAt: time.Now().Add(time.Hour),
			}
			require.NoError(t, s.StoreUpstreamTokens(ctx, "sess-strict", "github", legacy))

			// Permissive default: the legacy row is released (pre-Strict behavior).
			got, err := s.GetUpstreamTokens(ctx, "sess-strict", "github", &ExpectedBinding{UserID: "user-A"})
			require.NoError(t, err)
			require.NotNil(t, got)

			// Strict: the same row fails closed — no token material released.
			got, err = s.GetUpstreamTokens(ctx, "sess-strict", "github", &ExpectedBinding{UserID: "user-A", Strict: true})
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidBinding)
			assert.Nil(t, got, "strict mode must not release a row that cannot prove its owner")

			// Strict bulk read excludes the legacy row.
			all, err := s.GetAllUpstreamTokens(ctx, "sess-strict", &ExpectedBinding{UserID: "user-A", Strict: true})
			require.NoError(t, err)
			assert.NotContains(t, all, "github")

			// Strict with an owned row passes normally.
			seedBoundRow(ctx, t, s, "sess-strict-owned", "github")
			got, err = s.GetUpstreamTokens(ctx, "sess-strict-owned", "github",
				&ExpectedBinding{UserID: "user-A", Strict: true})
			require.NoError(t, err)
			require.NotNil(t, got)
		})
	})

	t.Run("scenario 11: equal-length and differing-length mismatches both fail", func(t *testing.T) {
		withStorage(t, func(ctx context.Context, s *MemoryStorage) {
			seedBoundRow(ctx, t, s, "sess-11", "github")

			_, err := s.GetUpstreamTokens(ctx, "sess-11", "github", &ExpectedBinding{UserID: "user-B"})
			assert.ErrorIs(t, err, ErrInvalidBinding, "equal-length mismatch")

			_, err = s.GetUpstreamTokens(ctx, "sess-11", "github", &ExpectedBinding{UserID: "user-A-plus-extra"})
			assert.ErrorIs(t, err, ErrInvalidBinding, "differing-length mismatch")
		})
	})
}

// TestMemoryStorage_UpstreamBinding_WarnLogs covers the log-asserting binding
// scenarios. It uses slog.SetDefault (process-global) and therefore does not
// run in parallel.
//
//nolint:paralleltest // captures slog default
func TestMemoryStorage_UpstreamBinding_WarnLogs(t *testing.T) {
	//nolint:paralleltest // captures slog default
	t.Run("scenario 8: bulk exclusion emits WARN with session, provider, dimension", func(t *testing.T) {
		s := NewMemoryStorage()
		t.Cleanup(func() { _ = s.Close() })
		ctx := context.Background()
		seedBoundRow(ctx, t, s, "sess-w8", "github")
		other := &UpstreamTokens{
			ProviderID: "gitlab", AccessToken: "other-access",
			ExpiresAt: time.Now().Add(time.Hour), UserID: "user-B",
		}
		require.NoError(t, s.StoreUpstreamTokens(ctx, "sess-w8", "gitlab", other))

		out := captureWarnLogs(t)
		all, err := s.GetAllUpstreamTokens(ctx, "sess-w8", &ExpectedBinding{UserID: "user-A"})
		require.NoError(t, err)
		require.Len(t, all, 1)
		assertBindingExclusionWarn(t, out.String(), "sess-w8", "gitlab", "user_id")
	})

	//nolint:paralleltest // captures slog default
	t.Run("scenario 10: legacy row returned with asserted user logs the unverified-owner WARN", func(t *testing.T) {
		s := NewMemoryStorage()
		t.Cleanup(func() { _ = s.Close() })
		ctx := context.Background()
		legacy := &UpstreamTokens{
			ProviderID: "github", AccessToken: "legacy-access",
			ExpiresAt: time.Now().Add(time.Hour),
		}
		require.NoError(t, s.StoreUpstreamTokens(ctx, "sess-w10", "github", legacy))

		out := captureWarnLogs(t)
		got, err := s.GetUpstreamTokens(ctx, "sess-w10", "github", &ExpectedBinding{UserID: "user-A"})
		require.NoError(t, err)
		require.NotNil(t, got, "legacy rows are released per the empty-field rule")
		// The caller asserted an owner the row cannot prove: exactly one WARN,
		// naming session and provider — never the exclusion WARN (nothing was
		// excluded) and never token material.
		assert.Contains(t, out.String(), "no recorded owner")
		assert.Contains(t, out.String(), "sess-w10")
		assert.Contains(t, out.String(), "github")
		assert.NotContains(t, out.String(), "binding validation failed")

		out.Reset()
		all, err := s.GetAllUpstreamTokens(ctx, "sess-w10", &ExpectedBinding{UserID: "user-A"})
		require.NoError(t, err)
		assert.Contains(t, all, "github")
		assert.Contains(t, out.String(), "no recorded owner")
		assert.NotContains(t, out.String(), "binding validation failed")
	})

	//nolint:paralleltest // captures slog default
	t.Run("scenario 10b: plain legacy passthrough (nil expected, no ctx user) logs nothing", func(t *testing.T) {
		s := NewMemoryStorage()
		t.Cleanup(func() { _ = s.Close() })
		ctx := context.Background()
		legacy := &UpstreamTokens{
			ProviderID: "github", AccessToken: "legacy-access",
			ExpiresAt: time.Now().Add(time.Hour),
		}
		require.NoError(t, s.StoreUpstreamTokens(ctx, "sess-w10b", "github", legacy))

		out := captureWarnLogs(t)
		got, err := s.GetUpstreamTokens(ctx, "sess-w10b", "github", nil)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Empty(t, out.String(), "legacy passthrough without an asserted user must not log (hot-path spam)")

		// An owned row with an asserted user is fully verified — no WARN either.
		seedBoundRow(ctx, t, s, "sess-w10c", "github")
		out.Reset()
		got, err = s.GetUpstreamTokens(ctx, "sess-w10c", "github", &ExpectedBinding{UserID: "user-A"})
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Empty(t, out.String(), "a fully-owned row matching the asserted user must not log")
	})
}
