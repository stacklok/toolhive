// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cache

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth/obo/exchange"
)

// base is a fixed clock origin so TTL arithmetic is fully deterministic.
var base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// fakeClock is a manually-advanced, concurrency-safe time source.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: base} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeExchanger is a configurable exchange.TokenExchanger test double that counts
// calls. fn receives the 1-based call number so tests can vary the result.
type fakeExchanger struct {
	mu    sync.Mutex
	calls int
	fn    func(ctx context.Context, userToken, audience string, call int) (exchange.Token, error)
}

var _ exchange.TokenExchanger = (*fakeExchanger)(nil)

func (f *fakeExchanger) ExchangeToken(ctx context.Context, userToken, audience string) (exchange.Token, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	fn := f.fn
	f.mu.Unlock()
	return fn(ctx, userToken, audience, call)
}

func (f *fakeExchanger) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// testTimeout bounds every blocking wait in these tests so a synchronization
// regression fails fast instead of hanging until the global go test timeout.
const testTimeout = 5 * time.Second

// recv receives one value from ch, failing the test if none arrives within
// testTimeout. Call it only from the test goroutine (it may call t.Fatal).
func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(testTimeout):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

// waitGroupWithTimeout waits for wg, failing the test if it does not finish
// within testTimeout. Call it only from the test goroutine.
func waitGroupWithTimeout(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	recv(t, done, "goroutines to finish")
}

// errBlockTimedOut is returned by a delegate whose test barrier was never
// released; it turns a hung test into a visible failure.
var errBlockTimedOut = errors.New("test barrier was never released")

// awaitRelease blocks a delegate until release is closed, giving up after
// testTimeout. It runs on the cache's detached exchange goroutine, where t.Fatal
// is not allowed, so it reports the timeout as an error instead.
func awaitRelease(release <-chan struct{}) error {
	select {
	case <-release:
		return nil
	case <-time.After(testTimeout):
		return errBlockTimedOut
	}
}

// goroutinesWaitingOnFlight counts goroutines parked in waitFor whose stack
// also names testName. Filtering by the test function keeps parallel tests in
// this package from inflating the count. It observes the scheduler rather than
// production internals, so no test hook is needed in Cache.
func goroutinesWaitingOnFlight(testName string) int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, "cache.waitFor(") && strings.Contains(g, testName) {
			count++
		}
	}
	return count
}

func TestCache_ServesCachedTokenWithinTTL(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	fake := &fakeExchanger{fn: func(_ context.Context, _, _ string, _ int) (exchange.Token, error) {
		return exchange.Token{AccessToken: "tok", Expiry: clock.now().Add(time.Hour)}, nil
	}}
	c := New(fake, WithClock(clock.now))

	first, err := c.Exchange(t.Context(), "user", "aud")
	require.NoError(t, err)
	assert.Equal(t, "tok", first)

	second, err := c.Exchange(t.Context(), "user", "aud")
	require.NoError(t, err)
	assert.Equal(t, "tok", second)

	assert.Equal(t, 1, fake.callCount(), "second call must be served from the cache")
}

