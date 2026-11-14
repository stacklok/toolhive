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

// backendRequest is one request the stand-in backend received.
type backendRequest struct {
	method string
	body   string
}

// TestTransparentProxyRejectsNonPOSTBodies proves, through the middleware chain
// the runner installs in front of a real transparent proxy, that:
//   - an authorized POST still reaches the backend;
//   - a request carrying a body on any method other than an exact POST is
//     rejected with 400 and never reaches the backend;
//   - a bodyless GET and DELETE still reach the backend unchanged.
//
// The non-POST-with-body cases fail on the pre-fix parser (the body is forwarded
// unparsed) and pass with the fix. A backend that answers a JSON-RPC message
// regardless of the HTTP method stands in for a method-lenient MCP server.
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
	var received []backendRequest
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = append(received, backendRequest{method: r.Method, body: string(body)})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"EXECUTED"}]}}`))
	})

	proxy := startTransparentProxyForMethodTest(t, runConfig, backend)
	client := &http.Client{Timeout: 5 * time.Second}

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

	deniedCall := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"admin_tool","arguments":{}}}`
	allowedCall := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"weather","arguments":{}}}`

	// A denied POST is authorized and rejected (403) before the backend.
	deniedResp := do(http.MethodPost, deniedCall)
	assert.Equal(t, http.StatusForbidden, deniedResp.StatusCode, "a denied POST must be rejected by authorization")
	drain(deniedResp)

	// An allowed POST is authorized and reaches the backend.
	allowedResp := do(http.MethodPost, allowedCall)
	assert.Equal(t, http.StatusOK, allowedResp.StatusCode, "an authorized POST must reach the backend")
	drain(allowedResp)

	// Every non-POST method carrying a body is rejected with 400.
	for _, method := range []string{"post", "Post", http.MethodPut, http.MethodPatch, http.MethodDelete} {
		resp := do(method, deniedCall)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
			"method %q carrying a body must be rejected with 400", method)
		drain(resp)
	}

	// Bodyless GET and DELETE pass through to the backend unchanged.
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		resp := do(method, "")
		assert.NotEqual(t, http.StatusBadRequest, resp.StatusCode,
			"a bodyless %s must not be rejected by the body check", method)
		drain(resp)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, req := range received {
		assert.NotContains(t, req.body, "admin_tool",
			"no denied or non-POST request must reach the backend (saw %s)", req.method)
	}
	assert.Equal(t, 1, countMatching(received, func(r backendRequest) bool {
		return r.method == http.MethodPost && strings.Contains(r.body, "weather")
	}), "exactly the authorized POST must reach the backend")
	assert.True(t, hasMatching(received, func(r backendRequest) bool {
		return r.method == http.MethodGet && r.body == ""
	}), "a bodyless GET must reach the backend")
	assert.True(t, hasMatching(received, func(r backendRequest) bool {
		return r.method == http.MethodDelete && r.body == ""
	}), "a bodyless DELETE must reach the backend")
}

func countMatching(reqs []backendRequest, pred func(backendRequest) bool) int {
	n := 0
	for _, r := range reqs {
		if pred(r) {
			n++
		}
	}
	return n
}

func hasMatching(reqs []backendRequest, pred func(backendRequest) bool) bool {
	return countMatching(reqs, pred) > 0
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
