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

	"github.com/stacklok/toolhive/pkg/networking"
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
// because under the option (b) persistence model the CLI flow does not
// exercise either path in a CLI-specific way. Specifically, the
// resolver's RFC 7591 §3.2.1 expiry refetch is in the loop only within
// a single PerformOAuthFlow call (which the CLI never makes repeat
// queries inside). Cross-invocation expiry is handled by the remote
// handler at pkg/auth/remote/handler.go via its HasCachedClientCredentials
// gate, which runs BEFORE this code path and short-circuits to a
// refresh-token flow when a usable cached client exists. The gap between
// "the resolver supports expiry refetch" and "the CLI's cross-invocation
// persistence loop uses it" is the option (b) trade-off documented on
// handleDynamicRegistration; option (a) would close that gap and is the
// natural follow-up.

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

	// Optional RFC 7591 response metadata used by renewal happy-path tests.
	clientSecretExpiresAt   int64
	registrationAccessToken string
	registrationClientURI   string
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
			ClientSecretExpiresAt:   cfg.clientSecretExpiresAt,
			RegistrationAccessToken: cfg.registrationAccessToken,
			RegistrationClientURI:   cfg.registrationClientURI,
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
				// Loopback test server: guard would otherwise refuse to dial it.
				AllowPrivateIPs: true,
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
		Scopes:          []string{"openid", "profile"},
		CallbackPort:    8765,
		AllowPrivateIPs: true, // loopback test server; guard would otherwise refuse to dial it
	}

	err := handleDynamicRegistration(context.Background(), server.URL, config)
	require.Error(t, err, "registration must fail when the upstream returns a redirect")
	assert.Contains(t, err.Error(), "redirect",
		"resolver error must mention the refused redirect so operators can correlate")
	assert.EqualValues(t, 0, atomic.LoadInt32(&foreignHits),
		"foreign origin must receive zero requests; the redirect refusal prevents the leak")
}

// TestHandleDynamicRegistration_MetadataRefetchBlocksPrivateIP pins the
// CWE-918 guard on resolveDCRCredentials's own metadata re-fetch (used to
// recover code_challenge_methods_supported on the pre-discovered path): an
// issuer that resolves to a private IP must be refused at connect time, the
// same as the resolver's own outbound calls in pkg/auth/dcr.
func TestHandleDynamicRegistration_MetadataRefetchBlocksPrivateIP(t *testing.T) {
	t.Parallel()

	config := &OAuthFlowConfig{
		Scopes:               []string{"openid", "profile"},
		CallbackPort:         8765,
		AuthorizeURL:         "https://10.255.255.1/authorize",
		TokenURL:             "https://10.255.255.1/token",
		RegistrationEndpoint: "https://10.255.255.1/register",
	}

	err := handleDynamicRegistration(context.Background(), "https://10.255.255.1", config)
	require.Error(t, err, "an issuer resolving to a private IP must be refused at connect time")
	assert.ErrorContains(t, err, networking.ErrPrivateIpAddress,
		"the metadata re-fetch must be guarded the same as the resolver's own outbound calls")
}

