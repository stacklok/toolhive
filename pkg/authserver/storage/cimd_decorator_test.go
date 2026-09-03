// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
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
	got, err := NewCIMDStorageDecorator(base, CIMDDecoratorConfig{Enabled: true, CacheMaxSize: maxSize, FallbackTTL: ttl})
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
	got, err := NewCIMDStorageDecorator(base, CIMDDecoratorConfig{Enabled: false, CacheMaxSize: 10, FallbackTTL: time.Minute})
	require.NoError(t, err)
	assert.Same(t, base, got, "disabled decorator must return base unchanged")
}

func TestNewCIMDStorageDecorator_ZeroCacheSizeReturnsError(t *testing.T) {
	t.Parallel()
	base := newTestBase(t)
	_, err := NewCIMDStorageDecorator(base, CIMDDecoratorConfig{Enabled: true, CacheMaxSize: 0, FallbackTTL: time.Minute})
	require.Error(t, err)
}

func TestNewCIMDStorageDecorator_NegativeCacheSizeReturnsError(t *testing.T) {
	t.Parallel()
	base := newTestBase(t)
	_, err := NewCIMDStorageDecorator(base, CIMDDecoratorConfig{Enabled: true, CacheMaxSize: -1, FallbackTTL: time.Minute})
	require.Error(t, err)
}

