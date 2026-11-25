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

	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	storagemocks "github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	upstreammocks "github.com/stacklok/toolhive/pkg/authserver/upstream/mocks"
)

// unexpiredRow is the row a lease holder is taken to have stored: unexpired,
// and carrying a refresh token the waiting caller has not seen.
func unexpiredRow() *storage.UpstreamTokens {
	return &storage.UpstreamTokens{
		ProviderID:   "github",
		AccessToken:  "access-rotated-refresh",
		RefreshToken: "rotated-refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
}

func expiredRow(refreshToken string) *storage.UpstreamTokens {
	return &storage.UpstreamTokens{
		ProviderID:      "github",
		AccessToken:     "access-" + refreshToken,
		RefreshToken:    refreshToken,
		UpstreamSubject: "subject",
		ExpiresAt:       time.Now().Add(-time.Hour),
	}
}

// expectAcquireHeld expects one acquisition that finds the lease held by
// someone else, which is what sends the caller into the waiting path.
func expectAcquireHeld(locker *storagemocks.MockUpstreamRefreshLocker) *gomock.Call {
	return locker.EXPECT().
		AcquireUpstreamRefreshLock(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, storage.ErrUpstreamRefreshLockHeld)
}

// expectAcquireGranted expects one acquisition that takes the lease, and one
// matching Release returning releaseErr. Expecting the Release here is the
// assertion that the leader gives the lease back: gomock fails the test if it
// is not called, and the leases this helper hands out are the only ones in
// these tests, so a lease left behind cannot pass unnoticed.
func expectAcquireGranted(
	ctrl *gomock.Controller, locker *storagemocks.MockUpstreamRefreshLocker, releaseErr error,
) *gomock.Call {
	lease := storagemocks.NewMockUpstreamRefreshLease(ctrl)
	lease.EXPECT().Release(gomock.Any()).Return(releaseErr)
	return locker.EXPECT().
		AcquireUpstreamRefreshLock(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(lease, nil)
}

// TestCoordinatedRefresh_WaiterNeverRedeems is the core of issue #4122: a
// replica that does not hold the row's lease must not call the upstream
// provider, whatever happens while it waits. The provider mock these cases run
// against has no RefreshTokens expectation, so any upstream redemption fails
// the test, and no lease is ever handed out, so there is nothing to release.
func TestCoordinatedRefresh_WaiterNeverRedeems(t *testing.T) {
	t.Parallel()

	caller := expiredRow("old-refresh")

	t.Run("returns the holder's row once it appears", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		locker := storagemocks.NewMockUpstreamRefreshLocker(ctrl)
		winner := unexpiredRow()

		// One acquisition per re-read: the first re-read still shows the
		// caller's own row, because the holder has not finished, and the
		// second shows the rotated row it stored.
		expectAcquireHeld(locker).Times(2)
		gomock.InOrder(
			store.EXPECT().GetUpstreamTokens(gomock.Any(), "session", "github").Return(caller, nil),
			store.EXPECT().GetUpstreamTokens(gomock.Any(), "session", "github").Return(winner, nil),
		)

		r := newLockTestRefresher(t, ctrl, store, locker)

		got, err := r.coordinatedRefresh(t.Context(), "session", "row", caller)
		require.NoError(t, err)
		assert.Equal(t, winner, got, "the waiter must hand back what the holder stored")
	})

	t.Run("fails when the caller's budget runs out", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		locker := storagemocks.NewMockUpstreamRefreshLocker(ctrl)

		// The holder never finishes, so the re-read keeps showing the caller's
		// own row.
		store.EXPECT().GetUpstreamTokens(gomock.Any(), "session", "github").Return(caller, nil)

		// The budget is spent from inside the second attempt rather than by a
		// wall-clock deadline, so the test pins the retry without racing the
		// scheduler: exactly two attempts, the second of which runs out.
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		gomock.InOrder(
			expectAcquireHeld(locker),
			locker.EXPECT().AcquireUpstreamRefreshLock(gomock.Any(), gomock.Any(), gomock.Any()).
				DoAndReturn(func(
					context.Context, storage.UpstreamTokenRowID, time.Duration,
				) (storage.UpstreamRefreshLease, error) {
					cancel()
					return nil, storage.ErrUpstreamRefreshLockHeld
				}),
		)

		r := newLockTestRefresher(t, ctrl, store, locker)

		got, err := r.coordinatedRefresh(ctx, "session", "row", caller)
		require.Error(t, err)
		assert.Nil(t, got)
		assert.ErrorIs(t, err, context.Canceled,
			"running out of budget is a failure, not a licence to redeem uncoordinated")
	})

	t.Run("fails closed when the lock backend errors", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		locker := storagemocks.NewMockUpstreamRefreshLocker(ctrl)
		backendDown := errors.New("redis: connection refused")

		// Exactly one attempt, and no re-read: a backend fault is not retried,
		// because the caller cannot know whether another replica is redeeming.
		locker.EXPECT().AcquireUpstreamRefreshLock(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, backendDown)

		r := newLockTestRefresher(t, ctrl, store, locker)

		got, err := r.coordinatedRefresh(t.Context(), "session", "row", caller)
		require.Error(t, err)
		assert.Nil(t, got)
		assert.ErrorIs(t, err, backendDown)
	})

	t.Run("ignores a row that changed but is still expired", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		locker := storagemocks.NewMockUpstreamRefreshLocker(ctrl)

		// A different row, but expired: the holder has not produced anything
		// usable, so the waiter must keep contending rather than return it.
		store.EXPECT().GetUpstreamTokens(gomock.Any(), "session", "github").
			Return(expiredRow("some-other-refresh"), nil)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		gomock.InOrder(
			expectAcquireHeld(locker),
			locker.EXPECT().AcquireUpstreamRefreshLock(gomock.Any(), gomock.Any(), gomock.Any()).
				DoAndReturn(func(
					context.Context, storage.UpstreamTokenRowID, time.Duration,
				) (storage.UpstreamRefreshLease, error) {
					cancel()
					return nil, storage.ErrUpstreamRefreshLockHeld
				}),
		)

		r := newLockTestRefresher(t, ctrl, store, locker)

		_, err := r.coordinatedRefresh(ctx, "session", "row", caller)
		assert.ErrorIs(t, err, context.Canceled)
	})
}

