// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package dcr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// probeUpstream is an authorization server double for cache-hit
// verification: RFC 8414 metadata, a registration endpoint that mints
// "fresh-client-id", a token endpoint whose answer each test chooses, and an
// authorization endpoint whose answer a test sets with setAuthorize (404
// until then).
type probeUpstream struct {
	*httptest.Server
	registrations atomic.Int32
	tokenRequests atomic.Int32
	authzRequests atomic.Int32

	mu        sync.Mutex
	lastToken *http.Request
	lastForm  url.Values
	lastAuthz *http.Request
	authorize http.HandlerFunc
}

func newProbeUpstream(t *testing.T, token http.HandlerFunc, registerStatus int) *probeUpstream {
	t.Helper()
	u := &probeUpstream{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(oauthproto.AuthorizationServerMetadata{
			Issuer:                u.URL,
			AuthorizationEndpoint: u.URL + "/authorize",
			TokenEndpoint:         u.URL + "/token",
			RegistrationEndpoint:  u.URL + "/register",
			// client_secret_post is advertised too so a test can configure
			// it; the preference order still selects client_secret_basic.
			TokenEndpointAuthMethodsSupported: []string{"client_secret_basic", "client_secret_post"},
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		u.registrations.Add(1)
		if registerStatus != http.StatusCreated {
			w.WriteHeader(registerStatus)
			return
		}
		var req oauthproto.DynamicClientRegistrationRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(oauthproto.DynamicClientRegistrationResponse{
			ClientID:                "fresh-client-id",
			ClientSecret:            "fresh-client-secret",
			TokenEndpointAuthMethod: req.TokenEndpointAuthMethod,
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		u.tokenRequests.Add(1)
		require.NoError(t, r.ParseForm())
		u.mu.Lock()
		u.lastToken, u.lastForm = r, r.PostForm
		u.mu.Unlock()
		token(w, r)
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		u.authzRequests.Add(1)
		u.mu.Lock()
		u.lastAuthz = r
		authorize := u.authorize
		u.mu.Unlock()
		if authorize == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		authorize(w, r)
	})
	u.Server = httptest.NewServer(mux)
	t.Cleanup(u.Close)
	return u
}

// setAuthorize chooses the authorization endpoint's answer.
func (u *probeUpstream) setAuthorize(h http.HandlerFunc) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.authorize = h
}

// tokenRequest returns the last token request and its parsed form.
func (u *probeUpstream) tokenRequest() (*http.Request, url.Values) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lastToken, u.lastForm
}

// authorizeRequest returns the last authorization request.
func (u *probeUpstream) authorizeRequest() *http.Request {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lastAuthz
}

func oauthErrorResponse(status int, code string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
	}
}

// preloadCachedRegistration stores a registration for upstream under the key
// ResolveCredentials derives from req and returns that request.
func preloadCachedRegistration(t *testing.T, cache CredentialStore, upstream *probeUpstream, authMethod string) *Request {
	t.Helper()
	scopes := []string{"openid"}
	key := Key{
		Issuer:      upstream.URL,
		UpstreamID:  upstream.URL,
		RedirectURI: upstream.URL + "/oauth/callback",
		ScopesHash:  storage.ScopesHash(scopes),
	}
	_, err := cache.PutIfAbsent(context.Background(), key, &Resolution{
		ClientID:                "cached-client-id",
		ClientSecret:            "cached-client-secret",
		AuthorizationEndpoint:   upstream.URL + "/authorize",
		TokenEndpoint:           upstream.URL + "/token",
		TokenEndpointAuthMethod: authMethod,
	})
	require.NoError(t, err)
	return &Request{
		Issuer:       upstream.URL,
		Scopes:       scopes,
		DiscoveryURL: upstream.URL + "/.well-known/oauth-authorization-server",
	}
}

