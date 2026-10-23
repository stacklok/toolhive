// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ory/fosite"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// startConsentTest starts an authorization against the default (plain http) base URL.
func startConsentTest(t *testing.T, opts ...baseTestSetupOption) (
	*Handler, *testStorageState, *mockIDPProvider, string, *http.Cookie, *httptest.ResponseRecorder,
) {
	t.Helper()
	return startConsentTestWithBase(t, false, opts...)
}

// startConsentTestWithBase can serve the page from an https base URL, which makes the
// binding cookie a Secure __Host- cookie.
func startConsentTestWithBase(t *testing.T, secure bool, opts ...baseTestSetupOption) (
	*Handler, *testStorageState, *mockIDPProvider, string, *http.Cookie, *httptest.ResponseRecorder,
) {
	t.Helper()
	h, state, upstream := handlerTestSetup(t, opts...)
	if secure {
		h.config.AuthorizationEndpointBaseURL = "https://auth.example.com"
	}
	page := authorizePage(t, h)
	_, rest, ok := strings.Cut(page.Body.String(), `name="handle" value="`)
	require.True(t, ok)
	handle, _, ok := strings.Cut(rest, `"`)
	require.True(t, ok)
	if secure {
		for _, c := range page.Result().Cookies() {
			if c.Name == browserBindingCookieName(handle, true) {
				return h, state, upstream, handle, c, page
			}
		}
		t.Fatal("secure binding cookie not set")
	}
	return h, state, upstream, handle, bindingCookieFrom(t, page, handle), page
}

// authorizePage issues a GET /oauth/authorize for the test client and returns the consent page.
func authorizePage(t *testing.T, h *Handler) *httptest.ResponseRecorder {
	t.Helper()
	params := url.Values{
		"client_id": {testAuthClientID}, "redirect_uri": {testAuthRedirectURI},
		"response_type": {"code"}, "state": {"client-state"},
		"code_challenge": {"challenge123"}, "code_challenge_method": {"S256"},
		"scope": {"openid"},
	}
	page := httptest.NewRecorder()
	h.AuthorizeHandler(page, httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+params.Encode(), nil))
	require.Equal(t, http.StatusOK, page.Code)
	return page
}

// renderConsentBody renders the consent page for pending and returns its body.
func renderConsentBody(t *testing.T, h *Handler, handle string, pending *storage.PendingAuthorization) string {
	t.Helper()
	client, err := h.storage.GetClient(context.Background(), testAuthClientID)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	h.renderConsent(rec, handle, pending, client)
	return rec.Body.String()
}

