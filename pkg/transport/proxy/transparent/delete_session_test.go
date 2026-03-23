// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeleteSessionCleanup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		seedSession      bool   // whether to pre-populate a session in the manager
		sessionID        string // the session ID to seed and/or reference (must be a valid UUID)
		deleteHeader     string // value of Mcp-Session-Id header on the DELETE request ("" = omit header)
		deleteStatusCode int    // status code the upstream returns for the DELETE
		expectSession    bool   // whether the session should exist after the DELETE
	}{
		{
			name:             "DELETE with 200 removes session",
			seedSession:      true,
			sessionID:        "cccccccc-0001-0001-0001-000000000001",
			deleteHeader:     "cccccccc-0001-0001-0001-000000000001",
			deleteStatusCode: http.StatusOK,
			expectSession:    false,
		},
		{
			name:             "DELETE with 404 removes session",
			seedSession:      true,
			sessionID:        "cccccccc-0002-0002-0002-000000000002",
			deleteHeader:     "cccccccc-0002-0002-0002-000000000002",
			deleteStatusCode: http.StatusNotFound,
			expectSession:    false,
		},
		{
			name:             "DELETE with 500 does not remove session",
			seedSession:      true,
			sessionID:        "cccccccc-0003-0003-0003-000000000003",
			deleteHeader:     "cccccccc-0003-0003-0003-000000000003",
			deleteStatusCode: http.StatusInternalServerError,
			expectSession:    true,
		},
		{
			name:             "DELETE without Mcp-Session-Id header does nothing",
			seedSession:      true,
			sessionID:        "cccccccc-0004-0004-0004-000000000004",
			deleteHeader:     "",
			deleteStatusCode: http.StatusOK,
			expectSession:    true,
		},
		{
			name:             "DELETE for non-existent session does not error",
			seedSession:      false,
			sessionID:        "cccccccc-0005-0005-0005-000000000005",
			deleteHeader:     "cccccccc-0005-0005-0005-000000000005",
			deleteStatusCode: http.StatusOK,
			expectSession:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, nil, false, false, "streamable-http", nil, nil, "", false)

			// Seed the session directly in the manager if needed.
			if tt.seedSession {
				require.NoError(t, p.sessionManager.AddWithID(tt.sessionID))
				_, ok := p.sessionManager.Get(tt.sessionID)
				require.True(t, ok, "session should exist after seeding")
			}

			// Create a target server that returns the desired status code for DELETE.
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.deleteStatusCode)
			}))
			defer target.Close()

			targetURL, _ := url.Parse(target.URL)
			proxy := createBasicProxy(p, targetURL)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodDelete, target.URL, nil)
			if tt.deleteHeader != "" {
				req.Header.Set("Mcp-Session-Id", tt.deleteHeader)
			}
			proxy.ServeHTTP(rec, req)

			_, ok := p.sessionManager.Get(tt.sessionID)
			assert.Equal(t, tt.expectSession, ok,
				"session existence mismatch: want exists=%v, got exists=%v", tt.expectSession, ok)
		})
	}
}
