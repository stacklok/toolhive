// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
)

// listChangedRealBackendTimeout bounds each real-backend list_changed test, for
// the same reason as forwardingRealBackendTimeout (see forwarding_realbackend_
// integration_test.go): these exercise an async backend -> vMCP coordinator ->
// downstream relay (with a 250ms coordinator debounce on top), which can take
// several seconds under a loaded, -race CI run.
const listChangedRealBackendTimeout = 60 * time.Second

// mutableBackend wraps a raw go-sdk (not mcpcompat) MCP server so the test can
// mutate its OWN tool/resource/prompt set at will — including from a plain test
// goroutine with no vMCP call in flight (the "idle" list_changed path) or from
// inside a tool handler (the "mid-call" path). It deliberately bypasses
// toolhive-core's mcpcompat shim: mcpcompat's MCPServer.AddTool only feeds the
// ONE underlying gosdk.Server the shim's StreamableHTTPServer builds lazily on
// first request, with no supported way to mutate that already-built server
// afterwards (see the comment in pkg/vmcp/session/internal/backend/
// mcp_session_listchanged_test.go). The raw go-sdk *Server has no such
// limitation: AddTool/RemoveTools (and their Resource/Prompt counterparts) work
// against the live server and emit real notifications/*/list_changed traffic,
// exactly as a genuine third-party backend built directly on go-sdk would.
type mutableBackend struct {
	srv *gosdk.Server
}

// addTool registers a trivial tool that returns "ok" when called.
func (m *mutableBackend) addTool(name string) {
	gosdk.AddTool[any, any](m.srv, &gosdk.Tool{Name: name},
		func(context.Context, *gosdk.CallToolRequest, any) (*gosdk.CallToolResult, any, error) {
			return &gosdk.CallToolResult{Content: []gosdk.Content{&gosdk.TextContent{Text: "ok"}}}, nil, nil
		})
}

// addSelfMutatingTool registers a tool whose handler adds newToolName to the
// SAME backend server before returning — the "mid-call" scenario: the
// backend's own tools/call handler triggers its own list_changed notification
// while vMCP's tools/call to it is still in flight.
func (m *mutableBackend) addSelfMutatingTool(name, newToolName string) {
	gosdk.AddTool[any, any](m.srv, &gosdk.Tool{Name: name},
		func(context.Context, *gosdk.CallToolRequest, any) (*gosdk.CallToolResult, any, error) {
			m.addTool(newToolName)
			return &gosdk.CallToolResult{Content: []gosdk.Content{&gosdk.TextContent{Text: "mutated"}}}, nil, nil
		})
}

func (m *mutableBackend) removeTool(name string) { m.srv.RemoveTools(name) }

func (m *mutableBackend) addResource(uri, name string) {
	m.srv.AddResource(&gosdk.Resource{URI: uri, Name: name},
		func(context.Context, *gosdk.ReadResourceRequest) (*gosdk.ReadResourceResult, error) {
			return &gosdk.ReadResourceResult{
				Contents: []*gosdk.ResourceContents{{URI: uri, Text: "resource-body"}},
			}, nil
		})
}

func (m *mutableBackend) removeResource(uri string) { m.srv.RemoveResources(uri) }

func (m *mutableBackend) addPrompt(name string) {
	m.srv.AddPrompt(&gosdk.Prompt{Name: name},
		func(context.Context, *gosdk.GetPromptRequest) (*gosdk.GetPromptResult, error) {
			return &gosdk.GetPromptResult{
				Messages: []*gosdk.PromptMessage{{
					Role:    "user",
					Content: &gosdk.TextContent{Text: "prompt-body"},
				}},
			}, nil
		})
}

// startMutableBackend starts a raw go-sdk streamable-HTTP backend seeded with
// one tool (so it advertises SOME capability at startup) and returns the
// mutableBackend handle plus its /mcp URL.
func startMutableBackend(t *testing.T) (*mutableBackend, string) {
	t.Helper()

	srv := gosdk.NewServer(&gosdk.Implementation{Name: "mutable-backend", Version: "1.0.0"}, nil)
	mb := &mutableBackend{srv: srv}
	mb.addTool("seed_tool")

	handler := gosdk.NewStreamableHTTPHandler(func(*http.Request) *gosdk.Server { return srv }, nil)
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	ts := httptest.NewServer(mux)
	// Force-close active connections before Close: vMCP holds a standalone SSE GET
	// stream open against this backend for the whole session (see
	// cleanupBackendServer), which Close would otherwise block on.
	cleanupBackendServer(t, ts)
	return mb, ts.URL + "/mcp"
}

