// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegression_LocalhostProxy_NonLocalhostHostHeaderRewritten verifies that
// tracingTransport.RoundTrip rewrites a non-localhost Host header to match the
// target URL's host. Without this, an attacker could inject a Host header to
// bypass host-based validation on the upstream server.
func TestRegression_LocalhostProxy_NonLocalhostHostHeaderRewritten(t *testing.T) {
	t.Parallel()

	// Capture the Host header the upstream server actually receives.
	var receivedHost string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHost = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	targetURL, err := url.Parse(upstream.URL)
	require.NoError(t, err)

	p := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, nil, false, false, "streamable-http", nil, nil, "", false)

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(targetURL)
			pr.SetXForwarded()
		},
		FlushInterval:  -1,
		Transport:      newTracingTransport(http.DefaultTransport, p),
		ModifyResponse: p.modifyResponse,
	}

	// Send a request with a malicious Host header.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://evil.example.com/some/path", nil)
	req.Host = "evil.example.com"

	proxy.ServeHTTP(rec, req)

	// The outbound request's Host must be the target URL's host,
	// not the attacker-supplied value.
	assert.Equal(t, targetURL.Host, receivedHost,
		"tracingTransport must rewrite Host header from attacker value to target URL host")
	assert.NotEqual(t, "evil.example.com", receivedHost,
		"attacker Host header must not reach the upstream server")
}
