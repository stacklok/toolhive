// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/ory/fosite"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func TestPromptRequiresConsent(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		prompt []string
		want   bool
	}{
		{name: "missing"},
		{name: "consent", prompt: []string{"consent"}, want: true},
		{name: "space separated", prompt: []string{"login consent"}, want: true},
		{name: "repeated", prompt: []string{"login", "consent"}, want: true},
		{name: "not a token", prompt: []string{"consented"}},
		{name: "other prompt", prompt: []string{"login"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, promptRequiresConsent(url.Values{"prompt": tt.prompt}))
		})
	}
}

func TestConsentExemptEligibility(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name                string
		allowlisted         bool
		public              bool
		backChannelOnly     bool
		grantTypes          []string
		clientResponseTypes []string
		requestTypes        fosite.Arguments
		redirectURIs        []string
		requestRedirects    []string
		rewrittenFrom       string
		want                bool
	}{
		{name: "eligible", allowlisted: true, grantTypes: []string{"authorization_code"}, clientResponseTypes: []string{"code"}, requestTypes: fosite.Arguments{"code"}, redirectURIs: []string{testAuthRedirectURI}, requestRedirects: []string{testAuthRedirectURI}, want: true},
		{name: "not allowlisted", grantTypes: []string{"authorization_code"}, clientResponseTypes: []string{"code"}, requestTypes: fosite.Arguments{"code"}, redirectURIs: []string{testAuthRedirectURI}, requestRedirects: []string{testAuthRedirectURI}},
		{name: "public client", allowlisted: true, public: true, grantTypes: []string{"authorization_code"}, clientResponseTypes: []string{"code"}, requestTypes: fosite.Arguments{"code"}, redirectURIs: []string{testAuthRedirectURI}, requestRedirects: []string{testAuthRedirectURI}},
		{name: "back-channel-only client", allowlisted: true, backChannelOnly: true, grantTypes: []string{"urn:ietf:params:oauth:grant-type:token-exchange"}, requestTypes: fosite.Arguments{"code"}, redirectURIs: []string{testAuthRedirectURI}, requestRedirects: []string{testAuthRedirectURI}},
		{name: "missing authorization-code grant", allowlisted: true, grantTypes: []string{"refresh_token"}, clientResponseTypes: []string{"code"}, requestTypes: fosite.Arguments{"code"}, redirectURIs: []string{testAuthRedirectURI}, requestRedirects: []string{testAuthRedirectURI}},
		{name: "missing code response type", allowlisted: true, grantTypes: []string{"authorization_code"}, clientResponseTypes: []string{"token"}, requestTypes: fosite.Arguments{"code"}, redirectURIs: []string{testAuthRedirectURI}, requestRedirects: []string{testAuthRedirectURI}},
		{name: "request response type is not exactly code", allowlisted: true, grantTypes: []string{"authorization_code"}, clientResponseTypes: []string{"code"}, requestTypes: fosite.Arguments{"code", "token"}, redirectURIs: []string{testAuthRedirectURI}, requestRedirects: []string{testAuthRedirectURI}},
		{name: "missing explicit redirect", allowlisted: true, grantTypes: []string{"authorization_code"}, clientResponseTypes: []string{"code"}, requestTypes: fosite.Arguments{"code"}, redirectURIs: []string{testAuthRedirectURI}},
		{name: "empty explicit redirect", allowlisted: true, grantTypes: []string{"authorization_code"}, clientResponseTypes: []string{"code"}, requestTypes: fosite.Arguments{"code"}, redirectURIs: []string{testAuthRedirectURI}, requestRedirects: []string{""}},
		{name: "repeated explicit redirect", allowlisted: true, grantTypes: []string{"authorization_code"}, clientResponseTypes: []string{"code"}, requestTypes: fosite.Arguments{"code"}, redirectURIs: []string{testAuthRedirectURI}, requestRedirects: []string{testAuthRedirectURI, testAuthRedirectURI}},
		{name: "unregistered redirect", allowlisted: true, grantTypes: []string{"authorization_code"}, clientResponseTypes: []string{"code"}, requestTypes: fosite.Arguments{"code"}, redirectURIs: []string{testAuthRedirectURI}, requestRedirects: []string{"https://other.example/callback"}},
		{name: "rewritten redirect", allowlisted: true, grantTypes: []string{"authorization_code"}, clientResponseTypes: []string{"code"}, requestTypes: fosite.Arguments{"code"}, redirectURIs: []string{testAuthRedirectURI}, requestRedirects: []string{testAuthRedirectURI}, rewrittenFrom: "http://localhost:1234/callback"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, state, _ := handlerTestSetup(t)
			client := *state.clients[testAuthClientID].(*fosite.DefaultClient)
			client.Public = tt.public
			client.Secret = []byte("test-secret")
			client.GrantTypes = tt.grantTypes
			client.ResponseTypes = tt.clientResponseTypes
			client.RedirectURIs = tt.redirectURIs
			if tt.backChannelOnly {
				client.ResponseTypes = nil
			}
			if tt.allowlisted {
				h.consentExemptClientIDSet = map[string]struct{}{testAuthClientID: {}}
			}
			ar := fosite.NewAuthorizeRequest()
			ar.Client = &client
			ar.ResponseTypes = tt.requestTypes
			validatedRedirect, err := url.Parse(testAuthRedirectURI)
			require.NoError(t, err)
			ar.RedirectURI = validatedRedirect
			form := make(url.Values)
			if tt.requestRedirects != nil {
				form["redirect_uri"] = tt.requestRedirects
			}
			assert.Equal(t, tt.want, h.consentExempt(ar, form, tt.rewrittenFrom))
		})
	}
}

