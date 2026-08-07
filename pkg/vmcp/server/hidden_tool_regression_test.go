// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// backendCallRecorder records the tool name of every call that reaches
// BackendClient.CallTool. Asserting a name is ABSENT here is what proves the
// request was rejected before dispatch: a JSON-RPC error alone would also be
// produced if vMCP had forwarded the call to the backend and then discarded the
// response.
type backendCallRecorder struct {
	mu    sync.Mutex
	names []string
}

func (r *backendCallRecorder) record(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.names = append(r.names, name)
}

func (r *backendCallRecorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.names...)
}

// TestRegression_HiddenToolNotDirectlyCallable is the two-directional guard for
// the tool-visibility invariant:
//
//	ADVERTISED == DIRECTLY CALLABLE, while the routing table stays deliberately
//	WIDER so composite workflow steps can reach hidden tools.
//
// The aggregator puts EVERY backend tool in the routing table, including those
// hidden from tools/list by excludeAllTools, per-workload excludeAll, or filter
// (default_aggregator.go:349) — on purpose, so a composite tool can call a tool
// its operator chose not to expose (#3636). core.CallTool used to resolve a
// direct call against that same table, so a hidden tool was callable by name; on
// the Modern (2026-07-28) path, where tools/call goes straight to core.CallTool,
// that was reachable straight off the wire.
//
// BOTH directions are asserted because they must move in opposite directions, and
// a single-direction test is exactly what let the bug ship:
//
//   - "hidden_tool is rejected AND never reaches the backend" fails if the
//     advertised-view check in core.CallTool is reverted.
//   - "a composite whose step targets hidden_tool still succeeds, and the backend
//     DOES receive hidden_tool" fails if someone instead 'fixes' this by pruning
//     hidden tools from the routing table — which would silently re-break #3636.
//
// Both eras are covered: Legacy is protected only by the SDK registering
// advertised names on the session, so pinning it here keeps the two paths from
// drifting apart again.
func TestRegression_HiddenToolNotDirectlyCallable(t *testing.T) {
	t.Parallel()

	const (
		visibleTool  = "visible_tool"
		hiddenTool   = "hidden_tool"
		compositeWF  = "wf_using_hidden"
		schemaObject = "object"
	)

	// newFixture builds a server whose advertised set is {visible_tool} while the
	// routing table also holds hidden_tool, plus one composite workflow whose only
	// step targets the hidden tool. factory is the session factory: the Legacy leg
	// needs one that stubs GetMetadataValue (enforceSessionBinding reads the
	// identity binding through it on every tools/call), which newNoopMockFactory
	// does not.
	newFixture := func(t *testing.T, factory vmcpsession.MultiSessionFactory) (string, *backendCallRecorder) {
		t.Helper()
		rec := &backendCallRecorder{}
		ts := buildTestServerWithOptions(t, factory, serverOptions{
			tools: []vmcp.Tool{{
				Name:        visibleTool,
				Description: "advertised",
				InputSchema: map[string]any{"type": schemaObject},
			}},
			hiddenTools: []vmcp.Tool{{
				Name:        hiddenTool,
				Description: "routable but withheld from tools/list",
				InputSchema: map[string]any{"type": schemaObject},
			}},
			workflowDefs: map[string]*composer.WorkflowDefinition{
				compositeWF: {
					Name: compositeWF,
					Steps: []composer.WorkflowStep{
						{ID: "s1", Type: composer.StepTypeTool, Tool: hiddenTool},
					},
				},
			},
			onBackendCall: func(name string) { rec.record(name) },
		})
		return ts.URL, rec
	}

	t.Run("modern", func(t *testing.T) {
		t.Parallel()
		baseURL, rec := newFixture(t, newNoopMockFactory(t))

		// tools/list must not leak the hidden tool.
		listResp, listBody := postModern(t, baseURL, "tools/list", nil, 1, "")
		defer listResp.Body.Close()
		require.Equal(t, http.StatusOK, listResp.StatusCode, "decoded: %+v", listBody)
		advertised := modernToolNames(t, listBody)
		assert.Contains(t, advertised, visibleTool)
		assert.NotContains(t, advertised, hiddenTool,
			"a tool hidden by excludeAll/filter must not appear in tools/list")

		// The advertised tool is callable, proving the fixture's happy path works and
		// the rejection below is about visibility, not a broken harness.
		okResp, okBody := postModern(t, baseURL, "tools/call",
			map[string]any{"name": visibleTool, "arguments": map[string]any{}}, 2, visibleTool)
		defer okResp.Body.Close()
		assert.Equal(t, http.StatusOK, okResp.StatusCode, "decoded: %+v", okBody)
		assert.NotContains(t, okBody, "error", "an advertised tool must remain callable: %+v", okBody)

		// THE REGRESSION: calling the hidden tool by name must be refused with
		// -32602 (HTTP 400), matching what the Legacy SDK path answers for a tool it
		// never registered — not -32603, and certainly not a success.
		hiddenResp, hiddenBody := postModern(t, baseURL, "tools/call",
			map[string]any{"name": hiddenTool, "arguments": map[string]any{}}, 3, hiddenTool)
		defer hiddenResp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, hiddenResp.StatusCode,
			"an unadvertised tool name is caller input, so it must map to HTTP 400: %+v", hiddenBody)
		errObj, ok := hiddenBody["error"].(map[string]any)
		require.True(t, ok, "expected a JSON-RPC error envelope, got %+v", hiddenBody)
		assert.Equal(t, float64(-32602), errObj["code"],
			"a hidden tool must be answered as an unknown tool, not laundered into -32603")

		// The security assertion: the call was refused BEFORE reaching the backend.
		assert.NotContains(t, rec.got(), hiddenTool,
			"vMCP must never forward a direct call to a tool hidden from tools/list")

		// THE COUNTERWEIGHT: a composite tool whose step targets the hidden tool must
		// still work, and the backend must actually receive the hidden tool name.
		// This is what fails if the routing table is pruned instead (#3636).
		wfResp, wfBody := postModern(t, baseURL, "tools/call",
			map[string]any{"name": compositeWF, "arguments": map[string]any{}}, 4, compositeWF)
		defer wfResp.Body.Close()
		require.Equal(t, http.StatusOK, wfResp.StatusCode, "decoded: %+v", wfBody)
		assert.Contains(t, rec.got(), hiddenTool,
			"a composite workflow step must still reach a hidden backend tool (#3636)")
	})

	t.Run("legacy", func(t *testing.T) {
		t.Parallel()
		// The Serve path sources the advertised set from the aggregator, not the
		// factory, so newMockFactory's tool argument only shapes its own routing
		// table; it is used here for its GetMetadataValue stub.
		factory, _ := newMockFactory(t, gomock.NewController(t), nil)
		baseURL, rec := newFixture(t, factory)
		sessionID := legacyInitialize(t, baseURL)

		names := listToolNames(t, baseURL, sessionID)
		assert.Contains(t, names, visibleTool)
		assert.NotContains(t, names, hiddenTool,
			"a tool hidden by excludeAll/filter must not appear in tools/list")

		// Legacy has no dispatcher-level classification: the SDK simply never
		// registered the hidden tool on the session, so it answers -32602. Asserting
		// the decoded error.code (not a substring of the body) is what keeps the two
		// eras from drifting apart on the same input -- this is the same assertion
		// the Modern leg makes above, so a divergence in either direction fails.
		// The message text does differ by era: toolhive-core's mcpcompat rewrites
		// go-sdk's `unknown tool "X"` to `tool "X" not found`, so only the code is
		// compared.
		hiddenErr := legacyCallError(t, baseURL, sessionID, 3, hiddenTool)
		assert.Equal(t, float64(-32602), hiddenErr["code"],
			"Legacy must also refuse a hidden tool as an unknown tool: %+v", hiddenErr)
		assert.NotContains(t, rec.got(), hiddenTool,
			"vMCP must never forward a direct call to a tool hidden from tools/list")

		// Same counterweight as the Modern leg.
		legacyCall(t, baseURL, sessionID, 4, "tools/call",
			map[string]any{"name": compositeWF, "arguments": map[string]any{}})
		assert.Contains(t, rec.got(), hiddenTool,
			"a composite workflow step must still reach a hidden backend tool (#3636)")
	})
}

