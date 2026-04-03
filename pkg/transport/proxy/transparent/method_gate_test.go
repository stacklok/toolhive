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
	}{
		{
			name:           "GET returns 405",
			method:         http.MethodGet,
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:           "HEAD returns 405",
			method:         http.MethodHead,
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:           "DELETE returns 405",
			method:         http.MethodDelete,
			expectedStatus: http.StatusMethodNotAllowed,
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
		})
	}
}