func TestResolveCredentials_CacheHitVerifiesRegistration(t *testing.T) {
	t.Parallel()

	var redirectFollowed atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectFollowed.Add(1)
	}))
	t.Cleanup(redirectTarget.Close)
	// Cleanups run after the parallel subtests finish.
	t.Cleanup(func() {
		assert.Zero(t, redirectFollowed.Load(), "the probe must not follow redirects with client credentials")
	})

	tests := []struct {
		name              string
		authMethod        string
		token             http.HandlerFunc
		registerStatus    int
		wantClientID      string
		wantErr           bool
		wantRegistrations int32
		wantTokenRequests int32
	}{
		{
			name:       "invalid_grant keeps the live registration",
			authMethod: "client_secret_basic", token: oauthErrorResponse(http.StatusBadRequest, "invalid_grant"),
			wantClientID: "cached-client-id", wantTokenRequests: 1,
		},
		{
			name:       "invalid_client replaces the registration",
			authMethod: "client_secret_basic", token: oauthErrorResponse(http.StatusUnauthorized, "invalid_client"),
			wantClientID: "fresh-client-id", wantRegistrations: 1, wantTokenRequests: 1,
		},
		{
			name:       "client_secret_post client is verified and replaced",
			authMethod: "client_secret_post", token: oauthErrorResponse(http.StatusUnauthorized, "invalid_client"),
			wantClientID: "fresh-client-id", wantRegistrations: 1, wantTokenRequests: 1,
		},
		{
			name:       "public client is verified and replaced",
			authMethod: "none", token: oauthErrorResponse(http.StatusUnauthorized, "invalid_client"),
			wantClientID: "fresh-client-id", wantRegistrations: 1, wantTokenRequests: 1,
		},
		{
			name:       "other OAuth errors keep the registration",
			authMethod: "client_secret_basic", token: oauthErrorResponse(http.StatusBadRequest, "invalid_request"),
			wantClientID: "cached-client-id", wantTokenRequests: 1,
		},
		{
			name:       "server errors keep the registration",
			authMethod: "client_secret_basic",
			token: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantClientID: "cached-client-id", wantTokenRequests: 1,
		},
		{
			name:       "non-OAuth error bodies keep the registration",
			authMethod: "client_secret_basic",
			token: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte("<html>blocked</html>"))
			},
			wantClientID: "cached-client-id", wantTokenRequests: 1,
		},
		{
			name:       "redirects are refused and keep the registration",
			authMethod: "client_secret_basic",
			token: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, redirectTarget.URL, http.StatusTemporaryRedirect)
			},
			wantClientID: "cached-client-id", wantTokenRequests: 1,
		},
		{
			name:         "private_key_jwt clients are not verified",
			authMethod:   "private_key_jwt",
			token:        oauthErrorResponse(http.StatusUnauthorized, "invalid_client"),
			wantClientID: "cached-client-id",
		},
		{
			name:       "failed re-registration is an error",
			authMethod: "client_secret_basic", token: oauthErrorResponse(http.StatusUnauthorized, "invalid_client"),
			registerStatus: http.StatusInternalServerError,
			wantErr:        true, wantRegistrations: 1, wantTokenRequests: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			registerStatus := tt.registerStatus
			if registerStatus == 0 {
				registerStatus = http.StatusCreated
			}
			upstream := newProbeUpstream(t, tt.token, registerStatus)
			cache := newMemoryDCRStore(t)
			req := preloadCachedRegistration(t, cache, upstream, tt.authMethod)

			got, err := ResolveCredentials(context.Background(), req, cache)

			assert.Equal(t, tt.wantRegistrations, upstream.registrations.Load(), "registrations")
			assert.Equal(t, tt.wantTokenRequests, upstream.tokenRequests.Load(), "token requests")
			assert.Zero(t, upstream.authzRequests.Load(), "the default probe uses the token endpoint only")
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantClientID, got.ClientID)

			stored, ok, err := cache.Get(context.Background(), Key{
				Issuer: upstream.URL, UpstreamID: upstream.URL,
				RedirectURI: upstream.URL + "/oauth/callback", ScopesHash: storage.ScopesHash([]string{"openid"}),
			})
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, tt.wantClientID, stored.ClientID, "cache must hold the returned registration")

			if tt.wantTokenRequests == 0 {
				return
			}
			r, form := upstream.tokenRequest()
			assert.Equal(t, "authorization_code", form.Get("grant_type"))
			assert.NotEmpty(t, form.Get("code"))
			user, password, hasBasic := r.BasicAuth()
			switch tt.authMethod {
			case "client_secret_basic":
				require.True(t, hasBasic, "client_secret_basic must authenticate with HTTP Basic")
				assert.Equal(t, "cached-client-id", user)
				assert.Equal(t, "cached-client-secret", password)
			case "client_secret_post":
				assert.False(t, hasBasic)
				assert.Equal(t, "cached-client-id", form.Get("client_id"))
				assert.Equal(t, "cached-client-secret", form.Get("client_secret"))
			case "none":
				assert.False(t, hasBasic)
				assert.Equal(t, "cached-client-id", form.Get("client_id"))
				assert.Empty(t, form.Get("client_secret"), "a public client must not send its cached secret")
			}
		})
	}
}

// TestResolveCredentials_CacheHitProbesConfiguredTokenEndpoint pins that the
// probe goes to the operator-configured token endpoint, not to the endpoint
// recorded in the cached row, so a tampered cache cannot receive the secret.
func TestResolveCredentials_CacheHitProbesConfiguredTokenEndpoint(t *testing.T) {
	t.Parallel()

	var decoyRequests atomic.Int32
	decoy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		decoyRequests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(decoy.Close)

	upstream := newProbeUpstream(t, oauthErrorResponse(http.StatusBadRequest, "invalid_grant"), http.StatusCreated)
	cache := newMemoryDCRStore(t)
	key := Key{
		Issuer: upstream.URL, UpstreamID: upstream.URL,
		RedirectURI: upstream.URL + "/oauth/callback", ScopesHash: storage.ScopesHash([]string{"openid"}),
	}
	_, err := cache.PutIfAbsent(context.Background(), key, &Resolution{
		ClientID:                "cached-client-id",
		ClientSecret:            "cached-client-secret",
		AuthorizationEndpoint:   upstream.URL + "/authorize",
		TokenEndpoint:           decoy.URL + "/token",
		TokenEndpointAuthMethod: "client_secret_basic",
	})
	require.NoError(t, err)

	got, err := ResolveCredentials(context.Background(), &Request{
		Issuer:        upstream.URL,
		Scopes:        []string{"openid"},
		DiscoveryURL:  upstream.URL + "/.well-known/oauth-authorization-server",
		TokenEndpoint: upstream.URL + "/token",
	}, cache)
	require.NoError(t, err)
	assert.Equal(t, "cached-client-id", got.ClientID)
	assert.Equal(t, int32(1), upstream.tokenRequests.Load())
	assert.Zero(t, decoyRequests.Load(), "the cached token endpoint must not receive the probe")
}

