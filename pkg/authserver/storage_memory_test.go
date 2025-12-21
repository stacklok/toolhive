package authserver

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

// mockClient implements fosite.Client for testing.
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

// mockRequester implements fosite.Requester for testing.
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
		id:                id,
		requestedAt:       time.Now(),
		client:            client,
		requestedScopes:   fosite.Arguments{"openid", "profile"},
		requestedAudience: fosite.Arguments{},
		grantedScopes:     fosite.Arguments{"openid"},
		grantedAudience:   fosite.Arguments{},
		form:              make(url.Values),
		session:           NewSession("test-subject", "test-idp-session", ""),
	}
}

// newMockRequesterWithExpiration creates a mock requester with specific expiration times.
func newMockRequesterWithExpiration(id string, client fosite.Client, tokenType fosite.TokenType, expiresAt time.Time) *mockRequester {
	session := NewSession("test-subject", "test-idp-session", "")
	session.SetExpiresAt(tokenType, expiresAt)

	return &mockRequester{
		id:                id,
		requestedAt:       time.Now(),
		client:            client,
		requestedScopes:   fosite.Arguments{"openid", "profile"},
		requestedAudience: fosite.Arguments{},
		grantedScopes:     fosite.Arguments{"openid"},
		grantedAudience:   fosite.Arguments{},
		form:              make(url.Values),
		session:           session,
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
	assert.NotNil(t, storage.idpTokens)
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

func TestMemoryStorage_RegisterClient(t *testing.T) {
	t.Parallel()

	storage := NewMemoryStorage()
	defer storage.Close()

	client := &mockClient{id: "test-client"}

	storage.RegisterClient(client)

	retrieved, err := storage.GetClient(context.Background(), "test-client")
	require.NoError(t, err)
	assert.Equal(t, client, retrieved)
}

// --- ClientManager Tests ---

func TestMemoryStorage_GetClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		clientID string
		setup    func(*MemoryStorage)
		wantErr  bool
	}{
		{
			name:     "existing client",
			clientID: "test-client",
			setup: func(s *MemoryStorage) {
				s.RegisterClient(&mockClient{id: "test-client"})
			},
			wantErr: false,
		},
		{
			name:     "non-existent client",
			clientID: "non-existent",
			setup:    func(_ *MemoryStorage) {},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			storage := NewMemoryStorage()
			defer storage.Close()

			tt.setup(storage)

			client, err := storage.GetClient(context.Background(), tt.clientID)
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, fosite.ErrNotFound)
				assert.Nil(t, client)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, client)
				assert.Equal(t, tt.clientID, client.GetID())
			}
		})
	}
}

func TestMemoryStorage_ClientAssertionJWT(t *testing.T) {
	t.Parallel()

	t.Run("unknown JTI is valid", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		err := storage.ClientAssertionJWTValid(ctx, "unknown-jti")
		require.NoError(t, err)
	})

	t.Run("known JTI is invalid", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		jti := "test-jti"
		exp := time.Now().Add(time.Hour)
		err := storage.SetClientAssertionJWT(ctx, jti, exp)
		require.NoError(t, err)

		err = storage.ClientAssertionJWTValid(ctx, jti)
		require.Error(t, err)
		assert.ErrorIs(t, err, fosite.ErrJTIKnown)
	})

	t.Run("expired JTI is valid", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		jti := "expired-jti"
		exp := time.Now().Add(-time.Hour) // Already expired
		err := storage.SetClientAssertionJWT(ctx, jti, exp)
		require.NoError(t, err)

		err = storage.ClientAssertionJWTValid(ctx, jti)
		require.NoError(t, err)
	})

	t.Run("cleanup expired JTIs on set", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		// Add an expired JTI
		storage.mu.Lock()
		storage.clientAssertionJWTs["old-jti"] = time.Now().Add(-time.Hour)
		storage.mu.Unlock()

		// Set a new JTI which should trigger cleanup
		err := storage.SetClientAssertionJWT(ctx, "new-jti", time.Now().Add(time.Hour))
		require.NoError(t, err)

		storage.mu.RLock()
		_, exists := storage.clientAssertionJWTs["old-jti"]
		storage.mu.RUnlock()
		assert.False(t, exists, "expired JTI should have been cleaned up")
	})
}

// --- AuthorizeCodeStorage Tests ---

