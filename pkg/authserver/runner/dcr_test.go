// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

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
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
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

	// observeRegistration is called for each request hitting the
	// registration endpoint. Safe for concurrent use.
	observeRegistration func(r *http.Request, body []byte)
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
		resp := oauthproto.DynamicClientRegistrationResponse{
			ClientID:                "test-client-id",
			ClientSecret:            "test-client-secret",
			RegistrationAccessToken: "test-reg-token",
			RegistrationClientURI:   server.URL + "/register/test-client-id",
			TokenEndpointAuthMethod: req.TokenEndpointAuthMethod,
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

	cache := NewInMemoryDCRCredentialStore()
	issuer := server.URL

	// Pre-populate the cache with a resolution matching the key we will
	// look up.
	redirectURI := issuer + "/oauth/callback"
	key := DCRKey{
		Issuer:      issuer,
		RedirectURI: redirectURI,
		ScopesHash:  scopesHash([]string{"openid", "profile"}),
	}
	preloaded := &DCRResolution{
		ClientID:              "preloaded-id",
		ClientSecret:          "preloaded-secret",
		AuthorizationEndpoint: "https://preloaded/authorize",
		TokenEndpoint:         "https://preloaded/token",
	}
	require.NoError(t, cache.Put(context.Background(), key, preloaded))

	rc := &authserver.OAuth2UpstreamRunConfig{
		Scopes: []string{"openid", "profile"},
		DCRConfig: &authserver.DCRUpstreamConfig{
			DiscoveryURL: issuer + "/.well-known/openid-configuration",
		},
	}

	got, err := resolveDCRCredentials(context.Background(), rc, issuer, cache)
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

	cache := NewInMemoryDCRCredentialStore()
	issuer := server.URL
	rc := &authserver.OAuth2UpstreamRunConfig{
		Scopes: []string{"openid", "profile"},
		DCRConfig: &authserver.DCRUpstreamConfig{
			DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
		},
	}

	res, err := resolveDCRCredentials(context.Background(), rc, issuer, cache)
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
	var req oauthproto.DynamicClientRegistrationRequest
	require.NoError(t, json.Unmarshal(gotBody, &req))
	assert.Equal(t, []string{issuer + "/oauth/callback"}, req.RedirectURIs)
	assert.ElementsMatch(t, []string{"openid", "profile"}, []string(req.Scopes))

	// Cache was populated.
	cached, ok, err := cache.Get(context.Background(),
		DCRKey{Issuer: issuer, RedirectURI: issuer + "/oauth/callback", ScopesHash: scopesHash([]string{"openid", "profile"})})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "test-client-id", cached.ClientID)
}

func TestResolveDCRCredentials_ExplicitEndpointsOverride(t *testing.T) {
	t.Parallel()

	server := newDCRTestServer(t, dcrTestHandlerConfig{})
	cache := NewInMemoryDCRCredentialStore()
	issuer := server.URL

	rc := &authserver.OAuth2UpstreamRunConfig{
		AuthorizationEndpoint: "https://explicit.example.com/authorize",
		TokenEndpoint:         "https://explicit.example.com/token",
		Scopes:                []string{"openid"},
		DCRConfig: &authserver.DCRUpstreamConfig{
			DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
		},
	}

	res, err := resolveDCRCredentials(context.Background(), rc, issuer, cache)
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

	cache := NewInMemoryDCRCredentialStore()
	issuer := server.URL
	rc := &authserver.OAuth2UpstreamRunConfig{
		Scopes: []string{"openid"},
		DCRConfig: &authserver.DCRUpstreamConfig{
			DiscoveryURL:           issuer + "/.well-known/oauth-authorization-server",
			InitialAccessTokenFile: tokenPath,
		},
	}

	_, err := resolveDCRCredentials(context.Background(), rc, issuer, cache)
	require.NoError(t, err)
	assert.Equal(t, "Bearer iat-secret-value", gotAuthHeader)
}