// TestCache_FreshnessBoundary pins when a cached token stops being served: it
// is fresh strictly before expiry-skew and stale from that instant on. Each row
// exchanges once at base, advances the clock, and exchanges again.
func TestCache_FreshnessBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		ttl       time.Duration
		opts      []Option
		advance   time.Duration
		wantCalls int
		wantToken string
	}{
		{
			// Default 60s skew on a 100s token: stale from base+40s.
			name:      "default skew, just before the window",
			ttl:       100 * time.Second,
			advance:   39 * time.Second,
			wantCalls: 1,
			wantToken: "tok-1",
		},
		{
			name:      "default skew, exactly at expiry minus skew",
			ttl:       100 * time.Second,
			advance:   40 * time.Second,
			wantCalls: 2,
			wantToken: "tok-2",
		},
		{
			name:      "default skew, inside the window",
			ttl:       100 * time.Second,
			advance:   45 * time.Second,
			wantCalls: 2,
			wantToken: "tok-2",
		},
		{
			name:      "custom skew, exactly at expiry minus skew",
			ttl:       100 * time.Second,
			opts:      []Option{WithSkew(10 * time.Second)},
			advance:   90 * time.Second,
			wantCalls: 2,
			wantToken: "tok-2",
		},
		{
			name:      "custom skew, just before expiry minus skew",
			ttl:       100 * time.Second,
			opts:      []Option{WithSkew(10 * time.Second)},
			advance:   89 * time.Second,
			wantCalls: 1,
			wantToken: "tok-1",
		},
		{
			// A token whose whole lifetime is inside the skew is never served
			// from the cache, even with no time passing.
			name:      "TTL shorter than skew is never reused",
			ttl:       30 * time.Second,
			advance:   0,
			wantCalls: 2,
			wantToken: "tok-2",
		},
		{
			name:      "zero skew serves until expiry",
			ttl:       100 * time.Second,
			opts:      []Option{WithSkew(0)},
			advance:   99 * time.Second,
			wantCalls: 1,
			wantToken: "tok-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clock := newFakeClock()
			fake := &fakeExchanger{fn: func(_ context.Context, _, _ string, call int) (exchange.Token, error) {
				return exchange.Token{AccessToken: fmt.Sprintf("tok-%d", call), Expiry: clock.now().Add(tt.ttl)}, nil
			}}
			c := New(fake, append([]Option{WithClock(clock.now)}, tt.opts...)...)

			v, err := c.Exchange(t.Context(), "user", "aud")
			require.NoError(t, err)
			assert.Equal(t, "tok-1", v)

			clock.advance(tt.advance)
			v, err = c.Exchange(t.Context(), "user", "aud")
			require.NoError(t, err)
			assert.Equal(t, tt.wantToken, v)
			assert.Equal(t, tt.wantCalls, fake.callCount())
		})
	}
}

// TestWithClock_NilKeepsWallClock verifies a nil clock is ignored rather than
// installed (which would panic on the first lookup), and that the retained
// wall clock still drives caching.
func TestWithClock_NilKeepsWallClock(t *testing.T) {
	t.Parallel()

	fake := &fakeExchanger{fn: func(_ context.Context, _, _ string, _ int) (exchange.Token, error) {
		return exchange.Token{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)}, nil
	}}
	c := New(fake, WithClock(nil))
	require.NotNil(t, c.now)

	for range 2 {
		v, err := c.Exchange(t.Context(), "user", "aud")
		require.NoError(t, err)
		assert.Equal(t, "tok", v)
	}
	assert.Equal(t, 1, fake.callCount(), "the second call must be served from the cache")
}

func TestCache_PartitionsByUserToken(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	fake := &fakeExchanger{fn: func(_ context.Context, userToken, _ string, _ int) (exchange.Token, error) {
		return exchange.Token{AccessToken: "tok:" + userToken, Expiry: clock.now().Add(time.Hour)}, nil
	}}
	c := New(fake, WithClock(clock.now))

	a, err := c.Exchange(t.Context(), "userAlice", "aud")
	require.NoError(t, err)
	assert.Equal(t, "tok:userAlice", a)

	b, err := c.Exchange(t.Context(), "userBob", "aud")
	require.NoError(t, err)
	assert.Equal(t, "tok:userBob", b)
	assert.Equal(t, 2, fake.callCount(), "different user tokens must not share a cache entry")

	// Each user still reads its own cached token; no cross-contamination.
	a2, err := c.Exchange(t.Context(), "userAlice", "aud")
	require.NoError(t, err)
	assert.Equal(t, "tok:userAlice", a2)
	b2, err := c.Exchange(t.Context(), "userBob", "aud")
	require.NoError(t, err)
	assert.Equal(t, "tok:userBob", b2)
	assert.Equal(t, 2, fake.callCount())
}

func TestCache_PartitionsByAudience(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	fake := &fakeExchanger{fn: func(_ context.Context, _, audience string, _ int) (exchange.Token, error) {
		return exchange.Token{AccessToken: "tok:" + audience, Expiry: clock.now().Add(time.Hour)}, nil
	}}
	c := New(fake, WithClock(clock.now))

	v1, err := c.Exchange(t.Context(), "user", "aud-1")
	require.NoError(t, err)
	assert.Equal(t, "tok:aud-1", v1)

	v2, err := c.Exchange(t.Context(), "user", "aud-2")
	require.NoError(t, err)
	assert.Equal(t, "tok:aud-2", v2)
	assert.Equal(t, 2, fake.callCount(), "different audiences must not share a cache entry")
}

