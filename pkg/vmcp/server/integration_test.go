package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
	discoveryMocks "github.com/stacklok/toolhive/pkg/vmcp/discovery/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// TestIntegration_AggregatorToRouterToServer tests the complete integration
// of the aggregation pipeline with the router and server.
//
// This validates:
// 1. Aggregator creates a valid RoutingTable
// 2. Router accepts and stores the routing table
// 3. Server registers capabilities from aggregated results
// 4. Router can successfully route requests to backends
func TestIntegration_AggregatorToRouterToServer(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	ctx := context.Background()

	// Step 1: Create mock backend client that returns capabilities
	mockBackendClient := mocks.NewMockBackendClient(ctrl)

	// Mock backend returns capabilities when queried
	backend1Capabilities := &vmcp.CapabilityList{
		Tools: []vmcp.Tool{
			{
				Name:        "create_issue",
				Description: "Create a GitHub issue",
				InputSchema: map[string]any{
					"title": map[string]any{"type": "string"},
					"body":  map[string]any{"type": "string"},
				},
				BackendID: "github",
			},
		},
		Resources: []vmcp.Resource{
			{
				URI:         "file:///github/repos",
				Name:        "GitHub Repositories",
				Description: "List of repositories",
				MimeType:    "application/json",
				BackendID:   "github",
			},
		},
		Prompts: []vmcp.Prompt{
			{
				Name:        "code_review",
				Description: "Generate code review",
				Arguments:   []vmcp.PromptArgument{},
				BackendID:   "github",
			},
		},
		SupportsLogging:  true,
		SupportsSampling: false,
	}

	backend2Capabilities := &vmcp.CapabilityList{
		Tools: []vmcp.Tool{
			{
				Name:        "create_issue",
				Description: "Create a Jira issue",
				InputSchema: map[string]any{
					"summary":     map[string]any{"type": "string"},
					"description": map[string]any{"type": "string"},
				},
				BackendID: "jira",
			},
		},
		Resources: []vmcp.Resource{},
		Prompts:   []vmcp.Prompt{},
	}

	// Mock ListCapabilities for both backends
	mockBackendClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, target *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
			if target.WorkloadID == "github" {
				return backend1Capabilities, nil
			}
			return backend2Capabilities, nil
		}).
		Times(2)

	// Step 2: Create aggregator with prefix conflict resolver
	conflictResolver := aggregator.NewPrefixConflictResolver("{workload}_")
	agg := aggregator.NewDefaultAggregator(
		mockBackendClient,
		conflictResolver,
		nil, // no tool configs
	)

	// Step 3: Run aggregation on mock backends
	backends := []vmcp.Backend{
		{
			ID:            "github",
			Name:          "GitHub MCP",
			BaseURL:       "http://github-mcp:8080",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
		},
		{
			ID:            "jira",
			Name:          "Jira MCP",
			BaseURL:       "http://jira-mcp:8080",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
		},
	}

	aggregatedCaps, err := agg.AggregateCapabilities(ctx, backends)
	require.NoError(t, err)
	require.NotNil(t, aggregatedCaps)

	// Validate aggregated capabilities
	assert.Equal(t, 2, len(aggregatedCaps.Tools), "Should have 2 tools after prefix resolution")
	assert.Equal(t, 1, len(aggregatedCaps.Resources), "Should have 1 resource")
	assert.Equal(t, 1, len(aggregatedCaps.Prompts), "Should have 1 prompt")

	// Validate tool names have prefixes
	toolNames := make(map[string]bool)
	for _, tool := range aggregatedCaps.Tools {
		toolNames[tool.Name] = true
	}
	assert.True(t, toolNames["github_create_issue"], "GitHub tool should have prefix")
	assert.True(t, toolNames["jira_create_issue"], "Jira tool should have prefix")

	// Validate routing table was created
	require.NotNil(t, aggregatedCaps.RoutingTable)
	assert.Equal(t, 2, len(aggregatedCaps.RoutingTable.Tools))
	assert.Equal(t, 1, len(aggregatedCaps.RoutingTable.Resources))
	assert.Equal(t, 1, len(aggregatedCaps.RoutingTable.Prompts))

	// Step 4: Create router and add capabilities to context
	rt := router.NewDefaultRouter()

	// Add discovered capabilities to context
	ctxWithCaps := discovery.WithDiscoveredCapabilities(ctx, aggregatedCaps)

	// Step 5: Verify router can route to correct backends (using context with capabilities)
	target, err := rt.RouteTool(ctxWithCaps, "github_create_issue")
	require.NoError(t, err)
	assert.Equal(t, "github", target.WorkloadID)
	assert.Equal(t, "http://github-mcp:8080", target.BaseURL)

	target, err = rt.RouteTool(ctxWithCaps, "jira_create_issue")
	require.NoError(t, err)
	assert.Equal(t, "jira", target.WorkloadID)
	assert.Equal(t, "http://jira-mcp:8080", target.BaseURL)

	target, err = rt.RouteResource(ctxWithCaps, "file:///github/repos")
	require.NoError(t, err)
	assert.Equal(t, "github", target.WorkloadID)

	target, err = rt.RoutePrompt(ctxWithCaps, "code_review")
	require.NoError(t, err)
	assert.Equal(t, "github", target.WorkloadID)

	// Step 6: Create discovery manager and server
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)

	// Mock discovery to return our aggregated capabilities
	mockDiscoveryMgr.EXPECT().
		Discover(gomock.Any(), gomock.Any()).
		Return(aggregatedCaps, nil).
		AnyTimes()

	// Mock Stop to be called during server shutdown
	mockDiscoveryMgr.EXPECT().Stop().Times(1)

	srv, err := server.New(ctx, &server.Config{
		Name:    "test-vmcp",
		Version: "1.0.0",
		Host:    "127.0.0.1",
		Port:    4484,
	}, rt, mockBackendClient, mockDiscoveryMgr, backends, nil)
	require.NoError(t, err)

	// Validate server address
	assert.Equal(t, "127.0.0.1:4484", srv.Address())

	// Step 7: Start server and validate it's running
	serverCtx, cancelServer := context.WithCancel(ctx)
	t.Cleanup(cancelServer)

	// Start server in background
	serverErrCh := make(chan error, 1)
	go func() {
		if err := srv.Start(serverCtx); err != nil && !errors.Is(err, context.Canceled) {
			serverErrCh <- err
		}
	}()

	// Wait for server to be ready by checking if the port is listening
	serverReady := false
	for i := 0; i < 10; i++ {
		conn, err := net.DialTimeout("tcp", srv.Address(), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			serverReady = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Check if server failed to start
	select {
	case err := <-serverErrCh:
		t.Fatalf("Server failed to start: %v", err)
	default:
		// Server is running
	}

	require.True(t, serverReady, fmt.Sprintf("Server did not start listening on %s within timeout", srv.Address()))

	// Clean up: stop the server
	cancelServer()
	time.Sleep(100 * time.Millisecond) // Give server time to shutdown
}

// TestIntegration_HTTPRequestFlowWithRoutingTable reproduces the routing table initialization issue.
// This test verifies that the routing table is properly stored in VMCPSession during initialization
// and can be retrieved for subsequent requests via the complete HTTP request flow.
func TestIntegration_HTTPRequestFlowWithRoutingTable(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	ctx := context.Background()

	// Create mock backend with test tool
	mockBackendClient := mocks.NewMockBackendClient(ctrl)

	testCapabilities := &vmcp.CapabilityList{
		Tools: []vmcp.Tool{
			{
				Name:        "test_tool",
				Description: "A test tool",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"input": map[string]any{"type": "string"},
					},
				},
				BackendID: "test-backend",
			},
		},
		Resources:        []vmcp.Resource{},
		Prompts:          []vmcp.Prompt{},
		SupportsLogging:  false,
		SupportsSampling: false,
	}

	// Mock ListCapabilities for discovery
	mockBackendClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(testCapabilities, nil).
		AnyTimes()

	// Mock CallTool for tool execution
	mockBackendClient.EXPECT().
		CallTool(gomock.Any(), gomock.Any(), "test_tool", gomock.Any()).
		Return(map[string]any{"result": "success"}, nil).
		AnyTimes()

	// Create real components
	backends := []vmcp.Backend{
		{
			ID:            "test-backend",
			Name:          "Test Backend",
			BaseURL:       "http://test-backend:8080",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
		},
	}

	// Create discovery manager
	conflictResolver := aggregator.NewPrefixConflictResolver("{workload}_")
	agg := aggregator.NewDefaultAggregator(mockBackendClient, conflictResolver, nil)
	discoveryMgr, err := discovery.NewManager(agg)
	require.NoError(t, err)

	// Create router
	rt := router.NewDefaultRouter()

	// Create identity middleware for auth (must set identity for discovery)
	identityMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity := &auth.Identity{
				Subject: "test-user",
				Name:    "testuser",
				Email:   "test@example.com",
			}
			ctx := auth.WithIdentity(r.Context(), identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}

	// Create and start server
	srv, err := server.New(ctx, &server.Config{
		Name:           "test-vmcp",
		Version:        "1.0.0",
		Host:           "127.0.0.1",
		Port:           0, // Use random available port
		SessionTTL:     5 * time.Minute,
		AuthMiddleware: identityMiddleware,
	}, rt, mockBackendClient, discoveryMgr, backends, nil)
	require.NoError(t, err)

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

	// STEP 1: Send initialize request (no session ID)
	t.Log("Sending initialize request")
	initReq := map[string]any{
		"method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "test-client",
				"version": "1.0.0",
			},
		},
	}

	initReqBody, err := json.Marshal(initReq)
	require.NoError(t, err)

	initResp, err := http.Post(baseURL+"/mcp", "application/json", bytes.NewReader(initReqBody))
	require.NoError(t, err)
	defer initResp.Body.Close()

	require.Equal(t, http.StatusOK, initResp.StatusCode, "Initialize request should succeed")

	// Extract session ID
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID, "Session ID should be returned")

	t.Logf("Got session ID: %s", sessionID)

	// Give server time to complete AfterInitialize hook
	time.Sleep(100 * time.Millisecond)

	// CRITICAL CHECK: Verify routing table is stored in session
	sess, ok := srv.SessionManager().Get(sessionID)
	require.True(t, ok, "Session should exist in manager")
	require.NotNil(t, sess, "Session should not be nil")

	t.Logf("Session type: %T", sess)

	vmcpSess, ok := sess.(*vmcpsession.VMCPSession)
	require.True(t, ok, "Session should be VMCPSession type, got: %T", sess)

	routingTable := vmcpSess.GetRoutingTable()
	require.NotNil(t, routingTable, "Routing table should be stored")
	if routingTable == nil {
		// Debug: Check session data
		t.Logf("Session ID: %s", vmcpSess.ID())
		t.Logf("Session Type: %v", vmcpSess.Type())
		t.Logf("Session Data: %v", vmcpSess.GetData())
		t.Fatal("REPRODUCER: Routing table is nil after initialization - this is the bug!")
		return
	}

	t.Logf("Routing table has %d tools", len(routingTable.Tools))
	// Note: Tool name is prefixed with backend ID due to conflict resolution
	require.Contains(t, routingTable.Tools, "test-backend_test_tool", "Routing table should have prefixed test_tool")

	// STEP 2: Send tool call request (with session ID)
	t.Log("Sending tool call request")
	toolCallReq := map[string]any{
		"method": "tools/call",
		"params": map[string]any{
			"name":      "test-backend_test_tool", // Prefixed name from conflict resolution
			"arguments": map[string]any{"input": "test"},
		},
	}

	toolCallReqBody, err := json.Marshal(toolCallReq)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, baseURL+"/mcp", bytes.NewReader(toolCallReqBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", sessionID)

	toolCallResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer toolCallResp.Body.Close()

	if toolCallResp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(toolCallResp.Body)
		t.Logf("Tool call failed with status %d: %s", toolCallResp.StatusCode, string(bodyBytes))
	}

	require.Equal(t, http.StatusOK, toolCallResp.StatusCode, "Tool call should succeed")

	t.Log("Test passed - routing table working correctly")
	cancelServer()
}

