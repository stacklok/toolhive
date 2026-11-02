// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
)

// These acceptance tests drive the URL elicitation answer end to end: a
// BackendClient decorator (the embedder's seam) returns
// vmcp.URLElicitationRequiredError from tools/call, and vMCP renders the
// fallback for every client that cannot receive a URL elicitation. The
// Legacy client is a real go-sdk v1.8 client.

const (
	urlElicitationID      = "github-connect"
	urlElicitationURL     = "https://connect.example.com/github?state=opaque"
	urlElicitationMessage = "Reconnect your GitHub account to continue"
	urlElicitationHost    = "connect.example.com"
)

// urlElicitingBackendClient refuses tools/call with the URL elicitation
// answer while answering is true, and otherwise delegates to the real client.
type urlElicitingBackendClient struct {
	vmcp.BackendClient
	answering *atomic.Bool
	answer    *vmcp.URLElicitationRequiredError
}

func (c *urlElicitingBackendClient) CallTool(
	ctx context.Context, target *vmcp.BackendTarget, toolName string, arguments map[string]any,
	meta map[string]any, paramHeaders map[string]string,
) (*vmcp.ToolCallResult, error) {
	if c.answering.Load() {
		return nil, c.answer
	}
	return c.BackendClient.CallTool(ctx, target, toolName, arguments, meta, paramHeaders)
}

func newURLAnswer(t *testing.T) *vmcp.URLElicitationRequiredError {
	t.Helper()
	e, err := vmcp.NewURLElicitation(urlElicitationID, urlElicitationURL, urlElicitationMessage)
	require.NoError(t, err)
	answer, err := vmcp.NewURLElicitationRequiredError(e)
	require.NoError(t, err)
	return answer
}

// newURLElicitingHandler builds a vMCP handler over a real backend whose
// BackendClient is decorated with urlElicitingBackendClient.
func newURLElicitingHandler(
	t *testing.T, backendURL string, answering *atomic.Bool, configure ...func(*server.Config),
) http.Handler {
	t.Helper()
	answer := newURLAnswer(t)
	return newRealTestHandlerWithBackendClient(t, backendURL, nil,
		func(inner vmcp.BackendClient) vmcp.BackendClient {
			return &urlElicitingBackendClient{BackendClient: inner, answering: answering, answer: answer}
		}, configure...)
}

func answeringFlag(v bool) *atomic.Bool {
	b := &atomic.Bool{}
	b.Store(v)
	return b
}

func callEcho(t *testing.T, cs *gosdk.ClientSession, toolName string, args map[string]any) (*gosdk.CallToolResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return cs.CallTool(ctx, &gosdk.CallToolParams{Name: toolName, Arguments: args})
}

func declaredCaps(caps map[string]any) map[string]any {
	return map[string]any{"_meta": map[string]any{"io.modelcontextprotocol/clientCapabilities": caps}}
}

func echoCallParams(caps map[string]any, extra map[string]any) map[string]any {
	params := declaredCaps(caps)
	params["name"] = "echo"
	params["arguments"] = map[string]any{"input": "hello"}
	for k, v := range extra {
		params[k] = v
	}
	return params
}

// TestIntegration_Modern_URLElicitation_UndeclaredFallsBack pins AC3.1: a
// Modern client that did not declare elicitation.url receives -32021 naming
// it, at HTTP 200, without the URL.
func TestIntegration_Modern_URLElicitation_UndeclaredFallsBack(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	ts := httptest.NewServer(newURLElicitingHandler(t, backendURL, answeringFlag(true)))
	t.Cleanup(ts.Close)

	for name, caps := range map[string]map[string]any{
		"elicitation {}":    {"elicitation": map[string]any{}},
		"form only":         {"elicitation": map[string]any{"form": map[string]any{}}},
		"no elicitation":    {},
		"sampling, not url": {"sampling": map[string]any{}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resp, decoded := postModern(t, ts.URL, "tools/call", echoCallParams(caps, nil), 9, "echo")
			raw, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			require.Equal(t, http.StatusOK, resp.StatusCode, "decoded: %+v", decoded)
			errObj, ok := decoded["error"].(map[string]any)
			require.True(t, ok, "decoded: %+v", decoded)
			assert.EqualValues(t, mcpparser.CodeMissingClientCapability, errObj["code"])
			assert.Contains(t, errObj["message"], `declare the "elicitation.url" client capability and retry`)
			assert.Equal(t, map[string]any{
				"requiredCapabilities": map[string]any{"elicitation": map[string]any{"url": map[string]any{}}},
			}, errObj["data"])
			assert.NotContains(t, string(raw), urlElicitationHost)
		})
	}
}