func TestCache_DoesNotCacheTokenWithoutExpiry(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	fake := &fakeExchanger{fn: func(_ context.Context, _, _ string, call int) (exchange.Token, error) {
		return exchange.Token{AccessToken: fmt.Sprintf("tok-%d", call)}, nil // zero Expiry
	}}
	c := New(fake, WithClock(clock.now))

	for i := 1; i <= 3; i++ {
		v, err := c.Exchange(t.Context(), "user", "aud")
		require.NoError(t, err)
		assert.Equal(t, fmt.Sprintf("tok-%d", i), v)
	}
	assert.Equal(t, 3, fake.callCount(), "a token with no expiry is never cached")
}

func TestCache_PropagatesDelegateError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("delegate boom")
	clock := newFakeClock()
	fake := &fakeExchanger{fn: func(_ context.Context, _, _ string, _ int) (exchange.Token, error) {
		return exchange.Token{}, sentinel
	}}
	c := New(fake, WithClock(clock.now))

	v, err := c.Exchange(t.Context(), "user", "aud")
	require.ErrorIs(t, err, sentinel)
	assert.Empty(t, v)

	// A failed exchange caches nothing, so the next call delegates again.
	_, _ = c.Exchange(t.Context(), "user", "aud")
	assert.Equal(t, 2, fake.callCount())
}

func TestCache_ConcurrentExchangesAreRaceFree(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	fake := &fakeExchanger{fn: func(_ context.Context, userToken, audience string, _ int) (exchange.Token, error) {
		return exchange.Token{
			AccessToken: "tok:" + userToken + ":" + audience,
			Expiry:      clock.now().Add(time.Hour),
		}, nil
	}}
	c := New(fake, WithClock(clock.now))

	ctx := t.Context()
	const goroutines = 64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			user := fmt.Sprintf("user-%d", i%4)
			aud := fmt.Sprintf("aud-%d", i%3)
			got, err := c.Exchange(ctx, user, aud)
			assert.NoError(t, err)
			assert.Equal(t, "tok:"+user+":"+aud, got)
		}(i)
	}
	waitGroupWithTimeout(t, &wg)
}

// TestCache_StarterCancellationDoesNotPoisonOthers verifies the detached-exchange
// semantics: cancelling whichever caller happened to start the flight releases
// that caller promptly with its own error, but the shared exchange runs to
// completion (it never observes the cancellation) so the other coalesced callers
// still get a valid token.
func TestCache_StarterCancellationDoesNotPoisonOthers(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	entered := make(chan struct{}, 2)
	proceed := make(chan struct{})
	fake := &fakeExchanger{fn: func(ctx context.Context, _, _ string, _ int) (exchange.Token, error) {
		entered <- struct{}{}
		if err := awaitRelease(proceed); err != nil {
			return exchange.Token{}, err
		}
		// The shared exchange must not inherit the starter's cancellation.
		if err := ctx.Err(); err != nil {
			return exchange.Token{}, err
		}
		return exchange.Token{AccessToken: "shared", Expiry: clock.now().Add(time.Hour)}, nil
	}}
	c := New(fake, WithClock(clock.now))

	// The starter holds a cancellable context.
	starterCtx, cancelStarter := context.WithCancel(t.Context())
	starterErr := make(chan error, 1)
	go func() {
		_, err := c.Exchange(starterCtx, "user", "aud")
		starterErr <- err
	}()
	recv(t, entered, "the detached exchange to begin")

	// A second caller with a healthy context coalesces onto the same exchange.
	followerTok := make(chan string, 1)
	go func() {
		v, err := c.Exchange(t.Context(), "user", "aud")
		assert.NoError(t, err)
		followerTok <- v
	}()

	// Cancelling the starter releases it promptly with its own error...
	cancelStarter()
	assert.ErrorIs(t, recv(t, starterErr, "the starter to return"), context.Canceled)

	// ...while the shared exchange completes and the follower still succeeds.
	close(proceed)
	assert.Equal(t, "shared", recv(t, followerTok, "the follower's token"))
	assert.Equal(t, 1, fake.callCount(), "the cancellation must not trigger a second exchange")
}

