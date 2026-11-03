// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"errors"
	"sync/atomic"
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

// fakeLocker stands in for a storage backend's UpstreamRefreshLocker. It is
// hand-written rather than generated because every case here turns on how the
// lease behaves over successive attempts — held, then free, then held by the
// caller — which reads far more clearly as a scripted sequence than as
// per-call mock expectations.
type fakeLocker struct {
	// results is consumed one entry per acquisition attempt; the last entry
	// repeats once exhausted, so a waiting caller can spin as long as it
	// likes against a lease that never frees.
	results []error

	attempts     atomic.Int32
	released     atomic.Int32
	releaseErr   error
	lastRowID    atomic.Value // storage.UpstreamTokenRowID
	lastLeaseTTL atomic.Int64
}

func (f *fakeLocker) AcquireUpstreamRefreshLock(
	_ context.Context, rowID storage.UpstreamTokenRowID, ttl time.Duration,
) (storage.UpstreamRefreshLease, error) {
	f.lastRowID.Store(rowID)
	f.lastLeaseTTL.Store(int64(ttl))
	n := int(f.attempts.Add(1)) - 1
	if n >= len(f.results) {
		n = len(f.results) - 1
	}
	if err := f.results[n]; err != nil {
		return nil, err
	}
	return &fakeLease{locker: f}, nil
}

type fakeLease struct{ locker *fakeLocker }

func (l *fakeLease) Release(context.Context) error {
	l.locker.released.Add(1)
	return l.locker.releaseErr
}

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