func TestResolveDCRCredentials_AuthMethodPreference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		supported []string
		expected  string
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
			name:      "falls back to none when only none supported",
			supported: []string{"none"},
			expected:  "none",
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
			})
			cache := NewInMemoryDCRCredentialStore()
			issuer := server.URL
			rc := &authserver.OAuth2UpstreamRunConfig{
				Scopes: []string{"openid"},
				DCRConfig: &authserver.DCRUpstreamConfig{
					DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
				},
			}

			res, err := resolveDCRCredentials(context.Background(), rc, issuer, cache)
			require.NoError(t, err)
			assert.Equal(t, tc.expected, res.TokenEndpointAuthMethod)
		})
	}
}

func TestResolveDCRCredentials_EmptyAuthMethodIntersectionErrors(t *testing.T) {
	t.Parallel()

	// Configure the server to advertise an unknown method so intersection is empty.
	server := newDCRTestServer(t, dcrTestHandlerConfig{
		tokenEndpointAuthMethodsSupported: []string{"tls_client_auth"},
	})
	cache := NewInMemoryDCRCredentialStore()
	issuer := server.URL
	rc := &authserver.OAuth2UpstreamRunConfig{
		Scopes: []string{"openid"},
		DCRConfig: &authserver.DCRUpstreamConfig{
			DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
		},
	}
	_, err := resolveDCRCredentials(context.Background(), rc, issuer, cache)
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
	cache := NewInMemoryDCRCredentialStore()
	issuer := server.URL
	rc := &authserver.OAuth2UpstreamRunConfig{
		Scopes: []string{"openid"},
		DCRConfig: &authserver.DCRUpstreamConfig{
			DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
		},
	}

	res, err := resolveDCRCredentials(context.Background(), rc, issuer, cache)
	require.NoError(t, err)
	assert.Equal(t, "test-client-id", res.ClientID)
	assert.Equal(t, "/register", gotPath)
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

	cache := NewInMemoryDCRCredentialStore()
	issuer := server.URL
	rc := &authserver.OAuth2UpstreamRunConfig{
		AuthorizationEndpoint: issuer + "/authorize",
		TokenEndpoint:         issuer + "/token",
		Scopes:                []string{"openid"},
		DCRConfig: &authserver.DCRUpstreamConfig{
			RegistrationEndpoint: issuer + "/custom/register",
		},
	}

	res, err := resolveDCRCredentials(context.Background(), rc, issuer, cache)
	require.NoError(t, err)
	assert.Equal(t, "direct-id", res.ClientID)
	assert.Equal(t, int32(0), atomic.LoadInt32(&discoveryHits),
		"discovery endpoint must not be contacted when RegistrationEndpoint is set")
	assert.Equal(t, int32(1), atomic.LoadInt32(&registrationHits))
}

func TestResolveDCRCredentials_RequiresClientIDEmpty(t *testing.T) {
	t.Parallel()

	cache := NewInMemoryDCRCredentialStore()
	rc := &authserver.OAuth2UpstreamRunConfig{
		ClientID: "preprovisioned",
		DCRConfig: &authserver.DCRUpstreamConfig{
			RegistrationEndpoint: "https://example.com/register",
		},
	}
	_, err := resolveDCRCredentials(context.Background(), rc, "https://example.com", cache)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pre-provisioned")
}

func TestResolveDCRCredentials_RequiresDCRConfig(t *testing.T) {
	t.Parallel()

	cache := NewInMemoryDCRCredentialStore()
	rc := &authserver.OAuth2UpstreamRunConfig{}
	_, err := resolveDCRCredentials(context.Background(), rc, "https://example.com", cache)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no dcr_config")
}

func TestNeedsDCR(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		rc       *authserver.OAuth2UpstreamRunConfig
		expected bool
	}{
		{name: "nil", rc: nil, expected: false},
		{name: "empty client_id and dcr_config", rc: &authserver.OAuth2UpstreamRunConfig{
			DCRConfig: &authserver.DCRUpstreamConfig{},
		}, expected: true},
		{name: "client_id without dcr", rc: &authserver.OAuth2UpstreamRunConfig{
			ClientID: "x",
		}, expected: false},
		{name: "client_id with dcr (invalid but returns false)", rc: &authserver.OAuth2UpstreamRunConfig{
			ClientID:  "x",
			DCRConfig: &authserver.DCRUpstreamConfig{},
		}, expected: false},
		{name: "both empty", rc: &authserver.OAuth2UpstreamRunConfig{}, expected: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, needsDCR(tc.rc))
		})
	}
}

