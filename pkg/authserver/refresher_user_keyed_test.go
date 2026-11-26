// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"fmt"
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
	storagemocks "github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	upstreammocks "github.com/stacklok/toolhive/pkg/authserver/upstream/mocks"
)

// canonicalUserMatcher matches a context by the canonical user it carries.
// wantUser "" matches a context with no canonical user at all.
type canonicalUserMatcher struct{ wantUser string }

func (m canonicalUserMatcher) Matches(x any) bool {
	ctx, ok := x.(context.Context)
	if !ok {
		return false
	}
	got, present := serveridentity.CanonicalUserFromContext(ctx)
	if m.wantUser == "" {
		return !present
	}
	return present && got == m.wantUser
}

func (m canonicalUserMatcher) String() string {
	if m.wantUser == "" {
		return "context without a canonical user"
	}
	return "context with canonical user " + m.wantUser
}

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
		// wantUser is the canonical user storage sees on success.
		wantUser string
		// wantErr is set when the refresher must refuse before any storage call.
		wantErr error
	}{
		{
			name: "field on sets the user from expired.UserID", userKeyed: true,
			ctx: context.Background(), rowUser: "user-1", wantUser: "user-1",
		},
		{
			name: "field on keeps a context user that matches the row", userKeyed: true,
			ctx: serveridentity.WithCanonicalUser(context.Background(), "user-1"), rowUser: "user-1", wantUser: "user-1",
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
			// Without expectations, any call fails the test.
			store := storagemocks.NewMockUpstreamTokenStorage(ctrl)
			provider := upstreammocks.NewMockOAuth2Provider(ctrl)
			expired := userKeyedExpiredRow(tt.rowUser, "stale")

			if tt.wantErr == nil {
				m := canonicalUserMatcher{wantUser: tt.wantUser}
				store.EXPECT().ResolveUpstreamTokenRowID(m, "session", "github").
					Return(storage.UpstreamTokenRowID("row"), nil)
				store.EXPECT().GetUpstreamTokens(m, "session", "github").Return(expired, storage.ErrExpired)
				provider.EXPECT().RefreshTokens(gomock.Any(), "stale", "upstream-user-1").
					Return(&upstream.Tokens{AccessToken: "fresh", ExpiresAt: time.Now().Add(time.Hour)}, nil)
				store.EXPECT().CompareAndSwapUpstreamTokens(m, "session", "github", "stale", gomock.Any()).Return(nil)
			}

			refresher := newUpstreamTokenRefresher(
				[]handlers.NamedUpstream{{Name: "github", Provider: provider}}, store, time.Hour, tt.userKeyed)
			result, err := refresher.RefreshAndStore(tt.ctx, "session", expired)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "fresh", result.AccessToken)
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
		[]handlers.NamedUpstream{{Name: "github", Provider: provider}}, stor, time.Hour, true)

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

// TestUpstreamTokenRefresher_ReReadRowOwner pins the owner check on the row the
// refresher re-reads before redeeming it. Storage that is not user-keyed can
// hand back a row of another user on the re-read even though the caller's row
// passed the owner check, so the check runs again on the re-read row whenever
// the context carries a canonical user.
func TestUpstreamTokenRefresher_ReReadRowOwner(t *testing.T) {
	t.Parallel()

	foreignExpired := userKeyedExpiredRow("user-b", "rt-b")
	foreignLive := userKeyedExpiredRow("user-b", "rt-b")
	foreignLive.AccessToken = "live-b"
	foreignLive.ExpiresAt = time.Now().Add(time.Hour)

	tests := []struct {
		name    string
		ctxUser string // "" leaves the context without a canonical user
		reRead  *storage.UpstreamTokens
		readErr error
		wantErr error
	}{
		{
			name: "expired foreign row is refused before redeem", ctxUser: "user-a",
			reRead: foreignExpired, readErr: storage.ErrExpired, wantErr: storage.ErrInvalidBinding,
		},
		{
			name: "live foreign row is refused, not returned", ctxUser: "user-a",
			reRead: foreignLive, wantErr: storage.ErrInvalidBinding,
		},
		{
			name:   "no canonical user keeps today's behaviour",
			reRead: foreignExpired, readErr: storage.ErrExpired,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			store := storagemocks.NewMockUpstreamTokenStorage(ctrl)
			provider := upstreammocks.NewMockOAuth2Provider(ctrl)

			store.EXPECT().ResolveUpstreamTokenRowID(gomock.Any(), "session", "github").
				Return(storage.UpstreamTokenRowID("row"), nil)
			store.EXPECT().GetUpstreamTokens(gomock.Any(), "session", "github").Return(tt.reRead, tt.readErr)
			if tt.wantErr == nil {
				provider.EXPECT().RefreshTokens(gomock.Any(), "rt-b", "upstream-user-b").
					Return(&upstream.Tokens{AccessToken: "fresh", ExpiresAt: time.Now().Add(time.Hour)}, nil)
				store.EXPECT().CompareAndSwapUpstreamTokens(gomock.Any(), "session", "github", "rt-b", gomock.Any()).
					Return(nil)
			}
			// With wantErr set, the provider and every write are left without
			// expectations, so any call fails the test.

			ctx := context.Background()
			if tt.ctxUser != "" {
				ctx = serveridentity.WithCanonicalUser(ctx, tt.ctxUser)
			}
			// Field off: the storage is not user-keyed, so only the re-read
			// check stands between the caller and the foreign row.
			refresher := newUpstreamTokenRefresher(
				[]handlers.NamedUpstream{{Name: "github", Provider: provider}}, store, time.Hour, false)
			result, err := refresher.RefreshAndStore(ctx, "session", userKeyedExpiredRow("user-a", "rt-a"))
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, result)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "fresh", result.AccessToken)
		})
	}
}