func TestConsentExemptRejectsValidatedRequestObjectRedirectOverride(t *testing.T) {
	t.Parallel()

	h, state, _ := handlerTestSetup(t)
	client := *state.clients[testAuthClientID].(*fosite.DefaultClient)
	client.Secret = []byte("test-secret")
	client.RedirectURIs = append(client.RedirectURIs, "https://client.example/overridden-callback")
	state.clients[testAuthClientID] = &client
	h.consentExemptClientIDSet = map[string]struct{}{testAuthClientID: {}}
	ar := fosite.NewAuthorizeRequest()
	ar.Client = &client
	ar.ResponseTypes = fosite.Arguments{oauthproto.ResponseTypeCode}
	validatedRedirect, err := url.Parse("https://client.example/overridden-callback")
	require.NoError(t, err)
	ar.RedirectURI = validatedRedirect

	// A signed request object may override the plain form redirect_uri before
	// Fosite validates the redirect. The final redirect must not be exempted.
	originalForm := url.Values{"redirect_uri": {testAuthRedirectURI}}
	require.False(t, h.consentExempt(ar, originalForm, ""))
}

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

func TestAuthorizeRoutes_RejectsMultipartCIMDRequestBeforeLookup(t *testing.T) {
	t.Parallel()

	handler, state, _ := handlerTestSetup(t)

	rec := httptest.NewRecorder()
	handler.Routes().ServeHTTP(rec, multipartAuthorizeRequest(t, "https://client.example.com/metadata"))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "multipart request bodies are not supported\n", rec.Body.String())
	assert.Zero(t, state.getClientCalls, "multipart CIMD request must be rejected before client lookup")
}

func TestAuthorizeRoutes_MultipartBackChannelOnlyAndUnknownClientsMatch(t *testing.T) {
	t.Parallel()

	handler, state, _ := handlerTestSetup(t)
	client, err := registration.NewSPIFFEClient(
		"spiffe-client",
		[]string{"openid"},
		[]string{"https://mcp.example.com"},
		nil,
	)
	require.NoError(t, err)
	state.clients[client.GetID()] = client

	configured := httptest.NewRecorder()
	handler.Routes().ServeHTTP(configured, multipartAuthorizeRequest(t, client.GetID()))
	unknown := httptest.NewRecorder()
	handler.Routes().ServeHTTP(unknown, multipartAuthorizeRequest(t, "unknown-client"))

	assert.Equal(t, http.StatusBadRequest, configured.Code)
	assert.Equal(t, unknown.Code, configured.Code)
	assert.Equal(t, unknown.Body.String(), configured.Body.String())
}

func TestAuthorizeRoutes_QueryParametersStillWork(t *testing.T) {
	t.Parallel()

	handler, _, _ := handlerTestSetup(t)
	req := httptest.NewRequest(http.MethodGet,
		"/oauth/authorize?client_id="+testAuthClientID+"&redirect_uri=http://evil.example/callback", nil)
	rec := httptest.NewRecorder()
	handler.Routes().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_request")
}

