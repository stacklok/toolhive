// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInMemoryDCRCredentialStore_PutGet_RoundTrip(t *testing.T) {
	t.Parallel()

	store := NewInMemoryDCRCredentialStore()
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

func TestInMemoryDCRCredentialStore_Get_MissingKey(t *testing.T) {
	t.Parallel()

	store := NewInMemoryDCRCredentialStore()
	ctx := context.Background()

	got, ok, err := store.Get(ctx, DCRKey{Issuer: "https://unknown.example.com"})
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestInMemoryDCRCredentialStore_DistinctKeysDoNotCollide(t *testing.T) {
	t.Parallel()

	store := NewInMemoryDCRCredentialStore()
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

func TestInMemoryDCRCredentialStore_Put_OverwritesExisting(t *testing.T) {
	t.Parallel()

	store := NewInMemoryDCRCredentialStore()
	ctx := context.Background()

	key := DCRKey{Issuer: "https://idp.example.com", RedirectURI: "https://x.example.com/cb"}
	require.NoError(t, store.Put(ctx, key, &DCRResolution{ClientID: "first"}))
	require.NoError(t, store.Put(ctx, key, &DCRResolution{ClientID: "second"}))

	got, ok, err := store.Get(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "second", got.ClientID)
}

func TestInMemoryDCRCredentialStore_GetReturnsDefensiveCopy(t *testing.T) {
	t.Parallel()

	store := NewInMemoryDCRCredentialStore()
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

func TestScopesHash_StableAcrossPermutation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b []string
	}{
		{
			name: "two-element permutation",
			a:    []string{"openid", "profile"},
			b:    []string{"profile", "openid"},
		},
		{
			name: "three-element permutation",
			a:    []string{"openid", "profile", "email"},
			b:    []string{"email", "openid", "profile"},
		},
		{
			// OAuth scope sets are sets, not multisets (RFC 6749 §3.3).
			// scopesHash deduplicates before hashing so a caller who
			// accidentally repeats a scope still hits the cache entry
			// keyed under the canonical set.
			name: "single element equals double element duplicate",
			a:    []string{"openid"},
			b:    []string{"openid", "openid"},
		},
		{
			name: "three-element with duplicate equals two-element unique",
			a:    []string{"openid", "profile", "openid"},
			b:    []string{"openid", "profile"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, scopesHash(tc.a), scopesHash(tc.b))
		})
	}
}

func TestScopesHash_DistinctForDistinctScopes(t *testing.T) {
	t.Parallel()

	a := scopesHash([]string{"openid"})
	b := scopesHash([]string{"openid", "profile"})
	c := scopesHash([]string{"profile"})
	d := scopesHash(nil)
	e := scopesHash([]string{})

	// Non-empty distinct sets produce distinct hashes.
	assert.NotEqual(t, a, b)
	assert.NotEqual(t, a, c)
	assert.NotEqual(t, b, c)
	assert.NotEqual(t, a, d)
	// nil and empty slice canonicalise to the same hash (both sort-then-join
	// to the empty canonical form).
	assert.Equal(t, d, e)
}

func TestScopesHash_NoCollisionFromBoundaryJoin(t *testing.T) {
	t.Parallel()

	// Without a delimiter that cannot appear inside a scope value,
	// ["ab", "c"] and ["a", "bc"] would collide. This test exists to
	// prevent a regression if the canonical form is ever simplified.
	h1 := scopesHash([]string{"ab", "c"})
	h2 := scopesHash([]string{"a", "bc"})
	assert.NotEqual(t, h1, h2)
}

func TestDCRStaleAgeThreshold_Is90Days(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 90*24*time.Hour, dcrStaleAgeThreshold)
}
