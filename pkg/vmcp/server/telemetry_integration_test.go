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
// backendAwareTestFactory
// ---------------------------------------------------------------------------
// Provides a bound MultiSession for TestIntegration_TelemetryMiddleware. On the New/Serve
// path the core sources the advertised set and routes tool calls through its own
// monitorBackends-wrapped backend client, so this factory only needs to open a bound
// session — its capability/call methods are not exercised by the Serve path.

// backendAwareTestSession is a minimal bound MultiSession.
type backendAwareTestSession struct {
	transportsession.Session
	tools        []vmcp.Tool
	routingTable *vmcp.RoutingTable
}

func (s *backendAwareTestSession) Tools() []vmcp.Tool                  { return s.tools }
func (s *backendAwareTestSession) AllTools() []vmcp.Tool               { return s.tools }
func (*backendAwareTestSession) Resources() []vmcp.Resource            { return nil }
func (*backendAwareTestSession) Prompts() []vmcp.Prompt                { return nil }
func (*backendAwareTestSession) BackendSessions() map[string]string    { return nil }
func (s *backendAwareTestSession) GetRoutingTable() *vmcp.RoutingTable { return s.routingTable }
func (*backendAwareTestSession) Close() error                          { return nil }

// CallTool is not exercised on the Serve path (the core routes tool calls); it returns an
// empty result to satisfy the MultiSession interface.
func (*backendAwareTestSession) CallTool(
	context.Context, *auth.Identity, string, map[string]any, map[string]any,
) (*vmcp.ToolCallResult, error) {
	return &vmcp.ToolCallResult{Content: []vmcp.Content{}}, nil
}

func (*backendAwareTestSession) ReadResource(
	context.Context, *auth.Identity, string,
) (*vmcp.ResourceReadResult, error) {
	return nil, errors.New("not implemented")
}

func (*backendAwareTestSession) GetPrompt(
	context.Context, *auth.Identity, string, map[string]any,
) (*vmcp.PromptGetResult, error) {
	return nil, errors.New("not implemented")
}

// backendAwareTestFactory creates bound backendAwareTestSessions.
type backendAwareTestFactory struct {
	tools        []vmcp.Tool
	routingTable *vmcp.RoutingTable
}

var _ vmcpsession.MultiSessionFactory = (*backendAwareTestFactory)(nil)

func newBackendAwareTestFactory(tools []vmcp.Tool, rt *vmcp.RoutingTable) *backendAwareTestFactory {
	return &backendAwareTestFactory{tools: tools, routingTable: rt}
}

func (f *backendAwareTestFactory) MakeSessionWithID(
	_ context.Context, id string, _ *auth.Identity, _ []*vmcp.Backend,
) (vmcpsession.MultiSession, error) {
	return f.newSession(id), nil
}

func (f *backendAwareTestFactory) RestoreSession(
	_ context.Context, id string, _ map[string]string, _ []*vmcp.Backend,
) (vmcpsession.MultiSession, error) {
	return f.newSession(id), nil
}

// newSession builds a session carrying the unauthenticated identity-binding sentinel.
// The Serve-path enforceSessionBinding reads it via GetMetadataValue before each
// core.CallTool; this test has no auth middleware, so the caller is anonymous and the
// sentinel admits it.
func (f *backendAwareTestFactory) newSession(id string) *backendAwareTestSession {
	sess := &backendAwareTestSession{
		Session:      transportsession.NewStreamableSession(id),
		tools:        f.tools,
		routingTable: f.routingTable,
	}
	sess.SetMetadata(vmcpsession.MetadataKeyIdentityBinding, "unauthenticated")
	return sess
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
	// The core sources the advertised set by aggregating over mockBackendClient (prefix
	// resolver → "search-svc_search"). core.New wraps this same client with monitorBackends
	// for telemetry, and core.CallTool routes tool calls through that wrapped client — so
	// the backend instrumentation (toolhive_vmcp_backend_requests) is exercised without the
	// session factory needing to hold the wrapped client.
	telemetryAgg := aggregator.NewDefaultAggregator(
		mockBackendClient, aggregator.NewPrefixConflictResolver("{workload}_"), nil, nil)
	telemetryFactory := newBackendAwareTestFactory(telemetryTools, telemetryRoutingTable)
	srv, err := New(ctx, &Config{
		Name:              "telemetry-vmcp",
		Version:           "1.0.0",
		Host:              "127.0.0.1",
		Port:              0, // Random available port
		TelemetryProvider: telemetryProvider,
		SessionFactory:    telemetryFactory,
		Aggregator:        telemetryAgg,
	}, rt, mockBackendClient, mockDiscoveryMgr, vmcp.NewImmutableRegistry(backends), nil)
	require.NoError(t, err)

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

		// --- Backend metrics (from backendtelemetry.MonitorBackends) ---

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
