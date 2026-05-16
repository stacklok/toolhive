// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package origin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/transport/types"
	typesmocks "github.com/stacklok/toolhive/pkg/transport/types/mocks"
)

// runMiddleware applies the middleware to a stub handler, issues a request
// with the given Origin header (skipped when empty), and returns the response.
func runMiddleware(
	t *testing.T,
	allowedOrigins []string,
	origin string,
) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	var nextCalled bool
	mw := createOriginHandler(allowedOrigins)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec, nextCalled
}

func TestOriginMiddleware_RequestPermitted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		allowedOrigins []string
		origin         string
	}{
		{
			name:           "empty allowlist disables middleware",
			allowedOrigins: nil,
			origin:         "http://evil.example",
		},
		{
			name:           "missing Origin header passes",
			allowedOrigins: []string{"http://localhost:8080"},
			origin:         "",
		},
		{
			name:           "exact match passes",
			allowedOrigins: []string{"http://localhost:8080"},
			origin:         "http://localhost:8080",
		},
		{
			name:           "match against second entry",
			allowedOrigins: []string{"http://localhost:8080", "http://127.0.0.1:8080"},
			origin:         "http://127.0.0.1:8080",
		},
		{
			name:           "case-insensitive scheme match (RFC 6454)",
			allowedOrigins: []string{"http://app.example.com"},
			origin:         "HTTP://app.example.com",
		},
		{
			name:           "case-insensitive host match (RFC 6454)",
			allowedOrigins: []string{"https://App.Example.com"},
			origin:         "https://app.example.com",
		},
		{
			name:           "mixed-case allowlist entry matches lowercase Origin",
			allowedOrigins: []string{"HTTPS://App.Example.com:443"},
			origin:         "https://app.example.com:443",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec, nextCalled := runMiddleware(t, tc.allowedOrigins, tc.origin)
			assert.True(t, nextCalled, "next handler must be invoked")
			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

func TestOriginMiddleware_RequestRejected(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		allowedOrigins []string
		origin         string
	}{
		{
			name:           "different host rejected",
			allowedOrigins: []string{"http://localhost:8080"},
			origin:         "http://evil.example",
		},
		{
			name:           "different port rejected (exact match required)",
			allowedOrigins: []string{"http://localhost:8080"},
			origin:         "http://localhost:9090",
		},
		{
			name:           "different scheme rejected",
			allowedOrigins: []string{"https://app.example.com"},
			origin:         "http://app.example.com",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec, nextCalled := runMiddleware(t, tc.allowedOrigins, tc.origin)
			assertForbiddenJSONRPC(t, rec, nextCalled)
		})
	}
}

