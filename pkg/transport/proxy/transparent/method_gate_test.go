// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/mcp"
)

func TestMethodGate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		stateless      bool
		protocolHeader string
		method         string
		expectGated    bool
	}{
		// stateless=true gates GET/HEAD/DELETE regardless of header
		{"stateless GET gated", true, "", http.MethodGet, true},
		{"stateless HEAD gated", true, "", http.MethodHead, true},
		{"stateless DELETE gated", true, "", http.MethodDelete, true},
		{"stateless POST passes", true, "", http.MethodPost, false},
		{"stateless with Modern header GET gated", true, mcp.MCPVersionModern, http.MethodGet, true},

		// stateless=false, Modern header gates GET/HEAD/DELETE
		{"Modern header GET gated", false, mcp.MCPVersionModern, http.MethodGet, true},
		{"Modern header HEAD gated", false, mcp.MCPVersionModern, http.MethodHead, true},
		{"Modern header DELETE gated", false, mcp.MCPVersionModern, http.MethodDelete, true},
		{"Modern header POST passes", false, mcp.MCPVersionModern, http.MethodPost, false},

		// stateless=false, no/other header: legacy behavior, nothing gated
		{"legacy GET passes", false, "", http.MethodGet, false},
		{"legacy HEAD passes", false, "", http.MethodHead, false},
		{"legacy DELETE passes", false, "", http.MethodDelete, false},
		{"legacy POST passes", false, "", http.MethodPost, false},
		{"other header GET passes", false, "2025-11-25", http.MethodGet, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var gotBody string
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				gotBody = string(body)
				w.WriteHeader(http.StatusOK)
			})

			p := &TransparentProxy{stateless: tc.stateless}
			handler := p.methodGate(inner)
			rec := httptest.NewRecorder()

			var body string
			if tc.method == http.MethodPost {
				body = "request-body-payload"
			}
			req := httptest.NewRequest(tc.method, "/", strings.NewReader(body))
			if tc.protocolHeader != "" {
				req.Header.Set("MCP-Protocol-Version", tc.protocolHeader)
			}

			handler.ServeHTTP(rec, req)

			if tc.expectGated {
				assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
				assert.Equal(t, "POST, OPTIONS", rec.Header().Get("Allow"))
			} else {
				assert.Equal(t, http.StatusOK, rec.Code)
				if tc.method == http.MethodPost {
					require.Equal(t, body, gotBody, "POST body must reach next handler intact")
				}
			}
		})
	}
}
