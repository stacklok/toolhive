// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lockerBackend names one UpstreamRefreshLocker implementation and supplies a
// fresh instance plus a way to push its leases past their TTL. Both backends
// run the same cases so the contract (non-blocking acquisition, takeover after
// expiry, ownership-checked release) cannot drift between them.
type lockerBackend struct {
	name string
	// with runs fn against a fresh backend. expire advances that backend past
	// d so an already-acquired lease is seen as expired, without the test
	// sleeping for a real TTL.
	with func(t *testing.T, fn func(ctx context.Context, locker UpstreamRefreshLocker, expire func(d time.Duration)))
}

func lockerBackends() []lockerBackend {
	return []lockerBackend{
		{
			name: "memory",
			with: func(t *testing.T, fn func(context.Context, UpstreamRefreshLocker, func(time.Duration))) {
				t.Helper()
				s := NewMemoryStorage()
				t.Cleanup(func() { _ = s.Close() })
				// MemoryStorage stamps leases from the wall clock, so the only
				// way to pass their expiry is to wait it out. Every caller
				// below asks for a TTL small enough that this is a few
				// milliseconds.
				fn(t.Context(), s, func(d time.Duration) { time.Sleep(d) })
			},
		},
		{
			name: "redis",
			with: func(t *testing.T, fn func(context.Context, UpstreamRefreshLocker, func(time.Duration))) {
				t.Helper()
				s, mr := newTestRedisStorage(t)
				t.Cleanup(func() {
					_ = s.Close()
					mr.Close()
				})
				fn(t.Context(), s, mr.FastForward)
			},
		},
	}
}

const testRowID = UpstreamTokenRowID("row-under-test")

// TestAcquireUpstreamRefreshLock covers the capability that keeps two replicas
// from redeeming the same single-use upstream refresh token: only one of them
// may hold a row's lease at a time, and a holder that never releases must not
// block the row beyond the lease TTL.
func TestAcquireUpstreamRefreshLock(t *testing.T) {
	t.Parallel()

	for _, backend := range lockerBackends() {
		t.Run(backend.name, func(t *testing.T) {
			t.Parallel()

			t.Run("second acquisition of a held row reports ErrUpstreamRefreshLockHeld", func(t *testing.T) {
				t.Parallel()
				backend.with(t, func(ctx context.Context, locker UpstreamRefreshLocker, _ func(time.Duration)) {
					lease, err := locker.AcquireUpstreamRefreshLock(ctx, testRowID, time.Minute)
					require.NoError(t, err)
					require.NotNil(t, lease)

					second, err := locker.AcquireUpstreamRefreshLock(ctx, testRowID, time.Minute)
					assert.Nil(t, second)
					assert.ErrorIs(t, err, ErrUpstreamRefreshLockHeld,
						"a waiter must be told to wait, not handed a second lease")
				})
			})

			t.Run("a different row is unaffected by a held lease", func(t *testing.T) {
				t.Parallel()
				backend.with(t, func(ctx context.Context, locker UpstreamRefreshLocker, _ func(time.Duration)) {
					_, err := locker.AcquireUpstreamRefreshLock(ctx, testRowID, time.Minute)
					require.NoError(t, err)

					other, err := locker.AcquireUpstreamRefreshLock(ctx, "another-row", time.Minute)
					require.NoError(t, err, "the lease is per row, not global")
					assert.NotNil(t, other)
				})
			})

			t.Run("release lets the next caller take the lease", func(t *testing.T) {
				t.Parallel()
				backend.with(t, func(ctx context.Context, locker UpstreamRefreshLocker, _ func(time.Duration)) {
					lease, err := locker.AcquireUpstreamRefreshLock(ctx, testRowID, time.Minute)
					require.NoError(t, err)
					require.NoError(t, lease.Release(ctx))

					next, err := locker.AcquireUpstreamRefreshLock(ctx, testRowID, time.Minute)
					require.NoError(t, err, "a released lease must not make a waiter sit out the TTL")
					assert.NotNil(t, next)
				})
			})

			t.Run("releasing twice is a no-op", func(t *testing.T) {
				t.Parallel()
				backend.with(t, func(ctx context.Context, locker UpstreamRefreshLocker, _ func(time.Duration)) {
					lease, err := locker.AcquireUpstreamRefreshLock(ctx, testRowID, time.Minute)
					require.NoError(t, err)
					require.NoError(t, lease.Release(ctx))
					assert.NoError(t, lease.Release(ctx))
				})
			})

			t.Run("a lease that expires can be taken over", func(t *testing.T) {
				t.Parallel()
				backend.with(t, func(ctx context.Context, locker UpstreamRefreshLocker, expire func(time.Duration)) {
					_, err := locker.AcquireUpstreamRefreshLock(ctx, testRowID, 20*time.Millisecond)
					require.NoError(t, err)

					expire(40 * time.Millisecond)

					takenOver, err := locker.AcquireUpstreamRefreshLock(ctx, testRowID, time.Minute)
					require.NoError(t, err, "a crashed holder must not block the row past its TTL")
					assert.NotNil(t, takenOver)
				})
			})

			t.Run("an expired holder's release does not free the new holder's lease", func(t *testing.T) {
				t.Parallel()
				backend.with(t, func(ctx context.Context, locker UpstreamRefreshLocker, expire func(time.Duration)) {
					stale, err := locker.AcquireUpstreamRefreshLock(ctx, testRowID, 20*time.Millisecond)
					require.NoError(t, err)

					expire(40 * time.Millisecond)

					_, err = locker.AcquireUpstreamRefreshLock(ctx, testRowID, time.Minute)
					require.NoError(t, err)

					// The stale holder unwinds late and releases. If that
					// deleted the new holder's lease, a third replica could
					// start redeeming alongside it.
					require.NoError(t, stale.Release(ctx))

					third, err := locker.AcquireUpstreamRefreshLock(ctx, testRowID, time.Minute)
					assert.Nil(t, third)
					assert.ErrorIs(t, err, ErrUpstreamRefreshLockHeld,
						"the live holder's lease must survive a stale holder's release")
				})
			})

			t.Run("rejects an empty row ID and a non-positive TTL", func(t *testing.T) {
				t.Parallel()
				backend.with(t, func(ctx context.Context, locker UpstreamRefreshLocker, _ func(time.Duration)) {
					_, err := locker.AcquireUpstreamRefreshLock(ctx, "", time.Minute)
					assert.ErrorIs(t, err, fosite.ErrInvalidRequest)

					_, err = locker.AcquireUpstreamRefreshLock(ctx, testRowID, 0)
					assert.ErrorIs(t, err, fosite.ErrInvalidRequest)
				})
			})
		})
	}
}

