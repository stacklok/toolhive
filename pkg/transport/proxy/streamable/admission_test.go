// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"
)

func TestAdmissionStreamableClientResponses(t *testing.T) {
	t.Parallel()
	for _, contentType := range []string{"text/plain", ""} {
		t.Run(contentType, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			t.Cleanup(cancel)
			forwarded := make(chan *jsonrpc2.Response, 1)
			proxy, _, stop := startProxyWithBackend(t, func(msg jsonrpc2.Message) {
				if response, ok := msg.(*jsonrpc2.Response); ok {
					select {
					case forwarded <- response:
					case <-ctx.Done():
					}
				}
			})
			t.Cleanup(stop)
			target := "http://" + proxy.Address() + StreamableHTTPEndpoint
			send := func(body, sid string) *http.Response {
				t.Helper()
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(body))
				require.NoError(t, err)
				if contentType != "" {
					req.Header.Set("Content-Type", contentType)
				}
				req.Header.Set("Mcp-Session-Id", sid)
				resp, err := http.DefaultClient.Do(req)
				require.NoError(t, err)
				_, err = io.Copy(io.Discard, resp.Body)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				return resp
			}
			init := send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`, "")
			require.Equal(t, http.StatusOK, init.StatusCode)
			sid := init.Header.Get("Mcp-Session-Id")
			require.NotEmpty(t, sid)
			for _, payload := range []string{
				`"result":{"content":[{"type":"text","text":"answer"}]}`,
				`"result":[1,"answer"]`, `"result":"answer"`, `"result":true`, `"result":42`, `"result":null`,
				`"error":{"code":-32603,"message":"client error","data":{"detail":"exact"}}`,
			} {
				body := `{"jsonrpc":"2.0","id":9007199254740993,` + payload + `}`
				resp := send(body, sid)
				require.Equal(t, http.StatusAccepted, resp.StatusCode, body)
				select {
				case msg := <-forwarded:
					require.Equal(t, int64(9007199254740993), msg.ID.Raw())
					encoded, err := jsonrpc2.EncodeMessage(msg)
					require.NoError(t, err)
					assert.JSONEq(t, body, string(encoded))
				case <-ctx.Done():
					t.Fatal("client response did not reach backend")
				}
			}
		})
	}
}

func TestAdmissionStandaloneStreamable(t *testing.T) {
	t.Parallel()
	// Modern requests need no persistent session at this standalone forwarding boundary.
	valid := modernBody(1, "tools/call")
	for _, tc := range []struct {
		name, body, contentType string
		status                  int
	}{
		{"malformed_plain", `{"jsonrpc":`, "text/plain", http.StatusBadRequest},
		{"ambiguous_missing", strings.Replace(valid, `"jsonrpc":"2.0"`, `"jsonrpc":"2.0","Method":"ping"`, 1), "", http.StatusBadRequest},
		{"mixed_json", strings.Replace(valid, `"jsonrpc":"2.0"`, `"jsonrpc":"2.0","result":{}`, 1), "application/json", http.StatusBadRequest},
		{"batch_missing", `[` + valid + `]`, "", http.StatusBadRequest},
		{"valid_plain", valid, "text/plain", http.StatusOK},
		{"valid_missing", valid, "", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			proxy, _, stop := startProxyWithBackend(t, func(jsonrpc2.Message) { calls.Add(1) })
			t.Cleanup(stop)
			target := "http://" + proxy.Address() + StreamableHTTPEndpoint
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(tc.body))
			require.NoError(t, err)
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			assert.Equal(t, tc.status, resp.StatusCode, "body: %s", body)
			if tc.status == http.StatusOK {
				assert.EqualValues(t, 1, calls.Load())
				assert.Contains(t, string(body), `"ok":true`)
			} else {
				assert.Zero(t, calls.Load(), "rejected input must not reach stdio")
			}
		})
	}
}