// TestCache_CoalescesConcurrentMissesForSameKey proves followers join the
// leader's flight rather than racing past it: the delegate is held open until
// every caller is provably parked in waitFor, and only then released. Were the
// in-flight map removed, each parked caller would own a separate flight and the
// delegate would run once per caller.
func TestCache_CoalescesConcurrentMissesForSameKey(t *testing.T) {
	t.Parallel()

	const callers = 17
	clock := newFakeClock()
	release := make(chan struct{})
	var calls atomic.Int32

	fake := &fakeExchanger{fn: func(_ context.Context, _, _ string, _ int) (exchange.Token, error) {
		calls.Add(1)
		// Hold the exchange open while the other callers pile up.
		if err := awaitRelease(release); err != nil {
			return exchange.Token{}, err
		}
		return exchange.Token{AccessToken: "shared", Expiry: clock.now().Add(time.Hour)}, nil
	}}
	c := New(fake, WithClock(clock.now))

	ctx := t.Context()
	results := make(chan string, callers)
	for range callers {
		go func() {
			v, err := c.Exchange(ctx, "user", "aud")
			assert.NoError(t, err)
			results <- v
		}()
	}

	// Every caller is parked on a flight before any flight can finish, so none
	// of them can be served from a cache entry the leader wrote.
	require.Eventually(t, func() bool {
		return goroutinesWaitingOnFlight(t.Name()) == callers
	}, testTimeout, 5*time.Millisecond, "all callers must be waiting on an in-flight exchange")

	close(release)

	for range callers {
		assert.Equal(t, "shared", recv(t, results, "a caller's result"))
	}
	assert.Equal(t, int32(1), calls.Load(),
		"concurrent misses for one key must collapse to a single underlying exchange")
}

// TestCache_CoalescedErrorReachesEveryCaller verifies a failed shared exchange
// hands its error to every coalesced caller and caches nothing.
func TestCache_CoalescedErrorReachesEveryCaller(t *testing.T) {
	t.Parallel()

	const callers = 8
	sentinel := errors.New("delegate boom")
	release := make(chan struct{})
	fake := &fakeExchanger{fn: func(_ context.Context, _, _ string, _ int) (exchange.Token, error) {
		if err := awaitRelease(release); err != nil {
			return exchange.Token{}, err
		}
		return exchange.Token{}, sentinel
	}}
	c := New(fake)

	ctx := t.Context()
	errs := make(chan error, callers)
	for range callers {
		go func() {
			_, err := c.Exchange(ctx, "user", "aud")
			errs <- err
		}()
	}

	require.Eventually(t, func() bool {
		return goroutinesWaitingOnFlight(t.Name()) == callers
	}, testTimeout, 5*time.Millisecond, "all callers must be waiting on the in-flight exchange")

	close(release)

	for range callers {
		assert.ErrorIs(t, recv(t, errs, "a caller's error"), sentinel)
	}
	assert.Equal(t, 1, fake.callCount(), "the failed exchange is shared, not repeated per caller")

	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Empty(t, c.entries, "a failed exchange caches nothing")
	assert.Empty(t, c.inflight, "the in-flight slot is freed after a failure")
}

// TestCache_JoinsExistingInflight deterministically exercises the single-flight
// join path: a caller that finds an in-flight exchange for its key waits for and
// shares that result instead of calling the delegate. It seeds the unexported
// in-flight map so the timing is not scheduling-dependent.
func TestCache_JoinsExistingInflight(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	fake := &fakeExchanger{fn: func(_ context.Context, _, _ string, _ int) (exchange.Token, error) {
		return exchange.Token{}, errors.New("delegate must not be called when joining an in-flight exchange")
	}}
	c := New(fake, WithClock(clock.now))

	done := make(chan struct{})
	leader := &call{done: done, token: exchange.Token{AccessToken: "shared", Expiry: clock.now().Add(time.Hour)}}
	key := cacheKey("user", "aud")
	c.mu.Lock()
	c.inflight[key] = leader
	c.mu.Unlock()
	close(done) // the in-flight exchange has already completed

	got, err := c.Exchange(t.Context(), "user", "aud")
	require.NoError(t, err)
	assert.Equal(t, "shared", got)
	assert.Equal(t, 0, fake.callCount())
}