func consentRequest(handle, decision, origin string, cookie *http.Cookie) *http.Request {
	form := url.Values{"handle": {handle}, "decision": {decision}}
	req := httptest.NewRequest(http.MethodPost, "/oauth/consent", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return req
}

func TestConsentHandler_RememberSelection(t *testing.T) {
	t.Parallel()
	h, state, upstream, handle, cookie, _ := startConsentTestWithBase(t, true)
	mem := storage.NewMemoryStorage()
	t.Cleanup(func() { require.NoError(t, mem.Close()) })
	h.rememberedStorage = mem
	h.config.AuthorizationEndpointBaseURL = "https://auth.example.com"
	form := url.Values{"handle": {handle}, "decision": {"approve"}, "remember": {"1"}}
	req := httptest.NewRequest(http.MethodPost, "/oauth/consent", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://auth.example.com")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ConsentHandler(rec, req)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.True(t, state.pendingAuths[upstream.capturedState].RememberConsent)
}

func TestConsentHandler_GateAndBrowserBinding(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, origin string
		cookie       func(*http.Cookie) *http.Cookie
		status       int
	}{
		{name: "missing origin", cookie: func(c *http.Cookie) *http.Cookie { return c }, status: http.StatusForbidden},
		{name: "null origin", origin: "null", cookie: func(c *http.Cookie) *http.Cookie { return c }, status: http.StatusForbidden},
		{name: "foreign origin", origin: "http://evil.example", cookie: func(c *http.Cookie) *http.Cookie { return c }, status: http.StatusForbidden},
		{name: "missing cookie", origin: testAuthIssuer, status: http.StatusBadRequest},
		{name: "wrong cookie", origin: testAuthIssuer, cookie: func(c *http.Cookie) *http.Cookie {
			clone := *c
			clone.Value = "wrong"
			return &clone
		}, status: http.StatusBadRequest},
		{name: "approved", origin: testAuthIssuer, cookie: func(c *http.Cookie) *http.Cookie { return c }, status: http.StatusSeeOther},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, state, upstream, handle, cookie, page := startConsentTest(t)
			assert.Empty(t, upstream.capturedState, "no upstream request before approval")
			assert.Empty(t, page.Header().Get("Location"))
			assert.NotContains(t, page.Header().Get("Content-Security-Policy"), "form-action 'self'")
			assert.Contains(t, page.Header().Get("Content-Security-Policy"), "base-uri 'none'")
			assert.Empty(t, page.Header().Get("Cross-Origin-Opener-Policy"))
			assert.Equal(t, "same-origin", page.Header().Get("Referrer-Policy"))
			assert.Contains(t, page.Body.String(), "localhost:8080/callback")
			var presented *http.Cookie
			if tc.cookie != nil {
				presented = tc.cookie(cookie)
			}
			rec := httptest.NewRecorder()
			h.ConsentHandler(rec, consentRequest(handle, "approve", tc.origin, presented))
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			if tc.status == http.StatusSeeOther {
				assert.NotEmpty(t, upstream.capturedState)
				assert.Equal(t, storage.ConsentStageApproved, state.pendingAuths[upstream.capturedState].ConsentStage)
				assert.NotContains(t, state.pendingAuths, handle)
			} else {
				assert.Empty(t, upstream.capturedState)
				assert.Contains(t, state.pendingAuths, handle)
			}
		})
	}
}

func TestConsentHandler_DenyAndReplay(t *testing.T) {
	t.Parallel()
	h, state, upstream, handle, cookie, _ := startConsentTest(t)
	rec := httptest.NewRecorder()
	h.ConsentHandler(rec, consentRequest(handle, "deny", testAuthIssuer, cookie))
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "error=access_denied")
	assert.Contains(t, rec.Header().Get("Location"), "state=client-state")
	assert.Empty(t, upstream.capturedState)
	assert.NotContains(t, state.pendingAuths, handle)
	replay := httptest.NewRecorder()
	h.ConsentHandler(replay, consentRequest(handle, "approve", testAuthIssuer, cookie))
	assert.Equal(t, http.StatusBadRequest, replay.Code)
	assert.Empty(t, upstream.capturedState)
}

func TestConsentHandler_DeleteFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{name: "backend failure", err: errors.New("storage unavailable"), status: http.StatusInternalServerError},
		{name: "lost race to a concurrent consume", err: storage.ErrNotFound, status: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, _, upstream, handle, cookie, _ := startConsentTest(t, withDeletePendingError(tc.err))
			rec := httptest.NewRecorder()
			h.ConsentHandler(rec, consentRequest(handle, "approve", testAuthIssuer, cookie))
			assert.Equal(t, tc.status, rec.Code)
			assert.Empty(t, rec.Header().Get("Location"))
			assert.Empty(t, upstream.capturedState)
		})
	}
}

func TestConsentHandler_AwaitingHandleCannotBeCallbackState(t *testing.T) {
	t.Parallel()
	h, state, upstream, handle, cookie, _ := startConsentTest(t)
	rec := httptest.NewRecorder()
	h.CallbackHandler(rec, newCallbackRequest("code=upstream-code&state="+handle, cookie))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, state.pendingAuths, handle)
	assert.Empty(t, upstream.capturedState)
}

