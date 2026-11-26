// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	serveridentity "github.com/stacklok/toolhive/pkg/authserver/identity"
	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	upstreammocks "github.com/stacklok/toolhive/pkg/authserver/upstream/mocks"
)

func userKeyedExpiredRow(userID, refreshToken string) *storage.UpstreamTokens {
	return &storage.UpstreamTokens{
		ProviderID:       "github",
		AccessToken:      "old-access",
		RefreshToken:     refreshToken,
		ExpiresAt:        time.Now().Add(-time.Hour),
		SessionExpiresAt: time.Now().Add(24 * time.Hour),
		UserID:           userID,
		UpstreamSubject:  "upstream-" + userID,
	}
}

// seedUserKeyedRow stores a row through the wrapper, as the callback does.
func seedUserKeyedRow(t *testing.T, w *storage.UserKeyedUpstreamStorage, row *storage.UpstreamTokens) {
	t.Helper()
	ctx := serveridentity.WithCanonicalUser(context.Background(), row.UserID)
	require.NoError(t, w.StoreUpstreamTokens(ctx, "ignored", row.ProviderID, row))
}

func TestUpstreamTokenRefresher_UserKeyed_ContextUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		userKeyed bool
		ctx       context.Context
		rowUser   string // the owner of the row the caller presents
		// wantErr is set when the refresher must refuse before any storage call.
		wantErr error
	}{
		{
			name: "field on sets the user from expired.UserID", userKeyed: true,
			ctx: context.Background(), rowUser: "user-1",
		},
		{
			name: "field on keeps a context user that matches the row", userKeyed: true,
			ctx: serveridentity.WithCanonicalUser(context.Background(), "user-1"), rowUser: "user-1",
		},
		{
			name: "field off adds no canonical user", userKeyed: false,
			ctx: context.Background(), rowUser: "user-1",
		},
		{
			name: "context user other than the row owner is refused", userKeyed: true,
			ctx:     serveridentity.WithCanonicalUser(context.Background(), "user-b"),
			rowUser: "user-a", wantErr: storage.ErrInvalidBinding,
		},
		{
			// WithCanonicalUser leaves ctx unchanged for "", so the empty value is stored directly.
			name: "explicitly empty context user is refused", userKeyed: true,
			ctx:     context.WithValue(context.Background(), serveridentity.CanonicalUserContextKey{}, ""),
			rowUser: "user-a", wantErr: storage.ErrNoCanonicalUser,
		},
		{
			name: "row without an owner and no context user is refused", userKeyed: true,
			ctx: context.Background(), rowUser: "", wantErr: storage.ErrNoCanonicalUser,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			base := storage.NewMemoryStorage()
			t.Cleanup(func() { _ = base.Close() })
			var stor storage.Storage = base
			var wrapper *storage.UserKeyedUpstreamStorage
			if tt.userKeyed {
				var err error
				wrapper, err = storage.NewUserKeyedUpstreamStorage(base)
				require.NoError(t, err)
				stor = wrapper
			}
			provider := upstreammocks.NewMockOAuth2Provider(ctrl)
			expired := userKeyedExpiredRow(tt.rowUser, "stale")

			if tt.wantErr == nil {
				if tt.userKeyed {
					seedUserKeyedRow(t, wrapper, expired)
				} else {
					require.NoError(t, base.StoreUpstreamTokens(tt.ctx, "session", "github", expired))
				}
				provider.EXPECT().RefreshTokens(gomock.Any(), "stale", "upstream-user-1").
					Return(&upstream.Tokens{AccessToken: "fresh", ExpiresAt: time.Now().Add(time.Hour)}, nil)
			}

			refresher := newUpstreamTokenRefresher(
				[]handlers.NamedUpstream{{Name: "github", Provider: provider}}, stor, time.Hour)
			result, err := refresher.RefreshAndStore(tt.ctx, "session", expired)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "fresh", result.AccessToken)

			if tt.userKeyed {
				stored, getErr := wrapper.GetUpstreamTokens(
					serveridentity.WithCanonicalUser(context.Background(), tt.rowUser), "ignored", "github")
				require.NoError(t, getErr)
				assert.Equal(t, "fresh", stored.AccessToken, "the row under the user's key is refreshed")
				return
			}
			stored, getErr := base.GetUpstreamTokens(context.Background(), "session", "github")
			require.NoError(t, getErr)
			assert.Equal(t, "fresh", stored.AccessToken)
		})
	}
}

