// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

func rememberedAuthorizeRequest(cookie *http.Cookie, scopes, resource, redirect string) *http.Request {
	params := url.Values{
		"client_id": {testAuthClientID}, "redirect_uri": {redirect}, "response_type": {"code"},
		"state": {"browser-state"}, "code_challenge": {"challenge123"}, "code_challenge_method": {"S256"},
		"scope": {scopes}, "resource": {resource},
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return req
}

func setupRememberedHandler(t *testing.T, opts ...baseTestSetupOption) (*Handler, *testStorageState, *mockIDPProvider, *storage.MemoryStorage, *http.Cookie) {
	t.Helper()
	h, state, upstream := handlerTestSetup(t, opts...)
	h.config.AuthorizationEndpointBaseURL = "https://auth.example.com"
	mem := storage.NewMemoryStorage()
	t.Cleanup(func() { require.NoError(t, mem.Close()) })
	h.rememberedStorage = mem
	ctx := context.Background()
	require.NoError(t, mem.StoreConsentSession(ctx, consentDigest("session-one"), &storage.ConsentSession{
		UserID: "user", ProviderID: "test-upstream", ProviderSubject: "user-123", ExpiresAt: time.Now().Add(time.Hour),
	}))
	require.NoError(t, mem.StoreClientApproval(ctx, "user", testAuthClientID, &storage.ClientApproval{
		RedirectURI: testAuthRedirectURI, Scopes: []string{"openid", "profile"}, Resource: "https://api.example.com", ExpiresAt: time.Now().Add(time.Hour),
	}))
	return h, state, upstream, mem, &http.Cookie{Name: h.consentCookieName(), Value: "session-one"}
}

func TestForeignScopedConsentCookieIsNotReused(t *testing.T) {
	t.Parallel()
	h, _, _, _, cookie := setupRememberedHandler(t)
	foreign, _, _ := handlerTestSetup(t)
	foreign.config.AccessTokenIssuer = "https://foreign-issuer.example.com"
	foreign.config.AuthorizationEndpointBaseURL = "https://auth.example.com/foreign"
	cookie.Name = foreign.consentCookieName()
	require.NotEqual(t, h.consentCookieName(), cookie.Name)
	w := httptest.NewRecorder()
	h.AuthorizeHandler(w, rememberedAuthorizeRequest(cookie, "openid", "https://api.example.com", testAuthRedirectURI))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "Allow this app to sign you in?")
}

func TestRememberedApprovalCoverage(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, scope, resource, redirect string
		adjust                          func(*testing.T, *storage.MemoryStorage)
		skip                            bool
	}{
		{name: "covers subset", scope: "openid", resource: "https://api.example.com", redirect: testAuthRedirectURI, skip: true},
		{name: "broader scope", scope: "openid email", resource: "https://api.example.com", redirect: testAuthRedirectURI},
		{name: "changed resource", scope: "openid", resource: "https://other.example.com", redirect: testAuthRedirectURI},
		{name: "changed redirect", scope: "openid", resource: "https://api.example.com", redirect: "http://localhost:8081/callback"},
		{name: "expired approval", scope: "openid", resource: "https://api.example.com", redirect: testAuthRedirectURI,
			adjust: func(t *testing.T, s *storage.MemoryStorage) {
				t.Helper()
				require.NoError(t, s.StoreClientApproval(context.Background(), "user", testAuthClientID, &storage.ClientApproval{ExpiresAt: time.Now().Add(5 * time.Millisecond)}))
				time.Sleep(15 * time.Millisecond)
			}},
		{name: "expired session", scope: "openid", resource: "https://api.example.com", redirect: testAuthRedirectURI,
			adjust: func(t *testing.T, s *storage.MemoryStorage) {
				t.Helper()
				require.NoError(t, s.StoreConsentSession(context.Background(), consentDigest("session-one"), &storage.ConsentSession{ExpiresAt: time.Now().Add(5 * time.Millisecond)}))
				time.Sleep(15 * time.Millisecond)
			}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, state, upstream, s, cookie := setupRememberedHandler(t)
			if tt.name == "changed redirect" {
				state.clients[testAuthClientID].(*fosite.DefaultClient).RedirectURIs = append(state.clients[testAuthClientID].GetRedirectURIs(), tt.redirect)
			}
			if tt.name == "changed resource" {
				h.config.AllowedAudiences = append(h.config.AllowedAudiences, tt.resource)
			}
			if tt.adjust != nil {
				tt.adjust(t, s)
			}
			w := httptest.NewRecorder()
			h.AuthorizeHandler(w, rememberedAuthorizeRequest(cookie, tt.scope, tt.resource, tt.redirect))
			if tt.skip {
				require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())
				require.NotEmpty(t, upstream.capturedState)
			} else {
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				require.Contains(t, w.Body.String(), "Allow this app to sign you in?")
				require.Empty(t, upstream.capturedState)
			}
		})
	}
}

