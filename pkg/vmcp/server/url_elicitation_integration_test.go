// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
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
// it, at HTTP 400, with the elicitation message and without the URL. A real
// go-sdk client keeps its session across that 400.
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

			require.Equal(t, http.StatusBadRequest, resp.StatusCode, "decoded: %+v", decoded)
			errObj, ok := decoded["error"].(map[string]any)
			require.True(t, ok, "decoded: %+v", decoded)
			assert.EqualValues(t, mcpparser.CodeMissingClientCapability, errObj["code"])
			assert.Contains(t, errObj["message"], urlElicitationMessage)
			assert.Contains(t, errObj["message"], `declare the "elicitation.url" client capability and retry`)
			assert.Equal(t, map[string]any{
				"requiredCapabilities": map[string]any{"elicitation": map[string]any{"url": map[string]any{}}},
			}, errObj["data"])
			assert.NotContains(t, string(raw), urlElicitationHost)
		})
	}

	t.Run("go-sdk client session survives the 400", func(t *testing.T) {
		t.Parallel()
		answering := answeringFlag(true)
		ts := httptest.NewServer(newURLElicitingHandler(t, backendURL, answering))
		t.Cleanup(ts.Close)

		client := gosdk.NewClient(&gosdk.Implementation{Name: "no-url-mode", Version: "1.0.0"}, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t.Cleanup(cancel)
		cs, err := client.Connect(ctx, &gosdk.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = cs.Close() })
		require.Equal(t, mcpparser.MCPVersionModern, cs.InitializeResult().ProtocolVersion)

		_, err = callEcho(t, cs, "echo", map[string]any{"input": "x"})
		require.Error(t, err, "the -32021 must fail the call")
		assert.Contains(t, err.Error(), urlElicitationMessage)
		assert.NotContains(t, err.Error(), urlElicitationHost)

		// The same session serves the next call once the decorator stops
		// answering: the 400 failed one call, not the connection.
		answering.Store(false)
		res, err := callEcho(t, cs, "echo", map[string]any{"input": "same session"})
		require.NoError(t, err)
		require.False(t, res.IsError, "%+v", res)
		text, ok := res.Content[0].(*gosdk.TextContent)
		require.True(t, ok)
		assert.Equal(t, "same session", text.Text)
	})
}

// TestIntegration_Modern_URLElicitation_DeclineEndsTheLoop pins decline and
// cancel handling with a real go-sdk client: the user refuses the elicitation,
// the client retries with that response, the decorator answers again, and
// vMCP ends the loop with an isError result carrying the message and no URL,
// after one prompt.
func TestIntegration_Modern_URLElicitation_DeclineEndsTheLoop(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	for _, action := range []string{"decline", "cancel"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			front, baseURL := newRecordingFront(t, newURLElicitingHandler(t, backendURL, answeringFlag(true)))

			var prompts atomic.Int32
			cs := urlModeClient(t, baseURL, func(*gosdk.ElicitRequest) (*gosdk.ElicitResult, error) {
				prompts.Add(1)
				return &gosdk.ElicitResult{Action: action}, nil
			})

			res, err := callEcho(t, cs, "echo", map[string]any{"input": "x"})
			require.NoError(t, err)
			require.True(t, res.IsError, "%+v", res)
			require.Len(t, res.Content, 1)
			text, ok := res.Content[0].(*gosdk.TextContent)
			require.True(t, ok)
			assert.Equal(t, urlElicitationMessage, text.Text)
			raw, err := json.Marshal(res)
			require.NoError(t, err)
			assert.NotContains(t, string(raw), urlElicitationHost)

			assert.EqualValues(t, 1, prompts.Load(), "the user must be asked once")
			assert.Len(t, front.toolCalls(), 2, "one input_required round, then the refused retry")
		})
	}
}

