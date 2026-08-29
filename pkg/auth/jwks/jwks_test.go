// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package jwks

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newECKey generates a fresh ECDSA P-256 key and imports its public half as a
// jwk.Key carrying the given key ID.
func newECKey(t *testing.T, kid string) (jwk.Key, *ecdsa.PrivateKey) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	key, err := jwk.Import(&priv.PublicKey)
	require.NoError(t, err)
	require.NoError(t, key.Set(jwk.KeyIDKey, kid))
	require.NoError(t, key.Set(jwk.AlgorithmKey, "ES256"))
	require.NoError(t, key.Set(jwk.KeyUsageKey, "sig"))
	return key, priv
}

// jwksDoc serialises the given public keys into a JWKS JSON document.
func jwksDoc(t *testing.T, keys ...jwk.Key) []byte {
	t.Helper()

	set := jwk.NewSet()
	for _, key := range keys {
		require.NoError(t, set.AddKey(key))
	}
	raw, err := json.Marshal(set)
	require.NoError(t, err)
	return raw
}

// mutableJWKSServer is a JWKS endpoint whose payload and status can be changed
// mid-test, and which counts how many times it was fetched.
type mutableJWKSServer struct {
	*httptest.Server

	hits   atomic.Int32
	mu     sync.Mutex
	body   []byte
	status int
}

