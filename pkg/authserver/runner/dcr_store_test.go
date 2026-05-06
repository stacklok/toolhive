// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInMemoryDCRResolutionCache_PutGet_RoundTrip(t *testing.T) {
	t.Parallel()

	store := newInMemoryDCRResolutionCache()
	ctx := context.Background()

	key := DCRKey{
		Issuer:      "https://idp.example.com",
		RedirectURI: "https://toolhive.example.com/oauth/callback",
		ScopesHash:  scopesHash([]string{"openid", "profile"}),
	}
	resolution := &DCRResolution{
		ClientID:                "client-abc",
		ClientSecret:            "secret-xyz",
		AuthorizationEndpoint:   "https://idp.example.com/authorize",
		TokenEndpoint:           "https://idp.example.com/token",
		RegistrationAccessToken: "reg-tok",
		RegistrationClientURI:   "https://idp.example.com/register/client-abc",
		TokenEndpointAuthMethod: "client_secret_basic",
		CreatedAt:               time.Now(),
	}

	require.NoError(t, store.Put(ctx, key, resolution))

	got, ok, err := store.Get(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, resolution.ClientID, got.ClientID)
	assert.Equal(t, resolution.ClientSecret, got.ClientSecret)
	assert.Equal(t, resolution.AuthorizationEndpoint, got.AuthorizationEndpoint)
	assert.Equal(t, resolution.TokenEndpoint, got.TokenEndpoint)
	assert.Equal(t, resolution.RegistrationAccessToken, got.RegistrationAccessToken)
	assert.Equal(t, resolution.RegistrationClientURI, got.RegistrationClientURI)
	assert.Equal(t, resolution.TokenEndpointAuthMethod, got.TokenEndpointAuthMethod)
}

