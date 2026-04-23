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

func TestCheckLoopback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		addr    string
		wantErr bool
	}{
		{"127.0.0.1:14000", false},
		{"localhost:14000", false},
		{"[::1]:14000", false},
		{"0.0.0.0:14000", true},
		{"192.168.1.1:14000", true},
		{"10.0.0.1:14000", true},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			t.Parallel()
			err := checkLoopback(tt.addr)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
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
	var receivedAuth string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer gateway.Close()

	cfg := &llm.Config{
		GatewayURL: gateway.URL,
		Proxy:      llm.ProxyConfig{ListenPort: freePort(t)},
	}
	p, err := New(cfg, &stubTokenSource{token: "fresh-token-abc"})
	require.NoError(t, err)
	defer p.listener.Close()

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer old-token")
	w := httptest.NewRecorder()
	p.handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "Bearer fresh-token-abc", receivedAuth,
		"gateway should receive the fresh token, not the original")
}

func TestHandler_StripsIncomingAuthorization(t *testing.T) {
	t.Parallel()
	var receivedAuth string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer gateway.Close()

	cfg := &llm.Config{
		GatewayURL: gateway.URL,
		Proxy:      llm.ProxyConfig{ListenPort: freePort(t)},
	}
	p, err := New(cfg, &stubTokenSource{token: "injected"})
	require.NoError(t, err)
	defer p.listener.Close()

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer user-supplied-token")
	w := httptest.NewRecorder()
	p.handler().ServeHTTP(w, req)

	assert.NotContains(t, receivedAuth, "user-supplied-token",
		"incoming Authorization must be stripped")
	assert.Equal(t, "Bearer injected", receivedAuth)
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

	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	p.handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Code)
}

// startTestProxy starts the proxy against the given gateway URL using a real
// TCP listener and returns the proxy's base URL. The proxy is stopped when
// t.Cleanup runs.
func startTestProxy(t *testing.T, gatewayURL string) string {
	t.Helper()
	cfg := &llm.Config{
		GatewayURL: gatewayURL,
		Proxy:      llm.ProxyConfig{ListenPort: freePort(t)},
	}
	p, err := New(cfg, &stubTokenSource{token: "test-token"})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() { _ = p.Start(ctx) }()

	// Wait for Serve to begin accepting connections.
	require.Eventually(t, func() bool {
		conn, dialErr := net.Dial("tcp", p.Addr())
		if dialErr != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 2*time.Second, 10*time.Millisecond, "proxy did not start in time")

	return "http://" + p.Addr()
}

func TestProxy_ForwardsPathQueryAndBody(t *testing.T) {
	t.Parallel()
	var (
		gotPath  string
		gotQuery string
		gotBody  []byte
		gotAuth  string
	)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotBody, _ = io.ReadAll(r.Body)
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer gateway.Close()

	proxyURL := startTestProxy(t, gateway.URL)

	body := strings.NewReader(`{"model":"gpt-4"}`)
	resp, err := http.Post(proxyURL+"/v1/chat/completions?stream=true", "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "/v1/chat/completions", gotPath)
	assert.Equal(t, "stream=true", gotQuery)
	assert.JSONEq(t, `{"model":"gpt-4"}`, string(gotBody))
	assert.Equal(t, "Bearer test-token", gotAuth)
}

func TestProxy_PassesThroughSSE(t *testing.T) {
	t.Parallel()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

	proxyURL := startTestProxy(t, gateway.URL)

	resp, err := http.Get(proxyURL + "/v1/chat/completions")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(got), "data: {\"id\":\"1\"}")
	assert.Contains(t, string(got), "data: [DONE]")
}

func TestProxy_PassesThroughErrorResponses(t *testing.T) {
	t.Parallel()
	for _, statusCode := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusInternalServerError} {
		statusCode := statusCode
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			t.Parallel()
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "upstream error", statusCode)
			}))
			defer gateway.Close()

			proxyURL := startTestProxy(t, gateway.URL)

			resp, err := http.Get(proxyURL + "/v1/models")
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, statusCode, resp.StatusCode, "error response must pass through unmodified")
		})
	}
}