func TestConsentHandler_ChangedUpstreamFailsClosed(t *testing.T) {
	t.Parallel()
	h, state, _, handle, cookie, _ := startConsentTest(t)
	h.upstreams = nil // Configuration changed while a persisted consent was awaiting approval.
	rec := httptest.NewRecorder()
	h.ConsentHandler(rec, consentRequest(handle, "approve", testAuthIssuer, cookie))
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "error=server_error")
	assert.Empty(t, state.pendingAuths)
}

func TestConsentPage_DCRClientName(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{"memory", "redis"} {
		for _, name := range []string{`<img src=x onerror=alert(1)>`, ""} {
			t.Run(backend+"/"+name, func(t *testing.T) {
				t.Parallel()
				var s storage.Storage
				if backend == "redis" {
					mr := miniredis.RunT(t)
					client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
					t.Cleanup(func() { require.NoError(t, client.Close()) })
					s = storage.NewRedisStorageWithClient(client, "consent:name:")
				} else {
					mem := storage.NewMemoryStorage()
					t.Cleanup(func() { require.NoError(t, mem.Close()) })
					s = mem
				}
				h := &Handler{storage: s, config: &server.AuthorizationServerConfig{
					Config:          &fosite.Config{AccessTokenIssuer: "https://auth.example.com"},
					ScopesSupported: registration.DefaultScopes,
				}}
				body, err := json.Marshal(oauthproto.DynamicClientRegistrationRequest{
					RedirectURIs: []string{"http://127.0.0.1:8080/callback"}, ClientName: name,
				})
				require.NoError(t, err)
				req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(string(body)))
				req.Header.Set("Content-Type", "application/json")
				registered := httptest.NewRecorder()
				h.RegisterClientHandler(registered, req)
				require.Equal(t, http.StatusCreated, registered.Code, registered.Body.String())
				var response oauthproto.DynamicClientRegistrationResponse
				require.NoError(t, json.Unmarshal(registered.Body.Bytes(), &response))
				client, err := s.GetClient(context.Background(), response.ClientID)
				require.NoError(t, err)
				assert.True(t, registration.DCRIssued(client))
				assert.Equal(t, name, registration.ClientName(client))
				assert.Equal(t, "none", client.(fosite.OpenIDConnectClient).GetTokenEndpointAuthMethod())
				page := httptest.NewRecorder()
				h.renderConsent(page, "handle", &storage.PendingAuthorization{
					RedirectURI: "http://127.0.0.1:8080/callback", ClientID: response.ClientID,
				}, client)
				assert.Contains(t, page.Body.String(), "Name given by the app (not verified):</dt><dd>")
				if name == "" {
					assert.Contains(t, page.Body.String(), "<dd>No name given</dd>")
				} else {
					assert.Contains(t, page.Body.String(), "&lt;img src=x onerror=alert(1)&gt;")
					assert.NotContains(t, page.Body.String(), name)
				}
			})
		}
	}
}