// staleReplaceStore simulates a concurrent rewrite of the same row winning
// the compare-and-set: ReplaceIfUnchanged hands back the rejected
// registration instead of the replacement.
type staleReplaceStore struct {
	CredentialStore
}

func (staleReplaceStore) ReplaceIfUnchanged(_ context.Context, _ Key, expected, _ *Resolution) (*Resolution, error) {
	return expected, nil
}

func TestResolveCredentials_RejectedRegistrationStillStoredIsAnError(t *testing.T) {
	t.Parallel()

	upstream := newProbeUpstream(t, oauthErrorResponse(http.StatusUnauthorized, "invalid_client"), http.StatusCreated)
	cache := staleReplaceStore{CredentialStore: newMemoryDCRStore(t)}
	req := preloadCachedRegistration(t, cache, upstream, "client_secret_basic")

	_, err := ResolveCredentials(context.Background(), req, cache)
	require.ErrorContains(t, err, "the rejected registration is still stored")
}

// TestResolveCredentials_CacheHitWithoutReplacerSkipsProbe pins that a store
// which cannot replace a registration gets today's cache-hit behaviour: the
// upstream is never contacted, even when it would reject the cached client.
// The wrapper embeds CredentialStore, as wrappers outside this package do,
// so it also pins that embedding does not pick up Replacer.
func TestResolveCredentials_CacheHitWithoutReplacerSkipsProbe(t *testing.T) {
	t.Parallel()

	upstream := newProbeUpstream(t, oauthErrorResponse(http.StatusUnauthorized, "invalid_client"), http.StatusCreated)
	cache := struct{ CredentialStore }{newMemoryDCRStore(t)}
	_, isReplacer := any(cache).(Replacer)
	require.False(t, isReplacer, "embedding CredentialStore must not provide Replacer")
	req := preloadCachedRegistration(t, cache, upstream, "client_secret_basic")

	got, err := ResolveCredentials(context.Background(), req, cache)
	require.NoError(t, err)
	assert.Equal(t, "cached-client-id", got.ClientID)
	assert.Zero(t, upstream.tokenRequests.Load(), "a store without Replacer must not be probed")
	assert.Zero(t, upstream.registrations.Load())
}

// TestResolveCredentials_CacheHitConfiguredAuthMethod pins
// Request.TokenEndpointAuthMethod on a cache hit: a registration made with
// another method is replaced without probing it, one made with the configured
// method is kept, and a store that cannot replace a registration fails rather
// than handing back a client with the wrong method.
func TestResolveCredentials_CacheHitConfiguredAuthMethod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		cachedAuthMethod  string
		replaceable       bool
		wantClientID      string
		wantRegistrations int32
		wantTokenRequests int32
		wantErrSub        string
	}{
		{
			name:              "registration made with another method is replaced",
			cachedAuthMethod:  "client_secret_basic",
			replaceable:       true,
			wantClientID:      "fresh-client-id",
			wantRegistrations: 1,
		},
		{
			name:              "registration made with the configured method is kept",
			cachedAuthMethod:  "client_secret_post",
			replaceable:       true,
			wantClientID:      "cached-client-id",
			wantTokenRequests: 1,
		},
		{
			name:             "store that cannot replace the registration fails",
			cachedAuthMethod: "client_secret_basic",
			wantErrSub:       "cannot replace it",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			upstream := newProbeUpstream(t, oauthErrorResponse(http.StatusBadRequest, "invalid_grant"), http.StatusCreated)
			cache := func() CredentialStore {
				if tc.replaceable {
					return newMemoryDCRStore(t)
				}
				return struct{ CredentialStore }{newMemoryDCRStore(t)}
			}()
			req := preloadCachedRegistration(t, cache, upstream, tc.cachedAuthMethod)
			req.TokenEndpointAuthMethod = "client_secret_post"

			got, err := ResolveCredentials(context.Background(), req, cache)
			if tc.wantErrSub != "" {
				require.ErrorContains(t, err, tc.wantErrSub)
				assert.Zero(t, upstream.registrations.Load())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantClientID, got.ClientID)
			assert.Equal(t, "client_secret_post", got.TokenEndpointAuthMethod)
			assert.Equal(t, tc.wantRegistrations, upstream.registrations.Load())
			assert.Equal(t, tc.wantTokenRequests, upstream.tokenRequests.Load())
		})
	}
}

