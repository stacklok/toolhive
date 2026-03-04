// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFix_Single401_DoesNotMarkUnauthenticated verifies that a single 401
// from the remote server does NOT fire the onUnauthorizedResponse callback.
// The workload should remain available for subsequent requests.
func TestFix_Single401_DoesNotMarkUnauthenticated(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int32
	remoteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := requestCount.Add(1)
		if count == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{}}`))
	}))
	defer remoteServer.Close()

	targetURL, err := url.Parse(remoteServer.URL)
	require.NoError(t, err)

	var unauthorizedCallbackCount atomic.Int32
	proxy := NewTransparentProxy(
		"127.0.0.1", 0, remoteServer.URL,
		nil, nil, nil,
		false, true, "streamable-http",
		nil,
		func() { unauthorizedCallbackCount.Add(1) },
		"", false,
	)

	reverseProxy := httputil.NewSingleHostReverseProxy(targetURL)
	reverseProxy.FlushInterval = -1
	reverseProxy.Transport = &tracingTransport{base: http.DefaultTransport, p: proxy}

	// First request: gets 401 from remote
	req1 := httptest.NewRequest("POST", "/v2/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"initialize","id":"1","params":{}}`))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	reverseProxy.ServeHTTP(rec1, req1)

	assert.Equal(t, http.StatusUnauthorized, rec1.Code,
		"first request should return 401 from remote")

	// FIX VERIFIED: callback was NOT fired on a single 401
	assert.Equal(t, int32(0), unauthorizedCallbackCount.Load(),
		"callback should NOT fire on a single 401 — workload remains available")

	// Second request: succeeds (proving workload is still alive)
	req2 := httptest.NewRequest("POST", "/v2/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"initialize","id":"2","params":{}}`))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	reverseProxy.ServeHTTP(rec2, req2)

	assert.Equal(t, http.StatusOK, rec2.Code,
		"second request should succeed — workload was not permanently killed")

	// Counter should have reset to 0 after the 200 response
	assert.Equal(t, int32(0), proxy.consecutiveUnauthorized.Load(),
		"consecutive counter should reset to 0 after successful response")
}

// TestFix_ConsecutiveThreshold_Required verifies that the callback only
// fires after the configured number of consecutive 401 responses.
func TestFix_ConsecutiveThreshold_Required(t *testing.T) {
	t.Parallel()

	// Remote server always returns 401
	remoteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer remoteServer.Close()

	targetURL, err := url.Parse(remoteServer.URL)
	require.NoError(t, err)

	var callbackFired atomic.Bool
	proxy := newTransparentProxyWithOptions(
		"127.0.0.1", 0, remoteServer.URL,
		nil, nil, nil,
		false, true, "streamable-http",
		nil,
		func() { callbackFired.Store(true) },
		"", false,
		nil, // middlewares
		withUnauthorizedThreshold(3),
	)

	reverseProxy := httputil.NewSingleHostReverseProxy(targetURL)
	reverseProxy.FlushInterval = -1
	reverseProxy.Transport = &tracingTransport{base: http.DefaultTransport, p: proxy}

	sendRequest := func() {
		req := httptest.NewRequest("POST", "/v2/mcp",
			strings.NewReader(`{"jsonrpc":"2.0","method":"initialize","id":"1","params":{}}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		reverseProxy.ServeHTTP(rec, req)
	}

	// First 401 — callback should NOT fire
	sendRequest()
	assert.False(t, callbackFired.Load(), "callback should not fire after 1st consecutive 401")
	assert.Equal(t, int32(1), proxy.consecutiveUnauthorized.Load())

	// Second 401 — callback should NOT fire
	sendRequest()
	assert.False(t, callbackFired.Load(), "callback should not fire after 2nd consecutive 401")
	assert.Equal(t, int32(2), proxy.consecutiveUnauthorized.Load())

	// Third 401 — callback SHOULD fire (threshold = 3)
	sendRequest()
	assert.True(t, callbackFired.Load(),
		"callback should fire after 3rd consecutive 401 (threshold reached)")
	assert.Equal(t, int32(3), proxy.consecutiveUnauthorized.Load())
}

// TestFix_NonConsecutive401s_ResetCounter verifies that a successful response
// between 401s resets the counter, preventing the callback from firing.
func TestFix_NonConsecutive401s_ResetCounter(t *testing.T) {
	t.Parallel()

	// Pattern: 401, 200, 401, 200 — should never reach threshold
	var requestCount atomic.Int32
	remoteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := requestCount.Add(1)
		if count%2 == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{}}`))
	}))
	defer remoteServer.Close()

	targetURL, err := url.Parse(remoteServer.URL)
	require.NoError(t, err)

	var callbackFired atomic.Bool
	proxy := newTransparentProxyWithOptions(
		"127.0.0.1", 0, remoteServer.URL,
		nil, nil, nil,
		false, true, "streamable-http",
		nil,
		func() { callbackFired.Store(true) },
		"", false,
		nil, // middlewares
		withUnauthorizedThreshold(3),
	)

	reverseProxy := httputil.NewSingleHostReverseProxy(targetURL)
	reverseProxy.FlushInterval = -1
	reverseProxy.Transport = &tracingTransport{base: http.DefaultTransport, p: proxy}

	sendRequest := func() {
		req := httptest.NewRequest("POST", "/v2/mcp",
			strings.NewReader(`{"jsonrpc":"2.0","method":"initialize","id":"1","params":{}}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		reverseProxy.ServeHTTP(rec, req)
	}

	// Send 6 requests (alternating 401/200) — counter never reaches threshold
	for range 6 {
		sendRequest()
	}

	assert.False(t, callbackFired.Load(),
		"callback should never fire when 401s are non-consecutive (reset by 200 responses)")
}

// TestFix_401PassesThroughButDoesNotKill verifies that the 401 response from
// the remote server is still passed through to the client (transparent proxy
// behavior), but does NOT permanently mark the workload on a single failure.
func TestFix_401PassesThroughButDoesNotKill(t *testing.T) {
	t.Parallel()

	remoteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="asana"`)
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "invalid_token"}`))
	}))
	defer remoteServer.Close()

	targetURL, err := url.Parse(remoteServer.URL)
	require.NoError(t, err)

	var callbackFired bool
	proxy := NewTransparentProxy(
		"127.0.0.1", 0, remoteServer.URL,
		nil, nil, nil,
		false, true, "streamable-http",
		nil,
		func() { callbackFired = true },
		"", false,
	)

	reverseProxy := httputil.NewSingleHostReverseProxy(targetURL)
	reverseProxy.FlushInterval = -1
	reverseProxy.Transport = &tracingTransport{base: http.DefaultTransport, p: proxy}

	req := httptest.NewRequest("POST", "/v2/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","method":"initialize","id":"1","params":{}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	reverseProxy.ServeHTTP(rec, req)

	// The 401 from the remote server is still passed through to the client
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"401 should still pass through to client (transparent proxy)")

	// But the workload is NOT permanently killed
	assert.False(t, callbackFired,
		"FIX VERIFIED: single 401 does NOT permanently mark workload as unauthenticated")
}