func TestConsentPage_BrowserFacingPrefixAndOrigin(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		base, origin, action string
	}{
		{"https://AUTH.Example.COM:443/proxy", "https://auth.example.com", "https://AUTH.Example.COM:443/proxy/oauth/consent"},
		{"http://LOCALHOST:80/auth", "http://localhost", "http://LOCALHOST:80/auth/oauth/consent"},
		{"https://AUTH.Example.COM:8443/proxy", "https://auth.example.com:8443", "https://AUTH.Example.COM:8443/proxy/oauth/consent"},
		{"https://[0:0:0:0:0:0:0:1]/proxy", "https://[::1]", "https://[0:0:0:0:0:0:0:1]/proxy/oauth/consent"},
		{"https://[0:0:0:0:0:0:0:1]:0443/proxy", "https://[::1]", "https://[0:0:0:0:0:0:0:1]:0443/proxy/oauth/consent"},
		{"https://[::1]:08443/proxy", "https://[::1]:8443", "https://[::1]:08443/proxy/oauth/consent"},
		{"https://AUTH.Example.COM:0443/proxy", "https://auth.example.com", "https://AUTH.Example.COM:0443/proxy/oauth/consent"},
		{"http://LOCALHOST:00080/auth", "http://localhost", "http://LOCALHOST:00080/auth/oauth/consent"},
		{"https://AUTH.Example.COM:08443/proxy", "https://auth.example.com:8443", "https://AUTH.Example.COM:08443/proxy/oauth/consent"},
		{"https://[::ffff:127.0.0.1]/proxy", "https://[::ffff:7f00:1]", "https://[::ffff:127.0.0.1]/proxy/oauth/consent"},
		{"https://bücher.example/proxy", "https://xn--bcher-kva.example", "https://b%c3%bccher.example/proxy/oauth/consent"},
	} {
		t.Run(tc.base, func(t *testing.T) {
			t.Parallel()
			h, _, _, handle, cookie, _ := startConsentTest(t)
			h.config.AuthorizationEndpointBaseURL = tc.base
			cookie.Name = browserBindingCookieName(handle, strings.HasPrefix(tc.base, "https://"))
			client, err := h.storage.GetClient(context.Background(), testAuthClientID)
			require.NoError(t, err)
			page := httptest.NewRecorder()
			h.renderConsent(page, handle, &storage.PendingAuthorization{RedirectURI: testAuthRedirectURI}, client)
			assert.Contains(t, page.Body.String(), `action="`+tc.action+`"`)
			foreign := httptest.NewRecorder()
			h.ConsentHandler(foreign, consentRequest(handle, "deny", "https://foreign.example", cookie))
			assert.Equal(t, http.StatusForbidden, foreign.Code)
			rec := httptest.NewRecorder()
			h.ConsentHandler(rec, consentRequest(handle, "deny", tc.origin, cookie))
			assert.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
		})
	}
}

func TestConsentHandler_InvalidConfiguredOrigin(t *testing.T) {
	t.Parallel()
	for _, base := range []string{"https://auth.example:65536", "https://auth.example:abc", "https://[not:ipv6]"} {
		t.Run(base, func(t *testing.T) {
			t.Parallel()
			h, _, _, handle, cookie, _ := startConsentTest(t)
			h.config.AuthorizationEndpointBaseURL = base
			rec := httptest.NewRecorder()
			h.ConsentHandler(rec, consentRequest(handle, "deny", base, cookie))
			assert.Equal(t, http.StatusForbidden, rec.Code)
		})
	}
}

func TestConsentPage_EscapesUntrustedRedirect(t *testing.T) {
	t.Parallel()
	h, _, _, handle, _, _ := startConsentTest(t)
	pending := &storage.PendingAuthorization{RedirectURI: "https://example.com/<script>alert(1)</script>", ClientID: testAuthClientID, UpstreamProviderName: "test-upstream"}
	client, err := h.storage.GetClient(httptest.NewRequest(http.MethodGet, "/", nil).Context(), testAuthClientID)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	h.renderConsent(rec, handle, pending, client)
	assert.NotContains(t, rec.Body.String(), "<script>alert(1)</script>")
	assert.Contains(t, rec.Body.String(), "&lt;script&gt;")
}

func TestClassifyRedirect(t *testing.T) {
	t.Parallel()
	const loopback = "An app running on this computer wants to receive your sign-in. Only continue if you just started that app."
	for _, tc := range []struct {
		name, uri, destination, summary string
	}{
		{"https host", "https://app.example.com/cb", "app.example.com",
			"An app wants to receive your sign-in at app.example.com. Only continue if you just started this from that app or site."},
		{"loopback ip with port", "http://127.0.0.1:8080/callback", "this computer", loopback},
		{"localhost", "http://localhost:3000/cb", "this computer", loopback},
		{"ipv6 loopback", "http://[::1]:9000/cb", "this computer", loopback},
		{"custom scheme with authority", "cursor://anysphere.cursor-retrieval/x", "an app that opens cursor:// links",
			"An app on this device that opens cursor:// links wants to receive your sign-in. Only continue if you just started that app."},
		{"custom scheme without authority", "com.example.app:/cb", "an app that opens com.example.app:// links",
			"An app on this device that opens com.example.app:// links wants to receive your sign-in. Only continue if you just started that app."},
		{"idn host", "https://bücher.example/cb", "bücher.example (xn--bcher-kva.example)",
			"An app wants to receive your sign-in at bücher.example (xn--bcher-kva.example). Only continue if you just started this from that app or site."},
		{"unparsable", "https://exa mple.com/%zz", "https://exa mple.com/%zz",
			"An app wants to receive your sign-in at the address shown below. Only continue if you just started this from that app or site."},
		{"empty host", "https:///cb", "https:///cb",
			"An app wants to receive your sign-in at the address shown below. Only continue if you just started this from that app or site."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			destination, summary := classifyRedirect(tc.uri)
			assert.Equal(t, tc.destination, destination)
			assert.Equal(t, tc.summary, summary)
		})
	}
}