func TestMemoryStorage_AuthorizeCodeSession(t *testing.T) {
	t.Parallel()

	t.Run("create and get", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		client := &mockClient{id: "test-client"}
		request := newMockRequester("req-1", client)

		code := "auth-code-123"
		err := storage.CreateAuthorizeCodeSession(ctx, code, request)
		require.NoError(t, err)

		retrieved, err := storage.GetAuthorizeCodeSession(ctx, code, nil)
		require.NoError(t, err)
		assert.Equal(t, request.GetID(), retrieved.GetID())
	})

	t.Run("get non-existent code", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		_, err := storage.GetAuthorizeCodeSession(ctx, "non-existent", nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, fosite.ErrNotFound)
	})

	t.Run("invalidate code", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		client := &mockClient{id: "test-client"}
		request := newMockRequester("req-1", client)

		code := "auth-code-to-invalidate"
		err := storage.CreateAuthorizeCodeSession(ctx, code, request)
		require.NoError(t, err)

		err = storage.InvalidateAuthorizeCodeSession(ctx, code)
		require.NoError(t, err)

		// Should still return the request along with the error
		retrieved, err := storage.GetAuthorizeCodeSession(ctx, code, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, fosite.ErrInvalidatedAuthorizeCode)
		assert.NotNil(t, retrieved, "must return request with invalidated error")
	})

	t.Run("invalidate non-existent code", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		err := storage.InvalidateAuthorizeCodeSession(ctx, "non-existent-code")
		require.Error(t, err)
		assert.ErrorIs(t, err, fosite.ErrNotFound)
	})
}

// --- AccessTokenStorage Tests ---

func TestMemoryStorage_AccessTokenSession(t *testing.T) {
	t.Parallel()

	t.Run("create and get", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		client := &mockClient{id: "test-client"}
		request := newMockRequester("req-1", client)

		signature := "access-token-sig-123"
		err := storage.CreateAccessTokenSession(ctx, signature, request)
		require.NoError(t, err)

		retrieved, err := storage.GetAccessTokenSession(ctx, signature, nil)
		require.NoError(t, err)
		assert.Equal(t, request.GetID(), retrieved.GetID())
	})

	t.Run("get non-existent token", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		_, err := storage.GetAccessTokenSession(ctx, "non-existent", nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, fosite.ErrNotFound)
	})

	t.Run("delete token", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		client := &mockClient{id: "test-client"}
		request := newMockRequester("req-1", client)

		signature := "access-token-to-delete"
		err := storage.CreateAccessTokenSession(ctx, signature, request)
		require.NoError(t, err)

		err = storage.DeleteAccessTokenSession(ctx, signature)
		require.NoError(t, err)

		_, err = storage.GetAccessTokenSession(ctx, signature, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, fosite.ErrNotFound)
	})

	t.Run("delete non-existent token returns error", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		// Delete should return error for non-existent tokens
		err := storage.DeleteAccessTokenSession(ctx, "non-existent-token")
		require.Error(t, err)
		assert.ErrorIs(t, err, fosite.ErrNotFound)
	})
}

// --- RefreshTokenStorage Tests ---

func TestMemoryStorage_RefreshTokenSession(t *testing.T) {
	t.Parallel()

	t.Run("create and get", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		client := &mockClient{id: "test-client"}
		request := newMockRequester("req-1", client)

		signature := "refresh-token-sig-123"
		err := storage.CreateRefreshTokenSession(ctx, signature, "access-sig", request)
		require.NoError(t, err)

		retrieved, err := storage.GetRefreshTokenSession(ctx, signature, nil)
		require.NoError(t, err)
		assert.Equal(t, request.GetID(), retrieved.GetID())
	})

	t.Run("get non-existent token", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		_, err := storage.GetRefreshTokenSession(ctx, "non-existent", nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, fosite.ErrNotFound)
	})

	t.Run("delete token", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		client := &mockClient{id: "test-client"}
		request := newMockRequester("req-1", client)

		signature := "refresh-token-to-delete"
		err := storage.CreateRefreshTokenSession(ctx, signature, "access-sig", request)
		require.NoError(t, err)

		err = storage.DeleteRefreshTokenSession(ctx, signature)
		require.NoError(t, err)

		_, err = storage.GetRefreshTokenSession(ctx, signature, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, fosite.ErrNotFound)
	})
}