func TestMalformedRememberedConsentShowsPage(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		adjust func(*testing.T, *Handler, *storage.MemoryStorage)
	}{
		{name: "session missing user ID", adjust: func(t *testing.T, _ *Handler, s *storage.MemoryStorage) {
			t.Helper()
			require.NoError(t, s.StoreConsentSession(context.Background(), consentDigest("session-one"), &storage.ConsentSession{
				ProviderID: "test-upstream", ProviderSubject: "user-123", ExpiresAt: time.Now().Add(time.Hour),
			}))
		}},
		{name: "session missing provider subject", adjust: func(t *testing.T, _ *Handler, s *storage.MemoryStorage) {
			t.Helper()
			require.NoError(t, s.StoreConsentSession(context.Background(), consentDigest("session-one"), &storage.ConsentSession{
				UserID: "user", ProviderID: "test-upstream", ExpiresAt: time.Now().Add(time.Hour),
			}))
		}},
		{name: "session missing provider ID", adjust: func(t *testing.T, _ *Handler, s *storage.MemoryStorage) {
			t.Helper()
			require.NoError(t, s.StoreConsentSession(context.Background(), consentDigest("session-one"), &storage.ConsentSession{
				UserID: "user", ProviderSubject: "user-123", ExpiresAt: time.Now().Add(time.Hour),
			}))
		}},
		{name: "session read returns nil", adjust: func(_ *testing.T, h *Handler, s *storage.MemoryStorage) {
			h.rememberedStorage = successfulReadConsent{RememberedConsentStorage: s, overrideSession: true}
		}},
		{name: "approval has zero expiry", adjust: func(_ *testing.T, h *Handler, s *storage.MemoryStorage) {
			h.rememberedStorage = successfulReadConsent{RememberedConsentStorage: s, overrideApproval: true, approval: &storage.ClientApproval{
				RedirectURI: testAuthRedirectURI, Scopes: []string{"openid", "profile"}, Resource: "https://api.example.com",
			}}
		}},
		{name: "approval read returns nil", adjust: func(_ *testing.T, h *Handler, s *storage.MemoryStorage) {
			h.rememberedStorage = successfulReadConsent{RememberedConsentStorage: s, overrideApproval: true}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, state, upstream, s, cookie := setupRememberedHandler(t)
			tt.adjust(t, h, s)
			w := httptest.NewRecorder()
			h.AuthorizeHandler(w, rememberedAuthorizeRequest(cookie, "openid", "https://api.example.com", testAuthRedirectURI))
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), "Allow this app to sign you in?")
			require.Empty(t, upstream.capturedState)
			require.Len(t, state.pendingAuths, 1)
			for _, pending := range state.pendingAuths {
				require.Equal(t, storage.ConsentStageAwaiting, pending.ConsentStage)
				require.Empty(t, pending.ExpectedUserID)
				require.Empty(t, pending.ExpectedProviderSubject)
				require.Empty(t, pending.ConsentSessionDigest)
			}
		})
	}
}

func TestRememberedApprovalPromptConsent(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		prompts []string
		page    bool
	}{
		{name: "no prompt"},
		{name: "consent", prompts: []string{"consent"}, page: true},
		{name: "combined prompt", prompts: []string{"login consent"}, page: true},
		{name: "repeated prompt params", prompts: []string{"login", "consent"}, page: true},
		{name: "unrelated token", prompts: []string{"consented"}},
		{name: "login only", prompts: []string{"login"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, _, upstream, _, cookie := setupRememberedHandler(t)
			req := rememberedAuthorizeRequest(cookie, "openid", "https://api.example.com", testAuthRedirectURI)
			params := req.URL.Query()
			if tt.prompts != nil {
				params["prompt"] = tt.prompts
				req.URL.RawQuery = params.Encode()
			}
			w := httptest.NewRecorder()
			h.AuthorizeHandler(w, req)
			if tt.page {
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				require.Contains(t, w.Body.String(), "Allow this app to sign you in?")
				require.Empty(t, upstream.capturedState)
			} else {
				require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())
				require.NotEmpty(t, upstream.capturedState)
			}
		})
	}
}