// TestCoordinatedRefresh_WinsLeaseOnRetry covers the waiter whose next
// acquisition succeeds: the row it re-read was unchanged, so there was nothing
// to hand back, and the attempt that wins the lease re-reads the row and
// redeems it rather than treating the free lease as permission to skip ahead.
func TestCoordinatedRefresh_WinsLeaseOnRetry(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	locker := storagemocks.NewMockUpstreamRefreshLocker(ctrl)
	caller := expiredRow("old-refresh")

	// Re-read after the first failed attempt: unchanged, so no winner. Then
	// the leader's own authoritative re-read.
	store.EXPECT().GetUpstreamTokens(gomock.Any(), "session", "github").
		Times(2).Return(caller, storage.ErrExpired)
	store.EXPECT().CompareAndSwapUpstreamTokens(
		gomock.Any(), "session", "github", "old-refresh", gomock.Any(),
	).Return(nil)

	provider := upstreammocks.NewMockOAuth2Provider(ctrl)
	provider.EXPECT().RefreshTokens(gomock.Any(), "old-refresh", "subject").
		Return(&upstream.Tokens{
			AccessToken:  "new-access",
			RefreshToken: "new-refresh",
			ExpiresAt:    time.Now().Add(time.Hour),
		}, nil)

	gomock.InOrder(
		expectAcquireHeld(locker),
		expectAcquireGranted(ctrl, locker, nil),
	)

	r := &upstreamTokenRefresher{
		providers:            map[string]upstream.OAuth2Provider{"github": provider},
		storage:              store,
		locker:               locker,
		refreshTokenLifespan: 24 * time.Hour,
	}

	got, err := r.coordinatedRefresh(t.Context(), "session", "row", caller)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "new-refresh", got.RefreshToken)
}