// readHookStore runs afterFirstRead once, after its first Get has read the
// row and before that Get returns it. Later Gets never wait for the hook
// (unlike sync.Once.Do, which would block them until it returns).
type readHookStore struct {
	inner interface {
		CredentialStore
		Replacer
	}
	fired          atomic.Bool
	afterFirstRead func()
}

func (s *readHookStore) Get(ctx context.Context, key Key) (*Resolution, bool, error) {
	res, ok, err := s.inner.Get(ctx, key)
	if s.fired.CompareAndSwap(false, true) {
		s.afterFirstRead()
	}
	return res, ok, err
}

func (s *readHookStore) PutIfAbsent(ctx context.Context, key Key, res *Resolution) (*Resolution, error) {
	return s.inner.PutIfAbsent(ctx, key, res)
}

func (s *readHookStore) ReplaceIfUnchanged(ctx context.Context, key Key, expected, replacement *Resolution) (*Resolution, error) {
	return s.inner.ReplaceIfUnchanged(ctx, key, expected, replacement)
}

// TestResolveCredentials_RejectedRegistrationReplacedOncePerProcess covers a
// second caller for the same Key arriving after the first caller has read
// the rejected row. Had the cache been read before the singleflight, the
// first caller would revalidate the row it read after the second caller's
// flight had already replaced it, and register a second client that the
// compare-and-set then discards, orphaned upstream. Reading inside the flight
// makes the second caller share the first caller's flight instead.
func TestResolveCredentials_RejectedRegistrationReplacedOncePerProcess(t *testing.T) {
	t.Parallel()

	upstream := newProbeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if clientID, _, _ := r.BasicAuth(); clientID == "cached-client-id" {
			oauthErrorResponse(http.StatusUnauthorized, "invalid_client")(w, r)
			return
		}
		oauthErrorResponse(http.StatusBadRequest, "invalid_grant")(w, r)
	}, http.StatusCreated)
	inner, ok := newMemoryDCRStore(t).(interface {
		CredentialStore
		Replacer
	})
	require.True(t, ok)
	req := preloadCachedRegistration(t, inner, upstream, "client_secret_basic")

	var (
		second    *Resolution
		secondErr error
	)
	secondDone := make(chan struct{})
	cache := &readHookStore{inner: inner}
	cache.afterFirstRead = func() {
		go func() {
			defer close(secondDone)
			second, secondErr = ResolveCredentials(context.Background(), req, cache)
		}()
		// Give the second caller time to finish on its own if it is not
		// coalesced with this one.
		select {
		case <-secondDone:
		case <-time.After(250 * time.Millisecond):
		}
	}

	first, err := ResolveCredentials(context.Background(), req, cache)
	require.NoError(t, err)
	select {
	case <-secondDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for the second ResolveCredentials call")
	}
	require.NoError(t, secondErr)

	assert.Equal(t, "fresh-client-id", first.ClientID)
	assert.Equal(t, "fresh-client-id", second.ClientID)
	assert.Equal(t, int32(1), upstream.registrations.Load(),
		"the rejected registration must be replaced by exactly one new client")
}

// preloadForAuthorizationProbe stores a confidential registration for
// upstream and returns a request that selects the authorization-endpoint
// probe for it.
func preloadForAuthorizationProbe(t *testing.T, cache CredentialStore, upstream *probeUpstream) *Request {
	t.Helper()
	req := preloadCachedRegistration(t, cache, upstream, "client_secret_basic")
	req.RegistrationProbe = ProbeAuthorizationEndpoint
	return req
}

