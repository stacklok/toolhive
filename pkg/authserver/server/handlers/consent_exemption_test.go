// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	authupstream "github.com/stacklok/toolhive/pkg/authserver/upstream"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

type failingConsentReads struct {
	storage.RememberedConsentStorage
	sessionReads  int
	approvalReads int
}

func (s *failingConsentReads) GetConsentSession(context.Context, string) (*storage.ConsentSession, error) {
	s.sessionReads++
	return nil, errors.New("consent session read must not happen")
}

func (s *failingConsentReads) GetClientApproval(context.Context, string, string) (*storage.ClientApproval, error) {
	s.approvalReads++
	return nil, errors.New("client approval read must not happen")
}

type upstreamAuthorizationCall struct {
	state     string
	challenge string
}

type recordingConsentUpstream struct {
	*mockIDPProvider
	calls []upstreamAuthorizationCall
}

func (p *recordingConsentUpstream) AuthorizationURL(
	state, challenge string, options ...authupstream.AuthorizationOption,
) (string, error) {
	p.calls = append(p.calls, upstreamAuthorizationCall{state: state, challenge: challenge})
	return p.mockIDPProvider.AuthorizationURL(state, challenge, options...)
}

func newConsentExemptHandler(t *testing.T) (*Handler, *testStorageState, *mockIDPProvider, fosite.Client) {
	t.Helper()
	h, state, upstream := handlerTestSetup(t)
	client, err := registration.New(registration.Config{
		ID:                      testAuthClientID,
		Secret:                  "confidential-client-secret",
		RedirectURIs:            []string{testAuthRedirectURI},
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodClientSecretBasic,
		GrantTypes:              []string{oauthproto.GrantTypeAuthorizationCode},
		ResponseTypes:           []string{oauthproto.ResponseTypeCode},
		Audience:                []string{"https://api.example.com"},
	})
	require.NoError(t, err)
	state.clients[testAuthClientID] = client
	h.consentExemptClientIDSet = map[string]struct{}{testAuthClientID: {}}
	return h, state, upstream, client
}

func consentExemptAuthorizeParams(redirectURI string) url.Values {
	params := url.Values{
		"client_id":             {testAuthClientID},
		"response_type":         {"code"},
		"state":                 {"downstream-state"},
		"code_challenge":        {"downstream-challenge"},
		"code_challenge_method": {"S256"},
	}
	if redirectURI != "<omitted>" {
		params["redirect_uri"] = []string{redirectURI}
	}
	return params
}