func TestMemoryStorage_RotateRefreshToken(t *testing.T) {
	t.Parallel()

	t.Run("rotate deletes refresh and access tokens", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		client := &mockClient{id: "test-client"}

		requestID := "request-123"
		request := newMockRequester(requestID, client)

		refreshSig := "refresh-sig-123"
		accessSig := "access-sig-123"

		// Create tokens
		err := storage.CreateRefreshTokenSession(ctx, refreshSig, accessSig, request)
		require.NoError(t, err)
		err = storage.CreateAccessTokenSession(ctx, accessSig, request)
		require.NoError(t, err)

		// Rotate
		err = storage.RotateRefreshToken(ctx, requestID, refreshSig)
		require.NoError(t, err)

		// Both should be deleted
		_, err = storage.GetRefreshTokenSession(ctx, refreshSig, nil)
		assert.ErrorIs(t, err, fosite.ErrNotFound)

		_, err = storage.GetAccessTokenSession(ctx, accessSig, nil)
		assert.ErrorIs(t, err, fosite.ErrNotFound)
	})

	t.Run("rotate non-existent token (no error)", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		err := storage.RotateRefreshToken(ctx, "non-existent-request", "non-existent-sig")
		require.NoError(t, err)
	})
}

// --- PKCERequestStorage Tests ---

func TestMemoryStorage_PKCERequestSession(t *testing.T) {
	t.Parallel()

	t.Run("create and get", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		client := &mockClient{id: "test-client"}
		request := newMockRequester("req-1", client)

		signature := "pkce-sig-123"
		err := storage.CreatePKCERequestSession(ctx, signature, request)
		require.NoError(t, err)

		retrieved, err := storage.GetPKCERequestSession(ctx, signature, nil)
		require.NoError(t, err)
		assert.Equal(t, request.GetID(), retrieved.GetID())
	})

	t.Run("get non-existent", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		_, err := storage.GetPKCERequestSession(ctx, "non-existent", nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, fosite.ErrNotFound)
	})

	t.Run("delete", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		client := &mockClient{id: "test-client"}
		request := newMockRequester("req-1", client)

		signature := "pkce-to-delete"
		err := storage.CreatePKCERequestSession(ctx, signature, request)
		require.NoError(t, err)

		err = storage.DeletePKCERequestSession(ctx, signature)
		require.NoError(t, err)

		_, err = storage.GetPKCERequestSession(ctx, signature, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, fosite.ErrNotFound)
	})
}

// --- IDP Token Storage Tests ---

func TestMemoryStorage_IDPTokens(t *testing.T) {
	t.Parallel()

	t.Run("store and get", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		sessionID := "session-123"
		tokens := &IDPTokens{
			AccessToken:  "idp-access-token",
			RefreshToken: "idp-refresh-token",
			IDToken:      "idp-id-token",
			ExpiresAt:    time.Now().Add(time.Hour),
			Subject:      "user@example.com",
			ClientID:     "test-client-id",
		}

		err := storage.StoreIDPTokens(ctx, sessionID, tokens)
		require.NoError(t, err)

		retrieved, err := storage.GetIDPTokens(ctx, sessionID)
		require.NoError(t, err)
		assert.Equal(t, tokens.AccessToken, retrieved.AccessToken)
		assert.Equal(t, tokens.RefreshToken, retrieved.RefreshToken)
		assert.Equal(t, tokens.IDToken, retrieved.IDToken)
		assert.Equal(t, tokens.Subject, retrieved.Subject)
		assert.Equal(t, tokens.ClientID, retrieved.ClientID)
	})

	t.Run("get non-existent", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		_, err := storage.GetIDPTokens(ctx, "non-existent")
		require.Error(t, err)
		assert.ErrorIs(t, err, fosite.ErrNotFound)
	})

	t.Run("delete", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		sessionID := "session-to-delete"
		tokens := &IDPTokens{
			AccessToken: "test",
			Subject:     "user@example.com",
			ClientID:    "test-client",
		}
		err := storage.StoreIDPTokens(ctx, sessionID, tokens)
		require.NoError(t, err)

		err = storage.DeleteIDPTokens(ctx, sessionID)
		require.NoError(t, err)

		_, err = storage.GetIDPTokens(ctx, sessionID)
		require.Error(t, err)
		assert.ErrorIs(t, err, fosite.ErrNotFound)
	})

	t.Run("overwrite existing tokens", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		sessionID := "session-overwrite"
		tokens1 := &IDPTokens{
			AccessToken: "token-1",
			Subject:     "user1@example.com",
			ClientID:    "client-1",
		}
		tokens2 := &IDPTokens{
			AccessToken: "token-2",
			Subject:     "user2@example.com",
			ClientID:    "client-2",
		}

		err := storage.StoreIDPTokens(ctx, sessionID, tokens1)
		require.NoError(t, err)

		err = storage.StoreIDPTokens(ctx, sessionID, tokens2)
		require.NoError(t, err)

		retrieved, err := storage.GetIDPTokens(ctx, sessionID)
		require.NoError(t, err)
		assert.Equal(t, "token-2", retrieved.AccessToken)
		assert.Equal(t, "user2@example.com", retrieved.Subject)
		assert.Equal(t, "client-2", retrieved.ClientID)
	})

	t.Run("binding fields are stored and retrieved correctly", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		sessionID := "session-with-binding"
		tokens := &IDPTokens{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			IDToken:      "id-token",
			ExpiresAt:    time.Now().Add(time.Hour),
			Subject:      "subject-123",
			ClientID:     "client-abc",
		}

		err := storage.StoreIDPTokens(ctx, sessionID, tokens)
		require.NoError(t, err)

		retrieved, err := storage.GetIDPTokens(ctx, sessionID)
		require.NoError(t, err)

		// Verify binding fields are correctly stored
		assert.Equal(t, "subject-123", retrieved.Subject)
		assert.Equal(t, "client-abc", retrieved.ClientID)
	})
}