func TestResolveCredentials_CacheHitAuthorizationEndpointProbe(t *testing.T) {
	t.Parallel()

	var redirectFollowed atomic.Int32
	idp := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectFollowed.Add(1)
	}))
	t.Cleanup(idp.Close)
	// Cleanups run after the parallel subtests finish.
	t.Cleanup(func() {
		assert.Zero(t, redirectFollowed.Load(), "the probe must not follow the redirect to the identity provider")
	})

	statusOnly := func(status int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }
	}
	tests := []struct {
		name              string
		authorize         http.HandlerFunc
		registerStatus    int
		wantClientID      string
		wantErr           bool
		wantRegistrations int32
	}{
		{
			name: "login page keeps the registration",
			authorize: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte("<html>sign in</html>"))
			},
			wantClientID: "cached-client-id",
		},
		{
			name: "redirect to the identity provider keeps the registration",
			authorize: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, idp.URL+"/login", http.StatusFound)
			},
			wantClientID: "cached-client-id",
		},
		{
			name: "redirect back to redirect_uri with an error keeps the registration",
			authorize: func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				http.Redirect(w, r, q.Get("redirect_uri")+"?error=invalid_scope&state="+q.Get("state"), http.StatusFound)
			},
			wantClientID: "cached-client-id",
		},
		{
			// The shape a vendor answered with for a purged public client
			// (its description named the redirect_uri, not the client).
			name:         "direct 400 invalid_request replaces the registration",
			authorize:    oauthErrorResponse(http.StatusBadRequest, "invalid_request"),
			wantClientID: "fresh-client-id", wantRegistrations: 1,
		},
		{
			// The shape a ToolHive auth server answers with (fosite).
			name:         "direct 401 invalid_client replaces the registration",
			authorize:    oauthErrorResponse(http.StatusUnauthorized, "invalid_client"),
			wantClientID: "fresh-client-id", wantRegistrations: 1,
		},
		{
			name:         "direct 403 unauthorized_client replaces the registration",
			authorize:    oauthErrorResponse(http.StatusForbidden, "unauthorized_client"),
			wantClientID: "fresh-client-id", wantRegistrations: 1,
		},
		{
			name: "direct 403 HTML page keeps the registration",
			authorize: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte("<html>Unknown client</html>"))
			},
			wantClientID: "cached-client-id",
		},
		{
			name: "direct 401 from a gateway without a body keeps the registration",
			authorize: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="gateway"`)
				w.WriteHeader(http.StatusUnauthorized)
			},
			wantClientID: "cached-client-id",
		},
		{
			name:         "direct 400 with a non-client error code keeps the registration",
			authorize:    oauthErrorResponse(http.StatusBadRequest, "server_error"),
			wantClientID: "cached-client-id",
		},
		{
			name: "direct 400 with a JSON body that is not an error object keeps the registration",
			authorize: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"message":"Unknown client"}`))
			},
			wantClientID: "cached-client-id",
		},
		{name: "404 keeps the registration", authorize: statusOnly(http.StatusNotFound), wantClientID: "cached-client-id"},
		{name: "405 keeps the registration", authorize: statusOnly(http.StatusMethodNotAllowed), wantClientID: "cached-client-id"},
		{name: "429 keeps the registration", authorize: statusOnly(http.StatusTooManyRequests), wantClientID: "cached-client-id"},
		{name: "500 keeps the registration", authorize: statusOnly(http.StatusInternalServerError), wantClientID: "cached-client-id"},
		{
			name:           "failed re-registration is an error",
			authorize:      oauthErrorResponse(http.StatusBadRequest, "invalid_request"),
			registerStatus: http.StatusInternalServerError,
			wantErr:        true, wantRegistrations: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			registerStatus := tt.registerStatus
			if registerStatus == 0 {
				registerStatus = http.StatusCreated
			}
			// The token endpoint would reject the cached client; the
			// authorization-endpoint probe must never ask it.
			upstream := newProbeUpstream(t, oauthErrorResponse(http.StatusUnauthorized, "invalid_client"), registerStatus)
			upstream.setAuthorize(tt.authorize)
			cache := newMemoryDCRStore(t)
			req := preloadForAuthorizationProbe(t, cache, upstream)

			got, err := ResolveCredentials(context.Background(), req, cache)

			assert.Equal(t, int32(1), upstream.authzRequests.Load(), "authorization requests")
			assert.Zero(t, upstream.tokenRequests.Load(), "token requests")
			assert.Equal(t, tt.wantRegistrations, upstream.registrations.Load(), "registrations")
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantClientID, got.ClientID)

			stored, ok, err := cache.Get(context.Background(), Key{
				Issuer: upstream.URL, UpstreamID: upstream.URL,
				RedirectURI: upstream.URL + "/oauth/callback", ScopesHash: storage.ScopesHash([]string{"openid"}),
			})
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, tt.wantClientID, stored.ClientID, "cache must hold the returned registration")

			r := upstream.authorizeRequest()
			require.NotNil(t, r)
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Empty(t, r.Header.Get("Authorization"), "the probe carries no client credentials")
			q := r.URL.Query()
			assert.Equal(t, "code", q.Get("response_type"))
			assert.Equal(t, "cached-client-id", q.Get("client_id"))
			assert.Equal(t, upstream.URL+"/oauth/callback", q.Get("redirect_uri"))
			assert.Equal(t, "openid", q.Get("scope"))
			assert.Equal(t, "S256", q.Get("code_challenge_method"))
			assert.NotEmpty(t, q.Get("code_challenge"))
			assert.NotEmpty(t, q.Get("state"))
			assert.Empty(t, q.Get("client_secret"))
		})
	}
}

// TestResolveCredentials_CacheHitProbesConfiguredAuthorizationEndpoint pins
// that the probe goes to the operator-configured authorization endpoint, not
// to the endpoint recorded in the cached row.
func TestResolveCredentials_CacheHitProbesConfiguredAuthorizationEndpoint(t *testing.T) {
	t.Parallel()

	var decoyRequests atomic.Int32
	decoy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		decoyRequests.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(decoy.Close)

	upstream := newProbeUpstream(t, oauthErrorResponse(http.StatusUnauthorized, "invalid_client"), http.StatusCreated)
	upstream.setAuthorize(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, upstream.URL+"/login", http.StatusFound)
	})
	cache := newMemoryDCRStore(t)
	key := Key{
		Issuer: upstream.URL, UpstreamID: upstream.URL,
		RedirectURI: upstream.URL + "/oauth/callback", ScopesHash: storage.ScopesHash([]string{"openid"}),
	}
	_, err := cache.PutIfAbsent(context.Background(), key, &Resolution{
		ClientID:                "cached-client-id",
		ClientSecret:            "cached-client-secret",
		AuthorizationEndpoint:   decoy.URL + "/authorize",
		TokenEndpoint:           upstream.URL + "/token",
		TokenEndpointAuthMethod: "client_secret_basic",
	})
	require.NoError(t, err)

	got, err := ResolveCredentials(context.Background(), &Request{
		Issuer:                upstream.URL,
		Scopes:                []string{"openid"},
		DiscoveryURL:          upstream.URL + "/.well-known/oauth-authorization-server",
		AuthorizationEndpoint: upstream.URL + "/authorize",
		RegistrationProbe:     ProbeAuthorizationEndpoint,
	}, cache)
	require.NoError(t, err)
	assert.Equal(t, "cached-client-id", got.ClientID)
	assert.Equal(t, int32(1), upstream.authzRequests.Load())
	assert.Zero(t, decoyRequests.Load(), "the cached authorization endpoint must not receive the probe")
}

