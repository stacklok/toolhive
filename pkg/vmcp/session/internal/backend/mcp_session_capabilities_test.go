// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpclient "github.com/stacklok/toolhive-core/mcpcompat/client"
	mcptransport "github.com/stacklok/toolhive-core/mcpcompat/client/transport"
	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// fakeBackend is a minimal JSON-RPC over streamable-HTTP fake server. It lets
// each test declare exactly which capabilities to advertise in the initialize
// response and how to respond to each list method, including JSON-RPC error
// codes. This is the surface relied on by initAndQueryCapabilities, and
// constructing it directly avoids the mcp-go server's automatic capability
// inference (which would not let us simulate the spec-violation case where a
// capability is advertised but the corresponding list method returns -32601).
type fakeBackend struct {
	t *testing.T

	// Capabilities to advertise in the initialize response.
	advertiseTools     bool
	advertiseResources bool
	advertisePrompts   bool

	// Optional overrides for list responses. If nil, the server returns an
	// empty list of the corresponding type. To inject a JSON-RPC error,
	// populate listResourcesErr / listPromptsErr / listToolsErr.
	listResourcesErr *jsonRPCError
	listPromptsErr   *jsonRPCError
	listToolsErr     *jsonRPCError

	// Tools/resources/prompts to return when the corresponding list method
	// is not configured to error.
	tools     []mcp.Tool
	resources []mcp.Resource
	prompts   []mcp.Prompt

	// toolsPageSize, when > 0, makes tools/list paginate: each response returns
	// at most toolsPageSize tools plus a NextCursor until the set is exhausted.
	// The cursor is the decimal offset of the next page. Used to verify the
	// backend connector follows pagination cursors (#5844).
	toolsPageSize int

	// methodCalls counts how many times each method was invoked, so tests can
	// assert that (e.g.) tools/list was reached after a recoverable
	// resources/list failure.
	mu          sync.Mutex
	methodCalls map[string]int

	// headersByMethod records the inbound request headers keyed by JSON-RPC
	// method name. Tests asserting transport-chain behavior (e.g. HeaderForward)
	// use headersFor(method) to inspect the headers a backend actually saw.
	headersByMethod map[string]http.Header
}

type jsonRPCError struct {
	code    int
	message string
}

// newFakeBackend wires up an httptest.Server speaking just enough JSON-RPC for
// the streamable-HTTP transport to complete Initialize and the three list
// methods. It returns the server URL to point a client at.
func newFakeBackend(t *testing.T, fb *fakeBackend) string {
	t.Helper()
	fb.t = t
	fb.methodCalls = make(map[string]int)
	fb.headersByMethod = make(map[string]http.Header)

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", fb.handle)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

func (f *fakeBackend) callCount(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.methodCalls[method]
}

// headersFor returns a clone of the inbound HTTP headers recorded for the most
// recent JSON-RPC request with the given method, or nil if no such request was
// seen. Cloning under the mutex keeps the caller safe from concurrent writes.
//
// The helper accepts any MCP method name; in-tree callers currently converge
// on tools/call, which trips unparam — but the helper must stay
// method-agnostic so future tests can inspect headers for Initialize, etc.
//
//nolint:unparam // method-agnostic test helper; see doc comment above.
func (f *fakeBackend) headersFor(method string) http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.headersByMethod[method]
	if h == nil {
		return nil
	}
	return h.Clone()
}