func newMutableJWKSServer(t *testing.T, initial []byte) *mutableJWKSServer {
	t.Helper()

	s := &mutableJWKSServer{body: initial, status: http.StatusOK}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.hits.Add(1)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.status != http.StatusOK {
			w.WriteHeader(s.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(s.body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *mutableJWKSServer) set(body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.body = body
	s.status = http.StatusOK
}

func (s *mutableJWKSServer) setStatus(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

// newShortFetcher builds a Fetcher with fast timeouts so tests never block on
// the production-sized registration budget or backoff windows.
func newShortFetcher(t *testing.T, opts ...Option) *Fetcher {
	t.Helper()

	base := []Option{
		WithInsecureAllowHTTP(true),
		WithAllowPrivateIPs(true),
		WithRegistrationTimeout(2 * time.Second),
	}
	opts = append(base, opts...)
	f, err := NewFetcher(context.Background(), opts...)
	require.NoError(t, err)
	return f
}

// TestFetcher_LookupResolvesKidAndRefreshesOnMiss proves the core Lookup seam:
// a kid present in the cached set resolves without a refetch, a kid the cache
// has never seen forces one rate-limited refresh, and — once the gate has
// elapsed — another unknown kid may trigger exactly one more refresh.
func TestFetcher_LookupResolvesKidAndRefreshesOnMiss(t *testing.T) {
	t.Parallel()

	key1, _ := newECKey(t, "k1")
	key2, _ := newECKey(t, "k2")
	srv := newMutableJWKSServer(t, jwksDoc(t, key1))

	f := newShortFetcher(t, WithMinKidRefreshInterval(150*time.Millisecond))

	// First lookup registers and fetches.
	_, err := f.Lookup(context.Background(), srv.URL, "k1")
	require.NoError(t, err)
	require.Equal(t, int32(1), srv.hits.Load(), "the first lookup must fetch exactly once")

	// Cache hit: no new fetch.
	_, err = f.Lookup(context.Background(), srv.URL, "k1")
	require.NoError(t, err)
	require.Equal(t, int32(1), srv.hits.Load(), "a cached kid must not trigger a refetch")

	// Rotate: kid2 is unknown, so exactly one refresh is forced and it must
	// surface the new key.
	srv.set(jwksDoc(t, key2))
	_, err = f.Lookup(context.Background(), srv.URL, "k2")
	require.NoError(t, err, "an unknown kid must trigger a refresh and resolve after rotation")
	require.Equal(t, int32(2), srv.hits.Load())

	// Within the (short) gate a third kid must NOT force another refresh...
	key3, _ := newECKey(t, "k3")
	srv.set(jwksDoc(t, key3))
	_, err = f.Lookup(context.Background(), srv.URL, "k3")
	require.Error(t, err, "a kid refresh within the rate-limit gate must not happen")
	require.Equal(t, int32(2), srv.hits.Load(), "no fetch may occur while the kid-refresh gate is closed")

	// ...but after the gate elapses, the unknown kid resolves.
	time.Sleep(250 * time.Millisecond)
	_, err = f.Lookup(context.Background(), srv.URL, "k3")
	require.NoError(t, err)
	require.Equal(t, int32(3), srv.hits.Load())
}

// TestFetcher_UnknownKidRefreshRateLimitedByDefault proves the DEFAULT
// minKidRefreshInterval (30s) closes the refresh gate immediately after one
// forced refresh, so a client spamming made-up kids cannot drive a fetch per
// request.
func TestFetcher_UnknownKidRefreshRateLimitedByDefault(t *testing.T) {
	t.Parallel()

	key1, _ := newECKey(t, "k1")
	srv := newMutableJWKSServer(t, jwksDoc(t, key1))

	f := newShortFetcher(t)

	_, err := f.Lookup(context.Background(), srv.URL, "k1")
	require.NoError(t, err)
	require.Equal(t, int32(1), srv.hits.Load())

	// First miss forces the one allowed refresh; the key still isn't there.
	_, err = f.Lookup(context.Background(), srv.URL, "made-up-kid")
	require.Error(t, err)
	require.Equal(t, int32(2), srv.hits.Load())

	// Subsequent misses within the default 30s gate must not fetch again.
	for range 3 {
		_, err = f.Lookup(context.Background(), srv.URL, "made-up-kid")
		require.Error(t, err)
	}
	require.Equal(t, int32(2), srv.hits.Load(),
		"the default kid-refresh gate must prevent a second forced fetch within 30s")
}

// TestFetcher_StaleOnError proves stale-on-error comes free from httprc/jwx
// semantics: once a set has been fetched, a later failing refresh (here a 500
// on the endpoint) must NOT evict it — the previously stored keys keep
// validating.
func TestFetcher_StaleOnError(t *testing.T) {
	t.Parallel()

	key1, _ := newECKey(t, "k1")
	srv := newMutableJWKSServer(t, jwksDoc(t, key1))

	f := newShortFetcher(t)

	_, err := f.Lookup(context.Background(), srv.URL, "k1")
	require.NoError(t, err)

	srv.setStatus(http.StatusInternalServerError)

	_, err = f.Lookup(context.Background(), srv.URL, "k1")
	require.NoError(t, err, "a failed refresh must not evict the previously fetched key set")

	set, err := f.KeySet(context.Background(), srv.URL)
	require.NoError(t, err)
	_, found := set.LookupKeyID("k1")
	assert.True(t, found, "KeySet must keep serving the stale-but-valid set")
}

// TestFetcher_FetchFailureBackoffGatesRetries proves the fetch-failure backoff:
// before the first successful fetch, repeated EnsureRegistered calls must
// replay the stored error instead of hitting the endpoint again; after the
// backoff elapses and the endpoint recovers, registration succeeds through the
// Refresh path (the resource was already registered by the failed attempt).
func TestFetcher_FetchFailureBackoffGatesRetries(t *testing.T) {
	t.Parallel()

	key1, _ := newECKey(t, "k1")
	srv := newMutableJWKSServer(t, jwksDoc(t, key1))
	srv.setStatus(http.StatusInternalServerError)

	f := newShortFetcher(t, WithFetchFailureBackoff(400*time.Millisecond), WithRegistrationTimeout(300*time.Millisecond))

	ctx := context.Background()

	// First attempt genuinely fetches (and fails).
	err := f.EnsureRegistered(ctx, srv.URL)
	require.Error(t, err)
	require.Equal(t, int32(1), srv.hits.Load())

	// Within the backoff window the stored error is replayed, no new fetch.
	errReplay := f.EnsureRegistered(ctx, srv.URL)
	require.Error(t, errReplay)
	assert.Contains(t, errReplay.Error(), "context deadline exceeded",
		"the stored fetch error must be replayed, not retried")
	require.Equal(t, int32(1), srv.hits.Load(),
		"a never-successfully-fetched endpoint must not be re-fetched within the backoff window")

	// After the backoff elapses, the retry goes through Refresh (the resource
	// is registered) and succeeds now that the endpoint recovered.
	time.Sleep(600 * time.Millisecond)
	srv.set(jwksDoc(t, key1))
	require.NoError(t, f.EnsureRegistered(ctx, srv.URL))
	_, err = f.Lookup(ctx, srv.URL, "k1")
	require.NoError(t, err)
	require.Equal(t, int32(2), srv.hits.Load())
}

// TestFetcher_FailedRefreshOnUnknownKidPreservesStaleSet proves the stale-set
// invariant under a failed refresh-on-unknown-kid: when the forced re-fetch
// Lookup issues for an unknown kid fails (endpoint erroring), the previously
// fetched key set must survive — the kid that was already known keeps
// resolving from cache, with no additional fetch.
func TestFetcher_FailedRefreshOnUnknownKidPreservesStaleSet(t *testing.T) {
	t.Parallel()

	key1, _ := newECKey(t, "k1")
	srv := newMutableJWKSServer(t, jwksDoc(t, key1))

	f := newShortFetcher(t, WithMinKidRefreshInterval(time.Millisecond))

	ctx := context.Background()

	// First lookup registers, fetches, and resolves k1.
	_, err := f.Lookup(ctx, srv.URL, "k1")
	require.NoError(t, err)
	require.Equal(t, int32(1), srv.hits.Load())

	// The endpoint starts failing; an unknown kid forces RefreshOnUnknownKid,
	// whose re-fetch hits the 500 and fails. The lookup for the unknown kid
	// must still fail, and must have genuinely attempted the refresh.
	srv.setStatus(http.StatusInternalServerError)
	_, err = f.Lookup(ctx, srv.URL, "k2")
	require.Error(t, err, "an unknown kid whose forced refresh failed must not resolve")
	require.Equal(t, int32(2), srv.hits.Load(),
		"the unknown-kid lookup must have attempted exactly one refresh")

	// The failed refresh must not have evicted the stale set: k1 still
	// resolves, served from cache without a new fetch.
	hitsBeforeStaleLookup := srv.hits.Load()
	_, err = f.Lookup(ctx, srv.URL, "k1")
	require.NoError(t, err, "the stale cached key set must survive a failed unknown-kid refresh")
	require.Equal(t, hitsBeforeStaleLookup, srv.hits.Load(),
		"the stale k1 lookup must be served from cache without a new fetch")
}

// TestFetcher_BodyLimit proves the default 1 MiB response-body cap: a JWKS
// response larger than the cap can never be fully read, so the fetch never
// succeeds and Lookup fails.
func TestFetcher_BodyLimit(t *testing.T) {
	t.Parallel()

	key1, _ := newECKey(t, "k1")
	valid := jwksDoc(t, key1)

	// A well-formed JWKS padded past the cap with an unrelated field: it would
	// parse fine if read in full, so only the cap cutting the read short makes
	// the fetch fail.
	var doc map[string]any
	require.NoError(t, json.Unmarshal(valid, &doc))
	doc["padding"] = strings.Repeat("a", int(DefaultBodyLimit)+1024)
	oversized, err := json.Marshal(doc)
	require.NoError(t, err)

	srv := newMutableJWKSServer(t, oversized)

	f := newShortFetcher(t, WithRegistrationTimeout(300*time.Millisecond))

	_, err = f.Lookup(context.Background(), srv.URL, "k1")
	require.Error(t, err, "a JWKS response exceeding the body cap must fail to fetch")
	require.Equal(t, int32(1), srv.hits.Load())
}

// TestLimitedBodyTransport asserts directly on the body cap transport. jwx
// surfaces every fetch failure as its own ready-wait timeout, so the cap's
// error never reaches a Fetcher caller and cannot be distinguished there from
// a 500 or a parse failure — asserting on the transport itself is the only way
// to pin that reading past the cap errors rather than truncating silently.
func TestLimitedBodyTransport(t *testing.T) {
	t.Parallel()

	const bodyCap = 1024

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 8*1024)))
	}))
	t.Cleanup(srv.Close)

	client := srv.Client()
	client.Transport = &limitedBodyTransport{base: client.Transport, max: bodyCap}

	resp, err := client.Get(srv.URL)
	require.NoError(t, err, "the cap applies to reading the body, not to the round trip")
	t.Cleanup(func() { _ = resp.Body.Close() })

	body, err := io.ReadAll(resp.Body)
	require.Error(t, err, "reading past the cap must fail rather than truncate silently: "+
		"a truncated JWKS would be parsed as though it were the whole document")
	var maxBytesErr *http.MaxBytesError
	require.ErrorAs(t, err, &maxBytesErr, "the error must surface the body limit")
	assert.LessOrEqual(t, int64(len(body)), int64(bodyCap),
		"no more than the cap may be delivered before the error")
}