// TestRedisUpstreamRefreshLock_BackendFailures covers what the Redis backend
// reports when it cannot be reached. The distinction matters: the refresher
// treats ErrUpstreamRefreshLockHeld as "wait for the holder" and anything else
// as "fail closed", so a fault reported as a refusal would park a request that
// should have errored, and a refusal reported as a fault would fail a request
// that only needed to wait.
func TestRedisUpstreamRefreshLock_BackendFailures(t *testing.T) {
	t.Parallel()

	t.Run("an unreachable backend is a fault, not a refusal", func(t *testing.T) {
		t.Parallel()
		s, mr := newTestRedisStorage(t)
		t.Cleanup(func() { _ = s.Close() })
		mr.Close()

		lease, err := s.AcquireUpstreamRefreshLock(t.Context(), testRowID, time.Minute)
		assert.Nil(t, lease)
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrUpstreamRefreshLockHeld,
			"a caller must fail closed here, not wait for a holder that does not exist")
	})

	t.Run("release reports a backend that went away", func(t *testing.T) {
		t.Parallel()
		s, mr := newTestRedisStorage(t)
		t.Cleanup(func() { _ = s.Close() })

		lease, err := s.AcquireUpstreamRefreshLock(t.Context(), testRowID, time.Minute)
		require.NoError(t, err)

		mr.Close()
		// Reported rather than swallowed, so the caller can log that the row
		// stays locked until the TTL runs out.
		assert.Error(t, lease.Release(t.Context()))
	})

	t.Run("a lease whose acquisition reply was lost does not block the row", func(t *testing.T) {
		t.Parallel()
		s, mr := newTestRedisStorage(t)
		t.Cleanup(func() {
			_ = s.Close()
			mr.Close()
		})

		// Stand in for the reply that never came back: the SET landed, so the
		// key is there, but no caller holds a lease over it. Clearing it is
		// best-effort, and the owner token is the one the failed acquisition
		// generated, so only that key is affected.
		key := redisUpstreamRefreshLockKey(s.keyPrefix, testRowID)
		require.NoError(t, s.client.Set(t.Context(), key, "orphaned-owner", time.Hour).Err())
		s.clearUnreleasableUpstreamRefreshLock(t.Context(), key, "orphaned-owner")

		lease, err := s.AcquireUpstreamRefreshLock(t.Context(), testRowID, time.Minute)
		require.NoError(t, err, "a lease nobody holds must not block the row for its whole TTL")
		assert.NotNil(t, lease)
	})

	t.Run("clearing leaves a lease another holder owns alone", func(t *testing.T) {
		t.Parallel()
		s, mr := newTestRedisStorage(t)
		t.Cleanup(func() {
			_ = s.Close()
			mr.Close()
		})

		held, err := s.AcquireUpstreamRefreshLock(t.Context(), testRowID, time.Minute)
		require.NoError(t, err)
		require.NotNil(t, held)

		// The cleanup runs with the owner token of an acquisition that failed,
		// which is not the one stored, so the live lease must survive.
		key := redisUpstreamRefreshLockKey(s.keyPrefix, testRowID)
		s.clearUnreleasableUpstreamRefreshLock(t.Context(), key, "some-other-owner")

		_, err = s.AcquireUpstreamRefreshLock(t.Context(), testRowID, time.Minute)
		assert.ErrorIs(t, err, ErrUpstreamRefreshLockHeld)
	})
}

