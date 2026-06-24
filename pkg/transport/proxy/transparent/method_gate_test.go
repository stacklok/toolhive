// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStatelessMethodGate(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		name           string
		method         string
		expectedStatus int
		expectAllow    bool
	}{
		{
			name:           "GET returns 405 with Allow header",
			method:         http.MethodGet,
			expectedStatus: http.StatusMethodNotAllowed,
			expectAllow:    true,
		},
		{
			name:           "HEAD returns 405 with Allow header",
			method:         http.MethodHead,
			expectedStatus: http.StatusMethodNotAllowed,
			expectAllow:    true,
		},
		{
			name:           "DELETE returns 405 with Allow header",
			method:         http.MethodDelete,
			expectedStatus: http.StatusMethodNotAllowed,
			expectAllow:    true,
		},
		{
			name:           "POST is forwarded",
			method:         http.MethodPost,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "PUT is forwarded",
			method:         http.MethodPut,
			expectedStatus: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			handler := statelessMethodGate(inner)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, "/", nil)

			handler.ServeHTTP(rec, req)

			assert.Equal(t, tc.expectedStatus, rec.Code)
			if tc.expectAllow {
				assert.Equal(t, statelessAllowedMethods, rec.Header().Get("Allow"))
			}
		})
	}
}

// TestCORSAllowedMethodsMatchGate guards the invariant that the CORS preflight
// advertises exactly the methods the server actually accepts. A stateless proxy
// only allows POST/OPTIONS, so a browser must not be told it can preflight GET
// or DELETE and then have the real request 405.
func TestCORSAllowedMethodsMatchGate(t *testing.T) {
	t.Parallel()

	stateful := &TransparentProxy{stateless: false}
	assert.Equal(t, statefulAllowedMethods, stateful.corsAllowedMethods(),
		"stateful proxy must advertise the full method set")

	stateless := &TransparentProxy{stateless: true}
	assert.Equal(t, statelessAllowedMethods, stateless.corsAllowedMethods(),
		"stateless proxy must advertise only the methods the gate permits")

	// The stateless preflight must never advertise a method the gate rejects.
	assert.NotContains(t, stateless.corsAllowedMethods(), "GET")
	assert.NotContains(t, stateless.corsAllowedMethods(), "DELETE")
}