// TestCache_FollowerRespectsContextCancellation verifies that a caller waiting on
// another goroutine's in-flight exchange is released by its own context, rather
// than being held until the (here, never-completing) leader finishes.
func TestCache_FollowerRespectsContextCancellation(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	fake := &fakeExchanger{fn: func(_ context.Context, _, _ string, _ int) (exchange.Token, error) {
		return exchange.Token{}, errors.New("delegate must not be called by a follower")
	}}
	c := New(fake, WithClock(clock.now))

	key := cacheKey("user", "aud")
	c.mu.Lock()
	c.inflight[key] = &call{done: make(chan struct{})} // never completes
	c.mu.Unlock()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := c.Exchange(ctx, "user", "aud")
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 0, fake.callCount())
}

// TestCache_PanickingDelegateFreesKey verifies the single-flight cleanup is
// panic-safe: because the exchange runs on a detached goroutine, a panicking
// delegate is recovered and surfaced to the caller as an error (rather than
// crashing the process), and the in-flight slot is freed so the key is not
// wedged for later callers.
func TestCache_PanickingDelegateFreesKey(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	var shouldPanic atomic.Bool
	shouldPanic.Store(true)
	fake := &fakeExchanger{fn: func(_ context.Context, _, _ string, _ int) (exchange.Token, error) {
		if shouldPanic.Load() {
			panic("delegate boom")
		}
		return exchange.Token{AccessToken: "after-panic", Expiry: clock.now().Add(time.Hour)}, nil
	}}
	c := New(fake, WithClock(clock.now))

	_, err := c.Exchange(t.Context(), "user", "aud")
	assert.ErrorIs(t, err, errExchangePanicked, "a panicking delegate must surface as an error, not crash")

	c.mu.Lock()
	_, wedged := c.inflight[cacheKey("user", "aud")]
	c.mu.Unlock()
	assert.False(t, wedged, "the in-flight slot must be freed after a panic")

	// The cache is not wedged: a later call for the same key proceeds normally.
	shouldPanic.Store(false)
	v, err := c.Exchange(t.Context(), "user", "aud")
	require.NoError(t, err)
	assert.Equal(t, "after-panic", v)
}

func TestNew_PanicsOnNilDelegate(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() { New(nil) })
}

func TestNew_PanicsOnNegativeSkew(t *testing.T) {
	t.Parallel()
	fake := &fakeExchanger{fn: func(_ context.Context, _, _ string, _ int) (exchange.Token, error) {
		return exchange.Token{}, nil
	}}
	assert.Panics(t, func() { New(fake, WithSkew(-time.Second)) })
}

// TestCache_SweepEvictsExpiredColdEntries verifies the bounded-memory invariant:
// inserting a fresh entry sweeps entries whose expiry has already passed, so the
// map does not accumulate stale sessions. It reaches into unexported state
// because the eviction is an internal memory-management property, not part of
// the public contract.
func TestCache_SweepEvictsExpiredColdEntries(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	fake := &fakeExchanger{fn: func(_ context.Context, _, _ string, _ int) (exchange.Token, error) {
		return exchange.Token{AccessToken: "t", Expiry: clock.now().Add(10 * time.Second)}, nil
	}}
	c := New(fake, WithClock(clock.now), WithSkew(0))

	_, err := c.Exchange(t.Context(), "cold", "aud") // expiry = base+10s
	require.NoError(t, err)

	clock.advance(20 * time.Second) // now base+20s — the cold entry has expired

	_, err = c.Exchange(t.Context(), "hot", "aud") // store() sweeps the cold entry
	require.NoError(t, err)

	c.mu.Lock()
	defer c.mu.Unlock()
	_, coldPresent := c.entries[cacheKey("cold", "aud")]
	_, hotPresent := c.entries[cacheKey("hot", "aud")]
	assert.False(t, coldPresent, "expired cold entry must be swept on insert")
	assert.True(t, hotPresent, "freshly inserted entry must remain")
	assert.Len(t, c.entries, 1)
}