// TestCoordinatedRefresh_LeaderReleases checks the lease is given back on
// every leader exit path, including the short-circuit where the row turned out
// to be fresh and no upstream call happened at all. A lease left behind would
// stall every other replica for its full TTL. The Release expectation lives in
// expectAcquireGranted, so each case fails if its lease is not released.
func TestCoordinatedRefresh_LeaderReleases(t *testing.T) {
	t.Parallel()

	t.Run("after a short-circuit on the authoritative re-read", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		locker := storagemocks.NewMockUpstreamRefreshLocker(ctrl)
		winner := unexpiredRow()
		store.EXPECT().GetUpstreamTokens(gomock.Any(), "session", "github").Return(winner, nil)
		expectAcquireGranted(ctrl, locker, nil)

		r := newLockTestRefresher(t, ctrl, store, locker)

		got, err := r.coordinatedRefresh(t.Context(), "session", "row", expiredRow("old-refresh"))
		require.NoError(t, err)
		assert.Equal(t, winner, got)
	})

	t.Run("after the refresh fails", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		locker := storagemocks.NewMockUpstreamRefreshLocker(ctrl)
		store.EXPECT().GetUpstreamTokens(gomock.Any(), "session", "github").
			Return(nil, errors.New("storage unavailable"))
		expectAcquireGranted(ctrl, locker, nil)

		r := newLockTestRefresher(t, ctrl, store, locker)

		_, err := r.coordinatedRefresh(t.Context(), "session", "row", expiredRow("old-refresh"))
		require.Error(t, err)
	})

	t.Run("a failed release does not fail the refresh", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		locker := storagemocks.NewMockUpstreamRefreshLocker(ctrl)
		winner := unexpiredRow()
		store.EXPECT().GetUpstreamTokens(gomock.Any(), "session", "github").Return(winner, nil)
		expectAcquireGranted(ctrl, locker, errors.New("redis: timeout"))

		r := newLockTestRefresher(t, ctrl, store, locker)

		got, err := r.coordinatedRefresh(t.Context(), "session", "row", expiredRow("old-refresh"))
		require.NoError(t, err, "the lease expires on its own; a failed release is only a delay")
		assert.Equal(t, winner, got)
	})
}

// TestCoordinatedRefresh_NoLocker checks a backend without the capability
// still refreshes, with the CAS write as its only cross-process coordination.
func TestCoordinatedRefresh_NoLocker(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	winner := unexpiredRow()
	store.EXPECT().GetUpstreamTokens(gomock.Any(), "session", "github").Return(winner, nil)

	r := newLockTestRefresher(t, ctrl, store, nil)

	got, err := r.coordinatedRefresh(t.Context(), "session", "row", expiredRow("old-refresh"))
	require.NoError(t, err)
	assert.Equal(t, winner, got)
}

// TestRefreshAndStore_TakesLeaseForResolvedRow checks the lease is taken for
// the same row identity the in-process singleflight group is keyed by, and for
// longer than the refresh deadline — if it could expire mid-redemption, a
// waiter would take over and redeem the same refresh token.
func TestRefreshAndStore_TakesLeaseForResolvedRow(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	locker := storagemocks.NewMockUpstreamRefreshLocker(ctrl)
	rowID := storage.UpstreamTokenRowID("resolved-row-id")
	store.EXPECT().ResolveUpstreamTokenRowID(gomock.Any(), "session", "github").Return(rowID, nil)
	store.EXPECT().GetUpstreamTokens(gomock.Any(), "session", "github").
		Return(unexpiredRow(), nil)

	var gotRowID storage.UpstreamTokenRowID
	var gotTTL time.Duration
	lease := storagemocks.NewMockUpstreamRefreshLease(ctrl)
	lease.EXPECT().Release(gomock.Any()).Return(nil)
	locker.EXPECT().AcquireUpstreamRefreshLock(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context, rowID storage.UpstreamTokenRowID, ttl time.Duration,
		) (storage.UpstreamRefreshLease, error) {
			gotRowID, gotTTL = rowID, ttl
			return lease, nil
		})

	r := newLockTestRefresher(t, ctrl, store, locker)

	_, err := r.RefreshAndStore(t.Context(), "session", expiredRow("old-refresh"))
	require.NoError(t, err)

	assert.Equal(t, rowID, gotRowID,
		"the lease must cover the same row singleflight deduplicates")
	assert.Greater(t, gotTTL, refreshTimeout,
		"the lease must outlast the holder's whole read-redeem-write sequence")
}