// legacyClient connects a go-sdk client pinned to 2025-11-25.
func legacyClient(t *testing.T, baseURL string) *gosdk.ClientSession {
	t.Helper()
	client := gosdk.NewClient(&gosdk.Implementation{Name: "url-elicitation-legacy", Version: "1.0.0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cs, err := client.Connect(ctx, &gosdk.StreamableClientTransport{Endpoint: baseURL + "/mcp"},
		&gosdk.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	require.Equal(t, "2025-11-25", cs.InitializeResult().ProtocolVersion)
	return cs
}

// callWhenRegistered retries a Legacy call until the session's tools are
// registered (registration completes asynchronously after initialize).
func callWhenRegistered(
	t *testing.T, cs *gosdk.ClientSession, toolName string, args map[string]any,
) *gosdk.CallToolResult {
	t.Helper()
	var res *gosdk.CallToolResult
	require.Eventually(t, func() bool {
		r, err := callEcho(t, cs, toolName, args)
		if err != nil {
			return false
		}
		res = r
		return true
	}, 10*time.Second, 50*time.Millisecond, "tool %q never became callable", toolName)
	return res
}

// TestIntegration_Legacy_URLElicitation_FallsBack pins AC3.2: a Legacy
// session receives an isError result whose text is the elicitation message,
// without the URL, through a direct tools/call and through the optimizer's
// call_tool meta-tool.
func TestIntegration_Legacy_URLElicitation_FallsBack(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)

	assertFallback := func(t *testing.T, res *gosdk.CallToolResult) {
		t.Helper()
		require.True(t, res.IsError, "%+v", res)
		require.Len(t, res.Content, 1)
		text, ok := res.Content[0].(*gosdk.TextContent)
		require.True(t, ok)
		assert.Equal(t, urlElicitationMessage, text.Text)
		raw, err := json.Marshal(res)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), urlElicitationHost)
	}

	t.Run("direct tools/call", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(newURLElicitingHandler(t, backendURL, answeringFlag(true)))
		t.Cleanup(ts.Close)

		cs := legacyClient(t, ts.URL)
		assertFallback(t, callWhenRegistered(t, cs, "echo", map[string]any{"input": "x"}))
	})

	t.Run("optimizer call_tool", func(t *testing.T) {
		t.Parallel()
		optFactory, cleanup, err := optimizer.NewOptimizerFactory(&optimizer.Config{})
		require.NoError(t, err)
		t.Cleanup(func() { _ = cleanup(context.Background()) })

		ts := httptest.NewServer(newURLElicitingHandler(t, backendURL, answeringFlag(true),
			func(cfg *server.Config) { cfg.OptimizerFactory = optFactory }))
		t.Cleanup(ts.Close)

		cs := legacyClient(t, ts.URL)
		res := callWhenRegistered(t, cs, "call_tool", map[string]any{
			"tool_name": "echo", "parameters": map[string]any{"input": "x"},
		})
		assertFallback(t, res)
		text := res.Content[0].(*gosdk.TextContent).Text
		assert.False(t, strings.HasPrefix(text, "call_tool failed"),
			"the inner tool's fallback must pass through the meta-tool unchanged")
	})
}
