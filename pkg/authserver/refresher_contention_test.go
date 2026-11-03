// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// blockingProvider parks inside RefreshTokens until the test releases it, so a
// second replica can be observed while the first is mid-redemption. It counts
// its calls, which is the assertion this whole test exists for: one upstream
// redemption, however many replicas wanted one.
//
// Only RefreshTokens is reachable here, so the rest of the interface is left
// embedded and nil — a call to any other method would panic rather than pass
// silently.
type blockingProvider struct {
	upstream.OAuth2Provider

	calls atomic.Int32

	// entered is closed when the first call reaches the provider.
	entered chan struct{}
	// release is closed to let a parked call finish, by the test on its happy
	// path and by releaseAll on teardown.
	release chan struct{}

	enteredOnce sync.Once
	releaseOnce sync.Once
}

// releaseAll unparks every call still waiting. It is idempotent so the test's
// own release and the teardown release cannot double-close.
func (p *blockingProvider) releaseAll() {
	p.releaseOnce.Do(func() { close(p.release) })
}

func (p *blockingProvider) RefreshTokens(ctx context.Context, _, _ string) (*upstream.Tokens, error) {
	p.calls.Add(1)
	p.enteredOnce.Do(func() { close(p.entered) })
	// ctx is a backstop only. RefreshAndStore detaches its refresh context
	// (context.WithoutCancel), so cancelling the test's context does not reach
	// here; what actually unparks this call when the test fails early is the
	// teardown releaseAll below.
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &upstream.Tokens{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
	}, nil
}

// refusalSignallingLocker reports, on a channel, that an acquisition was
// refused because another holder has the lease. It makes the "the waiter did
// not call the provider" assertion deterministic: the test can wait for the
// refusal instead of sleeping and hoping the waiter got far enough to
// misbehave.
type refusalSignallingLocker struct {
	storage.UpstreamRefreshLocker
	refused chan struct{}
}

func (l *refusalSignallingLocker) AcquireUpstreamRefreshLock(
	ctx context.Context, rowID storage.UpstreamTokenRowID, ttl time.Duration,
) (storage.UpstreamRefreshLease, error) {
	lease, err := l.UpstreamRefreshLocker.AcquireUpstreamRefreshLock(ctx, rowID, ttl)
	if errors.Is(err, storage.ErrUpstreamRefreshLockHeld) {
		select {
		case l.refused <- struct{}{}:
		default:
		}
	}
	return lease, err
}

// TestRefreshAndStore_TwoReplicasRedeemOnce is the end-to-end claim of #4122:
// two auth server processes sharing one Redis, each with its own singleflight
// group, concurrently find the same expired upstream row, and the upstream
// provider is asked to redeem the refresh token exactly once.
//
// Everything below the refresher is real — Redis (miniredis) storage, the
// Lua-scripted lease and CAS write, the row encoding — because the failure
// this guards against lives in the interaction between those layers, not in
// any one of them. The only test doubles are the provider (which has to park
// mid-redemption for the race to be observable at all) and a passthrough
// wrapper that reports lease refusals.
func TestRefreshAndStore_TwoReplicasRedeemOnce(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	stor := storage.NewRedisStorageWithClient(client, "test:auth:")
	t.Cleanup(func() {
		_ = stor.Close()
		mr.Close()
	})

	ctx := t.Context()
	expired := &storage.UpstreamTokens{
		ProviderID:       "github",
		AccessToken:      "old-access",
		RefreshToken:     "old-refresh",
		UpstreamSubject:  "subject",
		ExpiresAt:        time.Now().Add(-time.Hour),
		SessionExpiresAt: time.Now().Add(24 * time.Hour),
	}
	require.NoError(t, stor.StoreUpstreamTokens(ctx, "session", "github", expired))

	locker := storage.UpstreamRefreshLockerFrom(stor)
	require.NotNil(t, locker, "the Redis backend must provide the lease capability")

	provider := &blockingProvider{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	refused := make(chan struct{}, 1)

	// Two refreshers, as two processes would have: separate singleflight
	// groups (the zero value of the field), one shared storage backend. If
	// coordination lived only in singleflight, both would redeem.
	leader := newContendingRefresher(provider, stor, locker)
	waiter := newContendingRefresher(provider, stor,
		&refusalSignallingLocker{UpstreamRefreshLocker: locker, refused: refused})

	var (
		leaderRow, waiterRow *storage.UpstreamTokens
		leaderErr, waiterErr error
		wg                   sync.WaitGroup
	)

	// Registered after the storage cleanup, so it runs BEFORE it (cleanups are
	// LIFO): any goroutine still parked in the provider is unparked and
	// drained while Redis is still up. Without this, a t.Fatal before the
	// release below would leave a refresher blocked for the rest of the run.
	t.Cleanup(func() {
		provider.releaseAll()
		wg.Wait()
	})

	wg.Add(1)
	go func() {
		defer wg.Done()
		leaderRow, leaderErr = leader.RefreshAndStore(ctx, "session", expired)
	}()

	// The leader is now inside the provider, holding the lease.
	select {
	case <-provider.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the first refresher never reached the upstream provider")
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		waiterRow, waiterErr = waiter.RefreshAndStore(ctx, "session", expired)
	}()

	// The second refresher has tried for the lease and been refused. The
	// leader still holds it (it is parked in the provider until released
	// below), so every further attempt is refused too: from here until the
	// release, the waiter cannot legitimately reach the provider.
	select {
	case <-refused:
	case <-time.After(10 * time.Second):
		// Getting here means the second replica never asked for the lease at
		// all. The interesting number is then how many redemptions the
		// provider has seen: uncoordinated, the waiter walks straight past it
		// into a second redemption of the same refresh token, which is the
		// regression this test exists to catch.
		t.Fatalf("the second refresher never contended for the lease; upstream redemptions so far: %d",
			provider.calls.Load())
	}
	assert.Equal(t, int32(1), provider.calls.Load(),
		"the waiter must not redeem the refresh token while another replica holds the lease")

	provider.releaseAll()
	wg.Wait()

	require.NoError(t, leaderErr)
	require.NoError(t, waiterErr)
	assert.Equal(t, int32(1), provider.calls.Load(),
		"the refresh token must be redeemed upstream exactly once across both replicas")

	// Both callers end up with the leader's rotated tokens: the waiter either
	// re-read the stored row, or took the freed lease and found on its own
	// re-read that no refresh was needed any more.
	require.NotNil(t, leaderRow)
	require.NotNil(t, waiterRow)
	assert.Equal(t, "new-refresh", leaderRow.RefreshToken)
	assert.Equal(t, "new-refresh", waiterRow.RefreshToken)
	assert.Equal(t, "new-access", waiterRow.AccessToken)

	stored, err := stor.GetUpstreamTokens(ctx, "session", "github")
	require.NoError(t, err)
	assert.Equal(t, "new-refresh", stored.RefreshToken, "the rotated row must be what is persisted")
}

// newContendingRefresher builds one replica's refresher. Each call produces an
// independent singleflight group, which is what makes two of them stand in for
// two processes.
func newContendingRefresher(
	provider upstream.OAuth2Provider,
	stor storage.UpstreamTokenStorage,
	locker storage.UpstreamRefreshLocker,
) *upstreamTokenRefresher {
	return &upstreamTokenRefresher{
		providers:            map[string]upstream.OAuth2Provider{"github": provider},
		storage:              stor,
		locker:               locker,
		refreshTokenLifespan: 24 * time.Hour,
	}
}