// --- TTL and Cleanup Tests ---

func TestMemoryStorage_CleanupExpired(t *testing.T) {
	t.Parallel()

	t.Run("cleanup expired auth codes", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		client := &mockClient{id: "test-client"}

		// Create an auth code with expired session
		expiredRequest := newMockRequesterWithExpiration("req-expired", client, fosite.AuthorizeCode, time.Now().Add(-time.Hour))
		err := storage.CreateAuthorizeCodeSession(ctx, "expired-code", expiredRequest)
		require.NoError(t, err)

		// Create an auth code with valid session
		validRequest := newMockRequesterWithExpiration("req-valid", client, fosite.AuthorizeCode, time.Now().Add(time.Hour))
		err = storage.CreateAuthorizeCodeSession(ctx, "valid-code", validRequest)
		require.NoError(t, err)

		// Verify both exist
		stats := storage.Stats()
		assert.Equal(t, 2, stats.AuthCodes)

		// Run cleanup
		storage.cleanupExpired()

		// Verify only valid one remains
		stats = storage.Stats()
		assert.Equal(t, 1, stats.AuthCodes)

		_, err = storage.GetAuthorizeCodeSession(ctx, "expired-code", nil)
		assert.ErrorIs(t, err, fosite.ErrNotFound)

		_, err = storage.GetAuthorizeCodeSession(ctx, "valid-code", nil)
		assert.NoError(t, err)
	})

	t.Run("cleanup expired access tokens", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		client := &mockClient{id: "test-client"}

		// Create an expired access token
		expiredRequest := newMockRequesterWithExpiration("req-expired", client, fosite.AccessToken, time.Now().Add(-time.Hour))
		err := storage.CreateAccessTokenSession(ctx, "expired-token", expiredRequest)
		require.NoError(t, err)

		// Create a valid access token
		validRequest := newMockRequesterWithExpiration("req-valid", client, fosite.AccessToken, time.Now().Add(time.Hour))
		err = storage.CreateAccessTokenSession(ctx, "valid-token", validRequest)
		require.NoError(t, err)

		// Verify both exist
		stats := storage.Stats()
		assert.Equal(t, 2, stats.AccessTokens)

		// Run cleanup
		storage.cleanupExpired()

		// Verify only valid one remains
		stats = storage.Stats()
		assert.Equal(t, 1, stats.AccessTokens)

		_, err = storage.GetAccessTokenSession(ctx, "expired-token", nil)
		assert.ErrorIs(t, err, fosite.ErrNotFound)

		_, err = storage.GetAccessTokenSession(ctx, "valid-token", nil)
		assert.NoError(t, err)
	})

	t.Run("cleanup expired refresh tokens", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		client := &mockClient{id: "test-client"}

		// Create an expired refresh token
		expiredRequest := newMockRequesterWithExpiration("req-expired", client, fosite.RefreshToken, time.Now().Add(-time.Hour))
		err := storage.CreateRefreshTokenSession(ctx, "expired-token", "access-sig", expiredRequest)
		require.NoError(t, err)

		// Create a valid refresh token
		validRequest := newMockRequesterWithExpiration("req-valid", client, fosite.RefreshToken, time.Now().Add(time.Hour))
		err = storage.CreateRefreshTokenSession(ctx, "valid-token", "access-sig", validRequest)
		require.NoError(t, err)

		// Run cleanup
		storage.cleanupExpired()

		// Verify only valid one remains
		stats := storage.Stats()
		assert.Equal(t, 1, stats.RefreshTokens)
	})

	t.Run("cleanup expired PKCE requests", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		client := &mockClient{id: "test-client"}

		// Create an expired PKCE request
		expiredRequest := newMockRequesterWithExpiration("req-expired", client, fosite.AuthorizeCode, time.Now().Add(-time.Hour))
		err := storage.CreatePKCERequestSession(ctx, "expired-pkce", expiredRequest)
		require.NoError(t, err)

		// Create a valid PKCE request
		validRequest := newMockRequesterWithExpiration("req-valid", client, fosite.AuthorizeCode, time.Now().Add(time.Hour))
		err = storage.CreatePKCERequestSession(ctx, "valid-pkce", validRequest)
		require.NoError(t, err)

		// Run cleanup
		storage.cleanupExpired()

		// Verify only valid one remains
		stats := storage.Stats()
		assert.Equal(t, 1, stats.PKCERequests)
	})

	t.Run("cleanup expired IDP tokens", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		// Create expired IDP tokens
		expiredTokens := &IDPTokens{
			AccessToken: "expired",
			ExpiresAt:   time.Now().Add(-time.Hour),
		}
		err := storage.StoreIDPTokens(ctx, "expired-session", expiredTokens)
		require.NoError(t, err)

		// Create valid IDP tokens
		validTokens := &IDPTokens{
			AccessToken: "valid",
			ExpiresAt:   time.Now().Add(time.Hour),
		}
		err = storage.StoreIDPTokens(ctx, "valid-session", validTokens)
		require.NoError(t, err)

		// Run cleanup
		storage.cleanupExpired()

		// Verify only valid one remains
		stats := storage.Stats()
		assert.Equal(t, 1, stats.IDPTokens)

		_, err = storage.GetIDPTokens(ctx, "expired-session")
		assert.ErrorIs(t, err, fosite.ErrNotFound)

		_, err = storage.GetIDPTokens(ctx, "valid-session")
		assert.NoError(t, err)
	})

	t.Run("cleanup expired invalidated codes", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		client := &mockClient{id: "test-client"}

		// Create and invalidate an auth code, then manually expire the invalidation entry
		request := newMockRequesterWithExpiration("req-1", client, fosite.AuthorizeCode, time.Now().Add(time.Hour))
		err := storage.CreateAuthorizeCodeSession(ctx, "code-1", request)
		require.NoError(t, err)
		err = storage.InvalidateAuthorizeCodeSession(ctx, "code-1")
		require.NoError(t, err)

		// Manually expire the invalidated code entry
		storage.mu.Lock()
		if entry, ok := storage.invalidatedCodes["code-1"]; ok {
			entry.expiresAt = time.Now().Add(-time.Hour)
		}
		storage.mu.Unlock()

		// Verify the invalidated code exists
		stats := storage.Stats()
		assert.Equal(t, 1, stats.InvalidatedCodes)

		// Run cleanup
		storage.cleanupExpired()

		// Verify the invalidated code entry is removed
		stats = storage.Stats()
		assert.Equal(t, 0, stats.InvalidatedCodes)
	})

	t.Run("cleanup expired client assertion JWTs", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		storage := NewMemoryStorage()
		defer storage.Close()

		// Add an expired JTI
		err := storage.SetClientAssertionJWT(ctx, "expired-jti", time.Now().Add(-time.Hour))
		require.NoError(t, err)

		// Add a valid JTI
		err = storage.SetClientAssertionJWT(ctx, "valid-jti", time.Now().Add(time.Hour))
		require.NoError(t, err)

		// Run cleanup
		storage.cleanupExpired()

		// Verify only valid one remains
		stats := storage.Stats()
		assert.Equal(t, 1, stats.ClientAssertionJWTs)

		// The expired one should be considered valid (can be reused)
		err = storage.ClientAssertionJWTValid(ctx, "expired-jti")
		assert.NoError(t, err)

		// The valid one should still be known
		err = storage.ClientAssertionJWTValid(ctx, "valid-jti")
		assert.ErrorIs(t, err, fosite.ErrJTIKnown)
	})
}

