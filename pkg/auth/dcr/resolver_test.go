// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package dcr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// dcrTestHandlerConfig controls the behaviour of newDCRTestServer.
type dcrTestHandlerConfig struct {
	// omitRegistrationEndpoint causes discovery metadata to omit the
	// registration_endpoint field, triggering the synthesised /register
	// path.
	omitRegistrationEndpoint bool

	// registrationEndpointPath overrides the path served as
	// registration_endpoint. Defaults to "/register".
	registrationEndpointPath string

	// tokenEndpointAuthMethodsSupported is advertised in metadata.
	tokenEndpointAuthMethodsSupported []string

	// scopesSupported is advertised in metadata.
	scopesSupported []string

	// codeChallengeMethodsSupported is advertised in metadata. Tests that
	// exercise public-client (none) registration must include "S256" here,
	// since selectTokenEndpointAuthMethod refuses to select none without
	// it (RFC 7636 / OAuth 2.1).
	codeChallengeMethodsSupported []string

	// observeRegistration is called for each request hitting the
	// registration endpoint. Safe for concurrent use.
	observeRegistration func(r *http.Request, body []byte)

	// clientIDIssuedAt and clientSecretExpiresAt are echoed back in the
	// RFC 7591 §3.2.1 response. Both are int64 epoch seconds; 0 is the wire
	// convention for "field absent" and (for ClientSecretExpiresAt) "secret
	// does not expire".
	clientIDIssuedAt      int64
	clientSecretExpiresAt int64

	// clientID overrides the client_id returned by the registration
	// endpoint. Defaults to "test-client-id". Distinct values let a test
	// prove two upstreams received distinct dynamically-registered clients.
	clientID string
}

// newDCRTestServer mounts RFC 8414 metadata and a DCR endpoint on a single
// httptest.NewServer. The returned server's URL is the issuer; callers must
// t.Cleanup(server.Close) (not defer, when using t.Parallel()).
func newDCRTestServer(t *testing.T, cfg dcrTestHandlerConfig) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	var server *httptest.Server

	registrationPath := cfg.registrationEndpointPath
	if registrationPath == "" {
		registrationPath = "/register"
	}

	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		md := oauthproto.AuthorizationServerMetadata{
			Issuer:                            server.URL,
			AuthorizationEndpoint:             server.URL + "/authorize",
			TokenEndpoint:                     server.URL + "/token",
			JWKSURI:                           server.URL + "/jwks",
			TokenEndpointAuthMethodsSupported: cfg.tokenEndpointAuthMethodsSupported,
			ScopesSupported:                   cfg.scopesSupported,
			CodeChallengeMethodsSupported:     cfg.codeChallengeMethodsSupported,
		}
		if !cfg.omitRegistrationEndpoint {
			md.RegistrationEndpoint = server.URL + registrationPath
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(md)
	})

	mux.HandleFunc(registrationPath, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()
		if cfg.observeRegistration != nil {
			cfg.observeRegistration(r, body)
		}
		// Decode the request to echo the auth method back in the response.
		var req oauthproto.DynamicClientRegistrationRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		clientID := cfg.clientID
		if clientID == "" {
			clientID = "test-client-id"
		}
		resp := oauthproto.DynamicClientRegistrationResponse{
			ClientID:                clientID,
			ClientSecret:            "test-client-secret",
			RegistrationAccessToken: "test-reg-token",
			RegistrationClientURI:   server.URL + "/register/" + clientID,
			TokenEndpointAuthMethod: req.TokenEndpointAuthMethod,
			ClientIDIssuedAt:        cfg.clientIDIssuedAt,
			ClientSecretExpiresAt:   cfg.clientSecretExpiresAt,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	})

	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestResolveDCRCredentials_CacheHitShortCircuits(t *testing.T) {
	t.Parallel()

	// Count every request to every path — discovery, registration,
	// anything. The acceptance criterion is that a cache hit issues zero
	// network I/O, so the cache-hit path must never reach this server.
	var totalRequests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&totalRequests, 1)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(server.Close)

	cache := newMemoryDCRStore(t)
	issuer := server.URL

	// Pre-populate the cache with a resolution matching the key we will
	// look up.
	redirectURI := issuer + "/oauth/callback"
	// UpstreamID is the upstream issuer the resolver derives from the
	// DiscoveryURL below; for a well-known discovery URL that is the issuer
	// origin itself. The preloaded key must match it for the cache to hit.
	key := Key{
		Issuer:      issuer,
		UpstreamID:  issuer,
		RedirectURI: redirectURI,
		ScopesHash:  storage.ScopesHash([]string{"openid", "profile"}),
	}
	preloaded := &Resolution{
		ClientID:              "preloaded-id",
		ClientSecret:          "preloaded-secret",
		AuthorizationEndpoint: "https://preloaded/authorize",
		TokenEndpoint:         "https://preloaded/token",
	}
	require.NoError(t, cache.Put(context.Background(), key, preloaded))

	req := &Request{
		Issuer:       issuer,
		Scopes:       []string{"openid", "profile"},
		DiscoveryURL: issuer + "/.well-known/openid-configuration",
	}

	got, err := ResolveCredentials(context.Background(), req, cache)
	require.NoError(t, err)
	assert.Equal(t, "preloaded-id", got.ClientID)
	assert.Equal(t, "preloaded-secret", got.ClientSecret)
	assert.Equal(t, int32(0), atomic.LoadInt32(&totalRequests),
		"cache hit must not issue any network I/O (discovery or registration)")
}

func TestResolveDCRCredentials_RegistersOnCacheMiss(t *testing.T) {
	t.Parallel()

	var gotAuthHeader string
	var gotBody []byte
	server := newDCRTestServer(t, dcrTestHandlerConfig{
		tokenEndpointAuthMethodsSupported: []string{"client_secret_basic"},
		scopesSupported:                   []string{"openid", "profile"},
		observeRegistration: func(r *http.Request, body []byte) {
			gotAuthHeader = r.Header.Get("Authorization")
			gotBody = body
		},
	})

	cache := newMemoryDCRStore(t)
	issuer := server.URL
	req := &Request{
		Issuer:       issuer,
		Scopes:       []string{"openid", "profile"},
		DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
	}

	res, err := ResolveCredentials(context.Background(), req, cache)
	require.NoError(t, err)
	assert.Equal(t, "test-client-id", res.ClientID)
	assert.Equal(t, "test-client-secret", res.ClientSecret)
	assert.Equal(t, "test-reg-token", res.RegistrationAccessToken)
	assert.Equal(t, issuer+"/register/test-client-id", res.RegistrationClientURI)
	assert.Equal(t, issuer+"/authorize", res.AuthorizationEndpoint)
	assert.Equal(t, issuer+"/token", res.TokenEndpoint)
	assert.Equal(t, "client_secret_basic", res.TokenEndpointAuthMethod)
	assert.False(t, res.CreatedAt.IsZero(), "CreatedAt should be populated")
	// No initial access token configured -> no Authorization header.
	assert.Empty(t, gotAuthHeader)

	// Verify the request body carried the expected fields.
	var dcrReq oauthproto.DynamicClientRegistrationRequest
	require.NoError(t, json.Unmarshal(gotBody, &dcrReq))
	assert.Equal(t, []string{issuer + "/oauth/callback"}, dcrReq.RedirectURIs)
	assert.ElementsMatch(t, []string{"openid", "profile"}, []string(dcrReq.Scopes))

	// Cache was populated. UpstreamID is the issuer origin the resolver
	// derives from the oauth-authorization-server discovery URL.
	cached, ok, err := cache.Get(context.Background(),
		Key{Issuer: issuer, UpstreamID: issuer, RedirectURI: issuer + "/oauth/callback", ScopesHash: storage.ScopesHash([]string{"openid", "profile"})})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "test-client-id", cached.ClientID)
}

// TestResolveDCRCredentials_DistinctUpstreamsSameScopesDoNotCollide is the
// regression test for issue #5823. Within one embedded authserver every
// OAuth2 upstream shares the authserver's own Issuer and the single defaulted
// {issuer}/oauth/callback RedirectURI, so the cache key differed only by the
// scopes hash. Two upstreams configured with equal scopes therefore collided:
// the second read back the first's dynamically-registered client and never
// registered with its own authorization server. The UpstreamID key component
// disambiguates them.
func TestResolveDCRCredentials_DistinctUpstreamsSameScopesDoNotCollide(t *testing.T) {
	t.Parallel()

	var regCountA, regCountB int32
	serverA := newDCRTestServer(t, dcrTestHandlerConfig{
		clientID:                          "client-a",
		tokenEndpointAuthMethodsSupported: []string{"client_secret_basic"},
		scopesSupported:                   []string{"openid", "profile"},
		observeRegistration:               func(*http.Request, []byte) { atomic.AddInt32(&regCountA, 1) },
	})
	serverB := newDCRTestServer(t, dcrTestHandlerConfig{
		clientID:                          "client-b",
		tokenEndpointAuthMethodsSupported: []string{"client_secret_basic"},
		scopesSupported:                   []string{"openid", "profile"},
		observeRegistration:               func(*http.Request, []byte) { atomic.AddInt32(&regCountB, 1) },
	})

	// One shared cache, one shared local issuer, one shared scope set — the
	// embedded-authserver shape. Only the upstream (DiscoveryURL) differs.
	cache := newMemoryDCRStore(t)
	const localIssuer = "https://authserver.example.com"
	ctx := context.Background()
	reqFor := func(discoveryHost string) *Request {
		return &Request{
			Issuer:       localIssuer,
			Scopes:       []string{"openid", "profile"},
			DiscoveryURL: discoveryHost + "/.well-known/oauth-authorization-server",
		}
	}

	resA, err := ResolveCredentials(ctx, reqFor(serverA.URL), cache)
	require.NoError(t, err)
	resB, err := ResolveCredentials(ctx, reqFor(serverB.URL), cache)
	require.NoError(t, err)

	// The crux: the second upstream registered with its OWN authorization
	// server and received its own client, rather than reading back the
	// first's cached credentials.
	assert.Equal(t, "client-a", resA.ClientID)
	assert.Equal(t, "client-b", resB.ClientID,
		"second upstream must register with its own AS, not reuse the first's cached client (#5823)")
	assert.Equal(t, serverA.URL+"/token", resA.TokenEndpoint)
	assert.Equal(t, serverB.URL+"/token", resB.TokenEndpoint,
		"second upstream's endpoints must come from its own AS metadata")
	assert.Equal(t, int32(1), atomic.LoadInt32(&regCountA), "upstream A must register exactly once")
	assert.Equal(t, int32(1), atomic.LoadInt32(&regCountB),
		"upstream B must register exactly once; a cache collision would leave this at zero")

	// Both resolutions coexist in the cache under keys that differ only by
	// UpstreamID (shared Issuer, RedirectURI, and scopes hash).
	redirectURI := localIssuer + "/oauth/callback"
	scopesHash := storage.ScopesHash([]string{"openid", "profile"})
	cachedA, okA, err := cache.Get(ctx,
		Key{Issuer: localIssuer, UpstreamID: serverA.URL, RedirectURI: redirectURI, ScopesHash: scopesHash})
	require.NoError(t, err)
	require.True(t, okA)
	assert.Equal(t, "client-a", cachedA.ClientID)
	cachedB, okB, err := cache.Get(ctx,
		Key{Issuer: localIssuer, UpstreamID: serverB.URL, RedirectURI: redirectURI, ScopesHash: scopesHash})
	require.NoError(t, err)
	require.True(t, okB)
	assert.Equal(t, "client-b", cachedB.ClientID)
}

