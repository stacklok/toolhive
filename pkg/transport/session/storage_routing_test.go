// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	session "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/transport/session/mocks"
)

func TestRoutingStorage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("Store then Load serves from local cache", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		remote := mocks.NewMockStorage(ctrl)
		remote.EXPECT().Close().Return(nil)
		rs := session.NewRoutingStorage(10, remote)
		defer rs.Close() //nolint:errcheck

		s := session.NewProxySession("s1")

		remote.EXPECT().Store(ctx, s).Return(nil)
		require.NoError(t, rs.Store(ctx, s))

		// Load must hit local cache: Touch refreshes TTL, no remote Load call.
		remote.EXPECT().Touch(ctx, "s1").Return(nil)
		loaded, err := rs.Load(ctx, "s1")
		require.NoError(t, err)
		assert.Equal(t, "s1", loaded.ID())
	})

	t.Run("Load cache miss fetches from remote and caches result", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		remote := mocks.NewMockStorage(ctrl)
		remote.EXPECT().Close().Return(nil)
		rs := session.NewRoutingStorage(10, remote)
		defer rs.Close() //nolint:errcheck

		s := session.NewProxySession("s2")

		// First Load: cache miss → remote fetch.
		remote.EXPECT().Load(ctx, "s2").Return(s, nil)
		loaded, err := rs.Load(ctx, "s2")
		require.NoError(t, err)
		assert.Equal(t, "s2", loaded.ID())

		// Second Load: must hit local cache — Touch called, no remote Load.
		remote.EXPECT().Touch(ctx, "s2").Return(nil)
		_, err = rs.Load(ctx, "s2")
		require.NoError(t, err)
	})

	t.Run("Load not found returns ErrSessionNotFound", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		remote := mocks.NewMockStorage(ctrl)
		remote.EXPECT().Close().Return(nil)
		rs := session.NewRoutingStorage(10, remote)
		defer rs.Close() //nolint:errcheck

		remote.EXPECT().Load(ctx, "missing").Return(nil, session.ErrSessionNotFound)
		_, err := rs.Load(ctx, "missing")
		assert.Equal(t, session.ErrSessionNotFound, err)
	})

	t.Run("Delete removes from both tiers", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		remote := mocks.NewMockStorage(ctrl)
		remote.EXPECT().Close().Return(nil)
		rs := session.NewRoutingStorage(10, remote)
		defer rs.Close() //nolint:errcheck

		s := session.NewProxySession("s3")

		remote.EXPECT().Store(ctx, s).Return(nil)
		require.NoError(t, rs.Store(ctx, s))

		remote.EXPECT().Delete(ctx, "s3").Return(nil)
		require.NoError(t, rs.Delete(ctx, "s3"))

		// Local cache is gone; next Load is a miss → goes to remote.
		remote.EXPECT().Load(ctx, "s3").Return(nil, session.ErrSessionNotFound)
		_, err := rs.Load(ctx, "s3")
		assert.Equal(t, session.ErrSessionNotFound, err)
	})

	t.Run("Delete calls remote even when entry not in local cache", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		remote := mocks.NewMockStorage(ctrl)
		remote.EXPECT().Close().Return(nil)
		rs := session.NewRoutingStorage(10, remote)
		defer rs.Close() //nolint:errcheck

		// Local cache never had this entry.
		remote.EXPECT().Delete(ctx, "remote-only").Return(nil)
		require.NoError(t, rs.Delete(ctx, "remote-only"))
	})

	t.Run("Delete non-existent returns no error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		remote := mocks.NewMockStorage(ctrl)
		remote.EXPECT().Close().Return(nil)
		rs := session.NewRoutingStorage(10, remote)
		defer rs.Close() //nolint:errcheck

		remote.EXPECT().Delete(ctx, "ghost").Return(nil)
		assert.NoError(t, rs.Delete(ctx, "ghost"))
	})

	t.Run("Store remote failure does not pollute local cache", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		remote := mocks.NewMockStorage(ctrl)
		remote.EXPECT().Close().Return(nil)
		rs := session.NewRoutingStorage(10, remote)
		defer rs.Close() //nolint:errcheck

		storeErr := errors.New("remote unavailable")
		s := session.NewProxySession("s4")

		remote.EXPECT().Store(ctx, s).Return(storeErr)
		err := rs.Store(ctx, s)
		require.ErrorIs(t, err, storeErr)

		// Local cache must not have been updated; Load is a miss → remote call.
		remote.EXPECT().Load(ctx, "s4").Return(nil, session.ErrSessionNotFound)
		_, loadErr := rs.Load(ctx, "s4")
		assert.Equal(t, session.ErrSessionNotFound, loadErr)
	})

	t.Run("LRU eviction and remote recovery", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		remote := mocks.NewMockStorage(ctrl)
		remote.EXPECT().Close().Return(nil)
		rs := session.NewRoutingStorage(2, remote)
		defer rs.Close() //nolint:errcheck

		s1 := session.NewProxySession("evict-1")
		s2 := session.NewProxySession("evict-2")
		s3 := session.NewProxySession("evict-3")

		remote.EXPECT().Store(ctx, s1).Return(nil)
		remote.EXPECT().Store(ctx, s2).Return(nil)
		// Storing s3 evicts s1 from the LRU (capacity=2).
		remote.EXPECT().Store(ctx, s3).Return(nil)
		require.NoError(t, rs.Store(ctx, s1))
		require.NoError(t, rs.Store(ctx, s2))
		require.NoError(t, rs.Store(ctx, s3))

		// s1 is evicted from local; remote.Load recovers it.
		remote.EXPECT().Load(ctx, "evict-1").Return(s1, nil)
		loaded, err := rs.Load(ctx, "evict-1")
		require.NoError(t, err)
		assert.Equal(t, "evict-1", loaded.ID())
	})

	t.Run("DeleteExpired delegates to remote", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		remote := mocks.NewMockStorage(ctrl)
		remote.EXPECT().Close().Return(nil)
		rs := session.NewRoutingStorage(10, remote)
		defer rs.Close() //nolint:errcheck

		cutoff := time.Now().Add(-1 * time.Hour)
		remote.EXPECT().DeleteExpired(ctx, cutoff).Return(nil)
		require.NoError(t, rs.DeleteExpired(ctx, cutoff))
	})

	t.Run("Touch delegates to remote", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		remote := mocks.NewMockStorage(ctrl)
		remote.EXPECT().Close().Return(nil)
		rs := session.NewRoutingStorage(10, remote)
		defer rs.Close() //nolint:errcheck

		remote.EXPECT().Touch(ctx, "s5").Return(nil)
		require.NoError(t, rs.Touch(ctx, "s5"))
	})

	t.Run("Close closes remote and purges local cache", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		remote := mocks.NewMockStorage(ctrl)
		rs := session.NewRoutingStorage(10, remote)

		s1 := session.NewProxySession("c1")
		s2 := session.NewProxySession("c2")

		remote.EXPECT().Store(ctx, s1).Return(nil)
		remote.EXPECT().Store(ctx, s2).Return(nil)
		require.NoError(t, rs.Store(ctx, s1))
		require.NoError(t, rs.Store(ctx, s2))

		remote.EXPECT().Close().Return(nil)
		require.NoError(t, rs.Close())

		// Local cache purged: Load must miss locally and go to remote.
		remote.EXPECT().Load(ctx, "c1").Return(nil, session.ErrSessionNotFound)
		_, err := rs.Load(ctx, "c1")
		assert.Equal(t, session.ErrSessionNotFound, err)
	})

	t.Run("maxLocalEntries zero panics", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		remote := mocks.NewMockStorage(ctrl)
		assert.Panics(t, func() {
			session.NewRoutingStorage(0, remote)
		})
	})

	t.Run("maxLocalEntries negative panics", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		remote := mocks.NewMockStorage(ctrl)
		assert.Panics(t, func() {
			session.NewRoutingStorage(-1, remote)
		})
	})

	t.Run("nil remote panics", func(t *testing.T) {
		t.Parallel()
		assert.Panics(t, func() {
			session.NewRoutingStorage(10, nil)
		})
	})

	t.Run("Load after direct remote Store bypasses local cache on first access", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		remote := mocks.NewMockStorage(ctrl)
		remote.EXPECT().Close().Return(nil)
		rs := session.NewRoutingStorage(10, remote)
		defer rs.Close() //nolint:errcheck

		s := session.NewProxySession("direct")

		// No rs.Store — local cache is empty; Load must call remote.
		remote.EXPECT().Load(ctx, "direct").Return(s, nil)
		loaded, err := rs.Load(ctx, "direct")
		require.NoError(t, err)
		assert.Equal(t, "direct", loaded.ID())
	})

	t.Run("Load evicts local entry when remote Touch reports key expired", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		remote := mocks.NewMockStorage(ctrl)
		remote.EXPECT().Close().Return(nil)
		rs := session.NewRoutingStorage(10, remote)
		defer rs.Close() //nolint:errcheck

		s := session.NewProxySession("expired-remote")

		remote.EXPECT().Store(ctx, s).Return(nil)
		require.NoError(t, rs.Store(ctx, s))

		// Local cache hit, but Touch signals key is gone → local entry evicted, ErrSessionNotFound returned.
		remote.EXPECT().Touch(ctx, "expired-remote").Return(session.ErrSessionNotFound)
		_, err := rs.Load(ctx, "expired-remote")
		assert.Equal(t, session.ErrSessionNotFound, err)

		// Local cache was evicted; next Load falls through to remote.Load.
		remote.EXPECT().Load(ctx, "expired-remote").Return(s, nil)
		loaded, err := rs.Load(ctx, "expired-remote")
		require.NoError(t, err)
		assert.Equal(t, "expired-remote", loaded.ID())
	})

	t.Run("Touch error on local hit is logged but does not fail Load", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		remote := mocks.NewMockStorage(ctrl)
		remote.EXPECT().Close().Return(nil)
		rs := session.NewRoutingStorage(10, remote)
		defer rs.Close() //nolint:errcheck

		s := session.NewProxySession("s6")

		remote.EXPECT().Store(ctx, s).Return(nil)
		require.NoError(t, rs.Store(ctx, s))

		touchErr := errors.New("transient network error")
		remote.EXPECT().Touch(ctx, "s6").Return(touchErr)
		loaded, err := rs.Load(ctx, "s6")
		require.NoError(t, err)
		assert.Equal(t, "s6", loaded.ID())
	})

	t.Run("concurrent Store and Load under race detector", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		remote := mocks.NewMockStorage(ctrl)
		remote.EXPECT().Close().Return(nil)
		rs := session.NewRoutingStorage(100, remote)
		defer rs.Close() //nolint:errcheck

		s := session.NewProxySession("race-session")
		const n = 50

		remote.EXPECT().Store(ctx, s).Return(nil).AnyTimes()
		remote.EXPECT().Touch(ctx, "race-session").Return(nil).AnyTimes()
		remote.EXPECT().Load(ctx, "race-session").Return(s, nil).AnyTimes()

		done := make(chan struct{})
		go func() {
			defer close(done)
			for range n {
				_ = rs.Store(ctx, s)
			}
		}()
		for range n {
			_, _ = rs.Load(ctx, "race-session")
		}
		<-done
	})
}
