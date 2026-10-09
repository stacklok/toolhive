// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/transport/proxy/transparent"
)

func TestRememberedConsentCookieIssuedByAuthServerIsStrippedByProxy(t *testing.T) {
	t.Parallel()

	const authorizationOrigin = "https://auth.example.com"
	mockIDP := startMockOIDC(t)
	ts := setupTestServerWithMockOIDC(t, mockIDP,
		withAuthorizationEndpointBaseURL(authorizationOrigin),
	)

	client := noRedirectClient()
	challenge := strings.Repeat("A", 43)
	params := authorizationParams{
		ClientID:     testClientID,
		RedirectURI:  testRedirectURI,
		State:        "remembered-consent-state",
		Challenge:    challenge,
		Scope:        "openid",
		Resource:     testAudience,
		ResponseType: "code",
	}
	query := url.Values{
		"client_id":             {params.ClientID},
		"redirect_uri":          {params.RedirectURI},
		"state":                 {params.State},
		"code_challenge":        {params.Challenge},
		"code_challenge_method": {"S256"},
		"response_type":         {params.ResponseType},
		"scope":                 {params.Scope},
		"resource":              {params.Resource},
	}

	resp, err := client.Get(ts.Server.URL + "/oauth/authorize?" + query.Encode())
	require.NoError(t, err)
	page, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	handleMatch := consentHandlePattern.FindSubmatch(page)
	require.Len(t, handleMatch, 2, "consent handle missing")

	form := url.Values{
		"handle":   {string(handleMatch[1])},
		"decision": {"approve"},
		"remember": {"1"},
	}
	req, err := http.NewRequest(http.MethodPost, ts.Server.URL+"/oauth/consent", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", authorizationOrigin)
	for _, cookie := range resp.Cookies() {
		req.AddCookie(cookie)
	}
	resp, err = client.Do(req)
	require.NoError(t, err)
	consentCookies := resp.Cookies()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.NoError(t, drainAndClose(resp.Body))
	upstreamLocation, err := resp.Location()
	require.NoError(t, err)

	resp, err = client.Get(upstreamLocation.String())
	require.NoError(t, err)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	callbackLocation, err := resp.Location()
	require.NoError(t, err)
	require.NoError(t, drainAndClose(resp.Body))
	parsedServerURL, err := url.Parse(ts.Server.URL)
	require.NoError(t, err)
	callbackLocation.Scheme = parsedServerURL.Scheme
	callbackLocation.Host = parsedServerURL.Host

	resp = getWithCookies(t, client, callbackLocation.String(), consentCookies)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	issuedCookies := resp.Cookies()
	location, err := resp.Location()
	require.NoError(t, err)
	require.NoError(t, drainAndClose(resp.Body))
	require.NotEmpty(t, location.Query().Get("code"), "authorization must succeed")

	var consentCookie *http.Cookie
	for _, cookie := range issuedCookies {
		if strings.HasPrefix(cookie.Name, "__Host-thv_consent_") {
			consentCookie = cookie
			break
		}
	}
	require.NotNil(t, consentCookie, "authserver authorization response must issue remembered-consent cookie")
	require.NotEmpty(t, consentCookie.Value, "authserver-issued consent cookie must have a value")

	backendCookies := make(chan string, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case backendCookies <- r.Header.Get("Cookie"):
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)
	proxy := transparent.NewTransparentProxyWithOptions(
		"127.0.0.1", 0, backend.URL, nil, nil, nil, false, false, "streamable-http",
		nil, nil, "", false, nil, transparent.WithStripConsentCookie(),
	)
	startContext, startCancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(startCancel)
	require.NoError(t, proxy.Start(startContext))
	t.Cleanup(func() {
		stopContext, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		require.NoError(t, proxy.Stop(stopContext))
	})

	requestContext, requestCancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(requestCancel)
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost,
		"http://"+proxy.ListenerAddr()+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"tools/list"}`))
	require.NoError(t, err)
	request.AddCookie(&http.Cookie{Name: "backend", Value: "one"})
	request.AddCookie(consentCookie)
	backendResponse, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, backendResponse.StatusCode)
	require.NoError(t, drainAndClose(backendResponse.Body))

	select {
	case cookieHeader := <-backendCookies:
		require.Equal(t, "backend=one", cookieHeader,
			"proxy must preserve the ordinary backend cookie while stripping the issued consent cookie")
	case <-requestContext.Done():
		t.Fatal("timed out waiting for backend observation")
	}
}

func drainAndClose(body io.ReadCloser) error {
	_, readErr := io.Copy(io.Discard, body)
	closeErr := body.Close()
	if readErr != nil {
		return readErr
	}
	return closeErr
}

var consentHandlePattern = regexp.MustCompile(`name="handle" value="([^"]+)"`)
