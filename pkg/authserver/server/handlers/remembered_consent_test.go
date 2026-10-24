// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/transport/proxy/transparent"
)

func TestConsentCookieName(t *testing.T) {
	t.Parallel()
	h, _, _ := handlerTestSetup(t)
	h.config.AccessTokenIssuer = "https://issuer.test"
	h.config.AuthorizationEndpointBaseURL = "https://browser.test/a"
	const vector = "__Host-thv_consent_32e4fd9ce0adcf959a0cfcae38abeb7ca60a87c47def79fb2d7c0315849a7afa"
	replica, _, _ := handlerTestSetup(t)
	replica.config.AccessTokenIssuer = h.config.AccessTokenIssuer
	replica.config.AuthorizationEndpointBaseURL = h.config.AuthorizationEndpointBaseURL
	require.Equal(t, vector, h.consentCookieName())
	require.Equal(t, vector, replica.consentCookieName())

	for _, tt := range []struct{ issuer, base string }{
		{"https://issuer.test", "https://browser.test/b"},
		{"https://issuer.test", "https://browser.test:8443/a"},
		{"https://other.test", "https://browser.test/a"},
		// Without length prefixes these two pairs both encode to "abc".
		{"ab", "c"}, {"a", "bc"},
	} {
		h.config.AccessTokenIssuer = tt.issuer
		h.config.AuthorizationEndpointBaseURL = tt.base
		require.NotEqual(t, vector, h.consentCookieName())
	}
	h.config.AccessTokenIssuer, h.config.AuthorizationEndpointBaseURL = "ab", "c"
	first := h.consentCookieName()
	h.config.AccessTokenIssuer, h.config.AuthorizationEndpointBaseURL = "a", "bc"
	require.NotEqual(t, first, h.consentCookieName())
	h.config.AccessTokenIssuer, h.config.AuthorizationEndpointBaseURL = "https://issuer.test", ""
	fallback := h.consentCookieName()
	h.config.AuthorizationEndpointBaseURL = h.config.AccessTokenIssuer
	require.Equal(t, fallback, h.consentCookieName())
}

