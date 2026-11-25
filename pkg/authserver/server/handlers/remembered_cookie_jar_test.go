// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

func TestIndependentRememberedConsentCookieJar(t *testing.T) {
	t.Parallel()
	for _, samePort := range []bool{true, false} {
		name := "different ports"
		if samePort {
			name = "different paths on one port"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			first, _, firstUpstream := handlerTestSetup(t)
			second, _, secondUpstream := handlerTestSetup(t)
			firstStore := storage.NewMemoryStorage()
			secondStore := storage.NewMemoryStorage()
			t.Cleanup(func() {
				require.NoError(t, firstStore.Close())
				require.NoError(t, secondStore.Close())
			})
			first.rememberedStorage = firstStore
			second.rememberedStorage = secondStore
			pool := x509.NewCertPool()
			var bases [2]string
			if samePort {
				mux := http.NewServeMux()
				mux.Handle("/a/", http.StripPrefix("/a", first.Routes()))
				mux.Handle("/b/", http.StripPrefix("/b", second.Routes()))
				srv := httptest.NewTLSServer(mux)
				t.Cleanup(srv.Close)
				pool.AddCert(srv.Certificate())
				bases = [2]string{srv.URL + "/a", srv.URL + "/b"}
			} else {
				a := httptest.NewTLSServer(first.Routes())
				b := httptest.NewTLSServer(second.Routes())
				t.Cleanup(a.Close)
				t.Cleanup(b.Close)
				pool.AddCert(a.Certificate())
				pool.AddCert(b.Certificate())
				bases = [2]string{a.URL, b.URL}
			}
			// Both servers intentionally advertise the same issuer but use independent browser bases.
			first.config.AccessTokenIssuer = "https://issuer.example"
			second.config.AccessTokenIssuer = first.config.AccessTokenIssuer
			first.config.AuthorizationEndpointBaseURL = bases[0]
			second.config.AuthorizationEndpointBaseURL = bases[1]
			jar, err := cookiejar.New(nil)
			require.NoError(t, err)
			transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
			t.Cleanup(transport.CloseIdleConnections)
			client := &http.Client{Transport: transport, Jar: jar, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}

			for _, item := range []struct {
				base     string
				upstream *mockIDPProvider
			}{{bases[0], firstUpstream}, {bases[1], secondUpstream}} {
				resp, err := client.Get(item.base + "/oauth/authorize?" + url.Values{
					"client_id": {testAuthClientID}, "redirect_uri": {testAuthRedirectURI},
					"response_type": {"code"}, "state": {"browser-state"},
					"code_challenge": {"challenge123"}, "code_challenge_method": {"S256"},
					"scope": {"openid"}, "resource": {"https://api.example.com"},
				}.Encode())
				require.NoError(t, err)
				page := readBody(t, resp)
				require.Equal(t, http.StatusOK, resp.StatusCode, page)
				require.Contains(t, page, `name="remember"`)
				_, rest, ok := strings.Cut(page, `name="handle" value="`)
				require.True(t, ok)
				handle, _, ok := strings.Cut(rest, `"`)
				require.True(t, ok)
				form := url.Values{"handle": {handle}, "decision": {"approve"}, "remember": {"1"}}
				req, err := http.NewRequest(http.MethodPost, item.base+"/oauth/consent", strings.NewReader(form.Encode()))
				require.NoError(t, err)
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				origin, err := url.Parse(item.base)
				require.NoError(t, err)
				req.Header.Set("Origin", origin.Scheme+"://"+origin.Host)
				resp, err = client.Do(req)
				require.NoError(t, err)
				require.Equal(t, http.StatusSeeOther, resp.StatusCode, readBody(t, resp))
				require.NotEmpty(t, item.upstream.capturedState)
				resp, err = client.Get(item.base + "/oauth/callback?code=upstream-code&state=" + item.upstream.capturedState)
				require.NoError(t, err)
				require.Equal(t, http.StatusSeeOther, resp.StatusCode, readBody(t, resp))
				require.Contains(t, resp.Header.Get("Location"), "code=")
			}
			require.NotEqual(t, first.consentCookieName(), second.consentCookieName())
			baseURL, err := url.Parse(bases[0] + "/oauth/authorize")
			require.NoError(t, err)
			cookies := jar.Cookies(baseURL)
			var a, b *http.Cookie
			for _, c := range cookies {
				switch c.Name {
				case first.consentCookieName():
					a = c
				case second.consentCookieName():
					b = c
				}
			}
			require.NotNil(t, a)
			require.NotNil(t, b)
			require.NotEqual(t, a.Value, b.Value)
			_, err = firstStore.GetConsentSession(t.Context(), consentDigest(a.Value))
			require.NoError(t, err)
			_, err = secondStore.GetConsentSession(t.Context(), consentDigest(b.Value))
			require.NoError(t, err)
			for _, item := range []struct {
				h          *Handler
				base       string
				own, other *http.Cookie
			}{{first, bases[0], a, b}, {second, bases[1], b, a}} {
				query := url.Values{"client_id": {testAuthClientID}, "redirect_uri": {testAuthRedirectURI},
					"response_type": {"code"}, "state": {"browser-state"}, "code_challenge": {"challenge123"},
					"code_challenge_method": {"S256"}, "scope": {"openid"}, "resource": {"https://api.example.com"}}.Encode()
				resp, err := client.Get(item.base + "/oauth/authorize?" + query)
				require.NoError(t, err)
				require.Equal(t, http.StatusSeeOther, resp.StatusCode, readBody(t, resp))
				// Explicitly put the other server's cookie first, independent of jar ordering.
				req := httptest.NewRequest(http.MethodGet, item.base+"/oauth/authorize?"+query, nil)
				req.AddCookie(item.other)
				req.AddCookie(item.own)
				rec := httptest.NewRecorder()
				item.h.AuthorizeHandler(rec, req)
				require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
			}
		})
	}
}
