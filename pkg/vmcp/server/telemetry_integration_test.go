// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/telemetry"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	discoveryMocks "github.com/stacklok/toolhive/pkg/vmcp/discovery/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// ---------------------------------------------------------------------------
// backendAwareTestSession / backendAwareTestFactory
// ---------------------------------------------------------------------------
// Used by TestIntegration_TelemetryMiddleware to verify that tool calls reach
// the monitorBackends-wrapped backend client so backend-level metrics are recorded.

// backendClientRef holds a vmcp.BackendClient that can be set after server
// creation, once the monitorBackends-wrapped client is available.
type backendClientRef struct {
	mu     sync.Mutex
	client vmcp.BackendClient
}

func (r *backendClientRef) set(c vmcp.BackendClient) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.client = c
}

func (r *backendClientRef) get() vmcp.BackendClient {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.client
}

// backendAwareTestSession delegates CallTool to the wrapped backend client so
// that monitorBackends instrumentation is exercised during tool calls.
type backendAwareTestSession struct {
	transportsession.Session
	tools        []vmcp.Tool
	routingTable *vmcp.RoutingTable
	clientRef    *backendClientRef
}

func (s *backendAwareTestSession) Tools() []vmcp.Tool                  { return s.tools }
func (*backendAwareTestSession) Resources() []vmcp.Resource            { return nil }
func (*backendAwareTestSession) Prompts() []vmcp.Prompt                { return nil }
func (*backendAwareTestSession) BackendSessions() map[string]string    { return nil }
func (s *backendAwareTestSession) GetRoutingTable() *vmcp.RoutingTable { return s.routingTable }
func (*backendAwareTestSession) Close() error                          { return nil }

func (s *backendAwareTestSession) CallTool(
	ctx context.Context, _ *auth.Identity, toolName string, args map[string]any, meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	client := s.clientRef.get()
	if s.routingTable == nil || client == nil {
		return &vmcp.ToolCallResult{Content: []vmcp.Content{}}, nil
	}
	target, ok := s.routingTable.Tools[toolName]
	if !ok {
		return &vmcp.ToolCallResult{Content: []vmcp.Content{}}, nil
	}
	return client.CallTool(ctx, target, toolName, args, meta)
}

func (*backendAwareTestSession) ReadResource(
	_ context.Context, _ *auth.Identity, _ string,
) (*vmcp.ResourceReadResult, error) {
	return nil, errors.New("not implemented")
}

func (*backendAwareTestSession) GetPrompt(
	_ context.Context, _ *auth.Identity, _ string, _ map[string]any,
) (*vmcp.PromptGetResult, error) {
	return nil, errors.New("not implemented")
}

// backendAwareTestFactory creates backendAwareTestSessions.
type backendAwareTestFactory struct {
	tools        []vmcp.Tool
	routingTable *vmcp.RoutingTable
	clientRef    *backendClientRef
}

var _ vmcpsession.MultiSessionFactory = (*backendAwareTestFactory)(nil)

func newBackendAwareTestFactory(tools []vmcp.Tool, rt *vmcp.RoutingTable) (*backendAwareTestFactory, *backendClientRef) {
	ref := &backendClientRef{}
	return &backendAwareTestFactory{tools: tools, routingTable: rt, clientRef: ref}, ref
}

func (f *backendAwareTestFactory) MakeSessionWithID(
	_ context.Context, id string, _ *auth.Identity, _ bool, _ []*vmcp.Backend,
) (vmcpsession.MultiSession, error) {
	return &backendAwareTestSession{
		Session:      transportsession.NewStreamableSession(id),
		tools:        f.tools,
		routingTable: f.routingTable,
		clientRef:    f.clientRef,
	}, nil
}

