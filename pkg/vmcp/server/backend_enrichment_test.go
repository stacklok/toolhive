// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
)

// Common test constants
const toolsCallRequest = `{"method":"tools/call","params":{"name":"test-tool"}}`

// errorReader is a reader that always returns an error
type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("simulated read error")
}

// createTestHandler creates a handler that tracks if it was called
func createTestHandler() (http.Handler, *bool) {
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	return handler, &called
}

func TestBackendEnrichmentMiddleware(t *testing.T) {
	t.Parallel()

	t.Run("enriches backend name for tools/call request", func(t *testing.T) {
		t.Parallel()

		nextHandler, handlerCalled := createTestHandler()

		// Create routing table with a tool
		routingTable := &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{
				"test-tool": {
					WorkloadName: "backend-1",
				},
			},
		}

		// Create aggregated capabilities with routing table
		caps := &aggregator.AggregatedCapabilities{
			RoutingTable: routingTable,
		}

		// Create backend info that will be mutated
		backendInfo := &audit.BackendInfo{}

		// Create request with MCP tools/call body
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(toolsCallRequest)))

		// Add capabilities and backend info to context
		ctx := discovery.WithDiscoveredCapabilities(req.Context(), caps)
		ctx = audit.WithBackendInfo(ctx, backendInfo)
		req = req.WithContext(ctx)

		// Create response recorder
		rr := httptest.NewRecorder()

		// Create server instance and wrap handler with middleware
		srv := &Server{}
		middleware := srv.backendEnrichmentMiddleware(nextHandler)

		// Execute middleware
		middleware.ServeHTTP(rr, req)

		// Verify handler was called
		assert.True(t, *handlerCalled, "next handler should be called")
		assert.Equal(t, http.StatusOK, rr.Code)

		// Verify backend name was enriched
		assert.Equal(t, "backend-1", backendInfo.BackendName)
	})

	t.Run("enriches backend name for resources/read request", func(t *testing.T) {
		t.Parallel()

		nextHandler, handlerCalled := createTestHandler()

		// Create routing table with a resource
		routingTable := &vmcp.RoutingTable{
			Resources: map[string]*vmcp.BackendTarget{
				"file:///test/resource": {
					WorkloadName: "backend-2",
				},
			},
		}

		caps := &aggregator.AggregatedCapabilities{
			RoutingTable: routingTable,
		}

		backendInfo := &audit.BackendInfo{}

		requestBody := `{"method":"resources/read","params":{"uri":"file:///test/resource"}}`
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(requestBody)))

		ctx := discovery.WithDiscoveredCapabilities(req.Context(), caps)
		ctx = audit.WithBackendInfo(ctx, backendInfo)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()

		srv := &Server{}
		middleware := srv.backendEnrichmentMiddleware(nextHandler)
		middleware.ServeHTTP(rr, req)

		assert.True(t, *handlerCalled)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "backend-2", backendInfo.BackendName)
	})

	t.Run("enriches backend name for prompts/get request", func(t *testing.T) {
		t.Parallel()

		nextHandler, handlerCalled := createTestHandler()

		// Create routing table with a prompt
		routingTable := &vmcp.RoutingTable{
			Prompts: map[string]*vmcp.BackendTarget{
				"test-prompt": {
					WorkloadName: "backend-3",
				},
			},
		}

		caps := &aggregator.AggregatedCapabilities{
			RoutingTable: routingTable,
		}

		backendInfo := &audit.BackendInfo{}

		requestBody := `{"method":"prompts/get","params":{"name":"test-prompt"}}`
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(requestBody)))

		ctx := discovery.WithDiscoveredCapabilities(req.Context(), caps)
		ctx = audit.WithBackendInfo(ctx, backendInfo)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()

		srv := &Server{}
		middleware := srv.backendEnrichmentMiddleware(nextHandler)
		middleware.ServeHTTP(rr, req)

		assert.True(t, *handlerCalled)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "backend-3", backendInfo.BackendName)
	})

	t.Run("handles missing routing table gracefully", func(t *testing.T) {
		t.Parallel()

		nextHandler, handlerCalled := createTestHandler()

		// Create capabilities without routing table
		caps := &aggregator.AggregatedCapabilities{
			RoutingTable: nil,
		}

		backendInfo := &audit.BackendInfo{}

		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(toolsCallRequest)))

		ctx := discovery.WithDiscoveredCapabilities(req.Context(), caps)
		ctx = audit.WithBackendInfo(ctx, backendInfo)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()

		srv := &Server{}
		middleware := srv.backendEnrichmentMiddleware(nextHandler)
		middleware.ServeHTTP(rr, req)

		assert.True(t, *handlerCalled)
		assert.Equal(t, http.StatusOK, rr.Code)
		// Backend name should remain empty
		assert.Empty(t, backendInfo.BackendName)
	})

	t.Run("handles missing capabilities in context", func(t *testing.T) {
		t.Parallel()

		nextHandler, handlerCalled := createTestHandler()

		backendInfo := &audit.BackendInfo{}

		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(toolsCallRequest)))

		// Only add backend info, no capabilities
		ctx := audit.WithBackendInfo(req.Context(), backendInfo)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()

		srv := &Server{}
		middleware := srv.backendEnrichmentMiddleware(nextHandler)
		middleware.ServeHTTP(rr, req)

		assert.True(t, *handlerCalled)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Empty(t, backendInfo.BackendName)
	})

	t.Run("handles missing backend info in context", func(t *testing.T) {
		t.Parallel()

		nextHandler, handlerCalled := createTestHandler()

		routingTable := &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{
				"test-tool": {
					WorkloadName: "backend-1",
				},
			},
		}

		caps := &aggregator.AggregatedCapabilities{
			RoutingTable: routingTable,
		}

		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(toolsCallRequest)))

		// Only add capabilities, no backend info
		ctx := discovery.WithDiscoveredCapabilities(req.Context(), caps)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()

		srv := &Server{}
		middleware := srv.backendEnrichmentMiddleware(nextHandler)
		middleware.ServeHTTP(rr, req)

		// Should not panic, should proceed normally
		assert.True(t, *handlerCalled)
		assert.Equal(t, http.StatusOK, rr.Code)
	})

	t.Run("handles malformed JSON request", func(t *testing.T) {
		t.Parallel()

		nextHandler, handlerCalled := createTestHandler()

		routingTable := &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{
				"test-tool": {
					WorkloadName: "backend-1",
				},
			},
		}

		caps := &aggregator.AggregatedCapabilities{
			RoutingTable: routingTable,
		}

		backendInfo := &audit.BackendInfo{}

		// Malformed JSON
		requestBody := `{"method":"tools/call","params":{"name":"test-tool"`
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(requestBody)))

		ctx := discovery.WithDiscoveredCapabilities(req.Context(), caps)
		ctx = audit.WithBackendInfo(ctx, backendInfo)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()

		srv := &Server{}
		middleware := srv.backendEnrichmentMiddleware(nextHandler)
		middleware.ServeHTTP(rr, req)

		// Should not panic, should proceed with next handler
		assert.True(t, *handlerCalled)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Empty(t, backendInfo.BackendName)
	})

	t.Run("handles tool not found in routing table", func(t *testing.T) {
		t.Parallel()

		nextHandler, handlerCalled := createTestHandler()

		// Routing table with different tool
		routingTable := &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{
				"other-tool": {
					WorkloadName: "backend-1",
				},
			},
		}

		caps := &aggregator.AggregatedCapabilities{
			RoutingTable: routingTable,
		}

		backendInfo := &audit.BackendInfo{}

		requestBody := `{"method":"tools/call","params":{"name":"unknown-tool"}}`
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(requestBody)))

		ctx := discovery.WithDiscoveredCapabilities(req.Context(), caps)
		ctx = audit.WithBackendInfo(ctx, backendInfo)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()

		srv := &Server{}
		middleware := srv.backendEnrichmentMiddleware(nextHandler)
		middleware.ServeHTTP(rr, req)

		assert.True(t, *handlerCalled)
		assert.Equal(t, http.StatusOK, rr.Code)
		// Backend name should remain empty since tool not found
		assert.Empty(t, backendInfo.BackendName)
	})

	t.Run("properly restores request body for next handler", func(t *testing.T) {
		t.Parallel()
		var bodyRead []byte

		// Create a handler that reads the body
		bodyReadingHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var err error
			bodyRead, err = io.ReadAll(r.Body)
			require.NoError(t, err)
			w.WriteHeader(http.StatusOK)
		})

		routingTable := &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{
				"test-tool": {
					WorkloadName: "backend-1",
				},
			},
		}

		caps := &aggregator.AggregatedCapabilities{
			RoutingTable: routingTable,
		}

		backendInfo := &audit.BackendInfo{}

		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(toolsCallRequest)))

		ctx := discovery.WithDiscoveredCapabilities(req.Context(), caps)
		ctx = audit.WithBackendInfo(ctx, backendInfo)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()

		srv := &Server{}
		middleware := srv.backendEnrichmentMiddleware(bodyReadingHandler)
		middleware.ServeHTTP(rr, req)

		// Verify body was properly restored and readable by next handler
		assert.Equal(t, toolsCallRequest, string(bodyRead))
		assert.Equal(t, "backend-1", backendInfo.BackendName)
	})

	t.Run("handles nil request body", func(t *testing.T) {
		t.Parallel()

		nextHandler, handlerCalled := createTestHandler()

		routingTable := &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{
				"test-tool": {
					WorkloadName: "backend-1",
				},
			},
		}

		caps := &aggregator.AggregatedCapabilities{
			RoutingTable: routingTable,
		}

		backendInfo := &audit.BackendInfo{}

		req := httptest.NewRequest(http.MethodGet, "/mcp", nil)

		ctx := discovery.WithDiscoveredCapabilities(req.Context(), caps)
		ctx = audit.WithBackendInfo(ctx, backendInfo)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()

		srv := &Server{}
		middleware := srv.backendEnrichmentMiddleware(nextHandler)
		middleware.ServeHTTP(rr, req)

		// Should not panic, should proceed normally
		assert.True(t, *handlerCalled)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Empty(t, backendInfo.BackendName)
	})

	t.Run("handles empty request body", func(t *testing.T) {
		t.Parallel()

		nextHandler, handlerCalled := createTestHandler()

		routingTable := &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{
				"test-tool": {
					WorkloadName: "backend-1",
				},
			},
		}

		caps := &aggregator.AggregatedCapabilities{
			RoutingTable: routingTable,
		}

		backendInfo := &audit.BackendInfo{}

		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte("")))

		ctx := discovery.WithDiscoveredCapabilities(req.Context(), caps)
		ctx = audit.WithBackendInfo(ctx, backendInfo)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()

		srv := &Server{}
		middleware := srv.backendEnrichmentMiddleware(nextHandler)
		middleware.ServeHTTP(rr, req)

		assert.True(t, *handlerCalled)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Empty(t, backendInfo.BackendName)
	})

	t.Run("handles body read error gracefully", func(t *testing.T) {
		t.Parallel()

		nextHandler, handlerCalled := createTestHandler()

		routingTable := &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{
				"test-tool": {
					WorkloadName: "backend-1",
				},
			},
		}

		caps := &aggregator.AggregatedCapabilities{
			RoutingTable: routingTable,
		}

		backendInfo := &audit.BackendInfo{}

		// Create request with error reader that will fail on read
		req := httptest.NewRequest(http.MethodPost, "/mcp", io.NopCloser(errorReader{}))

		ctx := discovery.WithDiscoveredCapabilities(req.Context(), caps)
		ctx = audit.WithBackendInfo(ctx, backendInfo)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()

		srv := &Server{}
		middleware := srv.backendEnrichmentMiddleware(nextHandler)
		middleware.ServeHTTP(rr, req)

		// Should not panic, should proceed with next handler
		assert.True(t, *handlerCalled, "next handler should still be called even on read error")
		assert.Equal(t, http.StatusOK, rr.Code)
		// Backend name should remain empty since we couldn't read the body
		assert.Empty(t, backendInfo.BackendName)
	})

	t.Run("restores empty body after read error for downstream handlers", func(t *testing.T) {
		t.Parallel()
		var bodyRead []byte
		var readErr error

		// Create a handler that tries to read the body
		bodyReadingHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bodyRead, readErr = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		})

		routingTable := &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{
				"test-tool": {
					WorkloadName: "backend-1",
				},
			},
		}

		caps := &aggregator.AggregatedCapabilities{
			RoutingTable: routingTable,
		}

		backendInfo := &audit.BackendInfo{}

		// Create request with error reader
		req := httptest.NewRequest(http.MethodPost, "/mcp", io.NopCloser(errorReader{}))

		ctx := discovery.WithDiscoveredCapabilities(req.Context(), caps)
		ctx = audit.WithBackendInfo(ctx, backendInfo)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()

		srv := &Server{}
		middleware := srv.backendEnrichmentMiddleware(bodyReadingHandler)
		middleware.ServeHTTP(rr, req)

		// Verify body was restored (as empty) and readable by next handler without error
		assert.NoError(t, readErr, "downstream handler should be able to read restored body without error")
		assert.Empty(t, bodyRead, "restored body should be empty after read error")
	})
}