// modernToolNames extracts result.tools[].name from a Modern tools/list envelope.
func modernToolNames(t *testing.T, decoded map[string]any) []string {
	t.Helper()
	result, ok := decoded["result"].(map[string]any)
	require.True(t, ok, "expected a result object, got %+v", decoded)
	raw, ok := result["tools"].([]any)
	require.True(t, ok, "expected result.tools, got %+v", result)
	names := make([]string, 0, len(raw))
	for _, entry := range raw {
		tool, ok := entry.(map[string]any)
		require.True(t, ok, "expected a tool object, got %T", entry)
		name, ok := tool["name"].(string)
		require.True(t, ok, "expected tool.name, got %+v", tool)
		names = append(names, name)
	}
	return names
}

// legacyInitialize performs the Legacy (session-based) handshake and returns the
// session ID every subsequent request must carry.
func legacyInitialize(t *testing.T, baseURL string) string {
	t.Helper()
	resp := postMCP(t, baseURL, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
		},
	}, "")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "initialize should succeed")
	sessionID := resp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID, "initialize must return a session ID")
	return sessionID
}

// legacyCall sends a Legacy JSON-RPC request on an established session and returns
// the raw response body. Used where only "the call was answered" matters (the
// composite counterweight); assertions on an error envelope go through
// legacyCallError, which decodes.
func legacyCall(t *testing.T, baseURL, sessionID string, id int, method string, params map[string]any) string {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		body["params"] = params
	}
	resp := postMCP(t, baseURL, body, sessionID)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s should be answered; body: %s", method, string(raw))
	// Sanity: the body must be a JSON-RPC payload (possibly SSE-framed), not empty.
	require.NotEmpty(t, raw, "%s returned an empty body", method)
	return string(raw)
}

// legacyCallError issues a Legacy tools/call for toolName and returns the decoded
// JSON-RPC `error` object, failing the test if the response carries no error.
// Decoding (rather than substring-matching the body) is what makes this assertion
// as strong as the Modern leg's: it pins error.code for THIS request rather than
// accepting the digits appearing anywhere in the payload.
func legacyCallError(t *testing.T, baseURL, sessionID string, id int, toolName string) map[string]any {
	t.Helper()
	resp := postMCP(t, baseURL, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "tools/call",
		"params":  map[string]any{"name": toolName, "arguments": map[string]any{}},
	}, sessionID)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var env struct {
		Error map[string]any `json:"error"`
	}
	require.NoError(t, json.Unmarshal(raw, &env), "tools/call response was not JSON: %s", string(raw))
	require.NotNil(t, env.Error, "expected a JSON-RPC error envelope, got: %s", string(raw))
	return env.Error
}
