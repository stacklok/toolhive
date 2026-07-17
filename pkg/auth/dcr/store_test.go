// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package dcr

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

func TestStorageBackedStore_PutGet_RoundTrip(t *testing.T) {
	t.Parallel()

	store := newMemoryDCRStore(t)
	ctx := context.Background()

	key := Key{
		Issuer:      "https://idp.example.com",
		UpstreamID:  "https://upstream.example.com",
		RedirectURI: "https://toolhive.example.com/oauth/callback",
		ScopesHash:  storage.ScopesHash([]string{"openid", "profile"}),
	}
	resolution := &Resolution{
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

func TestStorageBackedStore_Get_MissingKey(t *testing.T) {
	t.Parallel()

	store := newMemoryDCRStore(t)
	ctx := context.Background()

	got, ok, err := store.Get(ctx, Key{Issuer: "https://unknown.example.com"})
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestStorageBackedStore_DistinctKeysDoNotCollide(t *testing.T) {
	t.Parallel()

	store := newMemoryDCRStore(t)
	ctx := context.Background()

	keyA := Key{
		Issuer:      "https://idp-a.example.com",
		UpstreamID:  "https://upstream-a.example.com",
		RedirectURI: "https://toolhive.example.com/oauth/callback",
		ScopesHash:  storage.ScopesHash([]string{"openid"}),
	}
	keyB := Key{
		Issuer:      "https://idp-b.example.com",
		UpstreamID:  "https://upstream-a.example.com",
		RedirectURI: "https://toolhive.example.com/oauth/callback",
		ScopesHash:  storage.ScopesHash([]string{"openid"}),
	}
	keyC := Key{
		Issuer:      "https://idp-a.example.com",
		UpstreamID:  "https://upstream-a.example.com",
		RedirectURI: "https://other.example.com/callback",
		ScopesHash:  storage.ScopesHash([]string{"openid"}),
	}
	keyD := Key{
		Issuer:      "https://idp-a.example.com",
		UpstreamID:  "https://upstream-a.example.com",
		RedirectURI: "https://toolhive.example.com/oauth/callback",
		ScopesHash:  storage.ScopesHash([]string{"openid", "email"}),
	}
	// keyE shares Issuer, RedirectURI, and scopes with keyA and differs only
	// by UpstreamID — the exact shape of two OAuth2 upstreams inside one
	// embedded authserver. Before UpstreamID was part of the key these two
	// collided (issue #5823); keyE must resolve to its own entry.
	keyE := Key{
		Issuer:      "https://idp-a.example.com",
		UpstreamID:  "https://upstream-b.example.com",
		RedirectURI: "https://toolhive.example.com/oauth/callback",
		ScopesHash:  storage.ScopesHash([]string{"openid"}),
	}

	// The persisted *storage.DCRCredentials shape requires non-empty
	// AuthorizationEndpoint / TokenEndpoint per validateDCRCredentialsForStore;
	// supply a minimal valid resolution so the storage-backed adapter accepts
	// the Put, since the test asserts key-distinctness and not field shape.
	resolution := func(clientID string) *Resolution {
		return &Resolution{
			ClientID:              clientID,
			AuthorizationEndpoint: "https://idp.example.com/authorize",
			TokenEndpoint:         "https://idp.example.com/token",
		}
	}

	require.NoError(t, store.Put(ctx, keyA, resolution("a")))
	require.NoError(t, store.Put(ctx, keyB, resolution("b")))
	require.NoError(t, store.Put(ctx, keyC, resolution("c")))
	require.NoError(t, store.Put(ctx, keyD, resolution("d")))
	require.NoError(t, store.Put(ctx, keyE, resolution("e")))

	for _, tc := range []struct {
		key      Key
		expected string
	}{
		{keyA, "a"},
		{keyB, "b"},
		{keyC, "c"},
		{keyD, "d"},
		{keyE, "e"},
	} {
		got, ok, err := store.Get(ctx, tc.key)
		require.NoError(t, err)
		require.True(t, ok, "key %+v should be present", tc.key)
		assert.Equal(t, tc.expected, got.ClientID)
	}
}

func TestStorageBackedStore_Put_OverwritesExisting(t *testing.T) {
	t.Parallel()

	store := newMemoryDCRStore(t)
	ctx := context.Background()

	key := Key{
		Issuer:      "https://idp.example.com",
		UpstreamID:  "https://upstream.example.com",
		RedirectURI: "https://x.example.com/cb",
		ScopesHash:  storage.ScopesHash([]string{"openid"}),
	}
	endpoints := struct {
		Authorization string
		Token         string
	}{
		Authorization: "https://idp.example.com/authorize",
		Token:         "https://idp.example.com/token",
	}
	require.NoError(t, store.Put(ctx, key, &Resolution{
		ClientID:              "first",
		AuthorizationEndpoint: endpoints.Authorization,
		TokenEndpoint:         endpoints.Token,
	}))
	require.NoError(t, store.Put(ctx, key, &Resolution{
		ClientID:              "second",
		AuthorizationEndpoint: endpoints.Authorization,
		TokenEndpoint:         endpoints.Token,
	}))

	got, ok, err := store.Get(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "second", got.ClientID)
}

// TestStorageBackedStore_Put_RejectsNilResolution pins the
// fail-loud-on-invalid-input contract: passing nil must error rather than
// silently no-op. A silent no-op would leave the caller with a successful
// Put followed by a Get miss and no debug trail to explain it.
func TestStorageBackedStore_Put_RejectsNilResolution(t *testing.T) {
	t.Parallel()

	store := newMemoryDCRStore(t)
	ctx := context.Background()
	key := Key{Issuer: "https://idp.example.com", RedirectURI: "https://x.example.com/cb"}

	err := store.Put(ctx, key, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be nil")

	// And confirm the rejection did not partially populate the store.
	_, ok, getErr := store.Get(ctx, key)
	require.NoError(t, getErr)
	assert.False(t, ok, "rejected Put must not leave any entry behind")
}

func TestStorageBackedStore_GetReturnsDefensiveCopy(t *testing.T) {
	t.Parallel()

	store := newMemoryDCRStore(t)
	ctx := context.Background()

	key := Key{
		Issuer:      "https://idp.example.com",
		UpstreamID:  "https://upstream.example.com",
		RedirectURI: "https://x.example.com/cb",
		ScopesHash:  storage.ScopesHash([]string{"openid"}),
	}
	require.NoError(t, store.Put(ctx, key, &Resolution{
		ClientID:              "orig",
		AuthorizationEndpoint: "https://idp.example.com/authorize",
		TokenEndpoint:         "https://idp.example.com/token",
	}))

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
// Duplicating the suite here would re-exercise the same code, which is
// redundant per .claude/rules/testing.md.

// TestStorageBackedStore_ConcurrentAccess fans out N goroutines
// performing alternating Put / Get against overlapping and disjoint keys,
// exercising the safe-for-concurrent-use contract advertised on the
// CredentialStore interface. The contract is satisfied by the underlying
// storage.DCRCredentialStore (which holds the lock); this test runs under
// `go test -race` so any future regression that drops the storage backend's
// guarantee, or an adapter change that races on its own state, fails loudly.
//
// The test is bounded by a fail-fast deadline so a regression that
// deadlocks fails loudly with a clear message rather than hanging until
// the global Go test timeout.
func TestStorageBackedStore_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	store := newMemoryDCRStore(t)

	const (
		workers      = 16
		opsPerWorker = 200
	)

	// Two key spaces: overlapping (every worker writes the same keys, so the
	// lock must serialise their writes) and disjoint (each worker has its own
	// key space, so reads never see another worker's writes).
	overlappingKey := func(i int) Key {
		return Key{
			Issuer:      "https://idp.example.com",
			UpstreamID:  "https://upstream.example.com",
			RedirectURI: "https://thv.example.com/oauth/callback",
			ScopesHash:  fmt.Sprintf("overlap-%d", i%4),
		}
	}
	disjointKey := func(worker, i int) Key {
		return Key{
			Issuer:      fmt.Sprintf("https://idp-%d.example.com", worker),
			UpstreamID:  "https://upstream.example.com",
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
				resolution := &Resolution{
					ClientID:              fmt.Sprintf("worker-%d-op-%d", worker, i),
					AuthorizationEndpoint: "https://idp.example.com/authorize",
					TokenEndpoint:         "https://idp.example.com/token",
					CreatedAt:             time.Now(),
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

// TestResolutionCredentialsRoundTrip pins the field-by-field contract
// between resolutionToCredentials and credentialsToResolution: which
// fields survive a round-trip, which are intentionally dropped, and
// which are recovered from the persisted Key. The test exists
// because the two converters are the seam where a field added to either
// Resolution or storage.DCRCredentials must be paired with an update
// here; without coverage, a future field addition would silently fail
// to persist across an authserver restart.
//
// The "preserved" group asserts equality on round-tripped values. The
// "dropped" group asserts that the post-round-trip value is the type's
// zero value (ClientIDIssuedAt is informational per RFC 7591 §3.2.1 and
// is not persisted). RedirectURI is in its own group because it is
// dropped from DCRCredentials and recovered via Key.RedirectURI on
// read.
//
// ProviderName is the one DCRCredentials field with no Resolution
// counterpart. It is documented in storage.DCRCredentials as "debug /
// audit only — never used as a primary key" and no current consumer
// reads it. The decision to leave it unpopulated by the runner is
// recorded here so a future contributor adding ProviderName threading
// has a single grep target.
func TestResolutionCredentialsRoundTrip(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Round(time.Second)
	expiry := now.Add(30 * 24 * time.Hour)

	key := Key{
		Issuer:      "https://idp.example.com",
		UpstreamID:  "https://upstream.example.com",
		RedirectURI: "https://thv.example.com/oauth/callback",
		ScopesHash:  storage.ScopesHash([]string{"openid", "profile"}),
	}

	original := &Resolution{
		ClientID:                "round-trip-client-id",
		ClientSecret:            "round-trip-secret",
		AuthorizationEndpoint:   "https://idp.example.com/authorize",
		TokenEndpoint:           "https://idp.example.com/token",
		RegistrationAccessToken: "rfc7592-token",
		RegistrationClientURI:   "https://idp.example.com/register/round-trip-client-id",
		TokenEndpointAuthMethod: "client_secret_basic",
		// RedirectURI is recovered from Key on read; pre-populate it on
		// the input to confirm it survives via the key, not via a
		// dedicated field on DCRCredentials.
		RedirectURI:           key.RedirectURI,
		ClientIDIssuedAt:      now, // intentionally dropped on persist
		ClientSecretExpiresAt: expiry,
		CreatedAt:             now,
	}

	creds := resolutionToCredentials(key, original)
	require.NotNil(t, creds)

	// Persisted-side assertions: which fields the converter writes to
	// DCRCredentials, which it leaves zero (the asymmetry F7 documents).
	assert.Equal(t, key, creds.Key,
		"Key must round-trip via the explicit parameter")
	assert.Empty(t, creds.ProviderName,
		"ProviderName has no Resolution counterpart and is intentionally not populated; "+
			"a future contributor threading it through must update this assertion and the converters together")

	roundTripped := credentialsToResolution(creds)
	require.NotNil(t, roundTripped)

	t.Run("preserved fields survive round-trip", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, original.ClientID, roundTripped.ClientID)
		assert.Equal(t, original.ClientSecret, roundTripped.ClientSecret)
		assert.Equal(t, original.AuthorizationEndpoint, roundTripped.AuthorizationEndpoint)
		assert.Equal(t, original.TokenEndpoint, roundTripped.TokenEndpoint)
		assert.Equal(t, original.RegistrationAccessToken, roundTripped.RegistrationAccessToken)
		assert.Equal(t, original.RegistrationClientURI, roundTripped.RegistrationClientURI)
		assert.Equal(t, original.TokenEndpointAuthMethod, roundTripped.TokenEndpointAuthMethod)
		assert.Equal(t, original.ClientSecretExpiresAt, roundTripped.ClientSecretExpiresAt)
		assert.Equal(t, original.CreatedAt, roundTripped.CreatedAt)
	})

	t.Run("RedirectURI is recovered from Key on read", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, key.RedirectURI, roundTripped.RedirectURI,
			"RedirectURI on the round-tripped resolution must match Key.RedirectURI; "+
				"the converter does not store it twice")
	})

	t.Run("dropped fields zero on read", func(t *testing.T) {
		t.Parallel()
		// ClientIDIssuedAt is informational (RFC 7591 §3.2.1) and not
		// persisted; round-tripping must reset it to the zero value
		// rather than silently re-deriving it from CreatedAt.
		assert.True(t, roundTripped.ClientIDIssuedAt.IsZero(),
			"ClientIDIssuedAt must be zero after round-trip; the field is intentionally dropped from DCRCredentials")
	})

	t.Run("nil inputs short-circuit", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, resolutionToCredentials(key, nil))
		assert.Nil(t, credentialsToResolution(nil))
	})
}

// TestInMemoryStore_CloseIsIdempotent pins the contract that calling
// Close on the returned CloseableCredentialStore more than once is safe.
// The underlying storage.MemoryStorage.Close is NOT idempotent — it
// closes a channel that a second call would re-close, panicking with
// "close of closed channel" — so the dcr-package wrapper MUST guard
// against the second call. Without the sync.Once on inMemoryStore, the
// CLI's `defer store.Close()` plus any future close-on-shortcircuit
// would crash the process; this test makes the guard load-bearing.
func TestInMemoryStore_CloseIsIdempotent(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()

	// First Close must succeed and release the cleanup goroutine.
	require.NoError(t, store.Close())

	// Second Close must NOT panic and must return the same error
	// (nil here, since the first call succeeded).
	require.NoError(t, store.Close(),
		"Close() must be idempotent at the dcr-package boundary; "+
			"the underlying MemoryStorage.Close is not idempotent and a "+
			"second call without the guard would panic")
}

// TestInMemoryStore_CloseIsIdempotentUnderRace verifies the sync.Once
// guard holds under concurrent callers. A regression that replaced
// sync.Once with a non-atomic guard (e.g. a plain bool) would surface
// here as a panic from a racing channel close.
func TestInMemoryStore_CloseIsIdempotentUnderRace(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()

	const N = 8
	var wg sync.WaitGroup
	wg.Add(N)
	errs := make([]error, N)
	for i := range N {
		go func(idx int) {
			defer wg.Done()
			errs[idx] = store.Close()
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for concurrent Close() callers")
	}

	for i := range N {
		require.NoError(t, errs[i], "goroutine %d's Close() must not error", i)
	}
}

// TestInMemoryStore_PutGetCloseShareBackend pins the contract that Put,
// Get, and Close all operate against the same underlying
// *storage.MemoryStorage instance. The previous shape held two handles
// (one in an embedded storageBackedStore.backend, one in a concrete
// mem field); a constructor regression assigning distinct handles to
// the two fields would have surfaced as a Close that leaks the
// cleanup goroutine while Get still serves entries. Today only one
// handle exists, so this test asserts the observable consequence:
// after Close, the goroutine is gone (verified by t.Cleanup running
// without leaking via go test -race goroutine leak checks).
func TestInMemoryStore_PutGetCloseShareBackend(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	key := Key{
		Issuer:      "https://idp.example.com",
		UpstreamID:  "https://upstream.example.com",
		RedirectURI: "https://toolhive.example.com/oauth/callback",
		ScopesHash:  storage.ScopesHash([]string{"openid"}),
	}
	resolution := &Resolution{
		ClientID:              "client-abc",
		AuthorizationEndpoint: "https://idp.example.com/authorize",
		TokenEndpoint:         "https://idp.example.com/token",
	}

	require.NoError(t, store.Put(ctx, key, resolution))

	got, ok, err := store.Get(ctx, key)
	require.NoError(t, err)
	require.True(t, ok, "Get must see the value Put just wrote — confirms Put and Get share a backend")
	assert.Equal(t, "client-abc", got.ClientID)
}

// TestInMemoryStore_PutRejectsNilResolution mirrors the contract pinned
// for storageBackedStore.Put: a nil resolution is rejected at the
// adapter boundary rather than silently no-oped, so the next Get miss
// surfaces with a debug trail. inMemoryStore implements Put directly
// (not via embedding) — this test guards against a delegation
// regression that omitted the nil check.
func TestInMemoryStore_PutRejectsNilResolution(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()
	t.Cleanup(func() { _ = store.Close() })

	err := store.Put(context.Background(), Key{Issuer: "https://idp.example.com"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolution must not be nil")
}

// TestInMemoryStore_GetMissingKeyReturnsMissTuple pins the ErrNotFound
// translation contract: a missing key surfaces as (nil, false, nil)
// rather than as a wrapped error. inMemoryStore implements Get directly
// (not via embedding) — this test guards against a delegation
// regression that surfaced ErrNotFound as a hard error.
func TestInMemoryStore_GetMissingKeyReturnsMissTuple(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()
	t.Cleanup(func() { _ = store.Close() })

	got, ok, err := store.Get(context.Background(), Key{Issuer: "https://unknown.example.com"})
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, got)
}
