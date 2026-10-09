// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstreamtoken

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	storagemocks "github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
)

func TestInProcessService_GetValidTokens(t *testing.T) {
	t.Parallel()

	validTokens := &storage.UpstreamTokens{
		ProviderID:      "github",
		AccessToken:     "valid-access-token",
		RefreshToken:    "refresh-token",
		IDToken:         "valid-id-token",
		ExpiresAt:       time.Now().Add(1 * time.Hour),
		UserID:          "user-1",
		UpstreamSubject: "sub-1",
		ClientID:        "client-1",
	}

	expiredTokens := &storage.UpstreamTokens{
		ProviderID:      "github",
		AccessToken:     "expired-access-token",
		RefreshToken:    "refresh-token",
		IDToken:         "login-id-token",
		ExpiresAt:       time.Now().Add(-1 * time.Hour),
		UserID:          "user-1",
		UpstreamSubject: "sub-1",
		ClientID:        "client-1",
	}

	expiredNoRefresh := &storage.UpstreamTokens{
		ProviderID:      "github",
		AccessToken:     "expired-access-token",
		RefreshToken:    "",
		ExpiresAt:       time.Now().Add(-1 * time.Hour),
		UserID:          "user-1",
		UpstreamSubject: "sub-1",
		ClientID:        "client-1",
	}

	refreshedTokens := &storage.UpstreamTokens{
		ProviderID:      "github",
		AccessToken:     "new-access-token",
		RefreshToken:    "new-refresh-token",
		ExpiresAt:       time.Now().Add(1 * time.Hour),
		UserID:          "user-1",
		UpstreamSubject: "sub-1",
		ClientID:        "client-1",
	}

	tests := []struct {
		name           string
		sessionID      string
		setupStorage   func(*storagemocks.MockUpstreamTokenStorage)
		setupRefresher func(*storagemocks.MockUpstreamTokenRefresher)
		wantToken      string
		wantIDToken    string
		wantErr        error
		wantAnyErr     bool // expect an error but not a specific sentinel
	}{
		{
			name:      "valid tokens returned directly",
			sessionID: "session-1",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-1", "default").
					Return(validTokens, nil)
			},
			setupRefresher: func(_ *storagemocks.MockUpstreamTokenRefresher) {},
			wantToken:      "valid-access-token",
			wantIDToken:    "valid-id-token",
		},
		{
			// The refresh response omits id_token (refreshedTokens has none), so
			// the original login ID token must be carried through (OIDC §12.2).
			name:      "expired tokens refreshed via storage ErrExpired",
			sessionID: "session-2",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-2", "default").
					Return(expiredTokens, storage.ErrExpired)
			},
			setupRefresher: func(r *storagemocks.MockUpstreamTokenRefresher) {
				r.EXPECT().RefreshAndStore(gomock.Any(), "session-2", expiredTokens).
					Return(refreshedTokens, nil)
			},
			wantToken:   "new-access-token",
			wantIDToken: "login-id-token",
		},
		{
			name:      "expired tokens refreshed via IsExpired check",
			sessionID: "session-3",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				// Storage returns expired tokens without error (defense in depth path)
				s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-3", "default").
					Return(expiredTokens, nil)
			},
			setupRefresher: func(r *storagemocks.MockUpstreamTokenRefresher) {
				r.EXPECT().RefreshAndStore(gomock.Any(), "session-3", expiredTokens).
					Return(refreshedTokens, nil)
			},
			wantToken:   "new-access-token",
			wantIDToken: "login-id-token",
		},
		{
			name:      "session not found",
			sessionID: "session-4",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-4", "default").
					Return(nil, storage.ErrNotFound)
			},
			setupRefresher: func(_ *storagemocks.MockUpstreamTokenRefresher) {},
			wantErr:        ErrSessionNotFound,
		},
		{
			name:      "expired with no refresh token",
			sessionID: "session-5",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-5", "default").
					Return(expiredNoRefresh, storage.ErrExpired)
			},
			setupRefresher: func(_ *storagemocks.MockUpstreamTokenRefresher) {},
			wantErr:        ErrNoRefreshToken,
		},
		{
			name:      "refresh fails",
			sessionID: "session-6",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-6", "default").
					Return(expiredTokens, storage.ErrExpired)
			},
			setupRefresher: func(r *storagemocks.MockUpstreamTokenRefresher) {
				r.EXPECT().RefreshAndStore(gomock.Any(), "session-6", expiredTokens).
					Return(nil, errors.New("upstream IDP unavailable"))
			},
			wantErr: ErrRefreshFailed,
		},
		{
			name:      "storage error propagated",
			sessionID: "session-7",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-7", "default").
					Return(nil, errors.New("redis connection lost"))
			},
			setupRefresher: func(_ *storagemocks.MockUpstreamTokenRefresher) {},
			wantAnyErr:     true,
		},
		{
			name:      "ErrExpired with nil tokens returns ErrNoRefreshToken",
			sessionID: "session-8",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-8", "default").
					Return(nil, storage.ErrExpired)
			},
			setupRefresher: func(_ *storagemocks.MockUpstreamTokenRefresher) {},
			wantErr:        ErrNoRefreshToken,
		},
		{
			name:      "invalid binding returns ErrInvalidBinding",
			sessionID: "session-9",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-9", "default").
					Return(nil, storage.ErrInvalidBinding)
			},
			setupRefresher: func(_ *storagemocks.MockUpstreamTokenRefresher) {},
			wantErr:        ErrInvalidBinding,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)

			mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
			mockRefresher := storagemocks.NewMockUpstreamTokenRefresher(ctrl)

			tt.setupStorage(mockStorage)
			tt.setupRefresher(mockRefresher)

			svc := NewInProcessService(mockStorage, mockRefresher)

			cred, err := svc.GetValidTokens(context.Background(), tt.sessionID, "default")

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, cred)
				return
			}
			if tt.wantAnyErr {
				require.Error(t, err)
				assert.Nil(t, cred)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, cred)
			assert.Equal(t, tt.wantToken, cred.AccessToken)
			assert.Equal(t, tt.wantIDToken, cred.IDToken)
		})
	}
}