// TestFetcher_PerInstanceFlagIsolation proves the package's core invariant:
// each Fetcher owns its own jwk.Cache and HTTP client, so configuring one
// Fetcher never leaks into another. Two Fetchers point at the SAME plain-HTTP
// JWKS URL: the permissive one fetches and succeeds; the strict one (which
// forbids plain HTTP) is rejected by its own URL policy without ever hitting
// the endpoint, and without disturbing the permissive Fetcher's cache.
func TestFetcher_PerInstanceFlagIsolation(t *testing.T) {
	t.Parallel()

	key1, _ := newECKey(t, "k1")
	srv := newMutableJWKSServer(t, jwksDoc(t, key1))

	permissive := newShortFetcher(t)

	// Deliberately NOT newShortFetcher: the strict Fetcher must keep the
	// default HTTPS-only policy. It still allows private IPs so the URL
	// policy — not the dial guard — is what rejects it.
	strict, err := NewFetcher(context.Background(),
		WithAllowPrivateIPs(true), WithRegistrationTimeout(2*time.Second))
	require.NoError(t, err)

	ctx := context.Background()

	_, err = permissive.Lookup(ctx, srv.URL, "k1")
	require.NoError(t, err, "the Fetcher permitting HTTP must succeed against the http:// endpoint")
	require.Equal(t, int32(1), srv.hits.Load())

	_, err = strict.Lookup(ctx, srv.URL, "k1")
	require.Error(t, err, "the Fetcher forbidding plain HTTP must be rejected for its own policy")
	assert.Contains(t, err.Error(), "must use HTTPS",
		"the rejection must come from the strict Fetcher's own URL policy")
	require.Equal(t, int32(1), srv.hits.Load(),
		"the strict Fetcher must fail policy validation before any fetch")

	// The strict Fetcher's failed attempt must not have disturbed the
	// permissive Fetcher's registered cache: a further lookup is still a
	// cache hit.
	_, err = permissive.Lookup(ctx, srv.URL, "k1")
	require.NoError(t, err)
	require.Equal(t, int32(1), srv.hits.Load())
}

