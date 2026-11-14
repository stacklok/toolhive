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
	// block, if non-nil, is invoked from Token. Tests use it to observe the
	// ctx the proxy passed in and to control timing at the observable boundary
	// (when the fetch resolves). It receives the token-fetch ctx and the token
	// the stub will return once it unblocks. This is test-only instrumentation;
	// production structs are unchanged.
	block func(ctx context.Context, token string)
}

func (s *stubTokenSource) Token(ctx context.Context) (string, error) {
	if s.block != nil {
		s.block(ctx, s.token)
	}
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
	p.handler(context.Background()).ServeHTTP(w, req)

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
	p.handler(context.Background()).ServeHTTP(w, req)

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
		p.handler(context.Background()).ServeHTTP(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code, "host %q should be rejected", host)
	}

	// Legitimate loopback hosts must be allowed through.
	for _, host := range []string{"127.0.0.1:14000", "localhost:14000", "[::1]:14000"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Host = host
		w := httptest.NewRecorder()
		p.handler(context.Background()).ServeHTTP(w, req)
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
	p.handler(context.Background()).ServeHTTP(w, req)

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
	p.handler(context.Background()).ServeHTTP(w, req)

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
		p.handler(context.Background()).ServeHTTP(w, req)

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
		p.handler(context.Background()).ServeHTTP(w, req)

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

// newBlockingTokenProxy builds a Proxy whose token source blocks inside Token
// until the returned release channel is closed (or the fetch ctx is cancelled,
// whichever is first). It also hands the fetch ctx to sawCtx, exactly once,
// so the caller can inspect the context the proxy rooted the fetch in.
//
// The gateway returns 200 so that, once the token is delivered, the handler
// completes normally; tests that only care about the fetch phase never reach it.
func newBlockingTokenProxy(t *testing.T) (p *Proxy, release chan struct{}, sawCtx chan context.Context) {
	t.Helper()

	gateway := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(gateway.Close)

	cfg := &llm.Config{
		GatewayURL: gateway.URL,
		Proxy:      llm.ProxyConfig{ListenPort: freePort(t)},
	}
	release = make(chan struct{})
	sawCtx = make(chan context.Context, 1)
	src := &stubTokenSource{
		token: "fresh-token",
		block: func(ctx context.Context, _ string) {
			select {
			case sawCtx <- ctx:
			default:
			}
			// Block until the test releases us or the proxy cancels the fetch.
			select {
			case <-release:
			case <-ctx.Done():
			}
		},
	}
	p, err := New(cfg, src)
	require.NoError(t, err)
	p.transport = gateway.Client().Transport
	t.Cleanup(func() { _ = p.listener.Close() })
	return p, release, sawCtx
}

// waitForFetchCtx waits for the stubbed Token to report the fetch ctx, failing
// fast on timeout so a miswire never hangs the suite.
func waitForFetchCtx(t *testing.T, sawCtx <-chan context.Context) context.Context {
	t.Helper()
	select {
	case ctx := <-sawCtx:
		return ctx
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for token fetch to start")
		return nil
	}
}

// TestHandler_TokenFetchSurvivesClientDisconnect pins the Step 1 contract that a
// client disconnecting mid-request does NOT cancel the token fetch: the fetch is
// rooted in the proxy-lifetime baseCtx, not the inbound request's context.
func TestHandler_TokenFetchSurvivesClientDisconnect(t *testing.T) {
	t.Parallel()
	p, release, sawCtx := newBlockingTokenProxy(t)

	baseCtx, baseCancel := context.WithCancel(context.Background())
	t.Cleanup(baseCancel)

	// Inbound request carries its own cancellable context, as a real
	// disconnected client would. Host is loopback to pass the DNS-rebinding guard.
	reqCtx, reqCancel := context.WithCancel(context.Background())
	t.Cleanup(reqCancel)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	req = req.WithContext(reqCtx)
	req.Host = "127.0.0.1"

	handlerDone := make(chan struct{})
	go func() {
		w := httptest.NewRecorder()
		p.handler(baseCtx).ServeHTTP(w, req)
		close(handlerDone)
	}()

	fetchCtx := waitForFetchCtx(t, sawCtx)

	// Disconnect the client while the fetch is still in flight.
	reqCancel()

	// Give the cancellation a moment to propagate, then assert the fetch ctx is
	// untouched: under the Step 1 contract the fetch is rooted in baseCtx, which
	// is still alive, so the fetch must keep running.
	select {
	case <-fetchCtx.Done():
		t.Fatal("fetch ctx was cancelled by request ctx — fetch must be rooted in baseCtx, not r.Context()")
	case <-time.After(100 * time.Millisecond):
	}

	// Releasing the stub delivers the token and the handler finishes normally.
	close(release)
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after token was delivered")
	}
}

// TestHandler_TokenFetchCancelsWithStartContext pins the other half of the
// Step 1 contract: cancelling Start's ctx (baseCtx) — e.g. via Ctrl+C — DOES
// cancel the in-flight token fetch.
func TestHandler_TokenFetchCancelsWithStartContext(t *testing.T) {
	t.Parallel()
	p, _, sawCtx := newBlockingTokenProxy(t)

	baseCtx, baseCancel := context.WithCancel(context.Background())
	t.Cleanup(baseCancel)

	req := loopbackRequest("/v1/models")
	handlerDone := make(chan struct{})
	go func() {
		w := httptest.NewRecorder()
		p.handler(baseCtx).ServeHTTP(w, req)
		close(handlerDone)
	}()

	fetchCtx := waitForFetchCtx(t, sawCtx)

	// Cancel Start's lifetime context, exactly as Ctrl+C would.
	baseCancel()

	// The fetch ctx is derived from baseCtx, so it must be cancelled now.
	select {
	case <-fetchCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("fetch ctx was not cancelled when baseCtx was cancelled")
	}

	// And the handler must unwind: the stub also returns on ctx.Done(), so the
	// token is delivered and the handler completes via the 200 proxy path.
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after baseCtx cancellation")
	}
}
