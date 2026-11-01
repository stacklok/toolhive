// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/transport/proxy/transparent"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// freeTCPPort asks the OS for an unused port. The proxy needs a concrete port,
// and it does not expose its listener to this external test package.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

// startBackend serves handler on a plain http.Server. httptest.Server.Close
// calls CloseIdleConnections on http.DefaultTransport, which breaks requests
// that parallel tests in this package have in flight on that shared transport.
func startBackend(t *testing.T, handler http.Handler) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + l.Addr().String()
}

// denyNamedTool authorizes every tool except the one named deniedTool.
type denyNamedTool struct {
	deniedTool string
}

func (d denyNamedTool) AuthorizeWithJWTClaims(
	_ context.Context,
	_ authorizers.MCPFeature,
	_ authorizers.MCPOperation,
	resourceID string,
	_ map[string]interface{},
) (bool, error) {
	return resourceID != d.deniedTool, nil
}

func TestSSEStreamListAnswerIsFilteredThroughProxy(t *testing.T) {
	t.Parallel()

	const toolsList = `{"jsonrpc":"2.0","id":1,"result":{"tools":[` +
		`{"name":"weather","inputSchema":{"type":"object"}},` +
		`{"name":"secret","inputSchema":{"type":"object"}}]}}`
	const toolsCall = `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"ok"}],"isError":false}}`

	testCases := []struct {
		name         string
		event        string
		wantContains string
		wantAbsent   string
	}{
		{name: "tools list drops the denied tool", event: toolsList, wantContains: "weather", wantAbsent: "secret"},
		{name: "tool call result passes through", event: toolsCall, wantContains: `"content"`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			backendURL := startBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, "event: message\ndata: "+tc.event+"\n\n")
			}))

			authzMiddleware := types.NamedMiddleware{
				Name: "authz",
				Function: func(next http.Handler) http.Handler {
					return authz.Middleware(denyNamedTool{deniedTool: "secret"}, next, nil)
				},
			}
			port := freeTCPPort(t)
			p := transparent.NewTransparentProxy("127.0.0.1", port, backendURL, nil, nil, nil, false, false, "sse", nil, nil, "", false, authzMiddleware)
			require.NoError(t, p.Start(t.Context()))
			t.Cleanup(func() { _ = p.Stop(context.Background()) })
			time.Sleep(150 * time.Millisecond)

			// A dedicated transport keeps this request off http.DefaultTransport,
			// which other parallel tests in the package share.
			client := &http.Client{Transport: &http.Transport{}}
			t.Cleanup(client.CloseIdleConnections)
			resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/sse", port))
			require.NoError(t, err)
			t.Cleanup(func() { _ = resp.Body.Close() })
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			assert.Contains(t, string(body), tc.wantContains)
			if tc.wantAbsent != "" {
				assert.False(t, strings.Contains(string(body), tc.wantAbsent),
					"the denied tool must not reach the client through the proxy")
			}
		})
	}
}