func TestConsentPage_LayoutAndNameTruncation(t *testing.T) {
	t.Parallel()
	h, _, _, handle, _, _ := startConsentTest(t)
	client, err := h.storage.GetClient(context.Background(), testAuthClientID)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	h.renderConsent(rec, handle, &storage.PendingAuthorization{
		RedirectURI: "cursor://anysphere.cursor-retrieval/x", ClientID: testAuthClientID, UpstreamProviderName: "okta",
	}, client)
	body := rec.Body.String()
	assert.Contains(t, body, "Allow this app to sign you in?")
	assert.Contains(t, body, "You will sign in with okta next.")
	assert.Contains(t, body, "Sends your sign-in to:</dt><dd><strong>an app that opens cursor:// links</strong>")
	assert.Contains(t, body, "<details><summary>Technical details</summary>")
	for _, field := range []string{"Full redirect URI", "Client ID", "Requested access", "Resource", "Identity provider (internal name)"} {
		assert.Contains(t, body, field)
	}

	named := namedConsentClient{DefaultClient: &fosite.DefaultClient{ID: "c"}, name: strings.Repeat("é", 200)}
	rec = httptest.NewRecorder()
	h.renderConsent(rec, handle, &storage.PendingAuthorization{RedirectURI: "https://app.example.com/cb"}, named)
	assert.Contains(t, rec.Body.String(), "<dd>"+strings.Repeat("é", maxConsentTextRunes)+"…</dd>")
	assert.NotContains(t, rec.Body.String(), strings.Repeat("é", maxConsentTextRunes+1))
}

func TestConsentPage_ClientNameStripsInvisibleRunes(t *testing.T) {
	t.Parallel()
	h, _, _, handle, _, _ := startConsentTest(t)
	for _, tc := range []struct {
		name, in, want string
	}{
		{name: "bidi override", in: "Evil\u202egnp.exe", want: "Evilgnp.exe"},
		{name: "zero width space", in: "Ac\u200bme", want: "Acme"},
		{name: "newline and tab", in: "Ac\nme\tApp", want: "AcmeApp"},
		{name: "only format characters", in: "\u202e\u200b\u2066", want: "No name given"},
		{name: "accented and CJK", in: "Café 工具", want: "Café 工具"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			named := namedConsentClient{DefaultClient: &fosite.DefaultClient{ID: "c"}, name: tc.in}
			rec := httptest.NewRecorder()
			h.renderConsent(rec, handle, &storage.PendingAuthorization{RedirectURI: "https://app.example.com/cb"}, named)
			assert.Contains(t, rec.Body.String(), "<dd>"+tc.want+"</dd>")
		})
	}
}

type namedConsentClient struct {
	*fosite.DefaultClient
	name string
}

func (c namedConsentClient) GetClientName() string { return c.name }

