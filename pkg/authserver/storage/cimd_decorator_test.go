// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/oauthproto/cimd"
)

// serveCIMDDoc starts an httptest.Server that serves a valid CIMD document at
// path. The document's client_id equals the full URL (scheme + host + path) as
// required by ValidateClientMetadataDocument. The returned server URL is the
// base (without path); append path to form the client_id.
//
// An optional pre-handler runs before the default JSON response, allowing
// tests to inject counters or delays. Pass nil to use the default behaviour.
func serveCIMDDoc(t *testing.T, path string, preHandler func()) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		if preHandler != nil {
			preHandler()
		}
		// client_id must equal the URL we are serving from.
		clientID := "http://" + r.Host + r.URL.Path
		doc := cimd.ClientMetadataDocument{
			ClientID:     clientID,
			RedirectURIs: []string{"https://example.com/callback"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newTestBase creates a MemoryStorage suitable for use as the decorator base in tests.
func newTestBase(t *testing.T) *MemoryStorage {
	t.Helper()
	base := NewMemoryStorage()
	t.Cleanup(func() { _ = base.Close() })
	return base
}

// newEnabledDecorator creates a CIMDStorageDecorator wrapping base.
func newEnabledDecorator(t *testing.T, base *MemoryStorage, maxSize int, ttl time.Duration) *CIMDStorageDecorator {
	t.Helper()
	got, err := NewCIMDStorageDecorator(base, true, maxSize, ttl)
	require.NoError(t, err)
	return got.(*CIMDStorageDecorator)
}

// cimdURL returns the CIMD URL for the given server and path.
func cimdURL(srv *httptest.Server, path string) string {
	return srv.URL + path
}

// --- Constructor tests ---

func TestNewCIMDStorageDecorator_DisabledReturnsBase(t *testing.T) {
	t.Parallel()
	base := newTestBase(t)
	got, err := NewCIMDStorageDecorator(base, false, 10, time.Minute)
	require.NoError(t, err)
	assert.Same(t, base, got, "disabled decorator must return base unchanged")
}

func TestNewCIMDStorageDecorator_ZeroCacheSizeReturnsError(t *testing.T) {
	t.Parallel()
	base := newTestBase(t)
	_, err := NewCIMDStorageDecorator(base, true, 0, time.Minute)
	require.Error(t, err)
}

func TestNewCIMDStorageDecorator_NegativeCacheSizeReturnsError(t *testing.T) {
	t.Parallel()
	base := newTestBase(t)
	_, err := NewCIMDStorageDecorator(base, true, -1, time.Minute)
	require.Error(t, err)
}

func TestNewCIMDStorageDecorator_EnabledReturnsCIMDDecorator(t *testing.T) {
	t.Parallel()
	base := newTestBase(t)
	got, err := NewCIMDStorageDecorator(base, true, 10, time.Minute)
	require.NoError(t, err)
	require.NotNil(t, got)
	_, isCIMD := got.(*CIMDStorageDecorator)
	assert.True(t, isCIMD, "enabled decorator must return a *CIMDStorageDecorator")
}

// --- Unwrap ---

func TestCIMDStorageDecorator_UnwrapReturnsBase(t *testing.T) {
	t.Parallel()
	base := newTestBase(t)
	dec := newEnabledDecorator(t, base, 10, time.Minute)
	assert.Same(t, base, dec.Unwrap())
}

// --- GetClient delegation for non-CIMD IDs ---

func TestCIMDStorageDecorator_GetClient_OpaqueIDDelegatesToBase(t *testing.T) {
	t.Parallel()
	base := newTestBase(t)
	ctx := context.Background()

	dc := &fosite.DefaultClient{ID: "opaque-client-id"}
	require.NoError(t, base.RegisterClient(ctx, dc))

	dec := newEnabledDecorator(t, base, 10, time.Minute)

	got, err := dec.GetClient(ctx, "opaque-client-id")
	require.NoError(t, err)
	assert.Equal(t, "opaque-client-id", got.GetID())
}

func TestCIMDStorageDecorator_GetClient_UnknownOpaqueIDReturnsError(t *testing.T) {
	t.Parallel()
	base := newTestBase(t)
	dec := newEnabledDecorator(t, base, 10, time.Minute)
	_, err := dec.GetClient(context.Background(), "unknown-opaque-id")
	require.Error(t, err)
}

// --- fetchOrCached / fetch (loopback HTTP accepted by FetchClientMetadataDocument) ---
// These tests call fetchOrCached directly (same package) using http://127.0.0.1
// URLs, which FetchClientMetadataDocument accepts for testing purposes.

func TestCIMDStorageDecorator_FetchOrCached_FetchesAndReturnsClient(t *testing.T) {
	t.Parallel()

	var fetchCount atomic.Int32
	srv := serveCIMDDoc(t, "/metadata.json", func() { fetchCount.Add(1) })

	id := cimdURL(srv, "/metadata.json")
	dec := newEnabledDecorator(t, newTestBase(t), 10, time.Minute)

	got, err := dec.fetchOrCached(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, id, got.GetID())
	assert.Equal(t, int32(1), fetchCount.Load())
}

func TestCIMDStorageDecorator_FetchOrCached_CacheHitAvoidsSecondFetch(t *testing.T) {
	t.Parallel()

	var fetchCount atomic.Int32
	srv := serveCIMDDoc(t, "/metadata.json", func() { fetchCount.Add(1) })

	id := cimdURL(srv, "/metadata.json")
	dec := newEnabledDecorator(t, newTestBase(t), 10, time.Minute)

	ctx := context.Background()
	_, err := dec.fetchOrCached(ctx, id)
	require.NoError(t, err)

	_, err = dec.fetchOrCached(ctx, id)
	require.NoError(t, err)

	assert.Equal(t, int32(1), fetchCount.Load(), "second call must be served from cache")
}

func TestCIMDStorageDecorator_FetchOrCached_LRUEvictionForcesRefetch(t *testing.T) {
	t.Parallel()

	var fetchCount atomic.Int32
	srv := serveCIMDDoc(t, "/a.json", func() { fetchCount.Add(1) })
	srv2 := serveCIMDDoc(t, "/b.json", func() { fetchCount.Add(1) })

	id1 := cimdURL(srv, "/a.json")
	id2 := cimdURL(srv2, "/b.json")

	// maxSize=1 forces eviction after the first entry.
	dec := newEnabledDecorator(t, newTestBase(t), 1, time.Minute)
	ctx := context.Background()

	_, err := dec.fetchOrCached(ctx, id1)
	require.NoError(t, err)

	// Fetching id2 evicts id1 from the single-slot cache.
	_, err = dec.fetchOrCached(ctx, id2)
	require.NoError(t, err)

	// id1 must re-fetch.
	_, err = dec.fetchOrCached(ctx, id1)
	require.NoError(t, err)

	assert.Equal(t, int32(3), fetchCount.Load(), "id1 must be fetched twice due to LRU eviction")
}

func TestCIMDStorageDecorator_FetchOrCached_SingleflightDeduplicatesConcurrentFetches(t *testing.T) {
	t.Parallel()

	var fetchCount atomic.Int32
	// Barrier lets us hold all goroutines until they are all in-flight.
	ready := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-ready
		fetchCount.Add(1)
		clientID := "http://" + r.Host + r.URL.Path
		doc := cimd.ClientMetadataDocument{
			ClientID:     clientID,
			RedirectURIs: []string{"https://example.com/callback"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)

	id := cimdURL(srv, "/metadata.json")
	dec := newEnabledDecorator(t, newTestBase(t), 10, time.Minute)

	const goroutines = 5
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	// Each goroutine signals on startBarrier immediately before calling
	// fetchOrCached. Draining all signals before closing ready ensures they
	// are all scheduled and about to enter sf.Do, making the singleflight
	// deduplication deterministic without relying on time.Sleep.
	startBarrier := make(chan struct{}, goroutines)

	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			startBarrier <- struct{}{}
			_, errs[i] = dec.fetchOrCached(context.Background(), id)
		}(i)
	}

	for range goroutines {
		<-startBarrier
	}
	close(ready)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for concurrent fetchOrCached goroutines")
	}

	for i, e := range errs {
		require.NoError(t, e, "goroutine %d returned an error", i)
	}
	assert.Equal(t, int32(1), fetchCount.Load(), "singleflight must collapse concurrent fetches into one")
}