func TestMemoryStorage_CleanupLoop(t *testing.T) {
	t.Parallel()

	t.Run("cleanup runs periodically", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		// Use a very short interval for testing
		storage := NewMemoryStorage(WithCleanupInterval(50 * time.Millisecond))
		defer storage.Close()

		client := &mockClient{id: "test-client"}

		// Create an expired auth code
		expiredRequest := newMockRequesterWithExpiration("req-expired", client, fosite.AuthorizeCode, time.Now().Add(-time.Hour))
		err := storage.CreateAuthorizeCodeSession(ctx, "expired-code", expiredRequest)
		require.NoError(t, err)

		// Verify it exists
		stats := storage.Stats()
		assert.Equal(t, 1, stats.AuthCodes)

		// Wait for cleanup to run
		time.Sleep(100 * time.Millisecond)

		// Verify it was cleaned up
		stats = storage.Stats()
		assert.Equal(t, 0, stats.AuthCodes)
	})

	t.Run("close stops cleanup goroutine", func(t *testing.T) {
		t.Parallel()

		storage := NewMemoryStorage(WithCleanupInterval(10 * time.Millisecond))

		// Close should not block
		done := make(chan struct{})
		go func() {
			storage.Close()
			close(done)
		}()

		select {
		case <-done:
			// Success - Close returned
		case <-time.After(1 * time.Second):
			t.Fatal("Close did not return in time")
		}
	})
}

