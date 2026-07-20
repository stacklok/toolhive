// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/llm"
)

// stubTokenSource is a test double for TokenSource.
type stubTokenSource struct {
	token string
	err   error
}

func (s *stubTokenSource) Token(_ context.Context) (string, error) {
	return s.token, s.err
}

// testClient returns an http.Client with a 5-second timeout for use in tests.
func testClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}

// loopbackRequest returns a GET server request with Host set to 127.0.0.1 so
// it passes the DNS-rebinding guard in the proxy handler.
func loopbackRequest(target string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = "127.0.0.1"
	return req
}

// newTLSGateway starts a TLS test server and returns a Proxy configured to
// trust its self-signed certificate.
func newTLSGateway(t *testing.T, handler http.Handler) *Proxy {
	t.Helper()
	gateway := httptest.NewTLSServer(handler)
	t.Cleanup(gateway.Close)

	cfg := &llm.Config{
		GatewayURL: gateway.URL,
		Proxy:      llm.ProxyConfig{ListenPort: freePort(t)},
	}
	p, err := New(cfg, &stubTokenSource{token: "test-token"})
	require.NoError(t, err)
	p.transport = gateway.Client().Transport
	t.Cleanup(func() { _ = p.listener.Close() })
	return p
}

// freePort returns an available TCP port on loopback.
// It binds then immediately closes to discover the port number; there is a
// small TOCTOU window before the caller binds, which is acceptable in tests.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

func TestNew_RejectsHTTPGatewayURL(t *testing.T) {
	t.Parallel()
	cfg := &llm.Config{
		GatewayURL: "http://gateway.example.com",
		Proxy:      llm.ProxyConfig{ListenPort: freePort(t)},
	}
	_, err := New(cfg, &stubTokenSource{token: "tok"})
	require.ErrorContains(t, err, "must use HTTPS")
}

func TestNew_ValidConfig(t *testing.T) {
	t.Parallel()
	cfg := &llm.Config{
		GatewayURL: "https://gateway.example.com",
		Proxy:      llm.ProxyConfig{ListenPort: freePort(t)},
	}
	p, err := New(cfg, &stubTokenSource{token: "tok"})
	require.NoError(t, err)
	require.NotNil(t, p)
	// Addr must be a valid TCP address on loopback.
	host, _, splitErr := net.SplitHostPort(p.Addr())
	require.NoError(t, splitErr)
	assert.Equal(t, "127.0.0.1", host)
	// Close the listener to free the port.
	_ = p.listener.Close()
}

func TestHandler_InjectsToken(t *testing.T) {
	t.Parallel()
	var (
		mu           sync.Mutex
		receivedAuth string
	)
	p := newTLSGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	p.tokenSource = &stubTokenSource{token: "fresh-token-abc"}

	req := loopbackRequest("/v1/models")
	req.Header.Set("Authorization", "Bearer old-token")
	w := httptest.NewRecorder()
	p.handler().ServeHTTP(w, req)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "Bearer fresh-token-abc", receivedAuth,
		"gateway should receive the fresh token, not the original")
}

func TestHandler_StripsIncomingAuthorization(t *testing.T) {
	t.Parallel()
	var (
		mu           sync.Mutex
		receivedAuth string
	)
	p := newTLSGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	p.tokenSource = &stubTokenSource{token: "injected"}

	req := loopbackRequest("/v1/models")
	req.Header.Set("Authorization", "Bearer user-supplied-token")
	w := httptest.NewRecorder()
	p.handler().ServeHTTP(w, req)

	mu.Lock()
	defer mu.Unlock()
	assert.NotContains(t, receivedAuth, "user-supplied-token",
		"incoming Authorization must be stripped")
	assert.Equal(t, "Bearer injected", receivedAuth)
}

func TestHandler_RejectsDNSRebindingHost(t *testing.T) {
	t.Parallel()
	cfg := &llm.Config{
		GatewayURL: "https://gateway.example.com",
		Proxy:      llm.ProxyConfig{ListenPort: freePort(t)},
	}
	p, err := New(cfg, &stubTokenSource{token: "tok"})
	require.NoError(t, err)
	defer p.listener.Close()

	for _, host := range []string{"evil.com", "evil.com:80", "192.168.1.1", "192.168.1.1:8080"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Host = host
		w := httptest.NewRecorder()
		p.handler().ServeHTTP(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code, "host %q should be rejected", host)
	}

	// Legitimate loopback hosts must be allowed through.
	for _, host := range []string{"127.0.0.1:14000", "localhost:14000", "[::1]:14000"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Host = host
		w := httptest.NewRecorder()
		p.handler().ServeHTTP(w, req)
		assert.NotEqual(t, http.StatusForbidden, w.Code, "host %q should be allowed", host)
	}
}

func TestHandler_Returns502OnTokenError(t *testing.T) {
	t.Parallel()
	cfg := &llm.Config{
		GatewayURL: "https://gateway.example.com",
		Proxy:      llm.ProxyConfig{ListenPort: freePort(t)},
	}
	p, err := New(cfg, &stubTokenSource{err: errors.New("token unavailable")})
	require.NoError(t, err)
	defer p.listener.Close()

	req := loopbackRequest("/v1/chat/completions")
	w := httptest.NewRecorder()
	p.handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Contains(t, w.Body.String(), `"type":"server_error"`)
	assert.NotContains(t, w.Body.String(), "token unavailable",
		"internal error detail must not be leaked to the client")
}

