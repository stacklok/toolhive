// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package headerforward

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCaptureMiddleware verifies the header capture middleware that reads
// allowlisted incoming request headers and carries them on a request-scoped
// context value for the per-session backend client to forward.
func TestCaptureMiddleware(t *testing.T) {
	t.Parallel()

	// runCapture builds the middleware chain, fires a GET /, and returns the
	// forwarded-headers map the terminal handler observed in the request context,
	// plus whether any context value was set at all.
	runCapture := func(t *testing.T, allowlist []string, reqHeaders map[string]string) (map[string]string, bool) {
		t.Helper()

		var got map[string]string
		var valueSet bool
		terminal := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got = ForwardedHeadersFromContext(r.Context())
			_, valueSet = r.Context().Value(forwardedHeadersKey{}).(map[string]string)
		})

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		for k, v := range reqHeaders {
			req.Header.Set(k, v)
		}
		CaptureMiddleware(allowlist)(terminal).ServeHTTP(httptest.NewRecorder(), req)
		return got, valueSet
	}

	t.Run("allowlisted_header_captured_into_context", func(t *testing.T) {
		t.Parallel()
		got, _ := runCapture(t, []string{"X-Api-Key"}, map[string]string{"X-Api-Key": "secret-value"})
		require.NotNil(t, got)
		assert.Equal(t, "secret-value", got["X-Api-Key"])
	})

	t.Run("non_allowlisted_header_absent", func(t *testing.T) {
		t.Parallel()
		got, _ := runCapture(t, []string{"X-Api-Key"},
			map[string]string{"X-Api-Key": "val", "X-Secret-Admin": "should-not-appear"})
		require.NotNil(t, got)
		_, exists := got["X-Secret-Admin"]
		assert.False(t, exists, "non-allowlisted header must not be captured")
	})

	t.Run("empty_allowlist_is_noop", func(t *testing.T) {
		t.Parallel()
		got, valueSet := runCapture(t, nil, map[string]string{"X-Api-Key": "value"})
		assert.Nil(t, got)
		assert.False(t, valueSet, "no context value should be set for an empty allowlist")
	})

	t.Run("no_matching_header_sets_no_context_value", func(t *testing.T) {
		t.Parallel()
		// Allowlist present but the request carries none of the listed headers.
		got, valueSet := runCapture(t, []string{"X-Api-Key"}, map[string]string{"X-Other": "v"})
		assert.Nil(t, got)
		assert.False(t, valueSet, "context value must be skipped when nothing is captured")
	})

	t.Run("header_matched_case_insensitively_canonical_form", func(t *testing.T) {
		t.Parallel()
		// Allowlist uses non-canonical casing; http.Header.Set canonicalises the request side.
		got, _ := runCapture(t, []string{"x-api-key"}, map[string]string{"X-Api-Key": "token123"})
		require.NotNil(t, got)
		assert.Equal(t, "token123", got["X-Api-Key"], "value must be accessible under the canonical key")
	})

	t.Run("no_panic_without_headers", func(t *testing.T) {
		t.Parallel()
		assert.NotPanics(t, func() {
			runCapture(t, []string{"X-Api-Key"}, nil)
		})
	})
}

// TestForwardedHeadersFromContext_Empty verifies the accessor returns nil when no
// capture middleware ran (e.g. the empty-allowlist no-op path).
func TestForwardedHeadersFromContext_Empty(t *testing.T) {
	t.Parallel()
	assert.Nil(t, ForwardedHeadersFromContext(t.Context()))
}