// drainNotifications consumes every pending forwarded notification plus any that
// arrive within a short quiet window, so a subsequent waitNotification proves an
// emission caused by a LATER action rather than the benign registration-time
// tools/list_changed (go-sdk emits one ~10ms after a session's tools are first
// projected — see the W0 capability-flip note in serve.go). The presence
// backstops already keep these tests sound; this tightens the notification
// assertion to the mutation-caused emission.
func (dc *downstreamClient) drainNotifications() {
	const quiet = 300 * time.Millisecond
	for {
		select {
		case <-dc.notifCh:
		case <-time.After(quiet):
			return
		}
	}
}

// waitToolPresence polls tools/list on the downstream client's session until
// wantName's presence matches want, or the context is done.
func waitToolPresence(ctx context.Context, t *testing.T, dc *downstreamClient, wantName string, want bool) {
	t.Helper()
	for {
		res, err := dc.c.ListTools(ctx, mcpmcp.ListToolsRequest{})
		if err == nil {
			found := false
			for _, tl := range res.Tools {
				if tl.Name == wantName {
					found = true
					break
				}
			}
			if found == want {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for tool %q presence=%v: %v", wantName, want, err)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// TestListChanged_MidCallMutate_RealBackend covers scenario 1 from the spec: a
// backend mutates its OWN tool set from inside a tools/call handler (mid-call).
// vMCP must relay the resulting notifications/tools/list_changed to the
// downstream client, and a subsequent tools/list must show the new tool and
// hide nothing that wasn't removed.
func TestListChanged_MidCallMutate_RealBackend(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), listChangedRealBackendTimeout)
	defer cancel()

	mb, backendURL := startMutableBackend(t)
	mb.addSelfMutatingTool("mutate_add", "added_midcall")

	vmcpTS := newRealTestServer(t, backendURL)
	dc := newDownstreamClient(ctx, t, vmcpTS.URL+"/mcp", false)

	// Wait for registration to complete (mutate_add advertised) and drain the
	// benign registration-time tools/list_changed, so the assertion below proves
	// the MID-CALL mutation emission.
	waitToolPresence(ctx, t, dc, "mutate_add", true)
	dc.drainNotifications()

	res, err := dc.c.CallTool(ctx, mcpCallToolParams("mutate_add", nil))
	require.NoError(t, err)
	require.False(t, res.IsError)

	dc.waitNotification(ctx, t, "notifications/tools/list_changed")
	waitToolPresence(ctx, t, dc, "added_midcall", true)
}

// TestListChanged_IdleMutate_RealBackend covers scenario 2 from the spec: the
// backend mutates its tool set from a plain test goroutine with NO vMCP call in
// flight. Delivery for this case depends entirely on the PERSISTENT session
// connector's continuous-listening stream (pkg/vmcp/session/internal/backend),
// not the per-call forwarding path.
func TestListChanged_IdleMutate_RealBackend(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), listChangedRealBackendTimeout)
	defer cancel()

	mb, backendURL := startMutableBackend(t)
	vmcpTS := newRealTestServer(t, backendURL)
	dc := newDownstreamClient(ctx, t, vmcpTS.URL+"/mcp", false)

	// Establish the session (and its persistent backend connection) before
	// mutating, and confirm the seed tool is visible first.
	waitToolPresence(ctx, t, dc, "seed_tool", true)
	dc.drainNotifications()

	mb.addTool("added_idle")

	dc.waitNotification(ctx, t, "notifications/tools/list_changed")
	waitToolPresence(ctx, t, dc, "added_idle", true)

	// Removal, unlike resources/prompts, IS supported for tools (setSessionToolsReplace):
	// prove the removed tool actually disappears downstream, not just that
	// additions propagate.
	dc.drainNotifications()
	mb.removeTool("added_idle")
	dc.waitNotification(ctx, t, "notifications/tools/list_changed")
	waitToolPresence(ctx, t, dc, "added_idle", false)
}

