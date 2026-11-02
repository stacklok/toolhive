// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
// vmcp.URLElicitationRequiredError from tools/call, and vMCP renders it per
// negotiated protocol. The Modern and Legacy clients are real go-sdk v1.8
// clients, so the tests also prove the rendered shapes are ones a conformant
// client acts on.

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

// recordedRequest is one HTTP request the recordingFront saw.
type recordedRequest struct {
	httpMethod string
	mcpMethod  string
	body       []byte
}

// recordingFront is the httptest server's handler. It records each request
// and forwards it to whichever vMCP handler is current, so a test can replace
// the vMCP instance between two requests from the same client.
type recordingFront struct {
	current atomic.Pointer[http.Handler]

	mu       sync.Mutex
	requests []recordedRequest
}

func newRecordingFront(t *testing.T, h http.Handler) (*recordingFront, string) {
	t.Helper()
	f := &recordingFront{}
	f.swap(h)
	ts := httptest.NewServer(f)
	t.Cleanup(ts.Close)
	return f, ts.URL
}

func (f *recordingFront) swap(h http.Handler) { f.current.Store(&h) }

func (f *recordingFront) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{
		httpMethod: r.Method, mcpMethod: r.Header.Get("Mcp-Method"), body: body,
	})
	f.mu.Unlock()
	(*f.current.Load()).ServeHTTP(w, r)
}

func (f *recordingFront) toolCalls() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var calls []recordedRequest
	for _, r := range f.requests {
		if r.mcpMethod == "tools/call" {
			calls = append(calls, r)
		}
	}
	return calls
}

func (f *recordingFront) httpMethods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	methods := make([]string, 0, len(f.requests))
	for _, r := range f.requests {
		methods = append(methods, r.httpMethod)
	}
	return methods
}

// urlModeClient connects a go-sdk Modern client declaring
// elicitation: {url: {}} whose elicitation handler calls onElicit.
func urlModeClient(
	t *testing.T, baseURL string, onElicit func(*gosdk.ElicitRequest) (*gosdk.ElicitResult, error),
) *gosdk.ClientSession {
	t.Helper()
	client := gosdk.NewClient(&gosdk.Implementation{Name: "url-elicitation-test", Version: "1.0.0"},
		&gosdk.ClientOptions{
			Capabilities: &gosdk.ClientCapabilities{
				Elicitation: &gosdk.ElicitationCapabilities{URL: &gosdk.URLElicitationCapabilities{}},
			},
			ElicitationHandler: func(_ context.Context, req *gosdk.ElicitRequest) (*gosdk.ElicitResult, error) {
				return onElicit(req)
			},
		})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cs, err := client.Connect(ctx, &gosdk.StreamableClientTransport{Endpoint: baseURL + "/mcp"}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	require.Equal(t, mcpparser.MCPVersionModern, cs.InitializeResult().ProtocolVersion,
		"the client must negotiate Modern, or this test exercises the Legacy path")
	return cs
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

var urlCaps = map[string]any{"elicitation": map[string]any{"url": map[string]any{}}}

// assertURLInputRequired asserts decoded is the input_required envelope the
// issue specifies for the fixture answer.
func assertURLInputRequired(t *testing.T, resp *http.Response, decoded map[string]any, wantID any) {
	t.Helper()
	require.Equal(t, http.StatusOK, resp.StatusCode, "decoded: %+v", decoded)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"),
		"input_required must arrive in a single JSON response, not a stream")
	assert.Empty(t, resp.Header.Get("Mcp-Session-Id"))
	assert.EqualValues(t, wantID, decoded["id"])
	result, ok := decoded["result"].(map[string]any)
	require.True(t, ok, "decoded: %+v", decoded)
	assert.Equal(t, "input_required", result["resultType"])
	assert.NotContains(t, result, "requestState")
	assert.Equal(t, map[string]any{
		urlElicitationID: map[string]any{
			"method": "elicitation/create",
			"params": map[string]any{"mode": "url", "message": urlElicitationMessage, "url": urlElicitationURL},
		},
	}, result["inputRequests"])
}

// TestIntegration_Modern_URLElicitation_InputRequired pins AC2.1: the answer
// reaches a Modern client that declared elicitation.url as an input_required
// envelope in one HTTP response, and a real go-sdk client delivers it to its
// elicitation handler.
func TestIntegration_Modern_URLElicitation_InputRequired(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)

	t.Run("wire envelope", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(newURLElicitingHandler(t, backendURL, answeringFlag(true)))
		t.Cleanup(ts.Close)

		resp, decoded := postModern(t, ts.URL, "tools/call", echoCallParams(urlCaps, nil), 41, "echo")
		assertURLInputRequired(t, resp, decoded, 41)
	})

	t.Run("go-sdk client delivers the URL elicitation", func(t *testing.T) {
		t.Parallel()
		answering := answeringFlag(true)
		front, baseURL := newRecordingFront(t, newURLElicitingHandler(t, backendURL, answering))

		var delivered atomic.Pointer[gosdk.ElicitParams]
		cs := urlModeClient(t, baseURL, func(req *gosdk.ElicitRequest) (*gosdk.ElicitResult, error) {
			delivered.Store(req.Params)
			// The user completed the connect page: the decorator stops
			// answering, so the client's retry completes.
			answering.Store(false)
			return &gosdk.ElicitResult{Action: "accept"}, nil
		})

		res, err := callEcho(t, cs, "echo", map[string]any{"input": "after connect"})
		require.NoError(t, err)
		require.False(t, res.IsError, "%+v", res)
		require.Len(t, res.Content, 1)
		text, ok := res.Content[0].(*gosdk.TextContent)
		require.True(t, ok)
		assert.Equal(t, "after connect", text.Text)

		got := delivered.Load()
		require.NotNil(t, got, "the elicitation handler must receive the URL elicitation")
		assert.Equal(t, "url", got.Mode)
		assert.Equal(t, urlElicitationURL, got.URL)
		assert.Equal(t, urlElicitationMessage, got.Message)
		assert.Empty(t, got.ElicitationID, "2026-07-28 URL mode carries no elicitationId")

		assert.Len(t, front.toolCalls(), 2, "one input_required round, then one retry")
		assert.NotContains(t, front.httpMethods(), http.MethodGet, "no standalone stream may be opened")
	})
}