func TestAuthorizeHandler_ConsentExemptionRequiresExplicitExactRedirect(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name           string
		redirects      []string
		response       string
		wantKind       string
		wantOAuthError string
	}{
		{name: "explicit exact redirect is exempt", redirects: []string{testAuthRedirectURI}, wantKind: "exempt"},
		{name: "inferred redirect requires consent", redirects: []string{"<omitted>"}, wantKind: "consent"},
		{name: "repeated redirect requires consent", redirects: []string{testAuthRedirectURI, testAuthRedirectURI}, wantKind: "consent"},
		{name: "empty redirect requires consent", redirects: []string{""}, wantKind: "consent"},
		{name: "mismatched redirect is OAuth error", redirects: []string{"https://other.example/callback"}, wantKind: "oauth_error", wantOAuthError: "invalid_request"},
		{name: "invalid redirect is OAuth error", redirects: []string{"http://["}, wantKind: "oauth_error", wantOAuthError: "invalid_request"},
		{name: "unsupported response type is OAuth error", redirects: []string{testAuthRedirectURI}, response: "token", wantKind: "oauth_error", wantOAuthError: "unsupported_response_type"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, state, upstream, _ := newConsentExemptHandler(t)
			params := consentExemptAuthorizeParams(tt.redirects[0])
			if len(tt.redirects) > 1 {
				params["redirect_uri"] = tt.redirects
			}
			if tt.response != "" {
				params.Set("response_type", tt.response)
			}
			rec := httptest.NewRecorder()
			h.AuthorizeHandler(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil))

			if tt.wantKind == "exempt" {
				require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
				require.Contains(t, rec.Header().Get("Location"), "https://idp.example.com/authorize")
				require.NotEmpty(t, upstream.capturedState, "an eligible exact redirect starts the upstream flow")
				require.Len(t, state.pendingAuths, 1, "an exempt flow must leave exactly one pending record")
				pending := state.pendingAuths[upstream.capturedState]
				require.NotNil(t, pending)
				require.Equal(t, storage.ConsentStageApproved, pending.ConsentStage,
					"the exempt flow is approved before upstream authentication")
				require.Equal(t, testAuthRedirectURI, pending.RedirectURI,
					"the exact registered redirect remains the downstream callback")
				require.Empty(t, pending.ExpectedUserID)
				require.Empty(t, pending.ExpectedProviderSubject)
				require.Empty(t, pending.ConsentSessionDigest)
				return
			}

			require.Empty(t, upstream.capturedState, "an ineligible or invalid authorization must not start upstream authentication")
			switch tt.wantKind {
			case "consent":
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Contains(t, rec.Body.String(), "Allow this app to sign you in?")
				require.Len(t, state.pendingAuths, 1)
				handle := onlyPendingHandle(t, state)
				require.Equal(t, storage.ConsentStageAwaiting, state.pendingAuths[handle].ConsentStage)
			case "oauth_error":
				require.NotEqual(t, http.StatusOK, rec.Code)
				require.Contains(t, rec.Body.String()+rec.Header().Get("Location"), tt.wantOAuthError)
				require.NotContains(t, rec.Body.String(), "Allow this app to sign you in?")
				require.Empty(t, state.pendingAuths)
			default:
				t.Fatalf("unexpected wantKind %q", tt.wantKind)
			}
		})
	}
}

func TestAuthorizeHandler_ConsentExemptionDoesNotReadRememberedConsent(t *testing.T) {
	t.Parallel()

	h, state, upstream, client := newConsentExemptHandler(t)
	require.True(t, registration.DCRIssued(client),
		"an allowlisted registration.New client remains eligible despite the DCR lifecycle marker")

	reads := &failingConsentReads{}
	h.rememberedStorage = reads
	params := consentExemptAuthorizeParams(testAuthRedirectURI)
	rec := httptest.NewRecorder()
	h.AuthorizeHandler(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil))

	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.NotEmpty(t, upstream.capturedState)
	require.Len(t, state.pendingAuths, 1, "an exempt flow must leave exactly one pending record")
	pending := state.pendingAuths[upstream.capturedState]
	require.NotNil(t, pending)
	require.Equal(t, storage.ConsentStageApproved, pending.ConsentStage)
	require.Empty(t, pending.ExpectedUserID)
	require.Empty(t, pending.ExpectedProviderSubject)
	require.Empty(t, pending.ConsentSessionDigest)
	require.Equal(t, 0, reads.sessionReads, "exemption must not read remembered browser sessions")
	require.Equal(t, 0, reads.approvalReads, "exemption must not read remembered client approvals")
	binding := bindingCookieFrom(t, rec, upstream.capturedState)
	require.Equal(t, hashBrowserBindingSecret(binding.Value), pending.BrowserBindingHash,
		"the exempt upstream redirect remains bound to the initiating browser")
	for _, cookie := range rec.Result().Cookies() {
		require.NotEqual(t, h.consentCookieName(), cookie.Name,
			"the exempt flow must not issue a remembered-consent cookie")
	}
}

