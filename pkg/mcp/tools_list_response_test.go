// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// eventStreamData returns the data of the events in body the way a client
// reads it: the value of each data line, without the space after the colon,
// joined with a newline.
func eventStreamData(t *testing.T, body string) string {
	t.Helper()
	var values []string
	for _, line := range strings.Split(body, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSuffix(line, "\r"), "data:"); ok {
			values = append(values, strings.TrimPrefix(value, " "))
		}
	}
	return strings.Join(values, "\n")
}

func TestNewListToolsMappingMiddleware_KeepsResultMembers(t *testing.T) {
	t.Parallel()

	// members are the members a tools/list result may carry beside its
	// tools; the id is an integer beyond float64 precision.
	const members = `"resultType":"complete","nextCursor":"page-2","cacheScope":"public","ttlMs":60000,` +
		`"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"example-server","version":"1.2.3"}}`
	const list = `{"jsonrpc":"2.0","id":12345678901234567890,"result":{` + members +
		`,"tools":[{"name":"allowed_tool","description":"Allowed"},{"name":"blocked_tool","description":"Blocked"}]}}`
	const filtered = `{"jsonrpc":"2.0","id":12345678901234567890,"result":{` + members +
		`,"tools":[{"description":"Allowed","name":"allowed_tool"}]}}`

	tests := []struct {
		name        string
		contentType string
		response    string
		// want is the body the client receives, or the data of the event
		// stream it receives.
		want string
	}{
		{
			name:        "JSON response",
			contentType: "application/json",
			response:    list,
			want:        filtered + "\n",
		},
		{
			name:        "event stream",
			contentType: "text/event-stream",
			response:    "event: message\ndata: " + list + "\n\n",
			want:        filtered,
		},
		{
			name:        "a list with the required members only gains none",
			contentType: "application/json",
			response:    `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"allowed_tool"},{"name":"blocked_tool"}]}}`,
			want:        `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"allowed_tool"}]}}` + "\n",
		},
		{
			// The shape reported in #6684: a backend on MCP 2026-07-28
			// answers tools/list over an event stream with resultType and
			// serverInfo, and the client needs both after filtering.
			name:        "filtered event stream keeps resultType",
			contentType: "text/event-stream",
			response: "event: message\ndata: " +
				`{"jsonrpc":"2.0","id":2,"result":{"resultType":"complete",` +
				`"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"github-mcp-server"}},` +
				`"tools":[{"name":"allowed_tool"},{"name":"blocked_tool"}]}}` + "\n\n",
			want: `{"jsonrpc":"2.0","id":2,"result":{"resultType":"complete",` +
				`"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"github-mcp-server"}},` +
				`"tools":[{"name":"allowed_tool"}]}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			middleware, err := NewListToolsMappingMiddleware(WithToolsFilter("allowed_tool"))
			require.NoError(t, err)

			handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(http.StatusOK)
				_, writeErr := w.Write([]byte(tt.response))
				require.NoError(t, writeErr)
			}))

			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", nil))

			got := recorder.Body.String()
			if tt.contentType == "text/event-stream" {
				got = eventStreamData(t, got)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEncodeToolsListResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		body  string
		tools []map[string]any
		want  string
	}{
		{
			// The id is an integer beyond float64 precision, the tools come
			// before other members of the result, and _meta holds white
			// space inside its value.
			name: "members keep their bytes and their place",
			body: `{"jsonrpc":"2.0","id":12345678901234567890,"result":{"resultType":"complete",` +
				`"tools":[{"name":"b","description":"B"},{"name":"a"}],"nextCursor":"page-2","_meta":{"k": [1,` + "\n" + ` 2]}}}`,
			tools: []map[string]any{{"name": "a"}},
			want: `{"jsonrpc":"2.0","id":12345678901234567890,"result":{"resultType":"complete",` +
				`"tools":[{"name":"a"}],"nextCursor":"page-2","_meta":{"k": [1,` + "\n" + ` 2]}}}`,
		},
		{
			name:  "tools are encoded with their members sorted",
			body:  `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"a","description":"A","inputSchema":{"type":"object"}}]}}`,
			tools: []map[string]any{{"name": "a", "description": "A", "inputSchema": map[string]any{"type": "object"}}},
			want:  `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"description":"A","inputSchema":{"type":"object"},"name":"a"}]}}`,
		},
		{
			name:  "no tool kept",
			body:  `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"a"}],"nextCursor":"page-2"}}`,
			tools: []map[string]any{},
			want:  `{"jsonrpc":"2.0","id":1,"result":{"tools":[],"nextCursor":"page-2"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var response toolsListResponse
			require.NoError(t, json.Unmarshal([]byte(tt.body), &response))

			got, err := encodeToolsListResponse([]byte(tt.body), response, tt.tools)
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

func TestSSEDataLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
		want    []string
	}{
		{
			name:    "every kind of line break ends a data line",
			payload: "a\r\nb\rc",
			want:    []string{"a", "b", "c"},
		},
		{
			name:    "a value without a line break is one data line",
			payload: `{"a":1}`,
			want:    []string{`{"a":1}`},
		},
		{
			name:    "an empty line is an empty data line",
			payload: "{\"a\":\n\n1}",
			want:    []string{`{"a":`, "", "1}"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got []string
			for _, line := range sseDataLines([]byte(tt.payload)) {
				got = append(got, string(line))
			}
			assert.Equal(t, tt.want, got)
		})
	}
}