// TestInProcessService_NilRefresher verifies the documented nil-refresher
// constructor path: when refresh is intentionally not configured, expired
// tokens with a refresh token still return ErrNoRefreshToken (not a panic).
func TestInProcessService_NilRefresher(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)

	expiredTokens := &storage.UpstreamTokens{
		AccessToken:  "expired-access-token",
		RefreshToken: "has-refresh-token",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	}

	mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	mockStorage.EXPECT().
		GetUpstreamTokens(gomock.Any(), "session-1", "default").
		Return(expiredTokens, storage.ErrExpired)

	svc := NewInProcessService(mockStorage, nil)

	cred, err := svc.GetValidTokens(context.Background(), "session-1", "default")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoRefreshToken)
	assert.Nil(t, cred)
}

func TestInProcessService_GetAllUpstreamCredentials(t *testing.T) {
	t.Parallel()

	freshTokens := &storage.UpstreamTokens{
		ProviderID:  "github",
		AccessToken: "github-access-token",
		IDToken:     "github-id-token",
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	}
	freshTokens2 := &storage.UpstreamTokens{
		ProviderID:  "atlassian",
		AccessToken: "atlassian-access-token",
		IDToken:     "atlassian-id-token",
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	}
	expiredTokens := &storage.UpstreamTokens{
		ProviderID:   "github",
		AccessToken:  "expired-github-token",
		RefreshToken: "github-refresh-token",
		IDToken:      "github-id-token",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	}
	expiredTokensAtlassian := &storage.UpstreamTokens{
		ProviderID:   "atlassian",
		AccessToken:  "expired-atlassian-token",
		RefreshToken: "atlassian-refresh-token",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	}
	refreshedTokens := &storage.UpstreamTokens{
		ProviderID:  "github",
		AccessToken: "new-github-token",
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	}
	refreshedTokensWithID := &storage.UpstreamTokens{
		ProviderID:  "github",
		AccessToken: "new-github-token",
		IDToken:     "rotated-github-id-token",
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	}

	tests := []struct {
		name           string
		sessionID      string
		setupStorage   func(*storagemocks.MockUpstreamTokenStorage)
		setupRefresher func(*storagemocks.MockUpstreamTokenRefresher)
		wantResult     map[string]UpstreamCredential
		wantFailed     []string
		wantErr        bool
	}{
		{
			name:      "all fresh tokens returned directly with IDs",
			sessionID: "session-1",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetAllUpstreamTokens(gomock.Any(), "session-1").
					Return(map[string]*storage.UpstreamTokens{
						"github":    freshTokens,
						"atlassian": freshTokens2,
					}, nil)
			},
			setupRefresher: func(_ *storagemocks.MockUpstreamTokenRefresher) {},
			wantResult: map[string]UpstreamCredential{
				"github":    {AccessToken: "github-access-token", IDToken: "github-id-token"},
				"atlassian": {AccessToken: "atlassian-access-token", IDToken: "atlassian-id-token"},
			},
		},
		{
			name:      "mixed fresh and expired with successful refresh carries ID token through",
			sessionID: "session-2",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetAllUpstreamTokens(gomock.Any(), "session-2").
					Return(map[string]*storage.UpstreamTokens{
						"atlassian": freshTokens2,
						"github":    expiredTokens,
					}, nil)
			},
			setupRefresher: func(r *storagemocks.MockUpstreamTokenRefresher) {
				r.EXPECT().RefreshAndStore(gomock.Any(), "session-2", expiredTokens).
					Return(refreshedTokens, nil)
			},
			wantResult: map[string]UpstreamCredential{
				"atlassian": {AccessToken: "atlassian-access-token", IDToken: "atlassian-id-token"},
				"github":    {AccessToken: "new-github-token", IDToken: "github-id-token"},
			},
		},
		{
			// When the refresh response rotates the id_token (OIDC §12.2), the
			// refreshed ID token must be preferred over the original login one.
			name:      "expired refresh that rotates id_token prefers refreshed ID token",
			sessionID: "session-11",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetAllUpstreamTokens(gomock.Any(), "session-11").
					Return(map[string]*storage.UpstreamTokens{
						"github": expiredTokens,
					}, nil)
			},
			setupRefresher: func(r *storagemocks.MockUpstreamTokenRefresher) {
				r.EXPECT().RefreshAndStore(gomock.Any(), "session-11", expiredTokens).
					Return(refreshedTokensWithID, nil)
			},
			wantResult: map[string]UpstreamCredential{
				"github": {AccessToken: "new-github-token", IDToken: "rotated-github-id-token"},
			},
		},
		{
			name:      "expired refresh fails reports provider in failed slice",
			sessionID: "session-3",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetAllUpstreamTokens(gomock.Any(), "session-3").
					Return(map[string]*storage.UpstreamTokens{
						"github": expiredTokens,
					}, nil)
			},
			setupRefresher: func(r *storagemocks.MockUpstreamTokenRefresher) {
				r.EXPECT().RefreshAndStore(gomock.Any(), "session-3", expiredTokens).
					Return(nil, errors.New("upstream IDP unavailable"))
			},
			wantResult: map[string]UpstreamCredential{},
			wantFailed: []string{"github"},
		},
		{
			name:      "multiple providers both fail refresh — all reported in failed slice",
			sessionID: "session-3b",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetAllUpstreamTokens(gomock.Any(), "session-3b").
					Return(map[string]*storage.UpstreamTokens{
						"github":    expiredTokens,
						"atlassian": expiredTokensAtlassian,
					}, nil)
			},
			setupRefresher: func(r *storagemocks.MockUpstreamTokenRefresher) {
				r.EXPECT().RefreshAndStore(gomock.Any(), "session-3b", expiredTokens).
					Return(nil, errors.New("github IDP unavailable"))
				r.EXPECT().RefreshAndStore(gomock.Any(), "session-3b", expiredTokensAtlassian).
					Return(nil, errors.New("atlassian IDP unavailable"))
			},
			wantResult: map[string]UpstreamCredential{},
			wantFailed: []string{"github", "atlassian"},
		},
		{
			name:      "empty session returns empty map",
			sessionID: "session-4",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetAllUpstreamTokens(gomock.Any(), "session-4").
					Return(map[string]*storage.UpstreamTokens{}, nil)
			},
			setupRefresher: func(_ *storagemocks.MockUpstreamTokenRefresher) {},
			wantResult:     map[string]UpstreamCredential{},
		},
		{
			name:      "storage error propagated",
			sessionID: "session-5",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetAllUpstreamTokens(gomock.Any(), "session-5").
					Return(nil, errors.New("redis connection lost"))
			},
			setupRefresher: func(_ *storagemocks.MockUpstreamTokenRefresher) {},
			wantErr:        true,
		},
		{
			name:      "nil tokens entry skipped",
			sessionID: "session-6",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetAllUpstreamTokens(gomock.Any(), "session-6").
					Return(map[string]*storage.UpstreamTokens{
						"github":    freshTokens,
						"atlassian": nil,
					}, nil)
			},
			setupRefresher: func(_ *storagemocks.MockUpstreamTokenRefresher) {},
			wantResult: map[string]UpstreamCredential{
				"github": {AccessToken: "github-access-token", IDToken: "github-id-token"},
			},
		},
		{
			name:      "expired with no refresh token reports provider in failed slice",
			sessionID: "session-7",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetAllUpstreamTokens(gomock.Any(), "session-7").
					Return(map[string]*storage.UpstreamTokens{
						"github": {
							ProviderID:   "github",
							AccessToken:  "expired-no-refresh",
							IDToken:      "github-id-token",
							ExpiresAt:    time.Now().Add(-1 * time.Hour),
							RefreshToken: "",
						},
					}, nil)
			},
			setupRefresher: func(_ *storagemocks.MockUpstreamTokenRefresher) {},
			wantResult:     map[string]UpstreamCredential{},
			wantFailed:     []string{"github"},
		},
		{
			name:      "zero ExpiresAt treated as non-expiring",
			sessionID: "session-8",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetAllUpstreamTokens(gomock.Any(), "session-8").
					Return(map[string]*storage.UpstreamTokens{
						"github": {
							ProviderID:  "github",
							AccessToken: "no-expiry-token",
							IDToken:     "github-id-token",
							ExpiresAt:   time.Time{},
						},
					}, nil)
			},
			setupRefresher: func(_ *storagemocks.MockUpstreamTokenRefresher) {},
			wantResult: map[string]UpstreamCredential{
				"github": {AccessToken: "no-expiry-token", IDToken: "github-id-token"},
			},
		},
		{
			name:      "missing ID token carried through as empty string",
			sessionID: "session-9",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetAllUpstreamTokens(gomock.Any(), "session-9").
					Return(map[string]*storage.UpstreamTokens{
						"github": {
							ProviderID:  "github",
							AccessToken: "no-id-token",
							IDToken:     "",
							ExpiresAt:   time.Now().Add(1 * time.Hour),
						},
					}, nil)
			},
			setupRefresher: func(_ *storagemocks.MockUpstreamTokenRefresher) {},
			wantResult: map[string]UpstreamCredential{
				"github": {AccessToken: "no-id-token", IDToken: ""},
			},
		},
		{
			// All providers have valid access tokens but none have ID tokens.
			// GetAllUpstreamCredentials MUST still return every provider with an
			// empty IDToken field — the decision to project empty ID tokens out
			// of Identity.UpstreamIDTokens is made in the middleware layer
			// (pkg/auth/token.go), not here.
			name:      "all access tokens present with all empty ID tokens returns every provider",
			sessionID: "session-10",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetAllUpstreamTokens(gomock.Any(), "session-10").
					Return(map[string]*storage.UpstreamTokens{
						"github": {
							ProviderID:  "github",
							AccessToken: "gh-access",
							IDToken:     "",
							ExpiresAt:   time.Now().Add(1 * time.Hour),
						},
						"atlassian": {
							ProviderID:  "atlassian",
							AccessToken: "atl-access",
							IDToken:     "",
							ExpiresAt:   time.Now().Add(1 * time.Hour),
						},
					}, nil)
			},
			setupRefresher: func(_ *storagemocks.MockUpstreamTokenRefresher) {},
			wantResult: map[string]UpstreamCredential{
				"github":    {AccessToken: "gh-access", IDToken: ""},
				"atlassian": {AccessToken: "atl-access", IDToken: ""},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)

			mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
			mockRefresher := storagemocks.NewMockUpstreamTokenRefresher(ctrl)

			tt.setupStorage(mockStorage)
			tt.setupRefresher(mockRefresher)

			svc := NewInProcessService(mockStorage, mockRefresher)

			result, failed, err := svc.GetAllUpstreamCredentials(context.Background(), tt.sessionID)

			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantResult, result)
			assert.ElementsMatch(t, tt.wantFailed, failed)
		})
	}
}

