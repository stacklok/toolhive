// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package usagemetrics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/transport/types/mocks"
)

func TestMiddleware_Handler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		mcpMethod       string
		expectIncrement bool
	}{
		{
			name:            "tool call increments counter",
			mcpMethod:       "tools/call",
			expectIncrement: true,
		},
		{
			name:            "non-tool call does not increment",
			mcpMethod:       "tools/list",
			expectIncrement: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			collector, _ := newTestCollector(t)

			// Create middleware with collector instance
			mw := NewMiddleware(collector)
			handler := mw.Handler()

			// Create a test request with MCP context
			req := httptest.NewRequest(http.MethodPost, "/messages", nil)

			// Add parsed MCP request to context
			parsedReq := &mcp.ParsedMCPRequest{
				Method:    tt.mcpMethod,
				IsRequest: true,
			}
			ctx := context.WithValue(req.Context(), mcp.MCPRequestContextKey, parsedReq)
			req = req.WithContext(ctx)

			// Record initial count
			initialCount := collector.GetCurrentCount()

			// Create response recorder
			rr := httptest.NewRecorder()

			// Create a test handler that just returns 200
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			// Wrap with middleware
			wrappedHandler := handler(testHandler)

			// Execute request
			wrappedHandler.ServeHTTP(rr, req)

			// Verify count
			expectedCount := initialCount
			if tt.expectIncrement {
				expectedCount++
			}
			assert.Equal(t, expectedCount, collector.GetCurrentCount())
		})
	}
}

func TestMiddleware_Close(t *testing.T) {
	t.Parallel()

	collector, _ := newTestCollector(t)
	middleware := NewMiddleware(collector)

	// Test that Close returns nil and shuts down collector
	assert.NoError(t, middleware.Close())
	assert.True(t, collector.shutdown.Load())
}

// profiledRunnerConfig is a runner configuration that describes its server.
type profiledRunnerConfig struct {
	*mocks.MockRunnerConfig
	profile Profile
}

func (c profiledRunnerConfig) UsageMetricsProfile() Profile { return c.profile }

func TestCreateMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      func(*gomock.Controller) types.RunnerConfig
		wantProfile *Profile
	}{
		{
			name: "runner config without a profile",
			config: func(ctrl *gomock.Controller) types.RunnerConfig {
				return mocks.NewMockRunnerConfig(ctrl)
			},
		},
		{
			name: "runner config that provides a profile",
			config: func(ctrl *gomock.Controller) types.RunnerConfig {
				return profiledRunnerConfig{
					MockRunnerConfig: mocks.NewMockRunnerConfig(ctrl),
					profile:          Profile{Transport: "stdio"},
				}
			},
			wantProfile: &Profile{Transport: "stdio"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockRunner := mocks.NewMockMiddlewareRunner(ctrl)
			mockRunner.EXPECT().GetConfig().Return(tt.config(ctrl))

			var captured *Middleware
			mockRunner.EXPECT().AddMiddleware(MiddlewareType, gomock.Any()).Do(func(_ string, mw types.Middleware) {
				typed, ok := mw.(*Middleware)
				require.True(t, ok, "expected *Middleware, got %T", mw)
				captured = typed
			})

			paramsJSON, err := json.Marshal(MiddlewareParams{})
			require.NoError(t, err)
			err = CreateMiddleware(&types.MiddlewareConfig{Type: MiddlewareType, Parameters: paramsJSON}, mockRunner)
			require.NoError(t, err)

			require.NotNil(t, captured)
			require.NotNil(t, captured.collector)
			// Stop the started collector against a local server.
			newReportServer(t).attach(captured.collector)
			t.Cleanup(func() { assert.NoError(t, captured.Close()) })

			if tt.wantProfile == nil {
				assert.Nil(t, captured.collector.profile)
				return
			}
			require.NotNil(t, captured.collector.profile)
			assert.Equal(t, *tt.wantProfile, captured.collector.profile())
		})
	}
}

// mcpCall describes one request sent through the parsing and usage metrics
// middleware chain.
type mcpCall struct {
	method string
	params string
	// target is the request path and query.
	target string
	// sessionHeader is sent as the Mcp-Session-Id request header.
	sessionHeader string
	// responseSession is set as the Mcp-Session-Id response header by the
	// inner handler, the way streamable HTTP servers assign sessions.
	responseSession string
}