// TestFetcher_CachesArePerInstance proves two Fetchers configured identically
// and pointed at the same URL each maintain their own cache — both fetch
// independently (2 total hits), never sharing a registration.
func TestFetcher_CachesArePerInstance(t *testing.T) {
	t.Parallel()

	key1, _ := newECKey(t, "k1")
	srv := newMutableJWKSServer(t, jwksDoc(t, key1))

	fa := newShortFetcher(t)
	fb := newShortFetcher(t)

	ctx := context.Background()
	_, err := fa.Lookup(ctx, srv.URL, "k1")
	require.NoError(t, err)
	_, err = fb.Lookup(ctx, srv.URL, "k1")
	require.NoError(t, err)
	require.Equal(t, int32(2), srv.hits.Load(),
		"each Fetcher must fetch through its own cache, not share one registration")
}

// TestFetcher_MaxKeysCapsKeyCount proves the too-many-keys guard: a JWKS
// serving more than the default maximum is rejected rather than trusted.
func TestFetcher_MaxKeysCapsKeyCount(t *testing.T) {
	t.Parallel()

	keys := make([]jwk.Key, DefaultMaxKeys+1)
	for i := range keys {
		key, _ := newECKey(t, fmt.Sprintf("k%d", i))
		keys[i] = key
	}
	srv := newMutableJWKSServer(t, jwksDoc(t, keys...))

	f := newShortFetcher(t)

	_, err := f.Lookup(context.Background(), srv.URL, "k0")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too many keys")
}

// TestFetcher_RefreshIntervalIsPinned confirms WithRefreshInterval actually
// threads jwk.WithConstantInterval through to the underlying httprc.Resource,
// even though the JWKS endpoint advertises a much longer Cache-Control
// max-age — an external endpoint must not get to choose how long its keys are
// cached (see the WithRefreshInterval doc comment).
func TestFetcher_RefreshIntervalIsPinned(t *testing.T) {
	t.Parallel()

	key1, _ := newECKey(t, "k1")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A long max-age that would push httprc's derived interval far past
		// the pinned interval if the constant interval were not applied.
		w.Header().Set("Cache-Control", "max-age=2592000") // 30 days
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksDoc(t, key1))
	}))
	t.Cleanup(srv.Close)

	f := newShortFetcher(t, WithRefreshInterval(5*time.Minute))

	_, err := f.Lookup(context.Background(), srv.URL, "k1")
	require.NoError(t, err)

	resource, err := f.cache.LookupResource(context.Background(), srv.URL)
	require.NoError(t, err)
	assert.Equal(t, 5*time.Minute, resource.ConstantInterval(),
		"registered resource must ignore the endpoint's own Cache-Control max-age")
}

