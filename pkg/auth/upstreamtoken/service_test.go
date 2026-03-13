// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstreamtoken

import (
	"context"
	"errors"
	"testing"
	"time"

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
		ExpiresAt:       time.Now().Add(1 * time.Hour),
		UserID:          "user-1",
		UpstreamSubject: "sub-1",
		ClientID:        "client-1",
	}

	expiredTokens := &storage.UpstreamTokens{
		ProviderID:      "github",
		AccessToken:     "expired-access-token",
		RefreshToken:    "refresh-token",
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
		wantErr        error
		wantAnyErr     bool // expect an error but not a specific sentinel
	}{
		{
			name:      "valid tokens returned directly",
			sessionID: "session-1",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-1").
					Return(validTokens, nil)
			},
			setupRefresher: func(_ *storagemocks.MockUpstreamTokenRefresher) {},
			wantToken:      "valid-access-token",
		},
		{
			name:      "expired tokens refreshed via storage ErrExpired",
			sessionID: "session-2",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-2").
					Return(expiredTokens, storage.ErrExpired)
			},
			setupRefresher: func(r *storagemocks.MockUpstreamTokenRefresher) {
				r.EXPECT().RefreshAndStore(gomock.Any(), "session-2", expiredTokens).
					Return(refreshedTokens, nil)
			},
			wantToken: "new-access-token",
		},
		{
			name:      "expired tokens refreshed via IsExpired check",
			sessionID: "session-3",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				// Storage returns expired tokens without error (defense in depth path)
				s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-3").
					Return(expiredTokens, nil)
			},
			setupRefresher: func(r *storagemocks.MockUpstreamTokenRefresher) {
				r.EXPECT().RefreshAndStore(gomock.Any(), "session-3", expiredTokens).
					Return(refreshedTokens, nil)
			},
			wantToken: "new-access-token",
		},
		{
			name:      "session not found",
			sessionID: "session-4",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-4").
					Return(nil, storage.ErrNotFound)
			},
			setupRefresher: func(_ *storagemocks.MockUpstreamTokenRefresher) {},
			wantErr:        ErrSessionNotFound,
		},
		{
			name:      "expired with no refresh token",
			sessionID: "session-5",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-5").
					Return(expiredNoRefresh, storage.ErrExpired)
			},
			setupRefresher: func(_ *storagemocks.MockUpstreamTokenRefresher) {},
			wantErr:        ErrNoRefreshToken,
		},
		{
			name:      "refresh fails",
			sessionID: "session-6",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-6").
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
				s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-7").
					Return(nil, errors.New("redis connection lost"))
			},
			setupRefresher: func(_ *storagemocks.MockUpstreamTokenRefresher) {},
			wantAnyErr:     true,
		},
		{
			name:      "ErrExpired with nil tokens returns ErrNoRefreshToken",
			sessionID: "session-8",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-8").
					Return(nil, storage.ErrExpired)
			},
			setupRefresher: func(_ *storagemocks.MockUpstreamTokenRefresher) {},
			wantErr:        ErrNoRefreshToken,
		},
		{
			name:      "invalid binding returns ErrInvalidBinding",
			sessionID: "session-9",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-9").
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

			cred, err := svc.GetValidTokens(context.Background(), tt.sessionID)

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
		GetUpstreamTokens(gomock.Any(), "session-1").
		Return(expiredTokens, storage.ErrExpired)

	svc := NewInProcessService(mockStorage, nil)

	cred, err := svc.GetValidTokens(context.Background(), "session-1")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoRefreshToken)
	assert.Nil(t, cred)
}