// TestResolveDCRCredentials_ConcurrentDistinctUpstreamsBothRegister guards the
// singleflight (flight-key) split, complementing the sequential
// TestResolveDCRCredentials_DistinctUpstreamsSameScopesDoNotCollide which only
// proves the persistent cache-key split. Two upstreams that share the caller's
// Issuer, RedirectURI, and scopes but differ by UpstreamID must not coalesce
// into one dcrFlight — each must register with its own AS. A barrier in the
// registration handlers forces both calls to be in-flight simultaneously, so a
// flight-key collision would surface as one call becoming the other's follower
// (its registration count staying at zero).
func TestResolveDCRCredentials_ConcurrentDistinctUpstreamsBothRegister(t *testing.T) {
	t.Parallel()

	// Barrier: release both registration handlers only once both have been
	// entered, forcing genuine in-flight overlap. The timeout keeps the test
	// fail-fast — if the two calls coalesced there would be only one handler
	// to enter, so the sole handler proceeds after the wait and the reg-count
	// assertions below catch the collision instead of the test hanging.
	var regCountA, regCountB, arrivals int32
	bothArrived := make(chan struct{})
	barrier := func() {
		if atomic.AddInt32(&arrivals, 1) == 2 {
			close(bothArrived)
		}
		select {
		case <-bothArrived:
		case <-time.After(3 * time.Second):
		}
	}

	serverA := newDCRTestServer(t, dcrTestHandlerConfig{
		clientID:                          "client-a",
		tokenEndpointAuthMethodsSupported: []string{"client_secret_basic"},
		scopesSupported:                   []string{"openid", "profile"},
		observeRegistration:               func(*http.Request, []byte) { atomic.AddInt32(&regCountA, 1); barrier() },
	})
	serverB := newDCRTestServer(t, dcrTestHandlerConfig{
		clientID:                          "client-b",
		tokenEndpointAuthMethodsSupported: []string{"client_secret_basic"},
		scopesSupported:                   []string{"openid", "profile"},
		observeRegistration:               func(*http.Request, []byte) { atomic.AddInt32(&regCountB, 1); barrier() },
	})

	cache := newMemoryDCRStore(t)
	const localIssuer = "https://authserver.example.com"
	reqFor := func(discoveryHost string) *Request {
		return &Request{
			Issuer:       localIssuer,
			Scopes:       []string{"openid", "profile"},
			DiscoveryURL: discoveryHost + "/.well-known/oauth-authorization-server",
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	results := make([]*Resolution, 2)
	errs := make([]error, 2)
	go func() {
		defer wg.Done()
		results[0], errs[0] = ResolveCredentials(context.Background(), reqFor(serverA.URL), cache)
	}()
	go func() {
		defer wg.Done()
		results[1], errs[1] = ResolveCredentials(context.Background(), reqFor(serverB.URL), cache)
	}()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for concurrent ResolveCredentials calls; possible flight-key deadlock")
	}

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.Equal(t, "client-a", results[0].ClientID)
	assert.Equal(t, "client-b", results[1].ClientID,
		"each concurrent upstream must receive its own client, not the other flight's leader result")
	assert.Equal(t, int32(1), atomic.LoadInt32(&regCountA), "upstream A must register exactly once")
	assert.Equal(t, int32(1), atomic.LoadInt32(&regCountB),
		"upstream B must register exactly once; a flight-key collision would coalesce the two calls and leave this at zero")
}

// TestResolveDCRCredentials_DistinctRegistrationEndpointsDoNotCollide covers
// the RegistrationEndpoint branch of resolveUpstreamKeyIdentity: two upstreams
// configured with explicit (distinct) registration endpoints but the same
// caller Issuer, RedirectURI, and scopes must still register separately. The
// UpstreamID is the registration endpoint URL on this branch.
func TestResolveDCRCredentials_DistinctRegistrationEndpointsDoNotCollide(t *testing.T) {
	t.Parallel()

	var regCountA, regCountB int32
	serverA := newDCRTestServer(t, dcrTestHandlerConfig{
		clientID:            "client-a",
		observeRegistration: func(*http.Request, []byte) { atomic.AddInt32(&regCountA, 1) },
	})
	serverB := newDCRTestServer(t, dcrTestHandlerConfig{
		clientID:            "client-b",
		observeRegistration: func(*http.Request, []byte) { atomic.AddInt32(&regCountB, 1) },
	})

	cache := newMemoryDCRStore(t)
	const localIssuer = "https://authserver.example.com"
	ctx := context.Background()
	// The RegistrationEndpoint branch performs no discovery, so the caller
	// must supply authorization/token endpoints explicitly.
	reqFor := func(server *httptest.Server) *Request {
		return &Request{
			Issuer:                localIssuer,
			Scopes:                []string{"openid", "profile"},
			RegistrationEndpoint:  server.URL + "/register",
			AuthorizationEndpoint: server.URL + "/authorize",
			TokenEndpoint:         server.URL + "/token",
		}
	}

	resA, err := ResolveCredentials(ctx, reqFor(serverA), cache)
	require.NoError(t, err)
	resB, err := ResolveCredentials(ctx, reqFor(serverB), cache)
	require.NoError(t, err)

	assert.Equal(t, "client-a", resA.ClientID)
	assert.Equal(t, "client-b", resB.ClientID,
		"second upstream must register with its own registration endpoint, not reuse the first's cached client (#5823)")
	assert.Equal(t, int32(1), atomic.LoadInt32(&regCountA), "upstream A must register exactly once")
	assert.Equal(t, int32(1), atomic.LoadInt32(&regCountB),
		"upstream B must register exactly once; a cache collision would leave this at zero")

	// The two entries coexist under keys whose only differing component is
	// UpstreamID (the registration endpoint URL).
	redirectURI := localIssuer + "/oauth/callback"
	scopesHash := storage.ScopesHash([]string{"openid", "profile"})
	_, okA, err := cache.Get(ctx,
		Key{Issuer: localIssuer, UpstreamID: serverA.URL + "/register", RedirectURI: redirectURI, ScopesHash: scopesHash})
	require.NoError(t, err)
	assert.True(t, okA, "upstream A entry must be keyed by its registration endpoint")
	_, okB, err := cache.Get(ctx,
		Key{Issuer: localIssuer, UpstreamID: serverB.URL + "/register", RedirectURI: redirectURI, ScopesHash: scopesHash})
	require.NoError(t, err)
	assert.True(t, okB, "upstream B entry must be keyed by its registration endpoint")
}

func TestResolveDCRCredentials_ExplicitEndpointsOverride(t *testing.T) {
	t.Parallel()

	server := newDCRTestServer(t, dcrTestHandlerConfig{})
	cache := newMemoryDCRStore(t)
	issuer := server.URL

	req := &Request{
		Issuer:                issuer,
		AuthorizationEndpoint: "https://explicit.example.com/authorize",
		TokenEndpoint:         "https://explicit.example.com/token",
		Scopes:                []string{"openid"},
		DiscoveryURL:          issuer + "/.well-known/oauth-authorization-server",
	}

	res, err := ResolveCredentials(context.Background(), req, cache)
	require.NoError(t, err)
	assert.Equal(t, "https://explicit.example.com/authorize", res.AuthorizationEndpoint)
	assert.Equal(t, "https://explicit.example.com/token", res.TokenEndpoint)
}

func TestResolveDCRCredentials_InitialAccessTokenAsBearer(t *testing.T) {
	t.Parallel()

	var gotAuthHeader string
	server := newDCRTestServer(t, dcrTestHandlerConfig{
		observeRegistration: func(r *http.Request, _ []byte) {
			gotAuthHeader = r.Header.Get("Authorization")
		},
	})

	// Use a file-based initial access token so the test can remain parallel
	// (t.Setenv and t.Parallel are mutually exclusive). tokenPath is scoped
	// to t.TempDir(), so concurrent subtests cannot clobber each other's
	// token values even if the test is later subdivided.
	tokenPath := filepath.Join(t.TempDir(), "iat")
	require.NoError(t, os.WriteFile(tokenPath, []byte("iat-secret-value\n"), 0o600))

	cache := newMemoryDCRStore(t)
	issuer := server.URL
	req := &Request{
		Issuer:             issuer,
		Scopes:             []string{"openid"},
		DiscoveryURL:       issuer + "/.well-known/oauth-authorization-server",
		InitialAccessToken: readTokenFile(t, tokenPath),
	}

	_, err := ResolveCredentials(context.Background(), req, cache)
	require.NoError(t, err)
	assert.Equal(t, "Bearer iat-secret-value", gotAuthHeader)
}

// TestResolveDCRCredentials_DoesNotForwardBearerOnRedirect pins the
// security property that an upstream cannot use a 30x redirect from the
// registration endpoint to coerce the resolver into re-issuing the
// registration request — and the attached RFC 7591 initial access token —
// against a different origin. The resolver must refuse the redirect; the
// foreign origin must observe zero traffic.
func TestResolveDCRCredentials_DoesNotForwardBearerOnRedirect(t *testing.T) {
	t.Parallel()

	// Foreign origin: a separate httptest server that records every request
	// it receives. After the test we assert it received exactly zero
	// requests, which proves the bearer token never crossed origins.
	var foreignHits int32
	var foreignAuthHeaders []string
	var foreignMu sync.Mutex
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignMu.Lock()
		atomic.AddInt32(&foreignHits, 1)
		foreignAuthHeaders = append(foreignAuthHeaders, r.Header.Get("Authorization"))
		foreignMu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(foreign.Close)

	// Upstream: serves discovery normally, but its /register handler 302s
	// to the foreign origin. A non-defended client would re-issue the
	// registration request (with the Authorization header) against
	// foreign.URL/stolen.
	mux := http.NewServeMux()
	var upstream *httptest.Server
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(oauthproto.AuthorizationServerMetadata{
			Issuer:                upstream.URL,
			AuthorizationEndpoint: upstream.URL + "/authorize",
			TokenEndpoint:         upstream.URL + "/token",
			JWKSURI:               upstream.URL + "/jwks",
			RegistrationEndpoint:  upstream.URL + "/register",
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/stolen", http.StatusFound)
	})
	upstream = httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	tokenPath := filepath.Join(t.TempDir(), "iat")
	require.NoError(t, os.WriteFile(tokenPath, []byte("iat-secret-value\n"), 0o600))

	cache := newMemoryDCRStore(t)
	issuer := upstream.URL
	req := &Request{
		Issuer:             issuer,
		Scopes:             []string{"openid"},
		DiscoveryURL:       issuer + "/.well-known/oauth-authorization-server",
		InitialAccessToken: readTokenFile(t, tokenPath),
	}

	_, err := ResolveCredentials(context.Background(), req, cache)
	require.Error(t, err, "registration must fail when the upstream returns a redirect")
	assert.ErrorIs(t, err, errDCRRedirectRefused,
		"the resolver must refuse to follow registration-endpoint redirects")

	foreignMu.Lock()
	defer foreignMu.Unlock()
	assert.EqualValues(t, 0, atomic.LoadInt32(&foreignHits),
		"foreign origin must receive zero requests; got %v Authorization headers: %v",
		atomic.LoadInt32(&foreignHits), foreignAuthHeaders)
}

// TestResolveDCRCredentials_BlocksPrivateIPTargets pins the CWE-918 SSRF guard
// added for issue #5825: both of the resolver's outbound calls — the discovery
// fetch and the registration POST — are dialed through a private-IP-guarded
// client, so a registration endpoint that resolves to a private or link-local
// address is refused at connect time. The guard fires before any bytes leave
// the host, so these cases need no live server behind the private target.
// AllowPrivateIPs defaults to false; loopback stays permitted for development.
func TestResolveDCRCredentials_BlocksPrivateIPTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		newReq func(t *testing.T) *Request
	}{
		{
			// Direct RegistrationEndpoint branch: an operator-configured
			// endpoint that resolves (or rebinds) to an RFC-1918 address.
			name: "direct registration endpoint on a private IP",
			newReq: func(_ *testing.T) *Request {
				// Issuer is unique per test: the process-global dcrFlight
				// singleflight keys on (Issuer, RedirectURI, ScopesHash), so a
				// shared synthetic issuer would coalesce this call with another
				// parallel test and return that test's result.
				return &Request{
					Issuer:               "https://block-direct.example.test",
					Scopes:               []string{"openid"},
					RegistrationEndpoint: "https://10.255.255.1/register",
				}
			},
		},
		{
			// Discovery-indirection branch: the discovery document is served
			// from an allowed (loopback) host, but it points
			// registration_endpoint at a link-local metadata address the
			// metadata itself controls. HTTPS so it clears scheme validation
			// and reaches the guarded dial.
			name: "registration endpoint from discovery on a link-local IP",
			newReq: func(t *testing.T) *Request {
				t.Helper()
				mux := http.NewServeMux()
				var server *httptest.Server
				mux.HandleFunc("/.well-known/oauth-authorization-server",
					func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(oauthproto.AuthorizationServerMetadata{
							Issuer:                server.URL,
							AuthorizationEndpoint: server.URL + "/authorize",
							TokenEndpoint:         server.URL + "/token",
							JWKSURI:               server.URL + "/jwks",
							RegistrationEndpoint:  "https://169.254.169.254/register",
						})
					})
				server = httptest.NewServer(mux)
				t.Cleanup(server.Close)
				return &Request{
					Issuer:       server.URL,
					Scopes:       []string{"openid"},
					DiscoveryURL: server.URL + "/.well-known/oauth-authorization-server",
				}
			},
		},
		{
			// Discovery-fetch branch itself: DiscoveryURL points directly at a
			// private IP, so the *discovery* client's own dial guard
			// (newGuardedDCRClient(discoveryHost, ...) in resolveDCREndpoints)
			// must refuse it. The two cases above only prove the guard on the
			// registration client; this proves it on the discovery client. No
			// live server is needed — the guard fires before any request is
			// sent, and deriveExpectedIssuerFromDiscoveryURL resolves the
			// issuer from the URL string alone.
			name: "discovery URL itself on a private IP",
			newReq: func(_ *testing.T) *Request {
				return &Request{
					Issuer:       "https://block-discovery.example.test",
					Scopes:       []string{"openid"},
					DiscoveryURL: "https://10.255.255.1/.well-known/oauth-authorization-server",
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ResolveCredentials(context.Background(), tc.newReq(t), newMemoryDCRStore(t))
			require.Error(t, err)
			assert.ErrorContains(t, err, networking.ErrPrivateIpAddress,
				"a private/link-local registration target must be refused at connect time")
		})
	}
}