func TestAuthorizeHandler_ConsentExemptionCreatesIndependentUpstreamFlows(t *testing.T) {
	t.Parallel()

	h, state, mockUpstream, _ := newConsentExemptHandler(t)
	recorder := &recordingConsentUpstream{mockIDPProvider: mockUpstream}
	h.upstreams[0].Provider = recorder
	var internalStates []string
	var cookies []*http.Cookie
	for _, downstreamState := range []string{"downstream-one", "downstream-two"} {
		params := consentExemptAuthorizeParams(testAuthRedirectURI)
		params.Set("state", downstreamState)
		rec := httptest.NewRecorder()
		h.AuthorizeHandler(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil))
		require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
		internalState := recorder.calls[len(recorder.calls)-1].state
		require.NotEmpty(t, internalState)
		internalStates = append(internalStates, internalState)
		cookies = append(cookies, bindingCookieFrom(t, rec, internalState))
	}

	require.Len(t, internalStates, 2)
	require.Len(t, state.pendingAuths, 2, "two independent exempt flows must leave exactly two pending records")
	require.NotEqual(t, internalStates[0], internalStates[1], "separate authorization transactions must have independent upstream states")
	require.NotEqual(t, cookies[0].Value, cookies[1].Value, "separate transactions must have independent browser-binding secrets")
	first := state.pendingAuths[internalStates[0]]
	second := state.pendingAuths[internalStates[1]]
	require.NotNil(t, first)
	require.NotNil(t, second)
	require.NotEqual(t, first.UpstreamPKCEVerifier, second.UpstreamPKCEVerifier,
		"separate transactions must have independent upstream PKCE verifiers")
	require.NotEqual(t, first.UpstreamNonce, second.UpstreamNonce,
		"separate transactions must have independent upstream OIDC nonces")
	require.Equal(t, "downstream-one", first.State)
	require.Equal(t, "downstream-two", second.State)
	require.Len(t, recorder.calls, 2)
	require.Equal(t, crypto.ComputePKCEChallenge(first.UpstreamPKCEVerifier), recorder.calls[0].challenge,
		"the first upstream challenge must be derived from its stored verifier")
	require.Equal(t, crypto.ComputePKCEChallenge(second.UpstreamPKCEVerifier), recorder.calls[1].challenge,
		"the second upstream challenge must be derived from its stored verifier")
	require.Equal(t, hashBrowserBindingSecret(cookies[0].Value), first.BrowserBindingHash)
	require.Equal(t, hashBrowserBindingSecret(cookies[1].Value), second.BrowserBindingHash)
}

func TestAuthorizeHandler_ConsentPromptOverridesExemptionAndRememberedApproval(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		prompts []string
	}{
		{name: "single prompt parameter", prompts: []string{"consent"}},
		{name: "repeated prompt parameters", prompts: []string{"login", "consent"}},
		{name: "space-separated prompt", prompts: []string{"login consent"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, state, upstream, remembered, cookie := setupRememberedHandler(t)
			client := state.clients[testAuthClientID].(*fosite.DefaultClient)
			client.Public = false
			client.Secret = []byte("confidential-client-secret")
			h.consentExemptClientIDSet = map[string]struct{}{testAuthClientID: {}}
			reads := &failingConsentReads{RememberedConsentStorage: remembered}
			h.rememberedStorage = reads

			req := rememberedAuthorizeRequest(cookie, "openid", "https://api.example.com", testAuthRedirectURI)
			params := req.URL.Query()
			params["prompt"] = tt.prompts
			req.URL.RawQuery = params.Encode()
			rec := httptest.NewRecorder()
			h.AuthorizeHandler(rec, req)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), "Allow this app to sign you in?",
				"prompt=consent must require a page even with exemption and a covering approval")
			require.Empty(t, upstream.capturedState, "forced consent must not start upstream authentication")
			require.Equal(t, storage.ConsentStageAwaiting, state.pendingAuths[onlyPendingHandle(t, state)].ConsentStage)
			require.Zero(t, reads.sessionReads, "forced consent must bypass remembered-session reads")
			require.Zero(t, reads.approvalReads, "forced consent must bypass remembered-approval reads")
		})
	}
}

func TestAuthorizeHandler_ConsentExemptionRuntimeEligibilityLossShowsConsent(t *testing.T) {
	t.Parallel()

	h, state, upstream, _ := newConsentExemptHandler(t)
	state.clients[testAuthClientID] = &fosite.DefaultClient{
		ID:            testAuthClientID,
		Public:        true,
		GrantTypes:    fosite.Arguments{oauthproto.GrantTypeAuthorizationCode},
		ResponseTypes: fosite.Arguments{oauthproto.ResponseTypeCode},
		RedirectURIs:  []string{testAuthRedirectURI},
		Audience:      []string{"https://api.example.com"},
	}
	params := consentExemptAuthorizeParams(testAuthRedirectURI)
	rec := httptest.NewRecorder()
	h.AuthorizeHandler(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Allow this app to sign you in?",
		"an allowlisted client that becomes public at runtime must fall back to ordinary consent")
	require.Empty(t, upstream.capturedState, "runtime eligibility loss must not silently preauthorize the request")
	pending := state.pendingAuths[onlyPendingHandle(t, state)]
	require.Equal(t, storage.ConsentStageAwaiting, pending.ConsentStage)
}