// TestBackendEnrichmentMiddleware_ServePath verifies the Serve path (s.core != nil):
// backend identity is resolved from the per-session advertised-set cache (keyed by the
// Mcp-Session-Id header, populated at registration), recorded as the backend's
// human-readable name to match the legacy path's WorkloadName, and resolved WITHOUT
// re-aggregating via the core — with no reliance on the discovery-into-context table.
func TestBackendEnrichmentMiddleware_ServePath(t *testing.T) {
	t.Parallel()

	const sessionID = "session-1"

	// Build the cache the way registration would: the core's single per-session
	// aggregation resolved "test-tool" to backend "github-mcp" (BackendID → display
	// name) and a resource to "docs-backend".
	cache, err := newAdvertisedSetCache(16)
	require.NoError(t, err)
	cache.store(sessionID, &advertisedSet{
		tools:     map[string]string{"test-tool": "github-mcp"},
		resources: map[string]string{"file:///doc": "docs-backend"},
	})
	// core is non-nil only to select the Serve branch; enrichment must NOT consult it.
	fc := &fakeCore{tools: []vmcp.Tool{{Name: "test-tool", BackendID: "backend-x"}}}
	srv := &Server{core: fc, advertisedSets: cache}

	enrich := func(t *testing.T, body, sid string) *audit.BackendInfo {
		t.Helper()
		nextHandler, handlerCalled := createTestHandler()
		backendInfo := &audit.BackendInfo{}
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(body)))
		if sid != "" {
			req.Header.Set("Mcp-Session-Id", sid)
		}
		req = req.WithContext(audit.WithBackendInfo(req.Context(), backendInfo))
		srv.backendEnrichmentMiddleware(nextHandler).ServeHTTP(httptest.NewRecorder(), req)
		assert.True(t, *handlerCalled)
		return backendInfo
	}

	t.Run("resolves a tool from the per-session cache (name, not raw BackendID)", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "github-mcp", enrich(t, toolsCallRequest, sessionID).BackendName)
	})

	t.Run("resolves a resource from the per-session cache", func(t *testing.T) {
		t.Parallel()
		body := `{"method":"resources/read","params":{"uri":"file:///doc"}}`
		assert.Equal(t, "docs-backend", enrich(t, body, sessionID).BackendName)
	})

	t.Run("misses for a capability not in the advertised set", func(t *testing.T) {
		t.Parallel()
		body := `{"method":"tools/call","params":{"name":"unknown-tool"}}`
		assert.Empty(t, enrich(t, body, sessionID).BackendName)
	})

	t.Run("misses when the session is unknown on this replica (cross-pod)", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, enrich(t, toolsCallRequest, "other-session").BackendName,
			"an unknown session must resolve no backend (and must not re-aggregate)")
	})

	t.Run("ignores the discovery context and resolves from the cache", func(t *testing.T) {
		t.Parallel()
		nextHandler, handlerCalled := createTestHandler()
		backendInfo := &audit.BackendInfo{}
		// A legacy routing table is present in context but must be ignored on the Serve path.
		caps := &aggregator.AggregatedCapabilities{RoutingTable: &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{"test-tool": {WorkloadName: "legacy-backend"}},
		}}
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(toolsCallRequest)))
		req.Header.Set("Mcp-Session-Id", sessionID)
		ctx := discovery.WithDiscoveredCapabilities(req.Context(), caps)
		ctx = audit.WithBackendInfo(ctx, backendInfo)
		req = req.WithContext(ctx)

		srv.backendEnrichmentMiddleware(nextHandler).ServeHTTP(httptest.NewRecorder(), req)

		assert.True(t, *handlerCalled)
		assert.Equal(t, "github-mcp", backendInfo.BackendName,
			"Serve path must resolve from the cache, not the discovery context (legacy-backend)")
	})

	// AC1 (issue #5493): an audited Serve-path request must NOT trigger a second backend
	// aggregation. Enrichment reads the cache and never calls core.Lookup*/List* — for
	// tools/call, resources/read, or prompts/get.
	t.Run("does not re-aggregate via the core for any method", func(t *testing.T) {
		t.Parallel()
		isolated := &fakeCore{
			tools:     []vmcp.Tool{{Name: "test-tool", BackendID: "backend-x"}},
			resources: []vmcp.Resource{{Name: "doc", URI: "file:///doc", BackendID: "backend-x"}},
		}
		isolatedCache, cErr := newAdvertisedSetCache(4)
		require.NoError(t, cErr)
		isolatedCache.store(sessionID, &advertisedSet{
			tools:     map[string]string{"test-tool": "github-mcp"},
			resources: map[string]string{"file:///doc": "docs-backend"},
		})
		isolatedSrv := &Server{core: isolated, advertisedSets: isolatedCache}

		for _, body := range []string{
			toolsCallRequest,
			`{"method":"resources/read","params":{"uri":"file:///doc"}}`,
			`{"method":"prompts/get","params":{"name":"p"}}`,
		} {
			nextHandler, _ := createTestHandler()
			req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(body)))
			req.Header.Set("Mcp-Session-Id", sessionID)
			req = req.WithContext(audit.WithBackendInfo(req.Context(), &audit.BackendInfo{}))
			isolatedSrv.backendEnrichmentMiddleware(nextHandler).ServeHTTP(httptest.NewRecorder(), req)
		}

		assert.Zero(t, isolated.lookupToolCalls.Load(), "enrichment must not call core.LookupTool")
		assert.Zero(t, isolated.listToolsCalls.Load(), "enrichment must not call core.ListTools")
		assert.Zero(t, isolated.lookupResCalls.Load(), "enrichment must not call core.LookupResource")
		assert.Zero(t, isolated.listResourcesCalls.Load(), "enrichment must not call core.ListResources")
		assert.Zero(t, isolated.lookupPromptCalls.Load(), "enrichment must not call core.LookupPrompt")
	})
}