// TestCoordinatedRefresh_WaiterNeverRedeems is the core of issue #4122: a
// replica that does not hold the row's lease must not call the upstream
// provider, whatever happens while it waits. A provider mock with no
// RefreshTokens expectation fails the test if it is called.
func TestCoordinatedRefresh_WaiterNeverRedeems(t *testing.T) {
	t.Parallel()

	held := storage.ErrUpstreamRefreshLockHeld
	caller := expiredRow("old-refresh")

	t.Run("returns the holder's row once it appears", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		winner := unexpiredRow()

		// First re-read still shows the caller's own row: the holder has not
		// finished. The second shows the rotated row it stored.
		gomock.InOrder(
			store.EXPECT().GetUpstreamTokens(gomock.Any(), "session", "github").Return(caller, nil),
			store.EXPECT().GetUpstreamTokens(gomock.Any(), "session", "github").Return(winner, nil),
		)

		locker := &fakeLocker{results: []error{held}}
		r := newLockTestRefresher(t, ctrl, store, locker)

		got, err := r.coordinatedRefresh(t.Context(), "session", "row", caller)
		require.NoError(t, err)
		assert.Equal(t, winner, got, "the waiter must hand back what the holder stored")
		assert.Zero(t, locker.released.Load(), "a waiter holds nothing, so it releases nothing")
	})

	t.Run("fails when the wait runs out", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		// The holder never finishes, so every re-read keeps showing the
		// caller's own row.
		store.EXPECT().GetUpstreamTokens(gomock.Any(), "session", "github").
			AnyTimes().Return(caller, nil)

		locker := &fakeLocker{results: []error{held}}
		r := newLockTestRefresher(t, ctrl, store, locker)

		ctx, cancel := context.WithTimeout(t.Context(), 120*time.Millisecond)
		defer cancel()

		got, err := r.coordinatedRefresh(ctx, "session", "row", caller)
		require.Error(t, err)
		assert.Nil(t, got)
		assert.ErrorIs(t, err, context.DeadlineExceeded,
			"running out of time is a failure, not a licence to redeem uncoordinated")
		assert.Greater(t, locker.attempts.Load(), int32(1), "the waiter should retry the lease")
	})

	t.Run("fails closed when the lock backend errors", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		backendDown := errors.New("redis: connection refused")

		locker := &fakeLocker{results: []error{backendDown}}
		r := newLockTestRefresher(t, ctrl, store, locker)

		got, err := r.coordinatedRefresh(t.Context(), "session", "row", caller)
		require.Error(t, err)
		assert.Nil(t, got)
		assert.ErrorIs(t, err, backendDown)
		assert.Equal(t, int32(1), locker.attempts.Load(),
			"a backend fault is not retried; the caller cannot know whether another replica is redeeming")
	})

	t.Run("ignores a row that changed but is still expired", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		// A different row, but expired: the holder has not produced anything
		// usable, so the waiter must keep waiting rather than return it.
		store.EXPECT().GetUpstreamTokens(gomock.Any(), "session", "github").
			AnyTimes().Return(expiredRow("some-other-refresh"), nil)

		locker := &fakeLocker{results: []error{held}}
		r := newLockTestRefresher(t, ctrl, store, locker)

		ctx, cancel := context.WithTimeout(t.Context(), 120*time.Millisecond)
		defer cancel()

		_, err := r.coordinatedRefresh(ctx, "session", "row", caller)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

// TestCoordinatedRefresh_TakesOverFreedLease covers the holder that crashed or
// whose lease expired: the row is unchanged, so the waiter must stop waiting
// and become the leader rather than sit out the TTL.
func TestCoordinatedRefresh_TakesOverFreedLease(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := storagemocks.NewMockUpstreamTokenStorage(ctrl)
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

	locker := &fakeLocker{results: []error{storage.ErrUpstreamRefreshLockHeld, nil}}
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
	assert.Equal(t, int32(1), locker.released.Load(), "the new leader must give the lease back")
}

// TestCoordinatedRefresh_LeaderReleases checks the lease is given back on
// every leader exit path, including the short-circuit where the row turned out
// to be fresh and no upstream call happened at all. A lease left behind would
// stall every other replica for its full TTL.
func TestCoordinatedRefresh_LeaderReleases(t *testing.T) {
	t.Parallel()

	t.Run("after a short-circuit on the authoritative re-read", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		winner := unexpiredRow()
		store.EXPECT().GetUpstreamTokens(gomock.Any(), "session", "github").Return(winner, nil)

		locker := &fakeLocker{results: []error{nil}}
		r := newLockTestRefresher(t, ctrl, store, locker)

		got, err := r.coordinatedRefresh(t.Context(), "session", "row", expiredRow("old-refresh"))
		require.NoError(t, err)
		assert.Equal(t, winner, got)
		assert.Equal(t, int32(1), locker.released.Load())
	})

	t.Run("after the refresh fails", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		store.EXPECT().GetUpstreamTokens(gomock.Any(), "session", "github").
			Return(nil, errors.New("storage unavailable"))

		locker := &fakeLocker{results: []error{nil}}
		r := newLockTestRefresher(t, ctrl, store, locker)

		_, err := r.coordinatedRefresh(t.Context(), "session", "row", expiredRow("old-refresh"))
		require.Error(t, err)
		assert.Equal(t, int32(1), locker.released.Load())
	})

	t.Run("a failed release does not fail the refresh", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storagemocks.NewMockUpstreamTokenStorage(ctrl)
		winner := unexpiredRow()
		store.EXPECT().GetUpstreamTokens(gomock.Any(), "session", "github").Return(winner, nil)

		locker := &fakeLocker{results: []error{nil}, releaseErr: errors.New("redis: timeout")}
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
	rowID := storage.UpstreamTokenRowID("resolved-row-id")
	store.EXPECT().ResolveUpstreamTokenRowID(gomock.Any(), "session", "github").Return(rowID, nil)
	store.EXPECT().GetUpstreamTokens(gomock.Any(), "session", "github").
		Return(unexpiredRow(), nil)

	locker := &fakeLocker{results: []error{nil}}
	r := newLockTestRefresher(t, ctrl, store, locker)

	_, err := r.RefreshAndStore(t.Context(), "session", expiredRow("old-refresh"))
	require.NoError(t, err)

	assert.Equal(t, rowID, locker.lastRowID.Load(),
		"the lease must cover the same row singleflight deduplicates")
	assert.Greater(t, time.Duration(locker.lastLeaseTTL.Load()), refreshTimeout,
		"the lease must outlast the holder's whole read-redeem-write sequence")
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