// resolveSignalStorage reports each ResolveUpstreamTokenRowID call so the test
// knows when a second caller has reached the singleflight key.
//
// It decorates real storage, rather than being a mock, so the resolved row IDs
// come from the user-keyed wrapper under test.
type resolveSignalStorage struct {
	storage.UpstreamTokenStorage
	resolves atomic.Int64
	second   chan struct{}
	mu       sync.Mutex
	rowIDs   []storage.UpstreamTokenRowID
}

// Unwrap lets storage.IsUserKeyed see the wrapper beneath this decorator.
func (s *resolveSignalStorage) Unwrap() storage.UpstreamTokenStorage { return s.UpstreamTokenStorage }

func (s *resolveSignalStorage) ResolveUpstreamTokenRowID(
	ctx context.Context, sessionID, providerName string,
) (storage.UpstreamTokenRowID, error) {
	id, err := s.UpstreamTokenStorage.ResolveUpstreamTokenRowID(ctx, sessionID, providerName)
	s.mu.Lock()
	s.rowIDs = append(s.rowIDs, id)
	s.mu.Unlock()
	if s.resolves.Add(1) == 2 {
		close(s.second)
	}
	return id, err
}

func TestUpstreamTokenRefresher_UserKeyed_TwoSessionsOneUpstreamRefresh(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	provider := upstreammocks.NewMockOAuth2Provider(ctrl)

	base := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })
	wrapper, err := storage.NewUserKeyedUpstreamStorage(base)
	require.NoError(t, err)
	expired := userKeyedExpiredRow("user-1", "stale")
	seedUserKeyedRow(t, wrapper, expired)
	stor := &resolveSignalStorage{UpstreamTokenStorage: wrapper, second: make(chan struct{})}

	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	provider.EXPECT().RefreshTokens(gomock.Any(), "stale", "upstream-user-1").AnyTimes().
		DoAndReturn(func(ctx context.Context, _, _ string) (*upstream.Tokens, error) {
			if calls.Add(1) == 1 {
				close(entered)
			}
			select {
			case <-release:
				return &upstream.Tokens{AccessToken: "fresh", RefreshToken: "rotated", ExpiresAt: time.Now().Add(time.Hour)}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})

	refresher := newUpstreamTokenRefresher(
		[]handlers.NamedUpstream{{Name: "github", Provider: provider}}, stor, time.Hour)

	var wg sync.WaitGroup
	results := make([]*storage.UpstreamTokens, 2)
	errs := make([]error, 2)
	run := func(i int, sessionID string) {
		defer wg.Done()
		results[i], errs[i] = refresher.RefreshAndStore(context.Background(), sessionID, expired)
	}
	wg.Add(2)
	go run(0, "session-one")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for the first refresh to reach the provider")
	}
	go run(1, "session-two")
	select {
	case <-stor.second:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for the second caller to resolve the row")
	}
	close(release)
	waitGroupTimeout(t, &wg, "timeout waiting for the two refresh callers")

	for i := range errs {
		require.NoError(t, errs[i], "caller %d", i)
		assert.Equal(t, "fresh", results[i].AccessToken)
	}
	stor.mu.Lock()
	rowIDs := append([]storage.UpstreamTokenRowID(nil), stor.rowIDs...)
	stor.mu.Unlock()
	require.Len(t, rowIDs, 2)
	assert.NotEmpty(t, rowIDs[0])
	assert.Equal(t, rowIDs[0], rowIDs[1], "both sessions must resolve to one row ID")
	assert.Equal(t, int64(1), calls.Load(), "two sessions of one user must redeem the upstream refresh token once")
}

// TestUpstreamTokenRefresher_ForeignRowThroughWrapper pins that a row owned by
// another user, found under the context user's key, is refused by the wrapper
// on the refresher's re-read before any provider call.
func TestUpstreamTokenRefresher_ForeignRowThroughWrapper(t *testing.T) {
	t.Parallel()

	base := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })
	wrapper, err := storage.NewUserKeyedUpstreamStorage(base)
	require.NoError(t, err)
	foreign := userKeyedExpiredRow("user-b", "rt-b")
	require.NoError(t, base.StoreUpstreamTokens(context.Background(), "user:user-a", "github", foreign))

	// No expectations: any provider call fails the test.
	provider := upstreammocks.NewMockOAuth2Provider(gomock.NewController(t))
	refresher := newUpstreamTokenRefresher(
		[]handlers.NamedUpstream{{Name: "github", Provider: provider}}, wrapper, time.Hour)

	result, err := refresher.RefreshAndStore(context.Background(), "session", userKeyedExpiredRow("user-a", "rt-a"))
	require.ErrorIs(t, err, storage.ErrInvalidBinding)
	assert.Nil(t, result)
}