func TestRememberedSecondAuthorizationSkipsConsent(t *testing.T) {
	t.Parallel()
	h, state, upstream, _, _ := setupRememberedHandler(t)
	// Start with no remembered session: the first flow must show the page.
	fresh := httptest.NewRecorder()
	h.AuthorizeHandler(fresh, rememberedAuthorizeRequest(nil, "openid", "https://api.example.com", testAuthRedirectURI))
	require.Equal(t, http.StatusOK, fresh.Code, fresh.Body.String())
	require.Contains(t, fresh.Body.String(), "name=\"remember\"")
	_, rest, ok := strings.Cut(fresh.Body.String(), `name="handle" value="`)
	require.True(t, ok)
	handle, _, ok := strings.Cut(rest, `"`)
	require.True(t, ok)
	form := url.Values{"handle": {handle}, "decision": {"approve"}, "remember": {"1"}}
	post := httptest.NewRequest(http.MethodPost, "/oauth/consent", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Origin", "https://auth.example.com")
	post.AddCookie(bindingCookieFromSecure(t, fresh, handle))
	started := httptest.NewRecorder()
	h.ConsentHandler(started, post)
	require.Equal(t, http.StatusSeeOther, started.Code, started.Body.String())
	callback := httptest.NewRecorder()
	h.CallbackHandler(callback, newCallbackRequest("code=upstream-code&state="+upstream.capturedState, bindingCookieFromSecure(t, started, upstream.capturedState)))
	require.Contains(t, callback.Header().Get("Location"), "code=")
	var sessionCookie *http.Cookie
	for _, c := range callback.Result().Cookies() {
		if c.Name == h.consentCookieName() {
			sessionCookie = c
		}
	}
	require.NotNil(t, sessionCookie)
	require.NotEmpty(t, state.users)
	second := httptest.NewRecorder()
	h.AuthorizeHandler(second, rememberedAuthorizeRequest(sessionCookie, "openid", "https://api.example.com", testAuthRedirectURI))
	require.Equal(t, http.StatusSeeOther, second.Code, second.Body.String())
	require.NotContains(t, second.Body.String(), "Allow this app to sign you in?")
	require.Equal(t, storage.ConsentStageApproved, state.pendingAuths[upstream.capturedState].ConsentStage)
	require.Equal(t, consentDigest(sessionCookie.Value), state.pendingAuths[upstream.capturedState].ConsentSessionDigest)
	completed := httptest.NewRecorder()
	h.CallbackHandler(completed, newCallbackRequest("code=upstream-code&state="+upstream.capturedState,
		bindingCookieFromSecure(t, second, upstream.capturedState)))
	require.Contains(t, completed.Header().Get("Location"), "code=")
	require.NotEmpty(t, state.upstreamTokens)
}

func TestRememberedIdentityMismatchRevokesOriginatingSession(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"subject switch", "account relink", "synthetic"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			h, state, upstream, s, cookie := setupRememberedHandler(t)
			ctx := context.Background()
			require.NoError(t, s.StoreConsentSession(ctx, consentDigest("another-session"), &storage.ConsentSession{
				UserID: "user", ProviderID: "test-upstream", ProviderSubject: "user-123", ExpiresAt: time.Now().Add(time.Hour),
			}))
			w := httptest.NewRecorder()
			h.AuthorizeHandler(w, rememberedAuthorizeRequest(cookie, "openid", "https://api.example.com", testAuthRedirectURI))
			require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())
			pending := state.pendingAuths[upstream.capturedState]
			require.Equal(t, consentDigest(cookie.Value), pending.ConsentSessionDigest)
			switch scenario {
			case "subject switch":
				upstream.exchangeResult.Subject = "other-account"
			case "account relink":
				state.providerIdentities["test-upstream:user-123"] = &storage.ProviderIdentity{UserID: "other-user"}
			case "synthetic":
				upstream.exchangeResult.Synthetic = true
			}
			callbackCookie := bindingCookieFromSecure(t, w, upstream.capturedState)
			req := newCallbackRequest("code=upstream-code&state="+upstream.capturedState, callbackCookie,
				&http.Cookie{Name: h.consentCookieName(), Value: "another-session"})
			result := httptest.NewRecorder()
			h.CallbackHandler(result, req)
			require.Contains(t, result.Header().Get("Location"), "error=access_denied")
			require.Contains(t, result.Header().Get("Location"), "state=browser-state")
			require.Empty(t, state.users, "ResolveUser must not auto-provision the switched identity")
			require.Empty(t, state.upstreamTokens, "no upstream token may be stored")
			_, err := s.GetConsentSession(ctx, consentDigest(cookie.Value))
			require.ErrorIs(t, err, storage.ErrNotFound)
			_, err = s.GetConsentSession(ctx, consentDigest("another-session"))
			require.NoError(t, err)
		})
	}
}

