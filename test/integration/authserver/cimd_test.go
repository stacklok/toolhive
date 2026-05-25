// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/oauthproto/cimd"
	"github.com/stacklok/toolhive/test/integration/authserver/helpers"
)

// serveCIMDDoc starts an httptest.Server serving a valid CIMD document at
// /metadata.json. The document's client_id equals the full URL
// ("http://" + r.Host + "/metadata.json"), and redirect_uris contains
// "http://localhost:8080/callback". The server is registered for cleanup
// via t.Cleanup. Returns the server and the full CIMD URL string.
func serveCIMDDoc(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metadata.json" {
			http.NotFound(w, r)
			return
		}
		clientID := "http://" + r.Host + r.URL.Path
		doc := cimd.ClientMetadataDocument{
			ClientID:     clientID,
			RedirectURIs: []string{"http://localhost:8080/callback"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)

	cimdURL := srv.URL + "/metadata.json"
	return cimdURL
}

// TestEmbeddedAuthServer_CIMD_DiscoveryAdvertisesSupport verifies that both
// discovery endpoints advertise client_id_metadata_document_supported: true
// when CIMD is enabled, and omit / set it to false when CIMD is disabled.
//
//nolint:paralleltest,tparallel // Subtests share expensive test fixtures
func TestEmbeddedAuthServer_CIMD_DiscoveryAdvertisesSupport(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	upstream := helpers.NewMockUpstreamIDP(t)

	t.Run("CIMD enabled advertises support in both discovery endpoints", func(t *testing.T) {
		cfg := helpers.NewTestAuthServerConfig(t, upstream.URL(),
			helpers.WithCIMD(&authserver.CIMDRunConfig{
				Enabled:      true,
				CacheMaxSize: 16,
			}),
		)

		authServer := helpers.NewEmbeddedAuthServer(ctx, t, cfg)
		server := httptest.NewServer(authServer.Handler())
		t.Cleanup(server.Close)

		client := helpers.NewOAuthClient(server.URL)

		oauthMeta, statusCode, err := client.GetOAuthDiscovery()
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, statusCode)
		assert.Equal(t, true, oauthMeta["client_id_metadata_document_supported"],
			"OAuth discovery must advertise client_id_metadata_document_supported: true when CIMD is enabled")

		oidcMeta, statusCode, err := client.GetOIDCDiscovery()
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, statusCode)
		assert.Equal(t, true, oidcMeta["client_id_metadata_document_supported"],
			"OIDC discovery must advertise client_id_metadata_document_supported: true when CIMD is enabled")
	})

	t.Run("CIMD disabled does not advertise support", func(t *testing.T) {
		// No WithCIMD option — CIMD is disabled by default.
		cfg := helpers.NewTestAuthServerConfig(t, upstream.URL())

		authServer := helpers.NewEmbeddedAuthServer(ctx, t, cfg)
		server := httptest.NewServer(authServer.Handler())
		t.Cleanup(server.Close)

		client := helpers.NewOAuthClient(server.URL)

		oauthMeta, statusCode, err := client.GetOAuthDiscovery()
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, statusCode)
		// Field absent or false — both mean "not supported".
		cimdFlag := oauthMeta["client_id_metadata_document_supported"]
		assert.True(t, cimdFlag == nil || cimdFlag == false,
			"OAuth discovery must not advertise CIMD support when disabled (got %v)", cimdFlag)

		oidcMeta, statusCode, err := client.GetOIDCDiscovery()
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, statusCode)
		cimdFlag = oidcMeta["client_id_metadata_document_supported"]
		assert.True(t, cimdFlag == nil || cimdFlag == false,
			"OIDC discovery must not advertise CIMD support when disabled (got %v)", cimdFlag)
	})
}

// TestEmbeddedAuthServer_CIMD_AuthorizeAcceptsCIMDClientID verifies that when
// CIMD is enabled, the authorization endpoint accepts a CIMD URL as client_id
// and redirects to the upstream IDP without requiring prior DCR registration.
func TestEmbeddedAuthServer_CIMD_AuthorizeAcceptsCIMDClientID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	upstream := helpers.NewMockUpstreamIDP(t)
	cimdURL := serveCIMDDoc(t)

	cfg := helpers.NewTestAuthServerConfig(t, upstream.URL(),
		helpers.WithCIMD(&authserver.CIMDRunConfig{
			Enabled:      true,
			CacheMaxSize: 16,
		}),
	)

	authServer := helpers.NewEmbeddedAuthServer(ctx, t, cfg)
	server := httptest.NewServer(authServer.Handler())
	t.Cleanup(server.Close)

	client := helpers.NewOAuthClient(server.URL)

	verifier := servercrypto.GeneratePKCEVerifier()
	challenge := servercrypto.ComputePKCEChallenge(verifier)

	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {cimdURL},
		"redirect_uri":          {"http://localhost:8080/callback"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {"test-state-cimd"},
		"resource":              {cfg.AllowedAudiences[0]},
	}

	resp, err := client.StartAuthorization(params)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = resp.Body.Close()
	})

	// CIMD resolution must succeed and redirect to the upstream IDP — not an
	// invalid_client error.
	assert.Equal(t, http.StatusFound, resp.StatusCode,
		"CIMD-resolved client must produce a 302 redirect to the upstream IDP")

	location := resp.Header.Get("Location")
	assert.NotEmpty(t, location, "Location header must be set on redirect")

	redirectURL, err := url.Parse(location)
	require.NoError(t, err)
	assert.Contains(t, redirectURL.String(), upstream.URL(),
		"redirect Location must point to the upstream IDP authorization endpoint")
}