func TestResolveCredentials_AuthorizationProbeTransportErrorKeepsRegistration(t *testing.T) {
	t.Parallel()

	upstream := newProbeUpstream(t, oauthErrorResponse(http.StatusUnauthorized, "invalid_client"), http.StatusCreated)
	unreachable := httptest.NewServer(http.NotFoundHandler())
	unreachableURL := unreachable.URL
	unreachable.Close()
	cache := newMemoryDCRStore(t)
	req := preloadForAuthorizationProbe(t, cache, upstream)
	req.AuthorizationEndpoint = unreachableURL + "/authorize"

	got, err := ResolveCredentials(context.Background(), req, cache)
	require.NoError(t, err)
	assert.Equal(t, "cached-client-id", got.ClientID)
	assert.Zero(t, upstream.registrations.Load())
	assert.Zero(t, upstream.tokenRequests.Load())
}

// TestResolveCredentials_ProbeNoneSkipsUpstream pins that ProbeNone returns
// the cached registration without contacting the upstream, even when both
// endpoints would reject the cached client.
func TestResolveCredentials_ProbeNoneSkipsUpstream(t *testing.T) {
	t.Parallel()

	upstream := newProbeUpstream(t, oauthErrorResponse(http.StatusUnauthorized, "invalid_client"), http.StatusCreated)
	upstream.setAuthorize(oauthErrorResponse(http.StatusBadRequest, "invalid_request"))
	cache := newMemoryDCRStore(t)
	req := preloadCachedRegistration(t, cache, upstream, "client_secret_basic")
	req.RegistrationProbe = ProbeNone

	got, err := ResolveCredentials(context.Background(), req, cache)
	require.NoError(t, err)
	assert.Equal(t, "cached-client-id", got.ClientID)
	assert.Zero(t, upstream.tokenRequests.Load())
	assert.Zero(t, upstream.authzRequests.Load())
	assert.Zero(t, upstream.registrations.Load())
}

func TestClassifyAuthorizationProbe(t *testing.T) {
	t.Parallel()

	invalidClient := []byte(`{"error":"invalid_client","error_description":"The requested OAuth 2.0 Client does not exist."}`)
	tests := []struct {
		name     string
		status   int
		body     []byte
		want     registrationStatus
		wantCode string
	}{
		{name: "200 page", status: http.StatusOK, body: []byte("<html>"), want: registrationLive},
		{name: "303 redirect", status: http.StatusSeeOther, want: registrationLive},
		{name: "400 invalid_request", status: http.StatusBadRequest,
			body: []byte(`{"error": "invalid_request", "error_description": "redirect_uri is not in the allowlist"}`),
			want: registrationRejected, wantCode: "invalid_request"},
		{name: "401 invalid_client", status: http.StatusUnauthorized, body: invalidClient,
			want: registrationRejected, wantCode: "invalid_client"},
		{name: "403 unauthorized_client", status: http.StatusForbidden, body: []byte(`{"error":"unauthorized_client"}`),
			want: registrationRejected, wantCode: "unauthorized_client"},
		{name: "400 other error code", status: http.StatusBadRequest, body: []byte(`{"error":"server_error"}`),
			want: registrationUnverified, wantCode: "server_error"},
		{name: "400 error code outside the log-safe alphabet", status: http.StatusBadRequest,
			body: []byte(`{"error":"<script>alert(1)</script>"}`), want: registrationUnverified},
		{name: "403 HTML", status: http.StatusForbidden, body: []byte("<html>Unknown client</html>"), want: registrationUnverified},
		{name: "401 empty body", status: http.StatusUnauthorized, want: registrationUnverified},
		{name: "422 invalid_client", status: http.StatusUnprocessableEntity, body: invalidClient, want: registrationUnverified},
		{name: "404 invalid_client", status: http.StatusNotFound, body: invalidClient, want: registrationUnverified},
		{name: "405", status: http.StatusMethodNotAllowed, want: registrationUnverified},
		{name: "429 invalid_client", status: http.StatusTooManyRequests, body: invalidClient, want: registrationUnverified},
		{name: "502 invalid_client", status: http.StatusBadGateway, body: invalidClient, want: registrationUnverified},
		{name: "100", status: http.StatusContinue, want: registrationUnverified},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, code := classifyAuthorizationProbe(tt.status, tt.body)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantCode, code)
		})
	}
}

