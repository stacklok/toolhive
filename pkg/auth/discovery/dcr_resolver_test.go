// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// Tests in this file pin the behavioural properties the CLI flow inherits
// from migrating its DCR call through pkg/auth/dcr.ResolveCredentials
// (issue #5219 sub-issue 4b). Each test mounts an httptest server that
// simulates a specific upstream behaviour and asserts that
// handleDynamicRegistration surfaces the resolver's reaction unchanged.
//
// The properties under test:
//
//   - S256 PKCE gating: an upstream advertising only "plain" PKCE causes
//     the CLI to surface a resolver error rather than register a public
//     client without S256 (RFC 7636 / OAuth 2.1 compliance).
//   - Bearer-token transport redirect refusal: the registration POST does
//     not follow 30x redirects, so a token-leak class of attack is
//     impossible.
//   - Singleflight deduplication: concurrent handleDynamicRegistration
//     calls for the same (issuer, scopes, redirectURI) coalesce into
//     exactly one upstream /register request.
//
// Expiry-driven refetch and panic recovery are pinned by the resolver's
// own test suite in pkg/auth/dcr; this file does not duplicate them
// because the CLI flow does not exercise either path in a CLI-specific
// way (the CLI's persistence layer lives outside the resolver, so expiry
// refetch is observable only via the remote handler's cached fields, not
// via PerformOAuthFlow alone).

// dcrTestServerConfig controls the mock upstream's behaviour for the CLI
// inherited-property tests below.
type dcrTestServerConfig struct {
	// codeChallengeMethodsSupported is the upstream's advertised
	// code_challenge_methods_supported value. Set to []string{"S256"} for
	// happy-path tests; set to []string{"plain"} or nil to exercise the
	// S256 gate.
	codeChallengeMethodsSupported []string

	// registrationStatusCode overrides the registration endpoint's
	// response status. When zero, the endpoint returns 201 Created with a
	// well-formed RFC 7591 response.
	registrationStatusCode int

	// registrationRedirectsTo, when non-empty, causes the registration
	// endpoint to issue a 302 to this URL — used to verify the bearer
	// transport's redirect refusal.
	registrationRedirectsTo string

	// onRegistration is called once per registration attempt. Safe for
	// concurrent use.
	onRegistration func(r *http.Request, body []byte)

	// registrationDelay introduces a delay inside the registration
	// handler so concurrent goroutines can pile up at the singleflight
	// before any can finish.
	registrationDelay time.Duration
}

// newDCRDiscoveryServer mounts /.well-known/openid-configuration and a
// /register endpoint on a single httptest server. The metadata advertises
// the server's URL as the issuer (so RFC 8414 §3.3 issuer match passes
// against any DiscoveryURL derived from the issuer).
func newDCRDiscoveryServer(t *testing.T, cfg dcrTestServerConfig) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		md := oauthproto.OIDCDiscoveryDocument{
			AuthorizationServerMetadata: oauthproto.AuthorizationServerMetadata{
				Issuer:                            server.URL,
				AuthorizationEndpoint:             server.URL + "/authorize",
				TokenEndpoint:                     server.URL + "/token",
				JWKSURI:                           server.URL + "/jwks",
				RegistrationEndpoint:              server.URL + "/register",
				CodeChallengeMethodsSupported:     cfg.codeChallengeMethodsSupported,
				TokenEndpointAuthMethodsSupported: []string{"none"},
				ResponseTypesSupported:            []string{"code"},
			},
			SubjectTypesSupported:            []string{"public"},
			IDTokenSigningAlgValuesSupported: []string{"RS256"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(md)
	})

	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if cfg.onRegistration != nil {
			cfg.onRegistration(r, body)
		}
		if cfg.registrationDelay > 0 {
			time.Sleep(cfg.registrationDelay)
		}
		if cfg.registrationRedirectsTo != "" {
			http.Redirect(w, r, cfg.registrationRedirectsTo, http.StatusFound)
			return
		}
		if cfg.registrationStatusCode != 0 {
			w.WriteHeader(cfg.registrationStatusCode)
			return
		}
		resp := oauthproto.DynamicClientRegistrationResponse{
			ClientID:                "cli-registered-client",
			TokenEndpointAuthMethod: "none",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	})

	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestHandleDynamicRegistration_InheritsS256Gating verifies the CLI flow
// surfaces the resolver's S256 PKCE error when the upstream advertises
// only "plain" (or omits code_challenge_methods_supported entirely). The
// pre-migration CLI would have registered as a public client regardless,
// silently violating RFC 7636 / OAuth 2.1; the migrated CLI MUST surface
// a clear error.
func TestHandleDynamicRegistration_InheritsS256Gating(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                          string
		codeChallengeMethodsSupported []string
	}{
		{name: "upstream omits code_challenge_methods_supported", codeChallengeMethodsSupported: nil},
		{name: "upstream advertises only plain", codeChallengeMethodsSupported: []string{"plain"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var registrationHits int32
			server := newDCRDiscoveryServer(t, dcrTestServerConfig{
				codeChallengeMethodsSupported: tc.codeChallengeMethodsSupported,
				onRegistration: func(_ *http.Request, _ []byte) {
					atomic.AddInt32(&registrationHits, 1)
				},
			})

			config := &OAuthFlowConfig{
				Scopes:       []string{"openid", "profile"},
				CallbackPort: 8765,
			}

			err := handleDynamicRegistration(context.Background(), server.URL, config)
			require.Error(t, err, "S256 gating must fail registration")
			assert.Contains(t, err.Error(), "S256",
				"resolver error must mention the missing S256 advertisement")
			assert.EqualValues(t, 0, atomic.LoadInt32(&registrationHits),
				"the upstream /register endpoint must NOT be contacted when the S256 gate fails")
		})
	}
}