func TestRememberedIdentityMismatchRevocationFailureFailsClosed(t *testing.T) {
	t.Parallel()
	h, state, upstream, s, cookie := setupRememberedHandler(t)
	started := httptest.NewRecorder()
	h.AuthorizeHandler(started, rememberedAuthorizeRequest(cookie, "openid", "https://api.example.com", testAuthRedirectURI))
	require.Equal(t, http.StatusSeeOther, started.Code, started.Body.String())
	require.Equal(t, consentDigest(cookie.Value), state.pendingAuths[upstream.capturedState].ConsentSessionDigest)

	upstream.exchangeResult.Subject = "other-account"
	deleteErr := errors.New("consent session deletion failed")
	failing := &deleteFailureConsent{RememberedConsentStorage: s, err: deleteErr}
	h.rememberedStorage = failing
	result := httptest.NewRecorder()
	h.CallbackHandler(result, newCallbackRequest("code=upstream-code&state="+upstream.capturedState,
		bindingCookieFromSecure(t, started, upstream.capturedState)))

	require.Equal(t, http.StatusSeeOther, result.Code, result.Body.String())
	location, err := url.Parse(result.Header().Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "server_error", location.Query().Get("error"))
	require.Equal(t, "browser-state", location.Query().Get("state"))
	require.NotContains(t, location.Query(), "code")
	require.Empty(t, state.authCodeSessions)
	require.Empty(t, state.users, "ResolveUser must not auto-provision the switched identity")
	require.Empty(t, state.upstreamTokens, "no upstream token may be stored")
	require.Equal(t, []string{consentDigest(cookie.Value)}, failing.deletedDigests)
	_, err = s.GetConsentSession(context.Background(), consentDigest(cookie.Value))
	require.NoError(t, err, "failed revocation must leave the originating session intact")
}

type deleteFailureConsent struct {
	storage.RememberedConsentStorage
	err            error
	deletedDigests []string
}

func (f *deleteFailureConsent) DeleteConsentSession(_ context.Context, digest string) error {
	f.deletedDigests = append(f.deletedDigests, digest)
	return f.err
}

func bindingCookieFromSecure(t *testing.T, w *httptest.ResponseRecorder, state string) *http.Cookie {
	t.Helper()
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == browserBindingCookieName(state, true) {
			return cookie
		}
	}
	t.Fatal("browser binding cookie missing")
	return nil
}

type successfulReadConsent struct {
	storage.RememberedConsentStorage
	overrideSession, overrideApproval bool
	approval                          *storage.ClientApproval
}

func (r successfulReadConsent) GetConsentSession(ctx context.Context, digest string) (*storage.ConsentSession, error) {
	if r.overrideSession {
		return nil, nil
	}
	return r.RememberedConsentStorage.GetConsentSession(ctx, digest)
}

func (r successfulReadConsent) GetClientApproval(ctx context.Context, userID, clientID string) (*storage.ClientApproval, error) {
	if r.overrideApproval {
		return r.approval, nil
	}
	return r.RememberedConsentStorage.GetClientApproval(ctx, userID, clientID)
}

type readFailureConsent struct {
	storage.RememberedConsentStorage
	failApproval bool
}

