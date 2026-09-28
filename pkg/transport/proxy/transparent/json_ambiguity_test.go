// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/transport/session"
)

//nolint:paralleltest // starts HTTP servers
func TestTransparentProxyRejectsAmbiguousJSONAndPreservesValidRequests(t *testing.T) {
	for _, transportType := range []string{"sse", "streamable-http"} {
		t.Run(transportType, func(t *testing.T) {
			var backendCalls atomic.Int32
			var forwardedBody, forwardedSID atomic.Value
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backendCalls.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, "failed to read request", http.StatusInternalServerError)
					return
				}
				forwardedBody.Store(body)
				forwardedSID.Store(r.Header.Get("Mcp-Session-Id"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{}}`))
			}))
			t.Cleanup(backend.Close)

			proxy := NewTransparentProxyWithOptions(
				"127.0.0.1", 0, backend.URL,
				nil, nil, nil,
				false, false, transportType,
				nil, nil, "", false,
				nil, // Admission must hold without the shared parser middleware.
			)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(func() {
				cancel()
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer stopCancel()
				_ = proxy.Stop(stopCtx)
			})
			require.NoError(t, proxy.Start(ctx))
			const sid = "owned-session"
			owned := session.NewProxySession(normalizeSessionID(sid))
			owned.SetMetadata(session.MetadataKeyIdentityBinding, "unauthenticated")
			require.NoError(t, proxy.sessionManager.AddSession(owned))
			proxyURL := fmt.Sprintf("http://%s/", proxy.listener.Addr().String())

			valid := `{"jsonrpc":"2.0", "id":2, "method":"tools/call", "params":{"name":"allowed","arguments":{"count":1e1000},"extension":{"keep":true}}}`
			for _, tc := range []struct {
				name, body, contentType string
				status                  int
			}{
				{"ambiguous_json", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"allowed","Name":"denied"}}`, "application/json", http.StatusBadRequest},
				{"malformed_plain", `{"jsonrpc":`, "text/plain", http.StatusBadRequest},
				{"mixed_missing", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"allowed"},"result":{}}`, "", http.StatusBadRequest},
				{"alias_plain", `{"jsonrpc":"2.0","id":1,"Method":"tools/call"}`, "text/plain", http.StatusBadRequest},
				{"batch_missing", `[` + valid + `]`, "", http.StatusBadRequest},
				{"valid_plain", valid, "text/plain", http.StatusOK},
				{"valid_missing", valid, "", http.StatusOK},
				{"response_object_plain", `{"jsonrpc":"2.0","id":9007199254740993,"result":{"content":[{"type":"text","text":"answer"}]}}`, "text/plain", http.StatusOK},
				{"response_array_missing", `{"jsonrpc":"2.0","id":-9223372036854775808,"result":[1,"answer"]}`, "", http.StatusOK},
				{"response_string_plain", `{"jsonrpc":"2.0","id":"server-call","result":"answer"}`, "text/plain", http.StatusOK},
				{"response_bool_missing", `{"jsonrpc":"2.0","id":1,"result":true}`, "", http.StatusOK},
				{"response_number_plain", `{"jsonrpc":"2.0","id":1,"result":42}`, "text/plain", http.StatusOK},
				{"response_null_missing", `{"jsonrpc":"2.0","id":1,"result":null}`, "", http.StatusOK},
				{"error_response_plain", `{"jsonrpc":"2.0","id":9007199254740993,"error":{"code":-32603,"message":"client error","data":{"detail":"exact"}}}`, "text/plain", http.StatusOK},
				{"error_response_missing", `{"jsonrpc":"2.0","id":"server-call","error":{"code":-32603,"message":"client error"}}`, "", http.StatusOK},
			} {
				t.Run(tc.name, func(t *testing.T) {
					before := backendCalls.Load()
					req, err := http.NewRequestWithContext(ctx, http.MethodPost, proxyURL, bytes.NewBufferString(tc.body))
					require.NoError(t, err)
					if tc.contentType != "" {
						req.Header.Set("Content-Type", tc.contentType)
					}
					req.Header.Set("Mcp-Session-Id", sid)
					response, err := http.DefaultClient.Do(req)
					require.NoError(t, err)
					drainAndClose(t, response)
					assert.Equal(t, tc.status, response.StatusCode)
					if tc.status == http.StatusOK {
						assert.Equal(t, before+1, backendCalls.Load())
						assert.Equal(t, []byte(tc.body), forwardedBody.Load())
						assert.Equal(t, sid, forwardedSID.Load())
					} else {
						assert.Equal(t, before, backendCalls.Load())
					}
				})
			}
		})
	}
}

func drainAndClose(t *testing.T, response *http.Response) {
	t.Helper()
	_, err := io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
}