// TestMemoryStorage_cleanupExpiredRefreshLeases checks that a lease nobody
// releases does not accumulate in the in-memory backend, and that the sweep
// cannot take a live lease with it.
func TestMemoryStorage_cleanupExpiredRefreshLeases(t *testing.T) {
	t.Parallel()

	t.Run("sweeps an expired lease and keeps a live one", func(t *testing.T) {
		t.Parallel()
		ctx, s := newLeaseSweepStorage(t)

		_, err := s.AcquireUpstreamRefreshLock(ctx, "expiring-row", 10*time.Millisecond)
		require.NoError(t, err)
		_, err = s.AcquireUpstreamRefreshLock(ctx, "live-row", time.Hour)
		require.NoError(t, err)

		time.Sleep(30 * time.Millisecond)
		s.cleanupExpired()

		s.mu.RLock()
		_, expiredPresent := s.upstreamRefreshLeases["expiring-row"]
		_, livePresent := s.upstreamRefreshLeases["live-row"]
		s.mu.RUnlock()

		assert.False(t, expiredPresent, "an expired lease should be swept")
		assert.True(t, livePresent, "a live lease must survive the sweep")
	})

	// cleanupExpired collects under the read lock and deletes under the write
	// lock, so a lease it collected may have been released and re-acquired by
	// the time the delete runs. The delete is driven directly here because
	// nothing can interleave with it from outside.
	t.Run("keeps a lease re-acquired since it was collected", func(t *testing.T) {
		t.Parallel()
		ctx, s := newLeaseSweepStorage(t)

		expiring, err := s.AcquireUpstreamRefreshLock(ctx, "contended-row", 10*time.Millisecond)
		require.NoError(t, err)

		time.Sleep(30 * time.Millisecond)
		collected := []UpstreamTokenRowID{"contended-row"}

		// The holder gives up its expired lease and a new one takes the row.
		require.NoError(t, expiring.Release(ctx))
		_, err = s.AcquireUpstreamRefreshLock(ctx, "contended-row", time.Hour)
		require.NoError(t, err)

		s.mu.Lock()
		s.deleteExpiredRefreshLeasesLocked(time.Now(), collected)
		_, present := s.upstreamRefreshLeases["contended-row"]
		s.mu.Unlock()

		assert.True(t, present,
			"deleting the new holder's lease would let a second replica redeem alongside it")
	})
}

func newLeaseSweepStorage(t *testing.T) (context.Context, *MemoryStorage) {
	t.Helper()
	s := NewMemoryStorage()
	t.Cleanup(func() { _ = s.Close() })
	return t.Context(), s
}

// TestUpstreamRefreshLockerFrom covers capability discovery. Decorators embed
// the Storage interface, so they do not carry the backend's optional
// capabilities; the helper has to peel them or the refresher would silently
// lose cross-process coordination whenever a decorator is installed.
func TestUpstreamRefreshLockerFrom(t *testing.T) {
	t.Parallel()

	t.Run("finds the capability on a bare backend", func(t *testing.T) {
		t.Parallel()
		s := NewMemoryStorage()
		t.Cleanup(func() { _ = s.Close() })
		assert.NotNil(t, UpstreamRefreshLockerFrom(s))
	})

	t.Run("finds the capability through a decorator", func(t *testing.T) {
		t.Parallel()
		s := NewMemoryStorage()
		t.Cleanup(func() { _ = s.Close() })
		assert.NotNil(t, UpstreamRefreshLockerFrom(NewCIMDShapeGuardStorage(s)))
	})

	t.Run("returns nil for a backend without the capability", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, UpstreamRefreshLockerFrom(lockerlessStorage{}))
	})

	t.Run("finds the capability through a chain of decorators", func(t *testing.T) {
		t.Parallel()
		s := NewMemoryStorage()
		t.Cleanup(func() { _ = s.Close() })
		assert.NotNil(t, UpstreamRefreshLockerFrom(
			NewCIMDShapeGuardStorage(NewCIMDShapeGuardStorage(s)),
		))
	})

	t.Run("returns nil for a decorator over a backend without the capability", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, UpstreamRefreshLockerFrom(NewCIMDShapeGuardStorage(lockerlessBackend{})))
	})

	t.Run("returns nil when the chain ends in an Unwrap that returns nothing", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, UpstreamRefreshLockerFrom(unwrappingStorage{}))
	})

	t.Run("returns nil for a nil storage", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, UpstreamRefreshLockerFrom(nil))
	})
}

// unwrappingStorage is a decorator whose Unwrap returns nothing, standing in
// for a chain that ends before any backend provides the capability.
type unwrappingStorage struct {
	UpstreamTokenStorage
}

func (unwrappingStorage) Unwrap() Storage { return nil }

// lockerlessBackend is a full Storage that provides no lease, so a real
// decorator can be put over it.
type lockerlessBackend struct {
	Storage
}

// lockerlessStorage is an UpstreamTokenStorage that provides neither the lock
// capability nor Unwrap, standing in for a third-party backend.
type lockerlessStorage struct {
	UpstreamTokenStorage
}