// TestResolveDCRCredentials_AllowPrivateIPsHonored proves req.AllowPrivateIPs
// is threaded through to the guarded client: with it set, the resolver dials
// the private target instead of refusing it at the guard. The target is a
// non-routable RFC 5737 documentation address (TEST-NET-1), so the dial fails
// with a network error rather than the guard error — and can never reach a
// real host.
func TestResolveDCRCredentials_AllowPrivateIPsHonored(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Issuer is unique per test so the process-global dcrFlight singleflight
	// (keyed on Issuer, RedirectURI, ScopesHash) cannot coalesce this call
	// with another parallel test and hand back that test's result.
	req := &Request{
		Issuer:               "https://allow-private.example.test",
		Scopes:               []string{"openid"},
		RegistrationEndpoint: "https://192.0.2.1/register",
		AllowPrivateIPs:      true,
	}

	_, err := ResolveCredentials(ctx, req, newMemoryDCRStore(t))
	require.Error(t, err, "the dead documentation address cannot complete registration")
	assert.NotContains(t, err.Error(), networking.ErrPrivateIpAddress,
		"AllowPrivateIPs=true must lift the guard so the dial is attempted, not refused")
}

// TestResolveDCRCredentials_AllowPrivateIPsHonoredViaDiscovery proves
// req.AllowPrivateIPs also lifts the guard on the discovery-indirection
// path — not just the direct RegistrationEndpoint branch covered by
// TestResolveDCRCredentials_AllowPrivateIPsHonored above: with it set, a
// registration_endpoint the discovery document points at a private IP is
// dialed instead of refused at the guard.
func TestResolveDCRCredentials_AllowPrivateIPsHonoredViaDiscovery(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/.well-known/oauth-authorization-server",
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(oauthproto.AuthorizationServerMetadata{
				Issuer:                server.URL,
				AuthorizationEndpoint: server.URL + "/authorize",
				TokenEndpoint:         server.URL + "/token",
				JWKSURI:               server.URL + "/jwks",
				// Non-routable RFC 5737 documentation address (TEST-NET-1): the
				// dial fails with a network error rather than reaching a real
				// host, same as TestResolveDCRCredentials_AllowPrivateIPsHonored.
				RegistrationEndpoint: "https://192.0.2.1/register",
			})
		})
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)

	req := &Request{
		Issuer:          server.URL,
		Scopes:          []string{"openid"},
		DiscoveryURL:    server.URL + "/.well-known/oauth-authorization-server",
		AllowPrivateIPs: true,
	}

	_, err := ResolveCredentials(ctx, req, newMemoryDCRStore(t))
	require.Error(t, err, "the dead documentation address cannot complete registration")
	assert.NotContains(t, err.Error(), networking.ErrPrivateIpAddress,
		"AllowPrivateIPs=true must lift the guard on the discovery-indirection path too")
}

// TestResolveDCRCredentials_DiscoveryRefusesCrossHostRedirect pins that the
// discovery fetch installs SameHostRedirectPolicy: a discovery endpoint that
// 30x-redirects to a different host must not be followed, so a malicious
// upstream cannot walk the metadata fetch onto an unintended origin (CWE-918).
// A different port counts as a different host, so two loopback httptest servers
// suffice.
func TestResolveDCRCredentials_DiscoveryRefusesCrossHostRedirect(t *testing.T) {
	t.Parallel()

	var foreignHits int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&foreignHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(foreign.Close)

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/.well-known/oauth-authorization-server", http.StatusFound)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	req := &Request{
		Issuer:       server.URL,
		Scopes:       []string{"openid"},
		DiscoveryURL: server.URL + "/.well-known/oauth-authorization-server",
	}

	_, err := ResolveCredentials(context.Background(), req, newMemoryDCRStore(t))
	require.Error(t, err, "discovery must fail when the endpoint redirects cross-host")
	assert.EqualValues(t, 0, atomic.LoadInt32(&foreignHits),
		"the discovery client must not follow a cross-host redirect")
}

func TestHostFromURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rawURL  string
		want    string
		wantErr bool
	}{
		{name: "https with port", rawURL: "https://idp.example.com:8443/register", want: "idp.example.com:8443"},
		{name: "https without port", rawURL: "https://idp.example.com/register", want: "idp.example.com"},
		{name: "loopback with port", rawURL: "http://127.0.0.1:5000/register", want: "127.0.0.1:5000"},
		{name: "missing host", rawURL: "/register", wantErr: true},
		{name: "malformed url", rawURL: "://bad", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := hostFromURL(tc.rawURL)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestResolveDCRCredentials_AuthMethodPreference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		supported []string
		// codeChallenge is the upstream's advertised
		// code_challenge_methods_supported. Required by the gating in
		// selectTokenEndpointAuthMethod whenever the test expects "none".
		codeChallenge []string
		expected      string
	}{
		{
			name:      "prefers client_secret_basic over none",
			supported: []string{"none", "client_secret_basic"},
			expected:  "client_secret_basic",
		},
		{
			name:      "prefers private_key_jwt over others",
			supported: []string{"client_secret_basic", "private_key_jwt", "none"},
			expected:  "private_key_jwt",
		},
		{
			name:          "falls back to none when only none supported and S256 advertised",
			supported:     []string{"none"},
			codeChallenge: []string{"S256"},
			expected:      "none",
		},
		{
			name:      "defaults to client_secret_basic when metadata omits the field",
			supported: nil,
			expected:  "client_secret_basic",
		},
		{
			name:      "prefers client_secret_basic over client_secret_post",
			supported: []string{"client_secret_post", "client_secret_basic"},
			expected:  "client_secret_basic",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := newDCRTestServer(t, dcrTestHandlerConfig{
				tokenEndpointAuthMethodsSupported: tc.supported,
				codeChallengeMethodsSupported:     tc.codeChallenge,
			})
			cache := newMemoryDCRStore(t)
			issuer := server.URL
			req := &Request{
				Issuer:       issuer,
				Scopes:       []string{"openid"},
				DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
			}

			res, err := ResolveCredentials(context.Background(), req, cache)
			require.NoError(t, err)
			assert.Equal(t, tc.expected, res.TokenEndpointAuthMethod)
		})
	}
}

// TestResolveDCRCredentials_RefusesNoneWithoutS256 pins the compliance gate
// added for the "none" auth method: an upstream that advertises only "none"
// for token_endpoint_auth_methods but does not advertise S256 in
// code_challenge_methods_supported must be rejected at boot rather than
// quietly registering a public client without RFC 7636 / OAuth 2.1 PKCE.
func TestResolveDCRCredentials_RefusesNoneWithoutS256(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		codeChallenge []string
	}{
		{name: "code_challenge_methods_supported omitted", codeChallenge: nil},
		{name: "code_challenge_methods_supported lists only plain", codeChallenge: []string{"plain"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := newDCRTestServer(t, dcrTestHandlerConfig{
				tokenEndpointAuthMethodsSupported: []string{"none"},
				codeChallengeMethodsSupported:     tc.codeChallenge,
			})
			cache := newMemoryDCRStore(t)
			issuer := server.URL
			req := &Request{
				Issuer:       issuer,
				Scopes:       []string{"openid"},
				DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
			}

			_, err := ResolveCredentials(context.Background(), req, cache)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "S256",
				"error must mention the missing S256 advertisement so operators can correlate")
			assert.Contains(t, err.Error(), "RFC 7636",
				"error must cite the spec being enforced")
		})
	}
}

func TestResolveDCRCredentials_EmptyAuthMethodIntersectionErrors(t *testing.T) {
	t.Parallel()

	// Configure the server to advertise an unknown method so intersection is empty.
	server := newDCRTestServer(t, dcrTestHandlerConfig{
		tokenEndpointAuthMethodsSupported: []string{"tls_client_auth"},
	})
	cache := newMemoryDCRStore(t)
	issuer := server.URL
	req := &Request{
		Issuer:       issuer,
		Scopes:       []string{"openid"},
		DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
	}
	_, err := ResolveCredentials(context.Background(), req, cache)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no supported token_endpoint_auth_method")
}

func TestResolveDCRCredentials_SynthesisedRegistrationEndpoint(t *testing.T) {
	t.Parallel()

	// registrationEndpointPath="/register" is the synthesised path the
	// resolver will construct when metadata omits registration_endpoint.
	var gotPath string
	server := newDCRTestServer(t, dcrTestHandlerConfig{
		omitRegistrationEndpoint: true,
		registrationEndpointPath: "/register",
		observeRegistration: func(r *http.Request, _ []byte) {
			gotPath = r.URL.Path
		},
	})
	cache := newMemoryDCRStore(t)
	issuer := server.URL
	req := &Request{
		Issuer:       issuer,
		Scopes:       []string{"openid"},
		DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
	}

	res, err := ResolveCredentials(context.Background(), req, cache)
	require.NoError(t, err)
	assert.Equal(t, "test-client-id", res.ClientID)
	assert.Equal(t, "/register", gotPath)
}

// TestResolveDCRCredentials_CodeChallengeMethodsSupportedFieldEnablesPublicClient
// pins the dcr.Request.CodeChallengeMethodsSupported field on the
// RegistrationEndpoint-direct branch. Without a DiscoveryURL the resolver
// cannot reach the S256 PKCE gate via a metadata fetch; the caller must
// supply code_challenge_methods_supported so the gate has an input. This is
// the path exercised when resolveDCRCredentials pre-fetches AS metadata and
// forwards the field to the resolver (see resolveDCRCredentials in
// pkg/auth/discovery, introduced to fix #5356).
func TestResolveDCRCredentials_CodeChallengeMethodsSupportedFieldEnablesPublicClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                          string
		codeChallengeMethodsSupported []string
		publicClient                  bool
		wantErr                       bool
		wantErrContains               string
	}{
		{
			name:                          "S256 in field allows public client registration",
			codeChallengeMethodsSupported: []string{"S256"},
			publicClient:                  true,
		},
		{
			name:                          "plain-only in field rejects public client",
			codeChallengeMethodsSupported: []string{"plain"},
			publicClient:                  true,
			wantErr:                       true,
			wantErrContains:               "S256",
		},
		{
			name:                          "field absent rejects public client",
			codeChallengeMethodsSupported: nil,
			publicClient:                  true,
			wantErr:                       true,
			wantErrContains:               "S256",
		},
		{
			name:                          "field absent ok for confidential client",
			codeChallengeMethodsSupported: nil,
			publicClient:                  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var registrationHits int32
			mux := http.NewServeMux()
			mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = r.Body.Close()
				_ = body
				atomic.AddInt32(&registrationHits, 1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"client_id":"field-supplied-client"}`))
			})
			server := httptest.NewServer(mux)
			t.Cleanup(server.Close)

			cache := newMemoryDCRStore(t)
			req := &Request{
				Issuer:                        server.URL,
				AuthorizationEndpoint:         server.URL + "/authorize",
				TokenEndpoint:                 server.URL + "/token",
				Scopes:                        []string{"openid"},
				RegistrationEndpoint:          server.URL + "/register",
				CodeChallengeMethodsSupported: tc.codeChallengeMethodsSupported,
				PublicClient:                  tc.publicClient,
			}

			_, err := ResolveCredentials(context.Background(), req, cache)
			if tc.wantErr {
				require.Error(t, err)
				if tc.wantErrContains != "" {
					assert.Contains(t, err.Error(), tc.wantErrContains)
				}
				assert.EqualValues(t, 0, atomic.LoadInt32(&registrationHits),
					"registration endpoint must not be contacted when S256 gate fails")
				return
			}
			require.NoError(t, err)
			assert.EqualValues(t, 1, atomic.LoadInt32(&registrationHits))
		})
	}
}

func TestResolveDCRCredentials_RegistrationEndpointDirectBypassesDiscovery(t *testing.T) {
	t.Parallel()

	var registrationHits int32
	var discoveryHits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(_ http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&discoveryHits, 1)
	})
	mux.HandleFunc("/custom/register", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&registrationHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"client_id":"direct-id"}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cache := newMemoryDCRStore(t)
	issuer := server.URL
	req := &Request{
		Issuer:                issuer,
		AuthorizationEndpoint: issuer + "/authorize",
		TokenEndpoint:         issuer + "/token",
		Scopes:                []string{"openid"},
		RegistrationEndpoint:  issuer + "/custom/register",
	}

	res, err := ResolveCredentials(context.Background(), req, cache)
	require.NoError(t, err)
	assert.Equal(t, "direct-id", res.ClientID)
	assert.Equal(t, int32(0), atomic.LoadInt32(&discoveryHits),
		"discovery endpoint must not be contacted when RegistrationEndpoint is set")
	assert.Equal(t, int32(1), atomic.LoadInt32(&registrationHits))
}

// TestResolveDCRCredentials_RejectsInvalidInputs covers every branch of
// validateResolveInputs in one place: nil request, empty issuer, neither
// discovery_url nor registration_endpoint set, both set, and nil
// credential store.
func TestResolveDCRCredentials_RejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		req        *Request
		cache      CredentialStore
		wantErrSub string
	}{
		{
			name:       "nil request",
			req:        nil,
			cache:      newMemoryDCRStore(t),
			wantErrSub: "request is required",
		},
		{
			name: "empty issuer",
			req: &Request{
				RegistrationEndpoint: "https://example.com/register",
			},
			cache:      newMemoryDCRStore(t),
			wantErrSub: "issuer is required",
		},
		{
			name: "neither discovery_url nor registration_endpoint set",
			req: &Request{
				Issuer: "https://example.com",
			},
			cache:      newMemoryDCRStore(t),
			wantErrSub: "must set either discovery_url or registration_endpoint",
		},
		{
			name: "both discovery_url and registration_endpoint set",
			req: &Request{
				Issuer:               "https://example.com",
				DiscoveryURL:         "https://example.com/.well-known/oauth-authorization-server",
				RegistrationEndpoint: "https://example.com/register",
			},
			cache:      newMemoryDCRStore(t),
			wantErrSub: "mutually exclusive",
		},
		{
			name: "nil cache",
			req: &Request{
				Issuer:               "https://example.com",
				RegistrationEndpoint: "https://example.com/register",
			},
			cache:      nil,
			wantErrSub: "credential store is required",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ResolveCredentials(context.Background(), tc.req, tc.cache)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErrSub)
		})
	}
}

func TestResolveUpstreamRedirectURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		configured string
		issuer     string
		expect     string
		wantErr    bool
	}{
		{
			name:       "defaults from issuer",
			configured: "",
			issuer:     "https://idp.example.com",
			expect:     "https://idp.example.com/oauth/callback",
		},
		{
			name:       "explicit https accepted",
			configured: "https://app.example.com/cb",
			issuer:     "https://idp.example.com",
			expect:     "https://app.example.com/cb",
		},
		{
			name:       "explicit loopback http accepted",
			configured: "http://localhost:8080/cb",
			issuer:     "https://idp.example.com",
			expect:     "http://localhost:8080/cb",
		},
		{
			name:       "explicit http non-loopback rejected",
			configured: "http://evil.example.com/cb",
			issuer:     "https://idp.example.com",
			wantErr:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveUpstreamRedirectURI(tc.configured, tc.issuer)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.expect, got)
		})
	}
}

// TestResolveDCRCredentials_DiscoveryURLHonoured verifies that the resolver
// fetches the operator-configured discovery URL exactly, rather than
// deriving well-known paths from the issuer. This is the behaviour that
// matters for multi-tenant IdPs where the configured URL and the
// issuer-derived paths disagree.
func TestResolveDCRCredentials_DiscoveryURLHonoured(t *testing.T) {
	t.Parallel()

	var discoveryPath string
	var discoveryHits int32
	var wellKnownHits int32
	mux := http.NewServeMux()
	// Mount well-known endpoints as tripwires — they must NOT be contacted
	// when DiscoveryURL points elsewhere.
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(_ http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&wellKnownHits, 1)
	})
	mux.HandleFunc("/.well-known/openid-configuration", func(_ http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&wellKnownHits, 1)
	})
	// Mount the operator-configured discovery URL at a tenant-aware path
	// that the well-known fallback would never derive from the issuer.
	var server *httptest.Server
	mux.HandleFunc("/tenants/acme/metadata", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&discoveryHits, 1)
		discoveryPath = r.URL.Path
		md := oauthproto.AuthorizationServerMetadata{
			Issuer:                server.URL,
			AuthorizationEndpoint: server.URL + "/authorize",
			TokenEndpoint:         server.URL + "/token",
			JWKSURI:               server.URL + "/jwks",
			RegistrationEndpoint:  server.URL + "/register",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(md)
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"client_id":"tenant-client"}`))
	})
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cache := newMemoryDCRStore(t)
	issuer := server.URL
	req := &Request{
		Issuer:       issuer,
		Scopes:       []string{"openid"},
		DiscoveryURL: issuer + "/tenants/acme/metadata",
	}

	res, err := ResolveCredentials(context.Background(), req, cache)
	require.NoError(t, err)
	assert.Equal(t, "tenant-client", res.ClientID)
	assert.Equal(t, int32(1), atomic.LoadInt32(&discoveryHits),
		"DiscoveryURL must be fetched exactly once")
	assert.Equal(t, "/tenants/acme/metadata", discoveryPath,
		"resolver must fetch the operator-configured DiscoveryURL, not a derived well-known path")
	assert.Equal(t, int32(0), atomic.LoadInt32(&wellKnownHits),
		"well-known discovery fallback must NOT be contacted when DiscoveryURL is set")
}

