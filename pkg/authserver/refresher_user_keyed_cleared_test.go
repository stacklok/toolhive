// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	serveridentity "github.com/stacklok/toolhive/pkg/authserver/identity"
	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	upstreammocks "github.com/stacklok/toolhive/pkg/authserver/upstream/mocks"
)

// TestUserKeyedClearedRowNeedsAuthorization runs the real refresher, the real
// user-keyed wrapper and the real service together: when a rotated refresh
// token cannot be saved, the shared row is kept with its refresh token cleared
// and its access token expired, so the connection needs authorization at once
// on both the expired-access-token path and the expired-ID-token path.
func TestUserKeyedClearedRowNeedsAuthorization(t *testing.T) {
	t.Parallel()

	const userID = "user-1"
	idToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"exp": time.Now().Add(-time.Hour).Unix(),
	}).SignedString([]byte("test-key"))
	require.NoError(t, err)

	tests := []struct {
		name string
		row  func() *storage.UpstreamTokens
	}{
		{
			name: "expired access token",
			row:  func() *storage.UpstreamTokens { return userKeyedExpiredRow(userID, "stale") },
		},
		{
			name: "no access token expiry and an expired ID token",
			row: func() *storage.UpstreamTokens {
				row := userKeyedExpiredRow(userID, "stale")
				row.AccessToken = "live"
				row.ExpiresAt = time.Time{}
				row.IDToken = idToken
				return row
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := storage.NewMemoryStorage()
			t.Cleanup(func() { _ = base.Close() })
			// The failing decorator sits below the wrapper, so the wrapper is
			// still the outermost layer the refresher and service detect.
			wrapper, err := storage.NewUserKeyedUpstreamStorage(&foreignFromReadStorage{Storage: base, failStores: true})
			require.NoError(t, err)
			seedUserKeyedRow(t, wrapper, tt.row())

			provider := upstreammocks.NewMockOAuth2Provider(gomock.NewController(t))
			provider.EXPECT().RefreshTokens(gomock.Any(), "stale", "upstream-user-1").Return(&upstream.Tokens{
				AccessToken: "fresh", RefreshToken: "rotated", ExpiresAt: time.Now().Add(time.Hour),
			}, nil)
			refresher := newUpstreamTokenRefresher(
				[]handlers.NamedUpstream{{Name: "github", Provider: provider}}, wrapper, time.Hour)
			svc := upstreamtoken.NewInProcessService(wrapper, refresher)
			ctx := context.Background()

			cred, err := svc.GetValidTokensForUser(ctx, userID, "github")
			require.ErrorIs(t, err, upstreamtoken.ErrNeedsAuthorization)
			assert.Nil(t, cred, "the stored credential must not be served")

			userCtx := serveridentity.WithCanonicalUser(ctx, userID)
			kept, err := wrapper.GetUpstreamTokens(userCtx, "ignored", "github")
			require.True(t, err == nil || assert.ErrorIs(t, err, storage.ErrExpired), "err = %v", err)
			require.NotNil(t, kept, "the row is kept")
			assert.Empty(t, kept.RefreshToken)
			assert.False(t, kept.ExpiresAt.After(time.Now()), "the access token must be expired")

			_, err = svc.GetValidTokensForUser(ctx, userID, "github")
			require.ErrorIs(t, err, upstreamtoken.ErrNeedsAuthorization, "a later read must not serve the row either")

			signedIn := userKeyedExpiredRow(userID, "new-rt")
			signedIn.AccessToken = "signed-in"
			signedIn.ExpiresAt = time.Now().Add(time.Hour)
			require.NoError(t, wrapper.StoreUpstreamTokens(userCtx, "ignored", "github", signedIn))
			cred, err = svc.GetValidTokensForUser(ctx, userID, "github")
			require.NoError(t, err)
			assert.Equal(t, "signed-in", cred.AccessToken)
		})
	}
}