func TestApplyResolution_RespectsExplicitEndpoints(t *testing.T) {
	t.Parallel()

	rc := &authserver.OAuth2UpstreamRunConfig{
		AuthorizationEndpoint: "https://explicit/authorize",
		TokenEndpoint:         "https://explicit/token",
	}
	res := &DCRResolution{
		ClientID:              "got-client",
		AuthorizationEndpoint: "https://discovered/authorize",
		TokenEndpoint:         "https://discovered/token",
	}
	applyResolution(rc, res)
	assert.Equal(t, "got-client", rc.ClientID)
	assert.Equal(t, "https://explicit/authorize", rc.AuthorizationEndpoint)
	assert.Equal(t, "https://explicit/token", rc.TokenEndpoint)
}

func TestApplyResolution_FillsMissingEndpoints(t *testing.T) {
	t.Parallel()

	rc := &authserver.OAuth2UpstreamRunConfig{}
	res := &DCRResolution{
		ClientID:              "got-client",
		AuthorizationEndpoint: "https://discovered/authorize",
		TokenEndpoint:         "https://discovered/token",
	}
	applyResolution(rc, res)
	assert.Equal(t, "got-client", rc.ClientID)
	assert.Equal(t, "https://discovered/authorize", rc.AuthorizationEndpoint)
	assert.Equal(t, "https://discovered/token", rc.TokenEndpoint)
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

	cache := NewInMemoryDCRCredentialStore()
	issuer := server.URL
	rc := &authserver.OAuth2UpstreamRunConfig{
		Scopes: []string{"openid"},
		DCRConfig: &authserver.DCRUpstreamConfig{
			DiscoveryURL: issuer + "/tenants/acme/metadata",
		},
	}

	res, err := resolveDCRCredentials(context.Background(), rc, issuer, cache)
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

	cache := NewInMemoryDCRCredentialStore()
	issuer := server.URL
	rc := &authserver.OAuth2UpstreamRunConfig{
		Scopes: []string{"openid"},
		DCRConfig: &authserver.DCRUpstreamConfig{
			DiscoveryURL: issuer + "/metadata",
		},
	}

	_, err := resolveDCRCredentials(context.Background(), rc, issuer, cache)
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
	cache := NewInMemoryDCRCredentialStore()
	issuer := server.URL
	rc := &authserver.OAuth2UpstreamRunConfig{
		// Scopes intentionally left empty so the resolver falls back to
		// the discovered scopes_supported.
		DCRConfig: &authserver.DCRUpstreamConfig{
			DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
		},
	}

	_, err := resolveDCRCredentials(context.Background(), rc, issuer, cache)
	require.NoError(t, err)

	var req oauthproto.DynamicClientRegistrationRequest
	require.NoError(t, json.Unmarshal(gotBody, &req))
	assert.ElementsMatch(t, []string{"openid", "profile", "email"}, []string(req.Scopes),
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
	cache := NewInMemoryDCRCredentialStore()
	issuer := server.URL
	rc := &authserver.OAuth2UpstreamRunConfig{
		DCRConfig: &authserver.DCRUpstreamConfig{
			DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
		},
	}

	res, err := resolveDCRCredentials(context.Background(), rc, issuer, cache)
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

func TestDCRStepError(t *testing.T) {
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

		var got *DCRStepError
		require.True(t, errors.As(wrapped, &got))
		assert.Equal(t, dcrStepCacheRead, got.Step)
		assert.Equal(t, "https://as", got.Issuer)
		assert.Equal(t, "https://app/cb", got.RedirectURI)
	})

	t.Run("resolveDCRCredentials wraps every failure in a DCRStepError", func(t *testing.T) {
		t.Parallel()

		// Precondition failure → dcrStepValidate.
		_, err := resolveDCRCredentials(context.Background(), nil, "https://as",
			NewInMemoryDCRCredentialStore())
		require.Error(t, err)
		var stepErr *DCRStepError
		require.True(t, errors.As(err, &stepErr))
		assert.Equal(t, dcrStepValidate, stepErr.Step)
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
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.expected, sanitizeErrorForLog(tc.in))
		})
	}
}