// TestIntegration_TelemetryMiddleware tests that the vMCP server records telemetry
// metrics when the telemetry middleware is enabled via TelemetryProvider.
//
// This validates:
// 1. Incoming MCP requests are counted by toolhive_mcp_requests
// 2. Request latency is tracked by toolhive_mcp_request_duration
// 3. Backend calls are counted by toolhive_vmcp_backend_requests
// 4. Backend discovery count is reported by toolhive_vmcp_backends_discovered
// 5. All metrics are accessible via the /metrics Prometheus endpoint
//
// Note: This test does not use t.Parallel() because subtests share the same
// server instance and TelemetryProvider sets global OTel providers.
//
//nolint:paralleltest // Subtests must run sequentially as they share server state
func TestIntegration_TelemetryMiddleware(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	ctx := context.Background()

	// Create telemetry provider with Prometheus metrics enabled.
	// This wires up a real meter provider with a Prometheus reader so we can
	// scrape /metrics to verify recorded metrics.
	telemetryProvider, err := telemetry.NewProvider(ctx, telemetry.Config{
		ServiceName:                 "vmcp-telemetry-test",
		ServiceVersion:              "1.0.0",
		EnablePrometheusMetricsPath: true,
		CustomAttributes: map[string]string{
			"deployment": "dan-demo",
			"region":     "us-east-1",
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { telemetryProvider.Shutdown(ctx) })

	// Create mock backend client
	mockBackendClient := mocks.NewMockBackendClient(ctrl)

	backendCapabilities := &vmcp.CapabilityList{
		Tools: []vmcp.Tool{
			{
				Name:        "search",
				Description: "Search for items",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{"type": "string"},
					},
				},
				BackendID: "search-svc",
			},
		},
		Resources: []vmcp.Resource{},
		Prompts:   []vmcp.Prompt{},
	}

	// Mock backend responses
	mockBackendClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(backendCapabilities, nil).
		AnyTimes()

	// Use MinTimes(1) to verify the backend client is actually called during tool execution.
	// If the tool call doesn't reach the backend client, this will cause a test failure.
	mockBackendClient.EXPECT().
		CallTool(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&vmcp.ToolCallResult{
			StructuredContent: map[string]any{"result": "found"},
			Content:           []vmcp.Content{},
		}, nil).
		MinTimes(1)

	backends := []vmcp.Backend{
		{
			ID:            "search-svc",
			Name:          "Search Service",
			BaseURL:       "http://search-svc:8080",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
		},
	}

	// Create discovery manager (follows same pattern as TestIntegration_AuditLogging)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockDiscoveryMgr.EXPECT().
		Discover(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
			resolver := aggregator.NewPrefixConflictResolver("{workload}_")
			agg := aggregator.NewDefaultAggregator(mockBackendClient, resolver, nil, nil)
			return agg.AggregateCapabilities(ctx, backends)
		}).
		AnyTimes()
	mockDiscoveryMgr.EXPECT().Stop().AnyTimes()

	// Create router
	rt := router.NewDefaultRouter()

	// Build the tools and routing table. The aggregator prefixes tool names with
	// "{workload}_", so "search" becomes "search-svc_search".
	telemetryTools := []vmcp.Tool{
		{
			Name:        "search-svc_search",
			Description: "Search for items",
			BackendID:   "search-svc",
		},
	}
	telemetryRoutingTable := &vmcp.RoutingTable{
		Tools: map[string]*vmcp.BackendTarget{
			"search-svc_search": {
				WorkloadID:   "search-svc",
				WorkloadName: "Search Service",
			},
		},
		Resources: map[string]*vmcp.BackendTarget{},
		Prompts:   map[string]*vmcp.BackendTarget{},
	}

	// Create server with telemetry provider — this also wraps the backend
	// client with monitorBackends() which instruments outgoing backend calls.
	// Use backendAwareTestFactory so that CallTool delegates to the monitorBackends-wrapped
	// backendClient, ensuring toolhive_vmcp_backend_requests metrics are recorded.
	telemetryFactory, clientRef := newBackendAwareTestFactory(telemetryTools, telemetryRoutingTable)
	srv, err := New(ctx, &Config{
		Name:              "telemetry-vmcp",
		Version:           "1.0.0",
		Host:              "127.0.0.1",
		Port:              0, // Random available port
		TelemetryProvider: telemetryProvider,
		SessionFactory:    telemetryFactory,
	}, rt, mockBackendClient, mockDiscoveryMgr, vmcp.NewImmutableRegistry(backends), nil)
	require.NoError(t, err)
	// Wire the monitorBackends-wrapped client into the session factory so that
	// tool calls go through the telemetry instrumentation layer.
	clientRef.set(srv.backendClient)

	// Start server
	serverCtx, cancelServer := context.WithCancel(ctx)
	t.Cleanup(cancelServer)

	serverErrCh := make(chan error, 1)
	go func() {
		if err := srv.Start(serverCtx); err != nil && !errors.Is(err, context.Canceled) {
			serverErrCh <- err
		}
	}()

	// Wait for server ready
	select {
	case <-srv.Ready():
	case err := <-serverErrCh:
		t.Fatalf("Server failed to start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Server timeout waiting for ready")
	}

	baseURL := "http://" + srv.Address()
	var sessionID string

	// Test 1: Initialize request
	t.Run("initialize request succeeds", func(t *testing.T) {
		initReq := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "initialize",
			"params": map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"clientInfo": map[string]any{
					"name":    "telemetry-test-client",
					"version": "1.0.0",
				},
			},
		}

		reqBody, err := json.Marshal(initReq)
		require.NoError(t, err)

		resp, err := http.Post(baseURL+"/mcp", "application/json", bytes.NewReader(reqBody))
		require.NoError(t, err)
		defer resp.Body.Close()

		require.Equal(t, http.StatusOK, resp.StatusCode, "Initialize should succeed")

		sessionID = resp.Header.Get("Mcp-Session-Id")
		require.NotEmpty(t, sessionID, "Session ID should be returned")
	})

	// Allow time for AfterInitialize/OnRegisterSession hooks to complete
	time.Sleep(200 * time.Millisecond)

	// Test 2: Tool call request — exercises both the telemetry middleware (incoming)
	// and the monitorBackends wrapper (outgoing backend call)
	t.Run("tool call succeeds", func(t *testing.T) {
		require.NotEmpty(t, sessionID, "Session ID must be set from initialize test")

		toolCallReq := map[string]any{
			"jsonrpc": "2.0",
			"id":      2,
			"method":  "tools/call",
			"params": map[string]any{
				"name":      "search-svc_search", // Prefixed by conflict resolver
				"arguments": map[string]any{"query": "test"},
			},
		}

		reqBody, err := json.Marshal(toolCallReq)
		require.NoError(t, err)

		req, err := http.NewRequest("POST", baseURL+"/mcp", bytes.NewReader(reqBody))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Mcp-Session-Id", sessionID)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		require.Equal(t, http.StatusOK, resp.StatusCode, "Tool call should succeed")
	})

	// Test 3: Verify Prometheus metrics
	t.Run("prometheus metrics contain expected request metrics", func(t *testing.T) {
		resp, err := http.Get(baseURL + "/metrics")
		require.NoError(t, err)
		defer resp.Body.Close()

		require.Equal(t, http.StatusOK, resp.StatusCode, "/metrics endpoint should be accessible")

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		metrics := string(body)

		// --- Incoming request metrics (from telemetry middleware in pkg/telemetry/middleware.go) ---

		// Request counter
		assert.Contains(t, metrics, "toolhive_mcp_requests",
			"Should record incoming request counter")
		assert.Contains(t, metrics, `server="telemetry-vmcp"`,
			"Request metrics should identify the vMCP server name")
		assert.Contains(t, metrics, `transport="streamable-http"`,
			"Request metrics should identify the transport type")

		// MCP method labels — the telemetry middleware should distinguish request types
		assert.Contains(t, metrics, `mcp_method="tools/call"`,
			"Request counter should have mcp_method label for tool calls")
		assert.Contains(t, metrics, `mcp_method="initialize"`,
			"Request counter should have mcp_method label for initialize")

		// Resource ID label — for tools/call the mcp_resource_id is the tool name
		assert.Contains(t, metrics, `mcp_resource_id="search-svc_search"`,
			"Request counter should have mcp_resource_id label with the called tool name")

		// Request duration histogram
		assert.Contains(t, metrics, "toolhive_mcp_request_duration",
			"Should record request duration histogram")

		// --- Backend metrics (from monitorBackends in vmcp/server/telemetry.go) ---

		// Backend request counter — recorded when the tool call was routed to the backend
		assert.Contains(t, metrics, "toolhive_vmcp_backend_requests",
			"Should record backend request counter from tool call routing")

		// Backend request duration histogram
		assert.Contains(t, metrics, "toolhive_vmcp_backend_requests_duration",
			"Should record backend request duration histogram")

		// Backend discovery gauge — recorded during server.New() for the initial backend list
		assert.Contains(t, metrics, "toolhive_vmcp_backends_discovered",
			"Should record backend discovery count gauge")

		// --- Custom resource attributes (from Config.CustomAttributes) ---
		// Custom attributes are added to the OTel resource and surface as labels on the
		// target_info gauge in Prometheus exposition format.
		assert.Contains(t, metrics, "target_info",
			"Should have target_info gauge for resource attributes")
		assert.Contains(t, metrics, `deployment="dan-demo"`,
			"Custom attribute 'deployment' should appear on target_info")
		assert.Contains(t, metrics, `region="us-east-1"`,
			"Custom attribute 'region' should appear on target_info")
	})

	cancelServer()
}
