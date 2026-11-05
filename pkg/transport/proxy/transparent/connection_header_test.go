// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

// TestRejectConnectionNamedHeaders checks which Connection values the helper
// lets through. Anything other than a hop-by-hop name is rejected, because
// httputil.ReverseProxy would remove the named header after the middlewares
// ran (dropping injected headers), and removing it earlier would hide it from
// validators such as the Origin check.
func TestRejectConnectionNamedHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		connection []string
		wantStatus int
	}{
		{name: "no Connection header", wantStatus: http.StatusOK},
		{name: "keep-alive", connection: []string{"keep-alive"}, wantStatus: http.StatusOK},
		{name: "close", connection: []string{"close"}, wantStatus: http.StatusOK},
		{name: "upgrade", connection: []string{"Upgrade"}, wantStatus: http.StatusOK},
		{name: "h2c upgrade", connection: []string{"Upgrade, HTTP2-Settings"}, wantStatus: http.StatusOK},
		{name: "te, padded", connection: []string{" TE ,\tkeep-alive"}, wantStatus: http.StatusOK},
		{name: "empty tokens", connection: []string{" , ,"}, wantStatus: http.StatusOK},
		{name: "end-to-end header named", connection: []string{"X-Tenant-ID"}, wantStatus: http.StatusBadRequest},
		{name: "named after upgrade", connection: []string{"upgrade , x-tenant-id"}, wantStatus: http.StatusBadRequest},
		{name: "named on a second line", connection: []string{"keep-alive", "mcp-session-id"}, wantStatus: http.StatusBadRequest},
		{name: "malformed token", connection: []string{`"keep-alive" X-Tenant-ID`}, wantStatus: http.StatusBadRequest},
		// Exercises the log truncation path for long caller-supplied tokens.
		{name: "very long token", connection: []string{strings.Repeat("x", 4096)}, wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := rejectConnectionNamedHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/mcp", nil)
			for _, v := range tt.connection {
				req.Header.Add("Connection", v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

// TestRegression_ConnectionHeaderThroughProxy sends requests through a running
// TransparentProxy whose middlewares validate one header and inject others.
// Before the fix, a client could name an injected header in Connection and
// httputil.ReverseProxy removed it before the request reached the upstream
// (GHSA-7qm5-mcvg-c27h).
func TestRegression_ConnectionHeaderThroughProxy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		connection   []string
		origin       string
		wantStatus   int
		wantUpstream bool
	}{
		{name: "injected headers named, lower case, two lines", connection: []string{"keep-alive", " x-tenant-id , authorization"},
			wantStatus: http.StatusBadRequest},
		// The check must run before validating middlewares: removing Origin
		// instead of rejecting would let a disallowed Origin through.
		{name: "disallowed origin named", connection: []string{"Origin"}, origin: "https://disallowed.example",
			wantStatus: http.StatusBadRequest},
		{name: "keep-alive", connection: []string{"keep-alive"}, wantStatus: http.StatusOK, wantUpstream: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			upstreamURL, received := newRecordingUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			base := startInjectingProxy(t, upstreamURL)

			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/mcp",
				strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			for _, v := range tt.connection {
				req.Header.Add("Connection", v)
			}

			resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, tt.wantStatus, resp.StatusCode)

			got := received()
			if !tt.wantUpstream {
				assert.Nil(t, got, "a rejected request must not reach the upstream")
				return
			}
			require.NotNil(t, got, "upstream was not reached")
			assert.Equal(t, "Bearer operator-token", got.Get("Authorization"))
			assert.Equal(t, "team-a", got.Get("X-Tenant-Id"))
		})
	}
}

// TestRegression_ConnectionHeaderUpgradeStillWorks checks that a protocol
// upgrade still reaches the upstream with its Connection and Upgrade headers
// and with the injected headers.
func TestRegression_ConnectionHeaderUpgradeStillWorks(t *testing.T) {
	t.Parallel()

	upstreamURL, received := newRecordingUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		_ = buf.Flush()
	})
	base := startInjectingProxy(t, upstreamURL)

	// http.Client does not expose the raw upgrade exchange well, so speak
	// HTTP/1.1 on a plain connection.
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(t.Context(), "tcp", strings.TrimPrefix(base, "http://"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))

	_, err = conn.Write([]byte("GET /mcp HTTP/1.1\r\n" +
		"Host: proxy.example.com\r\n" +
		"Connection: keep-alive, Upgrade\r\n" +
		"Upgrade: websocket\r\n\r\n"))
	require.NoError(t, err)

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)

	got := received()
	require.NotNil(t, got, "upstream was not reached")
	assert.Equal(t, "Upgrade", got.Get("Connection"))
	assert.Equal(t, "websocket", got.Get("Upgrade"))
	assert.Equal(t, "team-a", got.Get("X-Tenant-Id"))
	assert.Equal(t, "Bearer operator-token", got.Get("Authorization"))
}

// newRecordingUpstream starts an upstream that records the headers of the last
// request it received and then calls respond. The returned function returns
// the recorded headers, or nil if no request arrived.
func newRecordingUpstream(t *testing.T, respond http.HandlerFunc) (string, func() http.Header) {
	t.Helper()

	var (
		mu       sync.Mutex
		received http.Header
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		received = r.Header.Clone()
		mu.Unlock()
		respond(w, r)
	}))
	t.Cleanup(upstream.Close)

	return upstream.URL, func() http.Header {
		mu.Lock()
		defer mu.Unlock()
		return received
	}
}

// startInjectingProxy starts a TransparentProxy in front of upstreamURL and
// returns its base URL. Its middlewares stand in for the real ones: the outer
// one rejects a disallowed Origin when present (like the Origin middleware),
// the inner one injects an upstream token and an operator header (like token
// injection and header forwarding).
func startInjectingProxy(t *testing.T, upstreamURL string) string {
	t.Helper()

	validateOrigin := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if o := r.Header.Get("Origin"); o != "" && o != "http://localhost:8080" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	injectHeaders := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Header.Set("Authorization", "Bearer operator-token")
			r.Header.Set("X-Tenant-Id", "team-a")
			next.ServeHTTP(w, r)
		})
	}

	p := NewTransparentProxy("127.0.0.1", 0, upstreamURL, nil, nil, nil, false, true, "streamable-http", nil, nil, "", false,
		types.NamedMiddleware{Name: "validate-origin", Function: validateOrigin},
		types.NamedMiddleware{Name: "inject-headers", Function: injectHeaders},
	)
	require.NoError(t, p.Start(t.Context()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, p.Stop(ctx))
	})
	return "http://" + p.ListenerAddr()
}