// handle implements the JSON-RPC subset needed for backend init. The
// streamable-HTTP transport sends POST requests with Accept:
// application/json, text/event-stream — we always reply with
// application/json since we only need single-shot responses.
func (f *fakeBackend) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// The streamable-HTTP transport opens a GET to listen for server-pushed
		// notifications. Returning 405 cleanly tells it the server doesn't
		// support push, which is fine for our tests.
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		f.t.Errorf("fakeBackend: read body: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	var msg struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  struct {
			Cursor string `json:"cursor"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		f.t.Errorf("fakeBackend: decode: %v body=%s", err, string(body))
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	f.methodCalls[msg.Method]++
	f.headersByMethod[msg.Method] = r.Header.Clone()
	f.mu.Unlock()

	// Notifications (no id, e.g. notifications/initialized) get an empty 202.
	if len(msg.ID) == 0 || string(msg.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch msg.Method {
	case string(mcp.MethodInitialize):
		// Streamable-HTTP servers assign a session id during initialize.
		w.Header().Set("Mcp-Session-Id", "test-session")
		f.writeInitializeResult(w, msg.ID)
	case string(mcp.MethodToolsList):
		if f.listToolsErr != nil {
			f.writeError(w, msg.ID, f.listToolsErr)
			return
		}
		if f.toolsPageSize > 0 {
			f.writeToolsPage(w, msg.ID, msg.Params.Cursor)
			return
		}
		f.writeResult(w, msg.ID, map[string]any{"tools": f.tools})
	case string(mcp.MethodResourcesList):
		if f.listResourcesErr != nil {
			f.writeError(w, msg.ID, f.listResourcesErr)
			return
		}
		f.writeResult(w, msg.ID, map[string]any{"resources": f.resources})
	case string(mcp.MethodPromptsList):
		if f.listPromptsErr != nil {
			f.writeError(w, msg.ID, f.listPromptsErr)
			return
		}
		f.writeResult(w, msg.ID, map[string]any{"prompts": f.prompts})
	case string(mcp.MethodToolsCall):
		// Minimal CallToolResult with a single text content. Tests that exercise
		// the post-initialize transport chain (e.g. HeaderForward) need a method
		// they can invoke after Initialize completes; tools/call is the cheapest.
		f.writeResult(w, msg.ID, map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "ok"},
			},
			"isError": false,
		})
	default:
		f.writeError(w, msg.ID, &jsonRPCError{code: mcp.METHOD_NOT_FOUND, message: "Method not found"})
	}
}

func (f *fakeBackend) writeInitializeResult(w http.ResponseWriter, id json.RawMessage) {
	caps := map[string]any{}
	if f.advertiseTools {
		caps["tools"] = map[string]any{}
	}
	if f.advertiseResources {
		caps["resources"] = map[string]any{}
	}
	if f.advertisePrompts {
		caps["prompts"] = map[string]any{}
	}
	f.writeResult(w, id, map[string]any{
		"protocolVersion": mcp.LATEST_PROTOCOL_VERSION,
		"capabilities":    caps,
		"serverInfo":      map[string]any{"name": "fake-backend", "version": "0.0.0"},
	})
}

// writeToolsPage returns one page of f.tools starting at the offset encoded in
// cursor (empty = first page), plus a NextCursor when more tools remain. The
// cursor is the decimal offset of the next page.
func (f *fakeBackend) writeToolsPage(w http.ResponseWriter, id json.RawMessage, cursor string) {
	start := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil {
			f.writeError(w, id, &jsonRPCError{code: mcp.INVALID_PARAMS, message: "bad cursor"})
			return
		}
		start = n
	}
	end := start + f.toolsPageSize
	if end > len(f.tools) {
		end = len(f.tools)
	}
	result := map[string]any{"tools": f.tools[start:end]}
	if end < len(f.tools) {
		result["nextCursor"] = strconv.Itoa(end)
	}
	f.writeResult(w, id, result)
}

func (f *fakeBackend) writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"result":  result,
	}); err != nil {
		f.t.Errorf("fakeBackend: encode result: %v", err)
	}
}

func (f *fakeBackend) writeError(w http.ResponseWriter, id json.RawMessage, e *jsonRPCError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error": map[string]any{
			"code":    e.code,
			"message": e.message,
		},
	}); err != nil {
		f.t.Errorf("fakeBackend: encode error: %v", err)
	}
}

// newTestClient builds a streamable-HTTP mcpcompat client pointing at url and
// runs Start() so the transport is ready for initAndQueryCapabilities. Cleanup
// is registered via t.Cleanup.
func newTestClient(t *testing.T, url string) *mcpclient.Client {
	t.Helper()
	// Each test client gets its own transport with keep-alive disabled. A bare
	// &http.Client{} shares the process-global http.DefaultTransport, so across
	// t.Parallel() subtests one client's teardown (t.Cleanup -> Close) can close
	// idle connections a sibling is mid-request on. Go surfaces that as
	// "connection broken: http: CloseIdleConnections called", which the
	// streamable transport reports as a spurious 4xx/legacy-SSE init failure.
	c, err := mcpclient.NewStreamableHttpClient(url, mcptransport.WithHTTPBasicClient(&http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
	}))
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestInitAndQueryCapabilities_RecoversFromMethodNotFound verifies the issue
// #5231 fix: when a backend advertises resources or prompts capability but
// returns JSON-RPC -32601 to the corresponding list method, init must succeed
// with an empty capability set rather than aborting and dropping the backend's
// tools.
func TestInitAndQueryCapabilities_RecoversFromMethodNotFound(t *testing.T) {
	t.Parallel()

	helloTool := mcp.Tool{Name: "hello"}
	greetResource := mcp.Resource{
		URI:         "file:///greet.txt",
		Name:        "greet",
		Description: "greeting fixture",
		MIMEType:    "text/plain",
	}
	echoPrompt := mcp.Prompt{
		Name:        "echo",
		Description: "echoes its input",
		Arguments: []mcp.PromptArgument{
			{Name: "msg", Description: "what to echo", Required: true},
		},
	}

	tests := []struct {
		name             string
		fb               *fakeBackend
		wantTools        int
		wantResources    int
		wantPrompts      int
		wantToolsCalls   int
		wantResListCalls int
		wantPromptCalls  int
		assertCaps       func(t *testing.T, caps *vmcp.CapabilityList)
	}{
		{
			name: "resources/list -32601 after server advertised resources",
			fb: &fakeBackend{
				advertiseTools:     true,
				advertiseResources: true,
				tools:              []mcp.Tool{helloTool},
				listResourcesErr:   &jsonRPCError{code: mcp.METHOD_NOT_FOUND, message: "Method not found"},
			},
			wantTools:        1,
			wantResources:    0,
			wantPrompts:      0,
			wantToolsCalls:   1,
			wantResListCalls: 1,
		},
		{
			name: "prompts/list -32601 after server advertised prompts",
			fb: &fakeBackend{
				advertiseTools:   true,
				advertisePrompts: true,
				tools:            []mcp.Tool{helloTool},
				listPromptsErr:   &jsonRPCError{code: mcp.METHOD_NOT_FOUND, message: "Method not found"},
			},
			wantTools:       1,
			wantResources:   0,
			wantPrompts:     0,
			wantToolsCalls:  1,
			wantPromptCalls: 1,
		},
		{
			name: "both resources/list and prompts/list -32601",
			fb: &fakeBackend{
				advertiseTools:     true,
				advertiseResources: true,
				advertisePrompts:   true,
				tools:              []mcp.Tool{helloTool},
				listResourcesErr:   &jsonRPCError{code: mcp.METHOD_NOT_FOUND, message: "Method not found"},
				listPromptsErr:     &jsonRPCError{code: mcp.METHOD_NOT_FOUND, message: "Method not found"},
			},
			wantTools:        1,
			wantResources:    0,
			wantPrompts:      0,
			wantToolsCalls:   1,
			wantResListCalls: 1,
			wantPromptCalls:  1,
		},
		{
			name: "resources/list success populates caps with backend ID",
			fb: &fakeBackend{
				advertiseTools:     true,
				advertiseResources: true,
				tools:              []mcp.Tool{helloTool},
				resources:          []mcp.Resource{greetResource},
			},
			wantTools:        1,
			wantResources:    1,
			wantPrompts:      0,
			wantToolsCalls:   1,
			wantResListCalls: 1,
			assertCaps: func(t *testing.T, caps *vmcp.CapabilityList) {
				t.Helper()
				require.Len(t, caps.Resources, 1)
				got := caps.Resources[0]
				assert.Equal(t, greetResource.URI, got.URI)
				assert.Equal(t, greetResource.Name, got.Name)
				assert.Equal(t, greetResource.Description, got.Description)
				assert.Equal(t, greetResource.MIMEType, got.MimeType)
				assert.Equal(t, "fake-backend", got.BackendID)
			},
		},
		{
			name: "prompts/list success populates caps with arguments and backend ID",
			fb: &fakeBackend{
				advertiseTools:   true,
				advertisePrompts: true,
				tools:            []mcp.Tool{helloTool},
				prompts:          []mcp.Prompt{echoPrompt},
			},
			wantTools:       1,
			wantResources:   0,
			wantPrompts:     1,
			wantToolsCalls:  1,
			wantPromptCalls: 1,
			assertCaps: func(t *testing.T, caps *vmcp.CapabilityList) {
				t.Helper()
				require.Len(t, caps.Prompts, 1)
				got := caps.Prompts[0]
				assert.Equal(t, echoPrompt.Name, got.Name)
				assert.Equal(t, echoPrompt.Description, got.Description)
				assert.Equal(t, "fake-backend", got.BackendID)
				require.Len(t, got.Arguments, 1)
				assert.Equal(t, echoPrompt.Arguments[0].Name, got.Arguments[0].Name)
				assert.Equal(t, echoPrompt.Arguments[0].Description, got.Arguments[0].Description)
				assert.True(t, got.Arguments[0].Required)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			url := newFakeBackend(t, tc.fb)
			c := newTestClient(t, url)
			target := &vmcp.BackendTarget{
				WorkloadID:    "fake-backend",
				WorkloadName:  "fake-backend",
				BaseURL:       url,
				TransportType: "streamable-http",
			}

			caps, err := initAndQueryCapabilities(context.Background(), c, target)
			require.NoError(t, err, "init must succeed when list methods return -32601")
			require.NotNil(t, caps)

			assert.Len(t, caps.Tools, tc.wantTools)
			assert.Len(t, caps.Resources, tc.wantResources)
			assert.Len(t, caps.Prompts, tc.wantPrompts)

			// tools/list must have been reached — proving the backend's tool
			// surface remains usable even after a recoverable list-method
			// failure on resources/prompts.
			if tc.wantToolsCalls > 0 {
				assert.Equal(t, tc.wantToolsCalls, tc.fb.callCount(string(mcp.MethodToolsList)))
			}
			if tc.wantResListCalls > 0 {
				assert.Equal(t, tc.wantResListCalls, tc.fb.callCount(string(mcp.MethodResourcesList)))
			}
			if tc.wantPromptCalls > 0 {
				assert.Equal(t, tc.wantPromptCalls, tc.fb.callCount(string(mcp.MethodPromptsList)))
			}
			if tc.assertCaps != nil {
				tc.assertCaps(t, caps)
			}
		})
	}
}

// TestInitAndQueryCapabilities_FatalErrors verifies the regression guards
// called out in the issue's acceptance criteria:
//   - tools/list returning -32601 still aborts init (a backend with no tool
//     surface is not useful to expose).
//   - non-(-32601) errors from resources/list and prompts/list still abort init
//     (we are not silencing arbitrary failures).
func TestInitAndQueryCapabilities_FatalErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		fb        *fakeBackend
		errSubstr string
	}{
		{
			name: "tools/list -32601 remains fatal",
			fb: &fakeBackend{
				advertiseTools: true,
				listToolsErr:   &jsonRPCError{code: mcp.METHOD_NOT_FOUND, message: "Method not found"},
			},
			errSubstr: "list tools failed",
		},
		{
			name: "resources/list non-(-32601) remains fatal",
			fb: &fakeBackend{
				advertiseTools:     true,
				advertiseResources: true,
				listResourcesErr:   &jsonRPCError{code: mcp.INTERNAL_ERROR, message: "boom"},
			},
			errSubstr: "list resources failed",
		},
		{
			name: "prompts/list non-(-32601) remains fatal",
			fb: &fakeBackend{
				advertiseTools:   true,
				advertisePrompts: true,
				listPromptsErr:   &jsonRPCError{code: mcp.INVALID_PARAMS, message: "bad params"},
			},
			errSubstr: "list prompts failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			url := newFakeBackend(t, tc.fb)
			c := newTestClient(t, url)
			target := &vmcp.BackendTarget{
				WorkloadID:    "fake-backend",
				WorkloadName:  "fake-backend",
				BaseURL:       url,
				TransportType: "streamable-http",
			}

			_, err := initAndQueryCapabilities(context.Background(), c, target)
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.errSubstr)
		})
	}
}

// TestInitAndQueryCapabilities_FollowsToolPagination verifies the per-session
// backend connector follows MCP list-pagination cursors (#5844): a backend that
// returns its tools across multiple pages contributes its COMPLETE set, not just
// the first page. Before the fix the connector issued a single tools/list and
// silently dropped every tool beyond the first page.
func TestInitAndQueryCapabilities_FollowsToolPagination(t *testing.T) {
	t.Parallel()

	const (
		total    = 250
		pageSize = 100 // 3 pages: 100 + 100 + 50
	)
	tools := make([]mcp.Tool, total)
	for i := range tools {
		tools[i] = mcp.Tool{Name: fmt.Sprintf("tool_%d", i)}
	}
	fb := &fakeBackend{
		advertiseTools: true,
		tools:          tools,
		toolsPageSize:  pageSize,
	}
	url := newFakeBackend(t, fb)
	c := newTestClient(t, url)
	target := &vmcp.BackendTarget{
		WorkloadID:    "fake-backend",
		WorkloadName:  "fake-backend",
		BaseURL:       url,
		TransportType: "streamable-http",
	}

	caps, err := initAndQueryCapabilities(t.Context(), c, target)
	require.NoError(t, err)
	require.NotNil(t, caps)

	// Every page accumulated, with no gaps or duplicates across page boundaries.
	require.Len(t, caps.Tools, total, "connector must accumulate every page, not just the first")
	names := make(map[string]struct{}, total)
	for _, tl := range caps.Tools {
		names[tl.Name] = struct{}{}
	}
	assert.Len(t, names, total, "tool names must be distinct across pages (no overlap/duplication)")
	assert.Contains(t, names, "tool_0", "first page present")
	assert.Contains(t, names, fmt.Sprintf("tool_%d", total-1), "last page present")

	// The cursor loop must issue ceil(total/pageSize) tools/list calls — proof
	// pagination actually happened rather than one oversized page.
	assert.Equal(t, 3, fb.callCount(string(mcp.MethodToolsList)), "expected one tools/list per page")
}
