// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"
)

func TestAdmissionParserEnvelopes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body, contentType string
		method                  string
		code                    int
	}{
		{name: "call_plain", body: `{"jsonrpc":"2.0","id":9007199254740993,"method":"tools/call","params":{"name":"echo"}}`, contentType: "text/plain", method: "tools/call"},
		{name: "notification_missing", body: `{"jsonrpc":"2.0","method":"notifications/initialized"}`, method: "notifications/initialized"},
		{name: "response_plain", body: `{"jsonrpc":"2.0","id":1,"result":{}}`, contentType: "text/plain"},
		{name: "error_response_missing", body: `{"jsonrpc":"2.0","error":{"code":-32603,"message":"backend error"}}`},
		{name: "malformed_json", body: `{"jsonrpc":`, contentType: "application/json", code: -32700},
		{name: "trailing_plain", body: `{"jsonrpc":"2.0","id":1,"method":"ping"} {}`, contentType: "text/plain", code: -32700},
		{name: "mixed_missing", body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"},"result":{}}`, code: -32600},
		{name: "id_only_json", body: `{"jsonrpc":"2.0","id":1}`, contentType: "application/json", code: -32600},
		{name: "duplicate_plain", body: `{"jsonrpc":"2.0","id":1,"method":"ping","method":"tools/call"}`, contentType: "text/plain", code: -32600},
		{name: "alias_missing", body: `{"jsonrpc":"2.0","id":1,"Method":"tools/call"}`, code: -32600},
		{name: "malformed_batch", body: `[{"jsonrpc":"2.0","id":1,"method":"tools/call"`, contentType: "application/json", code: -32700},
		{name: "batch_plain", body: `[{"jsonrpc":"2.0","id":1,"method":"tools/call"}]`, contentType: "text/plain", code: -32600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			handler := ParsingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				parsed := GetParsedMCPRequest(r.Context())
				if tc.method == "" {
					assert.Nil(t, parsed)
				} else {
					require.NotNil(t, parsed)
					assert.Equal(t, tc.method, parsed.Method)
					if tc.method == "tools/call" {
						assert.Equal(t, int64(9007199254740993), parsed.ID)
					} else {
						assert.Nil(t, parsed.ID)
					}
				}
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				assert.Equal(t, tc.body, string(body))
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(http.MethodPost, "/custom-rpc", strings.NewReader(tc.body))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if tc.code == 0 {
				assert.Equal(t, http.StatusNoContent, rec.Code)
				assert.Equal(t, 1, calls)
			} else {
				assert.Equal(t, http.StatusBadRequest, rec.Code)
				assert.Equal(t, 0, calls)
				rpc := ParseMCPResponse(rec.Body.Bytes())
				assert.True(t, rpc.HasError)
				assert.Equal(t, tc.code, rpc.ErrorCode)
			}
		})
	}
}

func TestAdmissionParserBodyFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		limited bool
		status  int
	}{
		{"reader_error", false, http.StatusBadRequest}, {"read_limit", true, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tc.limited {
				req.Body = http.MaxBytesReader(rec, io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","method":"ping"}`)), 8)
			} else {
				req.Body = io.NopCloser(iotest.ErrReader(errors.New("private-reader-detail")))
			}
			calls := 0
			ParsingMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ })).ServeHTTP(rec, req)
			assert.Equal(t, tc.status, rec.Code)
			assert.Zero(t, calls)
			assert.NotContains(t, rec.Body.String(), "private-reader-detail")
		})
	}
}

func TestAdmissionParserNormalizesBOM(t *testing.T) {
	t.Parallel()
	const body = `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", bytes.NewReader(append(bytes.Clone(UTF8BOM), body...)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "text/plain")
	calls := 0
	ParsingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		actual, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, body, string(actual))
		assert.EqualValues(t, len(body), r.ContentLength)
		assert.Equal(t, "text/plain", r.Header.Get("Content-Type"))
		require.NotNil(t, r.GetBody)
		replay, err := r.GetBody()
		require.NoError(t, err)
		defer replay.Close()
		actual, err = io.ReadAll(replay)
		require.NoError(t, err)
		assert.Equal(t, body, string(actual))
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(httptest.NewRecorder(), req)
	assert.Equal(t, 1, calls)
}

func TestAdmissionBeforeToolOverride(t *testing.T) {
	t.Parallel()
	for _, contentType := range []string{"text/plain", ""} {
		for _, mixed := range []bool{false, true} {
			name := contentType + "/valid"
			if mixed {
				name = contentType + "/mixed"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				mapping, err := NewToolCallMappingMiddleware(WithToolsOverride("backend-tool", "public-tool", ""))
				require.NoError(t, err)
				body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"public-tool"}`
				if mixed {
					body += `,"result":{}`
				}
				body += `}`
				called := false
				next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					called = true
					forwarded, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					assert.Contains(t, string(forwarded), `"name":"backend-tool"`)
					w.WriteHeader(http.StatusNoContent)
				})
				req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
				if contentType != "" {
					req.Header.Set("Content-Type", contentType)
				}
				rec := httptest.NewRecorder()
				// Mapping precedes parsing in the real chain; it must not erase
				// the result member and turn invalid input into an admitted call.
				mapping(ParsingMiddleware(next)).ServeHTTP(rec, req)
				if mixed {
					assert.Equal(t, http.StatusBadRequest, rec.Code)
					assert.False(t, called)
				} else {
					assert.Equal(t, http.StatusNoContent, rec.Code)
					assert.True(t, called)
				}
			})
		}
	}
}

func TestAdmissionDecodeMessageExactIDs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw  string
		want any
	}{
		{`9007199254740993`, int64(9007199254740993)},
		{`9223372036854775807`, int64(9223372036854775807)},
		{`-9223372036854775808`, int64(-9223372036854775808)},
		{`"request-1"`, "request-1"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			msg, err := DecodeMessage([]byte(`{"jsonrpc":"2.0","id":` + tc.raw + `,"result":{}}`))
			require.NoError(t, err)
			response, ok := msg.(*jsonrpc2.Response)
			require.True(t, ok)
			assert.Equal(t, tc.want, response.ID.Raw())
		})
	}
}