func TestCIMDStorageDecorator_FetchOrCached_FetchFailureReturnsNotFound(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	dec := newEnabledDecorator(t, newTestBase(t), 10, time.Minute)
	_, err := dec.fetchOrCached(context.Background(), srv.URL+"/meta.json")
	require.Error(t, err)
	assert.ErrorIs(t, err, fosite.ErrNotFound, "fetch failure must be wrapped as fosite.ErrNotFound")
}

func TestCIMDStorageDecorator_FetchOrCached_ExpiredCacheEntryRefetches(t *testing.T) {
	t.Parallel()

	var fetchCount atomic.Int32
	srv := serveCIMDDoc(t, "/metadata.json", func() { fetchCount.Add(1) })

	id := cimdURL(srv, "/metadata.json")
	dec := newEnabledDecorator(t, newTestBase(t), 10, 1*time.Millisecond)

	ctx := context.Background()
	_, err := dec.fetchOrCached(ctx, id)
	require.NoError(t, err)

	time.Sleep(10 * time.Millisecond)

	_, err = dec.fetchOrCached(ctx, id)
	require.NoError(t, err)

	assert.Equal(t, int32(2), fetchCount.Load(), "expired cache entry must trigger a re-fetch")
}

// --- GetClient with HTTPS CIMD URLs ---
// Verify that GetClient routes HTTPS client_id values through fetchOrCached by
// pre-populating the cache directly (avoiding real network).