func TestAuthorizeHandler_ConsentExemptionRuntimeEligibilityLossUsesRememberedApproval(t *testing.T) {
	t.Parallel()

	h, state, upstream, _, cookie := setupRememberedHandler(t)
	h.consentExemptClientIDSet = map[string]struct{}{testAuthClientID: {}}
	// The persisted client is public now, so the runtime exemption check must
	// defer to the existing remembered-approval decision.
	req := rememberedAuthorizeRequest(cookie, "openid", "https://api.example.com", testAuthRedirectURI)
	rec := httptest.NewRecorder()
	h.AuthorizeHandler(rec, req)

	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.NotEmpty(t, upstream.capturedState)
	require.Len(t, state.pendingAuths, 1, "an exempt flow must leave exactly one pending record")
	pending := state.pendingAuths[upstream.capturedState]
	require.NotNil(t, pending)
	require.Equal(t, storage.ConsentStageApproved, pending.ConsentStage)
	require.Equal(t, "user", pending.ExpectedUserID,
		"remembered approval independently supplies its user binding after exemption eligibility is lost")
	require.NotEmpty(t, pending.ConsentSessionDigest)
}

func TestAuthorizeHandler_ValidLoopbackRewriteIsNotConsentExempt(t *testing.T) {
	t.Parallel()

	h, state, upstream := handlerTestSetup(t)
	client, err := registration.New(registration.Config{
		ID:                      "allowlisted-loopback-client",
		RedirectURIs:            []string{"http://localhost/callback"},
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodNone,
		Audience:                []string{"https://api.example.com"},
	})
	require.NoError(t, err)
	state.clients[client.GetID()] = client
	h.consentExemptClientIDSet = map[string]struct{}{client.GetID(): {}}
	params := consentExemptAuthorizeParams("http://localhost:54321/callback")
	params.Set("client_id", client.GetID())
	rec := httptest.NewRecorder()
	h.AuthorizeHandler(rec, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Allow this app to sign you in?",
		"a successfully rewritten loopback redirect must still use the ordinary consent page")
	require.Empty(t, upstream.capturedState, "a valid loopback rewrite must not take the exemption path")
	pending := state.pendingAuths[onlyPendingHandle(t, state)]
	require.Equal(t, "http://localhost:54321/callback", pending.RedirectURI,
		"ordinary consent retains the originally requested dynamic loopback redirect")
	require.Equal(t, storage.ConsentStageAwaiting, pending.ConsentStage)
}

func TestAuthorizeHandler_ConsentExemptionStillRequiresUpstreamBrowserBinding(t *testing.T) {
	t.Parallel()

	h, state, upstream, _ := newConsentExemptHandler(t)
	params := consentExemptAuthorizeParams(testAuthRedirectURI)
	started := httptest.NewRecorder()
	h.AuthorizeHandler(started, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil))
	require.Equal(t, http.StatusSeeOther, started.Code)
	pending := state.pendingAuths[upstream.capturedState]
	require.NotNil(t, pending)

	callback := httptest.NewRecorder()
	h.CallbackHandler(callback, newCallbackRequest("code=upstream-code&state="+upstream.capturedState))
	require.Equal(t, http.StatusBadRequest, callback.Code,
		"an exempt flow callback without the initiating browser's binding cookie must be rejected")
	require.Empty(t, state.upstreamTokens, "a rejected unbound callback must not attach upstream credentials")
	require.NotNil(t, state.pendingAuths[upstream.capturedState],
		"a foreign browser's rejected callback must not consume the pending flow")
}
