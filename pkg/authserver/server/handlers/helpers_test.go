// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

const (
	testAuthClientID    = "test-auth-client"
	testAuthRedirectURI = "http://localhost:8080/callback"
	testAuthIssuer      = "http://test-auth-issuer"
	testInternalState   = "internal-state-123"
)

// mockIDPProvider implements upstream.OAuth2Provider for testing.
type mockIDPProvider struct {
	providerType           upstream.ProviderType
	authorizationURL       string
	authURLErr             error
	exchangeTokens         *upstream.Tokens
	exchangeErr            error
	userInfo               *upstream.UserInfo
	userInfoErr            error
	refreshTokens          *upstream.Tokens
	refreshErr             error
	resolveIdentitySubject string
	resolveIdentityErr     error
	capturedState          string
	capturedCode           string
	capturedCodeChallenge  string
	capturedCodeVerifier   string
}

// Compile-time interface check.
var _ upstream.OAuth2Provider = (*mockIDPProvider)(nil)

func (m *mockIDPProvider) Type() upstream.ProviderType {
	if m.providerType == "" {
		return upstream.ProviderTypeOAuth2
	}
	return m.providerType
}

func (m *mockIDPProvider) AuthorizationURL(state, codeChallenge string, _ ...upstream.AuthorizationOption) (string, error) {
	m.capturedState = state
	m.capturedCodeChallenge = codeChallenge
	// Note: We can't easily extract nonce from options since the internal type is private.
	// The tests that need to verify nonce will set it directly via the mock setup.
	// For the authorize handler test, we capture nonce separately by having the handler
	// pass it via WithAdditionalParams and we verify it's non-empty via pending auth storage.
	if m.authURLErr != nil {
		return "", m.authURLErr
	}
	return m.authorizationURL + "?state=" + state, nil
}

func (m *mockIDPProvider) ExchangeCode(_ context.Context, code, codeVerifier string) (*upstream.Tokens, error) {
	m.capturedCode = code
	m.capturedCodeVerifier = codeVerifier
	if m.exchangeErr != nil {
		return nil, m.exchangeErr
	}
	return m.exchangeTokens, nil
}

func (m *mockIDPProvider) RefreshTokens(_ context.Context, _ string) (*upstream.Tokens, error) {
	if m.refreshErr != nil {
		return nil, m.refreshErr
	}
	return m.refreshTokens, nil
}

func (m *mockIDPProvider) FetchUserInfo(_ context.Context, _ string) (*upstream.UserInfo, error) {
	if m.userInfoErr != nil {
		return nil, m.userInfoErr
	}
	return m.userInfo, nil
}

func (m *mockIDPProvider) ResolveIdentity(_ context.Context, _ *upstream.Tokens, _ string) (string, error) {
	if m.resolveIdentityErr != nil {
		return "", m.resolveIdentityErr
	}
	// Return explicitly set subject if provided
	if m.resolveIdentitySubject != "" {
		return m.resolveIdentitySubject, nil
	}
	// Fallback: return userInfo subject if available
	if m.userInfo != nil && m.userInfo.Subject != "" {
		return m.userInfo.Subject, nil
	}
	// Default for tests
	return "user-123", nil
}

// testStorageState holds the in-memory state for testing.
type testStorageState struct {
	pendingAuths       map[string]*storage.PendingAuthorization
	upstreamTokens     map[string]*storage.UpstreamTokens
	clients            map[string]fosite.Client
	users              map[string]*storage.User
	providerIdentities map[string]*storage.ProviderIdentity // key: providerID:providerSubject
	idpTokenCount      int
}