func TestMemoryStorage_Stats(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	storage := NewMemoryStorage()
	defer storage.Close()

	// Initially empty
	stats := storage.Stats()
	assert.Equal(t, 0, stats.Clients)
	assert.Equal(t, 0, stats.AuthCodes)
	assert.Equal(t, 0, stats.AccessTokens)
	assert.Equal(t, 0, stats.RefreshTokens)
	assert.Equal(t, 0, stats.PKCERequests)
	assert.Equal(t, 0, stats.IDPTokens)
	assert.Equal(t, 0, stats.InvalidatedCodes)
	assert.Equal(t, 0, stats.ClientAssertionJWTs)

	// Add some items
	client := &mockClient{id: "test-client"}
	storage.RegisterClient(client)

	request := newMockRequester("req-1", client)
	_ = storage.CreateAuthorizeCodeSession(ctx, "code-1", request)
	_ = storage.CreateAccessTokenSession(ctx, "access-1", request)
	_ = storage.CreateRefreshTokenSession(ctx, "refresh-1", "access-1", request)
	_ = storage.CreatePKCERequestSession(ctx, "pkce-1", request)
	_ = storage.StoreIDPTokens(ctx, "idp-1", &IDPTokens{AccessToken: "test"})
	_ = storage.InvalidateAuthorizeCodeSession(ctx, "code-1")
	_ = storage.SetClientAssertionJWT(ctx, "jti-1", time.Now().Add(time.Hour))

	stats = storage.Stats()
	assert.Equal(t, 1, stats.Clients)
	assert.Equal(t, 1, stats.AuthCodes)
	assert.Equal(t, 1, stats.AccessTokens)
	assert.Equal(t, 1, stats.RefreshTokens)
	assert.Equal(t, 1, stats.PKCERequests)
	assert.Equal(t, 1, stats.IDPTokens)
	assert.Equal(t, 1, stats.InvalidatedCodes)
	assert.Equal(t, 1, stats.ClientAssertionJWTs)
}

func TestGetExpirationFromRequester(t *testing.T) {
	t.Parallel()

	t.Run("nil requester returns default", func(t *testing.T) {
		t.Parallel()

		defaultTTL := time.Hour
		before := time.Now()
		exp := getExpirationFromRequester(nil, fosite.AccessToken, defaultTTL)
		after := time.Now()

		assert.True(t, exp.After(before.Add(defaultTTL-time.Second)))
		assert.True(t, exp.Before(after.Add(defaultTTL+time.Second)))
	})

	t.Run("nil session returns default", func(t *testing.T) {
		t.Parallel()

		request := &mockRequester{session: nil}
		defaultTTL := time.Hour
		before := time.Now()
		exp := getExpirationFromRequester(request, fosite.AccessToken, defaultTTL)
		after := time.Now()

		assert.True(t, exp.After(before.Add(defaultTTL-time.Second)))
		assert.True(t, exp.Before(after.Add(defaultTTL+time.Second)))
	})

	t.Run("zero expiration returns default", func(t *testing.T) {
		t.Parallel()

		// Session without expiration set
		session := NewSession("test", "idp", "")
		request := &mockRequester{session: session}
		defaultTTL := time.Hour
		before := time.Now()
		exp := getExpirationFromRequester(request, fosite.AccessToken, defaultTTL)
		after := time.Now()

		assert.True(t, exp.After(before.Add(defaultTTL-time.Second)))
		assert.True(t, exp.Before(after.Add(defaultTTL+time.Second)))
	})

	t.Run("valid expiration is returned", func(t *testing.T) {
		t.Parallel()

		expectedExp := time.Now().Add(2 * time.Hour)
		session := NewSession("test", "idp", "")
		session.SetExpiresAt(fosite.AccessToken, expectedExp)
		request := &mockRequester{session: session}

		exp := getExpirationFromRequester(request, fosite.AccessToken, time.Hour)

		assert.WithinDuration(t, expectedExp, exp, time.Second)
	})
}