// TestAuthorizationProbeErrorAttrs_OmitsServerResponse pins that an
// inconclusive authorization-endpoint answer is logged by status only.
func TestAuthorizationProbeErrorAttrs_OmitsServerResponse(t *testing.T) {
	t.Parallel()

	const secretish = "do-not-log-this-response-text"
	upstream := newProbeUpstream(t, oauthErrorResponse(http.StatusUnauthorized, "invalid_client"), http.StatusCreated)
	upstream.setAuthorize(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(secretish))
	})
	req := preloadForAuthorizationProbe(t, newMemoryDCRStore(t), upstream)

	status, reason := verifyCachedRegistration(context.Background(), req, &Resolution{
		ClientID:                "cached-client-id",
		ClientSecret:            "cached-client-secret",
		AuthorizationEndpoint:   upstream.URL + "/authorize",
		TokenEndpoint:           upstream.URL + "/token",
		TokenEndpointAuthMethod: "client_secret_basic",
		RedirectURI:             upstream.URL + "/oauth/callback",
	})
	require.Equal(t, registrationUnverified, status)
	require.Error(t, reason)
	assert.NotContains(t, reason.Error(), secretish)
	assert.Equal(t, []any{"error", "dcr: authorization endpoint answered HTTP 429"}, probeErrorAttrs(reason))
}

// TestAuthorizationProbeErrorAttrs_RepeatsOnlyTheErrorCode pins that a
// structured error outside the rejection set is reported by status and
// error code, never by its error_description.
func TestAuthorizationProbeErrorAttrs_RepeatsOnlyTheErrorCode(t *testing.T) {
	t.Parallel()

	const secretish = "do-not-log-this-description"
	upstream := newProbeUpstream(t, oauthErrorResponse(http.StatusUnauthorized, "invalid_client"), http.StatusCreated)
	upstream.setAuthorize(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "server_error", "error_description": secretish})
	})
	req := preloadForAuthorizationProbe(t, newMemoryDCRStore(t), upstream)

	status, reason := verifyCachedRegistration(context.Background(), req, &Resolution{
		ClientID:                "cached-client-id",
		AuthorizationEndpoint:   upstream.URL + "/authorize",
		TokenEndpoint:           upstream.URL + "/token",
		TokenEndpointAuthMethod: "none",
		RedirectURI:             upstream.URL + "/oauth/callback",
	})
	require.Equal(t, registrationUnverified, status)
	require.Error(t, reason)
	assert.NotContains(t, reason.Error(), secretish)
	assert.Equal(t, []any{"error", `dcr: authorization endpoint answered HTTP 400 (error "server_error")`}, probeErrorAttrs(reason))
}

// TestResolveCredentials_AuthorizationProbeVerifiesPrivateKeyJWTClients pins
// that the authorization-endpoint probe, which sends no credentials, does
// probe a private_key_jwt client (the token-endpoint probe cannot sign for
// one and skips it).
func TestResolveCredentials_AuthorizationProbeVerifiesPrivateKeyJWTClients(t *testing.T) {
	t.Parallel()

	upstream := newProbeUpstream(t, oauthErrorResponse(http.StatusUnauthorized, "invalid_client"), http.StatusCreated)
	upstream.setAuthorize(oauthErrorResponse(http.StatusBadRequest, "invalid_client"))
	cache := newMemoryDCRStore(t)
	req := preloadCachedRegistration(t, cache, upstream, "private_key_jwt")
	req.RegistrationProbe = ProbeAuthorizationEndpoint

	got, err := ResolveCredentials(context.Background(), req, cache)
	require.NoError(t, err)
	assert.Equal(t, "fresh-client-id", got.ClientID)
	assert.Equal(t, int32(1), upstream.authzRequests.Load())
	assert.Equal(t, int32(1), upstream.registrations.Load())
	assert.Zero(t, upstream.tokenRequests.Load())
}

func TestProbeErrorAttrs_OmitsServerResponse(t *testing.T) {
	t.Parallel()

	const secretish = "do-not-log-this-response-text"
	tests := []struct {
		name  string
		token http.HandlerFunc
		want  []any
	}{
		{
			name: "oauth error with description",
			token: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": "invalid_request", "error_description": secretish,
				})
			},
			want: []any{"error_code", "invalid_request", "status", http.StatusBadRequest},
		},
		{
			name: "non-oauth body",
			token: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(secretish))
			},
			want: []any{"error_code", "", "status", http.StatusBadGateway},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			upstream := newProbeUpstream(t, tt.token, http.StatusCreated)
			req := preloadCachedRegistration(t, newMemoryDCRStore(t), upstream, "client_secret_basic")
			status, reason := verifyCachedRegistration(context.Background(), req, &Resolution{
				ClientID:                "cached-client-id",
				ClientSecret:            "cached-client-secret",
				TokenEndpoint:           upstream.URL + "/token",
				TokenEndpointAuthMethod: "client_secret_basic",
			})
			require.Equal(t, registrationUnverified, status)
			require.Error(t, reason)
			assert.Contains(t, reason.Error(), secretish, "the raw error does carry the server's text")
			attrs := probeErrorAttrs(reason)
			assert.Equal(t, tt.want, attrs)
		})
	}
}