func TestExpectedBrowserOrigin(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, base, want string
		wantErr          bool
	}{
		{name: "https default port omitted", base: "https://auth.example.com:443/p", want: "https://auth.example.com"},
		{name: "http default port omitted", base: "http://auth.example.com:80", want: "http://auth.example.com"},
		{name: "non-default port kept", base: "https://auth.example.com:8443", want: "https://auth.example.com:8443"},
		{name: "http on 443 keeps port", base: "http://auth.example.com:443", want: "http://auth.example.com:443"},
		{name: "https on 80 keeps port", base: "https://auth.example.com:80", want: "https://auth.example.com:80"},
		{name: "leading zero port", base: "https://auth.example.com:08443", want: "https://auth.example.com:8443"},
		{name: "uppercase host lowercased", base: "https://AUTH.Example.COM", want: "https://auth.example.com"},
		{name: "ipv4", base: "http://127.0.0.1:8080", want: "http://127.0.0.1:8080"},
		{name: "ipv6 compressed", base: "https://[0:0:0:0:0:0:0:1]/x", want: "https://[::1]"},
		{name: "ipv6 with port", base: "https://[::1]:8443", want: "https://[::1]:8443"},
		{name: "ipv4-mapped ipv6", base: "https://[::ffff:1.2.3.4]", want: "https://[::ffff:102:304]"},
		{name: "idn to punycode", base: "https://bücher.example", want: "https://xn--bcher-kva.example"},
		{name: "port out of range", base: "https://auth.example.com:65536", wantErr: true},
		{name: "non-numeric port", base: "https://auth.example.com:abc", wantErr: true},
		{name: "non-http scheme", base: "ftp://auth.example.com", wantErr: true},
		{name: "empty host", base: "https:///path", wantErr: true},
		{name: "empty string", base: "", wantErr: true},
		{name: "invalid ipv6", base: "https://[not:ipv6]", wantErr: true},
		{name: "ipv6 zone", base: "https://[fe80::1%25eth0]", wantErr: true},
		{name: "unparsable url", base: "https://exa mple.com/%zz", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := expectedBrowserOrigin(tc.base)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestConsentPage_RememberRowAndCSPHashes(t *testing.T) {
	t.Parallel()
	const label = "Don't ask again for this app on this browser"
	const helper = "Skips this page for 7 days when the same app asks for the same access."
	for _, canRemember := range []bool{true, false} {
		h, _, _, handle, _, _ := startConsentTest(t)
		if canRemember {
			mem := storage.NewMemoryStorage()
			t.Cleanup(func() { require.NoError(t, mem.Close()) })
			h.rememberedStorage = mem
		}
		client, err := h.storage.GetClient(context.Background(), testAuthClientID)
		require.NoError(t, err)
		rec := httptest.NewRecorder()
		h.renderConsent(rec, handle, &storage.PendingAuthorization{RedirectURI: "https://app.example.com/cb"}, client)
		body := rec.Body.String()

		csp := rec.Header().Get("Content-Security-Policy")
		hashes := regexp.MustCompile(`'sha256-([^']+)'`).FindAllStringSubmatch(csp, -1)
		styles := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindAllStringSubmatch(body, -1)
		require.Len(t, hashes, 2)
		require.Len(t, styles, 2)
		for i := range styles {
			sum := sha256.Sum256([]byte(styles[i][1]))
			assert.Equal(t, base64.StdEncoding.EncodeToString(sum[:]), hashes[i][1])
		}

		if !canRemember {
			assert.NotContains(t, body, label)
			assert.NotContains(t, body, helper)
			assert.NotContains(t, body, `name="remember"`)
			continue
		}
		assert.Contains(t, body, label)
		assert.Contains(t, body, helper)
		assert.NotContains(t, body, "checked")
		assert.Contains(t, body, `aria-describedby="remember-help"`)
		assert.Contains(t, body, `id="remember-help"`)
	}
}

func TestFormatDays(t *testing.T) {
	t.Parallel()
	for d, want := range map[time.Duration]string{
		24 * time.Hour: "1 day", 7 * 24 * time.Hour: "7 days", 30 * 24 * time.Hour: "30 days", 36 * time.Hour: "36 hours",
	} {
		assert.Equal(t, want, formatDays(d))
	}
}

func TestConsentPage_UpstreamDisplayName(t *testing.T) {
	t.Parallel()
	const name = "ffffffff-ffff-ffff-ffff-ffffffffffff"
	h, _, _, handle, _, _ := startConsentTest(t)
	h.upstreams[0].Name, h.upstreams[0].DisplayName = name, "Acme Okta"
	body := renderConsentBody(t, h, handle, &storage.PendingAuthorization{
		RedirectURI: testAuthRedirectURI, ClientID: testAuthClientID, UpstreamProviderName: name,
	})
	assert.Contains(t, body, "You will sign in with Acme Okta next.")
	assert.Contains(t, body, "<dt>Identity provider (internal name)</dt><dd>"+name+"</dd>")
	assert.NotContains(t, body, "You will sign in with "+name)
}

// TestConsentPage_UpstreamDisplayNameViaAuthorize drives a real GET /oauth/authorize,
// so the label reaches the page through the same path a browser takes. With several
// upstreams, the page names only the first one, which the user signs in with next.
func TestConsentPage_UpstreamDisplayNameViaAuthorize(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T) *Handler
		want   string
		absent string
	}{
		{name: "single upstream", setup: func(t *testing.T) *Handler {
			t.Helper()
			h, _, _ := handlerTestSetup(t)
			h.upstreams[0].Name, h.upstreams[0].DisplayName = "ffffffff-ffff-ffff-ffff-ffffffffffff", "Acme Okta"
			return h
		}, want: "Acme Okta", absent: "You will sign in with ffffffff"},
		{name: "first of several upstreams", setup: func(t *testing.T) *Handler {
			t.Helper()
			h, _, _, _ := multiUpstreamTestSetup(t)
			h.upstreams[0].DisplayName, h.upstreams[1].DisplayName = "First IdP", "Second IdP"
			return h
		}, want: "First IdP", absent: "Second IdP"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := authorizePage(t, tc.setup(t)).Body.String()
			assert.Contains(t, body, "You will sign in with "+tc.want+" next.")
			assert.NotContains(t, body, tc.absent)
		})
	}
}

