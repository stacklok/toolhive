// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

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
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	upstreammocks "github.com/stacklok/toolhive/pkg/authserver/upstream/mocks"
)

func TestUpstreamTokenRefresher_RefreshAndStore(t *testing.T) {
	t.Parallel()

	newExpiry := time.Now().Add(1 * time.Hour)

	baseExpired := &storage.UpstreamTokens{
		ProviderID:      "github",
		AccessToken:     "old-access",
		RefreshToken:    "old-refresh",
		IDToken:         "old-id-token",
		ExpiresAt:       time.Now().Add(-1 * time.Hour),
		UserID:          "user-123",
		UpstreamSubject: "upstream-sub-456",
		ClientID:        "client-abc",
	}

	tests := []struct {
		name           string
		sessionID      string
		expired        *storage.UpstreamTokens
		setupProvider  func(*testing.T, *upstreammocks.MockOAuth2Provider)
		setupStorage   func(*testing.T, *storagemocks.MockUpstreamTokenStorage)
		wantErr        bool
		wantErrContain string
		checkResult    func(*testing.T, *storage.UpstreamTokens)
	}{
		{
			name:      "successful refresh with token rotation",
			sessionID: "session-1",
			expired:   baseExpired,
			setupProvider: func(_ *testing.T, p *upstreammocks.MockOAuth2Provider) {
				p.EXPECT().RefreshTokens(gomock.Any(), "old-refresh", "upstream-sub-456").
					Return(&upstream.Tokens{
						AccessToken:  "new-access",
						RefreshToken: "new-refresh",
						IDToken:      "new-id-token",
						ExpiresAt:    newExpiry,
					}, nil)
			},
			setupStorage: func(_ *testing.T, s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().StoreUpstreamTokens(gomock.Any(), "session-1", gomock.Any()).
					DoAndReturn(func(_ context.Context, _ string, tokens *storage.UpstreamTokens) error {
						// Verify binding fields are preserved from expired tokens
						assert.Equal(t, "github", tokens.ProviderID)
						assert.Equal(t, "user-123", tokens.UserID)
						assert.Equal(t, "upstream-sub-456", tokens.UpstreamSubject)
						assert.Equal(t, "client-abc", tokens.ClientID)
						// Verify new token values
						assert.Equal(t, "new-access", tokens.AccessToken)
						assert.Equal(t, "new-refresh", tokens.RefreshToken)
						assert.Equal(t, "new-id-token", tokens.IDToken)
						assert.Equal(t, newExpiry, tokens.ExpiresAt)
						return nil
					})
			},
			checkResult: func(t *testing.T, result *storage.UpstreamTokens) {
				t.Helper()
				assert.Equal(t, "new-access", result.AccessToken)
				assert.Equal(t, "new-refresh", result.RefreshToken)
				assert.Equal(t, "new-id-token", result.IDToken)
				assert.Equal(t, newExpiry, result.ExpiresAt)
				// Binding fields preserved
				assert.Equal(t, "github", result.ProviderID)
				assert.Equal(t, "user-123", result.UserID)
				assert.Equal(t, "upstream-sub-456", result.UpstreamSubject)
				assert.Equal(t, "client-abc", result.ClientID)
			},
		},
		{
			name:      "provider does not rotate refresh token - keeps old one",
			sessionID: "session-2",
			expired:   baseExpired,
			setupProvider: func(_ *testing.T, p *upstreammocks.MockOAuth2Provider) {
				p.EXPECT().RefreshTokens(gomock.Any(), "old-refresh", "upstream-sub-456").
					Return(&upstream.Tokens{
						AccessToken:  "new-access",
						RefreshToken: "", // Provider did not rotate
						IDToken:      "new-id-token",
						ExpiresAt:    newExpiry,
					}, nil)
			},
			setupStorage: func(_ *testing.T, s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().StoreUpstreamTokens(gomock.Any(), "session-2", gomock.Any()).
					DoAndReturn(func(_ context.Context, _ string, tokens *storage.UpstreamTokens) error {
						assert.Equal(t, "old-refresh", tokens.RefreshToken)
						return nil
					})
			},
			checkResult: func(t *testing.T, result *storage.UpstreamTokens) {
				t.Helper()
				assert.Equal(t, "new-access", result.AccessToken)
				assert.Equal(t, "old-refresh", result.RefreshToken)
			},
		},
		{
			name:           "nil expired tokens returns error",
			sessionID:      "session-3",
			expired:        nil,
			setupProvider:  func(_ *testing.T, _ *upstreammocks.MockOAuth2Provider) {},
			setupStorage:   func(_ *testing.T, _ *storagemocks.MockUpstreamTokenStorage) {},
			wantErr:        true,
			wantErrContain: "expired tokens are required",
		},
		{
			name:      "empty refresh token returns error",
			sessionID: "session-4",
			expired: &storage.UpstreamTokens{
				ProviderID:      "github",
				AccessToken:     "old-access",
				RefreshToken:    "",
				UserID:          "user-123",
				UpstreamSubject: "upstream-sub-456",
				ClientID:        "client-abc",
			},
			setupProvider:  func(_ *testing.T, _ *upstreammocks.MockOAuth2Provider) {},
			setupStorage:   func(_ *testing.T, _ *storagemocks.MockUpstreamTokenStorage) {},
			wantErr:        true,
			wantErrContain: "no refresh token available",
		},
		{
			name:      "provider refresh fails returns error",
			sessionID: "session-5",
			expired:   baseExpired,
			setupProvider: func(_ *testing.T, p *upstreammocks.MockOAuth2Provider) {
				p.EXPECT().RefreshTokens(gomock.Any(), "old-refresh", "upstream-sub-456").
					Return(nil, errors.New("upstream IDP unavailable"))
			},
			setupStorage:   func(_ *testing.T, _ *storagemocks.MockUpstreamTokenStorage) {},
			wantErr:        true,
			wantErrContain: "upstream token refresh failed",
		},
		{
			name:      "storage fails after refresh - returns refreshed tokens anyway",
			sessionID: "session-6",
			expired:   baseExpired,
			setupProvider: func(_ *testing.T, p *upstreammocks.MockOAuth2Provider) {
				p.EXPECT().RefreshTokens(gomock.Any(), "old-refresh", "upstream-sub-456").
					Return(&upstream.Tokens{
						AccessToken:  "new-access",
						RefreshToken: "new-refresh",
						IDToken:      "new-id-token",
						ExpiresAt:    newExpiry,
					}, nil)
			},
			setupStorage: func(_ *testing.T, s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().StoreUpstreamTokens(gomock.Any(), "session-6", gomock.Any()).
					Return(errors.New("redis connection lost"))
			},
			checkResult: func(t *testing.T, result *storage.UpstreamTokens) {
				t.Helper()
				// Tokens should still be returned despite storage failure
				assert.Equal(t, "new-access", result.AccessToken)
				assert.Equal(t, "new-refresh", result.RefreshToken)
				assert.Equal(t, "new-id-token", result.IDToken)
				assert.Equal(t, newExpiry, result.ExpiresAt)
				// Binding fields preserved
				assert.Equal(t, "github", result.ProviderID)
				assert.Equal(t, "user-123", result.UserID)
				assert.Equal(t, "upstream-sub-456", result.UpstreamSubject)
				assert.Equal(t, "client-abc", result.ClientID)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)

			mockProvider := upstreammocks.NewMockOAuth2Provider(ctrl)
			mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)

			tt.setupProvider(t, mockProvider)
			tt.setupStorage(t, mockStorage)

			refresher := &upstreamTokenRefresher{
				provider: mockProvider,
				storage:  mockStorage,
			}

			result, err := refresher.RefreshAndStore(context.Background(), tt.sessionID, tt.expired)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrContain)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			if tt.checkResult != nil {
				tt.checkResult(t, result)
			}
		})
	}
}