// TestUpstreamTokenRefresher_ConflictWinnerOwner covers the re-reads after a
// lost compare-and-swap and after an invalid_grant: a row of another user is
// neither returned as the winner nor taken for a successor.
func TestUpstreamTokenRefresher_ConflictWinnerOwner(t *testing.T) {
	t.Parallel()

	foreignLive := userKeyedExpiredRow("user-b", "rt-b")
	foreignLive.AccessToken = "live-b"
	foreignLive.ExpiresAt = time.Now().Add(time.Hour)

	tests := []struct {
		name       string
		providerFn func(p *upstreammocks.MockOAuth2Provider)
		setupStore func(s *storagemocks.MockUpstreamTokenStorage)
		wantErr    error
	}{
		{
			name: "foreign winner of a lost compare-and-swap is refused",
			providerFn: func(p *upstreammocks.MockOAuth2Provider) {
				p.EXPECT().RefreshTokens(gomock.Any(), "rt-a", gomock.Any()).
					Return(&upstream.Tokens{AccessToken: "fresh", RefreshToken: "rotated", ExpiresAt: time.Now().Add(time.Hour)}, nil)
			},
			setupStore: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().CompareAndSwapUpstreamTokens(gomock.Any(), "session", "github", "rt-a", gomock.Any()).
					Return(storage.ErrConcurrentRefresh)
			},
			wantErr: storage.ErrInvalidBinding,
		},
		{
			name: "foreign row is not a successor of a rejected refresh token",
			providerFn: func(p *upstreammocks.MockOAuth2Provider) {
				p.EXPECT().RefreshTokens(gomock.Any(), "rt-a", gomock.Any()).
					Return(nil, fmt.Errorf("rejected: %w", upstream.ErrGrantRejected))
			},
			setupStore: func(*storagemocks.MockUpstreamTokenStorage) {},
			wantErr:    upstream.ErrGrantRejected,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			store := storagemocks.NewMockUpstreamTokenStorage(ctrl)
			provider := upstreammocks.NewMockOAuth2Provider(ctrl)

			store.EXPECT().ResolveUpstreamTokenRowID(gomock.Any(), "session", "github").
				Return(storage.UpstreamTokenRowID("row"), nil)
			gomock.InOrder(
				store.EXPECT().GetUpstreamTokens(gomock.Any(), "session", "github").
					Return(userKeyedExpiredRow("user-a", "rt-a"), storage.ErrExpired),
				store.EXPECT().GetUpstreamTokens(gomock.Any(), "session", "github").Return(foreignLive, nil),
			)
			tt.providerFn(provider)
			tt.setupStore(store)

			refresher := newUpstreamTokenRefresher(
				[]handlers.NamedUpstream{{Name: "github", Provider: provider}}, store, time.Hour, false)
			ctx := serveridentity.WithCanonicalUser(context.Background(), "user-a")
			result, err := refresher.RefreshAndStore(ctx, "session", userKeyedExpiredRow("user-a", "rt-a"))
			require.ErrorIs(t, err, tt.wantErr)
			assert.Nil(t, result)
		})
	}
}