// TestIntegration_Modern_URLElicitation_RetryIsStateless pins AC2.2: the
// retry is an ordinary tools/call carrying inputResponses, served by a fresh
// vMCP instance, and it either completes or receives a fresh input_required
// depending only on what the decorator returns.
func TestIntegration_Modern_URLElicitation_RetryIsStateless(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)

	t.Run("retry completes on a fresh instance once the decorator stops answering", func(t *testing.T) {
		t.Parallel()
		front, baseURL := newRecordingFront(t, newURLElicitingHandler(t, backendURL, answeringFlag(true)))
		fresh := newURLElicitingHandler(t, backendURL, answeringFlag(false))

		cs := urlModeClient(t, baseURL, func(*gosdk.ElicitRequest) (*gosdk.ElicitResult, error) {
			// Between the two rounds, replace the vMCP instance outright:
			// whatever the first instance held is gone.
			front.swap(fresh)
			return &gosdk.ElicitResult{Action: "accept"}, nil
		})

		res, err := callEcho(t, cs, "echo", map[string]any{"input": "retried"})
		require.NoError(t, err)
		require.False(t, res.IsError, "%+v", res)
		text, ok := res.Content[0].(*gosdk.TextContent)
		require.True(t, ok)
		assert.Equal(t, "retried", text.Text)

		calls := front.toolCalls()
		require.Len(t, calls, 2)
		var retry struct {
			Params map[string]any `json:"params"`
		}
		require.NoError(t, json.Unmarshal(calls[1].body, &retry))
		assert.Contains(t, retry.Params, "inputResponses", "the retry must carry the client's inputResponses")
		assert.NotContains(t, retry.Params, "requestState",
			"the round carried no requestState, so the retry must not echo one")
	})

	t.Run("retry receives a fresh input_required while the decorator still answers", func(t *testing.T) {
		t.Parallel()
		first := httptest.NewServer(newURLElicitingHandler(t, backendURL, answeringFlag(true)))
		t.Cleanup(first.Close)
		resp, decoded := postModern(t, first.URL, "tools/call", echoCallParams(urlCaps, nil), 1, "echo")
		assertURLInputRequired(t, resp, decoded, 1)

		fresh := httptest.NewServer(newURLElicitingHandler(t, backendURL, answeringFlag(true)))
		t.Cleanup(fresh.Close)
		resp, decoded = postModern(t, fresh.URL, "tools/call", echoCallParams(urlCaps, map[string]any{
			"inputResponses": map[string]any{urlElicitationID: map[string]any{"action": "accept"}},
		}), 2, "echo")
		assertURLInputRequired(t, resp, decoded, 2)
	})
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
