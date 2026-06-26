// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

func TestNextMissingUpstream(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setupTokens func(st *testStorageState)
		want        string
		wantErr     bool
	}{
		{
			name: "all satisfied",
			setupTokens: func(st *testStorageState) {
				st.upstreamTokens["test-session:provider-1"] = &storage.UpstreamTokens{
					ProviderID:  "provider-1",
					AccessToken: "tok-1",
					ExpiresAt:   time.Now().Add(time.Hour),
				}
				st.upstreamTokens["test-session:provider-2"] = &storage.UpstreamTokens{
					ProviderID:  "provider-2",
					AccessToken: "tok-2",
					ExpiresAt:   time.Now().Add(time.Hour),
				}
			},
			want: "",
		},
		{
			name:        "first missing",
			setupTokens: func(_ *testStorageState) {},
			want:        "provider-1",
		},
		{
			name: "provider-1 satisfied, provider-2 missing",
			setupTokens: func(st *testStorageState) {
				st.upstreamTokens["test-session:provider-1"] = &storage.UpstreamTokens{
					ProviderID:  "provider-1",
					AccessToken: "tok-1",
					ExpiresAt:   time.Now().Add(time.Hour),
				}
			},
			want: "provider-2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler, storState, _, _ := multiUpstreamTestSetup(t)
			tt.setupTokens(storState)

			got, err := handler.nextMissingUpstream(context.Background(), "test-session")
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNextMissingUpstream_RefreshExpired(t *testing.T) {
	t.Parallel()

	// provider-1 has an expired token (with a refresh token); provider-2 is live.
	// Presence alone would mark provider-1 satisfied; the refresh-then-classify
	// path must decide based on whether the expired leg can be refreshed.
	expiredProvider1 := func(st *testStorageState) {
		st.upstreamTokens["test-session:provider-1"] = &storage.UpstreamTokens{
			ProviderID:   "provider-1",
			AccessToken:  "tok-1",
			RefreshToken: "rt-1",
			ExpiresAt:    time.Now().Add(-time.Minute),
		}
		st.upstreamTokens["test-session:provider-2"] = &storage.UpstreamTokens{
			ProviderID:  "provider-2",
			AccessToken: "tok-2",
			ExpiresAt:   time.Now().Add(time.Hour),
		}
	}

	tests := []struct {
		name string
		// setupRefresher configures the mock refresher's expectations. Nil means no
		// refresher is wired into the handler at all.
		setupRefresher func(*mocks.MockUpstreamTokenRefresher)
		setupTokens    func(*testStorageState)
		want           string
	}{
		{
			name: "expired leg refreshed successfully is satisfied",
			setupRefresher: func(r *mocks.MockUpstreamTokenRefresher) {
				r.EXPECT().RefreshAndStore(gomock.Any(), "test-session", gomock.Any()).
					Return(&storage.UpstreamTokens{ProviderID: "provider-1", AccessToken: "tok-1-new"}, nil).
					Times(1)
			},
			setupTokens: expiredProvider1,
			want:        "",
		},
		{
			name: "expired leg whose refresh fails is reported missing",
			setupRefresher: func(r *mocks.MockUpstreamTokenRefresher) {
				r.EXPECT().RefreshAndStore(gomock.Any(), "test-session", gomock.Any()).
					Return(nil, errors.New("refresh token expired")).
					Times(1)
			},
			setupTokens: expiredProvider1,
			want:        "provider-1",
		},
		{
			name: "expired leg with no refresh token is reported missing without calling refresher",
			setupRefresher: func(_ *mocks.MockUpstreamTokenRefresher) {
				// RefreshAndStore must not be called when there is no refresh token.
			},
			setupTokens: func(st *testStorageState) {
				st.upstreamTokens["test-session:provider-1"] = &storage.UpstreamTokens{
					ProviderID:  "provider-1",
					AccessToken: "tok-1",
					ExpiresAt:   time.Now().Add(-time.Minute),
				}
			},
			want: "provider-1",
		},
		{
			name:           "expired leg with no refresher configured is reported missing",
			setupRefresher: nil,
			setupTokens:    expiredProvider1,
			want:           "provider-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var opts []Option
			if tt.setupRefresher != nil {
				ctrl := gomock.NewController(t)
				t.Cleanup(ctrl.Finish)
				refresher := mocks.NewMockUpstreamTokenRefresher(ctrl)
				tt.setupRefresher(refresher)
				opts = append(opts, WithUpstreamRefresher(refresher))
			}

			handler, storState, _, _ := multiUpstreamTestSetup(t, opts...)
			tt.setupTokens(storState)

			got, err := handler.nextMissingUpstream(context.Background(), "test-session")
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNextMissingUpstream_StorageError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(func() {
		ctrl.Finish()
	})

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
		AllowedAudiences:     []string{"https://api.example.com"},
	}

	oauth2Config, err := server.NewAuthorizationServerConfig(cfg)
	require.NoError(t, err)

	stor := mocks.NewMockStorage(ctrl)

	storageErr := errors.New("connection refused")
	stor.EXPECT().GetAllUpstreamTokens(gomock.Any(), gomock.Any()).Return(nil, storageErr).Times(1)

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

	mockUpstream1 := &mockIDPProvider{providerType: upstream.ProviderTypeOAuth2}
	mockUpstream2 := &mockIDPProvider{providerType: upstream.ProviderTypeOAuth2}

	handler, err := NewHandler(provider, oauth2Config, stor,
		[]NamedUpstream{
			{Name: "provider-1", Provider: mockUpstream1},
			{Name: "provider-2", Provider: mockUpstream2},
		},
	)
	require.NoError(t, err)

	got, err := handler.nextMissingUpstream(context.Background(), "test-session")
	require.Error(t, err)
	assert.ErrorContains(t, err, "failed to check upstream token state")
	assert.ErrorIs(t, err, storageErr)
	assert.Empty(t, got)
}

func TestHandler_issuer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   *server.AuthorizationServerConfig
		expected string
	}{
		{
			name: "returns configured AccessTokenIssuer",
			config: &server.AuthorizationServerConfig{
				Config: &fosite.Config{AccessTokenIssuer: "https://as.example.com"},
			},
			expected: "https://as.example.com",
		},
		{
			name: "returns empty string when AccessTokenIssuer is unset",
			config: &server.AuthorizationServerConfig{
				Config: &fosite.Config{},
			},
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := &Handler{config: tc.config}
			assert.Equal(t, tc.expected, h.issuer())
		})
	}
}