func multipartAuthorizeRequest(t *testing.T, clientID string) *http.Request {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("client_id", clientID))
	require.NoError(t, writer.WriteField("redirect_uri", "https://invalid.example/callback"))
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestAuthorizeHandler_BackChannelOnlyClientsMatchMissingClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		client         func(t *testing.T) fosite.Client
		explicitMarker bool
	}{
		{
			name: "SPIFFE client",
			client: func(t *testing.T) fosite.Client {
				t.Helper()
				client, err := registration.NewSPIFFEClient(
					"spiffe-client",
					[]string{"openid"},
					[]string{"https://mcp.example.com"},
					nil,
				)
				require.NoError(t, err)
				return client
			},
			explicitMarker: true,
		},
		{
			name: "delegate client",
			client: func(t *testing.T) fosite.Client {
				t.Helper()
				client, err := registration.NewStaticDelegateClient(registration.Config{
					ID:         "delegate-client",
					Secret:     "test-secret",
					GrantTypes: []string{"urn:ietf:params:oauth:grant-type:token-exchange"},
					Scopes:     []string{"openid"},
					Audience:   []string{"https://mcp.example.com"},
				})
				require.NoError(t, err)
				return client
			},
			// A delegate client is rejected via the pre-existing metadata-shape
			// inference (`consentpolicy.IsBackChannelOnlyClient`), not the explicit
			// registration.BackChannelOnly marker -- delegate clients are out of
			// scope for that marker and must keep their current behaviour.
			explicitMarker: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handler, state, _ := handlerTestSetup(t)
			client := tt.client(t)
			state.clients[client.GetID()] = client

			// Prove classification for this client type is directional: the
			// explicit marker is set (SPIFFE) or is not (delegate), rather than
			// both client types happening to reach the same /authorize outcome
			// via the same mechanism.
			assert.Equal(t, tt.explicitMarker, registration.BackChannelOnly(client))

			missing := httptest.NewRecorder()
			handler.AuthorizeHandler(missing, httptest.NewRequest(http.MethodGet,
				"/oauth/authorize?client_id=missing-client&redirect_uri=https://invalid.example/callback", nil))

			configured := httptest.NewRecorder()
			handler.AuthorizeHandler(configured, httptest.NewRequest(http.MethodGet,
				"/oauth/authorize?client_id="+client.GetID()+"&redirect_uri=https://invalid.example/callback", nil))

			require.Equal(t, http.StatusUnauthorized, configured.Code)
			assert.Equal(t, missing.Code, configured.Code)
			assert.Equal(t, missing.Body.String(), configured.Body.String())
			assert.Contains(t, configured.Body.String(), "invalid_client")
		})
	}
}

func TestAuthorizeHandler_RedisLoadedDelegateClientMatchesMissingClient(t *testing.T) {
	t.Parallel()

	handler, _, _ := handlerTestSetup(t)
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	stor := storage.NewRedisStorageWithClient(redisClient, "test:authorize:")
	t.Cleanup(func() { _ = stor.Close() })

	client, err := registration.NewStaticDelegateClient(registration.Config{
		ID:         "delegate-client",
		Secret:     "test-secret",
		GrantTypes: []string{oauthproto.GrantTypeTokenExchange},
		Scopes:     []string{"openid"},
		Audience:   []string{"https://mcp.example.com"},
	})
	require.NoError(t, err)
	require.NoError(t, stor.RegisterClient(context.Background(), client))
	handler.storage = stor

	missing := httptest.NewRecorder()
	handler.AuthorizeHandler(missing, httptest.NewRequest(http.MethodGet,
		"/oauth/authorize?client_id=missing-client&redirect_uri=https://invalid.example/callback", nil))

	configured := httptest.NewRecorder()
	handler.AuthorizeHandler(configured, httptest.NewRequest(http.MethodGet,
		"/oauth/authorize?client_id=delegate-client&redirect_uri=https://invalid.example/callback", nil))

	require.Equal(t, http.StatusUnauthorized, configured.Code)
	assert.Equal(t, missing.Code, configured.Code)
	assert.Equal(t, missing.Body.String(), configured.Body.String())
	assert.Contains(t, configured.Body.String(), "invalid_client")
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

	// This branch defers the missing-challenge check until code issuance.
	// The client must still pass consent before reaching the upstream IDP.
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

	// Accepting the authorization request shows consent, not an upstream redirect.
	rec = approveConsent(t, handler, rec)
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	location := rec.Header().Get("Location")
	// Approval starts upstream login; missing PKCE is rejected at code issuance.
	assert.Contains(t, location, "https://idp.example.com/authorize")
}

func TestAuthorizeHandler_PlainChallengeMethodAcceptedButValidatedAtToken(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t)

	// The challenge method is captured here and validated later by Fosite.
	// Consent is still required before redirecting upstream.
	params := url.Values{
		"client_id":             {testAuthClientID},
		"redirect_uri":          {testAuthRedirectURI},
		"response_type":         {"code"},
		"state":                 {"test-state"},
		"code_challenge":        {"challenge123"},
		"code_challenge_method": {"plain"}, // Not rejected at the authorize endpoint.
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	rec = approveConsent(t, handler, rec)
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	location := rec.Header().Get("Location")
	// Approval redirects to the upstream IDP; it does not validate the PKCE method.
	assert.Contains(t, location, "https://idp.example.com/authorize")
}

