// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func TestAuthorizeHandler_MissingClientID(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize", nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	// fosite returns 401 with invalid_client for missing/invalid client_id
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_client")
}

func TestAuthorizeHandler_MissingRedirectURI(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?client_id="+testAuthClientID, nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	// When redirect_uri is missing but client has registered URIs, fosite uses the
	// first registered URI and redirects with an error. If the client has exactly
	// one registered URI, fosite may accept the request.
	// Check that we get either a 400 error or a 303 redirect with error
	if rec.Code == http.StatusSeeOther {
		location := rec.Header().Get("Location")
		assert.Contains(t, location, "error=")
	} else {
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "invalid_request")
	}
}

func TestAuthorizeHandler_ClientNotFound(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?client_id=unknown&redirect_uri=http://example.com", nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	// fosite returns 401 with invalid_client for unknown clients
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_client")
}

func TestAuthorizeHandler_InvalidRedirectURI(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?client_id="+testAuthClientID+"&redirect_uri=http://evil.com/callback", nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	// fosite returns 400 with invalid_request for invalid redirect_uri
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_request")
}

func TestAuthorizeHandler_UnsupportedResponseType(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	params := url.Values{
		"client_id":     {testAuthClientID},
		"redirect_uri":  {testAuthRedirectURI},
		"response_type": {"token"}, // implicit flow not supported
		"state":         {"test-state"},
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	// fosite uses 303 See Other for error redirects per RFC 6749
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=unsupported_response_type")
	assert.Contains(t, location, "state=test-state")
}

func TestAuthorizeHandler_PKCENotValidatedAtAuthorizeEndpoint(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	// Note: Per RFC 7636, PKCE code_challenge is accepted at the authorize endpoint,
	// but the code_verifier is only validated at the token endpoint. Fosite follows
	// this pattern, so requests without code_challenge are accepted at /authorize
	// and will fail at /token instead.
	params := url.Values{
		"client_id":     {testAuthClientID},
		"redirect_uri":  {testAuthRedirectURI},
		"response_type": {"code"},
		"state":         {"test-state"},
		// Missing code_challenge - fosite accepts this at authorize endpoint
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	// Fosite accepts requests without PKCE at authorize endpoint per RFC 7636
	// PKCE validation happens at the token endpoint
	assert.Equal(t, http.StatusFound, rec.Code)
	location := rec.Header().Get("Location")
	// Should redirect to upstream IDP (not return error)
	assert.Contains(t, location, "https://idp.example.com/authorize")
}

func TestAuthorizeHandler_PlainChallengeMethodAcceptedButValidatedAtToken(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	// Note: Similar to missing PKCE, the challenge method is captured at authorize
	// but validated at token endpoint. The config has EnablePKCEPlainChallengeMethod=false,
	// which will reject "plain" method at the token endpoint.
	params := url.Values{
		"client_id":             {testAuthClientID},
		"redirect_uri":          {testAuthRedirectURI},
		"response_type":         {"code"},
		"state":                 {"test-state"},
		"code_challenge":        {"challenge123"},
		"code_challenge_method": {"plain"}, // Will fail at token endpoint, not authorize
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	// Fosite accepts requests at authorize endpoint; validation happens at token endpoint
	assert.Equal(t, http.StatusFound, rec.Code)
	location := rec.Header().Get("Location")
	// Should redirect to upstream IDP (not return error at authorize endpoint)
	assert.Contains(t, location, "https://idp.example.com/authorize")
}

func TestNewHandler_AllowsEmptyUpstreams(t *testing.T) {
	t.Parallel()

	handler, _, _ := handlerTestSetup(t)
	_, err := NewHandler(handler.provider, handler.config, handler.storage, nil)
	require.NoError(t, err)
}

// TestNewHandler_ErrorsOnNilConfig pins the constructor invariant that a
// nil AuthorizationServerConfig (or one with a nil embedded *fosite.Config)
// is rejected at construction time. Without this guard, issuer() (and any
// other helper that reads config.Config.*) panics inside an HTTP handler
// at request time — far harder to diagnose than a startup error.
func TestNewHandler_ErrorsOnNilConfig(t *testing.T) {
	t.Parallel()

	upstreams := []NamedUpstream{{Name: "x", Provider: nil}}

	_, err := NewHandler(nil, nil, nil, upstreams)
	require.Error(t, err, "NewHandler should error when AuthorizationServerConfig is nil")

	_, err = NewHandler(nil, &server.AuthorizationServerConfig{}, nil, upstreams)
	require.Error(t, err,
		"NewHandler should error when AuthorizationServerConfig.Config is nil")
}

// TestNewHandler_ErrorsOnDuplicateUpstreamNames pins that the constructor rejects
// duplicate upstream names. Names must be unique: upstreamByName returns the first
// match, tokens are keyed by name, and the authorization chain is keyed by name, so
// a duplicate would silently shadow a provider.
func TestNewHandler_ErrorsOnDuplicateUpstreamNames(t *testing.T) {
	t.Parallel()

	_, oauth2Config, stor, _ := baseTestSetup(t)
	upstreams := []NamedUpstream{
		{Name: "dup", Provider: &mockIDPProvider{}},
		{Name: "dup", Provider: &mockIDPProvider{}},
	}

	_, err := NewHandler(nil, oauth2Config, stor, upstreams)
	require.Error(t, err, "NewHandler should reject duplicate upstream names")
	assert.ErrorContains(t, err, "duplicate upstream name")
}

func TestAuthorizeHandler_RedirectsToUpstream(t *testing.T) {
	t.Parallel()
	handler, storState, mockUpstream := handlerTestSetup(t)

	params := url.Values{
		"client_id":             {testAuthClientID},
		"redirect_uri":          {testAuthRedirectURI},
		"response_type":         {"code"},
		"state":                 {"client-state"},
		"code_challenge":        {"challenge123"},
		"code_challenge_method": {"S256"},
		"scope":                 {"openid profile"},
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	// Should redirect to upstream IDP
	assert.Equal(t, http.StatusFound, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "https://idp.example.com/authorize")

	// Should have captured the internal state
	assert.NotEmpty(t, mockUpstream.capturedState)

	// Should have sent PKCE challenge to upstream IDP
	assert.NotEmpty(t, mockUpstream.capturedCodeChallenge, "upstream PKCE challenge should be set")

	// Should have stored pending authorization
	pending, ok := storState.pendingAuths[mockUpstream.capturedState]
	require.True(t, ok, "pending authorization should be stored")
	assert.Equal(t, testAuthClientID, pending.ClientID)
	assert.Equal(t, testAuthRedirectURI, pending.RedirectURI)
	assert.Equal(t, "client-state", pending.State)
	assert.Equal(t, "challenge123", pending.PKCEChallenge)
	assert.Equal(t, "S256", pending.PKCEMethod)
	assert.Contains(t, pending.Scopes, "openid")
	assert.Contains(t, pending.Scopes, "profile")

	// Should have stored upstream PKCE verifier
	assert.NotEmpty(t, pending.UpstreamPKCEVerifier, "upstream PKCE verifier should be stored")

	// Should have stored upstream nonce (nonce is generated and stored for upstream OIDC)
	assert.NotEmpty(t, pending.UpstreamNonce, "upstream nonce should be stored")

	// Verify the challenge matches the stored verifier
	assert.Equal(t, servercrypto.ComputePKCEChallenge(pending.UpstreamPKCEVerifier), mockUpstream.capturedCodeChallenge)
}

// registerLoopbackClient creates a public client with loopback redirect URIs (as
// DCR/CIMD would) and registers it in storState so the mock GetClient call the
// handler makes can resolve it.
func registerLoopbackClient(t *testing.T, storState *testStorageState, clientID string, redirectURIs ...string) {
	t.Helper()

	client, err := registration.New(registration.Config{
		ID:                      clientID,
		RedirectURIs:            redirectURIs,
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodNone,
	})
	require.NoError(t, err)
	storState.clients[clientID] = client
}

func TestAuthorizeHandler_LoopbackLocalhostDynamicPortIsAccepted(t *testing.T) {
	t.Parallel()
	handler, storState, mockUpstream := handlerTestSetup(t)

	const clientID = "loopback-localhost-client"
	registerLoopbackClient(t, storState, clientID, "http://localhost/callback")

	params := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {"http://localhost:54321/callback"},
		"response_type":         {"code"},
		"state":                 {"client-state"},
		"code_challenge":        {"challenge123"},
		"code_challenge_method": {"S256"},
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	require.Equal(t, http.StatusFound, rec.Code, "response body: %s", rec.Body.String())
	assert.Contains(t, rec.Header().Get("Location"), "https://idp.example.com/authorize")

	pending, ok := storState.pendingAuths[mockUpstream.capturedState]
	require.True(t, ok, "pending authorization should be stored")
	assert.Equal(t, "http://localhost:54321/callback", pending.RedirectURI,
		"the dynamic-port redirect_uri, not the portless registered one, must be preserved")
}

// TestAuthorizeHandler_LoopbackCaseInsensitiveLocalhostIsAccepted pins that a
// mixed-case "localhost" hostname is matched the same as lowercase --
// registration.hostnamesMatch treats "localhost" case-insensitively, and
// rewriteLoopbackRedirectURI's own pre-filter must not be stricter than the
// matcher it's gating (it previously used networking.IsLocalhost, a
// case-SENSITIVE check, which silently rejected "LOCALHOST" before the
// case-insensitive matcher ever saw it).
func TestAuthorizeHandler_LoopbackCaseInsensitiveLocalhostIsAccepted(t *testing.T) {
	t.Parallel()
	handler, storState, mockUpstream := handlerTestSetup(t)

	const clientID = "loopback-uppercase-localhost-client"
	registerLoopbackClient(t, storState, clientID, "http://localhost/callback")

	params := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {"http://LOCALHOST:54321/callback"},
		"response_type":         {"code"},
		"state":                 {"client-state"},
		"code_challenge":        {"challenge123"},
		"code_challenge_method": {"S256"},
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	require.Equal(t, http.StatusFound, rec.Code, "response body: %s", rec.Body.String())

	pending, ok := storState.pendingAuths[mockUpstream.capturedState]
	require.True(t, ok, "pending authorization should be stored")
	assert.Equal(t, "http://LOCALHOST:54321/callback", pending.RedirectURI)
}

func TestAuthorizeHandler_Loopback127001DynamicPortStillWorks(t *testing.T) {
	t.Parallel()
	handler, storState, mockUpstream := handlerTestSetup(t)

	const clientID = "loopback-127001-client"
	registerLoopbackClient(t, storState, clientID, "http://127.0.0.1/callback")

	params := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {"http://127.0.0.1:54321/callback"},
		"response_type":         {"code"},
		"state":                 {"client-state"},
		"code_challenge":        {"challenge123"},
		"code_challenge_method": {"S256"},
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	require.Equal(t, http.StatusFound, rec.Code, "response body: %s", rec.Body.String())

	pending, ok := storState.pendingAuths[mockUpstream.capturedState]
	require.True(t, ok, "pending authorization should be stored")
	assert.Equal(t, "http://127.0.0.1:54321/callback", pending.RedirectURI)
}

func TestAuthorizeHandler_LoopbackUnregisteredRedirectURIRejected(t *testing.T) {
	t.Parallel()
	handler, storState, _ := handlerTestSetup(t)

	const clientID = "loopback-unregistered-client"
	registerLoopbackClient(t, storState, clientID, "http://localhost/callback")

	params := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {"http://localhost:54321/not-the-registered-path"},
		"response_type":         {"code"},
		"state":                 {"client-state"},
		"code_challenge":        {"challenge123"},
		"code_challenge_method": {"S256"},
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_request")
}

// TestAuthorizeHandler_LoopbackErrorRedirectsToDynamicPort proves that a
// validation failure occurring after redirect_uri validation succeeds (here,
// an unsupported response_type) redirects to the client's real dynamic-port
// listener, not the registered portless placeholder that
// rewriteLoopbackRedirectURI substituted for fosite's exact-match check. See
// loopbackAuthorizeRequester for the mechanism.
func TestAuthorizeHandler_LoopbackErrorRedirectsToDynamicPort(t *testing.T) {
	t.Parallel()
	handler, storState, _ := handlerTestSetup(t)

	const clientID = "loopback-error-redirect-client"
	registerLoopbackClient(t, storState, clientID, "http://localhost/callback")

	params := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {"http://localhost:54321/callback"},
		"response_type": {"token"}, // implicit flow not supported
		"state":         {"client-state"},
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	// fosite uses 303 See Other for error redirects per RFC 6749
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=unsupported_response_type")
	assert.Contains(t, location, "state=client-state")
	assert.True(t, strings.HasPrefix(location, "http://localhost:54321/callback"),
		"error redirect must target the client's real dynamic-port listener, got: %s", location)
}

// TestAuthorizeHandler_Loopback127001ErrorRedirectsToDynamicPort proves an
// IP-literal loopback client (127.0.0.1) keeps working exactly as it did
// before the "localhost" loopback rewrite was introduced:
// rewriteLoopbackRedirectURI skips IP literals entirely, since fosite's own
// native loopback matching already preserves the dynamic port for these on
// both success and error redirects.
func TestAuthorizeHandler_Loopback127001ErrorRedirectsToDynamicPort(t *testing.T) {
	t.Parallel()
	handler, storState, _ := handlerTestSetup(t)

	const clientID = "loopback-127001-error-redirect-client"
	registerLoopbackClient(t, storState, clientID, "http://127.0.0.1/callback")

	params := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {"http://127.0.0.1:54321/callback"},
		"response_type": {"token"}, // implicit flow not supported
		"state":         {"client-state"},
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	assert.Equal(t, http.StatusSeeOther, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=unsupported_response_type")
	assert.True(t, strings.HasPrefix(location, "http://127.0.0.1:54321/callback"),
		"error redirect must preserve the dynamic port for IP-literal loopback clients, got: %s", location)
}

// TestAuthorizeHandler_LoopbackPostValidationErrorRedirectsToDynamicPort proves
// that a failure AFTER NewAuthorizeRequest succeeds -- not just a validation
// failure inside it -- still redirects to the client's real dynamic-port
// listener rather than the registered portless literal. StorePendingAuthorization
// failing is the regression case: ar is valid at that point (rewriteLoopbackRedirectURI
// already substituted the portless literal into it), so the error path must
// use the wrapped requester too, not the raw one.
func TestAuthorizeHandler_LoopbackPostValidationErrorRedirectsToDynamicPort(t *testing.T) {
	t.Parallel()
	handler, storState, _ := handlerTestSetup(t, withStorePendingError(errors.New("storage unavailable")))

	const clientID = "loopback-post-validation-error-client"
	registerLoopbackClient(t, storState, clientID, "http://localhost/callback")

	params := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {"http://localhost:54321/callback"},
		"response_type":         {"code"},
		"state":                 {"client-state"},
		"code_challenge":        {"challenge123"},
		"code_challenge_method": {"S256"},
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	assert.Equal(t, http.StatusSeeOther, rec.Code)
	location := rec.Header().Get("Location")
	assert.Contains(t, location, "error=server_error")
	assert.True(t, strings.HasPrefix(location, "http://localhost:54321/callback"),
		"a post-validation failure must still redirect to the client's real dynamic-port listener, got: %s", location)
}

func TestLoopbackAuthorizeRequester_IsRedirectURIValid(t *testing.T) {
	t.Parallel()

	loopbackClient, err := registration.New(registration.Config{
		ID:                      "wrapper-test-client",
		RedirectURIs:            []string{"http://localhost/callback"},
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodNone,
	})
	require.NoError(t, err)

	confidentialClient, err := registration.New(registration.Config{
		ID:                      "wrapper-test-confidential-client",
		Secret:                  "s3cr3t-plaintext",
		RedirectURIs:            []string{"https://example.com/callback"},
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodClientSecretBasic,
	})
	require.NoError(t, err)

	tests := []struct {
		name string
		// arRedirectURI, when non-empty, seeds the embedded requester's own
		// RedirectURI, exercising fosite's own IsRedirectURIValid. Left empty
		// (nil ar.RedirectURI) for cases that must exercise only the fallback.
		arRedirectURI string
		client        fosite.Client
		redirectURI   string
		wantValid     bool
	}{
		{
			name:        "nil client is invalid",
			client:      nil,
			redirectURI: "http://localhost:54321/callback",
			wantValid:   false,
		},
		{
			name:        "genuine loopback dynamic-port match is valid",
			client:      loopbackClient,
			redirectURI: "http://localhost:54321/callback",
			wantValid:   true,
		},
		{
			name:        "unregistered path is not a loopback match",
			client:      loopbackClient,
			redirectURI: "http://localhost:54321/not-registered",
			wantValid:   false,
		},
		{
			// Pins the widen-only invariant (see IsRedirectURIValid's doc
			// comment): would fail if the override reverted to consulting
			// only the public-clients-only loopback matcher, since
			// RegisteredLoopbackRedirectURI unconditionally rejects
			// confidential clients via its !IsPublic() guard.
			name:          "confidential client with exact-match redirect_uri is valid via fosite's own check",
			arRedirectURI: "https://example.com/callback",
			client:        confidentialClient,
			redirectURI:   "https://example.com/callback",
			wantValid:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			redirectURI, err := url.Parse(tt.redirectURI)
			require.NoError(t, err)

			ar := fosite.NewAuthorizeRequest()
			ar.Client = tt.client
			if tt.arRedirectURI != "" {
				ar.RedirectURI, err = url.Parse(tt.arRedirectURI)
				require.NoError(t, err)
			}
			wrapped := &loopbackAuthorizeRequester{AuthorizeRequester: ar, redirectURI: redirectURI}

			assert.Equal(t, tt.wantValid, wrapped.IsRedirectURIValid())
		})
	}
}