// TestFetcher_EnsureRegisteredToleratesAlreadyRegistered proves the
// ErrResourceAlreadyExists absorption: re-running EnsureRegistered for a URL
// that is already registered with the Fetcher's cache (e.g. after an external
// state reset) must not fail.
func TestFetcher_EnsureRegisteredToleratesAlreadyRegistered(t *testing.T) {
	t.Parallel()

	key1, _ := newECKey(t, "k1")
	srv := newMutableJWKSServer(t, jwksDoc(t, key1))

	f := newShortFetcher(t)

	ctx := context.Background()
	require.NoError(t, f.EnsureRegistered(ctx, srv.URL))

	// Simulate the state an OIDC re-discovery reset used to produce: the
	// fetched marker is cleared but the URL is still registered with the
	// cache, so the next EnsureRegistered takes the IsRegistered→Refresh path.
	f.mu.Lock()
	f.fetched = false
	f.mu.Unlock()

	require.NoError(t, f.EnsureRegistered(ctx, srv.URL),
		"re-registering an already-registered URL must not fail")
}

// TestFetcher_EnsureRegisteredRegistersNewURLAfterSuccess proves the URL
// switch: once a URL has been fetched successfully, EnsureRegistered for a
// DIFFERENT URL (e.g. after OIDC discovery resolves a new jwks_uri) must not
// be short-circuited by the previous success — it must register and fetch the
// new resource.
func TestFetcher_EnsureRegisteredRegistersNewURLAfterSuccess(t *testing.T) {
	t.Parallel()

	key1, _ := newECKey(t, "k1")
	key2, _ := newECKey(t, "k2")
	srvA := newMutableJWKSServer(t, jwksDoc(t, key1))
	srvB := newMutableJWKSServer(t, jwksDoc(t, key2))

	f := newShortFetcher(t)

	ctx := context.Background()
	_, err := f.Lookup(ctx, srvA.URL, "k1")
	require.NoError(t, err)
	require.Equal(t, int32(1), srvA.hits.Load())

	_, err = f.Lookup(ctx, srvB.URL, "k2")
	require.NoError(t, err, "a URL other than the fetched one must be registered and fetched")
	require.Equal(t, int32(1), srvB.hits.Load())
}

// TestValidateJWKSURL exercises ValidateJWKSURL: the SSRF guard applied on
// every register/refresh, and shared with pkg/authserver/config.go's
// config-time check so the two can't drift out of sync.
func TestValidateJWKSURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		url               string
		insecureAllowHTTP bool
		wantErr           string
	}{
		{name: "https accepted", url: "https://issuer.example.com/jwks"},
		{name: "http rejected", url: "http://issuer.example.com/jwks", wantErr: "must use HTTPS"},
		{
			name:    "userinfo with password rejected",
			url:     "https://user:hunter2@issuer.example.com/jwks",
			wantErr: "must not contain userinfo",
		},
		{
			// url.Parse sets User for a bare username too, and net/http would
			// still send it as a Basic auth header.
			name:    "userinfo without password rejected",
			url:     "https://user@issuer.example.com/jwks",
			wantErr: "must not contain userinfo",
		},
		{
			name:              "userinfo rejected even with insecureAllowHTTP",
			url:               "http://user:hunter2@issuer.example.com/jwks",
			insecureAllowHTTP: true,
			wantErr:           "must not contain userinfo",
		},
		{
			name:              "http accepted with insecureAllowHTTP",
			url:               "http://issuer.example.com/jwks",
			insecureAllowHTTP: true,
		},
		{
			name:              "ftp rejected even with insecureAllowHTTP",
			url:               "ftp://issuer.example.com/jwks",
			insecureAllowHTTP: true,
			wantErr:           "must use HTTPS",
		},
		{
			name:              "no scheme rejected even with insecureAllowHTTP",
			url:               "//issuer.example.com/jwks",
			insecureAllowHTTP: true,
			wantErr:           "must use HTTPS",
		},
		{name: "loopback IP literal rejected", url: "https://127.0.0.1/jwks", wantErr: "private or loopback"},
		{name: "private IP literal rejected", url: "https://10.1.2.3/jwks", wantErr: "private or loopback"},
		{name: "malformed URL rejected", url: "://not-a-url", wantErr: "invalid URL"},
		{name: "missing host rejected", url: "https:///jwks", wantErr: "host is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateJWKSURL(tt.url, tt.insecureAllowHTTP, false)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