// TestHandleDynamicRegistration_MetadataRefetchAllowPrivateIPsHonored proves
// OAuthFlowConfig.AllowPrivateIPs lifts the guard on the same metadata
// re-fetch pinned as blocked-by-default above. The target is a non-routable
// RFC 5737 documentation address (TEST-NET-1), so the dial fails with a
// network error rather than the guard error — and can never reach a real host.
func TestHandleDynamicRegistration_MetadataRefetchAllowPrivateIPsHonored(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	config := &OAuthFlowConfig{
		Scopes:               []string{"openid", "profile"},
		CallbackPort:         8765,
		AuthorizeURL:         "https://192.0.2.1/authorize",
		TokenURL:             "https://192.0.2.1/token",
		RegistrationEndpoint: "https://192.0.2.1/register",
		AllowPrivateIPs:      true,
	}

	err := handleDynamicRegistration(ctx, "https://192.0.2.1", config)
	require.Error(t, err, "the dead documentation address cannot complete registration")
	assert.NotContains(t, err.Error(), networking.ErrPrivateIpAddress,
		"AllowPrivateIPs=true must lift the guard on the metadata re-fetch")
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
			Scopes:          []string{"openid", "profile"},
			CallbackPort:    8765,
			AllowPrivateIPs: true, // loopback test server; guard would otherwise refuse to dial it
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

// TestHandleDynamicRegistration_NonRootIssuerRFC8414PathInsertion is a
// regression test for #5356. Authorization servers with a non-root issuer
// path (e.g. https://example.com/oauth) may serve their RFC 8414 metadata
// exclusively at the path-insertion URL
// (scheme://host/.well-known/oauth-authorization-server/path), not at the
// OIDC issuer-suffix URL ({issuer}/.well-known/openid-configuration).
// Prior to the fix, resolveDCRCredentials only constructed the OIDC URL and
// failed for those servers. The fix routes through
// oauthproto.FetchAuthorizationServerMetadata which tries all well-known
// URL forms in priority order.
func TestHandleDynamicRegistration_NonRootIssuerRFC8414PathInsertion(t *testing.T) {
	t.Parallel()

	var registrationHits int32
	var server *httptest.Server
	mux := http.NewServeMux()

	// Serve metadata ONLY at the RFC 8414 §3.1 path-insertion URL.
	// The OIDC issuer-suffix URL (/oauth/.well-known/openid-configuration)
	// is intentionally not mounted — it returns 404, simulating the Gleean
	// AS behaviour that triggered #5356.
	mux.HandleFunc("/.well-known/oauth-authorization-server/oauth", func(w http.ResponseWriter, _ *http.Request) {
		// Issuer must carry the non-root path to pass RFC 8414 §3.3 validation.
		md := oauthproto.AuthorizationServerMetadata{
			Issuer:                            server.URL + "/oauth",
			AuthorizationEndpoint:             server.URL + "/oauth/authorize",
			TokenEndpoint:                     server.URL + "/oauth/token",
			RegistrationEndpoint:              server.URL + "/oauth/register",
			CodeChallengeMethodsSupported:     []string{"S256"},
			TokenEndpointAuthMethodsSupported: []string{"none"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(md)
	})

	mux.HandleFunc("/oauth/register", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		atomic.AddInt32(&registrationHits, 1)
		resp := oauthproto.DynamicClientRegistrationResponse{
			ClientID:                "non-root-client",
			TokenEndpointAuthMethod: "none",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	})

	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)

	config := &OAuthFlowConfig{
		Scopes:          []string{"openid", "profile"},
		CallbackPort:    8765,
		AllowPrivateIPs: true, // loopback test server; guard would otherwise refuse to dial it
	}

	err := handleDynamicRegistration(context.Background(), server.URL+"/oauth", config)
	require.NoError(t, err, "DCR must succeed when metadata is served only at RFC 8414 path-insertion URL")
	assert.Equal(t, "non-root-client", config.ClientID)
	assert.EqualValues(t, 1, atomic.LoadInt32(&registrationHits),
		"expected exactly one /oauth/register call")
}

// TestHandleDynamicRegistration_PreDiscoveredPathNonRootIssuer tests the path
// where the CLI already has OAuth endpoints from a prior discovery
// (config.RegistrationEndpoint/AuthorizeURL/TokenURL are all populated) and
// the issuer has a non-root path. getDiscoveryDocument short-circuits on this
// path and returns a synthesised document with empty
// code_challenge_methods_supported; resolveDCRCredentials must then re-fetch
// via multi-URL fallback to populate the S256 PKCE gate. This is the more
// common production scenario vs the fresh-discovery path tested by
// TestHandleDynamicRegistration_NonRootIssuerRFC8414PathInsertion.
func TestHandleDynamicRegistration_PreDiscoveredPathNonRootIssuer(t *testing.T) {
	t.Parallel()

	var registrationHits int32
	var server *httptest.Server
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/oauth-authorization-server/oauth", func(w http.ResponseWriter, _ *http.Request) {
		md := oauthproto.AuthorizationServerMetadata{
			Issuer:                            server.URL + "/oauth",
			AuthorizationEndpoint:             server.URL + "/oauth/authorize",
			TokenEndpoint:                     server.URL + "/oauth/token",
			RegistrationEndpoint:              server.URL + "/oauth/register",
			CodeChallengeMethodsSupported:     []string{"S256"},
			TokenEndpointAuthMethodsSupported: []string{"none"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(md)
	})

	mux.HandleFunc("/oauth/register", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		atomic.AddInt32(&registrationHits, 1)
		resp := oauthproto.DynamicClientRegistrationResponse{
			ClientID:                "pre-discovered-client",
			TokenEndpointAuthMethod: "none",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	})

	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)

	// Simulate the pre-discovered path: all three endpoints are already set so
	// getDiscoveryDocument short-circuits without a network call and returns a
	// synthesised document with empty code_challenge_methods_supported.
	config := &OAuthFlowConfig{
		Scopes:               []string{"openid", "profile"},
		CallbackPort:         8765,
		RegistrationEndpoint: server.URL + "/oauth/register",
		AuthorizeURL:         server.URL + "/oauth/authorize",
		TokenURL:             server.URL + "/oauth/token",
	}

	err := handleDynamicRegistration(context.Background(), server.URL+"/oauth", config)
	require.NoError(t, err, "DCR must succeed on pre-discovered path with non-root issuer")
	assert.Equal(t, "pre-discovered-client", config.ClientID)
	assert.EqualValues(t, 1, atomic.LoadInt32(&registrationHits))
}

// TestHandleDynamicRegistration_SynthesisesEndpointWhenMetadataOmitsIt
// verifies that when the re-discovery call inside resolveDCRCredentials returns
// ErrRegistrationEndpointMissing (valid metadata but no registration_endpoint),
// the endpoint is synthesised as {issuer}/register and the registration is
// routed there — mirroring the nanobot/Hydra convention that the resolver's
// DiscoveryURL branch applies. The pre-discovered path is used to bypass the
// handleDynamicRegistration guard that otherwise exits early when
// registration_endpoint is absent from the initial discovery document.
func TestHandleDynamicRegistration_SynthesisesEndpointWhenMetadataOmitsIt(t *testing.T) {
	t.Parallel()

	var registrationHits int32
	var server *httptest.Server
	mux := http.NewServeMux()

	// Metadata has all required fields but deliberately omits
	// registration_endpoint, causing FetchAuthorizationServerMetadata to
	// return (partialMeta, ErrRegistrationEndpointMissing).
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		md := oauthproto.AuthorizationServerMetadata{
			Issuer:                            server.URL,
			AuthorizationEndpoint:             server.URL + "/authorize",
			TokenEndpoint:                     server.URL + "/token",
			CodeChallengeMethodsSupported:     []string{"S256"},
			TokenEndpointAuthMethodsSupported: []string{"none"},
			// registration_endpoint intentionally absent
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(md)
	})

	// resolveDCRCredentials synthesises {issuer}/register for a root issuer.
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		atomic.AddInt32(&registrationHits, 1)
		resp := oauthproto.DynamicClientRegistrationResponse{
			ClientID:                "synthesised-client",
			TokenEndpointAuthMethod: "none",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	})

	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)

	// The pre-discovered path sets RegistrationEndpoint so
	// handleDynamicRegistration's guard passes; resolveDCRCredentials then
	// re-fetches and hits the ErrRegistrationEndpointMissing synthesis branch.
	config := &OAuthFlowConfig{
		Scopes:               []string{"openid", "profile"},
		CallbackPort:         8765,
		RegistrationEndpoint: server.URL + "/register",
		AuthorizeURL:         server.URL + "/authorize",
		TokenURL:             server.URL + "/token",
	}

	err := handleDynamicRegistration(context.Background(), server.URL, config)
	require.NoError(t, err, "DCR must succeed when registration_endpoint is absent from re-fetched metadata")
	assert.Equal(t, "synthesised-client", config.ClientID)
	assert.EqualValues(t, 1, atomic.LoadInt32(&registrationHits),
		"synthesised endpoint must be contacted exactly once")
}