// TestResolveDCRCredentials_DiscoveryURLIssuerMismatchRejected verifies that
// the resolver enforces RFC 8414 §3.3 issuer equality even when the caller
// pins the discovery URL — a document that advertises a different issuer is
// rejected.
func TestResolveDCRCredentials_DiscoveryURLIssuerMismatchRejected(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/metadata", func(w http.ResponseWriter, _ *http.Request) {
		// Advertise a different issuer than the caller's.
		md := oauthproto.AuthorizationServerMetadata{
			Issuer:               "https://different.example.com",
			TokenEndpoint:        "https://different.example.com/token",
			RegistrationEndpoint: "https://different.example.com/register",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(md)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cache := newMemoryDCRStore(t)
	issuer := server.URL
	req := &Request{
		Issuer:       issuer,
		Scopes:       []string{"openid"},
		DiscoveryURL: issuer + "/metadata",
	}

	_, err := ResolveCredentials(context.Background(), req, cache)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "issuer mismatch")
}

// TestResolveDCRCredentials_DiscoveredScopesFallback verifies that when the
// caller leaves rc.Scopes empty, the resolver sends the scopes advertised
// by the upstream in scopes_supported.
func TestResolveDCRCredentials_DiscoveredScopesFallback(t *testing.T) {
	t.Parallel()

	var gotBody []byte
	server := newDCRTestServer(t, dcrTestHandlerConfig{
		scopesSupported: []string{"openid", "profile", "email"},
		observeRegistration: func(_ *http.Request, body []byte) {
			gotBody = body
		},
	})
	cache := newMemoryDCRStore(t)
	issuer := server.URL
	req := &Request{
		// Scopes intentionally left empty so the resolver falls back to
		// the discovered scopes_supported.
		Issuer:       issuer,
		DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
	}

	_, err := ResolveCredentials(context.Background(), req, cache)
	require.NoError(t, err)

	var dcrReq oauthproto.DynamicClientRegistrationRequest
	require.NoError(t, json.Unmarshal(gotBody, &dcrReq))
	assert.ElementsMatch(t, []string{"openid", "profile", "email"}, []string(dcrReq.Scopes),
		"registration request must carry the discovered scopes_supported")
}

// TestResolveDCRCredentials_EmptyScopesOmitted verifies that when neither
// rc.Scopes nor metadata.ScopesSupported provides any scopes, the
// registration succeeds and the request body omits the scope field.
func TestResolveDCRCredentials_EmptyScopesOmitted(t *testing.T) {
	t.Parallel()

	var gotBody []byte
	server := newDCRTestServer(t, dcrTestHandlerConfig{
		// Neither scopesSupported nor rc.Scopes — the "empty scope" branch.
		observeRegistration: func(_ *http.Request, body []byte) {
			gotBody = body
		},
	})
	cache := newMemoryDCRStore(t)
	issuer := server.URL
	req := &Request{
		Issuer:       issuer,
		DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
	}

	res, err := ResolveCredentials(context.Background(), req, cache)
	require.NoError(t, err)
	assert.Equal(t, "test-client-id", res.ClientID)

	// The scope field must be omitted (omitempty) rather than sent as an
	// empty string — an empty string would violate RFC 7591 §2, and
	// ScopeList's MarshalJSON correctly relies on omitempty.
	var raw map[string]any
	require.NoError(t, json.Unmarshal(gotBody, &raw))
	_, present := raw["scope"]
	assert.False(t, present, "registration request must omit the scope field when no scopes are configured")
}

// TestResolveDCRCredentials_UpstreamIssuerDerivedFromDiscoveryURL verifies
// the production case: the function-param `issuer` (this auth server's
// issuer) differs from the upstream's issuer, and the resolver still
// completes DCR by deriving the upstream's expected issuer from the
// DiscoveryURL itself rather than reusing the caller-supplied issuer for
// RFC 8414 §3.3 verification.
//
// Pre-fix this test would have failed with `issuer mismatch (RFC 8414 §3.3):
// expected "https://our-auth.example", got "<server.URL>"`, because the
// resolver used the caller's issuer as expectedIssuer.
func TestResolveDCRCredentials_UpstreamIssuerDerivedFromDiscoveryURL(t *testing.T) {
	t.Parallel()

	server := newDCRTestServer(t, dcrTestHandlerConfig{
		tokenEndpointAuthMethodsSupported: []string{"client_secret_basic"},
	})
	cache := newMemoryDCRStore(t)

	// Caller-supplied issuer names this auth server, NOT the upstream.
	// Production wiring always passes its own issuer here (see
	// embeddedauthserver.go: buildUpstreamConfigs(... cfg.Issuer ...)).
	ourIssuer := "https://our-auth.example.com"

	req := &Request{
		Issuer: ourIssuer,
		// Explicit redirect URI so the resolver does not try to default
		// it from ourIssuer (which would still work, but isolating the
		// concern under test keeps the failure mode crisp).
		RedirectURI:  "https://our-auth.example.com/oauth/callback",
		Scopes:       []string{"openid"},
		DiscoveryURL: server.URL + "/.well-known/oauth-authorization-server",
	}

	res, err := ResolveCredentials(context.Background(), req, cache)
	require.NoError(t, err,
		"resolver must derive expectedIssuer from DiscoveryURL, not from the caller's issuer")
	assert.Equal(t, "test-client-id", res.ClientID)
	assert.Equal(t, server.URL+"/authorize", res.AuthorizationEndpoint)
	assert.Equal(t, server.URL+"/token", res.TokenEndpoint)
}

