// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cache

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

func TestCache_ReExchangesAfterExpiry(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	const ttl = 100 * time.Second
	fake := &fakeExchanger{fn: func(_ context.Context, _, _ string, call int) (exchange.Token, error) {
		return exchange.Token{
			AccessToken: fmt.Sprintf("tok-%d", call),
			Expiry:      clock.now().Add(ttl),
		}, nil
	}}
	c := New(fake, WithClock(clock.now)) // default 60s skew → stale at base+40s

	v, err := c.Exchange(t.Context(), "user", "aud")
	require.NoError(t, err)
	assert.Equal(t, "tok-1", v)
	assert.Equal(t, 1, fake.callCount())

	// Still fresh just before the skew window opens.
	clock.advance(39 * time.Second) // now base+39s, threshold base+40s
	v, err = c.Exchange(t.Context(), "user", "aud")
	require.NoError(t, err)
	assert.Equal(t, "tok-1", v)
	assert.Equal(t, 1, fake.callCount())

	// Inside the skew window the token is treated as stale and re-exchanged.
	clock.advance(6 * time.Second) // now base+45s, past base+40s
	v, err = c.Exchange(t.Context(), "user", "aud")
	require.NoError(t, err)
	assert.Equal(t, "tok-2", v)
	assert.Equal(t, 2, fake.callCount())
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
	wg.Wait()
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
		<-proceed
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
	<-entered // the detached exchange has begun

	// A second caller with a healthy context coalesces onto the same exchange.
	followerTok := make(chan string, 1)
	go func() {
		v, err := c.Exchange(t.Context(), "user", "aud")
		assert.NoError(t, err)
		followerTok <- v
	}()

	// Cancelling the starter releases it promptly with its own error...
	cancelStarter()
	assert.ErrorIs(t, <-starterErr, context.Canceled)

	// ...while the shared exchange completes and the follower still succeeds.
	close(proceed)
	assert.Equal(t, "shared", <-followerTok)
	assert.Equal(t, 1, fake.callCount(), "the cancellation must not trigger a second exchange")
}

func TestCache_CoalescesConcurrentMissesForSameKey(t *testing.T) {
	t.Parallel()

	const followers = 16
	clock := newFakeClock()
	// Buffered past the expected count so a (broken) extra exchange cannot
	// deadlock on the signal — it would surface as a failed call-count assert.
	entered := make(chan struct{}, followers+1)
	release := make(chan struct{})
	var calls atomic.Int32

	fake := &fakeExchanger{fn: func(_ context.Context, _, _ string, _ int) (exchange.Token, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-release // hold the in-flight exchange open while followers pile up
		return exchange.Token{AccessToken: "shared", Expiry: clock.now().Add(time.Hour)}, nil
	}}
	c := New(fake, WithClock(clock.now))

	ctx := t.Context()
	results := make(chan string, followers+1)
	doExchange := func() {
		v, err := c.Exchange(ctx, "user", "aud")
		assert.NoError(t, err)
		results <- v
	}

	go doExchange() // leader
	<-entered       // leader is registered in-flight and inside the delegate

	for i := 0; i < followers; i++ {
		go doExchange() // each joins the leader's in-flight exchange
	}

	close(release) // let the single exchange complete; everyone shares its result

	for i := 0; i < followers+1; i++ {
		assert.Equal(t, "shared", <-results)
	}
	assert.Equal(t, int32(1), calls.Load(),
		"concurrent misses for one key must collapse to a single underlying exchange")
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
