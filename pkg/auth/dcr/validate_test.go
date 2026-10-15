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
// "fresh-client-id", and a token endpoint whose answer each test chooses.
type probeUpstream struct {
	*httptest.Server
	registrations atomic.Int32
	tokenRequests atomic.Int32

	mu        sync.Mutex
	lastToken *http.Request
	lastForm  url.Values
}

func newProbeUpstream(t *testing.T, token http.HandlerFunc, registerStatus int) *probeUpstream {
	t.Helper()
	u := &probeUpstream{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(oauthproto.AuthorizationServerMetadata{
			Issuer:                            u.URL,
			AuthorizationEndpoint:             u.URL + "/authorize",
			TokenEndpoint:                     u.URL + "/token",
			RegistrationEndpoint:              u.URL + "/register",
			TokenEndpointAuthMethodsSupported: []string{"client_secret_basic"},
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
	u.Server = httptest.NewServer(mux)
	t.Cleanup(u.Close)
	return u
}

// tokenRequest returns the last token request and its parsed form.
func (u *probeUpstream) tokenRequest() (*http.Request, url.Values) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lastToken, u.lastForm
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
