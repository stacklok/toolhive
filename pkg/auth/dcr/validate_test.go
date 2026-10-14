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