// TestIntegration_ConflictResolutionStrategies tests that different
// conflict resolution strategies work end-to-end.
func TestIntegration_ConflictResolutionStrategies(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create backends with conflicting tool names
	createBackendsWithConflicts := func() []vmcp.Backend {
		return []vmcp.Backend{
			{
				ID:            "backend1",
				Name:          "Backend 1",
				BaseURL:       "http://backend1:8080",
				TransportType: "streamable-http",
				HealthStatus:  vmcp.BackendHealthy,
			},
			{
				ID:            "backend2",
				Name:          "Backend 2",
				BaseURL:       "http://backend2:8080",
				TransportType: "streamable-http",
				HealthStatus:  vmcp.BackendHealthy,
			},
		}
	}

	t.Run("prefix strategy creates unique tool names", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockBackendClient := mocks.NewMockBackendClient(ctrl)

		// Both backends have "create" tool
		capabilities := &vmcp.CapabilityList{
			Tools: []vmcp.Tool{
				{Name: "create", Description: "Create something", BackendID: "backend1"},
			},
		}

		mockBackendClient.EXPECT().
			ListCapabilities(gomock.Any(), gomock.Any()).
			Return(capabilities, nil).
			Times(2)

		resolver := aggregator.NewPrefixConflictResolver("{workload}_")
		agg := aggregator.NewDefaultAggregator(mockBackendClient, resolver, nil)

		result, err := agg.AggregateCapabilities(ctx, createBackendsWithConflicts())
		require.NoError(t, err)

		// Should have 2 tools with different names
		assert.Equal(t, 2, len(result.Tools))
		toolNames := []string{result.Tools[0].Name, result.Tools[1].Name}
		assert.Contains(t, toolNames, "backend1_create")
		assert.Contains(t, toolNames, "backend2_create")
	})

	t.Run("priority strategy drops lower priority conflicts", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockBackendClient := mocks.NewMockBackendClient(ctrl)

		mockBackendClient.EXPECT().
			ListCapabilities(gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, target *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
				// Create a new CapabilityList for each call to avoid race conditions
				return &vmcp.CapabilityList{
					Tools: []vmcp.Tool{
						{
							Name:        "create",
							Description: "Create something",
							BackendID:   target.WorkloadID,
						},
					},
				}, nil
			}).
			Times(2)

		resolver, err := aggregator.NewPriorityConflictResolver([]string{"backend1", "backend2"})
		require.NoError(t, err)
		agg := aggregator.NewDefaultAggregator(mockBackendClient, resolver, nil)

		result, err := agg.AggregateCapabilities(ctx, createBackendsWithConflicts())
		require.NoError(t, err)

		// Should have 1 tool from backend1 (higher priority)
		assert.Equal(t, 1, len(result.Tools))
		assert.Equal(t, "create", result.Tools[0].Name)
		assert.Equal(t, "backend1", result.Tools[0].BackendID)
	})
}