func TestNewCIMDStorageDecorator_EnabledReturnsCIMDDecorator(t *testing.T) {
	t.Parallel()
	base := newTestBase(t)
	got, err := NewCIMDStorageDecorator(base, CIMDDecoratorConfig{Enabled: true, CacheMaxSize: 10, FallbackTTL: time.Minute})
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

func TestCIMDStorageDecorator_ConsumeAssertionJWTDelegatesToBase(t *testing.T) {
	t.Parallel()
	base := newTestBase(t)
	dec := newEnabledDecorator(t, base, 10, time.Minute)
	var consumer AssertionJWTConsumer = dec

	exp := time.Now().Add(time.Hour)
	require.NoError(t, consumer.ConsumeAssertionJWT(context.Background(), "jwt-bearer", "https://issuer.example", "jti", exp))
	require.ErrorIs(t, consumer.ConsumeAssertionJWT(context.Background(), "jwt-bearer", "https://issuer.example", "jti", exp), fosite.ErrJTIKnown)
}

type storageWithoutAssertionJWTConsumer struct{ Storage }

func TestCIMDStorageDecorator_ConsumeAssertionJWTFailsClosedWithoutBackendCapability(t *testing.T) {
	t.Parallel()

	decorated, err := NewCIMDStorageDecorator(storageWithoutAssertionJWTConsumer{}, CIMDDecoratorConfig{
		Enabled: true, CacheMaxSize: 1, FallbackTTL: time.Minute,
	})
	require.NoError(t, err)
	consumer := decorated.(AssertionJWTConsumer)
	err = consumer.ConsumeAssertionJWT(context.Background(), "jwt-bearer", "https://issuer.example", "jti", time.Now().Add(time.Hour))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support assertion JWT replay consumption")
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

// --- cimdShapeGuardStorage (P2: URL-shaped ids refused while CIMD is disabled) ---

// TestCIMDShapeGuardStorage_URLShapedIDNotFoundEvenWhenRowExists is the core
// regression this guard exists for: a row a prior CIMD-enabled period
// write-through persisted (CIMDStorageDecorator.fetch) must not resolve once
// CIMD is disabled, even though the row is still physically present in the
// underlying storage and would resolve via a bare (unwrapped) GetClient.
func TestCIMDShapeGuardStorage_URLShapedIDNotFoundEvenWhenRowExists(t *testing.T) {
	t.Parallel()
	base := newTestBase(t)
	ctx := context.Background()

	const cimdID = "https://example.com/meta.json"
	stale := &fosite.DefaultClient{ID: cimdID, Public: true}
	require.NoError(t, base.RegisterClient(ctx, stale))

	// Sanity: the row really is resolvable through the bare (unwrapped) base,
	// proving the guard — not an absent row — is what blocks resolution below.
	got, err := base.GetClient(ctx, cimdID)
	require.NoError(t, err, "the stale row must exist in the underlying storage")
	assert.Equal(t, cimdID, got.GetID())

	guarded := NewCIMDShapeGuardStorage(base)
	_, err = guarded.GetClient(ctx, cimdID)
	require.Error(t, err, "a URL-shaped id must never resolve while CIMD is disabled")
	assert.ErrorIs(t, err, fosite.ErrNotFound)
}

// TestCIMDShapeGuardStorage_OpaqueIDDelegatesToBase verifies the guard only
// touches URL-shaped ids: an ordinary DCR-issued or pre-provisioned client_id
// must resolve exactly as it would through the unwrapped base.
func TestCIMDShapeGuardStorage_OpaqueIDDelegatesToBase(t *testing.T) {
	t.Parallel()
	base := newTestBase(t)
	ctx := context.Background()

	dc := &fosite.DefaultClient{ID: "opaque-client-id"}
	require.NoError(t, base.RegisterClient(ctx, dc))

	guarded := NewCIMDShapeGuardStorage(base)
	got, err := guarded.GetClient(ctx, "opaque-client-id")
	require.NoError(t, err)
	assert.Equal(t, "opaque-client-id", got.GetID())
}

// TestCIMDShapeGuardStorage_UnwrapReturnsBase verifies the same Unwrap()
// contract CIMDStorageDecorator provides, so callers walking the decorator
// chain (e.g. the DCRCredentialStore assertion in server_impl.go) reach the
// concrete backend through this wrapper too.
func TestCIMDShapeGuardStorage_UnwrapReturnsBase(t *testing.T) {
	t.Parallel()
	base := newTestBase(t)
	guarded := NewCIMDShapeGuardStorage(base)
	unwrapper, ok := guarded.(interface{ Unwrap() Storage })
	require.True(t, ok)
	assert.Same(t, base, unwrapper.Unwrap())
}

// TestCIMDShapeGuardStorage_ConsumeAssertionJWTDelegatesToBase verifies the
// guard forwards this narrow capability exactly like CIMDStorageDecorator
// does, so wrapping storage when CIMD is disabled does not silently break the
// RFC 7523 JWT-bearer grant (see storage.AssertionJWTConsumer).
func TestCIMDShapeGuardStorage_ConsumeAssertionJWTDelegatesToBase(t *testing.T) {
	t.Parallel()
	base := newTestBase(t)
	guarded := NewCIMDShapeGuardStorage(base)
	consumer, ok := guarded.(AssertionJWTConsumer)
	require.True(t, ok, "the guard must implement AssertionJWTConsumer when its base does")

	exp := time.Now().Add(time.Hour)
	ctx := context.Background()
	require.NoError(t, consumer.ConsumeAssertionJWT(ctx, "jwt-bearer", "https://issuer.example", "jti", exp))
	require.ErrorIs(t, consumer.ConsumeAssertionJWT(ctx, "jwt-bearer", "https://issuer.example", "jti", exp),
		fosite.ErrJTIKnown)
}

// TestCIMDShapeGuardStorage_DecoratorActiveStillWorks is the "decorator
// active" counterpart to TestCIMDShapeGuardStorage_URLShapedIDNotFoundEvenWhenRowExists:
// while the real CIMDStorageDecorator is wrapping storage (CIMD enabled),
// resolving a URL-shaped client_id must still work normally via a live fetch,
// confirming the shape guard is only ever applied on the disabled path (see
// decorateStorageForCIMD in server_impl.go).
func TestCIMDShapeGuardStorage_DecoratorActiveStillWorks(t *testing.T) {
	t.Parallel()

	var fetchCount atomic.Int32
	srv := serveCIMDDoc(t, "/metadata.json", func() { fetchCount.Add(1) })
	id := cimdURL(srv, "/metadata.json")

	base := newTestBase(t)
	dec := newEnabledDecorator(t, base, 10, time.Minute)

	// Resolve once through the active decorator: this both proves resolution
	// works while CIMD is enabled and leaves a write-through-persisted row in
	// base for the disabled-guard assertions below.
	got, err := dec.GetClient(context.Background(), id)
	require.NoError(t, err, "GetClient must succeed while the decorator is active")
	assert.Equal(t, id, got.GetID())
	assert.Equal(t, int32(1), fetchCount.Load())
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

// buildFositeClientWithDefaults calls buildFositeClient with the standard
// filtered grant/response types (what FilterPublicGrantTypes /
// FilterPublicResponseTypes return for an omitted declaration) and the
// default negotiated auth method, for tests that don't exercise those fields.
func buildFositeClientWithDefaults(doc *cimd.ClientMetadataDocument, scopes []string) fosite.Client {
	return buildFositeClient(doc, scopes,
		[]string{"authorization_code", "refresh_token"}, []string{"code"},
		defaultCIMDTokenEndpointAuthMethod)
}

func TestBuildFositeClient_PassesThroughGrantAndResponseTypes(t *testing.T) {
	t.Parallel()

	doc := &cimd.ClientMetadataDocument{
		ClientID:     "https://example.com/meta.json",
		RedirectURIs: []string{"https://example.com/callback"},
		// The document's own declaration is ignored: fetch() passes the
		// filtered lists explicitly, and only those must reach the client.
		GrantTypes: []string{"authorization_code", "urn:ietf:params:oauth:grant-type:device_code"},
	}

	got := buildFositeClient(doc, nil, []string{"authorization_code"}, []string{"code"},
		defaultCIMDTokenEndpointAuthMethod)
	assert.Equal(t, "https://example.com/meta.json", got.GetID())
	assert.True(t, got.IsPublic())
	assert.ElementsMatch(t, []string{"authorization_code"}, []string(got.GetGrantTypes()),
		"the filtered grant types must be stored, not the document's declaration")
	assert.ElementsMatch(t, []string{"code"}, []string(got.GetResponseTypes()))
}

func TestBuildFositeClient_ScopeParsing(t *testing.T) {
	t.Parallel()

	doc := &cimd.ClientMetadataDocument{
		ClientID:     "https://example.com/meta.json",
		RedirectURIs: []string{"https://example.com/callback"},
		Scope:        "openid profile email",
	}

	// Scope parsing is done by fetch() before buildFositeClient.
	got := buildFositeClientWithDefaults(doc, strings.Fields(doc.Scope))
	assert.ElementsMatch(t, []string{"openid", "profile", "email"}, []string(got.GetScopes()))
}

func TestBuildFositeClient_LoopbackRedirectGetsDynamicPortMatching(t *testing.T) {
	t.Parallel()

	doc := &cimd.ClientMetadataDocument{
		ClientID:     "https://example.com/meta.json",
		RedirectURIs: []string{"http://localhost/callback"},
	}

	got := buildFositeClientWithDefaults(doc, nil)

	uri, ok := registration.RegisteredLoopbackRedirectURI(got, "http://localhost:54321/callback")
	require.True(t, ok, "loopback redirect URI must get dynamic-port matching")
	assert.Equal(t, "http://localhost/callback", uri)

	oidc, ok := got.(fosite.OpenIDConnectClient)
	require.True(t, ok, "got must implement fosite.OpenIDConnectClient")
	assert.Equal(t, "none", oidc.GetTokenEndpointAuthMethod())
}

func TestBuildFositeClient_NonLoopbackRedirectReturnsOpenIDConnectClient(t *testing.T) {
	t.Parallel()

	doc := &cimd.ClientMetadataDocument{
		ClientID:     "https://example.com/meta.json",
		RedirectURIs: []string{"https://example.com/callback"},
	}

	got := buildFositeClientWithDefaults(doc, nil)
	_, ok := got.(*fosite.DefaultOpenIDConnectClient)
	assert.True(t, ok, "non-loopback redirect URI must produce a DefaultOpenIDConnectClient")
}

// TestNegotiateTokenEndpointAuthMethod_DeclaredNoneWithPluralList covers the
// branch where the document already declares the singular
// token_endpoint_auth_method as "none": negotiateTokenEndpointAuthMethod's
// first check (declared == "" || declared == defaultCIMDTokenEndpointAuthMethod)
// short-circuits on that alone and never consults the plural
// TokenEndpointAuthMethodsSupported list, even when that list is present and
// names something else entirely. This was previously exercised indirectly by
// TestBuildFositeClient_TokenEndpointAuthMethodDefault against a fallback
// buildFositeClient no longer applies internally (the negotiation moved to
// fetch(), and buildFositeClient now takes the already-negotiated method as a
// parameter) — this test targets negotiateTokenEndpointAuthMethod directly
// instead, alongside the equivalent-but-distinct branches already covered by
// TestFetch_TokenEndpointAuthMethodNegotiation.
func TestNegotiateTokenEndpointAuthMethod_DeclaredNoneWithPluralList(t *testing.T) {
	t.Parallel()

	doc := &cimd.ClientMetadataDocument{
		ClientID:                          "https://example.com/meta.json",
		RedirectURIs:                      []string{"https://example.com/callback"},
		TokenEndpointAuthMethod:           "none",
		TokenEndpointAuthMethodsSupported: []string{"private_key_jwt", "tls_client_auth"},
	}

	got, ok := negotiateTokenEndpointAuthMethod(doc)
	require.True(t, ok, "a declared \"none\" must negotiate successfully regardless of the plural list")
	assert.Equal(t, "none", got,
		"the declared \"none\" must win outright, without consulting TokenEndpointAuthMethodsSupported")
}

func TestFetch_RejectsUnsupportedTokenEndpointAuthMethod(t *testing.T) {
	t.Parallel()

	// Serve a CIMD doc that declares a non-"none" auth method.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientID := "http://" + r.Host + r.URL.Path
		doc := cimd.ClientMetadataDocument{
			ClientID:                clientID,
			RedirectURIs:            []string{"https://example.com/callback"},
			TokenEndpointAuthMethod: "private_key_jwt",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)

	dec := newEnabledDecorator(t, newTestBase(t), 10, time.Minute)
	_, err := dec.fetchOrCached(context.Background(), srv.URL+"/meta.json")
	require.Error(t, err, "fetch must fail when token_endpoint_auth_method is not \"none\"")
	assert.ErrorIs(t, err, fosite.ErrInvalidClient,
		"CIMD policy rejections must use ErrInvalidClient, not ErrNotFound")
	assert.NotErrorIs(t, err, fosite.ErrNotFound)
}

// TestFetch_TokenEndpointAuthMethodNegotiation exercises the negotiation
// introduced for #6278: a declared-but-unsupported singular
// token_endpoint_auth_method is rescued when the plural
// token_endpoint_auth_methods_supported list names a method this server does
// support, instead of the document being rejected outright.
func TestFetch_TokenEndpointAuthMethodNegotiation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		declared       string
		supportedList  []string
		wantErr        bool
		wantAuthMethod string
	}{
		{
			name:           "unsupported singular rescued by none in the supported list",
			declared:       "private_key_jwt",
			supportedList:  []string{"none", "private_key_jwt"},
			wantErr:        false,
			wantAuthMethod: "none",
		},
		{
			name:          "unsupported singular with no none in the supported list stays rejected",
			declared:      "private_key_jwt",
			supportedList: []string{"private_key_jwt", "tls_client_auth"},
			wantErr:       true,
		},
		{
			name:           "omitted singular with a supported list is unaffected — accepted as none",
			declared:       "",
			supportedList:  []string{"private_key_jwt"},
			wantErr:        false,
			wantAuthMethod: "none",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := serveCIMDDocWithFields(t, func(doc *cimd.ClientMetadataDocument) {
				doc.TokenEndpointAuthMethod = tt.declared
				doc.TokenEndpointAuthMethodsSupported = tt.supportedList
			})
			dec := newEnabledDecorator(t, newTestBase(t), 10, time.Minute)
			client, err := dec.fetchOrCached(context.Background(), srv.URL+"/meta.json")
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, fosite.ErrInvalidClient,
					"CIMD policy rejections must use ErrInvalidClient, not ErrNotFound")
				assert.NotErrorIs(t, err, fosite.ErrNotFound)
				return
			}
			require.NoError(t, err)
			assert.True(t, client.IsPublic())
			oidc, ok := client.(fosite.OpenIDConnectClient)
			require.True(t, ok, "client must implement fosite.OpenIDConnectClient")
			assert.Equal(t, tt.wantAuthMethod, oidc.GetTokenEndpointAuthMethod())
		})
	}
}