// TestListChanged_FanOut_RealBackend covers scenario 3: two concurrent
// downstream sessions against the same vMCP instance must BOTH observe an idle
// backend mutation.
func TestListChanged_FanOut_RealBackend(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), listChangedRealBackendTimeout)
	defer cancel()

	mb, backendURL := startMutableBackend(t)
	vmcpTS := newRealTestServer(t, backendURL)

	dcA := newDownstreamClient(ctx, t, vmcpTS.URL+"/mcp", false)
	dcB := newDownstreamClient(ctx, t, vmcpTS.URL+"/mcp", false)

	waitToolPresence(ctx, t, dcA, "seed_tool", true)
	waitToolPresence(ctx, t, dcB, "seed_tool", true)
	dcA.drainNotifications()
	dcB.drainNotifications()

	mb.addTool("added_fanout")

	dcA.waitNotification(ctx, t, "notifications/tools/list_changed")
	dcB.waitNotification(ctx, t, "notifications/tools/list_changed")
	waitToolPresence(ctx, t, dcA, "added_fanout", true)
	waitToolPresence(ctx, t, dcB, "added_fanout", true)
}

// TestListChanged_ResourcesAndPromptsAdd_RealBackend covers scenario 4: a
// backend ADDING a resource or a prompt propagates a list_changed notification
// and the new item appears downstream. Removal propagation for
// resources/prompts is an explicit, documented toolhive-core follow-up (see
// listChangedCoordinator.resweepResources), so it is intentionally NOT
// asserted here.
func TestListChanged_ResourcesAndPromptsAdd_RealBackend(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), listChangedRealBackendTimeout)
	defer cancel()

	mb, backendURL := startMutableBackend(t)
	vmcpTS := newRealTestServer(t, backendURL)
	dc := newDownstreamClient(ctx, t, vmcpTS.URL+"/mcp", false)

	waitToolPresence(ctx, t, dc, "seed_tool", true)
	dc.drainNotifications()

	mb.addResource("file:///new-resource.txt", "new-resource")
	dc.waitNotification(ctx, t, "notifications/resources/list_changed")

	require.Eventually(t, func() bool {
		res, err := dc.c.ListResources(ctx, mcpmcp.ListResourcesRequest{})
		if err != nil {
			return false
		}
		for _, r := range res.Resources {
			if r.URI == "file:///new-resource.txt" {
				return true
			}
		}
		return false
	}, 10*time.Second, 100*time.Millisecond, "the added resource must appear in resources/list")

	dc.drainNotifications()
	mb.addPrompt("new-prompt")
	dc.waitNotification(ctx, t, "notifications/prompts/list_changed")

	require.Eventually(t, func() bool {
		res, err := dc.c.ListPrompts(ctx, mcpmcp.ListPromptsRequest{})
		if err != nil {
			return false
		}
		for _, p := range res.Prompts {
			if p.Name == "new-prompt" {
				return true
			}
		}
		return false
	}, 10*time.Second, 100*time.Millisecond, "the added prompt must appear in prompts/list")
}