func TestRememberedConsentCookieNotForwardedToBackend(t *testing.T) {
	t.Parallel()
	h, _, _ := handlerTestSetup(t)
	s := storage.NewMemoryStorage()
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	h.rememberedStorage = s
	pending := &storage.PendingAuthorization{ClientID: testAuthClientID, RedirectURI: testAuthRedirectURI,
		Scopes: []string{"openid"}, Resource: "https://api.example.com", UpstreamProviderName: "test-upstream",
		PKCEChallenge: strings.Repeat("A", 43), PKCEMethod: "S256", RememberConsent: true,
		FirstProviderSubject: "sub", ConsentStage: storage.ConsentStageApproved}
	w := httptest.NewRecorder()
	require.NoError(t, h.writeAuthorizationResponse(context.Background(), w, pending, "sid", "user", "", ""))
	var consentCookie *http.Cookie
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == h.consentCookieName() {
			consentCookie = cookie
		}
	}
	require.NotNil(t, consentCookie, "authorization response must set remembered-consent cookie")

	backendCookies := make(chan string, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCookies <- r.Header.Get("Cookie")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)
	proxy := transparent.NewTransparentProxyWithOptions(
		"127.0.0.1", 0, backend.URL, nil, nil, nil, false, false, "streamable-http",
		nil, nil, "", false, nil, transparent.WithStripConsentCookie(),
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, proxy.Stop(ctx))
	})
	require.NoError(t, proxy.Start(context.Background()))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+proxy.ListenerAddr()+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"tools/list"}`))
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: "backend", Value: "one"})
	req.AddCookie(consentCookie)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "backend=one", <-backendCookies)
}

func TestRememberCapabilityResolvedFromHTTPSBackend(t *testing.T) {
	t.Parallel()
	provider, cfg, _, _ := baseTestSetup(t)
	s := storage.NewMemoryStorage()
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	upstreams := []NamedUpstream{{Name: "first", Provider: &mockIDPProvider{}}}
	h, err := NewHandler(provider, cfg, s, upstreams)
	require.NoError(t, err)
	require.Nil(t, h.rememberedStorage)
	cfg.AuthorizationEndpointBaseURL = "https://auth.example.com"
	h, err = NewHandler(provider, cfg, s, upstreams)
	require.NoError(t, err)
	require.Same(t, s, h.rememberedStorage)
}

func TestRememberConsentOnCompletedFlow(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		remember   bool
		first      string
		capable    bool
		wantCookie bool
	}{
		{"remember", true, "sub", true, true},
		{"not selected", false, "sub", true, false},
		{"synthetic", true, "", true, false},
		{"insecure or incapable", true, "sub", false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, _, _ := handlerTestSetup(t)
			s := storage.NewMemoryStorage()
			t.Cleanup(func() { require.NoError(t, s.Close()) })
			if tt.capable {
				h.rememberedStorage = s
			}
			pending := &storage.PendingAuthorization{ClientID: testAuthClientID, RedirectURI: testAuthRedirectURI,
				Scopes: []string{"openid"}, Resource: "https://api.example.com", UpstreamProviderName: "test-upstream",
				PKCEChallenge: strings.Repeat("A", 43), PKCEMethod: "S256", RememberConsent: tt.remember, FirstProviderSubject: tt.first, ConsentStage: storage.ConsentStageApproved}
			w := httptest.NewRecorder()
			require.NoError(t, h.writeAuthorizationResponse(context.Background(), w, pending, "sid", "user", "", ""))
			cookie := w.Result().Cookies()
			var consentCookieFound bool
			for _, c := range cookie {
				if c.Name == h.consentCookieName() {
					consentCookieFound = true
					require.True(t, c.Secure)
					require.Empty(t, c.Domain)
					require.Equal(t, http.SameSiteLaxMode, c.SameSite)
					require.True(t, c.HttpOnly)
					require.Equal(t, "/", c.Path)
					require.Equal(t, 7*24*60*60, c.MaxAge)
					require.Equal(t, h.consentCookieName(), c.Name)
					session, err := s.GetConsentSession(context.Background(), consentDigest(c.Value))
					require.NoError(t, err)
					require.Equal(t, "sub", session.ProviderSubject)
					require.WithinDuration(t, time.Now().Add(storage.ConsentSessionTTL), session.ExpiresAt, time.Minute)
				}
			}
			require.Equal(t, tt.wantCookie, consentCookieFound)
			approval, err := s.GetClientApproval(context.Background(), "user", testAuthClientID)
			if tt.wantCookie {
				require.NoError(t, err)
				require.Equal(t, []string{"openid"}, approval.Scopes)
			} else {
				require.ErrorIs(t, err, storage.ErrNotFound)
			}
		})
	}
}

// Errors while persisting a remembered choice must not prevent code issuance.
type failingRememberedStorage struct {
	storage.RememberedConsentStorage
	failSession bool
}

func (f failingRememberedStorage) StoreClientApproval(context.Context, string, string, *storage.ClientApproval) error {
	if !f.failSession {
		return errors.New("unavailable")
	}
	return nil
}
func (failingRememberedStorage) StoreConsentSession(context.Context, string, *storage.ConsentSession) error {
	return errors.New("unavailable")
}

func TestRememberStorageFailureStillIssuesCode(t *testing.T) {
	t.Parallel()
	for _, failSession := range []bool{false, true} {
		h, _, _ := handlerTestSetup(t)
		h.rememberedStorage = failingRememberedStorage{failSession: failSession}
		pending := &storage.PendingAuthorization{ClientID: testAuthClientID, RedirectURI: testAuthRedirectURI,
			Scopes: []string{"openid"}, Resource: "https://api.example.com", UpstreamProviderName: "test-upstream",
			PKCEChallenge: strings.Repeat("A", 43), PKCEMethod: "S256", RememberConsent: true, FirstProviderSubject: "sub", ConsentStage: storage.ConsentStageApproved}
		w := httptest.NewRecorder()
		require.NoError(t, h.writeAuthorizationResponse(context.Background(), w, pending, "sid", "user", "", ""))
		require.Contains(t, w.Header().Get("Location"), "code=")
		for _, cookie := range w.Result().Cookies() {
			require.NotEqual(t, h.consentCookieName(), cookie.Name)
		}
	}
}

func TestRememberCheckboxRequiresCapabilityAndHTTPS(t *testing.T) {
	t.Parallel()
	h, _, _ := handlerTestSetup(t)
	pending := &storage.PendingAuthorization{RedirectURI: testAuthRedirectURI, ClientID: testAuthClientID}
	w := httptest.NewRecorder()
	client, err := h.storage.GetClient(context.Background(), testAuthClientID)
	require.NoError(t, err)
	h.renderConsent(w, "handle", pending, client)
	require.NotContains(t, w.Body.String(), "name=\"remember\"")
	s := storage.NewMemoryStorage()
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	h.rememberedStorage = s
	h.config.AuthorizationEndpointBaseURL = "https://test-auth-issuer"
	w = httptest.NewRecorder()
	h.renderConsent(w, "handle", pending, client)
	require.True(t, strings.Contains(w.Body.String(), "name=\"remember\""))
}