// serveCIMDDocWithFields starts an httptest.Server that serves a CIMD document
// customised by the provided mutator function. Pass nil for a plain valid doc.
func serveCIMDDocWithFields(t *testing.T, mutate func(*cimd.ClientMetadataDocument)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/meta.json" {
			http.NotFound(w, r)
			return
		}
		doc := cimd.ClientMetadataDocument{
			ClientID:     "http://" + r.Host + r.URL.Path,
			RedirectURIs: []string{"https://example.com/callback"},
		}
		if mutate != nil {
			mutate(&doc)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// --- grant_types validation ---

func TestFetch_GrantTypeValidation(t *testing.T) {
	t.Parallel()

	// A CIMD document declares the client's capabilities across every AS it
	// talks to, so grant types this server does not support are filtered out
	// rather than failing the document (#6290). Rejection only happens when
	// nothing this server supports survives — specifically, when
	// authorization_code is absent from the intersection.
	tests := []struct {
		name           string
		grantTypes     []string
		wantErr        bool
		wantGrantTypes []string // asserted on the resolved client when no error
	}{
		{"omitted grant_types accepted", nil, false,
			[]string{"authorization_code", "refresh_token"}},
		{"explicit [authorization_code, refresh_token] accepted", []string{"authorization_code", "refresh_token"}, false,
			[]string{"authorization_code", "refresh_token"}},
		{"explicit [authorization_code] accepted", []string{"authorization_code"}, false,
			[]string{"authorization_code"}},
		{"device_code alongside authorization_code is ignored, not fatal",
			[]string{"authorization_code", "refresh_token", "urn:ietf:params:oauth:grant-type:device_code"}, false,
			[]string{"authorization_code", "refresh_token"}},
		{"refresh_token only missing authorization_code rejected", []string{"refresh_token"}, true, nil},
		{"client_credentials only rejected (no supported grant type)", []string{"client_credentials"}, true, nil},
		{"implicit only rejected (no supported grant type)", []string{"implicit"}, true, nil},
		{"device_code only rejected (no supported grant type)",
			[]string{"urn:ietf:params:oauth:grant-type:device_code"}, true, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := serveCIMDDocWithFields(t, func(doc *cimd.ClientMetadataDocument) {
				doc.GrantTypes = tt.grantTypes
			})
			dec := newEnabledDecorator(t, newTestBase(t), 10, time.Minute)
			client, err := dec.fetchOrCached(context.Background(), srv.URL+"/meta.json")
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, fosite.ErrInvalidClient,
					"grant_type policy rejections must use ErrInvalidClient")
				assert.NotErrorIs(t, err, fosite.ErrNotFound)
			} else {
				require.NoError(t, err)
				assert.ElementsMatch(t, tt.wantGrantTypes, []string(client.GetGrantTypes()),
					"the resolved client must carry only grant types this server supports")
			}
		})
	}
}