func TestDeriveExpectedIssuerFromDiscoveryURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		discoveryURL string
		want         string
		wantErr      bool
	}{
		{
			name:         "oauth well-known suffix at host root",
			discoveryURL: "https://mcp.atlassian.com/.well-known/oauth-authorization-server",
			want:         "https://mcp.atlassian.com",
		},
		{
			name:         "oidc well-known suffix at host root",
			discoveryURL: "https://accounts.example.com/.well-known/openid-configuration",
			want:         "https://accounts.example.com",
		},
		{
			name:         "oauth well-known suffix with tenant path prefix",
			discoveryURL: "https://idp.example.com/tenants/acme/.well-known/oauth-authorization-server",
			want:         "https://idp.example.com/tenants/acme",
		},
		{
			name:         "oidc well-known suffix with tenant path prefix",
			discoveryURL: "https://idp.example.com/tenants/acme/.well-known/openid-configuration",
			want:         "https://idp.example.com/tenants/acme",
		},
		{
			// RFC 8414 §3.1 path-insertion: well-known segment between host
			// and tenant path. Issuer reconstructed as origin + tenant path.
			// Matches the shape served by Datadog's MCP authorization server
			// (mcp.us5.datadoghq.com) and any other provider with a path-
			// component issuer that follows the RFC literally.
			name:         "oauth well-known path-insertion (RFC 8414 §3.1)",
			discoveryURL: "https://mcp.us5.datadoghq.com/.well-known/oauth-authorization-server/v1/mcp",
			want:         "https://mcp.us5.datadoghq.com/v1/mcp",
		},
		{
			name:         "oauth well-known path-insertion with multi-segment tenant",
			discoveryURL: "https://idp.example.com/.well-known/oauth-authorization-server/tenants/acme",
			want:         "https://idp.example.com/tenants/acme",
		},
		{
			name:         "oidc well-known path-insertion",
			discoveryURL: "https://idp.example.com/.well-known/openid-configuration/tenants/acme",
			want:         "https://idp.example.com/tenants/acme",
		},
		{
			// Trailing-slash edge case: hits the HasPrefix arm (path doesn't end
			// at the bare suffix) but has no tenant after it. Without the empty-
			// path normalisation, TrimPrefix would leave a stray "/" and produce
			// a spurious "https://host/" that fails the RFC 8414 §3.3 byte
			// equality check.
			name:         "oauth well-known with trailing slash normalises to origin",
			discoveryURL: "https://mcp.example.com/.well-known/oauth-authorization-server/",
			want:         "https://mcp.example.com",
		},
		{
			name:         "oidc well-known with trailing slash normalises to origin",
			discoveryURL: "https://idp.example.com/.well-known/openid-configuration/",
			want:         "https://idp.example.com",
		},
		{
			name:         "non-well-known path falls back to origin",
			discoveryURL: "https://idp.example.com/tenants/acme/metadata",
			want:         "https://idp.example.com",
		},
		{
			name:         "query and fragment are stripped",
			discoveryURL: "https://idp.example.com/.well-known/oauth-authorization-server?x=1#frag",
			want:         "https://idp.example.com",
		},
		{
			name:         "empty url is rejected",
			discoveryURL: "",
			wantErr:      true,
		},
		{
			name:         "missing scheme is rejected",
			discoveryURL: "idp.example.com/.well-known/oauth-authorization-server",
			wantErr:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := deriveExpectedIssuerFromDiscoveryURL(tc.discoveryURL)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// countingStore is a CredentialStore decorator that counts the number of
// Get calls that returned a hit. The singleflight coalescing test uses it
// to assert that no concurrent caller observed a cache hit during the run:
// a hit during the test would mean a goroutine raced past the gate, took
// the cache-lookup short-circuit instead of joining the singleflight, and
// silently weakened the test's coverage.
type countingStore struct {
	inner CredentialStore
	hits  atomic.Int32
}

func (c *countingStore) Get(ctx context.Context, key Key) (*Resolution, bool, error) {
	res, ok, err := c.inner.Get(ctx, key)
	if ok {
		c.hits.Add(1)
	}
	return res, ok, err
}

func (c *countingStore) Put(ctx context.Context, key Key, res *Resolution) error {
	return c.inner.Put(ctx, key, res)
}

// TestResolveDCRCredentials_SingleflightCoalescesConcurrentCallers pins the
// behaviour that N concurrent callers for the same Key result in exactly
// one RegisterClientDynamically call against the upstream — preventing the
// orphaned-registration class of bug raised in PR #5042 review.
//
// "Exactly one registration" is necessary but not sufficient to prove the
// singleflight coalescing path actually fired: a late-arriving goroutine
// that reached ResolveCredentials after the leader's cache.Put would
// short-circuit through lookupCachedResolution, take the cache hit, and
// still leave registrationCalls == 1. A countingStore wrapper makes that
// regression loud — we assert no caller observed a cache hit, so any timing
// slip fails the test instead of silently weakening coverage.
func TestResolveDCRCredentials_SingleflightCoalescesConcurrentCallers(t *testing.T) {
	t.Parallel()

	// gate blocks the registration handler until the test releases it,
	// guaranteeing all goroutines pile up at the singleflight before any
	// has a chance to finish and populate the cache.
	gate := make(chan struct{})

	var registrationCalls int32
	server := newDCRTestServer(t, dcrTestHandlerConfig{
		observeRegistration: func(_ *http.Request, _ []byte) {
			<-gate
			atomic.AddInt32(&registrationCalls, 1)
		},
	})

	cache := &countingStore{inner: newMemoryDCRStore(t)}
	issuer := server.URL
	req := &Request{
		Issuer:       issuer,
		Scopes:       []string{"openid", "profile"},
		DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
	}

	const N = 8
	results := make([]*Resolution, N)
	errs := make([]error, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			res, err := ResolveCredentials(context.Background(), req, cache)
			results[idx] = res
			errs[idx] = err
		}(i)
	}

	// Release the gate so every blocked handler can proceed. Even if Go
	// scheduled the leader's handler concurrently with the followers'
	// arrival, only the leader actually invokes the handler — the followers
	// wait inside singleflight.Do.
	//
	// 250 ms gives every goroutine slack to reach singleflight.Do under CI
	// load before the gate releases. If this still races, the countingStore
	// assertion below fails loudly rather than silently weakening coverage.
	time.Sleep(250 * time.Millisecond)
	close(gate)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for concurrent ResolveCredentials goroutines")
	}

	for i := 0; i < N; i++ {
		require.NoError(t, errs[i], "goroutine %d errored", i)
		require.NotNil(t, results[i], "goroutine %d got nil resolution", i)
		assert.Equal(t, "test-client-id", results[i].ClientID)
	}
	assert.EqualValues(t, 1, atomic.LoadInt32(&registrationCalls),
		"expected exactly one registration despite %d concurrent callers; got %d",
		N, atomic.LoadInt32(&registrationCalls))
	assert.EqualValues(t, 0, cache.hits.Load(),
		"no goroutine should have observed a cache hit; if any did, the gate window "+
			"was too short and a late-arriver took the lookupCachedResolution "+
			"short-circuit instead of exercising the singleflight coalescing path")
}

// TestSynthesiseRegistrationEndpoint_PreservesIssuerPath guards the fix for
// PR #5042 review comment #2: an issuer with a tenant prefix must surface
// in the synthesised registration URL so DCR-on-multi-tenant providers
// register at the correct tenant-aware path.
func TestSynthesiseRegistrationEndpoint_PreservesIssuerPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		issuer string
		want   string
	}{
		{
			name:   "host-only issuer",
			issuer: "https://idp.example.com",
			want:   "https://idp.example.com/register",
		},
		{
			name:   "trailing slash on host-only issuer is normalised",
			issuer: "https://idp.example.com/",
			want:   "https://idp.example.com/register",
		},
		{
			name:   "tenant prefix preserved",
			issuer: "https://idp.example.com/tenants/acme",
			want:   "https://idp.example.com/tenants/acme/register",
		},
		{
			name:   "tenant prefix with trailing slash normalised",
			issuer: "https://idp.example.com/tenants/acme/",
			want:   "https://idp.example.com/tenants/acme/register",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := synthesiseRegistrationEndpoint(tc.issuer)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestResolveUpstreamRedirectURI_PreservesIssuerPath is the companion to
// TestSynthesiseRegistrationEndpoint_PreservesIssuerPath for the redirect
// URI defaulting path: a tenant-prefixed issuer must not get its path
// stripped when /oauth/callback is appended.
func TestResolveUpstreamRedirectURI_PreservesIssuerPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		issuer string
		want   string
	}{
		{
			name:   "host-only issuer",
			issuer: "https://thv.example.com",
			want:   "https://thv.example.com/oauth/callback",
		},
		{
			name:   "tenant prefix preserved",
			issuer: "https://thv.example.com/tenants/acme",
			want:   "https://thv.example.com/tenants/acme/oauth/callback",
		},
		{
			name:   "trailing slash normalised",
			issuer: "https://thv.example.com/tenants/acme/",
			want:   "https://thv.example.com/tenants/acme/oauth/callback",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveUpstreamRedirectURI("", tc.issuer)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestResolveDCREndpoints_DirectRegistrationEndpointValidated covers
// PR #5042 review comment #10: the cfg.RegistrationEndpoint short-circuit
// branch validates the URL locally before performRegistration constructs a
// bearer-token transport for it. Non-HTTPS or malformed values must be
// rejected up front, not deep inside oauthproto.
func TestResolveDCREndpoints_DirectRegistrationEndpointValidated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		registrationEndpoint string
		wantErrSub           string
	}{
		{
			name:                 "http non-loopback rejected",
			registrationEndpoint: "http://idp.example.com/register",
			wantErrSub:           "must use https",
		},
		{
			name:                 "missing scheme rejected",
			registrationEndpoint: "idp.example.com/register",
			wantErrSub:           "missing scheme or host",
		},
		{
			name:                 "loopback http accepted",
			registrationEndpoint: "http://127.0.0.1:8080/register",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := &Request{RegistrationEndpoint: tc.registrationEndpoint}
			_, err := resolveDCREndpoints(context.Background(), req)
			if tc.wantErrSub == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErrSub)
		})
	}
}

