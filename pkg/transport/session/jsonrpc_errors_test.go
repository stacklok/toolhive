// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotFoundBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		requestID  any
		expectedID any // expected value after JSON round-trip
	}{
		{
			name:       "nil request ID",
			requestID:  nil,
			expectedID: nil,
		},
		{
			name:       "integer request ID",
			requestID:  42,
			expectedID: float64(42), // JSON numbers decode as float64
		},
		{
			name:       "string request ID",
			requestID:  "abc-123",
			expectedID: "abc-123",
		},
		{
			name:       "float64 request ID",
			requestID:  float64(7),
			expectedID: float64(7),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := NotFoundBody(tt.requestID)

			// Verify it's valid JSON
			var parsed map[string]any
			require.NoError(t, json.Unmarshal(body, &parsed))

			// Check JSON-RPC fields
			assert.Equal(t, "2.0", parsed["jsonrpc"])
			assert.Equal(t, tt.expectedID, parsed["id"])

			errObj, ok := parsed["error"].(map[string]any)
			require.True(t, ok, "error field should be an object")
			assert.Equal(t, float64(CodeSessionNotFound), errObj["code"])
			assert.Equal(t, MessageSessionNotFound, errObj["message"])

			// Verify the raw body contains the detection string that MCP clients check
			assert.Contains(t, string(body), `"code":-32001`)
		})
	}
}

func TestWriteNotFound(t *testing.T) {
	t.Parallel()

	w := httptest.NewRecorder()
	WriteNotFound(w, "req-1")

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Contains(t, w.Body.String(), `"code":-32001`)
	assert.Contains(t, w.Body.String(), `"id":"req-1"`)
}

func TestNotFoundResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		requestID any
		wantID    string
	}{
		{
			// A request with no id of its own: bodiless GET/DELETE, or a
			// notification. JSON-RPC requires a null id here.
			name:      "nil id renders null",
			requestID: nil,
			wantID:    `"id":null`,
		},
		{
			name:      "string id is echoed",
			requestID: "req-1",
			wantID:    `"id":"req-1"`,
		},
		{
			// The shape the transparent proxy passes: the raw bytes of the
			// incoming "id", forwarded verbatim so a numeric id stays numeric
			// rather than being coerced to a float or a string (#5945).
			name:      "raw numeric id is echoed verbatim",
			requestID: json.RawMessage(`42`),
			wantID:    `"id":42`,
		},
		{
			name:      "raw string id is echoed verbatim",
			requestID: json.RawMessage(`"abc"`),
			wantID:    `"id":"abc"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			resp := NotFoundResponse(req, tt.requestID)

			assert.Equal(t, http.StatusNotFound, resp.StatusCode)
			assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
			assert.Equal(t, req, resp.Request)

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			assert.Contains(t, string(body), `"code":-32001`)
			assert.Contains(t, string(body), tt.wantID)
			// ContentLength must match the body actually written, or a client
			// reading exactly that many bytes truncates or blocks.
			assert.Equal(t, int64(len(body)), resp.ContentLength)
		})
	}
}