func TestLookupBackendName(t *testing.T) {
	t.Parallel()

	routingTable := &vmcp.RoutingTable{
		Tools: map[string]*vmcp.BackendTarget{
			"tool-1": {WorkloadName: "backend-a"},
			"tool-2": {WorkloadName: "backend-b"},
		},
		Resources: map[string]*vmcp.BackendTarget{
			"file:///resource-1": {WorkloadName: "backend-c"},
			"file:///resource-2": {WorkloadName: "backend-d"},
		},
		Prompts: map[string]*vmcp.BackendTarget{
			"prompt-1": {WorkloadName: "backend-e"},
			"prompt-2": {WorkloadName: "backend-f"},
		},
	}

	t.Run("looks up tool by name", func(t *testing.T) {
		t.Parallel()
		params := map[string]any{
			"name": "tool-1",
		}

		result := lookupBackendName("tools/call", params, routingTable)
		assert.Equal(t, "backend-a", result)
	})

	t.Run("looks up resource by URI", func(t *testing.T) {
		t.Parallel()
		params := map[string]any{
			"uri": "file:///resource-1",
		}

		result := lookupBackendName("resources/read", params, routingTable)
		assert.Equal(t, "backend-c", result)
	})

	t.Run("looks up prompt by name", func(t *testing.T) {
		t.Parallel()
		params := map[string]any{
			"name": "prompt-1",
		}

		result := lookupBackendName("prompts/get", params, routingTable)
		assert.Equal(t, "backend-e", result)
	})

	t.Run("returns empty string for unknown tool", func(t *testing.T) {
		t.Parallel()
		params := map[string]any{
			"name": "unknown-tool",
		}

		result := lookupBackendName("tools/call", params, routingTable)
		assert.Empty(t, result)
	})

	t.Run("returns empty string for unknown resource", func(t *testing.T) {
		t.Parallel()
		params := map[string]any{
			"uri": "file:///unknown-resource",
		}

		result := lookupBackendName("resources/read", params, routingTable)
		assert.Empty(t, result)
	})

	t.Run("returns empty string for unknown prompt", func(t *testing.T) {
		t.Parallel()
		params := map[string]any{
			"name": "unknown-prompt",
		}

		result := lookupBackendName("prompts/get", params, routingTable)
		assert.Empty(t, result)
	})

	t.Run("returns empty string for unknown method", func(t *testing.T) {
		t.Parallel()
		params := map[string]any{
			"name": "tool-1",
		}

		result := lookupBackendName("unknown/method", params, routingTable)
		assert.Empty(t, result)
	})

	t.Run("handles missing parameter for tools/call", func(t *testing.T) {
		t.Parallel()
		params := map[string]any{
			"other": "value",
		}

		result := lookupBackendName("tools/call", params, routingTable)
		assert.Empty(t, result)
	})

	t.Run("handles missing parameter for resources/read", func(t *testing.T) {
		t.Parallel()
		params := map[string]any{
			"other": "value",
		}

		result := lookupBackendName("resources/read", params, routingTable)
		assert.Empty(t, result)
	})

	t.Run("handles missing parameter for prompts/get", func(t *testing.T) {
		t.Parallel()
		params := map[string]any{
			"other": "value",
		}

		result := lookupBackendName("prompts/get", params, routingTable)
		assert.Empty(t, result)
	})

	t.Run("handles non-string parameter for tools/call", func(t *testing.T) {
		t.Parallel()
		params := map[string]any{
			"name": 123, // Integer instead of string
		}

		result := lookupBackendName("tools/call", params, routingTable)
		assert.Empty(t, result)
	})

	t.Run("handles non-string parameter for resources/read", func(t *testing.T) {
		t.Parallel()
		params := map[string]any{
			"uri": []string{"not", "a", "string"},
		}

		result := lookupBackendName("resources/read", params, routingTable)
		assert.Empty(t, result)
	})

	t.Run("handles non-string parameter for prompts/get", func(t *testing.T) {
		t.Parallel()
		params := map[string]any{
			"name": map[string]string{"not": "a string"},
		}

		result := lookupBackendName("prompts/get", params, routingTable)
		assert.Empty(t, result)
	})

	t.Run("handles nil params map", func(t *testing.T) {
		t.Parallel()

		result := lookupBackendName("tools/call", nil, routingTable)
		assert.Empty(t, result)
	})

	t.Run("handles empty routing table", func(t *testing.T) {
		t.Parallel()
		emptyTable := &vmcp.RoutingTable{
			Tools:     map[string]*vmcp.BackendTarget{},
			Resources: map[string]*vmcp.BackendTarget{},
			Prompts:   map[string]*vmcp.BackendTarget{},
		}

		params := map[string]any{
			"name": "tool-1",
		}

		result := lookupBackendName("tools/call", params, emptyTable)
		assert.Empty(t, result)
	})
}