// TestEndpointsFromMetadata_RejectsInsecureDiscoveredEndpoints covers
// PR #5042 review comment #13: a self-consistent metadata document that
// advertises an http:// authorization or token endpoint must be rejected
// rather than silently flowing through to the auth-code/token-exchange
// path. A compromised TLS connection to the metadata host is the threat
// model.
func TestEndpointsFromMetadata_RejectsInsecureDiscoveredEndpoints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		metadata   *oauthproto.AuthorizationServerMetadata
		wantErrSub string
	}{
		{
			name: "http authorization_endpoint rejected",
			metadata: &oauthproto.AuthorizationServerMetadata{
				Issuer:                "https://idp.example.com",
				AuthorizationEndpoint: "http://idp.example.com/authorize",
				TokenEndpoint:         "https://idp.example.com/token",
				RegistrationEndpoint:  "https://idp.example.com/register",
			},
			wantErrSub: "authorization_endpoint",
		},
		{
			name: "http token_endpoint rejected",
			metadata: &oauthproto.AuthorizationServerMetadata{
				Issuer:                "https://idp.example.com",
				AuthorizationEndpoint: "https://idp.example.com/authorize",
				TokenEndpoint:         "http://idp.example.com/token",
				RegistrationEndpoint:  "https://idp.example.com/register",
			},
			wantErrSub: "token_endpoint",
		},
		{
			name: "missing authorization_endpoint rejected",
			metadata: &oauthproto.AuthorizationServerMetadata{
				Issuer:               "https://idp.example.com",
				TokenEndpoint:        "https://idp.example.com/token",
				RegistrationEndpoint: "https://idp.example.com/register",
			},
			wantErrSub: "authorization_endpoint is required",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := endpointsFromMetadata(tc.metadata, nil, "https://idp.example.com")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErrSub)
		})
	}
}

// failingDCRStore is a test double whose Get and Put always fail. Used by
// TestResolveDCRCredentials_CacheFailureWraps* below to exercise the wrap
// messages that operators see when the store backend errors at runtime.
type failingDCRStore struct {
	getErr error
	putErr error
}

func (f failingDCRStore) Get(_ context.Context, _ Key) (*Resolution, bool, error) {
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	return nil, false, nil
}

func (f failingDCRStore) Put(_ context.Context, _ Key, _ *Resolution) error {
	return f.putErr
}

// TestResolveDCRCredentials_CacheGetFailureWrapped covers PR #5042 review
// comment #12 for the cache.Get error path. When the store backend fails
// (e.g. a Redis network error in Phase 3), the resolver wraps the error
// with the operator-debugging contract message "dcr: cache lookup".
func TestResolveDCRCredentials_CacheGetFailureWrapped(t *testing.T) {
	t.Parallel()

	storeErr := errors.New("simulated backend failure")
	store := failingDCRStore{getErr: storeErr}

	req := &Request{
		Issuer:               "https://idp.example.com",
		RegistrationEndpoint: "https://idp.example.com/register",
	}

	_, err := ResolveCredentials(context.Background(), req, store)
	require.Error(t, err)
	assert.ErrorIs(t, err, storeErr,
		"cache.Get error must be wrapped with %%w so callers can inspect the cause")
	assert.Contains(t, err.Error(), dcrStepCacheRead,
		"step identifier is part of the operator-debugging contract")
	assert.Contains(t, err.Error(), "cache lookup",
		"the wrap message is part of the operator-debugging contract")
}

// TestResolveDCRCredentials_CachePutFailureWrapped covers PR #5042 review
// comment #12 for the cache.Put error path. The path runs after a
// successful registration, so we route the test through a real upstream
// httptest server and only make Put fail.
func TestResolveDCRCredentials_CachePutFailureWrapped(t *testing.T) {
	t.Parallel()

	server := newDCRTestServer(t, dcrTestHandlerConfig{})

	storeErr := errors.New("simulated put backend failure")
	store := failingDCRStore{putErr: storeErr}

	req := &Request{
		Issuer:       server.URL,
		Scopes:       []string{"openid"},
		DiscoveryURL: server.URL + "/.well-known/oauth-authorization-server",
	}

	_, err := ResolveCredentials(context.Background(), req, store)
	require.Error(t, err)
	assert.ErrorIs(t, err, storeErr,
		"cache.Put error must be wrapped with %%w so callers can inspect the cause")
	assert.Contains(t, err.Error(), dcrStepCacheWrite,
		"step identifier is part of the operator-debugging contract")
	assert.Contains(t, err.Error(), "cache put",
		"the wrap message is part of the operator-debugging contract")
}

// TestBuildResolution_PopulatesRFC7591ExpiryFields covers the conversion of
// the int64 epoch fields client_id_issued_at and client_secret_expires_at
// into time.Time on Resolution. The wire convention "0 means absent /
// does not expire" is preserved as the zero time.Time.
func TestBuildResolution_PopulatesRFC7591ExpiryFields(t *testing.T) {
	t.Parallel()

	const (
		issuedEpoch  int64 = 1_700_000_000 // 2023-11-14T22:13:20Z
		expiresEpoch int64 = 1_800_000_000 // 2027-01-15T08:00:00Z
	)

	tests := []struct {
		name          string
		issuedAt      int64
		expiresAt     int64
		wantIssuedAt  time.Time
		wantExpiresAt time.Time
	}{
		{
			name:          "both fields populated",
			issuedAt:      issuedEpoch,
			expiresAt:     expiresEpoch,
			wantIssuedAt:  time.Unix(issuedEpoch, 0).UTC(),
			wantExpiresAt: time.Unix(expiresEpoch, 0).UTC(),
		},
		{
			name:          "client_secret_expires_at zero means does-not-expire",
			issuedAt:      issuedEpoch,
			expiresAt:     0,
			wantIssuedAt:  time.Unix(issuedEpoch, 0).UTC(),
			wantExpiresAt: time.Time{},
		},
		{
			name:          "both fields omitted by upstream",
			issuedAt:      0,
			expiresAt:     0,
			wantIssuedAt:  time.Time{},
			wantExpiresAt: time.Time{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resolution := buildResolution(
				&oauthproto.DynamicClientRegistrationResponse{
					ClientID:              "id",
					ClientSecret:          "secret",
					ClientIDIssuedAt:      tc.issuedAt,
					ClientSecretExpiresAt: tc.expiresAt,
				},
				&dcrEndpoints{
					authorizationEndpoint: "https://idp.example.com/authorize",
					tokenEndpoint:         "https://idp.example.com/token",
				},
				"client_secret_basic",
				"https://idp.example.com/oauth/callback",
			)
			assert.Equal(t, tc.wantIssuedAt, resolution.ClientIDIssuedAt)
			assert.Equal(t, tc.wantExpiresAt, resolution.ClientSecretExpiresAt)
		})
	}
}

// TestResolveDCRCredentials_RefetchesOnExpiredCachedSecret pins the fix for
// the cache-serves-expired-secrets bug: when an entry's
// ClientSecretExpiresAt has passed, lookupCachedResolution treats it as a
// miss so registerAndCache re-runs and overwrites the stale entry. Without
// this, the cached secret would be served indefinitely past the upstream-
// asserted expiry and every token-endpoint call would 401 with no signal
// back to the resolver.
func TestResolveDCRCredentials_RefetchesOnExpiredCachedSecret(t *testing.T) {
	t.Parallel()

	var registrationCalls int32
	server := newDCRTestServer(t, dcrTestHandlerConfig{
		// Issue a secret that expired one minute ago. Every fresh
		// registration call will produce an already-expired entry; the
		// resolver will refetch on every Resolve as a result.
		clientSecretExpiresAt: time.Now().Add(-time.Minute).Unix(),
		observeRegistration: func(_ *http.Request, _ []byte) {
			atomic.AddInt32(&registrationCalls, 1)
		},
	})

	cache := newMemoryDCRStore(t)
	issuer := server.URL
	req := &Request{
		Issuer:       issuer,
		Scopes:       []string{"openid"},
		DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
	}

	// First call: registers, populates cache with already-expired entry.
	res1, err := ResolveCredentials(context.Background(), req, cache)
	require.NoError(t, err)
	require.NotNil(t, res1)
	require.False(t, res1.ClientSecretExpiresAt.IsZero(),
		"upstream advertised an expiry — the resolution must echo it")
	require.True(t, time.Now().After(res1.ClientSecretExpiresAt),
		"test setup should have produced an already-expired secret")
	require.EqualValues(t, 1, atomic.LoadInt32(&registrationCalls))

	// Second call: the cached entry is expired, so the resolver must refetch.
	res2, err := ResolveCredentials(context.Background(), req, cache)
	require.NoError(t, err)
	require.NotNil(t, res2)
	assert.EqualValues(t, 2, atomic.LoadInt32(&registrationCalls),
		"expired cache entry must trigger a re-registration; got %d total calls",
		atomic.LoadInt32(&registrationCalls))
}

// TestResolveDCRCredentials_HonoursFutureExpiryAndZero pins that
// lookupCachedResolution does NOT refetch when the cached secret is still
// valid — either because the upstream-asserted expiry is in the future, or
// because the upstream omitted client_secret_expires_at (zero ⇒ "does not
// expire" per RFC 7591 §3.2.1). The cache hit path is the hot path and a
// regression here would silently increase upstream load.
func TestResolveDCRCredentials_HonoursFutureExpiryAndZero(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		expiresAt int64
	}{
		{name: "future expiry served from cache", expiresAt: time.Now().Add(time.Hour).Unix()},
		{name: "zero (does not expire) served from cache", expiresAt: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var registrationCalls int32
			server := newDCRTestServer(t, dcrTestHandlerConfig{
				clientSecretExpiresAt: tc.expiresAt,
				observeRegistration: func(_ *http.Request, _ []byte) {
					atomic.AddInt32(&registrationCalls, 1)
				},
			})
			cache := newMemoryDCRStore(t)
			issuer := server.URL
			req := &Request{
				Issuer:       issuer,
				Scopes:       []string{"openid"},
				DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
			}

			_, err := ResolveCredentials(context.Background(), req, cache)
			require.NoError(t, err)
			_, err = ResolveCredentials(context.Background(), req, cache)
			require.NoError(t, err)

			assert.EqualValues(t, 1, atomic.LoadInt32(&registrationCalls),
				"second call must hit the cache; got %d total registrations",
				atomic.LoadInt32(&registrationCalls))
		})
	}
}