func initializeParams(clientName string) string {
	return fmt.Sprintf(`{"protocolVersion":"2025-06-18","capabilities":{},`+
		`"clientInfo":{"name":%q,"version":"1.0.0"}}`, clientName)
}

// serveMCP sends calls in order through mcp.ParsingMiddleware and the usage
// metrics middleware, sharing one Middleware so session state carries over.
func serveMCP(t *testing.T, mw *Middleware, calls []mcpCall) {
	t.Helper()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := r.Header.Get("X-Test-Response-Session"); id != "" {
			w.Header().Set(sessionIDHeader, id)
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := mcp.ParsingMiddleware(mw.Handler()(inner))

	for i, call := range calls {
		target := call.target
		if target == "" {
			target = "/mcp"
		}
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, i+1, call.method, call.params)
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if call.sessionHeader != "" {
			req.Header.Set(sessionIDHeader, call.sessionHeader)
		}
		if call.responseSession != "" {
			req.Header.Set("X-Test-Response-Session", call.responseSession)
		}
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code, "call %d (%s): %s", i, call.method, rr.Body.String())
	}
}

func TestMiddleware_AttributesToolCallsToClients(t *testing.T) {
	t.Parallel()

	const toolCall = `{"name":"echo","arguments":{}}`

	tests := []struct {
		name  string
		calls []mcpCall
		want  map[string]int64
	}{
		{
			name: "streamable HTTP session from initialize response",
			calls: []mcpCall{
				{method: "initialize", params: initializeParams("claude-code"), responseSession: "s1"},
				{method: "tools/call", params: toolCall, sessionHeader: "s1"},
			},
			want: map[string]int64{"claude-code": 1},
		},
		{
			name: "SSE session from query parameter",
			calls: []mcpCall{
				{method: "initialize", params: initializeParams("Visual Studio Code"), target: "/messages?session_id=x"},
				{method: "tools/call", params: toolCall, target: "/messages?session_id=x"},
				{method: "tools/call", params: toolCall, target: "/messages?session_id=x"},
			},
			want: map[string]int64{"vscode": 2},
		},
		{
			name: "unknown session",
			calls: []mcpCall{
				{method: "initialize", params: initializeParams("claude-code"), responseSession: "s1"},
				{method: "tools/call", params: toolCall, sessionHeader: "other-session"},
				{method: "tools/call", params: toolCall},
			},
			want: map[string]int64{ClientUnknown: 2},
		},
		{
			name: "Modern request carries clientInfo in _meta",
			calls: []mcpCall{
				{
					method: "tools/call",
					params: `{"name":"echo","arguments":{},"_meta":{` +
						`"io.modelcontextprotocol/clientInfo":{"name":"cursor-vscode","version":"1.0.0"}}}`,
				},
			},
			want: map[string]int64{"cursor": 1},
		},
		{
			name: "Modern clientInfo wins over session",
			calls: []mcpCall{
				{method: "initialize", params: initializeParams("claude-code"), responseSession: "s1"},
				{
					method: "tools/call",
					params: `{"name":"echo","_meta":{` +
						`"io.modelcontextprotocol/clientInfo":{"name":"zed"}}}`,
					sessionHeader: "s1",
				},
			},
			want: map[string]int64{"zed": 1},
		},
		{
			name: "other methods are not counted",
			calls: []mcpCall{
				{method: "initialize", params: initializeParams("claude-code"), responseSession: "s1"},
				{method: "tools/list", params: `{}`, sessionHeader: "s1"},
				{method: "prompts/get", params: `{"name":"p"}`, sessionHeader: "s1"},
				{method: "resources/read", params: `{"uri":"file:///x"}`, sessionHeader: "s1"},
			},
			want: map[string]int64{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			collector, err := NewCollector()
			require.NoError(t, err) // never started or flushed

			serveMCP(t, NewMiddleware(collector), tt.calls)

			total, byClient := collector.snapshotCounts()
			assert.Equal(t, tt.want, byClient)
			var sum int64
			for _, n := range byClient {
				sum += n
			}
			assert.Equal(t, sum, total, "counter total must equal the per-client sum")
		})
	}
}

func TestMiddleware_NilCollectorPassesThrough(t *testing.T) {
	t.Parallel()

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	})
	handler := mcp.ParsingMiddleware(NewMiddleware(nil).Handler()(inner))

	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}`))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.True(t, called)
	assert.Equal(t, http.StatusTeapot, rr.Code)
	assert.NoError(t, NewMiddleware(nil).Close())
}