func TestOriginMiddleware_MultipleOriginHeadersRejected(t *testing.T) {
	t.Parallel()

	var nextCalled bool
	mw := createOriginHandler([]string{"http://localhost:8080"})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Add("Origin", "http://localhost:8080")
	req.Header.Add("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assertForbiddenJSONRPC(t, rec, nextCalled)
}

// assertForbiddenJSONRPC validates that rec carries a 403 with a canonical
// JSON-RPC error body and that the inner handler was never invoked.
func assertForbiddenJSONRPC(t *testing.T, rec *httptest.ResponseRecorder, nextCalled bool) {
	t.Helper()
	assert.False(t, nextCalled, "next handler must NOT be invoked")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	body, err := io.ReadAll(rec.Body)
	require.NoError(t, err)
	var parsed struct {
		JSONRPC string `json:"jsonrpc"`
		Error   struct {
			Code    int64  `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		ID any `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &parsed))
	assert.Equal(t, "2.0", parsed.JSONRPC)
	assert.Equal(t, jsonRPCCodeInvalidRequest, parsed.Error.Code)
	assert.Equal(t, "Origin not allowed", parsed.Error.Message)
	assert.Nil(t, parsed.ID)
}

func TestCreateOriginMiddleware_PublicAPI(t *testing.T) {
	t.Parallel()
	mw := CreateOriginMiddleware([]string{"http://localhost:8080"})
	require.NotNil(t, mw)

	// Sanity-check it behaves the same as the internal constructor.
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestCreateMiddleware_Factory(t *testing.T) {
	t.Parallel()

	t.Run("valid parameters register middleware", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := typesmocks.NewMockMiddlewareRunner(ctrl)

		params := MiddlewareParams{AllowedOrigins: []string{"http://localhost:8080"}}
		cfg, err := types.NewMiddlewareConfig(MiddlewareType, params)
		require.NoError(t, err)

		runner.EXPECT().
			AddMiddleware(MiddlewareType, gomock.AssignableToTypeOf(&FactoryMiddleware{})).
			Times(1)

		require.NoError(t, CreateMiddleware(cfg, runner))
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := typesmocks.NewMockMiddlewareRunner(ctrl)

		cfg := &types.MiddlewareConfig{
			Type:       MiddlewareType,
			Parameters: json.RawMessage(`{not json}`),
		}

		err := CreateMiddleware(cfg, runner)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to unmarshal origin middleware parameters")
	})

	t.Run("empty allowlist still registers pass-through", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		runner := typesmocks.NewMockMiddlewareRunner(ctrl)

		cfg, err := types.NewMiddlewareConfig(MiddlewareType, MiddlewareParams{AllowedOrigins: nil})
		require.NoError(t, err)

		runner.EXPECT().
			AddMiddleware(MiddlewareType, gomock.AssignableToTypeOf(&FactoryMiddleware{})).
			Times(1)

		require.NoError(t, CreateMiddleware(cfg, runner))
	})
}

func TestFactoryMiddleware_Lifecycle(t *testing.T) {
	t.Parallel()

	mw := &FactoryMiddleware{handler: createOriginHandler([]string{"http://localhost:8080"})}
	require.NotNil(t, mw.Handler())
	require.NoError(t, mw.Close())
}

func TestResolveAllowedOrigins(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		host     string
		port     int
		explicit []string
		want     []string
	}{
		{
			name:     "explicit list wins over loopback derivation",
			host:     "127.0.0.1",
			port:     8080,
			explicit: []string{"https://app.example.com"},
			want:     []string{"https://app.example.com"},
		},
		{
			name: "loopback IPv4 auto-derives localhost defaults",
			host: "127.0.0.1",
			port: 8080,
			want: []string{
				"http://localhost:8080",
				"http://127.0.0.1:8080",
				"http://[::1]:8080",
			},
		},
		{
			name: "non-standard loopback IPv4 auto-derives defaults",
			host: "127.0.0.2",
			port: 8080,
			want: []string{
				"http://localhost:8080",
				"http://127.0.0.1:8080",
				"http://[::1]:8080",
			},
		},
		{
			name: "localhost string auto-derives defaults",
			host: "localhost",
			port: 8080,
			want: []string{
				"http://localhost:8080",
				"http://127.0.0.1:8080",
				"http://[::1]:8080",
			},
		},
		{
			name: "IPv6 loopback ::1 auto-derives defaults",
			host: "::1",
			port: 9090,
			want: []string{
				"http://localhost:9090",
				"http://127.0.0.1:9090",
				"http://[::1]:9090",
			},
		},
		{
			name: "IPv6 loopback in bracket form auto-derives defaults",
			host: "[::1]",
			port: 9090,
			want: []string{
				"http://localhost:9090",
				"http://127.0.0.1:9090",
				"http://[::1]:9090",
			},
		},
		{
			name: "non-loopback host with empty explicit returns nil",
			host: "0.0.0.0",
			port: 8080,
			want: nil,
		},
		{
			name: "public host with empty explicit returns nil",
			host: "192.168.1.10",
			port: 8080,
			want: nil,
		},
		{
			name: "garbage host returns nil",
			host: "not-a-host",
			port: 8080,
			want: nil,
		},
		{
			name: "zero port disables derivation",
			host: "127.0.0.1",
			port: 0,
			want: nil,
		},
		{
			name: "negative port disables derivation",
			host: "127.0.0.1",
			port: -1,
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ResolveAllowedOrigins(tc.host, tc.port, tc.explicit)
			assert.Equal(t, tc.want, got)
		})
	}
}