func TestAuthorizeHandler_ResourceSelection(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name      string
		allowed   []string
		resources []string
		want      string
	}{
		{name: "sole default", allowed: []string{"https://api.example.com"}, want: "https://api.example.com"},
		{name: "explicit", allowed: []string{"https://api.example.com", "https://other.example.com"}, resources: []string{"https://other.example.com"}, want: "https://other.example.com"},
		{name: "ambiguous", allowed: []string{"https://api.example.com", "https://other.example.com"}},
		{name: "unconfigured", allowed: nil},
		{name: "repeated", allowed: []string{"https://api.example.com"}, resources: []string{"https://api.example.com", "https://api.example.com"}},
		{name: "empty", allowed: []string{"https://api.example.com"}, resources: []string{""}},
		{name: "unknown", allowed: []string{"https://api.example.com"}, resources: []string{"https://other.example.com"}},
		{name: "malformed", allowed: []string{"https://api.example.com", "relative"}, resources: []string{"relative"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handler, state, upstream := handlerTestSetup(t)
			handler.config.AllowedAudiences = tt.allowed
			params := url.Values{
				"client_id": {testAuthClientID}, "redirect_uri": {testAuthRedirectURI},
				"response_type": {"code"}, "state": {"client-state"},
				"code_challenge": {"challenge123"}, "code_challenge_method": {"S256"},
			}
			if tt.resources != nil {
				params["resource"] = tt.resources
			}
			rec := httptest.NewRecorder()
			handler.AuthorizeHandler(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil))
			if tt.want == "" {
				assert.Equal(t, http.StatusSeeOther, rec.Code)
				assert.Contains(t, rec.Header().Get("Location"), "error=invalid_target")
				assert.Empty(t, upstream.capturedState)
				assert.Empty(t, state.pendingAuths)
				return
			}
			require.Equal(t, http.StatusOK, rec.Code)
			require.Len(t, state.pendingAuths, 1)
			assert.Empty(t, upstream.capturedState)
			for _, pending := range state.pendingAuths {
				assert.Equal(t, tt.want, pending.Resource)
				assert.Equal(t, storage.ConsentStageAwaiting, pending.ConsentStage)
			}
		})
	}
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
	rec = approveConsent(t, handler, rec)

	// Should redirect to upstream IDP
	assert.Equal(t, http.StatusSeeOther, rec.Code)
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

	// Should have bound the pending authorization to this browser: the cookie
	// on the redirect hashes to the stored binding.
	cookie := bindingCookieFrom(t, rec, mockUpstream.capturedState)
	assert.Equal(t, hashBrowserBindingSecret(cookie.Value), pending.BrowserBindingHash)
	assert.Equal(t, "/", cookie.Path)
	assert.True(t, cookie.HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
	assert.Equal(t, int(storage.DefaultPendingAuthorizationTTL.Seconds()), cookie.MaxAge)
	assert.False(t, cookie.Secure, "plain-http test issuer must not set Secure")
	assert.False(t, strings.HasPrefix(cookie.Name, hostCookiePrefix), "plain-http test issuer must not use __Host-")
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
		Audience:                []string{"https://api.example.com"},
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
	rec = approveConsent(t, handler, rec)

	require.Equal(t, http.StatusSeeOther, rec.Code, "response body: %s", rec.Body.String())
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
	rec = approveConsent(t, handler, rec)

	require.Equal(t, http.StatusSeeOther, rec.Code, "response body: %s", rec.Body.String())

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
	rec = approveConsent(t, handler, rec)

	require.Equal(t, http.StatusSeeOther, rec.Code, "response body: %s", rec.Body.String())

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

// TestAuthorizeHandler_CIMDRateLimited also proves the early back-channel-only
// client lookup falls through to fosite when the limiter refuses it, so the 429
// comes from the NewAuthorizeRequest mapping. The loopback rewrite lookup is not
// exercised: the 127.0.0.1 redirect_uri returns before any GetClient call.
func TestAuthorizeHandler_CIMDRateLimited(t *testing.T) {
	t.Parallel()
	handler, _, _ := handlerTestSetup(t,
		withGetClientErrorAfterCalls(0, fosite.ErrNotFound.WithWrap(storage.ErrCIMDRateLimited)))

	req := httptest.NewRequest(http.MethodGet,
		"/oauth/authorize?client_id=https://client.example.com/metadata&redirect_uri=http://127.0.0.1:8080/callback", nil)
	rec := httptest.NewRecorder()

	handler.AuthorizeHandler(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "1", rec.Header().Get("Retry-After"))
}
