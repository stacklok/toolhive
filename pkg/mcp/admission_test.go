// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
		id                      any
		code                    int
	}{
		{name: "call_plain", body: `{"jsonrpc":"2.0","id":9007199254740993,"method":"tools/call","params":{"name":"echo"}}`, contentType: "text/plain", method: "tools/call", id: int64(9007199254740993)},
		{name: "params_omitted", body: `{"jsonrpc":"2.0","id":"request-1","method":"ping"}`, method: "ping", id: "request-1"},
		{name: "params_object", body: `{"jsonrpc":"2.0","id":0,"method":"ping","params":{}}`, method: "ping", id: int64(0)},
		{name: "params_null", body: `{"jsonrpc":"2.0","id":1,"method":"ping","params":null}`, code: -32600},
		{name: "params_array", body: `{"jsonrpc":"2.0","id":1,"method":"ping","params":[]}`, code: -32600},
		{name: "id_null", body: `{"jsonrpc":"2.0","id":null,"method":"ping"}`, code: -32600},
		{name: "id_fractional", body: `{"jsonrpc":"2.0","id":1.5,"method":"ping"}`, code: -32600},
		{name: "id_decimal_integer", body: `{"jsonrpc":"2.0","id":1.0,"method":"ping"}`, code: -32600},
		{name: "id_exponent", body: `{"jsonrpc":"2.0","id":1e0,"method":"ping"}`, code: -32600},
		{name: "response_params_null", body: `{"jsonrpc":"2.0","id":1,"result":{},"params":null}`, code: -32600},
		{name: "response_params_array", body: `{"jsonrpc":"2.0","id":1,"result":{},"params":[]}`, code: -32600},
		{name: "response_id_null", body: `{"jsonrpc":"2.0","id":null,"result":{}}`, code: -32600},
		{name: "response_id_fractional", body: `{"jsonrpc":"2.0","id":1.5,"result":{}}`, code: -32600},
		{name: "response_id_exponent", body: `{"jsonrpc":"2.0","id":1e0,"result":{}}`, code: -32600},
		{name: "error_id_null", body: `{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"client error"}}`, code: -32600},
		{name: "error_id_fractional", body: `{"jsonrpc":"2.0","id":1.5,"error":{"code":-32603,"message":"client error"}}`, code: -32600},
		{name: "error_id_exponent", body: `{"jsonrpc":"2.0","id":1e0,"error":{"code":-32603,"message":"client error"}}`, code: -32600},
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
					assert.Equal(t, tc.id, parsed.ID)
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

//nolint:paralleltest // captures the process-global slog logger
func TestAdmissionParserBodyFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		reason string
		status int
	}{
		{"reader_error", errors.New("reader-detail-marker"), "read_error", http.StatusBadRequest},
		{"read_limit", nil, "body_too_large", http.StatusRequestEntityTooLarge},
		{"wrapped_read_limit", fmt.Errorf("reader-detail-marker: %w", &http.MaxBytesError{Limit: 8}),
			"body_too_large", http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			t.Cleanup(func() { slog.SetDefault(previous) })

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			const body = `{"jsonrpc":"2.0","id":"id-marker","method":"body-marker"}`
			if tc.err == nil {
				req.Body = http.MaxBytesReader(rec, io.NopCloser(strings.NewReader(body)), 8)
			} else {
				req.Body = io.NopCloser(io.MultiReader(strings.NewReader(body), iotest.ErrReader(tc.err)))
			}
			calls := 0
			ParsingMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ })).ServeHTTP(rec, req)
			assert.Equal(t, tc.status, rec.Code)
			assert.Zero(t, calls)
			for _, marker := range []string{"reader-detail-marker", "body-marker", "id-marker"} {
				assert.NotContains(t, rec.Body.String(), marker)
				assert.NotContains(t, logs.String(), marker)
			}
			var entry map[string]any
			require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
			assert.NotEmpty(t, entry["time"])
			delete(entry, "time")
			assert.Equal(t, map[string]any{
				"level": "WARN", "msg": "rejected unreadable MCP request body", "reason": tc.reason,
			}, entry)
		})
	}
}

//nolint:paralleltest // captures the process-global slog logger
func TestAdmissionDecodeMessageLogs(t *testing.T) {
	for _, tc := range []struct {
		name, body, reason string
	}{
		{"parse", `{"id":"id-marker","method":"body-marker",`, "parse"},
		{"batch", `[{"jsonrpc":"2.0","id":"id-marker","method":"body-marker"}]`, "batch"},
		{"invalid_request", `{"jsonrpc":"2.0","id":"id-marker","method":"body-marker","result":{}}`, "invalid_request"},
		{"invalid_error", `{"jsonrpc":"2.0","id":"id-marker","error":{"code":null,"message":"error-marker"}}`, "invalid_request"},
		{"valid_request", `{"jsonrpc":"2.0","id":"id-marker","method":"body-marker"}`, ""},
		{"valid_error_without_id", `{"jsonrpc":"2.0","error":{"code":-32603,"message":"error-marker"}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			t.Cleanup(func() { slog.SetDefault(previous) })

			msg, err := DecodeMessage([]byte(tc.body))
			if tc.reason == "" {
				require.NoError(t, err)
				require.NotNil(t, msg)
				assert.Empty(t, logs.String())
				return
			}
			require.Error(t, err)
			assert.Nil(t, msg)
			for _, marker := range []string{tc.body, err.Error(), "id-marker", "body-marker", "error-marker"} {
				assert.NotContains(t, logs.String(), marker)
			}
			var entry map[string]any
			require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
			assert.NotEmpty(t, entry["time"])
			delete(entry, "time")
			assert.Equal(t, map[string]any{
				"level": "WARN", "msg": "rejected invalid MCP JSON-RPC message", "reason": tc.reason,
			}, entry)
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
		{`0`, int64(0)},
		{`""`, ""},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			for _, envelope := range []struct {
				name, fields string
			}{
				{"request", `"method":"ping"`},
				{"result", `"result":{}`},
				{"error", `"error":{"code":-32603,"message":"client error"}`},
			} {
				t.Run(envelope.name, func(t *testing.T) {
					t.Parallel()
					msg, err := DecodeMessage([]byte(`{"jsonrpc":"2.0","id":` + tc.raw + `,` + envelope.fields + `}`))
					require.NoError(t, err)
					if envelope.name == "request" {
						request, ok := msg.(*jsonrpc2.Request)
						require.True(t, ok)
						assert.Equal(t, tc.want, request.ID.Raw())
					} else {
						response, ok := msg.(*jsonrpc2.Response)
						require.True(t, ok)
						assert.Equal(t, tc.want, response.ID.Raw())
					}
				})
			}
		})
	}
}
