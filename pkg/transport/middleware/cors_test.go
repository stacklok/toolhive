// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runCORS applies the CORS middleware to a stub handler, issues a request with
// the given method and Origin header (skipped when empty), and returns the
// response and whether the next handler was invoked.
func runCORS(t *testing.T, allowedOrigins []string, allowedMethods, method, origin string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	var nextCalled bool
	mw := CORS(allowedOrigins, allowedMethods)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(method, "/mcp", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec, nextCalled
}

func TestCORS_PreflightAllowedOrigin(t *testing.T) {
	t.Parallel()
	rec, nextCalled := runCORS(t, []string{"http://localhost:6274"}, "POST, OPTIONS", http.MethodOptions, "http://localhost:6274")

	assert.False(t, nextCalled, "OPTIONS preflight must be intercepted, never forwarded")
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "http://localhost:6274", rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "POST, OPTIONS", rec.Header().Get("Access-Control-Allow-Methods"))
	// MCP-Protocol-Version and Last-Event-ID are not CORS-safelisted request
	// headers; omitting them breaks spec-compliant browser clients (the #5588
	// ship-blocker).
	assert.Equal(t, "Content-Type, Accept, Authorization, Mcp-Session-Id, MCP-Protocol-Version, Last-Event-ID",
		rec.Header().Get("Access-Control-Allow-Headers"))
	assert.Equal(t, "Mcp-Session-Id, MCP-Protocol-Version",
		rec.Header().Get("Access-Control-Expose-Headers"))
	assert.Equal(t, "86400", rec.Header().Get("Access-Control-Max-Age"))
	assert.Contains(t, rec.Header().Values("Vary"), "Origin", "Vary: Origin must be added so caches key on Origin")
}

func TestCORS_PreflightDisallowedOrigin(t *testing.T) {
	t.Parallel()
	rec, nextCalled := runCORS(t, []string{"http://localhost:6274"}, "POST, OPTIONS", http.MethodOptions, "http://evil.example")

	assert.False(t, nextCalled, "OPTIONS preflight must be intercepted, never forwarded")
	assert.Equal(t, http.StatusNoContent, rec.Code)
	// Fail closed: no ACAO means the browser rejects the follow-up request.
	assert.Equal(t, "", rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "", rec.Header().Get("Access-Control-Allow-Headers"))
	assert.Equal(t, "", rec.Header().Get("Access-Control-Expose-Headers"))
	assert.Equal(t, "", rec.Header().Get("Access-Control-Max-Age"))
}

func TestCORS_ActualRequestAllowedOrigin(t *testing.T) {
	t.Parallel()
	rec, nextCalled := runCORS(t, []string{"http://localhost:6274"}, "GET, POST, DELETE, OPTIONS", http.MethodGet, "http://localhost:6274")

	assert.True(t, nextCalled, "non-OPTIONS request must be forwarded")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "http://localhost:6274", rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "GET, POST, DELETE, OPTIONS", rec.Header().Get("Access-Control-Allow-Methods"))
	assert.Contains(t, rec.Header().Values("Vary"), "Origin")
	assert.Equal(t, "", rec.Header().Get("Access-Control-Max-Age"), "Max-Age is preflight-only")
}

func TestCORS_ActualRequestWithoutOrigin(t *testing.T) {
	t.Parallel()
	rec, nextCalled := runCORS(t, []string{"http://localhost:6274"}, "POST, OPTIONS", http.MethodPost, "")

	assert.True(t, nextCalled)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "", rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "", rec.Header().Get("Access-Control-Allow-Methods"))
}

func TestCORS_EmptyOriginsIsNoOp(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		method string
		origin string
	}{
		{http.MethodGet, "http://localhost:6274"},
		{http.MethodOptions, "http://localhost:6274"},
	} {
		rec, nextCalled := runCORS(t, nil, "POST, OPTIONS", tc.method, tc.origin)
		assert.True(t, nextCalled, "empty allowlist must be a pure passthrough for %s", tc.method)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "", rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORS_ExactMatchOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		allowed  []string
		origin   string
		expected string
	}{
		// The allowed entry must never act as a prefix: no CWE-942 style
		// scheme+host prefix matching at all.
		{"different port rejected", []string{"http://localhost:6274"}, "http://localhost:9999", ""},
		{"evil subdomain rejected", []string{"http://localhost"}, "http://localhost.evil.com", ""},
		{"evil subdomain vs port-bearing entry rejected", []string{"http://localhost:6274"}, "http://localhost.evil.com", ""},
		{"evil subdomain with port rejected", []string{"http://localhost"}, "http://localhost.evil.com:6274", ""},
		{"scheme difference rejected", []string{"http://localhost:6274"}, "https://localhost:6274", ""},
		{"exact match accepted", []string{"http://localhost:6274"}, "http://localhost:6274", "http://localhost:6274"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec, _ := runCORS(t, tc.allowed, "POST, OPTIONS", http.MethodGet, tc.origin)
			assert.Equal(t, tc.expected, rec.Header().Get("Access-Control-Allow-Origin"))
		})
	}
}

func TestCORS_CaseVariantMatches(t *testing.T) {
	t.Parallel()
	// Matching must be canonicalized identically to the origin middleware
	// (RFC 6454: scheme and host are ASCII-case-insensitive), so a case-variant
	// allowlist entry produces the same ACAO as a verbatim one.
	rec, _ := runCORS(t, []string{"HTTP://Localhost:6274"}, "POST, OPTIONS", http.MethodOptions, "http://localhost:6274")
	assert.Equal(t, "http://localhost:6274", rec.Header().Get("Access-Control-Allow-Origin"))
}

func TestValidateAllowedOrigins(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		origins []string
		want    []string
		wantErr bool
	}{
		{"nil input yields empty result", nil, []string{}, false},
		{"empty entries dropped", []string{"", "http://localhost:6274", "  "}, []string{"http://localhost:6274"}, false},
		{"trailing slash normalized", []string{"http://localhost:6274/"}, []string{"http://localhost:6274"}, false},
		{"valid entries preserved", []string{"https://my-mcp.example.com"}, []string{"https://my-mcp.example.com"}, false},
		{"missing scheme rejected", []string{"localhost:6274"}, nil, true},
		{"wildcard rejected", []string{"*"}, nil, true},
		{"garbage rejected", []string{"not a url"}, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateAllowedOrigins(tc.origins)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