// legacyClient connects a go-sdk client pinned to protocolVersion that
// declares caps (nil declares nothing).
func legacyClient(
	t *testing.T, baseURL, protocolVersion string, caps *gosdk.ClientCapabilities,
) *gosdk.ClientSession {
	t.Helper()
	client := gosdk.NewClient(&gosdk.Implementation{Name: "url-elicitation-legacy", Version: "1.0.0"},
		&gosdk.ClientOptions{Capabilities: caps})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cs, err := client.Connect(ctx, &gosdk.StreamableClientTransport{Endpoint: baseURL + "/mcp"},
		&gosdk.ClientSessionOptions{ProtocolVersion: protocolVersion})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	require.Equal(t, protocolVersion, cs.InitializeResult().ProtocolVersion)
	return cs
}

// legacyURLCaps declares elicitation: {url: {}}.
func legacyURLCaps() *gosdk.ClientCapabilities {
	return &gosdk.ClientCapabilities{
		Elicitation: &gosdk.ElicitationCapabilities{URL: &gosdk.URLElicitationCapabilities{}},
	}
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

// legacyCallErr retries a Legacy call until the session's tools are
// registered (registration completes asynchronously after initialize) and
// returns the call's JSON-RPC error.
func legacyCallErr(t *testing.T, cs *gosdk.ClientSession, toolName string, args map[string]any) error {
	t.Helper()
	var callErr error
	ok := assert.Eventually(t, func() bool {
		_, err := callEcho(t, cs, toolName, args)
		callErr = err
		var wireErr *jsonrpc.Error
		return errors.As(err, &wireErr) && wireErr.Code == gosdk.CodeURLElicitationRequired
	}, 10*time.Second, 50*time.Millisecond)
	require.True(t, ok, "tool %q never returned -32042; last error: %v", toolName, callErr)
	return callErr
}

// twoElicitationAnswer is an answer with two elicitations, so the tests can
// pin that data.elicitations keeps every one, in order.
func twoElicitationAnswer(t *testing.T) *vmcp.URLElicitationRequiredError {
	t.Helper()
	first, err := vmcp.NewURLElicitation(urlElicitationID, urlElicitationURL, urlElicitationMessage)
	require.NoError(t, err)
	second, err := vmcp.NewURLElicitation("jira-connect", "https://connect.example.com/jira", "Reconnect Jira")
	require.NoError(t, err)
	answer, err := vmcp.NewURLElicitationRequiredError(first, second)
	require.NoError(t, err)
	return answer
}

func newTwoElicitationHandler(t *testing.T, backendURL string, configure ...func(*server.Config)) http.Handler {
	t.Helper()
	answer := twoElicitationAnswer(t)
	return newRealTestHandlerWithBackendClient(t, backendURL, nil,
		func(inner vmcp.BackendClient) vmcp.BackendClient {
			return &urlElicitingBackendClient{BackendClient: inner, answering: answeringFlag(true), answer: answer}
		}, configure...)
}

// assertURLElicitationRequired asserts err is the MCP 2025-11-25
// URLElicitationRequiredError for twoElicitationAnswer.
func assertURLElicitationRequired(t *testing.T, err error) {
	t.Helper()
	var wireErr *jsonrpc.Error
	require.ErrorAs(t, err, &wireErr)
	assert.Equal(t, int64(gosdk.CodeURLElicitationRequired), wireErr.Code)
	assert.Equal(t, "URL elicitation required", wireErr.Message)

	var data struct {
		Elicitations []*gosdk.ElicitParams `json:"elicitations"`
	}
	require.NoError(t, json.Unmarshal(wireErr.Data, &data))
	require.Len(t, data.Elicitations, 2)
	for i, want := range []struct{ id, url, message string }{
		{urlElicitationID, urlElicitationURL, urlElicitationMessage},
		{"jira-connect", "https://connect.example.com/jira", "Reconnect Jira"},
	} {
		got := data.Elicitations[i]
		assert.Equal(t, "url", got.Mode)
		assert.Equal(t, want.id, got.ElicitationID)
		assert.Equal(t, want.url, got.URL)
		assert.Equal(t, want.message, got.Message)
	}
}

func newOptimizerConfig(t *testing.T) func(*server.Config) {
	t.Helper()
	optFactory, cleanup, err := optimizer.NewOptimizerFactory(&optimizer.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cleanup(context.Background()) })
	return func(cfg *server.Config) { cfg.OptimizerFactory = optFactory }
}