// TestHandleDynamicRegistration_InheritsRedirectRefusal verifies that the
// registration POST does not follow a redirect. A non-defended client
// would re-issue the registration request (with any attached bearer)
// against the redirect target; the resolver's bearerTokenTransport refuses,
// and the CLI surfaces the resulting *url.Error.
func TestHandleDynamicRegistration_InheritsRedirectRefusal(t *testing.T) {
	t.Parallel()

	// Foreign origin: a separate server that records every request.
	var foreignHits int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&foreignHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(foreign.Close)

	server := newDCRDiscoveryServer(t, dcrTestServerConfig{
		codeChallengeMethodsSupported: []string{"S256"},
		registrationRedirectsTo:       foreign.URL + "/stolen",
	})

	config := &OAuthFlowConfig{
		Scopes:       []string{"openid", "profile"},
		CallbackPort: 8765,
	}

	err := handleDynamicRegistration(context.Background(), server.URL, config)
	require.Error(t, err, "registration must fail when the upstream returns a redirect")
	assert.Contains(t, err.Error(), "redirect",
		"resolver error must mention the refused redirect so operators can correlate")
	assert.EqualValues(t, 0, atomic.LoadInt32(&foreignHits),
		"foreign origin must receive zero requests; the redirect refusal prevents the leak")
}

// TestHandleDynamicRegistration_InheritsSingleflightDedup verifies that
// N concurrent handleDynamicRegistration calls for the same (issuer,
// scopes, redirectURI) tuple coalesce into exactly one upstream /register
// request. The pre-migration CLI would have made N parallel registrations
// and produced N orphaned client_ids at the upstream — a real production
// hazard for rate-limited DCR endpoints and a cleanup task for operators.
//
// "Exactly one /register" is the property under test. The CLI's
// per-invocation in-memory store means cross-invocation reuse is NOT
// observable here (that lives in pkg/auth/remote/handler.go); this test
// strictly pins the intra-call singleflight.
func TestHandleDynamicRegistration_InheritsSingleflightDedup(t *testing.T) {
	t.Parallel()

	// Each invocation of handleDynamicRegistration constructs a fresh
	// dcr.InMemoryStore, so cross-invocation cache hits are NOT
	// possible. The singleflight at the resolver layer is package-global,
	// which is what coalesces concurrent goroutines hitting the same
	// (issuer, redirectURI, scopesHash) key.
	var registrationHits int32
	server := newDCRDiscoveryServer(t, dcrTestServerConfig{
		codeChallengeMethodsSupported: []string{"S256"},
		registrationDelay:             250 * time.Millisecond,
		onRegistration: func(_ *http.Request, _ []byte) {
			atomic.AddInt32(&registrationHits, 1)
		},
	})

	const N = 6
	var wg sync.WaitGroup
	wg.Add(N)
	results := make([]*OAuthFlowConfig, N)
	errs := make([]error, N)

	for i := 0; i < N; i++ {
		// Each goroutine gets its own OAuthFlowConfig so we can assert
		// the resolution was applied to every caller's config.
		cfg := &OAuthFlowConfig{
			Scopes:       []string{"openid", "profile"},
			CallbackPort: 8765,
		}
		results[i] = cfg
		go func(idx int) {
			defer wg.Done()
			errs[idx] = handleDynamicRegistration(context.Background(), server.URL, cfg)
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for concurrent handleDynamicRegistration goroutines")
	}

	for i := 0; i < N; i++ {
		require.NoError(t, errs[i], "goroutine %d errored", i)
		assert.Equal(t, "cli-registered-client", results[i].ClientID,
			"every concurrent caller must observe the same resolved client_id")
	}
	assert.EqualValues(t, 1, atomic.LoadInt32(&registrationHits),
		"expected exactly one /register call despite %d concurrent goroutines; got %d",
		N, atomic.LoadInt32(&registrationHits))
}

// TestHandleDynamicRegistration_PopulatesEndpoints verifies the contract
// the rest of the CLI flow depends on: handleDynamicRegistration writes
// the resolved AuthorizeURL / TokenURL onto OAuthFlowConfig so
// createOAuthConfig can construct the OAuth2 flow without re-discovery.
func TestHandleDynamicRegistration_PopulatesEndpoints(t *testing.T) {
	t.Parallel()

	server := newDCRDiscoveryServer(t, dcrTestServerConfig{
		codeChallengeMethodsSupported: []string{"S256"},
	})

	config := &OAuthFlowConfig{
		Scopes:       []string{"openid", "profile"},
		CallbackPort: 8765,
	}

	err := handleDynamicRegistration(context.Background(), server.URL, config)
	require.NoError(t, err)
	assert.Equal(t, "cli-registered-client", config.ClientID,
		"handleDynamicRegistration must populate OAuthFlowConfig.ClientID")
	assert.Equal(t, server.URL+"/authorize", config.AuthorizeURL,
		"handleDynamicRegistration must populate OAuthFlowConfig.AuthorizeURL")
	assert.Equal(t, server.URL+"/token", config.TokenURL,
		"handleDynamicRegistration must populate OAuthFlowConfig.TokenURL")
}
