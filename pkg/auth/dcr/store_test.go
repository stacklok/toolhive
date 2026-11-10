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

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/oauthproto/dcrkey"
)

func TestInMemoryStore_PutGet_RoundTrip(t *testing.T) {
	t.Parallel()

	store := newMemoryDCRStore(t)
	ctx := context.Background()

	key := Key{
		Issuer:      "https://idp.example.com",
		UpstreamID:  "https://upstream.example.com",
		RedirectURI: "https://toolhive.example.com/oauth/callback",
		ScopesHash:  dcrkey.ScopesHash([]string{"openid", "profile"}),
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

	authoritative, err := store.PutIfAbsent(ctx, key, resolution)
	require.NoError(t, err)
	assert.Equal(t, resolution.ClientID, authoritative.ClientID,
		"an uncontested claim must return the caller's own resolution")

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

func TestInMemoryStore_Get_MissingKey(t *testing.T) {
	t.Parallel()

	store := newMemoryDCRStore(t)
	ctx := context.Background()

	got, ok, err := store.Get(ctx, Key{Issuer: "https://unknown.example.com"})
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestInMemoryStore_DistinctKeysDoNotCollide(t *testing.T) {
	t.Parallel()

	store := newMemoryDCRStore(t)
	ctx := context.Background()

	keyA := Key{
		Issuer:      "https://idp-a.example.com",
		UpstreamID:  "https://upstream-a.example.com",
		RedirectURI: "https://toolhive.example.com/oauth/callback",
		ScopesHash:  dcrkey.ScopesHash([]string{"openid"}),
	}
	keyB := Key{
		Issuer:      "https://idp-b.example.com",
		UpstreamID:  "https://upstream-a.example.com",
		RedirectURI: "https://toolhive.example.com/oauth/callback",
		ScopesHash:  dcrkey.ScopesHash([]string{"openid"}),
	}
	keyC := Key{
		Issuer:      "https://idp-a.example.com",
		UpstreamID:  "https://upstream-a.example.com",
		RedirectURI: "https://other.example.com/callback",
		ScopesHash:  dcrkey.ScopesHash([]string{"openid"}),
	}
	keyD := Key{
		Issuer:      "https://idp-a.example.com",
		UpstreamID:  "https://upstream-a.example.com",
		RedirectURI: "https://toolhive.example.com/oauth/callback",
		ScopesHash:  dcrkey.ScopesHash([]string{"openid", "email"}),
	}
	// keyE shares Issuer, RedirectURI, and scopes with keyA and differs only
	// by UpstreamID — the exact shape of two OAuth2 upstreams inside one
	// embedded authserver. Before UpstreamID was part of the key these two
	// collided (issue #5823); keyE must resolve to its own entry.
	keyE := Key{
		Issuer:      "https://idp-a.example.com",
		UpstreamID:  "https://upstream-b.example.com",
		RedirectURI: "https://toolhive.example.com/oauth/callback",
		ScopesHash:  dcrkey.ScopesHash([]string{"openid"}),
	}
	// keyF is keyA under an operator-set RotationKey: the rotation must
	// address its own entry while keyA's row stays reachable.
	keyF := keyA
	keyF.RotationKey = "r2"

	// Supply valid endpoints; this test exercises key isolation.
	resolution := func(clientID string) *Resolution {
		return &Resolution{
			ClientID:              clientID,
			AuthorizationEndpoint: "https://idp.example.com/authorize",
			TokenEndpoint:         "https://idp.example.com/token",
		}
	}

	for _, put := range []struct {
		key      Key
		clientID string
	}{
		{keyA, "a"}, {keyB, "b"}, {keyC, "c"}, {keyD, "d"}, {keyE, "e"}, {keyF, "f"},
	} {
		_, err := store.PutIfAbsent(ctx, put.key, resolution(put.clientID))
		require.NoError(t, err)
	}

	for _, tc := range []struct {
		key      Key
		expected string
	}{
		{keyA, "a"},
		{keyB, "b"},
		{keyC, "c"},
		{keyD, "d"},
		{keyE, "e"},
		{keyF, "f"},
	} {
		got, ok, err := store.Get(ctx, tc.key)
		require.NoError(t, err)
		require.True(t, ok, "key %+v should be present", tc.key)
		assert.Equal(t, tc.expected, got.ClientID)
	}
}

// TestInMemoryStore_PutIfAbsent_FirstClaimWins pins the create-if-absent
// contract that replaced unconditional overwrite (see PutIfAbsent doc): a
// second PutIfAbsent for a key that already holds a value must NOT overwrite
// it. Instead it returns the existing (first) resolution as the authoritative
// value, and the store keeps holding the first entry. This is the fix for the
// concurrent-replica bug where two callers independently register distinct
// RFC 7591 clients for the same key — only one registration may ever become
// the durable, agreed-upon value.
func TestInMemoryStore_PutIfAbsent_FirstClaimWins(t *testing.T) {
	t.Parallel()

	store := newMemoryDCRStore(t)
	ctx := context.Background()

	key := Key{
		Issuer:      "https://idp.example.com",
		UpstreamID:  "https://upstream.example.com",
		RedirectURI: "https://x.example.com/cb",
		ScopesHash:  dcrkey.ScopesHash([]string{"openid"}),
	}
	endpoints := struct {
		Authorization string
		Token         string
	}{
		Authorization: "https://idp.example.com/authorize",
		Token:         "https://idp.example.com/token",
	}
	first, err := store.PutIfAbsent(ctx, key, &Resolution{
		ClientID:              "first",
		AuthorizationEndpoint: endpoints.Authorization,
		TokenEndpoint:         endpoints.Token,
	})
	require.NoError(t, err)
	assert.Equal(t, "first", first.ClientID, "the uncontested first claim returns its own resolution")

	second, err := store.PutIfAbsent(ctx, key, &Resolution{
		ClientID:              "second",
		AuthorizationEndpoint: endpoints.Authorization,
		TokenEndpoint:         endpoints.Token,
	})
	require.NoError(t, err)
	assert.Equal(t, "first", second.ClientID,
		"the loser must get back the winner's resolution, not its own")

	got, ok, err := store.Get(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "first", got.ClientID, "the store must keep the first-claimed entry, not overwrite it")
}

// TestInMemoryStore_Put_RejectsNilResolution pins the
// fail-loud-on-invalid-input contract: passing nil must error rather than
// silently no-op. A silent no-op would leave the caller with a successful
// Put followed by a Get miss and no debug trail to explain it.
func TestInMemoryStore_Put_RejectsNilResolution(t *testing.T) {
	t.Parallel()

	store := newMemoryDCRStore(t)
	ctx := context.Background()
	key := Key{Issuer: "https://idp.example.com", RedirectURI: "https://x.example.com/cb"}

	_, err := store.PutIfAbsent(ctx, key, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be nil")

	// And confirm the rejection did not partially populate the store.
	_, ok, getErr := store.Get(ctx, key)
	require.NoError(t, getErr)
	assert.False(t, ok, "rejected Put must not leave any entry behind")
}

func TestInMemoryStore_GetReturnsDefensiveCopy(t *testing.T) {
	t.Parallel()

	store := newMemoryDCRStore(t)
	ctx := context.Background()

	key := Key{
		Issuer:      "https://idp.example.com",
		UpstreamID:  "https://upstream.example.com",
		RedirectURI: "https://x.example.com/cb",
		ScopesHash:  dcrkey.ScopesHash([]string{"openid"}),
	}
	_, err := store.PutIfAbsent(ctx, key, &Resolution{
		ClientID:              "orig",
		AuthorizationEndpoint: "https://idp.example.com/authorize",
		TokenEndpoint:         "https://idp.example.com/token",
	})
	require.NoError(t, err)

	got, ok, err := store.Get(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	got.ClientID = "mutated"

	refetched, ok, err := store.Get(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "orig", refetched.ClientID)
}

// Canonical scope-hash tests live in pkg/oauthproto/dcrkey.

// TestInMemoryStore_ConcurrentAccess exercises overlapping and disjoint keys.
// The timeout bounds regressions in the mutex-protected map.
func TestInMemoryStore_ConcurrentAccess(t *testing.T) {
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
					if _, err := store.PutIfAbsent(ctx, overlappingKey(i), resolution); err != nil {
						atomic.AddInt32(&errCount, 1)
					}
					if _, _, err := store.Get(ctx, overlappingKey(i)); err != nil {
						atomic.AddInt32(&errCount, 1)
					}
				} else {
					if _, err := store.PutIfAbsent(ctx, disjointKey(worker, i), resolution); err != nil {
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

// TestInMemoryStore_CloseIsIdempotent pins the compatibility no-op contract.
func TestInMemoryStore_CloseIsIdempotent(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()

	require.NoError(t, store.Close())
	require.NoError(t, store.Close())
}

// TestInMemoryStore_CloseIsIdempotentUnderRace exercises concurrent Close calls.
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

// TestInMemoryStore_PutGetAfterClose pins the absence of closed-state semantics.
func TestInMemoryStore_PutGetAfterClose(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	key := Key{
		Issuer:      "https://idp.example.com",
		UpstreamID:  "https://upstream.example.com",
		RedirectURI: "https://toolhive.example.com/oauth/callback",
		ScopesHash:  dcrkey.ScopesHash([]string{"openid"}),
	}
	resolution := &Resolution{
		ClientID:              "client-abc",
		AuthorizationEndpoint: "https://idp.example.com/authorize",
		TokenEndpoint:         "https://idp.example.com/token",
	}

	require.NoError(t, store.Close())
	_, err := store.PutIfAbsent(ctx, key, resolution)
	require.NoError(t, err)

	got, ok, err := store.Get(ctx, key)
	require.NoError(t, err)
	require.True(t, ok, "Close must not prevent subsequent writes or reads")
	assert.Equal(t, "client-abc", got.ClientID)
}

// TestInMemoryStore_PutRejectsNilResolution checks fail-loud input handling.
func TestInMemoryStore_PutRejectsNilResolution(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()
	t.Cleanup(func() { _ = store.Close() })

	_, err := store.PutIfAbsent(context.Background(), Key{Issuer: "https://idp.example.com"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolution must not be nil")
}

// TestInMemoryStore_GetMissingKeyReturnsMissTuple pins the miss contract.
func TestInMemoryStore_GetMissingKeyReturnsMissTuple(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()
	t.Cleanup(func() { _ = store.Close() })

	got, ok, err := store.Get(context.Background(), Key{Issuer: "https://unknown.example.com"})
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestCredentialStore_ReplaceIfUnchanged(t *testing.T) {
	t.Parallel()

	key := Key{
		Issuer:      "https://idp.example.com",
		UpstreamID:  "https://upstream.example.com",
		RedirectURI: "https://toolhive.example.com/oauth/callback",
		ScopesHash:  dcrkey.ScopesHash([]string{"openid"}),
	}
	registration := func(clientID string) *Resolution {
		return &Resolution{
			ClientID:              clientID,
			ClientSecret:          clientID + "-secret",
			AuthorizationEndpoint: "https://upstream.example.com/authorize",
			TokenEndpoint:         "https://upstream.example.com/token",
			CreatedAt:             time.Now(),
		}
	}
	stale, newer, fresh := registration("stale-id"), registration("newer-id"), registration("fresh-id")

	type replacingStore interface {
		CredentialStore
		Replacer
	}
	newStore := func(t *testing.T) replacingStore {
		t.Helper()
		s := NewInMemoryStore()
		t.Cleanup(func() { _ = s.Close() })
		r, ok := s.(replacingStore)
		require.True(t, ok, "the in-memory store must implement Replacer")
		return r
	}
	tests := []struct {
		name     string
		stored   *Resolution
		wantID   string
		wantRead string
	}{
		{name: "replaces the registration the caller read", stored: stale, wantID: "fresh-id", wantRead: "fresh-id"},
		{name: "keeps a registration a concurrent writer already replaced", stored: newer, wantID: "newer-id", wantRead: "newer-id"},
		{name: "claims the key when the row is gone", stored: nil, wantID: "fresh-id", wantRead: "fresh-id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := newStore(t)
			if tt.stored != nil {
				_, err := store.PutIfAbsent(ctx, key, tt.stored)
				require.NoError(t, err)
			}

			candidate := *fresh
			candidate.RedirectURI = "noncanonical"
			candidate.ClientIDIssuedAt = time.Now()
			original := candidate
			got, err := store.ReplaceIfUnchanged(ctx, key, stale, &candidate)
			require.NoError(t, err)
			assert.Equal(t, original, candidate, "replacement must not mutate caller input")
			assert.Equal(t, tt.wantID, got.ClientID)
			want := original
			if tt.stored == newer {
				want = *newer
			}
			want.RedirectURI, want.ClientIDIssuedAt = key.RedirectURI, time.Time{}
			assert.Equal(t, want, *got)
			candidate.ClientID, got.ClientID = "input-mutated", "output-mutated"

			read, ok, err := store.Get(ctx, key)
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, tt.wantRead, read.ClientID)
			assert.Equal(t, want, *read)
		})
	}
	t.Run("rejects nil resolutions before returning a concurrent winner", func(t *testing.T) {
		t.Parallel()
		store := newStore(t)
		_, err := store.PutIfAbsent(context.Background(), key, newer)
		require.NoError(t, err)
		_, err = store.ReplaceIfUnchanged(context.Background(), key, nil, fresh)
		require.Error(t, err)
		_, err = store.ReplaceIfUnchanged(context.Background(), key, stale, nil)
		require.Error(t, err)
		_, err = store.PutIfAbsent(context.Background(), key, nil)
		require.Error(t, err)
	})
}

func TestInMemoryStore_Validation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		field      string
		invalidate func(*Key, *Resolution)
	}{
		{"key issuer", func(k *Key, _ *Resolution) { k.Issuer = "" }},
		{"key upstream_id", func(k *Key, _ *Resolution) { k.UpstreamID = "" }},
		{"key redirect_uri", func(k *Key, _ *Resolution) { k.RedirectURI = "" }},
		{"key scopes_hash", func(k *Key, _ *Resolution) { k.ScopesHash = "" }},
		{"client_id", func(_ *Key, r *Resolution) { r.ClientID = "" }},
		{"authorization_endpoint", func(_ *Key, r *Resolution) { r.AuthorizationEndpoint = "" }},
		{"token_endpoint", func(_ *Key, r *Resolution) { r.TokenEndpoint = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := NewInMemoryStore()
			key := Key{Issuer: "issuer", UpstreamID: "upstream", RedirectURI: "redirect", ScopesHash: dcrkey.ScopesHash(nil)}
			valid := &Resolution{ClientID: "client", AuthorizationEndpoint: "auth", TokenEndpoint: "token"}
			candidate := *valid
			tt.invalidate(&key, &candidate)
			// Validation must run before looking up even an existing winner.
			memory := store.(*inMemoryStore)
			memory.entries[key] = *valid
			_, err := store.PutIfAbsent(ctx, key, &candidate)
			var oauthErr *fosite.RFC6749Error
			require.ErrorAs(t, err, &oauthErr)
			assert.Equal(t, fosite.ErrInvalidRequest.ErrorField, oauthErr.ErrorField)
			assert.Equal(t, "dcr credentials "+tt.field+" cannot be empty", oauthErr.HintField)
			// Replacement validates a matching or missing row, but a different
			// stored identity wins before candidate validation.
			replacer := store.(Replacer)
			_, err = replacer.ReplaceIfUnchanged(ctx, key, valid, &candidate)
			require.Error(t, err)
			winner, err := replacer.ReplaceIfUnchanged(ctx, key, &Resolution{ClientID: "other"}, &candidate)
			require.NoError(t, err)
			assert.Equal(t, valid.ClientID, winner.ClientID)
			delete(memory.entries, key)
			_, err = replacer.ReplaceIfUnchanged(ctx, key, valid, &candidate)
			require.Error(t, err)
			_, ok, err := store.Get(ctx, key)
			require.NoError(t, err)
			assert.False(t, ok)
		})
	}
}

func TestInMemoryStore_CopiesNormalizationAndExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key := Key{Issuer: "issuer", UpstreamID: "upstream", RedirectURI: "canonical", ScopesHash: dcrkey.ScopesHash(nil)}
	for _, expiry := range []time.Time{{}, time.Now().Add(time.Hour), time.Now().Add(-time.Hour)} {
		t.Run(fmt.Sprint(expiry), func(t *testing.T) {
			t.Parallel()
			store := NewInMemoryStore()
			original := Resolution{ClientID: "public", AuthorizationEndpoint: "auth", TokenEndpoint: "token", RedirectURI: "ignored", ClientIDIssuedAt: time.Now(), CreatedAt: time.Now().Add(-time.Hour), ClientSecretExpiresAt: expiry}
			input := original
			want := original
			want.RedirectURI = key.RedirectURI
			want.ClientIDIssuedAt = time.Time{}
			result, err := store.PutIfAbsent(ctx, key, &input)
			require.NoError(t, err)
			assert.Equal(t, original, input)
			assert.Equal(t, want, *result)
			input.ClientID, result.ClientID = "input-mutated", "output-mutated"
			got, ok, err := store.Get(ctx, key)
			require.NoError(t, err)
			require.True(t, ok, "even expired physical entries remain readable")
			assert.Equal(t, want, *got)
			got.ClientID = "get-mutated"
			fresh := original
			fresh.ClientID, fresh.ClientSecretExpiresAt = "fresh", time.Time{}
			replacement, err := store.PutIfAbsent(ctx, key, &fresh)
			require.NoError(t, err)
			wantID := "public"
			if !expiry.IsZero() && time.Now().After(expiry) {
				wantID = "fresh"
			}
			assert.Equal(t, wantID, replacement.ClientID)
			replacement.ClientID = "winner-mutated"
			got, _, err = store.Get(ctx, key)
			require.NoError(t, err)
			assert.Equal(t, wantID, got.ClientID)
		})
	}
}

func TestInMemoryStore_ReplacementContention(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewInMemoryStore()
	key := Key{Issuer: "issuer", UpstreamID: "upstream", RedirectURI: "redirect", ScopesHash: dcrkey.ScopesHash(nil)}
	stale := &Resolution{ClientID: "stale", AuthorizationEndpoint: "auth", TokenEndpoint: "token", ClientSecretExpiresAt: time.Now().Add(-time.Hour)}
	_, err := store.PutIfAbsent(ctx, key, stale)
	require.NoError(t, err)
	const workers = 16
	results := make(chan *Resolution, workers)
	errs := make(chan error, workers)
	start := make(chan struct{})
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for i := range workers {
		go func() {
			select {
			case <-start:
			case <-ctx.Done():
				return
			}
			candidate := *stale
			candidate.ClientID = fmt.Sprint(i)
			candidate.ClientSecretExpiresAt = time.Time{}
			got, err := store.(Replacer).ReplaceIfUnchanged(ctx, key, stale, &candidate)
			if err != nil {
				errs <- err
				return
			}
			results <- got
		}()
	}
	close(start)
	winner := ""
	for range workers {
		select {
		case err := <-errs:
			require.NoError(t, err)
		case result := <-results:
			if winner == "" {
				winner = result.ClientID
			}
			assert.Equal(t, winner, result.ClientID)
			result.ClientID = "mutated"
		case <-ctx.Done():
			t.Fatal("timeout waiting for replacement contenders")
		}
	}
	got, _, err := store.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, winner, got.ClientID)
}