// TestVerifyCachedRegistration_InvalidProbeEndpointIsUnverified pins that a
// configured probe endpoint the resolver's URL policy rejects keeps the cached
// registration (unverified) instead of failing the resolve or probing anyway.
func TestVerifyCachedRegistration_InvalidProbeEndpointIsUnverified(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		probe RegistrationProbe
		set   func(*Request)
	}{
		{
			name:  "token endpoint with a non-https scheme",
			probe: ProbeTokenEndpoint,
			set:   func(r *Request) { r.TokenEndpoint = "ftp://upstream.example.com/token" },
		},
		{
			name:  "authorization endpoint with a non-https scheme",
			probe: ProbeAuthorizationEndpoint,
			set:   func(r *Request) { r.AuthorizationEndpoint = "ftp://upstream.example.com/authorize" },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			upstream := newProbeUpstream(t, oauthErrorResponse(http.StatusUnauthorized, "invalid_client"), http.StatusCreated)
			upstream.setAuthorize(oauthErrorResponse(http.StatusBadRequest, "invalid_request"))
			req := preloadCachedRegistration(t, newMemoryDCRStore(t), upstream, "client_secret_basic")
			req.RegistrationProbe = tt.probe
			tt.set(req)

			status, reason := verifyCachedRegistration(context.Background(), req, &Resolution{
				ClientID:                "cached-client-id",
				ClientSecret:            "cached-client-secret",
				AuthorizationEndpoint:   upstream.URL + "/authorize",
				TokenEndpoint:           upstream.URL + "/token",
				TokenEndpointAuthMethod: "client_secret_basic",
				RedirectURI:             upstream.URL + "/oauth/callback",
			})
			assert.Equal(t, registrationUnverified, status)
			require.Error(t, reason)
			assert.Zero(t, upstream.tokenRequests.Load(), "the cached token endpoint must not be probed instead")
			assert.Zero(t, upstream.authzRequests.Load(), "the cached authorization endpoint must not be probed instead")
		})
	}
}

// TestVerifyCachedRegistration_AuthorizationProbeRedirectURIFallback pins
// that a cached row without a redirect URI probes with the resolver's
// redirect URI (configured, else derived from the local issuer), and that an
// underivable one is reported rather than probed with an empty value.
func TestVerifyCachedRegistration_AuthorizationProbeRedirectURIFallback(t *testing.T) {
	t.Parallel()

	cached := func(upstream *probeUpstream) *Resolution {
		return &Resolution{
			ClientID:                "cached-client-id",
			AuthorizationEndpoint:   upstream.URL + "/authorize",
			TokenEndpoint:           upstream.URL + "/token",
			TokenEndpointAuthMethod: "none",
		}
	}

	t.Run("configured redirect URI is used", func(t *testing.T) {
		t.Parallel()
		upstream := newProbeUpstream(t, oauthErrorResponse(http.StatusUnauthorized, "invalid_client"), http.StatusCreated)
		upstream.setAuthorize(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		req := preloadForAuthorizationProbe(t, newMemoryDCRStore(t), upstream)
		req.RedirectURI = "https://proxy.example.com/oauth/callback"

		status, reason := verifyCachedRegistration(context.Background(), req, cached(upstream))
		require.NoError(t, reason)
		assert.Equal(t, registrationLive, status)
		r := upstream.authorizeRequest()
		require.NotNil(t, r)
		assert.Equal(t, "https://proxy.example.com/oauth/callback", r.URL.Query().Get("redirect_uri"))
	})

	t.Run("underivable redirect URI is an error", func(t *testing.T) {
		t.Parallel()
		upstream := newProbeUpstream(t, oauthErrorResponse(http.StatusUnauthorized, "invalid_client"), http.StatusCreated)
		upstream.setAuthorize(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		req := preloadForAuthorizationProbe(t, newMemoryDCRStore(t), upstream)
		req.Issuer = "not a url"

		status, reason := verifyCachedRegistration(context.Background(), req, cached(upstream))
		assert.Equal(t, registrationUnverified, status)
		require.Error(t, reason)
		assert.Zero(t, upstream.authzRequests.Load(), "no probe without a redirect URI")
	})
}

func TestAuthorizationProbeURL_RejectsUnparsableEndpoint(t *testing.T) {
	t.Parallel()

	_, err := authorizationProbeURL("://missing-scheme", "client", "https://proxy.example.com/oauth/callback", nil)
	require.Error(t, err)
}

// TestClassifyProbe_SuccessfulExchangeIsLive pins the token-probe verdict
// when the upstream issues tokens for the random code: the client
// authenticated, so the registration is live.
func TestClassifyProbe_SuccessfulExchangeIsLive(t *testing.T) {
	t.Parallel()

	assert.Equal(t, registrationLive, classifyProbe(nil))
}
