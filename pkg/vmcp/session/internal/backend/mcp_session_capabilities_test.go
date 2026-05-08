// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

	// methodCalls counts how many times each method was invoked, so tests can
	// assert that (e.g.) tools/list was reached after a recoverable
	// resources/list failure.
	mu          sync.Mutex
	methodCalls map[string]int
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
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		f.t.Errorf("fakeBackend: decode: %v body=%s", err, string(body))
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	f.methodCalls[msg.Method]++
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

// newTestClient builds a streamable-HTTP mark3labs client pointing at url and
// runs Start() so the transport is ready for initAndQueryCapabilities. Cleanup
// is registered via t.Cleanup.
func newTestClient(t *testing.T, url string) *mcpclient.Client {
	t.Helper()
	c, err := mcpclient.NewStreamableHttpClient(url, mcptransport.WithHTTPBasicClient(&http.Client{}))
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

	tests := []struct {
		name             string
		fb               *fakeBackend
		wantTools        int
		wantResources    int
		wantPrompts      int
		wantToolsCalled  bool
		wantResListCalls int
		wantPromptCalls  int
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
			wantToolsCalled:  true,
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
			wantToolsCalled: true,
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
			wantToolsCalled:  true,
			wantResListCalls: 1,
			wantPromptCalls:  1,
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
			if tc.wantToolsCalled {
				assert.Equal(t, 1, tc.fb.callCount(string(mcp.MethodToolsList)),
					"tools/list must be invoked exactly once")
			}
			if tc.wantResListCalls > 0 {
				assert.Equal(t, tc.wantResListCalls, tc.fb.callCount(string(mcp.MethodResourcesList)))
			}
			if tc.wantPromptCalls > 0 {
				assert.Equal(t, tc.wantPromptCalls, tc.fb.callCount(string(mcp.MethodPromptsList)))
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
			assert.True(t, strings.Contains(err.Error(), tc.errSubstr),
				"error %q must contain %q", err.Error(), tc.errSubstr)
		})
	}
}