// panickingPutDCRStore is a test double whose Put panics with a fixed
// value. Get is a normal cache miss so callers reach the singleflight
// closure and trigger the panic via cache.Put inside registerAndCache.
type panickingPutDCRStore struct {
	panicValue any
}

func (panickingPutDCRStore) Get(_ context.Context, _ Key) (*Resolution, bool, error) {
	return nil, false, nil
}

func (s panickingPutDCRStore) Put(_ context.Context, _ Key, _ *Resolution) error {
	panic(s.panicValue)
}

// TestResolveDCRCredentials_RecoversPanicInsideSingleflight pins the
// behaviour that a panic inside the singleflight closure does not propagate
// up as a panic to either the leader goroutine or any of the followers.
// singleflight.Group re-panics the leader's panic in every follower, so
// without the recover N concurrent callers for the same Key would all
// crash with the same value. The defer/recover converts the panic to a
// *dcrStepError(dcrStepRegister, ..., Stack: <captured>); the boundary
// caller's LogStepError emits the single Error record and every caller
// gets the same wrapped error.
func TestResolveDCRCredentials_RecoversPanicInsideSingleflight(t *testing.T) {
	t.Parallel()

	server := newDCRTestServer(t, dcrTestHandlerConfig{})
	store := panickingPutDCRStore{panicValue: "boom"}

	issuer := server.URL
	req := &Request{
		Issuer:       issuer,
		Scopes:       []string{"openid"},
		DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
	}

	const N = 6
	var wg sync.WaitGroup
	wg.Add(N)
	errs := make([]error, N)
	panicked := make([]bool, N)

	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			defer func() {
				// If the recover inside the singleflight closure is
				// missing, the panic re-propagates here. Capture it so
				// the assertion below produces a clear failure message
				// rather than a runtime crash that taints other tests.
				if r := recover(); r != nil {
					panicked[idx] = true
				}
			}()
			_, errs[idx] = ResolveCredentials(context.Background(), req, store)
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for concurrent callers")
	}

	for i := 0; i < N; i++ {
		require.False(t, panicked[i],
			"goroutine %d observed an un-recovered panic from the singleflight closure", i)
		require.Error(t, errs[i],
			"goroutine %d should have received an error converted from the panic", i)
		assert.Contains(t, errs[i].Error(), "panicked",
			"goroutine %d's error must mention the panic so operators can correlate; got %q",
			i, errs[i].Error())
		assert.Contains(t, errs[i].Error(), "boom",
			"goroutine %d's error must include the panic value so the cause is recoverable", i)

		// The captured stack and dcrStepRegister tag must travel with the
		// returned error so the boundary log (LogStepError) emits a
		// single Error record without a duplicate in-defer log.
		var stepErr *dcrStepError
		require.True(t, errors.As(errs[i], &stepErr),
			"goroutine %d's error must wrap a *dcrStepError; got %T", i, errs[i])
		assert.Equal(t, dcrStepRegister, stepErr.Step,
			"goroutine %d's wrapped error must carry the dcrStepRegister step", i)
		assert.NotEmpty(t, stepErr.Stack,
			"goroutine %d's wrapped error must include the captured panic stack", i)
	}
}

func TestDcrStepError(t *testing.T) {
	t.Parallel()

	t.Run("Error formats step and cause", func(t *testing.T) {
		t.Parallel()

		cause := fmt.Errorf("boom")
		e := newDCRStepError(dcrStepRegister, "https://as", "https://app/cb", cause)
		assert.Equal(t, "dcr: dcr_call: boom", e.Error())
	})

	t.Run("Unwrap returns cause", func(t *testing.T) {
		t.Parallel()

		cause := fmt.Errorf("inner")
		e := newDCRStepError(dcrStepValidate, "", "", cause)
		assert.Same(t, cause, errors.Unwrap(e))
	})

	t.Run("errors.As extracts the typed error", func(t *testing.T) {
		t.Parallel()

		cause := fmt.Errorf("inner")
		e := newDCRStepError(dcrStepCacheRead, "https://as", "https://app/cb", cause)
		wrapped := fmt.Errorf("outer: %w", e)

		var got *dcrStepError
		require.True(t, errors.As(wrapped, &got))
		assert.Equal(t, dcrStepCacheRead, got.Step)
		assert.Equal(t, "https://as", got.Issuer)
		assert.Equal(t, "https://app/cb", got.RedirectURI)
	})

	t.Run("ResolveCredentials wraps every failure in a dcrStepError", func(t *testing.T) {
		t.Parallel()

		// Precondition failure → dcrStepValidate.
		_, err := ResolveCredentials(context.Background(), nil, newMemoryDCRStore(t))
		require.Error(t, err)
		var stepErr *dcrStepError
		require.True(t, errors.As(err, &stepErr))
		assert.Equal(t, dcrStepValidate, stepErr.Step)
	})

	t.Run("malformed DiscoveryURL fails at dcrStepResolveUpstream", func(t *testing.T) {
		t.Parallel()

		// A DiscoveryURL with no scheme/host passes validateResolveInputs
		// (which only checks non-emptiness) and the Issuer-based redirect
		// derivation, then fails in resolveUpstreamKeyIdentity —
		// deriveExpectedIssuerFromDiscoveryURL rejects the missing origin.
		// This is a new failure mode introduced with UpstreamID: a malformed
		// DiscoveryURL now surfaces here rather than later in
		// resolveDCREndpoints, so the step must be dcrStepResolveUpstream.
		req := &Request{
			Issuer:       "https://authserver.example.com",
			Scopes:       []string{"openid"},
			DiscoveryURL: "not-a-url",
		}
		_, err := ResolveCredentials(context.Background(), req, newMemoryDCRStore(t))
		require.Error(t, err)
		var stepErr *dcrStepError
		require.True(t, errors.As(err, &stepErr))
		assert.Equal(t, dcrStepResolveUpstream, stepErr.Step)
	})
}

func TestSanitizeErrorForLog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		in       error
		expected string
	}{
		{
			name:     "nil error returns empty string",
			in:       nil,
			expected: "",
		},
		{
			name:     "no URL passes through unchanged",
			in:       fmt.Errorf("something went wrong"),
			expected: "something went wrong",
		},
		{
			name:     "URL without query is preserved",
			in:       fmt.Errorf("GET https://as.example.com/register failed"),
			expected: "GET https://as.example.com/register failed",
		},
		{
			name:     "URL query is stripped",
			in:       fmt.Errorf("GET https://as.example.com/register?token=leak&foo=bar failed"),
			expected: "GET https://as.example.com/register failed",
		},
		{
			name:     "multiple URLs — only queries are stripped, hosts and paths remain",
			in:       fmt.Errorf("from https://a.example/p1?x=1 to https://b.example/p2?y=2"),
			expected: "from https://a.example/p1 to https://b.example/p2",
		},
		// Trailing punctuation regression cases: the query is stripped
		// but the sentence punctuation adjacent to the URL must be
		// preserved verbatim. Without trimURLTrailingPunctuation the
		// regex's greedy character class would absorb these into the
		// raw query and drop them along with it.
		{
			name:     "trailing comma is preserved when query is stripped",
			in:       fmt.Errorf("error reaching https://as.example.com/register?x=1, retrying."),
			expected: "error reaching https://as.example.com/register, retrying.",
		},
		{
			name:     "trailing period is preserved when query is stripped",
			in:       fmt.Errorf("failed: https://a.example/p?q=1."),
			expected: "failed: https://a.example/p.",
		},
		{
			name:     "trailing closing paren is preserved when query is stripped",
			in:       fmt.Errorf("(see https://ex.com/a?b=1)"),
			expected: "(see https://ex.com/a)",
		},
		{
			name:     "mixed trailing punctuation is preserved when query is stripped",
			in:       fmt.Errorf("at https://ex.com/a?b=1]!"),
			expected: "at https://ex.com/a]!",
		},
		{
			name:     "trailing punctuation on URL without query is still preserved",
			in:       fmt.Errorf("see https://ex.com/a."),
			expected: "see https://ex.com/a.",
		},
		{
			name:     "go http client style quoted URL is sanitised",
			in:       fmt.Errorf(`Get "https://as.example.com/register?token=abc": EOF`),
			expected: `Get "https://as.example.com/register": EOF`,
		},
		{
			name:     "embedded userinfo is stripped",
			in:       fmt.Errorf("connect https://user:pass@host.example/path?q=1 failed"),
			expected: "connect https://host.example/path failed",
		},
		{
			name:     "embedded userinfo without query is stripped",
			in:       fmt.Errorf("connect https://user:pass@host.example/path failed"),
			expected: "connect https://host.example/path failed",
		},
		{
			name:     "fragment is stripped",
			in:       fmt.Errorf("callback https://app.example/cb#access_token=abc failed"),
			expected: "callback https://app.example/cb failed",
		},
		{
			// url.Parse normalises the scheme to lowercase, so the
			// post-sanitisation form matches the canonical scheme casing.
			name:     "uppercase scheme is sanitised",
			in:       fmt.Errorf("GET HTTPS://as.example.com/register?token=leak failed"),
			expected: "GET https://as.example.com/register failed",
		},
		// Redis scheme coverage — the embedded-authserver DCR path
		// persists through pkg/authserver/storage/redis.go, and a
		// redis-go error chain on the Get/Put critical path can embed a
		// sentinel/cluster URL with credentials. Without these the
		// sanitiser would leave the password in the slog.Error attribute.
		{
			name:     "redis URL userinfo is stripped",
			in:       fmt.Errorf("dial redis://user:secret@redis.internal:6379/0 failed"),
			expected: "dial redis://redis.internal:6379/0 failed",
		},
		{
			name:     "rediss URL userinfo is stripped",
			in:       fmt.Errorf("dial rediss://user:secret@redis.internal:6379/0 failed"),
			expected: "dial rediss://redis.internal:6379/0 failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, SanitizeErrorForLog(tc.in))
		})
	}
}
