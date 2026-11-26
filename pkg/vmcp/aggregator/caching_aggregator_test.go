// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/headerforward"
)

func ctxWithSubject(subject string) context.Context {
	return auth.WithIdentity(context.Background(),
		&auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: subject}})
}

var testBackends = []vmcp.Backend{{ID: "b1"}, {ID: "b2"}}

// TestCachingAggregator_CacheHitWithinTTL: two calls with the same identity and backend set
// within the TTL hit the cache, so the wrapped aggregator sweeps only once.
func TestCachingAggregator_CacheHitWithinTTL(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)
	caps := &aggregator.AggregatedCapabilities{}
	mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(caps, nil).Times(1)

	c := aggregator.NewCachingAggregator(mock, time.Hour)
	ctx := ctxWithSubject("alice")

	first, err := c.AggregateCapabilities(ctx, testBackends)
	require.NoError(t, err)
	second, err := c.AggregateCapabilities(ctx, testBackends)
	require.NoError(t, err)
	assert.Same(t, first, second, "the cached view must be returned on a hit")
	assert.Same(t, caps, second)
}

// TestCachingAggregator_KeyedByIdentityAndHeaders: distinct subjects, and distinct forwarded
// credentials for the same subject, are distinct cache keys — each sweeps the backends. This
// is the security-critical property: one caller's view is never served to another.
func TestCachingAggregator_KeyedByIdentityAndHeaders(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)
	// alice, bob, and alice-with-a-forwarded-token are three distinct keys → three sweeps.
	mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).
		Return(&aggregator.AggregatedCapabilities{}, nil).Times(3)

	c := aggregator.NewCachingAggregator(mock, time.Hour)

	_, err := c.AggregateCapabilities(ctxWithSubject("alice"), testBackends)
	require.NoError(t, err)
	_, err = c.AggregateCapabilities(ctxWithSubject("bob"), testBackends)
	require.NoError(t, err)
	aliceWithToken := headerforward.WithForwardedHeaders(ctxWithSubject("alice"),
		map[string]string{"Authorization": "Bearer xyz"})
	_, err = c.AggregateCapabilities(aliceWithToken, testBackends)
	require.NoError(t, err)
}

// TestCachingAggregator_TTLExpiry: once the TTL elapses, the next call re-sweeps.
func TestCachingAggregator_TTLExpiry(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)
	mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).
		Return(&aggregator.AggregatedCapabilities{}, nil).Times(2)

	c := aggregator.NewCachingAggregator(mock, 20*time.Millisecond)
	ctx := ctxWithSubject("alice")

	_, err := c.AggregateCapabilities(ctx, testBackends)
	require.NoError(t, err)
	time.Sleep(40 * time.Millisecond)
	_, err = c.AggregateCapabilities(ctx, testBackends)
	require.NoError(t, err)
}

// TestCachingAggregator_DisabledWhenTTLNonPositive: a ttl <= 0 returns the wrapped aggregator
// unwrapped, so callers cannot silently get a permanently-stale cache.
func TestCachingAggregator_DisabledWhenTTLNonPositive(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)

	assert.Same(t, mock, aggregator.NewCachingAggregator(mock, 0))
	assert.Same(t, mock, aggregator.NewCachingAggregator(mock, -time.Second))

	// A nil aggregator is returned as-is so the downstream nil check (core.New) still fires
	// rather than being masked by a non-nil caching wrapper.
	var nilAgg aggregator.Aggregator
	assert.Nil(t, aggregator.NewCachingAggregator(nilAgg, time.Hour))
}

// TestCachingAggregator_InvalidateAll verifies the CacheInvalidator seam
// (#5748): the returned Aggregator satisfies aggregator.CacheInvalidator, and
// calling InvalidateAll purges the entire cache so a subsequent call for the
// SAME identity re-sweeps rather than serving the (still TTL-fresh) cached
// view.
func TestCachingAggregator_InvalidateAll(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)
	mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).
		Return(&aggregator.AggregatedCapabilities{}, nil).Times(2)

	c := aggregator.NewCachingAggregator(mock, time.Hour)
	invalidator, ok := c.(aggregator.CacheInvalidator)
	require.True(t, ok, "cachingAggregator must implement CacheInvalidator")

	ctx := ctxWithSubject("alice")
	_, err := c.AggregateCapabilities(ctx, testBackends)
	require.NoError(t, err)

	invalidator.InvalidateAll()

	// Well within the TTL, so a hit would prove the purge did not happen; the
	// mock's Times(2) expectation is the actual re-sweep assertion.
	_, err = c.AggregateCapabilities(ctx, testBackends)
	require.NoError(t, err, "the second call after InvalidateAll must re-sweep, not hit a stale entry")
}

