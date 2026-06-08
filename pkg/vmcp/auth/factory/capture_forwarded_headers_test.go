// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgauth "github.com/stacklok/toolhive/pkg/auth"
)

// TestCaptureForwardedHeadersMiddleware verifies the header capture middleware that
// reads allowlisted incoming request headers and attaches them to the identity.
func TestCaptureForwardedHeadersMiddleware(t *testing.T) {
	t.Parallel()

	type captureResult struct {
		forwardedHeaders map[string]string
		identityPresent  bool
	}

	// runCapture builds the middleware chain, fires a GET /, and returns what the
	// terminal handler recorded.  reqHeaders is applied to the outgoing request;
	// identity is injected into context when non-nil (nil simulates no upstream auth).
	runCapture := func(
		t *testing.T,
		allowlist []string,
		identity *pkgauth.Identity,
		reqHeaders map[string]string,
	) captureResult {
		t.Helper()

		var got captureResult
		terminal := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			id, ok := pkgauth.IdentityFromContext(r.Context())
			got.identityPresent = ok
			if ok && id != nil {
				got.forwardedHeaders = id.ForwardedHeaders
			}
		})

		handler := http.Handler(captureForwardedHeadersMiddleware(allowlist)(terminal))
		if identity != nil {
			inject := func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					ctx := pkgauth.WithIdentity(r.Context(), identity)
					next.ServeHTTP(w, r.WithContext(ctx))
				})
			}
			handler = inject(handler)
		}

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		for k, v := range reqHeaders {
			req.Header.Set(k, v)
		}
		handler.ServeHTTP(httptest.NewRecorder(), req)
		return got
	}

	t.Run("allowlisted_header_present_appears_in_forwarded_headers", func(t *testing.T) {
		t.Parallel()
		identity := &pkgauth.Identity{PrincipalInfo: pkgauth.PrincipalInfo{Subject: "alice"}}
		got := runCapture(t, []string{"X-Api-Key"}, identity, map[string]string{"X-Api-Key": "secret-value"})
		require.True(t, got.identityPresent)
		require.NotNil(t, got.forwardedHeaders)
		assert.Equal(t, "secret-value", got.forwardedHeaders["X-Api-Key"])
	})

	t.Run("non_allowlisted_header_absent_from_forwarded_headers", func(t *testing.T) {
		t.Parallel()
		identity := &pkgauth.Identity{PrincipalInfo: pkgauth.PrincipalInfo{Subject: "alice"}}
		got := runCapture(t, []string{"X-Api-Key"}, identity,
			map[string]string{"X-Api-Key": "val", "X-Secret-Admin": "should-not-appear"})
		require.True(t, got.identityPresent)
		_, exists := got.forwardedHeaders["X-Secret-Admin"]
		assert.False(t, exists, "non-allowlisted header must not appear in ForwardedHeaders")
	})

	t.Run("empty_allowlist_identity_unchanged_no_clone", func(t *testing.T) {
		t.Parallel()
		original := &pkgauth.Identity{PrincipalInfo: pkgauth.PrincipalInfo{Subject: "alice"}}
		got := runCapture(t, nil, original, map[string]string{"X-Api-Key": "value"})
		require.True(t, got.identityPresent)
		assert.Nil(t, got.forwardedHeaders)
		assert.Nil(t, original.ForwardedHeaders) // original must not be mutated
	})

	t.Run("original_identity_not_mutated_after_capture", func(t *testing.T) {
		t.Parallel()
		original := &pkgauth.Identity{PrincipalInfo: pkgauth.PrincipalInfo{Subject: "bob"}}
		got := runCapture(t, []string{"X-Tenant-Id"}, original, map[string]string{"X-Tenant-Id": "acme-corp"})
		require.True(t, got.identityPresent)
		assert.Equal(t, "acme-corp", got.forwardedHeaders["X-Tenant-Id"])
		assert.Nil(t, original.ForwardedHeaders,
			"original identity pointer must not be mutated — middleware must clone")
	})

	t.Run("no_identity_in_context_no_panic_passes_through", func(t *testing.T) {
		t.Parallel()
		// nil identity → no inject middleware → no identity in context.
		assert.NotPanics(t, func() {
			got := runCapture(t, []string{"X-Api-Key"}, nil, map[string]string{"X-Api-Key": "val"})
			assert.False(t, got.identityPresent)
		})
	})

	t.Run("header_name_matched_case_insensitively_canonical_form", func(t *testing.T) {
		t.Parallel()
		// Allowlist uses non-canonical casing; Go's http.Header.Set canonicalises on the request side.
		original := &pkgauth.Identity{PrincipalInfo: pkgauth.PrincipalInfo{Subject: "carol"}}
		got := runCapture(t, []string{"x-api-key"}, original, map[string]string{"X-Api-Key": "token123"})
		require.True(t, got.identityPresent)
		assert.Equal(t, "token123", got.forwardedHeaders["X-Api-Key"],
			"header value must be accessible under the canonical key")
	})
}
