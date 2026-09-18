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

	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

//nolint:paralleltest // starts HTTP servers
func TestTransparentProxyRejectsAmbiguousJSONAndPreservesValidRequests(t *testing.T) {
	for _, transportType := range []string{"sse", "streamable-http"} {
		t.Run(transportType, func(t *testing.T) {
			var backendCalls atomic.Int32
			forwardedBodies := make(chan []byte, 1)
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backendCalls.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, "failed to read request", http.StatusInternalServerError)
					return
				}
				forwardedBodies <- body
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{}}`))
			}))
			t.Cleanup(backend.Close)

			proxy := NewTransparentProxyWithOptions(
				"127.0.0.1", 0, backend.URL,
				nil, nil, nil,
				false, false, transportType,
				nil, nil, "", false,
				[]types.NamedMiddleware{{Name: mcp.ParserMiddlewareType, Function: mcp.ParsingMiddleware}},
			)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(func() {
				cancel()
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer stopCancel()
				_ = proxy.Stop(stopCtx)
			})
			require.NoError(t, proxy.Start(ctx))
			proxyURL := fmt.Sprintf("http://%s/", proxy.listener.Addr().String())

			ambiguous := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"allowed","Name":"denied"}}`
			response := postJSON(ctx, t, proxyURL, ambiguous)
			assert.Equal(t, http.StatusBadRequest, response.StatusCode)
			drainAndClose(t, response)
			assert.Zero(t, backendCalls.Load())

			valid := `{"jsonrpc":"2.0", "id":2, "method":"tools/call", "params":{"name":"allowed","arguments":{"count":1e1000},"extension":{"keep":true}}}`
			response = postJSON(ctx, t, proxyURL, valid)
			assert.Equal(t, http.StatusOK, response.StatusCode)
			drainAndClose(t, response)
			assert.Equal(t, int32(1), backendCalls.Load())
			assert.Equal(t, []byte(valid), <-forwardedBodies)
		})
	}
}

func postJSON(ctx context.Context, t *testing.T, target, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewBufferString(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return response
}

func drainAndClose(t *testing.T, response *http.Response) {
	t.Helper()
	_, err := io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
}