// TestCachingAggregator_ErrorNotCached: a failed sweep is not cached, so the next call retries
// the wrapped aggregator.
func TestCachingAggregator_ErrorNotCached(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)
	gomock.InOrder(
		mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(nil, errors.New("boom")),
		mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).
			Return(&aggregator.AggregatedCapabilities{}, nil),
	)

	c := aggregator.NewCachingAggregator(mock, time.Hour)
	ctx := ctxWithSubject("alice")

	_, err := c.AggregateCapabilities(ctx, testBackends)
	require.Error(t, err)
	_, err = c.AggregateCapabilities(ctx, testBackends)
	require.NoError(t, err, "the error must not have been cached")
}

// TestCachingAggregator_ConcurrentMissesShareOneSweep: a burst of concurrent misses for the
// same key runs one backend sweep, and every caller gets that sweep's result.
func TestCachingAggregator_ConcurrentMissesShareOneSweep(t *testing.T) {
	t.Parallel()
	const callers = 16
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)
	caps := &aggregator.AggregatedCapabilities{}
	started := make(chan struct{}, callers)
	release := make(chan struct{})
	mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
			started <- struct{}{}
			<-release
			return caps, nil
		}).Times(1)

	c := aggregator.NewCachingAggregator(mock, time.Hour)
	ctx := ctxWithSubject("alice")

	results := []<-chan aggResult{callAsync(ctx, c)}
	receive(t, started)
	// The leader is now blocked inside the sweep, so these callers miss the cache and join
	// its flight. One that runs only after the release hits the cached entry instead; both
	// paths must leave the wrapped aggregator called exactly once.
	for range callers - 1 {
		results = append(results, callAsync(ctx, c))
	}
	close(release)

	for _, ch := range results {
		r := receive(t, ch)
		require.NoError(t, r.err)
		assert.Same(t, caps, r.caps)
	}
}

// TestCachingAggregator_DifferentKeysDoNotShareSweep: a caller with a different subject
// runs its own sweep even while another subject's sweep is in flight.
func TestCachingAggregator_DifferentKeysDoNotShareSweep(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)
	started := make(chan string, 2)
	release := make(chan struct{})
	mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, _ []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
			id, _ := auth.IdentityFromContext(ctx)
			started <- id.Subject
			<-release
			return &aggregator.AggregatedCapabilities{}, nil
		}).Times(2)

	c := aggregator.NewCachingAggregator(mock, time.Hour)

	alice := callAsync(ctxWithSubject("alice"), c)
	assert.Equal(t, "alice", receive(t, started))
	bob := callAsync(ctxWithSubject("bob"), c)
	assert.Equal(t, "bob", receive(t, started),
		"bob must sweep while alice's sweep is still blocked, not join it")
	close(release)

	ar := receive(t, alice)
	br := receive(t, bob)
	require.NoError(t, ar.err)
	require.NoError(t, br.err)
	assert.NotSame(t, ar.caps, br.caps)
}

// TestCachingAggregator_CancelledWaiterReturnsPromptly: a caller whose own context is
// cancelled returns ctx.Err() without waiting for the shared sweep it joined.
func TestCachingAggregator_CancelledWaiterReturnsPromptly(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)
	caps := &aggregator.AggregatedCapabilities{}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
			started <- struct{}{}
			<-release
			return caps, nil
		}).Times(1)

	c := aggregator.NewCachingAggregator(mock, time.Hour)

	leader := callAsync(ctxWithSubject("alice"), c)
	receive(t, started)

	waiterCtx, cancelWaiter := context.WithCancel(ctxWithSubject("alice"))
	t.Cleanup(cancelWaiter)
	waiter := callAsync(waiterCtx, c)
	cancelWaiter()

	wr := receive(t, waiter)
	require.ErrorIs(t, wr.err, context.Canceled)
	select {
	case <-leader:
		t.Fatal("the leader's sweep must still be blocked when the cancelled waiter returns")
	default:
	}

	close(release)
	lr := receive(t, leader)
	require.NoError(t, lr.err)
	assert.Same(t, caps, lr.caps)
}