func (r readFailureConsent) GetConsentSession(ctx context.Context, digest string) (*storage.ConsentSession, error) {
	if !r.failApproval {
		return nil, errors.New("storage offline")
	}
	return r.RememberedConsentStorage.GetConsentSession(ctx, digest)
}
func (readFailureConsent) GetClientApproval(context.Context, string, string) (*storage.ClientApproval, error) {
	return nil, errors.New("storage offline")
}

func TestRememberedIdentityLookupFailureHasNoSideEffects(t *testing.T) {
	t.Parallel()
	h, state, upstream, s, cookie := setupRememberedHandler(t, func(c *baseTestSetupConfig) { c.getProviderIdentityErr = errors.New("storage offline") })
	w := httptest.NewRecorder()
	h.AuthorizeHandler(w, rememberedAuthorizeRequest(cookie, "openid", "https://api.example.com", testAuthRedirectURI))
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())
	result := httptest.NewRecorder()
	h.CallbackHandler(result, newCallbackRequest("code=upstream-code&state="+upstream.capturedState,
		bindingCookieFromSecure(t, w, upstream.capturedState)))
	require.Contains(t, result.Header().Get("Location"), "error=server_error")
	require.Empty(t, state.users)
	require.Empty(t, state.upstreamTokens)
	_, err := s.GetConsentSession(context.Background(), consentDigest(cookie.Value))
	require.NoError(t, err, "transient lookup failures must not revoke a valid session")
}

func TestRememberedStorageErrorFailsClosed(t *testing.T) {
	t.Parallel()
	for _, failApproval := range []bool{false, true} {
		h, _, upstream, s, cookie := setupRememberedHandler(t)
		h.rememberedStorage = readFailureConsent{RememberedConsentStorage: s, failApproval: failApproval}
		w := httptest.NewRecorder()
		h.AuthorizeHandler(w, rememberedAuthorizeRequest(cookie, "openid", "https://api.example.com", testAuthRedirectURI))
		require.Contains(t, w.Header().Get("Location"), "error=server_error")
		require.Empty(t, upstream.capturedState)
	}
}

func TestRememberedApprovalLoopbackRegistration(t *testing.T) {
	t.Parallel()
	h, state, _, s, _ := setupRememberedHandler(t)
	c := state.clients[testAuthClientID]
	c.(*fosite.DefaultClient).RedirectURIs = []string{"http://localhost/callback"}
	require.NoError(t, s.StoreClientApproval(context.Background(), "user", testAuthClientID, &storage.ClientApproval{
		RedirectURI: "http://localhost:8081/callback", Scopes: []string{"openid"}, Resource: "https://api.example.com", ExpiresAt: time.Now().Add(time.Hour),
	}))
	session, digest, err := h.coveringConsent(context.Background(), rememberedAuthorizeRequest(&http.Cookie{Name: h.consentCookieName(), Value: "session-one"}, "openid", "https://api.example.com", "http://localhost:8082/callback"), c, "http://localhost:8082/callback", []string{"openid"}, "https://api.example.com")
	require.NoError(t, err)
	require.NotNil(t, session)
	require.NotEmpty(t, digest)
}

func TestApprovalCovers(t *testing.T) {
	t.Parallel()
	client := &fosite.DefaultClient{ID: testAuthClientID, Public: true, RedirectURIs: []string{"http://localhost/callback", testAuthRedirectURI}}
	approval := &storage.ClientApproval{
		RedirectURI: "http://localhost:8081/callback", Scopes: []string{"openid", "profile"}, Resource: "https://api.example.com",
	}
	for _, tt := range []struct {
		name     string
		redirect string
		scopes   []string
		resource string
		want     bool
	}{
		{"same envelope", "http://localhost:8081/callback", []string{"openid"}, "https://api.example.com", true},
		{"no scopes", "http://localhost:8081/callback", nil, "https://api.example.com", true},
		{"loopback port differs", "http://localhost:8082/callback", []string{"openid", "profile"}, "https://api.example.com", true},
		{"broader scope", "http://localhost:8081/callback", []string{"openid", "email"}, "https://api.example.com", false},
		{"other resource", "http://localhost:8081/callback", []string{"openid"}, "https://other.example.com", false},
		{"unregistered redirect", "http://localhost:8081/other", []string{"openid"}, "https://api.example.com", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, approvalCovers(client, approval, tt.redirect, tt.scopes, tt.resource))
		})
	}
}