func TestBackendEnrichmentMiddleware_ContextPropagation(t *testing.T) {
	t.Parallel()

	t.Run("preserves context for downstream handlers", func(t *testing.T) {
		t.Parallel()

		var receivedCtx context.Context
		nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedCtx = r.Context()
			w.WriteHeader(http.StatusOK)
		})

		routingTable := &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{
				"test-tool": {WorkloadName: "backend-1"},
			},
		}

		caps := &aggregator.AggregatedCapabilities{
			RoutingTable: routingTable,
		}

		backendInfo := &audit.BackendInfo{}

		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(toolsCallRequest)))

		// Add some custom context value
		type contextKey string
		const testKey contextKey = "test-key"
		ctx := context.WithValue(req.Context(), testKey, "test-value")
		ctx = discovery.WithDiscoveredCapabilities(ctx, caps)
		ctx = audit.WithBackendInfo(ctx, backendInfo)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()

		srv := &Server{}
		middleware := srv.backendEnrichmentMiddleware(nextHandler)
		middleware.ServeHTTP(rr, req)

		// Verify custom context value was preserved
		require.NotNil(t, receivedCtx)
		assert.Equal(t, "test-value", receivedCtx.Value(testKey))

		// Verify capabilities and backend info are still accessible
		receivedCaps, ok := discovery.DiscoveredCapabilitiesFromContext(receivedCtx)
		assert.True(t, ok)
		assert.NotNil(t, receivedCaps)

		receivedBackendInfo, ok := audit.BackendInfoFromContext(receivedCtx)
		assert.True(t, ok)
		assert.NotNil(t, receivedBackendInfo)
		assert.Equal(t, "backend-1", receivedBackendInfo.BackendName)
	})
}
