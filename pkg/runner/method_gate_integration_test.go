// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	"github.com/stacklok/toolhive/pkg/transport/proxy/transparent"
	statusesmocks "github.com/stacklok/toolhive/pkg/workloads/statuses/mocks"
)

// TestTransparentProxyRejectsNonPOSTBodies proves, through the middleware chain
// the runner installs in front of the transparent proxy, that a request carrying
// a body on any method other than an exact POST is rejected with 405 and never
// reaches the backend, while an ordinary POST is still authorized and forwarded.
// It fails on the pre-fix parser (the body is forwarded unparsed) and passes with
// the fix. A backend that dispatches a JSON-RPC message regardless of the HTTP
// method stands in for a method-lenient MCP server.
func TestTransparentProxyRejectsNonPOSTBodies(t *testing.T) {
	t.Parallel()

	authzConfig, err := authorizers.NewConfig(cedar.Config{
		Version: "1.0",
		Type:    cedar.ConfigType,
		Options: &cedar.ConfigOptions{
			Policies:     []string{`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`},
			EntitiesJSON: "[]",
		},
	})
	require.NoError(t, err)

	runConfig := NewRunConfig()
	runConfig.Name = "test-server"
	runConfig.AuthzConfig = authzConfig

	var mu sync.Mutex
	var backendMethods []string
	backend := newRecordingBackend(t, &mu, &backendMethods)

	proxy := startTransparentProxyForMethodTest(t, runConfig, backend)
	client := &http.Client{Timeout: 5 * time.Second}

	// A denied tools/call. On POST it is authorized (403). On any other method
	// carrying a body it must be rejected by the proxy before reaching the backend.
	deniedCall := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"admin_tool","arguments":{}}}`

	do := func(method, body string) *http.Response {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, reqErr := http.NewRequestWithContext(t.Context(), method, "http://"+proxy.ListenerAddr()+"/mcp", reader)
		require.NoError(t, reqErr)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, doErr := client.Do(req)
		require.NoError(t, doErr)
		return resp
	}
	drain := func(resp *http.Response) {
		t.Helper()
		_, _ = io.Copy(io.Discard, resp.Body)
		require.NoError(t, resp.Body.Close())
	}

	// Baseline: an exact POST is authorized (403 for the denied tool), proving the
	// backend and policy are wired correctly.
	postResp := do(http.MethodPost, deniedCall)
	assert.Equal(t, http.StatusForbidden, postResp.StatusCode, "an exact POST must still be authorized")
	drain(postResp)

	// Every non-POST method carrying the same body must be rejected with 405.
	for _, method := range []string{"post", "Post", http.MethodPut, http.MethodPatch, http.MethodDelete} {
		resp := do(method, deniedCall)
		assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode,
			"method %q carrying a body must be rejected", method)
		assert.Equal(t, "GET, POST, DELETE", resp.Header.Get("Allow"), "method %q", method)
		drain(resp)
	}

	mu.Lock()
	assert.Empty(t, backendMethods, "no denied non-POST request must reach the backend")
	mu.Unlock()
}

// newRecordingBackend returns a backend that records the method of every request
// that reaches it and answers any JSON-RPC message, whatever the HTTP method —
// standing in for an MCP server that routes methods leniently.
func newRecordingBackend(t *testing.T, mu *sync.Mutex, methods *[]string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*methods = append(*methods, r.Method)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"EXECUTED"}]}}`))
	})
}

// startTransparentProxyForMethodTest builds the runner middleware chain and puts a
// transparent proxy in front of backend, returning the started proxy.
func startTransparentProxyForMethodTest(
	t *testing.T, runConfig *RunConfig, backend http.Handler,
) *transparent.TransparentProxy {
	t.Helper()

	require.NoError(t, PopulateMiddlewareConfigs(runConfig))
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	runner := NewRunner(runConfig, statusesmocks.NewMockStatusManager(ctrl))
	for _, mwConfig := range runConfig.MiddlewareConfigs {
		factory, ok := runner.supportedMiddleware[mwConfig.Type]
		require.True(t, ok, "no factory for middleware type %q", mwConfig.Type)
		require.NoError(t, factory(&mwConfig, runner))
	}
	t.Cleanup(func() {
		for _, mw := range runner.middlewares {
			_ = mw.Close()
		}
	})

	backendServer := httptest.NewServer(backend)
	t.Cleanup(backendServer.Close)

	proxy := transparent.NewTransparentProxy(
		"127.0.0.1", 0, backendServer.URL, nil, nil, nil, false, false, "sse", nil, nil, "", false, runner.namedMiddlewares...,
	)
	proxyCtx, cancelProxy := context.WithCancel(t.Context())
	t.Cleanup(cancelProxy)
	require.NoError(t, proxy.Start(proxyCtx))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, proxy.Stop(ctx))
	})
	return proxy
}