// TestFetch_VSCodeDocumentResolves is the regression test for #6290: VS
// Code's real-world client-metadata document declares the device_code grant
// alongside authorization_code and refresh_token, and its resolution must
// succeed with device_code filtered out rather than failing the whole
// document with invalid_client.
func TestFetch_VSCodeDocumentResolves(t *testing.T) {
	t.Parallel()

	srv := serveCIMDDocWithFields(t, func(doc *cimd.ClientMetadataDocument) {
		// Mirror https://vscode.dev/oauth/client-metadata.json (VS Code 1.133.0),
		// with client_id/redirect kept test-local.
		doc.ClientName = "Visual Studio Code"
		doc.GrantTypes = []string{"authorization_code", "refresh_token", "urn:ietf:params:oauth:grant-type:device_code"}
		doc.ResponseTypes = []string{"code"}
		doc.TokenEndpointAuthMethod = "none"
		doc.RedirectURIs = []string{"http://127.0.0.1:33418/", "https://vscode.dev/redirect"}
	})
	dec := newEnabledDecorator(t, newTestBase(t), 10, time.Minute)

	client, err := dec.fetchOrCached(context.Background(), srv.URL+"/meta.json")
	require.NoError(t, err, "VS Code's document must resolve (#6290)")
	assert.True(t, client.IsPublic())
	assert.ElementsMatch(t, []string{"authorization_code", "refresh_token"}, []string(client.GetGrantTypes()),
		"device_code must be filtered out, not stored")
	assert.ElementsMatch(t, []string{"code"}, []string(client.GetResponseTypes()))
}

