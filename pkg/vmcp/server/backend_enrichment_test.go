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