var optimizerEchoArgs = map[string]any{"tool_name": "echo", "parameters": map[string]any{"input": "x"}}

// TestIntegration_Legacy_URLElicitation_Required pins #7023 AC1.1: a go-sdk
// 2025-11-25 client that declared elicitation: {url: {}} receives -32042 with
// every elicitation in data.elicitations, in order, each with its
// elicitationId.
func TestIntegration_Legacy_URLElicitation_Required(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	ts := httptest.NewServer(newTwoElicitationHandler(t, backendURL))
	t.Cleanup(ts.Close)

	cs := legacyClient(t, ts.URL, mcpparser.MCPVersionLegacy, legacyURLCaps())
	assertURLElicitationRequired(t, legacyCallErr(t, cs, "echo", map[string]any{"input": "x"}))
}

// TestIntegration_Legacy_URLElicitation_RequiredViaOptimizer pins #7023
// AC1.2: through the optimizer's call_tool meta-tool, the same client
// receives the same -32042 with the same data.
func TestIntegration_Legacy_URLElicitation_RequiredViaOptimizer(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	ts := httptest.NewServer(newTwoElicitationHandler(t, backendURL, newOptimizerConfig(t)))
	t.Cleanup(ts.Close)

	cs := legacyClient(t, ts.URL, mcpparser.MCPVersionLegacy, legacyURLCaps())
	assertURLElicitationRequired(t, legacyCallErr(t, cs, "call_tool", optimizerEchoArgs))
}

// TestIntegration_Legacy_URLElicitation_UndeclaredFallsBack pins #7023 AC2.1:
// a Legacy client that did not declare elicitation.url, or that negotiated a
// revision older than 2025-11-25, receives an isError result whose text is the
// elicitation message, without the URL and without -32042, through a direct
// tools/call and through the optimizer's call_tool meta-tool.
func TestIntegration_Legacy_URLElicitation_UndeclaredFallsBack(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	direct := httptest.NewServer(newURLElicitingHandler(t, backendURL, answeringFlag(true)))
	t.Cleanup(direct.Close)
	viaOptimizer := httptest.NewServer(newURLElicitingHandler(t, backendURL, answeringFlag(true), newOptimizerConfig(t)))
	t.Cleanup(viaOptimizer.Close)

	clients := map[string]struct {
		version string
		caps    *gosdk.ClientCapabilities
	}{
		"elicitation {}": {mcpparser.MCPVersionLegacy, &gosdk.ClientCapabilities{
			Elicitation: &gosdk.ElicitationCapabilities{},
		}},
		"form only": {mcpparser.MCPVersionLegacy, &gosdk.ClientCapabilities{
			Elicitation: &gosdk.ElicitationCapabilities{Form: &gosdk.FormElicitationCapabilities{}},
		}},
		"no elicitation":                {mcpparser.MCPVersionLegacy, nil},
		"elicitation.url on 2025-06-18": {"2025-06-18", legacyURLCaps()},
	}
	paths := map[string]struct {
		baseURL  string
		toolName string
		args     map[string]any
	}{
		"direct tools/call":   {direct.URL, "echo", map[string]any{"input": "x"}},
		"optimizer call_tool": {viaOptimizer.URL, "call_tool", optimizerEchoArgs},
	}

	for clientName, c := range clients {
		for pathName, p := range paths {
			t.Run(clientName+"/"+pathName, func(t *testing.T) {
				t.Parallel()
				cs := legacyClient(t, p.baseURL, c.version, c.caps)
				res := callWhenRegistered(t, cs, p.toolName, p.args)

				require.True(t, res.IsError, "%+v", res)
				require.Len(t, res.Content, 1)
				text, ok := res.Content[0].(*gosdk.TextContent)
				require.True(t, ok)
				assert.Equal(t, urlElicitationMessage, text.Text,
					"the inner tool's fallback must pass through the meta-tool unchanged")
				raw, err := json.Marshal(res)
				require.NoError(t, err)
				assert.NotContains(t, string(raw), urlElicitationHost)
			})
		}
	}
}