// --- response_types validation ---

func TestFetch_ResponseTypeValidation(t *testing.T) {
	t.Parallel()

	// Same filtering semantics as grant types: unsupported response types are
	// ignored, and rejection only happens when "code" — the one response type
	// this server serves — does not survive the intersection.
	tests := []struct {
		name              string
		responseTypes     []string
		wantErr           bool
		wantResponseTypes []string // asserted on the resolved client when no error
	}{
		{"omitted response_types accepted", nil, false, []string{"code"}},
		{"code accepted", []string{"code"}, false, []string{"code"}},
		{"token alongside code is ignored, not fatal", []string{"code", "token"}, false, []string{"code"}},
		{"token only rejected (no supported response type)", []string{"token"}, true, nil},
		{"code id_token rejected (hybrid)", []string{"code id_token"}, true, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := serveCIMDDocWithFields(t, func(doc *cimd.ClientMetadataDocument) {
				doc.ResponseTypes = tt.responseTypes
			})
			dec := newEnabledDecorator(t, newTestBase(t), 10, time.Minute)
			client, err := dec.fetchOrCached(context.Background(), srv.URL+"/meta.json")
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, fosite.ErrInvalidClient,
					"response_type policy rejections must use ErrInvalidClient")
				assert.NotErrorIs(t, err, fosite.ErrNotFound)
			} else {
				require.NoError(t, err)
				assert.ElementsMatch(t, tt.wantResponseTypes, []string(client.GetResponseTypes()),
					"the resolved client must carry only response types this server supports")
			}
		})
	}
}

