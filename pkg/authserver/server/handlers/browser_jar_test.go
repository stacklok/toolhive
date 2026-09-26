// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests in this file drive the handlers over a real HTTP server with a
// real cookie jar, so that host-only cookie delivery is exercised rather than
// simulated. The server listens on 127.0.0.1 and is also reachable as
// "localhost"; a browser keeps separate cookies for the two names, so
// advertising a page on one host and expecting its cookie on the other fails
// here exactly as it would in a browser. The issuer is placed on 127.0.0.1 and
// the browser-facing authorize base URL on localhost, the split-host shape
// AuthorizationEndpointBaseURL exists for.

// splitHostServer serves h and returns the server plus the browser-facing base
// URL (localhost) that differs from the issuer host (127.0.0.1).
func splitHostServer(t *testing.T, h *Handler) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(h.Routes())
	t.Cleanup(srv.Close)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	browserBase := fmt.Sprintf("http://localhost:%d", port)
	h.config.AccessTokenIssuer = srv.URL
	h.config.AuthorizationEndpointBaseURL = browserBase
	return srv, browserBase
}

// jarClient is a browser-like client: it keeps cookies per host and does not
// follow redirects, so each hop can be inspected.
func jarClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

// TestDeviceVerificationFlow_CookieJar_AdvertisedPageReachesCallback starts
// where a real user does, at the verification URI the device authorization
// response advertises, and proves the browser-binding cookie set there is
// delivered to /oauth/callback on the browser-facing host. Advertising the page
// from the issuer instead would strand the cookie on the other host and every
// device login would fail the binding check.
func TestDeviceVerificationFlow_CookieJar_AdvertisedPageReachesCallback(t *testing.T) {
	t.Parallel()
	h, mockUpstream := setupDeviceVerificationHandler(t)
	srv, browserBase := splitHostServer(t, h)
	client := jarClient(t)

	// The device asks for a code on the issuer host.
	resp, err := client.PostForm(srv.URL+"/oauth/device_authorization",
		url.Values{"client_id": {"device-client"}, "scope": {"openid"}})
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(readBody(t, resp)), &body))
	verificationURI := body["verification_uri_complete"].(string)
	userCode := body["user_code"].(string)
	require.True(t, strings.HasPrefix(verificationURI, browserBase),
		"verification page must be advertised on the browser-facing host, got %s", verificationURI)

	// The user opens the advertised page and submits the form it renders.
	resp, err = client.Get(verificationURI)
	require.NoError(t, err)
	page := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, page)
	token := formTokenPattern.FindStringSubmatch(page)
	require.Len(t, token, 2)

	resp, err = client.PostForm(browserBase+"/oauth/device",
		url.Values{"user_code": {userCode}, deviceFormTokenField: {token[1]}})
	require.NoError(t, err)
	require.Equal(t, http.StatusFound, resp.StatusCode, readBody(t, resp))
	state := mockUpstream.capturedState
	require.NotEmpty(t, state)

	// The IdP sends the browser back to the callback on the browser-facing host.
	resp, err = client.Get(browserBase + "/oauth/callback?code=upstream-code&state=" + state)
	require.NoError(t, err)
	confirm := readBody(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode, confirm)
	assert.Contains(t, confirm, "Ada Lovelace", "the confirmation page is shown to the binding browser")
}

// TestAuthorizeFlow_CookieJar_BrowserFacingHostReachesCallback is the
// authorization-code counterpart: a browser that starts at /oauth/authorize on
// the browser-facing host completes the callback there with a real cookie jar.
func TestAuthorizeFlow_CookieJar_BrowserFacingHostReachesCallback(t *testing.T) {
	t.Parallel()
	handler, _, mockUpstream := handlerTestSetup(t)
	_, browserBase := splitHostServer(t, handler)
	client := jarClient(t)

	params := url.Values{
		"client_id":             {testAuthClientID},
		"redirect_uri":          {testAuthRedirectURI},
		"response_type":         {"code"},
		"state":                 {"client-state"},
		"code_challenge":        {"challenge123"},
		"code_challenge_method": {"S256"},
		"scope":                 {"openid"},
	}
	resp, err := client.Get(browserBase + "/oauth/authorize?" + params.Encode())
	require.NoError(t, err)
	require.Equal(t, http.StatusFound, resp.StatusCode, readBody(t, resp))
	state := mockUpstream.capturedState
	require.NotEmpty(t, state)

	resp, err = client.Get(browserBase + "/oauth/callback?code=upstream-code&state=" + state)
	require.NoError(t, err)
	body := readBody(t, resp)
	assert.Equal(t, http.StatusSeeOther, resp.StatusCode, body)
	assert.Contains(t, resp.Header.Get("Location"), "code=")
}
