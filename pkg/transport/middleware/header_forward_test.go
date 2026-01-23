// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/types"
	typesmocks "github.com/stacklok/toolhive/pkg/transport/types/mocks"
)

func init() {
	logger.Initialize()
}

// executeMiddleware is a test helper that creates a request, applies the middleware, and returns the captured request.
func executeMiddleware(t *testing.T, mw func(http.Handler) http.Handler, existingHeaders map[string]string) *http.Request {
	t.Helper()
	var captured *http.Request
	handler := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		captured = r
	}))
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	for k, v := range existingHeaders {
		req.Header.Set(k, v)
	}
	handler.ServeHTTP(httptest.NewRecorder(), req)
	return captured
}

func TestCreateHeaderForwardMiddleware(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		configHeaders   map[string]string
		existingHeaders map[string]string
		expected        map[string]string
	}{
		{
			name:          "nil config returns no-op",
			configHeaders: nil,
			expected:      map[string]string{},
		},
		{
			name:          "empty config returns no-op",
			configHeaders: map[string]string{},
			expected:      map[string]string{},
		},
		{
			name:          "single header",
			configHeaders: map[string]string{"X-Custom": "value"},
			expected:      map[string]string{"X-Custom": "value"},
		},
		{
			name:          "multiple headers",
			configHeaders: map[string]string{"X-One": "1", "X-Two": "2"},
			expected:      map[string]string{"X-One": "1", "X-Two": "2"},
		},
		{
			name:          "canonicalizes lowercase names",
			configHeaders: map[string]string{"x-custom-header": "value"},
			expected:      map[string]string{"X-Custom-Header": "value"},
		},
		{
			name:            "overwrites existing header",
			configHeaders:   map[string]string{"X-Custom": "new"},
			existingHeaders: map[string]string{"X-Custom": "old"},
			expected:        map[string]string{"X-Custom": "new"},
		},
		{
			name:            "preserves other existing headers",
			configHeaders:   map[string]string{"X-Injected": "injected"},
			existingHeaders: map[string]string{"X-Existing": "existing"},
			expected:        map[string]string{"X-Injected": "injected", "X-Existing": "existing"},
		},
		{
			name:          "empty value is allowed",
			configHeaders: map[string]string{"X-Empty": ""},
			expected:      map[string]string{"X-Empty": ""},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mw, err := CreateHeaderForwardMiddleware(tc.configHeaders)
			require.NoError(t, err)
			captured := executeMiddleware(t, mw, tc.existingHeaders)
			for k, v := range tc.expected {
				assert.Equal(t, v, captured.Header.Get(k), "header %s", k)
			}
		})
	}
}

func TestCreateHeaderForwardMiddleware_RestrictedHeaders(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		header string
	}{
		{name: "Host", header: "Host"},
		{name: "Connection", header: "Connection"},
		{name: "Keep-Alive", header: "Keep-Alive"},
		{name: "Te", header: "Te"},
		{name: "Trailer", header: "Trailer"},
		{name: "Upgrade", header: "Upgrade"},
		{name: "Http2-Settings", header: "Http2-Settings"},
		{name: "Proxy-Authorization", header: "Proxy-Authorization"},
		{name: "Proxy-Authenticate", header: "Proxy-Authenticate"},
		{name: "Proxy-Connection", header: "Proxy-Connection"},
		{name: "Transfer-Encoding", header: "Transfer-Encoding"},
		{name: "Content-Length", header: "Content-Length"},
		{name: "Forwarded", header: "Forwarded"},
		{name: "X-Forwarded-For", header: "X-Forwarded-For"},
		{name: "X-Forwarded-Host", header: "X-Forwarded-Host"},
		{name: "X-Forwarded-Proto", header: "X-Forwarded-Proto"},
		{name: "X-Real-Ip", header: "X-Real-Ip"},
		{name: "lowercase variant", header: "x-forwarded-for"},
		{name: "mixed case variant", header: "content-LENGTH"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := CreateHeaderForwardMiddleware(map[string]string{tc.header: "value"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "is restricted and cannot be configured for forwarding")
		})
	}
}

func TestCreateHeaderForwardMiddleware_AuthorizationAllowed(t *testing.T) {
	t.Parallel()
	mw, err := CreateHeaderForwardMiddleware(map[string]string{"Authorization": "Bearer token"})
	require.NoError(t, err)
	captured := executeMiddleware(t, mw, nil)
	assert.Equal(t, "Bearer token", captured.Header.Get("Authorization"))
}

func TestCreateMiddleware(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		params  json.RawMessage
		wantErr bool
	}{
		{
			name:    "valid params",
			params:  mustMarshal(t, HeaderForwardMiddlewareParams{AddHeaders: map[string]string{"X-Key": "val"}}),
			wantErr: false,
		},
		{
			name:    "empty headers gives no-op",
			params:  mustMarshal(t, HeaderForwardMiddlewareParams{AddHeaders: map[string]string{}}),
			wantErr: false,
		},
		{
			name:    "invalid JSON params",
			params:  json.RawMessage(`{not json`),
			wantErr: true,
		},
		{
			name:    "restricted header returns error",
			params:  mustMarshal(t, HeaderForwardMiddlewareParams{AddHeaders: map[string]string{"Host": "evil.com"}}),
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			runner := typesmocks.NewMockMiddlewareRunner(ctrl)

			cfg := &types.MiddlewareConfig{
				Type:       HeaderForwardMiddlewareName,
				Parameters: tc.params,
			}

			if !tc.wantErr {
				runner.EXPECT().AddMiddleware(HeaderForwardMiddlewareName, gomock.Any()).Times(1)
			}

			err := CreateMiddleware(cfg, runner)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}