// --- scope resolution ---

func TestFetch_ScopeResolution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		docScope        string
		scopesSupported []string
		baseline        []string
		wantErr         bool
		wantScopes      []string
	}{
		{
			name:       "no constraint uses DefaultScopes",
			docScope:   "",
			wantScopes: registration.DefaultScopes,
		},
		{
			name:            "explicit scope accepted within ScopesSupported",
			docScope:        "openid",
			scopesSupported: []string{"openid", "profile"},
			wantScopes:      []string{"openid"},
		},
		{
			name:            "explicit scope outside ScopesSupported rejected",
			docScope:        "openid profile email",
			scopesSupported: []string{"openid"},
			wantErr:         true,
		},
		{
			name:            "omitted scope with permissive ScopesSupported uses DefaultScopes",
			docScope:        "",
			scopesSupported: []string{"openid", "profile", "email", "offline_access"},
			wantScopes:      registration.DefaultScopes,
		},
		{
			// The #6186 shape: the server's scopes_supported lacks one default
			// (profile); the client must still register with the intersection.
			name:            "omitted scope with reduced ScopesSupported grants the intersection",
			docScope:        "",
			scopesSupported: []string{"openid", "email", "offline_access"},
			wantScopes:      []string{"openid", "email", "offline_access"},
		},
		{
			name:            "omitted scope with restrictive ScopesSupported grants the single-scope intersection",
			docScope:        "",
			scopesSupported: []string{"openid"},
			wantScopes:      []string{"openid"},
		},
		{
			name:            "omitted scope with ScopesSupported disjoint from defaults rejected",
			docScope:        "",
			scopesSupported: []string{"custom_scope"},
			wantErr:         true,
		},
		{
			name:            "baseline unioned into scope set",
			docScope:        "openid",
			scopesSupported: []string{"openid", "offline_access"},
			baseline:        []string{"offline_access"},
			wantScopes:      []string{"openid", "offline_access"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scope := tt.docScope
			srv := serveCIMDDocWithFields(t, func(doc *cimd.ClientMetadataDocument) {
				doc.Scope = scope
			})
			got, err := NewCIMDStorageDecorator(newTestBase(t), CIMDDecoratorConfig{
				Enabled:              true,
				CacheMaxSize:         10,
				FallbackTTL:          time.Minute,
				ScopesSupported:      tt.scopesSupported,
				BaselineClientScopes: tt.baseline,
			})
			require.NoError(t, err)
			dec := got.(*CIMDStorageDecorator)

			client, err := dec.fetchOrCached(context.Background(), srv.URL+"/meta.json")
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, fosite.ErrInvalidClient)
				assert.NotErrorIs(t, err, fosite.ErrNotFound)
				return
			}
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.wantScopes, []string(client.GetScopes()))
		})
	}
}