// TestEmbeddedAuthServer_CIMD_DisabledRejectsCIMDClientID verifies that when
// CIMD is disabled, a CIMD URL presented as client_id is rejected — it is not
// resolved via the metadata document protocol and the request does not
// produce a 302 redirect to the upstream IDP.
func TestEmbeddedAuthServer_CIMD_DisabledRejectsCIMDClientID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	upstream := helpers.NewMockUpstreamIDP(t)
	cimdURL := serveCIMDDoc(t)

	// No WithCIMD option — CIMD is disabled.
	cfg := helpers.NewTestAuthServerConfig(t, upstream.URL())

	authServer := helpers.NewEmbeddedAuthServer(ctx, t, cfg)
	server := httptest.NewServer(authServer.Handler())
	t.Cleanup(server.Close)

	client := helpers.NewOAuthClient(server.URL)

	verifier := servercrypto.GeneratePKCEVerifier()
	challenge := servercrypto.ComputePKCEChallenge(verifier)

	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {cimdURL},
		"redirect_uri":          {"http://localhost:8080/callback"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {"test-state-cimd-disabled"},
		"resource":              {cfg.AllowedAudiences[0]},
	}

	resp, err := client.StartAuthorization(params)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = resp.Body.Close()
	})

	// With CIMD disabled the CIMD URL is treated as an unknown opaque client_id
	// and the authorize request must fail — either a non-302 error response or
	// a redirect to the client's redirect_uri carrying an error parameter, but
	// NOT a redirect to the upstream IDP.
	location := resp.Header.Get("Location")

	isUpstreamRedirect := func() bool {
		if location == "" {
			return false
		}
		redirectURL, parseErr := url.Parse(location)
		if parseErr != nil {
			return false
		}
		return redirectURL.Host == mustParseURL(t, upstream.URL()).Host
	}

	assert.False(t, isUpstreamRedirect(),
		"CIMD disabled: authorize must NOT redirect to the upstream IDP (Location: %q)", location)

	// The response must signal an error — either directly (4xx) or as an
	// error redirect to the registered redirect_uri.
	if resp.StatusCode == http.StatusFound {
		// Redirect-with-error case: the redirect must carry an error parameter
		// and must NOT point to the upstream IDP (already asserted above).
		redirectURL, err := url.Parse(location)
		require.NoError(t, err)
		assert.NotEmpty(t, redirectURL.Query().Get("error"),
			"error redirect must carry an error query parameter")
	} else {
		assert.GreaterOrEqual(t, resp.StatusCode, http.StatusBadRequest,
			"CIMD disabled: authorize must return an error status (4xx) when client_id is unrecognised")
	}
}

// TestEmbeddedAuthServer_CIMD_NoDCRRequired verifies that when CIMD is enabled
// a client can complete the authorization step without any prior call to the
// DCR registration endpoint. This is the core CIMD value proposition: the
// client_id URL itself carries the registration metadata.
func TestEmbeddedAuthServer_CIMD_NoDCRRequired(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	upstream := helpers.NewMockUpstreamIDP(t)
	cimdURL := serveCIMDDoc(t)

	cfg := helpers.NewTestAuthServerConfig(t, upstream.URL(),
		helpers.WithCIMD(&authserver.CIMDRunConfig{
			Enabled:      true,
			CacheMaxSize: 16,
		}),
	)

	authServer := helpers.NewEmbeddedAuthServer(ctx, t, cfg)
	server := httptest.NewServer(authServer.Handler())
	t.Cleanup(server.Close)

	// Deliberately do NOT call client.RegisterClient() before StartAuthorization.
	// This test asserts that the absence of a prior DCR call is not an obstacle.
	client := helpers.NewOAuthClient(server.URL)

	verifier := servercrypto.GeneratePKCEVerifier()
	challenge := servercrypto.ComputePKCEChallenge(verifier)

	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {cimdURL},
		"redirect_uri":          {"http://localhost:8080/callback"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {"test-state-no-dcr"},
		"resource":              {cfg.AllowedAudiences[0]},
	}

	resp, err := client.StartAuthorization(params)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = resp.Body.Close()
	})

	// Without any DCR call the authorize request must still succeed because
	// the CIMD decorator resolves the client on the fly from the metadata URL.
	assert.Equal(t, http.StatusFound, resp.StatusCode,
		"authorize must succeed (302 to upstream) without any prior DCR call when CIMD is enabled")

	location := resp.Header.Get("Location")
	assert.NotEmpty(t, location)

	redirectURL, err := url.Parse(location)
	require.NoError(t, err)
	assert.Contains(t, redirectURL.String(), upstream.URL(),
		"Location must point to the upstream IDP, proving CIMD resolved the client without DCR")
}

// mustParseURL parses rawURL and fails the test on error.
func mustParseURL(t *testing.T, rawURL string) *url.URL {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err, "failed to parse URL %q", rawURL)
	return u
}