// TestNextUpstreamRefreshPoll pins the waiter's backoff. Each attempt costs an
// acquisition and a row read on a backend several replicas share, so a poll
// that stayed at its initial interval would put hundreds of round-trips per
// waiting request on it; one that grew without a cap would make a waiter miss
// a completed refresh for far longer than the refresh itself took.
func TestNextUpstreamRefreshPoll(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current time.Duration
		want    time.Duration
	}{
		{"doubles from the initial interval", upstreamRefreshLockPollInitial, 50 * time.Millisecond},
		{"keeps doubling below the cap", 100 * time.Millisecond, 200 * time.Millisecond},
		{"clamps a double that would overshoot", 300 * time.Millisecond, upstreamRefreshLockPollMax},
		{"stays at the cap", upstreamRefreshLockPollMax, upstreamRefreshLockPollMax},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, nextUpstreamRefreshPoll(tt.current))
		})
	}
}

// TestNewUpstreamTokenRefresher_ResolvesLocker covers the one production site
// that resolves the lease capability. Without it, dropping that resolution —
// or a storage decorator that stops exposing Unwrap — would silently degrade
// every multi-replica deployment to CAS-only coordination with the rest of the
// suite still green, since every other test supplies the locker directly.
func TestNewUpstreamTokenRefresher_ResolvesLocker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		storage    func(t *testing.T) storage.UpstreamTokenStorage
		wantLocker bool
	}{
		{
			name: "backend providing the capability",
			storage: func(t *testing.T) storage.UpstreamTokenStorage {
				t.Helper()
				return newLockTestMemoryStorage(t)
			},
			wantLocker: true,
		},
		{
			name: "backend behind the decorators newServer applies",
			storage: func(t *testing.T) storage.UpstreamTokenStorage {
				t.Helper()
				decorated, err := storage.NewClientAdmissionStorage(newLockTestMemoryStorage(t), nil)
				require.NoError(t, err)
				return decorated
			},
			wantLocker: true,
		},
		{
			name: "backend without the capability",
			storage: func(t *testing.T) storage.UpstreamTokenStorage {
				t.Helper()
				return storagemocks.NewMockUpstreamTokenStorage(gomock.NewController(t))
			},
			wantLocker: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider := upstreammocks.NewMockOAuth2Provider(gomock.NewController(t))
			refresher := newUpstreamTokenRefresher(
				[]handlers.NamedUpstream{{Name: "github", Provider: provider}},
				tt.storage(t),
				24*time.Hour,
			)
			require.NotNil(t, refresher)

			impl, ok := refresher.(*upstreamTokenRefresher)
			require.True(t, ok)
			if tt.wantLocker {
				assert.NotNil(t, impl.locker,
					"a backend providing the lease must be coordinated across replicas")
				return
			}
			assert.Nil(t, impl.locker)
		})
	}
}

func newLockTestMemoryStorage(t *testing.T) *storage.MemoryStorage {
	t.Helper()
	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = stor.Close() })
	return stor
}

// newLockTestRefresher builds a refresher whose provider mock has no
// RefreshTokens expectation, so any upstream redemption fails the test.
func newLockTestRefresher(
	t *testing.T,
	ctrl *gomock.Controller,
	store storage.UpstreamTokenStorage,
	locker storage.UpstreamRefreshLocker,
) *upstreamTokenRefresher {
	t.Helper()
	return &upstreamTokenRefresher{
		providers:            map[string]upstream.OAuth2Provider{"github": upstreammocks.NewMockOAuth2Provider(ctrl)},
		storage:              store,
		locker:               locker,
		refreshTokenLifespan: 24 * time.Hour,
	}
}