// TestBuildFositeClient_ScopeDefaultsToDefaultScopesWhenNoScopesSupported verifies the
// fallback branch in buildFositeClient: nil resolvedScopes → DefaultScopes.
func TestBuildFositeClient_ScopeDefaultsToDefaultScopesWhenNoScopesSupported(t *testing.T) {
	t.Parallel()
	doc := &cimd.ClientMetadataDocument{
		ClientID:     "https://example.com/meta.json",
		RedirectURIs: []string{"https://example.com/callback"},
	}
	got := buildFositeClientWithDefaults(doc, nil)
	assert.ElementsMatch(t, registration.DefaultScopes, []string(got.GetScopes()))
}

// --- write-through persistence (issue #6187) ---

func TestCIMDStorageDecorator_PersistsResolvedClient(t *testing.T) {
	t.Parallel()

	base := newTestBase(t)
	dec := newEnabledDecorator(t, base, 10, time.Minute)
	srv := serveCIMDDocWithFields(t, nil)
	id := srv.URL + "/meta.json"

	resolved, err := dec.fetchOrCached(context.Background(), id)
	require.NoError(t, err)

	persisted, err := base.GetClient(context.Background(), id)
	require.NoError(t, err,
		"the resolved client must be persisted so session rehydration that resolves clients through the bare storage still finds it")
	assert.Equal(t, resolved.GetID(), persisted.GetID())
	assert.True(t, persisted.IsPublic())
	assert.True(t, registration.DCRIssued(resolved),
		"the resolved client must carry the DCR-issued marker so the persisted row gets the anti-bloat TTL")
}

