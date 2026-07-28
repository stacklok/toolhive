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
		wantIDKey  bool
		expectedID any // expected value after JSON round-trip, only checked when wantIDKey
	}{
		{
			name:      "nil request ID omits id key",
			requestID: nil,
			wantIDKey: false,
		},
		{
			name:      "raw null request ID omits id key",
			requestID: json.RawMessage(`null`),
			wantIDKey: false,
		},
		{
			name:      "nil RawMessage omits id key",
			requestID: json.RawMessage(nil),
			wantIDKey: false,
		},
		{
			name:       "integer request ID",
			requestID:  42,
			wantIDKey:  true,
			expectedID: float64(42), // JSON numbers decode as float64
		},
		{
			name:       "zero request ID is still present",
			requestID:  0,
			wantIDKey:  true,
			expectedID: float64(0),
		},
		{
			name:       "string request ID",
			requestID:  "abc-123",
			wantIDKey:  true,
			expectedID: "abc-123",
		},
		{
			name:       "float64 request ID",
			requestID:  float64(7),
			wantIDKey:  true,
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

			id, ok := parsed["id"]
			assert.Equal(t, tt.wantIDKey, ok, `"id" key presence`)
			if tt.wantIDKey {
				assert.Equal(t, tt.expectedID, id)
			}

			errObj, ok := parsed["error"].(map[string]any)
			require.True(t, ok, "error field should be an object")
			assert.Equal(t, float64(CodeSessionNotFound), errObj["code"])
			assert.Equal(t, MessageSessionNotFound, errObj["message"])

			// Verify the raw body contains the detection string that MCP clients check
			assert.Contains(t, string(body), `"code":-32001`)
		})
	}
}

func TestNotFoundBodyMarshalFallback(t *testing.T) {
	t.Parallel()

	// A channel is not JSON-marshalable, forcing json.Marshal to fail so the
	// hand-crafted fallback literal is exercised. Since requestID is non-nil
	// and not a json.RawMessage, HasJSONRPCID reports it as present, so the
	// fallback must still omit "id" per the marshal-failure branch, not echo it.
	body := NotFoundBody(make(chan int))

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))
	assert.Equal(t, "2.0", parsed["jsonrpc"])
	_, ok := parsed["id"]
	assert.False(t, ok, `fallback body must omit "id" key`)

	errObj, ok := parsed["error"].(map[string]any)
	require.True(t, ok, "error field should be an object")
	assert.Equal(t, float64(CodeSessionNotFound), errObj["code"])
	assert.Equal(t, MessageSessionNotFound, errObj["message"])
}

func TestHasJSONRPCID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		requestID any
		want      bool
	}{
		{name: "nil", requestID: nil, want: false},
		{name: "raw null", requestID: json.RawMessage(`null`), want: false},
		{name: "raw nil slice", requestID: json.RawMessage(nil), want: false},
		{name: "raw empty slice", requestID: json.RawMessage(""), want: false},
		{name: "raw numeric", requestID: json.RawMessage(`42`), want: true},
		{name: "raw string", requestID: json.RawMessage(`"abc"`), want: true},
		{name: "string", requestID: "abc-123", want: true},
		{name: "integer zero", requestID: 0, want: true},
		{name: "integer", requestID: 42, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, HasJSONRPCID(tt.requestID))
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
		wantIDKey bool
		wantID    string // only checked when wantIDKey
	}{
		{
			// A request with no id of its own: bodiless GET/DELETE, or a
			// notification. MCP narrows base JSON-RPC to omit the key here
			// rather than emit "id":null (schema/2025-11-25, HasJSONRPCID).
			name:      "nil id omits the id key",
			requestID: nil,
			wantIDKey: false,
		},
		{
			// The transparent proxy threads the incoming id through as raw
			// bytes; literal null must be treated the same as a missing id.
			name:      "raw null id omits the id key",
			requestID: json.RawMessage(`null`),
			wantIDKey: false,
		},
		{
			name:      "nil RawMessage omits the id key",
			requestID: json.RawMessage(nil),
			wantIDKey: false,
		},
		{
			name:      "string id is echoed",
			requestID: "req-1",
			wantIDKey: true,
			wantID:    `"id":"req-1"`,
		},
		{
			name:      "zero id is echoed, not treated as absent",
			requestID: 0,
			wantIDKey: true,
			wantID:    `"id":0`,
		},
		{
			// The shape the transparent proxy passes: the raw bytes of the
			// incoming "id", forwarded verbatim so a numeric id stays numeric
			// rather than being coerced to a float or a string (#5945).
			name:      "raw numeric id is echoed verbatim",
			requestID: json.RawMessage(`42`),
			wantIDKey: true,
			wantID:    `"id":42`,
		},
		{
			name:      "raw string id is echoed verbatim",
			requestID: json.RawMessage(`"abc"`),
			wantIDKey: true,
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

			var parsed map[string]any
			require.NoError(t, json.Unmarshal(body, &parsed))
			_, ok := parsed["id"]
			assert.Equal(t, tt.wantIDKey, ok, `"id" key presence`)
			if tt.wantIDKey {
				assert.Contains(t, string(body), tt.wantID)
			}

			// ContentLength must match the body actually written, or a client
			// reading exactly that many bytes truncates or blocks.
			assert.Equal(t, int64(len(body)), resp.ContentLength)
		})
	}
}