// TestHandleDynamicRegistration_PopulatesEndpointsAndRenewalMetadata verifies
// the contract the rest of the CLI flow depends on: handleDynamicRegistration
// writes the resolved endpoints and DCR renewal metadata onto OAuthFlowConfig.
func TestHandleDynamicRegistration_PopulatesEndpointsAndRenewalMetadata(t *testing.T) {
	t.Parallel()

	const (
		callbackPort          = 8765
		secretExpiryUnix      = int64(1_893_456_000)
		registrationToken     = "registration-access-token"
		registrationClientURI = "https://issuer.example/register/cli-registered-client"
	)
	server := newDCRDiscoveryServer(t, dcrTestServerConfig{
		codeChallengeMethodsSupported: []string{"S256"},
		clientSecretExpiresAt:         secretExpiryUnix,
		registrationAccessToken:       registrationToken,
		registrationClientURI:         registrationClientURI,
	})

	config := &OAuthFlowConfig{
		Scopes:          []string{"openid", "profile"},
		CallbackPort:    callbackPort,
		AllowPrivateIPs: true, // loopback test server; guard would otherwise refuse to dial it
	}

	err := handleDynamicRegistration(context.Background(), server.URL, config)
	require.NoError(t, err)
	assert.Equal(t, "cli-registered-client", config.ClientID,
		"handleDynamicRegistration must populate OAuthFlowConfig.ClientID")
	assert.Equal(t, server.URL+"/authorize", config.AuthorizeURL,
		"handleDynamicRegistration must populate OAuthFlowConfig.AuthorizeURL")
	assert.Equal(t, server.URL+"/token", config.TokenURL,
		"handleDynamicRegistration must populate OAuthFlowConfig.TokenURL")
	assert.Equal(t, time.Unix(secretExpiryUnix, 0).UTC(), config.SecretExpiry)
	assert.Equal(t, registrationToken, config.RegistrationAccessToken)
	assert.Equal(t, registrationClientURI, config.RegistrationClientURI)
	assert.Equal(t, "none", config.TokenEndpointAuthMethod)
	assert.Equal(t, callbackPort, config.RegisteredCallbackPort)
}