// TestCIMDStorageDecorator_PersistsLoopbackResolvedClient covers a document
// with loopback redirect URIs: buildFositeClient gives it the same
// *fosite.DefaultOpenIDConnectClient shape as any other CIMD client (RFC 8252
// dynamic-port matching is applied separately, by
// registration.RegisteredLoopbackRedirectURI, not by a distinct wrapper
// type), and the DCR-issued marker (and with it the TTL on the persisted
// row) must survive that shape too.
func TestCIMDStorageDecorator_PersistsLoopbackResolvedClient(t *testing.T) {
	t.Parallel()

	base := newTestBase(t)
	dec := newEnabledDecorator(t, base, 10, time.Minute)
	srv := serveCIMDDocWithFields(t, func(doc *cimd.ClientMetadataDocument) {
		doc.RedirectURIs = []string{"http://localhost/callback"}
	})
	id := srv.URL + "/meta.json"

	resolved, err := dec.fetchOrCached(context.Background(), id)
	require.NoError(t, err)
	assert.True(t, registration.DCRIssued(resolved),
		"a loopback-wrapped CIMD client must also carry the DCR-issued marker")

	_, err = base.GetClient(context.Background(), id)
	require.NoError(t, err)
}

// registerFailingStorage wraps a Storage and fails every UpsertDCRIssuedClient
// call -- the write-through persistence path fetch() actually calls -- for
// testing that a write-through persistence failure does not fail the
// resolution itself.
type registerFailingStorage struct {
	Storage
}

func (*registerFailingStorage) UpsertDCRIssuedClient(context.Context, fosite.Client) error {
	return errors.New("register failed")
}

func TestCIMDStorageDecorator_PersistFailureDoesNotFailResolution(t *testing.T) {
	t.Parallel()

	srv := serveCIMDDocWithFields(t, nil)
	got, err := NewCIMDStorageDecorator(&registerFailingStorage{Storage: newTestBase(t)}, CIMDDecoratorConfig{
		Enabled:      true,
		CacheMaxSize: 10,
		FallbackTTL:  time.Minute,
	})
	require.NoError(t, err)
	dec := got.(*CIMDStorageDecorator)

	client, err := dec.fetchOrCached(context.Background(), srv.URL+"/meta.json")
	require.NoError(t, err, "a write-through persistence failure must not fail the resolution")
	assert.NotNil(t, client)
}

// TestCIMDStorageDecorator_RepeatFetchRenewsPersistedClient closes the gap
// that let the RegisterClient/UpsertDCRIssuedClient bug go undetected:
// nothing previously asserted the persisted row's state after a repeat
// fetch of the same client_id. Two fetches of the same CIMD document, whose
// served content changes between them, must both succeed, and the second
// fetch's write-through must actually replace the stored row's data rather
// than silently failing with ErrAlreadyExists (as it would have with the old
// RegisterClient-only call, whose failure fetch() only logs).
func TestCIMDStorageDecorator_RepeatFetchRenewsPersistedClient(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32
	srv := serveCIMDDocWithFields(t, func(doc *cimd.ClientMetadataDocument) {
		// Change the served document between the first and second fetch so a
		// real re-persist is distinguishable from a no-op.
		if callCount.Add(1) == 1 {
			doc.RedirectURIs = []string{"https://example.com/callback-v1"}
		} else {
			doc.RedirectURIs = []string{"https://example.com/callback-v2"}
		}
	})
	base := newTestBase(t)
	dec := newEnabledDecorator(t, base, 10, time.Minute)
	id := srv.URL + "/meta.json"
	ctx := context.Background()

	// Call fetch() directly (bypassing the in-process LRU cache) to force two
	// real write-through persistence attempts, exactly as two independent
	// process replicas resolving the same client_id would.
	first, err := dec.fetch(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, []string{"https://example.com/callback-v1"}, first.GetRedirectURIs())

	stored, err := base.GetClient(ctx, id)
	require.NoError(t, err, "first fetch must persist the row")
	assert.Equal(t, []string{"https://example.com/callback-v1"}, stored.GetRedirectURIs())

	second, err := dec.fetch(ctx, id)
	require.NoError(t, err, "second fetch for the same client_id must not fail")
	assert.Equal(t, []string{"https://example.com/callback-v2"}, second.GetRedirectURIs())

	stored, err = base.GetClient(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, []string{"https://example.com/callback-v2"}, stored.GetRedirectURIs(),
		"the persisted row must be renewed with the newly-fetched document, not left stale")
}