// TestCachingAggregator_LeaderCancelledWaiterSweepsOnOwnContext: when the leader's context
// is cancelled mid-sweep, a waiter with a live context gets the full result from its own
// sweep instead of the leader's outcome, and only that full result is cached. This holds
// whether the leader's sweep fails with context.Canceled or succeeds with a partial view
// (DefaultAggregator skips backends that fail, cancellation included).
func TestCachingAggregator_LeaderCancelledWaiterSweepsOnOwnContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		leaderResult func(ctx context.Context) (*aggregator.AggregatedCapabilities, error)
	}{
		{
			name: "leader sweep fails with the cancellation",
			leaderResult: func(ctx context.Context) (*aggregator.AggregatedCapabilities, error) {
				return nil, fmt.Errorf("query backends: %w", ctx.Err())
			},
		},
		{
			name: "leader sweep fails as if no backend answered",
			leaderResult: func(context.Context) (*aggregator.AggregatedCapabilities, error) {
				return nil, fmt.Errorf("query backends: %w", aggregator.ErrNoBackendCapabilities)
			},
		},
		{
			name: "leader sweep succeeds with a partial view",
			leaderResult: func(context.Context) (*aggregator.AggregatedCapabilities, error) {
				return &aggregator.AggregatedCapabilities{}, nil
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			mock := mocks.NewMockAggregator(ctrl)
			full := &aggregator.AggregatedCapabilities{}
			leaderCtx, cancelLeader := context.WithCancel(ctxWithSubject("alice"))
			t.Cleanup(cancelLeader)
			started := make(chan struct{}, 1)
			// Two sweeps: the leader's and the waiter's own. The final call below must hit
			// the cache, so a third sweep fails the test.
			mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).
				DoAndReturn(func(ctx context.Context, _ []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
					if ctx == leaderCtx {
						started <- struct{}{}
						<-ctx.Done()
						return tc.leaderResult(ctx)
					}
					return full, nil
				}).Times(2)

			c := aggregator.NewCachingAggregator(mock, time.Hour)
			ctx := ctxWithSubject("alice")

			leader := callAsync(leaderCtx, c)
			receive(t, started)
			waiter := callAsync(ctx, c)
			cancelLeader()

			lr := receive(t, leader)
			require.ErrorIs(t, lr.err, context.Canceled)
			wr := receive(t, waiter)
			require.NoError(t, wr.err, "the waiter must not inherit the leader's outcome")
			assert.Same(t, full, wr.caps)

			cached, err := c.AggregateCapabilities(ctx, testBackends)
			require.NoError(t, err)
			assert.Same(t, full, cached, "only the waiter's full view may be cached")
		})
	}
}

// TestCachingAggregator_PartialViewAfterLeaderCancelNotCached: a sweep that succeeds after
// the leader's context ended is not cached, because the view may be missing the backends
// that were not reached in time. The waiter's own sweep fails, so it caches nothing either,
// and the cache must be empty once the waiter returns: the waiter only sees the flight's
// result after the flight has finished storing (or not storing) it.
func TestCachingAggregator_PartialViewAfterLeaderCancelNotCached(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)
	leaderCtx, cancelLeader := context.WithCancel(ctxWithSubject("alice"))
	t.Cleanup(cancelLeader)
	started := make(chan struct{}, 1)
	mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, _ []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
			if ctx == leaderCtx {
				started <- struct{}{}
				<-ctx.Done()
				return &aggregator.AggregatedCapabilities{}, nil
			}
			return nil, errors.New("backend unavailable")
		}).Times(2)

	c := aggregator.NewCachingAggregator(mock, time.Hour)

	leader := callAsync(leaderCtx, c)
	receive(t, started)
	waiter := callAsync(ctxWithSubject("alice"), c)
	cancelLeader()

	lr := receive(t, leader)
	require.ErrorIs(t, lr.err, context.Canceled)
	wr := receive(t, waiter)
	require.ErrorContains(t, wr.err, "backend unavailable",
		"the waiter must sweep on its own context rather than receive the leader's partial view")
	assert.Equal(t, 0, aggregator.CachedEntriesForTest(c),
		"a view built under a cancelled leader context must not be cached")
}