func TestConsentPage_UpstreamDisplayNameSanitized(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, in, want, absent string
	}{
		{name: "bidi override", in: "Acme\u202eatkO", want: "AcmeatkO"},
		{name: "zero width space", in: "Ac\u200bme Okta", want: "Acme Okta"},
		{name: "newline", in: "Acme\nOkta", want: "AcmeOkta"},
		{name: "surrounding whitespace", in: "  Acme Okta \u200b ", want: "Acme Okta"},
		{name: "html escaped", in: "<b>Acme</b>", want: "&lt;b&gt;Acme&lt;/b&gt;", absent: "<b>Acme"},
		{name: "ampersand and quotes escaped", in: `A&B "C"`, want: "A&amp;B &#34;C&#34;"},
		{name: "truncated", in: strings.Repeat("é", 200), want: strings.Repeat("é", maxConsentTextRunes) + "…"},
		{name: "exactly at the cap is kept whole", in: strings.Repeat("é", maxConsentTextRunes), want: strings.Repeat("é", maxConsentTextRunes)},
		{name: "no space before the ellipsis", in: strings.Repeat("a", maxConsentTextRunes-1) + " bc", want: strings.Repeat("a", maxConsentTextRunes-1) + "…"},
		{name: "empty falls back to name", in: "", want: "test-upstream"},
		{name: "whitespace only falls back to name", in: " \t ", want: "test-upstream"},
		{name: "format runes only fall back to name", in: "\u202e\u200b\u2066", want: "test-upstream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, _, _, handle, _, _ := startConsentTest(t)
			h.upstreams[0].DisplayName = tc.in
			body := renderConsentBody(t, h, handle, &storage.PendingAuthorization{
				RedirectURI: testAuthRedirectURI, ClientID: testAuthClientID, UpstreamProviderName: "test-upstream",
			})
			assert.Contains(t, body, "You will sign in with "+tc.want+" next.")
			assert.Contains(t, body, "<dt>Identity provider (internal name)</dt><dd>test-upstream</dd>")
			if tc.absent != "" {
				assert.NotContains(t, body, tc.absent)
			}
		})
	}
}

