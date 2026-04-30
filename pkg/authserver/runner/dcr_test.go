// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"encoding/json"
	"errors"
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
		resp := oauthproto.DynamicClientRegistrationResponse{
			ClientID:                "test-client-id",
			ClientSecret:            "test-client-secret",
			RegistrationAccessToken: "test-reg-token",
			RegistrationClientURI:   server.URL + "/register/test-client-id",
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

	cache := NewInMemoryDCRCredentialStore()
	issuer := upstream.URL
	rc := &authserver.OAuth2UpstreamRunConfig{
		Scopes: []string{"openid"},
		DCRConfig: &authserver.DCRUpstreamConfig{
			DiscoveryURL:           issuer + "/.well-known/oauth-authorization-server",
			InitialAccessTokenFile: tokenPath,
		},
	}

	_, err := resolveDCRCredentials(context.Background(), rc, issuer, cache)
	require.Error(t, err, "registration must fail when the upstream returns a redirect")
	assert.ErrorIs(t, err, errDCRRedirectRefused,
		"the resolver must refuse to follow registration-endpoint redirects")

	foreignMu.Lock()
	defer foreignMu.Unlock()
	assert.EqualValues(t, 0, atomic.LoadInt32(&foreignHits),
		"foreign origin must receive zero requests; got %v Authorization headers: %v",
		atomic.LoadInt32(&foreignHits), foreignAuthHeaders)
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
		{name: "client_id wins over dcr_config (defensive AND semantic)", rc: &authserver.OAuth2UpstreamRunConfig{
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
	cache := NewInMemoryDCRCredentialStore()

	// Caller-supplied issuer names this auth server, NOT the upstream.
	// Production wiring always passes its own issuer here (see
	// embeddedauthserver.go: buildUpstreamConfigs(... cfg.Issuer ...)).
	ourIssuer := "https://our-auth.example.com"

	rc := &authserver.OAuth2UpstreamRunConfig{
		// Explicit redirect URI so the resolver does not try to default
		// it from ourIssuer (which would still work, but isolating the
		// concern under test keeps the failure mode crisp).
		RedirectURI: "https://our-auth.example.com/oauth/callback",
		Scopes:      []string{"openid"},
		DCRConfig: &authserver.DCRUpstreamConfig{
			DiscoveryURL: server.URL + "/.well-known/oauth-authorization-server",
		},
	}

	res, err := resolveDCRCredentials(context.Background(), rc, ourIssuer, cache)
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

// TestResolveDCRCredentials_SingleflightCoalescesConcurrentCallers pins the
// behaviour that N concurrent callers for the same DCRKey result in exactly
// one RegisterClientDynamically call against the upstream — preventing the
// orphaned-registration class of bug raised in PR #5042 review.
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

	cache := NewInMemoryDCRCredentialStore()
	issuer := server.URL
	rc := &authserver.OAuth2UpstreamRunConfig{
		Scopes: []string{"openid", "profile"},
		DCRConfig: &authserver.DCRUpstreamConfig{
			DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
		},
	}

	const N = 8
	results := make([]*DCRResolution, N)
	errs := make([]error, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			res, err := resolveDCRCredentials(context.Background(), rc, issuer, cache)
			results[idx] = res
			errs[idx] = err
		}(i)
	}

	// Release the gate so every blocked handler can proceed. Even if Go
	// scheduled the leader's handler concurrently with the followers'
	// arrival, only the leader actually invokes the handler — the followers
	// wait inside singleflight.Do.
	time.Sleep(50 * time.Millisecond)
	close(gate)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for concurrent resolveDCRCredentials goroutines")
	}

	for i := 0; i < N; i++ {
		require.NoError(t, errs[i], "goroutine %d errored", i)
		require.NotNil(t, results[i], "goroutine %d got nil resolution", i)
		assert.Equal(t, "test-client-id", results[i].ClientID)
	}
	assert.EqualValues(t, 1, atomic.LoadInt32(&registrationCalls),
		"expected exactly one registration despite %d concurrent callers; got %d",
		N, atomic.LoadInt32(&registrationCalls))
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

// TestApplyResolution_DoesNotOverwritePreProvisionedClientID verifies the
// defence-in-depth in applyResolution: a caller that bypasses
// validateResolveInputs and invokes applyResolution directly with a
// pre-provisioned ClientID does not have it silently clobbered.
func TestApplyResolution_DoesNotOverwritePreProvisionedClientID(t *testing.T) {
	t.Parallel()

	rc := &authserver.OAuth2UpstreamRunConfig{
		ClientID: "pre-provisioned",
	}
	res := &DCRResolution{
		ClientID: "would-be-overwrite",
	}
	applyResolution(rc, res)
	assert.Equal(t, "pre-provisioned", rc.ClientID,
		"applyResolution must not overwrite a non-empty ClientID")
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
			cfg := &authserver.DCRUpstreamConfig{RegistrationEndpoint: tc.registrationEndpoint}
			_, err := resolveDCREndpoints(context.Background(), cfg)
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

func (f failingDCRStore) Get(_ context.Context, _ DCRKey) (*DCRResolution, bool, error) {
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	return nil, false, nil
}

func (f failingDCRStore) Put(_ context.Context, _ DCRKey, _ *DCRResolution) error {
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

	rc := &authserver.OAuth2UpstreamRunConfig{
		DCRConfig: &authserver.DCRUpstreamConfig{
			RegistrationEndpoint: "https://idp.example.com/register",
		},
	}

	_, err := resolveDCRCredentials(context.Background(), rc, "https://idp.example.com", store)
	require.Error(t, err)
	assert.ErrorIs(t, err, storeErr,
		"cache.Get error must be wrapped with %%w so callers can inspect the cause")
	assert.Contains(t, err.Error(), "dcr: cache lookup",
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

	rc := &authserver.OAuth2UpstreamRunConfig{
		Scopes: []string{"openid"},
		DCRConfig: &authserver.DCRUpstreamConfig{
			DiscoveryURL: server.URL + "/.well-known/oauth-authorization-server",
		},
	}

	_, err := resolveDCRCredentials(context.Background(), rc, server.URL, store)
	require.Error(t, err)
	assert.ErrorIs(t, err, storeErr,
		"cache.Put error must be wrapped with %%w so callers can inspect the cause")
	assert.Contains(t, err.Error(), "dcr: cache put",
		"the wrap message is part of the operator-debugging contract")
}

// TestBuildResolution_PopulatesRFC7591ExpiryFields covers the conversion of
// the int64 epoch fields client_id_issued_at and client_secret_expires_at
// into time.Time on DCRResolution. The wire convention "0 means absent /
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

	cache := NewInMemoryDCRCredentialStore()
	issuer := server.URL
	rc := &authserver.OAuth2UpstreamRunConfig{
		Scopes: []string{"openid"},
		DCRConfig: &authserver.DCRUpstreamConfig{
			DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
		},
	}

	// First call: registers, populates cache with already-expired entry.
	res1, err := resolveDCRCredentials(context.Background(), rc, issuer, cache)
	require.NoError(t, err)
	require.NotNil(t, res1)
	require.False(t, res1.ClientSecretExpiresAt.IsZero(),
		"upstream advertised an expiry — the resolution must echo it")
	require.True(t, time.Now().After(res1.ClientSecretExpiresAt),
		"test setup should have produced an already-expired secret")
	require.EqualValues(t, 1, atomic.LoadInt32(&registrationCalls))

	// Second call: the cached entry is expired, so the resolver must refetch.
	res2, err := resolveDCRCredentials(context.Background(), rc, issuer, cache)
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
			cache := NewInMemoryDCRCredentialStore()
			issuer := server.URL
			rc := &authserver.OAuth2UpstreamRunConfig{
				Scopes: []string{"openid"},
				DCRConfig: &authserver.DCRUpstreamConfig{
					DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
				},
			}

			_, err := resolveDCRCredentials(context.Background(), rc, issuer, cache)
			require.NoError(t, err)
			_, err = resolveDCRCredentials(context.Background(), rc, issuer, cache)
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

func (panickingPutDCRStore) Get(_ context.Context, _ DCRKey) (*DCRResolution, bool, error) {
	return nil, false, nil
}

func (s panickingPutDCRStore) Put(_ context.Context, _ DCRKey, _ *DCRResolution) error {
	panic(s.panicValue)
}

// TestResolveDCRCredentials_RecoversPanicInsideSingleflight pins the
// behaviour that a panic inside the singleflight closure does not propagate
// up as a panic to either the leader goroutine or any of the followers.
// singleflight.Group re-panics the leader's panic in every follower, so
// without the recover N concurrent callers for the same DCRKey would all
// crash with the same value. The defer/recover converts the panic to a
// normal error, the panic is logged at Error with a stack, and every
// caller gets the same wrapped error.
func TestResolveDCRCredentials_RecoversPanicInsideSingleflight(t *testing.T) {
	t.Parallel()

	server := newDCRTestServer(t, dcrTestHandlerConfig{})
	store := panickingPutDCRStore{panicValue: "boom"}

	issuer := server.URL
	rc := &authserver.OAuth2UpstreamRunConfig{
		Scopes: []string{"openid"},
		DCRConfig: &authserver.DCRUpstreamConfig{
			DiscoveryURL: issuer + "/.well-known/oauth-authorization-server",
		},
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
			_, errs[idx] = resolveDCRCredentials(context.Background(), rc, issuer, store)
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
	}
}
