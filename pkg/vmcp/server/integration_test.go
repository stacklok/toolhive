package server_test

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
	discoveryMocks "github.com/stacklok/toolhive/pkg/vmcp/discovery/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
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

	srv := server.New(&server.Config{
		Name:    "test-vmcp",
		Version: "1.0.0",
		Host:    "127.0.0.1",
		Port:    4484,
	}, rt, mockBackendClient, mockDiscoveryMgr, backends)

	// Validate server address
	assert.Equal(t, "127.0.0.1:4484", srv.Address())

	// Step 7: Start server and validate it's running
	serverCtx, cancelServer := context.WithCancel(ctx)
	t.Cleanup(cancelServer)

	// Start server in background
	serverErrCh := make(chan error, 1)
	go func() {
		if err := srv.Start(serverCtx); err != nil && err != context.Canceled {
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