func TestConsentPage_MissingUpstreamFallsBackToName(t *testing.T) {
	t.Parallel()
	h, state, _, handle, cookie, _ := startConsentTest(t)
	// The pending authorization's upstream was removed after the request started;
	// a different upstream with a display name is configured instead.
	h.upstreams = []NamedUpstream{{Name: "other", DisplayName: "Other IdP", Provider: h.upstreams[0].Provider}}
	body := renderConsentBody(t, h, handle, state.pendingAuths[handle])
	assert.Contains(t, body, "You will sign in with test-upstream next.")
	assert.NotContains(t, body, "Other IdP")

	approve := httptest.NewRecorder()
	h.ConsentHandler(approve, consentRequest(handle, "approve", testAuthIssuer, cookie))
	assert.Equal(t, http.StatusSeeOther, approve.Code)
	assert.Contains(t, approve.Header().Get("Location"), "error=server_error")
	assert.Empty(t, state.pendingAuths)
}

// TestDisplayNameChangeKeepsStoredRecordsValid guards that renaming an upstream's
// display name leaves records keyed by Name usable: the pending authorization and
// upstream tokens still load under the same name, and remembered consent still
// matches. The record types carry no DisplayName field, so the label cannot be
// persisted; this test pins the lookups, not the absence of the label.
func TestDisplayNameChangeKeepsStoredRecordsValid(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			var s interface {
				storage.Storage
				storage.RememberedConsentStorage
			}
			if backend == "redis" {
				mr := miniredis.RunT(t)
				client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
				t.Cleanup(func() { require.NoError(t, client.Close()) })
				s = storage.NewRedisStorageWithClient(client, "consent:display:")
			} else {
				mem := storage.NewMemoryStorage()
				t.Cleanup(func() { require.NoError(t, mem.Close()) })
				s = mem
			}
			h, _, _ := handlerTestSetup(t)
			h.rememberedStorage = s
			h.upstreams[0].DisplayName = "Acme Okta"

			pending := &storage.PendingAuthorization{ClientID: testAuthClientID, RedirectURI: testAuthRedirectURI,
				Scopes: []string{"openid"}, UpstreamProviderName: "test-upstream", InternalState: "internal-state",
				PKCEChallenge: strings.Repeat("A", 43), PKCEMethod: "S256", RememberConsent: true,
				FirstProviderSubject: "sub", ConsentStage: storage.ConsentStageApproved, CreatedAt: time.Now()}
			require.NoError(t, s.StorePendingAuthorization(ctx, "internal-state", pending))
			require.NoError(t, s.StoreUpstreamTokens(ctx, "sid", "test-upstream", &storage.UpstreamTokens{
				ProviderID: "test-upstream", AccessToken: "at", UserID: "user", UpstreamSubject: "sub",
				ClientID: testAuthClientID, ExpiresAt: time.Now().Add(time.Hour),
			}))
			remember := httptest.NewRecorder()
			h.maybeRememberConsent(ctx, remember, pending, "user")
			var consentCookie *http.Cookie
			for _, c := range remember.Result().Cookies() {
				if c.Name == h.consentCookieName() {
					consentCookie = c
				}
			}
			require.NotNil(t, consentCookie)

			h.upstreams[0].DisplayName = "Renamed IdP"

			loaded, err := s.LoadPendingAuthorization(ctx, "internal-state")
			require.NoError(t, err)
			assert.Equal(t, "test-upstream", loaded.UpstreamProviderName)
			tokens, err := s.GetUpstreamTokens(ctx, "sid", "test-upstream")
			require.NoError(t, err)
			assert.Equal(t, "test-upstream", tokens.ProviderID)
			req := httptest.NewRequest(http.MethodGet, "/oauth/authorize", nil)
			req.AddCookie(consentCookie)
			session, _, err := h.rememberedSession(ctx, req)
			require.NoError(t, err)
			require.NotNil(t, session, "remembered consent must survive a display name change")
			assert.Equal(t, "test-upstream", session.ProviderID)
		})
	}
}