// handlerTestSetup creates a test setup with all dependencies including an upstream provider.
func handlerTestSetup(t *testing.T) (*Handler, *testStorageState, *mockIDPProvider) {
	t.Helper()

	ctrl := gomock.NewController(t)
	t.Cleanup(func() {
		ctrl.Finish()
	})

	// Generate RSA key for testing
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	secret := make([]byte, 32)
	_, err = rand.Read(secret)
	require.NoError(t, err)

	cfg := &server.AuthorizationServerParams{
		Issuer:               testAuthIssuer,
		AccessTokenLifespan:  time.Hour,
		RefreshTokenLifespan: time.Hour * 24,
		AuthCodeLifespan:     time.Minute * 10,
		HMACSecrets:          servercrypto.NewHMACSecrets(secret),
		SigningKeyID:         "test-key-1",
		SigningKeyAlgorithm:  "RS256",
		SigningKey:           rsaKey,
	}

	oauth2Config, err := server.NewAuthorizationServerConfig(cfg)
	require.NoError(t, err)

	// Create mock storage with in-memory state
	storState := &testStorageState{
		pendingAuths:       make(map[string]*storage.PendingAuthorization),
		upstreamTokens:     make(map[string]*storage.UpstreamTokens),
		clients:            make(map[string]fosite.Client),
		users:              make(map[string]*storage.User),
		providerIdentities: make(map[string]*storage.ProviderIdentity),
	}

	stor := mocks.NewMockStorage(ctrl)

	// Register a test client (public client for PKCE)
	testClient := &fosite.DefaultClient{
		ID:            testAuthClientID,
		Secret:        nil, // public client
		RedirectURIs:  []string{testAuthRedirectURI},
		ResponseTypes: []string{"code"},
		GrantTypes:    []string{"authorization_code", "refresh_token"},
		Scopes:        []string{"openid", "profile", "email"},
		Public:        true,
	}
	storState.clients[testAuthClientID] = testClient

	// Setup mock expectations for GetClient
	stor.EXPECT().GetClient(gomock.Any(), testAuthClientID).DoAndReturn(func(_ context.Context, id string) (fosite.Client, error) {
		if c, ok := storState.clients[id]; ok {
			return c, nil
		}
		return nil, fosite.ErrNotFound
	}).AnyTimes()
	stor.EXPECT().GetClient(gomock.Any(), gomock.Not(testAuthClientID)).Return(nil, fosite.ErrNotFound).AnyTimes()

	// Setup mock expectations for pending authorization storage
	stor.EXPECT().StorePendingAuthorization(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, state string, pending *storage.PendingAuthorization) error {
			if state == "" {
				return storage.ErrNotFound
			}
			if pending == nil {
				return storage.ErrNotFound
			}
			storState.pendingAuths[state] = pending
			return nil
		}).AnyTimes()

	stor.EXPECT().LoadPendingAuthorization(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, state string) (*storage.PendingAuthorization, error) {
			if p, ok := storState.pendingAuths[state]; ok {
				return p, nil
			}
			return nil, storage.ErrNotFound
		}).AnyTimes()

	stor.EXPECT().DeletePendingAuthorization(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, state string) error {
			if _, ok := storState.pendingAuths[state]; !ok {
				return storage.ErrNotFound
			}
			delete(storState.pendingAuths, state)
			return nil
		}).AnyTimes()

	// Setup mock expectations for upstream tokens storage
	stor.EXPECT().StoreUpstreamTokens(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, sessionID string, tokens *storage.UpstreamTokens) error {
			storState.upstreamTokens[sessionID] = tokens
			storState.idpTokenCount++
			return nil
		}).AnyTimes()

	stor.EXPECT().DeleteUpstreamTokens(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, sessionID string) error {
			delete(storState.upstreamTokens, sessionID)
			return nil
		}).AnyTimes()

	// Setup mock expectations for authorization code storage (needed by fosite)
	stor.EXPECT().CreateAuthorizeCodeSession(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	stor.EXPECT().GetAuthorizeCodeSession(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fosite.ErrNotFound).AnyTimes()
	stor.EXPECT().InvalidateAuthorizeCodeSession(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	// Setup mock expectations for PKCE storage (needed by fosite)
	stor.EXPECT().CreatePKCERequestSession(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	stor.EXPECT().GetPKCERequestSession(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fosite.ErrNotFound).AnyTimes()
	stor.EXPECT().DeletePKCERequestSession(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	// Setup mock expectations for user storage (needed by UserResolver)
	stor.EXPECT().CreateUser(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, user *storage.User) error {
			storState.users[user.ID] = user
			return nil
		}).AnyTimes()

	stor.EXPECT().GetUser(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, id string) (*storage.User, error) {
			if user, ok := storState.users[id]; ok {
				return user, nil
			}
			return nil, storage.ErrNotFound
		}).AnyTimes()

	stor.EXPECT().GetProviderIdentity(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, providerID, providerSubject string) (*storage.ProviderIdentity, error) {
			key := providerID + ":" + providerSubject
			if identity, ok := storState.providerIdentities[key]; ok {
				return identity, nil
			}
			return nil, storage.ErrNotFound
		}).AnyTimes()

	stor.EXPECT().CreateProviderIdentity(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, identity *storage.ProviderIdentity) error {
			key := identity.ProviderID + ":" + identity.ProviderSubject
			storState.providerIdentities[key] = identity
			return nil
		}).AnyTimes()

	stor.EXPECT().UpdateProviderIdentityLastUsed(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, providerID, providerSubject string, lastUsedAt time.Time) error {
			key := providerID + ":" + providerSubject
			if identity, ok := storState.providerIdentities[key]; ok {
				identity.LastUsedAt = lastUsedAt
				return nil
			}
			return storage.ErrNotFound
		}).AnyTimes()

	stor.EXPECT().DeleteUser(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, id string) error {
			if _, ok := storState.users[id]; !ok {
				return storage.ErrNotFound
			}
			delete(storState.users, id)
			return nil
		}).AnyTimes()

	// Create fosite provider with authorization code support
	jwtStrategy := compose.NewOAuth2JWTStrategy(
		func(_ context.Context) (any, error) {
			return rsaKey, nil
		},
		compose.NewOAuth2HMACStrategy(oauth2Config.Config),
		oauth2Config.Config,
	)

	provider := compose.Compose(
		oauth2Config.Config,
		stor,
		&compose.CommonStrategy{CoreStrategy: jwtStrategy},
		compose.OAuth2AuthorizeExplicitFactory,
		compose.OAuth2RefreshTokenGrantFactory,
		compose.OAuth2PKCEFactory,
	)

	mockUpstream := &mockIDPProvider{
		providerType:     upstream.ProviderTypeOAuth2,
		authorizationURL: "https://idp.example.com/authorize",
		exchangeTokens: &upstream.Tokens{
			AccessToken:  "upstream-access-token",
			RefreshToken: "upstream-refresh-token",
			IDToken:      "upstream-id-token",
			ExpiresAt:    time.Now().Add(time.Hour),
		},
		userInfo: &upstream.UserInfo{
			Subject: "user-123",
			Email:   "user@example.com",
			Name:    "Test User",
		},
	}

	handler := NewHandler(provider, oauth2Config, stor, mockUpstream)

	return handler, storState, mockUpstream
}