// --- Concurrent Access Tests ---

func TestMemoryStorage_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	t.Run("concurrent writes", func(t *testing.T) {
		t.Parallel()

		storage := NewMemoryStorage()
		defer storage.Close()

		ctx := context.Background()
		client := &mockClient{id: "test-client"}

		var wg sync.WaitGroup
		numGoroutines := 100

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				request := newMockRequester(fmt.Sprintf("req-%d", idx), client)
				code := fmt.Sprintf("code-%d", idx)
				_ = storage.CreateAuthorizeCodeSession(ctx, code, request)
			}(i)
		}

		wg.Wait()
	})

	t.Run("concurrent reads and writes", func(t *testing.T) {
		t.Parallel()

		storage := NewMemoryStorage()
		defer storage.Close()

		ctx := context.Background()
		client := &mockClient{id: "test-client"}

		// Pre-populate some data
		for i := 0; i < 10; i++ {
			request := newMockRequester("preload-req", client)
			_ = storage.CreateAccessTokenSession(ctx, fmt.Sprintf("preload-token-%d", i), request)
		}

		var wg sync.WaitGroup
		numGoroutines := 100

		for i := 0; i < numGoroutines; i++ {
			wg.Add(2)
			// Writer
			go func(idx int) {
				defer wg.Done()
				request := newMockRequester(fmt.Sprintf("req-%d", idx), client)
				_ = storage.CreateAccessTokenSession(ctx, fmt.Sprintf("token-%d", idx), request)
			}(i)
			// Reader
			go func(idx int) {
				defer wg.Done()
				_, _ = storage.GetAccessTokenSession(ctx, fmt.Sprintf("preload-token-%d", idx%10), nil)
			}(i)
		}

		wg.Wait()
	})

	t.Run("concurrent client registration and lookup", func(t *testing.T) {
		t.Parallel()

		storage := NewMemoryStorage()
		defer storage.Close()

		ctx := context.Background()

		var wg sync.WaitGroup
		numGoroutines := 50

		for i := 0; i < numGoroutines; i++ {
			wg.Add(2)
			// Register clients
			go func(idx int) {
				defer wg.Done()
				c := &mockClient{id: fmt.Sprintf("client-%d", idx)}
				storage.RegisterClient(c)
			}(i)
			// Look up clients (may or may not exist yet)
			go func(idx int) {
				defer wg.Done()
				_, _ = storage.GetClient(ctx, fmt.Sprintf("client-%d", idx))
			}(i)
		}

		wg.Wait()

		// Verify all clients were successfully registered
		for i := 0; i < numGoroutines; i++ {
			client, err := storage.GetClient(ctx, fmt.Sprintf("client-%d", i))
			require.NoError(t, err, "client-%d should exist", i)
			require.NotNil(t, client, "client-%d should not be nil", i)
			assert.Equal(t, fmt.Sprintf("client-%d", i), client.GetID())
		}
	})

	t.Run("concurrent cleanup with writes", func(t *testing.T) {
		t.Parallel()

		storage := NewMemoryStorage()
		defer storage.Close()

		ctx := context.Background()
		client := &mockClient{id: "test-client"}

		var wg sync.WaitGroup
		numGoroutines := 50

		for i := 0; i < numGoroutines; i++ {
			wg.Add(2)
			// Writer
			go func(idx int) {
				defer wg.Done()
				request := newMockRequester(fmt.Sprintf("req-%d", idx), client)
				_ = storage.CreateAccessTokenSession(ctx, fmt.Sprintf("token-%d", idx), request)
			}(i)
			// Cleanup trigger
			go func(_ int) {
				defer wg.Done()
				storage.cleanupExpired()
			}(i)
		}

		wg.Wait()
	})
}

// --- Input Validation Tests ---

