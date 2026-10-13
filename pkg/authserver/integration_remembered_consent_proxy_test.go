// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
	_, _, issuedCookies := completeAuthorizationFlowWithConsent(t, ts.Server.URL, params, authorizationOrigin, true)

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