// TestListChanged_CapabilityRegression is scenario 5: the initialize response
// must advertise tools.listChanged, resources.subscribe+listChanged, and
// prompts.listChanged, all true (W0's capability flip).
func TestListChanged_CapabilityRegression(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	vmcpTS := newRealTestServer(t, backendURL)

	resp := postMCP(t, vmcpTS.URL, map[string]any{
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
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var rpc struct {
		Result struct {
			Capabilities struct {
				Tools *struct {
					ListChanged bool `json:"listChanged"`
				} `json:"tools"`
				Resources *struct {
					Subscribe   bool `json:"subscribe"`
					ListChanged bool `json:"listChanged"`
				} `json:"resources"`
				Prompts *struct {
					ListChanged bool `json:"listChanged"`
				} `json:"prompts"`
			} `json:"capabilities"`
		} `json:"result"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rpc))

	require.NotNil(t, rpc.Result.Capabilities.Tools, "tools capability must be advertised")
	assert.True(t, rpc.Result.Capabilities.Tools.ListChanged, "tools.listChanged must be true")

	require.NotNil(t, rpc.Result.Capabilities.Resources, "resources capability must be advertised")
	assert.True(t, rpc.Result.Capabilities.Resources.Subscribe, "resources.subscribe must be true")
	assert.True(t, rpc.Result.Capabilities.Resources.ListChanged, "resources.listChanged must be true")

	require.NotNil(t, rpc.Result.Capabilities.Prompts, "prompts capability must be advertised")
	assert.True(t, rpc.Result.Capabilities.Prompts.ListChanged, "prompts.listChanged must be true")
}

// TestListChanged_ResourceRemovalNotPropagated_RegressionGuard locks in the
// CURRENT add-only limitation for resources (and, by the same mechanism,
// prompts): a resource a backend REMOVES stays advertised in the downstream
// resources/list until the session ends, because the per-session resource
// overlay sync (mcpcompat's syncSessionResources) only ever ADDS — it has no
// RemoveResources reconciliation the way tools' syncSessionTools has RemoveTools.
//
// This is intentional for THIS PR (see listChangedCoordinator.resweepResources).
// When the toolhive-core follow-up makes resource/prompt sync reconciling, this
// test WILL start failing — that failure is the intended signal to update it to
// assert removal now propagates (and to flip the coordinator's resource/prompt
// path to setSessionToolsReplace-style semantics). Note reads are unaffected:
// coreResourceHandler routes every resources/read through core.ReadResource,
// which re-derives admission per call, so a stale overlay entry cannot be READ
// once the backend/admission drops it — only listed.
func TestListChanged_ResourceRemovalNotPropagated_RegressionGuard(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), listChangedRealBackendTimeout)
	defer cancel()

	mb, backendURL := startMutableBackend(t)
	vmcpTS := newRealTestServer(t, backendURL)
	dc := newDownstreamClient(ctx, t, vmcpTS.URL+"/mcp", false)

	waitToolPresence(ctx, t, dc, "seed_tool", true)
	dc.drainNotifications()

	// Keep a second resource present for the whole test so removing `uri` never
	// drops the backend to ZERO resources: a go-sdk backend infers its resource
	// capability from registered resources, so removing the last one would leave
	// no resources capability and emit NO resources/list_changed — the re-sweep
	// would never fire and the guard would pass vacuously. With `keep` remaining,
	// the removal still emits a real notification and drives an actual re-sweep.
	const (
		keep = "file:///keep.txt"
		uri  = "file:///removable.txt"
	)
	mb.addResource(keep, "keep")
	mb.addResource(uri, "removable")
	dc.waitNotification(ctx, t, "notifications/resources/list_changed")

	resourcePresent := func() bool {
		res, err := dc.c.ListResources(ctx, mcpmcp.ListResourcesRequest{})
		if err != nil {
			return false
		}
		for _, r := range res.Resources {
			if r.URI == uri {
				return true
			}
		}
		return false
	}
	require.Eventually(t, resourcePresent, 10*time.Second, 100*time.Millisecond,
		"the added resource must first appear before we can assert removal is NOT propagated")

	// Remove it on the backend; `keep` remains, so the backend emits
	// resources/list_changed and vMCP re-sweeps — but the add-only overlay cannot
	// drop the stale entry.
	dc.drainNotifications()
	mb.removeResource(uri)
	dc.waitNotification(ctx, t, "notifications/resources/list_changed")

	// Settle past the re-sweep, then assert the removed resource STILL lists
	// (the documented add-only gap). Flip this to require absence when the
	// toolhive-core follow-up lands.
	time.Sleep(500 * time.Millisecond)
	assert.True(t, resourcePresent(),
		"KNOWN add-only limitation: a removed resource stays advertised until session end "+
			"(update this guard when the toolhive-core resource/prompt removal follow-up lands)")
}

// mcpCallToolParams builds a mcpmcp.CallToolRequest for name/args, mirroring the
// shape forwarding_realbackend_integration_test.go's tests build inline. Small
// helper to avoid repeating the Params wrapper at each call site in this file.
func mcpCallToolParams(name string, args map[string]any) mcpmcp.CallToolRequest {
	return mcpmcp.CallToolRequest{Params: mcpmcp.CallToolParams{Name: name, Arguments: args}}
}