// TestIntegration_AuditLogging tests that the vMCP server logs MCP operations
// when audit middleware is enabled.
// Note: This test does not use t.Parallel() because subtests share the same
// server instance and audit log file, and must run sequentially.
//
//nolint:paralleltest // Subtests must run sequentially as they share server state
func TestIntegration_AuditLogging(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	ctx := context.Background()

	// Create temp file for audit logs
	auditLogFile, err := os.CreateTemp("", "vmcp-audit-test-*.log")
	require.NoError(t, err)
	auditLogPath := auditLogFile.Name()
	auditLogFile.Close()
	t.Cleanup(func() {
		os.Remove(auditLogPath)
	})

	// Create audit config that writes to temp file
	auditConfig := &audit.Config{
		Component:           "vmcp-server-test",
		IncludeRequestData:  true,
		IncludeResponseData: false,
		MaxDataSize:         2048,
		LogFile:             auditLogPath,
	}

	// Create mock backend client
	mockBackendClient := mocks.NewMockBackendClient(ctrl)

	// Define backend capabilities
	backendCapabilities := &vmcp.CapabilityList{
		Tools: []vmcp.Tool{
			{
				Name:        "get_weather",
				Description: "Get weather information",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"location": map[string]any{"type": "string"},
					},
				},
				BackendID: "weather-service",
			},
		},
		Resources: []vmcp.Resource{
			{
				URI:         "weather://current",
				Name:        "Current Weather",
				Description: "Current weather data",
				MimeType:    "application/json",
				BackendID:   "weather-service",
			},
		},
		Prompts: []vmcp.Prompt{
			{
				Name:        "weather_summary",
				Description: "Generate weather summary",
				Arguments:   []vmcp.PromptArgument{},
				BackendID:   "weather-service",
			},
		},
	}

	// Mock backend responses
	mockBackendClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(backendCapabilities, nil).
		AnyTimes()

	mockBackendClient.EXPECT().
		CallTool(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(map[string]any{
			"result": "Sunny, 72°F",
		}, nil).
		AnyTimes()

	mockBackendClient.EXPECT().
		ReadResource(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]byte(`{"temp": 72, "condition": "sunny"}`), nil).
		AnyTimes()

	// Create backends
	backends := []vmcp.Backend{
		{
			ID:   "weather-service",
			Name: "Weather Service",
		},
	}

	// Create router
	rt := router.NewDefaultRouter()

	// Create discovery manager
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockDiscoveryMgr.EXPECT().
		Discover(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
			resolver := aggregator.NewPrefixConflictResolver("{workload}_")
			agg := aggregator.NewDefaultAggregator(mockBackendClient, resolver, nil)
			return agg.AggregateCapabilities(ctx, backends)
		}).
		AnyTimes()
	mockDiscoveryMgr.EXPECT().Stop().AnyTimes()

	// Helper function to read audit log file
	readAuditLog := func() string {
		data, err := os.ReadFile(auditLogPath)
		if err != nil {
			return ""
		}
		return string(data)
	}

	// Create server with audit config
	srv, err := server.New(ctx, &server.Config{
		Host:        "127.0.0.1",
		Port:        0, // Random port
		AuditConfig: auditConfig,
	}, rt, mockBackendClient, mockDiscoveryMgr, backends, nil)
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

	// Capture session ID for subsequent requests
	var sessionID string

	// Test 1: Initialize request should be logged
	t.Run("initialize request is logged", func(t *testing.T) {
		initReq := map[string]any{
			"method": "initialize",
			"params": map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"clientInfo": map[string]any{
					"name":    "audit-test-client",
					"version": "1.0.0",
				},
			},
		}

		reqBody, err := json.Marshal(initReq)
		require.NoError(t, err)

		resp, err := http.Post(baseURL+"/mcp", "application/json", bytes.NewReader(reqBody))
		require.NoError(t, err)
		defer resp.Body.Close()

		require.Equal(t, http.StatusOK, resp.StatusCode)

		// Capture session ID for subsequent tests
		sessionID = resp.Header.Get("Mcp-Session-Id")
		require.NotEmpty(t, sessionID, "Session ID should be returned")

		// Wait for audit event to be written
		time.Sleep(500 * time.Millisecond)

		// Verify audit log contains initialize event
		auditLog := readAuditLog()
		assert.Contains(t, auditLog, "vmcp-server-test", "Should contain component name")
		assert.Contains(t, auditLog, "\"method\":\"initialize\"", "Should log initialize method in request data")
		assert.Contains(t, auditLog, "audit-test-client", "Should capture client name")
	})

	// Test 2: Tool list request should be logged
	t.Run("tools/list request is logged", func(t *testing.T) {
		require.NotEmpty(t, sessionID, "Session ID must be set from initialize test")

		toolsReq := map[string]any{
			"method": "tools/list",
		}

		reqBody, err := json.Marshal(toolsReq)
		require.NoError(t, err)

		req, err := http.NewRequest("POST", baseURL+"/mcp", bytes.NewReader(reqBody))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Mcp-Session-Id", sessionID)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		// Wait for audit event
		time.Sleep(500 * time.Millisecond)

		auditLog := readAuditLog()
		assert.Contains(t, auditLog, "\"method\":\"tools/list\"", "Should log tools/list method in request data")
		assert.Contains(t, auditLog, "vmcp-server-test", "Should contain component name")
	})

	// Test 3: Tool call should be logged
	t.Run("tool call is logged", func(t *testing.T) {
		require.NotEmpty(t, sessionID, "Session ID must be set from initialize test")

		toolCallReq := map[string]any{
			"method": "tools/call",
			"params": map[string]any{
				"name": "weather-service_get_weather", // Prefix added by aggregator
				"arguments": map[string]any{
					"location": "San Francisco",
				},
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

		// Check response
		require.Equal(t, http.StatusOK, resp.StatusCode, "HTTP request should succeed")
		body, _ := io.ReadAll(resp.Body)
		t.Logf("tools/call response: %s", string(body))

		// Wait for audit event
		time.Sleep(500 * time.Millisecond)

		auditLog := readAuditLog()
		assert.Contains(t, auditLog, "\"method\":\"tools/call\"", "Should log tools/call method in request data")
		assert.Contains(t, auditLog, "get_weather", "Should capture tool name in request data")
		assert.Contains(t, auditLog, "San Francisco", "Should capture tool arguments in request data")
		assert.Contains(t, auditLog, "vmcp-server-test", "Should contain component name")
		assert.Contains(t, auditLog, "\"backend_name\":\"Weather Service\"", "Should capture backend routing decision")
	})

	// Test 4: Resource read should be logged
	t.Run("resource read is logged", func(t *testing.T) {
		require.NotEmpty(t, sessionID, "Session ID must be set from initialize test")

		resourceReq := map[string]any{
			"method": "resources/read",
			"params": map[string]any{
				"uri": "weather://current",
			},
		}

		reqBody, err := json.Marshal(resourceReq)
		require.NoError(t, err)

		req, err := http.NewRequest("POST", baseURL+"/mcp", bytes.NewReader(reqBody))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Mcp-Session-Id", sessionID)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		// Wait for audit event
		time.Sleep(500 * time.Millisecond)

		auditLog := readAuditLog()
		assert.Contains(t, auditLog, "\"method\":\"resources/read\"", "Should log resources/read method in request data")
		assert.Contains(t, auditLog, "weather://current", "Should capture resource URI in request data")
		assert.Contains(t, auditLog, "vmcp-server-test", "Should contain component name")
		assert.Contains(t, auditLog, "\"backend_name\":\"Weather Service\"", "Should capture backend routing decision")
	})

	// Test 5: Verify audit events have required fields
	t.Run("audit events contain required fields", func(t *testing.T) {
		// Get all audit logs
		auditLog := readAuditLog()

		// Split into individual log lines
		lines := strings.Split(strings.TrimSpace(auditLog), "\n")
		require.Greater(t, len(lines), 0, "Should have at least one audit event")

		// Parse first audit event
		var auditEvent map[string]any
		err := json.Unmarshal([]byte(lines[0]), &auditEvent)
		require.NoError(t, err, "Audit log should be valid JSON")

		// Verify required fields
		assert.Contains(t, auditEvent, "audit_id", "Should have audit_id")
		assert.Contains(t, auditEvent, "type", "Should have type")
		assert.Contains(t, auditEvent, "logged_at", "Should have logged_at")
		assert.Contains(t, auditEvent, "outcome", "Should have outcome")
		assert.Contains(t, auditEvent, "component", "Should have component")
		assert.Contains(t, auditEvent, "source", "Should have source")

		// Verify component value
		assert.Equal(t, "vmcp-server-test", auditEvent["component"])

		// Verify source has network information
		source, ok := auditEvent["source"].(map[string]any)
		require.True(t, ok, "Source should be an object")
		assert.Equal(t, "network", source["type"])
		assert.Contains(t, source, "value", "Source should have IP address")
	})
}

// TestIntegration_AuditLoggingWithAuth tests that the vMCP server audit logs capture user
// identity from authentication tokens.
//
//nolint:paralleltest // Uses dedicated server instance
func TestIntegration_AuditLoggingWithAuth(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	ctx := context.Background()

	// Create mock backend client
	mockBackendClient := mocks.NewMockBackendClient(ctrl)

	// Create mock discovery manager
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockDiscoveryMgr.EXPECT().
		Discover(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
			return &aggregator.AggregatedCapabilities{
				Tools: []vmcp.Tool{
					{
						Name:        "test_tool",
						Description: "A test tool",
						InputSchema: map[string]any{"type": "object"},
					},
				},
			}, nil
		}).
		AnyTimes()
	mockDiscoveryMgr.EXPECT().Stop().AnyTimes()

	backends := []vmcp.Backend{}

	// Create router
	rt := router.NewDefaultRouter()

	// Create identity middleware for auth
	identityMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity := &auth.Identity{
				Subject: "user-123",
				Name:    "John Doe",
				Email:   "john.doe@example.com",
				Claims: map[string]any{
					"client_name":    "mcp-client",
					"client_version": "2.0.0",
				},
			}
			ctx := auth.WithIdentity(r.Context(), identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}

	// Create temp file for audit logs
	auditLogFile, err := os.CreateTemp("", "vmcp-auth-audit-*.log")
	require.NoError(t, err)
	auditLogPath := auditLogFile.Name()
	auditLogFile.Close()
	defer os.Remove(auditLogPath)

	// Create audit config
	auditConfig := &audit.Config{
		Component:           "vmcp-auth-test",
		IncludeRequestData:  true,
		IncludeResponseData: true,
		LogFile:             auditLogPath,
	}

	// Create server with auth middleware and audit config
	srv, err := server.New(ctx, &server.Config{
		Host:           "127.0.0.1",
		Port:           0, // Let OS assign port
		AuditConfig:    auditConfig,
		AuthMiddleware: identityMiddleware,
	}, rt, mockBackendClient, mockDiscoveryMgr, backends, nil)
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

	// Make an MCP request (initialize)
	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"clientInfo": map[string]any{
				"name":    "auth-test-client",
				"version": "1.0.0",
			},
		},
	}
	reqBody, _ := json.Marshal(initReq)
	req, _ := http.NewRequest("POST", baseURL+"/mcp", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Wait for audit event to be written
	time.Sleep(500 * time.Millisecond)

	// Read and verify audit log
	auditData, err := os.ReadFile(auditLogPath)
	require.NoError(t, err)
	auditLog := string(auditData)

	// Verify user identity fields are captured
	assert.Contains(t, auditLog, "user-123", "Should capture user ID (subject)")
	assert.Contains(t, auditLog, "John Doe", "Should capture user name")
	assert.Contains(t, auditLog, "mcp-client", "Should capture client name from claims")
	assert.Contains(t, auditLog, "2.0.0", "Should capture client version from claims")

	// Parse the audit event and verify subjects structure
	lines := strings.Split(strings.TrimSpace(auditLog), "\n")
	require.Greater(t, len(lines), 0, "Should have at least one audit event")

	var auditEvent map[string]any
	err = json.Unmarshal([]byte(lines[0]), &auditEvent)
	require.NoError(t, err, "Audit log should be valid JSON")

	// Verify subjects field exists and has correct structure
	subjects, ok := auditEvent["subjects"].(map[string]any)
	require.True(t, ok, "Should have subjects field")
	assert.Equal(t, "user-123", subjects["user_id"], "Should have correct user_id")
	assert.Equal(t, "John Doe", subjects["user"], "Should have correct user name")
	assert.Equal(t, "mcp-client", subjects["client_name"], "Should have correct client_name")
	assert.Equal(t, "2.0.0", subjects["client_version"], "Should have correct client_version")
}