func TestCIMDStorageDecorator_GetClient_CIMDURLHitsCacheDirectly(t *testing.T) {
	t.Parallel()

	base := newTestBase(t)
	dec := newEnabledDecorator(t, base, 10, time.Minute)

	const httpsID = "https://example.com/meta.json"
	fakeClient := &fosite.DefaultClient{ID: httpsID}

	// Pre-populate the cache so no real HTTP fetch is needed.
	dec.cache.Add(httpsID, &cimdCacheEntry{
		client:  fakeClient,
		expires: time.Now().Add(time.Minute),
	})

	got, err := dec.GetClient(context.Background(), httpsID)
	require.NoError(t, err)
	assert.Equal(t, httpsID, got.GetID())
}

// --- buildFositeClient ---

func TestBuildFositeClient_Defaults(t *testing.T) {
	t.Parallel()

	doc := &cimd.ClientMetadataDocument{
		ClientID:     "https://example.com/meta.json",
		RedirectURIs: []string{"https://example.com/callback"},
	}

	got := buildFositeClient(doc)
	assert.Equal(t, "https://example.com/meta.json", got.GetID())
	assert.True(t, got.IsPublic())
	assert.ElementsMatch(t, []string{"authorization_code", "refresh_token"}, []string(got.GetGrantTypes()))
	assert.ElementsMatch(t, []string{"code"}, []string(got.GetResponseTypes()))
}

func TestBuildFositeClient_ExplicitGrantTypes(t *testing.T) {
	t.Parallel()

	doc := &cimd.ClientMetadataDocument{
		ClientID:     "https://example.com/meta.json",
		RedirectURIs: []string{"https://example.com/callback"},
		GrantTypes:   []string{"authorization_code"},
	}

	got := buildFositeClient(doc)
	assert.ElementsMatch(t, []string{"authorization_code"}, []string(got.GetGrantTypes()))
}

func TestBuildFositeClient_ScopeParsing(t *testing.T) {
	t.Parallel()

	doc := &cimd.ClientMetadataDocument{
		ClientID:     "https://example.com/meta.json",
		RedirectURIs: []string{"https://example.com/callback"},
		Scope:        "openid profile email",
	}

	got := buildFositeClient(doc)
	assert.ElementsMatch(t, []string{"openid", "profile", "email"}, []string(got.GetScopes()))
}

func TestBuildFositeClient_LoopbackRedirectWrapsInLoopbackClient(t *testing.T) {
	t.Parallel()

	doc := &cimd.ClientMetadataDocument{
		ClientID:     "https://example.com/meta.json",
		RedirectURIs: []string{"http://localhost/callback"},
	}

	got := buildFositeClient(doc)
	// LoopbackClient adds MatchRedirectURI — check the distinctive method is present.
	type loopbackMatcher interface {
		MatchRedirectURI(string) bool
	}
	_, ok := got.(loopbackMatcher)
	assert.True(t, ok, "loopback redirect URI must produce a LoopbackClient")

	// TokenEndpointAuthMethod must be preserved through the LoopbackClient wrapper.
	oidc, ok := got.(fosite.OpenIDConnectClient)
	require.True(t, ok, "LoopbackClient must implement fosite.OpenIDConnectClient")
	assert.Equal(t, "none", oidc.GetTokenEndpointAuthMethod(),
		"loopback client must preserve TokenEndpointAuthMethod from the OIDC client")
}

func TestBuildFositeClient_NonLoopbackRedirectReturnsOpenIDConnectClient(t *testing.T) {
	t.Parallel()

	doc := &cimd.ClientMetadataDocument{
		ClientID:     "https://example.com/meta.json",
		RedirectURIs: []string{"https://example.com/callback"},
	}

	got := buildFositeClient(doc)
	_, ok := got.(*fosite.DefaultOpenIDConnectClient)
	assert.True(t, ok, "non-loopback redirect URI must produce a DefaultOpenIDConnectClient")
}

func TestBuildFositeClient_TokenEndpointAuthMethodDefault(t *testing.T) {
	t.Parallel()

	doc := &cimd.ClientMetadataDocument{
		ClientID:     "https://example.com/meta.json",
		RedirectURIs: []string{"https://example.com/callback"},
	}

	got := buildFositeClient(doc)
	if oidc, ok := got.(fosite.OpenIDConnectClient); ok {
		assert.Equal(t, "none", oidc.GetTokenEndpointAuthMethod())
	}
}