func TestInMemoryDCRResolutionCache_Get_MissingKey(t *testing.T) {
	t.Parallel()

	store := newInMemoryDCRResolutionCache()
	ctx := context.Background()

	got, ok, err := store.Get(ctx, DCRKey{Issuer: "https://unknown.example.com"})
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestInMemoryDCRResolutionCache_DistinctKeysDoNotCollide(t *testing.T) {
	t.Parallel()

	store := newInMemoryDCRResolutionCache()
	ctx := context.Background()

	keyA := DCRKey{
		Issuer:      "https://idp-a.example.com",
		RedirectURI: "https://toolhive.example.com/oauth/callback",
		ScopesHash:  scopesHash([]string{"openid"}),
	}
	keyB := DCRKey{
		Issuer:      "https://idp-b.example.com",
		RedirectURI: "https://toolhive.example.com/oauth/callback",
		ScopesHash:  scopesHash([]string{"openid"}),
	}
	keyC := DCRKey{
		Issuer:      "https://idp-a.example.com",
		RedirectURI: "https://other.example.com/callback",
		ScopesHash:  scopesHash([]string{"openid"}),
	}
	keyD := DCRKey{
		Issuer:      "https://idp-a.example.com",
		RedirectURI: "https://toolhive.example.com/oauth/callback",
		ScopesHash:  scopesHash([]string{"openid", "email"}),
	}

	require.NoError(t, store.Put(ctx, keyA, &DCRResolution{ClientID: "a"}))
	require.NoError(t, store.Put(ctx, keyB, &DCRResolution{ClientID: "b"}))
	require.NoError(t, store.Put(ctx, keyC, &DCRResolution{ClientID: "c"}))
	require.NoError(t, store.Put(ctx, keyD, &DCRResolution{ClientID: "d"}))

	for _, tc := range []struct {
		key      DCRKey
		expected string
	}{
		{keyA, "a"},
		{keyB, "b"},
		{keyC, "c"},
		{keyD, "d"},
	} {
		got, ok, err := store.Get(ctx, tc.key)
		require.NoError(t, err)
		require.True(t, ok, "key %+v should be present", tc.key)
		assert.Equal(t, tc.expected, got.ClientID)
	}
}

func TestInMemoryDCRResolutionCache_Put_OverwritesExisting(t *testing.T) {
	t.Parallel()

	store := newInMemoryDCRResolutionCache()
	ctx := context.Background()

	key := DCRKey{Issuer: "https://idp.example.com", RedirectURI: "https://x.example.com/cb"}
	require.NoError(t, store.Put(ctx, key, &DCRResolution{ClientID: "first"}))
	require.NoError(t, store.Put(ctx, key, &DCRResolution{ClientID: "second"}))

	got, ok, err := store.Get(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "second", got.ClientID)
}

// TestInMemoryDCRResolutionCache_Put_RejectsNilResolution pins the
// fail-loud-on-invalid-input contract: passing nil must error rather than
// silently no-op. A silent no-op would leave the caller with a successful
// Put followed by a Get miss and no debug trail to explain it.
func TestInMemoryDCRResolutionCache_Put_RejectsNilResolution(t *testing.T) {
	t.Parallel()

	store := newInMemoryDCRResolutionCache()
	ctx := context.Background()
	key := DCRKey{Issuer: "https://idp.example.com", RedirectURI: "https://x.example.com/cb"}

	err := store.Put(ctx, key, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be nil")

	// And confirm the rejection did not partially populate the store.
	_, ok, getErr := store.Get(ctx, key)
	require.NoError(t, getErr)
	assert.False(t, ok, "rejected Put must not leave any entry behind")
}

func TestInMemoryDCRResolutionCache_GetReturnsDefensiveCopy(t *testing.T) {
	t.Parallel()

	store := newInMemoryDCRResolutionCache()
	ctx := context.Background()

	key := DCRKey{Issuer: "https://idp.example.com"}
	require.NoError(t, store.Put(ctx, key, &DCRResolution{ClientID: "orig"}))

	got, ok, err := store.Get(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	got.ClientID = "mutated"

	refetched, ok, err := store.Get(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "orig", refetched.ClientID)
}

// Tests for the canonical scopes-hash form live next to the canonical
// implementation in pkg/authserver/storage/memory_test.go (TestScopesHash_*).
// The runner-package binding `scopesHash = storage.ScopesHash` would only
// re-exercise the same code, so duplicating the suite here would be redundant
// per .claude/rules/testing.md.

// TestInMemoryDCRResolutionCache_ConcurrentAccess fans out N goroutines
// performing alternating Put / Get against overlapping and disjoint keys,
// exercising the sync.RWMutex guard advertised in the dcrResolutionCache
// interface doc. With go test -race this catches any future change that
// drops the lock or introduces a data race in the map access.
//
// The test is bounded by a fail-fast deadline so a regression that
// deadlocks fails loudly with a clear message rather than hanging until
// the global Go test timeout.
func TestInMemoryDCRResolutionCache_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	store := newInMemoryDCRResolutionCache()

	const (
		workers      = 16
		opsPerWorker = 200
	)

	// Two key spaces: overlapping (every worker writes the same keys, so the
	// lock must serialise their writes) and disjoint (each worker has its own
	// key space, so reads never see another worker's writes).
	overlappingKey := func(i int) DCRKey {
		return DCRKey{
			Issuer:      "https://idp.example.com",
			RedirectURI: "https://thv.example.com/oauth/callback",
			ScopesHash:  fmt.Sprintf("overlap-%d", i%4),
		}
	}
	disjointKey := func(worker, i int) DCRKey {
		return DCRKey{
			Issuer:      fmt.Sprintf("https://idp-%d.example.com", worker),
			RedirectURI: "https://thv.example.com/oauth/callback",
			ScopesHash:  fmt.Sprintf("disjoint-%d", i),
		}
	}

	var errCount int32
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(worker int) {
			defer wg.Done()
			ctx := context.Background()
			for i := 0; i < opsPerWorker; i++ {
				resolution := &DCRResolution{
					ClientID:  fmt.Sprintf("worker-%d-op-%d", worker, i),
					CreatedAt: time.Now(),
				}
				if i%2 == 0 {
					if err := store.Put(ctx, overlappingKey(i), resolution); err != nil {
						atomic.AddInt32(&errCount, 1)
					}
					if _, _, err := store.Get(ctx, overlappingKey(i)); err != nil {
						atomic.AddInt32(&errCount, 1)
					}
				} else {
					if err := store.Put(ctx, disjointKey(worker, i), resolution); err != nil {
						atomic.AddInt32(&errCount, 1)
					}
					if _, _, err := store.Get(ctx, disjointKey(worker, i)); err != nil {
						atomic.AddInt32(&errCount, 1)
					}
				}
			}
		}(w)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for concurrent store operations to finish; possible deadlock")
	}

	assert.Zero(t, atomic.LoadInt32(&errCount),
		"no Get/Put should have errored under concurrent access")
}