// TestCachingAggregator_PrunesExpiredEntriesOnMiss: a sweep for one key removes other keys'
// expired entries, so a key that is never requested again does not stay resident, while
// unexpired entries survive the prune.
func TestCachingAggregator_PrunesExpiredEntriesOnMiss(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)
	// alice, carol and bob sweep once each; the final carol call must be a cache hit.
	mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
			return &aggregator.AggregatedCapabilities{}, nil
		}).Times(3)

	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	c := aggregator.NewCachingAggregatorWithClockForTest(mock, time.Minute, clock.Now)

	_, err := c.AggregateCapabilities(ctxWithSubject("alice"), testBackends)
	require.NoError(t, err)
	clock.Advance(30 * time.Second)
	carol, err := c.AggregateCapabilities(ctxWithSubject("carol"), testBackends)
	require.NoError(t, err)
	clock.Advance(40 * time.Second) // alice is now 70s old (expired), carol 40s (fresh)
	require.Equal(t, 2, aggregator.CachedEntriesForTest(c), "expiry alone must not remove entries")

	_, err = c.AggregateCapabilities(ctxWithSubject("bob"), testBackends)
	require.NoError(t, err)
	assert.Equal(t, 2, aggregator.CachedEntriesForTest(c), "bob's miss must prune alice's expired entry")

	carolAgain, err := c.AggregateCapabilities(ctxWithSubject("carol"), testBackends)
	require.NoError(t, err)
	assert.Same(t, carol, carolAgain, "carol's unexpired entry must survive the prune")
}

// TestCachingAggregator_InvalidateAllDuringSweep: a sweep in flight when InvalidateAll runs
// returns its result to its caller but does not cache it, and a caller that misses after
// InvalidateAll starts a new sweep rather than joining the pre-invalidation one.
func TestCachingAggregator_InvalidateAllDuringSweep(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)
	var calls atomic.Int32
	started := make(chan struct{}, 2)
	releases := []chan struct{}{make(chan struct{}), make(chan struct{})}
	mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
			n := calls.Add(1)
			started <- struct{}{}
			<-releases[n-1]
			return &aggregator.AggregatedCapabilities{}, nil
		}).Times(2)

	c := aggregator.NewCachingAggregator(mock, time.Hour)
	invalidator, ok := c.(aggregator.CacheInvalidator)
	require.True(t, ok)
	ctx := ctxWithSubject("alice")

	stale := callAsync(ctx, c)
	receive(t, started)
	invalidator.InvalidateAll()

	fresh := callAsync(ctx, c)
	receive(t, started) // a second sweep started while the stale one is still blocked

	close(releases[0])
	sr := receive(t, stale)
	require.NoError(t, sr.err)
	assert.Equal(t, 0, aggregator.CachedEntriesForTest(c),
		"the pre-invalidation sweep must not repopulate the cache")

	close(releases[1])
	fr := receive(t, fresh)
	require.NoError(t, fr.err)
	assert.NotSame(t, sr.caps, fr.caps)

	cached, err := c.AggregateCapabilities(ctx, testBackends)
	require.NoError(t, err)
	assert.Same(t, fr.caps, cached, "only the post-invalidation sweep may be cached")
}

// TestCachingAggregator_PanicReturnsError: the shared sweep runs on singleflight's goroutine,
// where a panic would crash the process, so a panicking aggregator surfaces as an error.
func TestCachingAggregator_PanicReturnsError(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mock := mocks.NewMockAggregator(ctrl)
	mock.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
			panic("boom")
		})

	c := aggregator.NewCachingAggregator(mock, time.Hour)

	_, err := c.AggregateCapabilities(ctxWithSubject("alice"), testBackends)
	require.ErrorContains(t, err, "capability aggregation panicked: boom")
}

type aggResult struct {
	caps *aggregator.AggregatedCapabilities
	err  error
}

// callAsync runs AggregateCapabilities on its own goroutine and delivers the result on the
// returned channel.
func callAsync(ctx context.Context, c aggregator.Aggregator) <-chan aggResult {
	ch := make(chan aggResult, 1)
	go func() {
		caps, err := c.AggregateCapabilities(ctx, testBackends)
		ch <- aggResult{caps: caps, err: err}
	}()
	return ch
}

// receive returns the next value from ch, failing the test if none arrives in time.
func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting on channel")
		var zero T
		return zero
	}
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}