// TestInProcessService_GetAllUpstreamCredentials_NilRefresher verifies that
// when the refresher is nil, expired tokens in the bulk path are omitted
// (not panicking).
func TestInProcessService_GetAllUpstreamCredentials_NilRefresher(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)

	mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	mockStorage.EXPECT().
		GetAllUpstreamTokens(gomock.Any(), "session-1").
		Return(map[string]*storage.UpstreamTokens{
			"github": {
				ProviderID:   "github",
				AccessToken:  "expired-token",
				RefreshToken: "has-refresh",
				IDToken:      "github-id-token",
				ExpiresAt:    time.Now().Add(-1 * time.Hour),
			},
		}, nil)

	svc := NewInProcessService(mockStorage, nil)

	result, failed, err := svc.GetAllUpstreamCredentials(context.Background(), "session-1")

	require.NoError(t, err)
	assert.Equal(t, map[string]UpstreamCredential{}, result)
	assert.Equal(t, []string{"github"}, failed)
}

// TestInProcessService_RefreshOnExpiredIDToken covers the opt-in ID-token
// staleness trigger on both read paths. Each case performs one read per entry
// in rows; the refresher mock fails the test on any refresh it did not expect.
func TestInProcessService_RefreshOnExpiredIDToken(t *testing.T) {
	t.Parallel()

	expiredIDToken := signedIDToken(t, time.Now().Add(-10*time.Minute))
	freshIDToken := signedIDToken(t, time.Now().Add(time.Hour))
	rotatedIDToken := signedIDToken(t, time.Now().Add(time.Hour))

	staleRow := &storage.UpstreamTokens{
		ProviderID:   "entra",
		AccessToken:  "valid-access-token",
		RefreshToken: "refresh-token",
		IDToken:      expiredIDToken,
		ExpiresAt:    time.Now().Add(30 * time.Minute),
	}
	staleRowNoRefresh := &storage.UpstreamTokens{
		ProviderID:  "entra",
		AccessToken: "valid-access-token",
		IDToken:     expiredIDToken,
		ExpiresAt:   time.Now().Add(30 * time.Minute),
	}
	freshRow := &storage.UpstreamTokens{
		ProviderID:   "entra",
		AccessToken:  "valid-access-token",
		RefreshToken: "refresh-token",
		IDToken:      freshIDToken,
		ExpiresAt:    time.Now().Add(30 * time.Minute),
	}
	rotatedRow := &storage.UpstreamTokens{
		ProviderID:   "entra",
		AccessToken:  "new-access-token",
		RefreshToken: "refresh-token",
		IDToken:      rotatedIDToken,
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	// What the refresher persists when the provider omits id_token: the
	// expired login ID token is carried forward alongside a new access token.
	carriedForwardRow := &storage.UpstreamTokens{
		ProviderID:   "entra",
		AccessToken:  "new-access-token",
		RefreshToken: "refresh-token",
		IDToken:      expiredIDToken,
		ExpiresAt:    time.Now().Add(time.Hour),
	}

	tests := []struct {
		name           string
		enabled        bool
		rows           []*storage.UpstreamTokens
		setupRefresher func(*storagemocks.MockUpstreamTokenRefresher)
		want           []UpstreamCredential
	}{
		{
			name:    "enabled: expired ID token triggers one refresh and returns the rotated ID token",
			enabled: true,
			rows:    []*storage.UpstreamTokens{staleRow},
			setupRefresher: func(r *storagemocks.MockUpstreamTokenRefresher) {
				r.EXPECT().RefreshAndStore(gomock.Any(), "session-1", staleRow).
					Return(rotatedRow, nil).Times(1)
			},
			want: []UpstreamCredential{{AccessToken: "new-access-token", IDToken: rotatedIDToken}},
		},
		{
			name:           "enabled: no refresh token returns the stored expired ID token without refreshing",
			enabled:        true,
			rows:           []*storage.UpstreamTokens{staleRowNoRefresh},
			setupRefresher: func(*storagemocks.MockUpstreamTokenRefresher) {},
			want:           []UpstreamCredential{{AccessToken: "valid-access-token", IDToken: expiredIDToken}},
		},
		{
			name:    "enabled: omitted id_token is carried forward and not refreshed again before access-token expiry",
			enabled: true,
			rows:    []*storage.UpstreamTokens{staleRow, carriedForwardRow},
			setupRefresher: func(r *storagemocks.MockUpstreamTokenRefresher) {
				r.EXPECT().RefreshAndStore(gomock.Any(), "session-1", staleRow).
					Return(carriedForwardRow, nil).Times(1)
			},
			want: []UpstreamCredential{
				{AccessToken: "new-access-token", IDToken: expiredIDToken},
				{AccessToken: "new-access-token", IDToken: expiredIDToken},
			},
		},
		{
			name:    "enabled: failed refresh returns stored tokens and is not retried before access-token expiry",
			enabled: true,
			rows:    []*storage.UpstreamTokens{staleRow, staleRow},
			setupRefresher: func(r *storagemocks.MockUpstreamTokenRefresher) {
				r.EXPECT().RefreshAndStore(gomock.Any(), "session-1", staleRow).
					Return(nil, errors.New("upstream unavailable")).Times(1)
			},
			want: []UpstreamCredential{
				{AccessToken: "valid-access-token", IDToken: expiredIDToken},
				{AccessToken: "valid-access-token", IDToken: expiredIDToken},
			},
		},
		{
			name:           "enabled: unexpired ID token does not trigger a refresh",
			enabled:        true,
			rows:           []*storage.UpstreamTokens{freshRow},
			setupRefresher: func(*storagemocks.MockUpstreamTokenRefresher) {},
			want:           []UpstreamCredential{{AccessToken: "valid-access-token", IDToken: freshIDToken}},
		},
		{
			name:           "disabled: expired ID token with valid access token does not trigger a refresh",
			enabled:        false,
			rows:           []*storage.UpstreamTokens{staleRow},
			setupRefresher: func(*storagemocks.MockUpstreamTokenRefresher) {},
			want:           []UpstreamCredential{{AccessToken: "valid-access-token", IDToken: expiredIDToken}},
		},
	}

	readers := []struct {
		name string
		read func(t *testing.T, svc *InProcessService) UpstreamCredential
		// expect registers the storage call for a single read returning row.
		expect func(s *storagemocks.MockUpstreamTokenStorage, row *storage.UpstreamTokens) *gomock.Call
	}{
		{
			name: "GetValidTokens",
			read: func(t *testing.T, svc *InProcessService) UpstreamCredential {
				t.Helper()
				cred, err := svc.GetValidTokens(context.Background(), "session-1", "entra")
				require.NoError(t, err)
				require.NotNil(t, cred)
				return *cred
			},
			expect: func(s *storagemocks.MockUpstreamTokenStorage, row *storage.UpstreamTokens) *gomock.Call {
				return s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-1", "entra").Return(row, nil)
			},
		},
		{
			name: "GetAllUpstreamCredentials",
			read: func(t *testing.T, svc *InProcessService) UpstreamCredential {
				t.Helper()
				creds, failed, err := svc.GetAllUpstreamCredentials(context.Background(), "session-1")
				require.NoError(t, err)
				assert.Empty(t, failed)
				require.Contains(t, creds, "entra")
				return creds["entra"]
			},
			expect: func(s *storagemocks.MockUpstreamTokenStorage, row *storage.UpstreamTokens) *gomock.Call {
				return s.EXPECT().GetAllUpstreamTokens(gomock.Any(), "session-1").
					Return(map[string]*storage.UpstreamTokens{"entra": row}, nil)
			},
		},
	}

	for _, tt := range tests {
		for _, rd := range readers {
			t.Run(tt.name+"/"+rd.name, func(t *testing.T) {
				t.Parallel()

				ctrl := gomock.NewController(t)
				mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
				mockRefresher := storagemocks.NewMockUpstreamTokenRefresher(ctrl)

				calls := make([]any, 0, len(tt.rows))
				for _, row := range tt.rows {
					calls = append(calls, rd.expect(mockStorage, row).Times(1))
				}
				gomock.InOrder(calls...)
				tt.setupRefresher(mockRefresher)

				opts := []Option{}
				if tt.enabled {
					opts = append(opts, WithRefreshOnExpiredIDToken())
				}
				svc := NewInProcessService(mockStorage, mockRefresher, opts...)

				for i := range tt.rows {
					assert.Equal(t, tt.want[i], rd.read(t, svc), "read %d", i)
				}
			})
		}
	}
}

// signedIDToken returns a JWT whose only claim is exp. The signature is
// irrelevant: the service reads exp without verifying it.
func signedIDToken(t *testing.T, exp time.Time) string {
	t.Helper()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"exp": exp.Unix()}).
		SignedString([]byte("test-key"))
	require.NoError(t, err)
	return tok
}