func TestMemoryStorage_InputValidation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &mockClient{id: "test-client"}

	t.Run("CreateAuthorizeCodeSession empty code", func(t *testing.T) {
		t.Parallel()

		storage := NewMemoryStorage()
		defer storage.Close()

		request := newMockRequester("req-1", client)
		err := storage.CreateAuthorizeCodeSession(ctx, "", request)
		require.Error(t, err)
		require.ErrorIs(t, err, fosite.ErrInvalidRequest)
	})

	t.Run("CreateAuthorizeCodeSession nil request", func(t *testing.T) {
		t.Parallel()

		storage := NewMemoryStorage()
		defer storage.Close()

		err := storage.CreateAuthorizeCodeSession(ctx, "valid-code", nil)
		require.Error(t, err)
		require.ErrorIs(t, err, fosite.ErrInvalidRequest)
	})

	t.Run("CreateAccessTokenSession empty signature", func(t *testing.T) {
		t.Parallel()

		storage := NewMemoryStorage()
		defer storage.Close()

		request := newMockRequester("req-1", client)
		err := storage.CreateAccessTokenSession(ctx, "", request)
		require.Error(t, err)
		require.ErrorIs(t, err, fosite.ErrInvalidRequest)
	})

	t.Run("CreateAccessTokenSession nil request", func(t *testing.T) {
		t.Parallel()

		storage := NewMemoryStorage()
		defer storage.Close()

		err := storage.CreateAccessTokenSession(ctx, "valid-signature", nil)
		require.Error(t, err)
		require.ErrorIs(t, err, fosite.ErrInvalidRequest)
	})

	t.Run("CreateRefreshTokenSession empty signature", func(t *testing.T) {
		t.Parallel()

		storage := NewMemoryStorage()
		defer storage.Close()

		request := newMockRequester("req-1", client)
		err := storage.CreateRefreshTokenSession(ctx, "", "access-sig", request)
		require.Error(t, err)
		require.ErrorIs(t, err, fosite.ErrInvalidRequest)
	})

	t.Run("CreateRefreshTokenSession nil request", func(t *testing.T) {
		t.Parallel()

		storage := NewMemoryStorage()
		defer storage.Close()

		err := storage.CreateRefreshTokenSession(ctx, "valid-signature", "access-sig", nil)
		require.Error(t, err)
		require.ErrorIs(t, err, fosite.ErrInvalidRequest)
	})

	t.Run("CreatePKCERequestSession empty signature", func(t *testing.T) {
		t.Parallel()

		storage := NewMemoryStorage()
		defer storage.Close()

		request := newMockRequester("req-1", client)
		err := storage.CreatePKCERequestSession(ctx, "", request)
		require.Error(t, err)
		require.ErrorIs(t, err, fosite.ErrInvalidRequest)
	})

	t.Run("CreatePKCERequestSession nil request", func(t *testing.T) {
		t.Parallel()

		storage := NewMemoryStorage()
		defer storage.Close()

		err := storage.CreatePKCERequestSession(ctx, "valid-signature", nil)
		require.Error(t, err)
		require.ErrorIs(t, err, fosite.ErrInvalidRequest)
	})

	t.Run("StoreIDPTokens empty sessionID", func(t *testing.T) {
		t.Parallel()

		storage := NewMemoryStorage()
		defer storage.Close()

		tokens := &IDPTokens{
			AccessToken: "test-token",
			Subject:     "user@example.com",
			ClientID:    "test-client",
		}
		err := storage.StoreIDPTokens(ctx, "", tokens)
		require.Error(t, err)
		require.ErrorIs(t, err, fosite.ErrInvalidRequest)
	})

	t.Run("StoreIDPTokens nil tokens is valid", func(t *testing.T) {
		t.Parallel()

		storage := NewMemoryStorage()
		defer storage.Close()

		// nil tokens is valid per the code - it stores a defensive copy
		err := storage.StoreIDPTokens(ctx, "valid-session-id", nil)
		require.NoError(t, err)

		// Verify it was stored
		retrieved, err := storage.GetIDPTokens(ctx, "valid-session-id")
		require.NoError(t, err)
		assert.Nil(t, retrieved)
	})

	t.Run("StorePendingAuthorization empty state", func(t *testing.T) {
		t.Parallel()

		storage := NewMemoryStorage()
		defer storage.Close()

		pending := &PendingAuthorization{
			ClientID:      "test-client",
			RedirectURI:   "https://example.com/callback",
			State:         "client-state",
			InternalState: "internal-state",
		}
		err := storage.StorePendingAuthorization(ctx, "", pending)
		require.Error(t, err)
		require.ErrorIs(t, err, fosite.ErrInvalidRequest)
	})

	t.Run("StorePendingAuthorization nil pending", func(t *testing.T) {
		t.Parallel()

		storage := NewMemoryStorage()
		defer storage.Close()

		err := storage.StorePendingAuthorization(ctx, "valid-state", nil)
		require.Error(t, err)
		require.ErrorIs(t, err, fosite.ErrInvalidRequest)
	})
}

// --- Interface Compliance Test ---

func TestMemoryStorage_ImplementsStorage(t *testing.T) {
	t.Parallel()

	// This test verifies that MemoryStorage implements the Storage interface
	var _ Storage = (*MemoryStorage)(nil)
}
