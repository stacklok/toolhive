// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// okHandler is a trivial inner handler that records whether it was called.
func newCallTracker() (http.Handler, *bool) {
	called := false
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	return h, &called
}

func TestMatchCORSOrigin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		requestOrigin  string
		allowed        []string
		expectedResult string
	}{
		{
			name:           "empty request origin returns empty",
			requestOrigin:  "",
			allowed:        []string{"http://localhost:6274"},
			expectedResult: "",
		},
		{
			name:           "exact match returns origin",
			requestOrigin:  "http://localhost:6274",
			allowed:        []string{"http://localhost:6274"},
			expectedResult: "http://localhost:6274",
		},
		{
			name:           "exact match among multiple allowed origins",
			requestOrigin:  "http://localhost:3000",
			allowed:        []string{"http://localhost:6274", "http://localhost:3000"},
			expectedResult: "http://localhost:3000",
		},
		{
			name:           "prefix match: scheme+host matches scheme+host+port",
			requestOrigin:  "http://localhost:6274",
			allowed:        []string{"http://localhost"},
			expectedResult: "http://localhost:6274",
		},
		{
			name:           "prefix match: https scheme",
			requestOrigin:  "https://localhost:9000",
			allowed:        []string{"https://localhost"},
			expectedResult: "https://localhost:9000",
		},
		{
			name:           "no false prefix match: partial scheme should not match",
			requestOrigin:  "http://localhost:6274",
			allowed:        []string{"http://local"},
			expectedResult: "",
		},
		{
			name:           "wildcard returns literal asterisk",
			requestOrigin:  "http://example.com",
			allowed:        []string{"*"},
			expectedResult: "*",
		},
		{
			name:           "non-matching origin returns empty",
			requestOrigin:  "http://evil.example.com",
			allowed:        []string{"http://localhost:6274"},
			expectedResult: "",
		},
		{
			name:           "https origin does not match http entry",
			requestOrigin:  "https://localhost:6274",
			allowed:        []string{"http://localhost:6274"},
			expectedResult: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := matchCORSOrigin(tc.requestOrigin, tc.allowed)
			assert.Equal(t, tc.expectedResult, result)
		})
	}
}

func TestCORS_EmptyOrigins_IsNoop(t *testing.T) {
	t.Parallel()

	inner, called := newCallTracker()
	mwFn := CORS(nil)(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Origin", "http://localhost:6274")

	mwFn.ServeHTTP(rec, req)

	assert.True(t, *called, "inner handler must be called when CORS is disabled")
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"), "no CORS header should be set")
}

func TestCORS_NonOptions_MatchingOrigin_AddsCORSHeaders(t *testing.T) {
	t.Parallel()

	inner, _ := newCallTracker()
	mwFn := CORS([]string{"http://localhost:6274"})(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Origin", "http://localhost:6274")

	mwFn.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "http://localhost:6274", rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, corsAllowedMethods, rec.Header().Get("Access-Control-Allow-Methods"))
	assert.Equal(t, corsAllowedHeaders, rec.Header().Get("Access-Control-Allow-Headers"))
	assert.Equal(t, corsExposedHeaders, rec.Header().Get("Access-Control-Expose-Headers"))
	assert.Contains(t, rec.Header().Get("Vary"), "Origin")
}

func TestCORS_NonOptions_NonMatchingOrigin_NoCORSHeaders(t *testing.T) {
	t.Parallel()

	inner, called := newCallTracker()
	mwFn := CORS([]string{"http://localhost:6274"})(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Origin", "http://evil.example.com")

	mwFn.ServeHTTP(rec, req)

	assert.True(t, *called, "inner handler must still be called for non-matching non-OPTIONS")
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"), "no CORS header for non-matching origin")
}

func TestCORS_Preflight_MatchingOrigin_Returns204WithHeaders(t *testing.T) {
	t.Parallel()

	inner, called := newCallTracker()
	mwFn := CORS([]string{"http://localhost:6274"})(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
	req.Header.Set("Origin", "http://localhost:6274")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Content-Type")

	mwFn.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code, "preflight must return 204 No Content")
	assert.False(t, *called, "inner handler must NOT be called for preflight")
	assert.Equal(t, "http://localhost:6274", rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, corsAllowedMethods, rec.Header().Get("Access-Control-Allow-Methods"))
	assert.Equal(t, corsAllowedHeaders, rec.Header().Get("Access-Control-Allow-Headers"))
	assert.Equal(t, corsMaxAge, rec.Header().Get("Access-Control-Max-Age"))
}

func TestCORS_Preflight_NonMatchingOrigin_Returns204NoHeaders(t *testing.T) {
	t.Parallel()

	inner, called := newCallTracker()
	mwFn := CORS([]string{"http://localhost:6274"})(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
	req.Header.Set("Origin", "http://evil.example.com")

	mwFn.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code, "OPTIONS always returns 204 when CORS is active")
	assert.False(t, *called, "inner handler must NOT be called for OPTIONS")
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"), "no CORS header for non-matching origin")
	assert.Empty(t, rec.Header().Get("Access-Control-Max-Age"))
}

func TestCORS_PrefixMatch_LocalhostAnyPort(t *testing.T) {
	t.Parallel()

	inner, _ := newCallTracker()
	// "http://localhost" should match http://localhost on any port
	mwFn := CORS([]string{"http://localhost"})(inner)

	origins := []string{
		"http://localhost:6274",
		"http://localhost:3000",
		"http://localhost:8080",
	}

	for _, origin := range origins {
		t.Run(origin, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req.Header.Set("Origin", origin)

			mwFn.ServeHTTP(rec, req)

			assert.Equal(t, origin, rec.Header().Get("Access-Control-Allow-Origin"),
				"prefix match must set concrete origin, not the entry")
		})
	}
}

func TestCORS_Wildcard_MatchesAnyOrigin(t *testing.T) {
	t.Parallel()

	inner, _ := newCallTracker()
	mwFn := CORS([]string{"*"})(inner)

	origins := []string{
		"http://localhost:6274",
		"http://example.com",
		"https://app.example.org",
	}

	for _, origin := range origins {
		t.Run(origin, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req.Header.Set("Origin", origin)

			mwFn.ServeHTTP(rec, req)

			assert.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"),
				"wildcard entry must return literal * not the request origin")
		})
	}
}

func TestCORS_NoOriginHeader_NoCORSHeaders(t *testing.T) {
	t.Parallel()

	inner, called := newCallTracker()
	mwFn := CORS([]string{"http://localhost:6274"})(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	// No Origin header (non-browser request)

	mwFn.ServeHTTP(rec, req)

	assert.True(t, *called, "non-browser requests must reach the backend")
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
}