func TestHandler_Returns401WithActionableMessageOnErrTokenRequired(t *testing.T) {
	t.Parallel()
	cfg := &llm.Config{
		GatewayURL: "https://gateway.example.com",
		Proxy:      llm.ProxyConfig{ListenPort: freePort(t)},
	}
	p, err := New(cfg, &stubTokenSource{err: llm.ErrTokenRequired})
	require.NoError(t, err)
	defer p.listener.Close()

	req := loopbackRequest("/v1/chat/completions")
	w := httptest.NewRecorder()
	p.handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	body := w.Body.String()
	assert.Contains(t, body, "thv llm setup")
	assert.Contains(t, body, `"type":"authentication_error"`)
	assert.Contains(t, body, `"code":"token_required"`)
}

// startTestProxy starts the proxy against the given TLS gateway using a real
// TCP listener and returns the proxy's base URL. The proxy is stopped when
// t.Cleanup runs.
func startTestProxy(t *testing.T, gateway *httptest.Server) string {
	t.Helper()
	cfg := &llm.Config{
		GatewayURL: gateway.URL,
		Proxy:      llm.ProxyConfig{ListenPort: freePort(t)},
	}
	p, err := New(cfg, &stubTokenSource{token: "test-token"})
	require.NoError(t, err)
	p.transport = gateway.Client().Transport

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	serveErr := make(chan error, 1)
	go func() { serveErr <- p.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-serveErr; err != nil {
			t.Errorf("proxy exited with unexpected error: %v", err)
		}
	})

	// Wait until the HTTP server is actually serving — a TCP dial can succeed
	// as soon as the listener is bound (kernel backlog), before Serve() runs.
	// An HTTP response (any status) confirms the handler loop is active.
	// The request must have a loopback Host to pass the DNS-rebinding guard.
	addr := p.Addr()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	require.Eventually(t, func() bool {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+addr+"/readyz", nil)
		req.Host = "127.0.0.1"
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return true
	}, 2*time.Second, 10*time.Millisecond, "proxy did not start in time")

	return "http://" + p.Addr()
}

func TestProxy_ForwardsPathQueryAndBody(t *testing.T) {
	t.Parallel()
	var (
		mu       sync.Mutex
		gotPath  string
		gotQuery string
		gotBody  []byte
		gotAuth  string
	)
	gateway := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotBody = b
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer gateway.Close()

	proxyURL := startTestProxy(t, gateway)

	body := strings.NewReader(`{"model":"gpt-4"}`)
	resp, err := testClient().Post(proxyURL+"/v1/chat/completions?stream=true", "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()

	// The HTTP response completing guarantees the handler has returned, so the
	// mutex is not held at this point. Reading under the lock is still correct.
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "/v1/chat/completions", gotPath)
	assert.Equal(t, "stream=true", gotQuery)
	assert.JSONEq(t, `{"model":"gpt-4"}`, string(gotBody))
	assert.Equal(t, "Bearer test-token", gotAuth)
}

func TestProxy_PassesThroughSSE(t *testing.T) {
	t.Parallel()
	gateway := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)
		for _, chunk := range []string{
			"data: {\"id\":\"1\"}\n\n",
			"data: {\"id\":\"2\"}\n\n",
			"data: [DONE]\n\n",
		} {
			_, _ = fmt.Fprint(w, chunk)
			flusher.Flush()
		}
	}))
	defer gateway.Close()

	proxyURL := startTestProxy(t, gateway)

	resp, err := testClient().Get(proxyURL + "/v1/chat/completions")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(got), "data: {\"id\":\"1\"}")
	assert.Contains(t, string(got), "data: [DONE]")
}

func TestWithTLSSkipVerify(t *testing.T) {
	t.Parallel()

	// Self-signed upstream — default transport cannot verify this certificate.
	gateway := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(gateway.Close)

	t.Run("default transport rejects self-signed cert", func(t *testing.T) {
		t.Parallel()
		cfg := &llm.Config{
			GatewayURL: gateway.URL,
			Proxy:      llm.ProxyConfig{ListenPort: freePort(t)},
		}
		p, err := New(cfg, &stubTokenSource{token: "tok"})
		require.NoError(t, err)
		t.Cleanup(func() { _ = p.listener.Close() })

		req := loopbackRequest("/v1/models")
		w := httptest.NewRecorder()
		p.handler().ServeHTTP(w, req)

		// Certificate verification failure surfaces as 502 Bad Gateway.
		assert.Equal(t, http.StatusBadGateway, w.Code)
	})

	t.Run("WithTLSSkipVerify(true) accepts self-signed cert", func(t *testing.T) {
		t.Parallel()
		cfg := &llm.Config{
			GatewayURL: gateway.URL,
			Proxy:      llm.ProxyConfig{ListenPort: freePort(t)},
		}
		p, err := New(cfg, &stubTokenSource{token: "tok"}, WithTLSSkipVerify(true))
		require.NoError(t, err)
		t.Cleanup(func() { _ = p.listener.Close() })

		req := loopbackRequest("/v1/models")
		w := httptest.NewRecorder()
		p.handler().ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})
}

func TestProxy_PassesThroughErrorResponses(t *testing.T) {
	t.Parallel()
	for _, statusCode := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusInternalServerError} {
		statusCode := statusCode
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			t.Parallel()
			gateway := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "upstream error", statusCode)
			}))
			defer gateway.Close()

			proxyURL := startTestProxy(t, gateway)

			resp, err := testClient().Get(proxyURL + "/v1/models")
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, statusCode, resp.StatusCode, "error response must pass through unmodified")
		})
	}
}
