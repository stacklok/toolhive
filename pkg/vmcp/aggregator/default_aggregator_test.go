// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
)

const testBackendID1 = "backend1"

func TestDefaultAggregator_QueryCapabilities(t *testing.T) {
	t.Parallel()

	t.Run("successful query", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mocks.NewMockBackendClient(ctrl)
		backend := newTestBackend("backend1", withBackendName("Backend 1"))

		expectedCaps := newTestCapabilityList(
			withTools(newTestTool("test_tool", "backend1")),
			withResources(newTestResource("test://resource", "backend1")),
			withPrompts(newTestPrompt("test_prompt", "backend1")),
			withLogging(true))

		mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).Return(expectedCaps, nil)

		agg := NewDefaultAggregator(mockClient, nil, nil, nil)
		result, err := agg.QueryCapabilities(context.Background(), backend)

		require.NoError(t, err)
		assert.Equal(t, "backend1", result.BackendID)
		require.Len(t, result.Tools, 1)
		assert.Equal(t, "test_tool", result.Tools[0].Name)
		assert.Len(t, result.Resources, 1)
		assert.Len(t, result.Prompts, 1)
		assert.True(t, result.SupportsLogging)
		assert.False(t, result.SupportsSampling)
	})

	t.Run("backend query failure", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mocks.NewMockBackendClient(ctrl)
		backend := newTestBackend("backend1", withBackendName("Backend 1"))

		mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).
			Return(nil, errors.New("connection failed"))

		agg := NewDefaultAggregator(mockClient, nil, nil, nil)
		result, err := agg.QueryCapabilities(context.Background(), backend)

		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "backend1")
	})
}

func TestDefaultAggregator_QueryAllCapabilities(t *testing.T) {
	t.Parallel()

	t.Run("query multiple backends successfully", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mocks.NewMockBackendClient(ctrl)
		backends := []vmcp.Backend{
			newTestBackend("backend1", withBackendName("Backend 1")),
			newTestBackend("backend2", withBackendName("Backend 2"),
				withBackendURL("http://localhost:8081"),
				withBackendTransport("sse")),
		}

		caps1 := newTestCapabilityList(withTools(newTestTool("tool1", "backend1")))
		caps2 := newTestCapabilityList(withTools(newTestTool("tool2", "backend2")))

		mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).Return(caps1, nil)
		mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).Return(caps2, nil)

		agg := NewDefaultAggregator(mockClient, nil, nil, nil)
		result, err := agg.QueryAllCapabilities(context.Background(), backends)

		require.NoError(t, err)
		require.Len(t, result, 2)
		assert.Contains(t, result, "backend1")
		assert.Contains(t, result, "backend2")
	})

	t.Run("graceful handling of partial failures", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mocks.NewMockBackendClient(ctrl)
		backends := []vmcp.Backend{
			newTestBackend(testBackendID1),
			newTestBackend("backend2", withBackendURL("http://localhost:8081")),
		}

		caps1 := newTestCapabilityList(withTools(newTestTool("tool1", testBackendID1)))

		mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, target *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
				if target.WorkloadID == testBackendID1 {
					return caps1, nil
				}
				return nil, errors.New("connection timeout")
			}).Times(2)

		agg := NewDefaultAggregator(mockClient, nil, nil, nil)
		result, err := agg.QueryAllCapabilities(context.Background(), backends)

		require.NoError(t, err)
		require.Len(t, result, 1)
		assert.Contains(t, result, testBackendID1)
		assert.NotContains(t, result, "backend2")
	})

	t.Run("all backends fail", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mocks.NewMockBackendClient(ctrl)
		backends := []vmcp.Backend{newTestBackend("backend1")}

		mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).
			Return(nil, errors.New("connection failed"))

		agg := NewDefaultAggregator(mockClient, nil, nil, nil)
		result, err := agg.QueryAllCapabilities(context.Background(), backends)

		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "no backends returned capabilities")
	})
}

func TestDefaultAggregator_ResolveConflicts(t *testing.T) {
	t.Parallel()

	t.Run("basic conflict detection", func(t *testing.T) {
		t.Parallel()
		capabilities := map[string]*BackendCapabilities{
			"backend1": {
				BackendID: "backend1",
				Tools: []vmcp.Tool{
					{Name: "tool1", Description: "Tool 1 from backend1", BackendID: "backend1"},
					{Name: "shared_tool", Description: "Shared from backend1", BackendID: "backend1"},
				},
			},
			"backend2": {
				BackendID: "backend2",
				Tools: []vmcp.Tool{
					{Name: "tool2", Description: "Tool 2 from backend2", BackendID: "backend2"},
					{Name: "shared_tool", Description: "Shared from backend2", BackendID: "backend2"},
				},
			},
		}

		agg := NewDefaultAggregator(nil, nil, nil, nil)
		resolved, err := agg.ResolveConflicts(context.Background(), capabilities)

		require.NoError(t, err)
		assert.NotNil(t, resolved)
		// In Phase 1, we just collect tools - conflict is detected but first one wins
		assert.Contains(t, resolved.Tools, "tool1")
		assert.Contains(t, resolved.Tools, "tool2")
		assert.Contains(t, resolved.Tools, "shared_tool")
		// Shared tool should have one backend (whichever was encountered first in map iteration)
		// Map iteration order is non-deterministic, so accept either backend
		sharedToolBackend := resolved.Tools["shared_tool"].BackendID
		assert.True(t, sharedToolBackend == "backend1" || sharedToolBackend == "backend2",
			"shared_tool should belong to either backend1 or backend2, got: %s", sharedToolBackend)
	})

	t.Run("no conflicts", func(t *testing.T) {
		t.Parallel()
		capabilities := map[string]*BackendCapabilities{
			"backend1": {
				BackendID: "backend1",
				Tools: []vmcp.Tool{
					{Name: "unique1", BackendID: "backend1"},
				},
			},
			"backend2": {
				BackendID: "backend2",
				Tools: []vmcp.Tool{
					{Name: "unique2", BackendID: "backend2"},
				},
			},
		}

		agg := NewDefaultAggregator(nil, nil, nil, nil)
		resolved, err := agg.ResolveConflicts(context.Background(), capabilities)

		require.NoError(t, err)
		assert.Len(t, resolved.Tools, 2)
		assert.Contains(t, resolved.Tools, "unique1")
		assert.Contains(t, resolved.Tools, "unique2")
	})
}

func TestDefaultAggregator_MergeCapabilities(t *testing.T) {
	t.Parallel()

	t.Run("merge resolved capabilities", func(t *testing.T) {
		t.Parallel()
		resolved := &ResolvedCapabilities{
			Tools: map[string]*ResolvedTool{
				"tool1": {
					ResolvedName: "tool1",
					OriginalName: "tool1",
					Description:  "Tool 1",
					BackendID:    "backend1",
				},
				"tool2": {
					ResolvedName: "tool2",
					OriginalName: "tool2",
					Description:  "Tool 2",
					BackendID:    "backend2",
				},
			},
			Resources: []vmcp.Resource{
				{URI: "test://resource1", BackendID: "backend1"},
			},
			Prompts: []vmcp.Prompt{
				{Name: "prompt1", BackendID: "backend1"},
			},
			SupportsLogging:  true,
			SupportsSampling: false,
		}

		// Create registry with test backends
		backends := []vmcp.Backend{
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
				TransportType: "sse",
				HealthStatus:  vmcp.BackendHealthy,
			},
		}
		registry := vmcp.NewImmutableRegistry(backends)

		agg := NewDefaultAggregator(nil, nil, nil, nil)
		aggregated, err := agg.MergeCapabilities(context.Background(), resolved, registry)

		require.NoError(t, err)
		assert.Len(t, aggregated.Tools, 2)
		assert.Len(t, aggregated.Resources, 1)
		assert.Len(t, aggregated.Prompts, 1)
		assert.True(t, aggregated.SupportsLogging)
		assert.False(t, aggregated.SupportsSampling)

		// Check routing table
		assert.NotNil(t, aggregated.RoutingTable)
		assert.Contains(t, aggregated.RoutingTable.Tools, "tool1")
		assert.Contains(t, aggregated.RoutingTable.Tools, "tool2")
		assert.Contains(t, aggregated.RoutingTable.Resources, "test://resource1")
		assert.Contains(t, aggregated.RoutingTable.Prompts, "prompt1")

		// Verify routing table has full backend information
		tool1Target := aggregated.RoutingTable.Tools["tool1"]
		assert.NotNil(t, tool1Target)
		assert.Equal(t, "backend1", tool1Target.WorkloadID)
		assert.Equal(t, "Backend 1", tool1Target.WorkloadName)
		assert.Equal(t, "http://backend1:8080", tool1Target.BaseURL)
		assert.Equal(t, "streamable-http", tool1Target.TransportType)
		assert.Equal(t, vmcp.BackendHealthy, tool1Target.HealthStatus)

		tool2Target := aggregated.RoutingTable.Tools["tool2"]
		assert.NotNil(t, tool2Target)
		assert.Equal(t, "backend2", tool2Target.WorkloadID)
		assert.Equal(t, "Backend 2", tool2Target.WorkloadName)
		assert.Equal(t, "http://backend2:8080", tool2Target.BaseURL)
		assert.Equal(t, "sse", tool2Target.TransportType)

		// Check metadata
		assert.Equal(t, 2, aggregated.Metadata.ToolCount)
		assert.Equal(t, 1, aggregated.Metadata.ResourceCount)
		assert.Equal(t, 1, aggregated.Metadata.PromptCount)
	})
}

func TestDefaultAggregator_AggregateCapabilities(t *testing.T) {
	t.Parallel()

	t.Run("full aggregation pipeline", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mocks.NewMockBackendClient(ctrl)
		backends := []vmcp.Backend{
			newTestBackend("backend1", withBackendName("Backend 1")),
			newTestBackend("backend2", withBackendName("Backend 2"),
				withBackendURL("http://localhost:8081"),
				withBackendTransport("sse")),
		}

		caps1 := newTestCapabilityList(
			withTools(newTestTool("tool1", "backend1")),
			withResources(newTestResource("test://resource1", "backend1")),
			withLogging(true))

		caps2 := newTestCapabilityList(
			withTools(newTestTool("tool2", "backend2")),
			withSampling(true))

		mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).Return(caps1, nil)
		mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).Return(caps2, nil)

		agg := NewDefaultAggregator(mockClient, nil, nil, nil)
		result, err := agg.AggregateCapabilities(context.Background(), backends)

		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Len(t, result.Tools, 2)
		assert.Len(t, result.Resources, 1)
		assert.True(t, result.SupportsLogging)
		assert.True(t, result.SupportsSampling)
		assert.Equal(t, 2, result.Metadata.BackendCount)
		assert.Equal(t, 2, result.Metadata.ToolCount)
		assert.Equal(t, 1, result.Metadata.ResourceCount)
	})
}

func TestDefaultAggregator_ExcludeAllTools(t *testing.T) {
	t.Parallel()

	// NOTE: ExcludeAll is applied in MergeCapabilities, NOT in QueryCapabilities.
	// This allows the routing table to contain all tools (for composite tools)
	// while only filtering the advertised tools list.

	t.Run("QueryCapabilities returns all tools even with global excludeAllTools", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mocks.NewMockBackendClient(ctrl)
		backend := newTestBackend("backend1", withBackendName("Backend 1"))

		// Backend returns tools - they should still be returned by QueryCapabilities
		// because ExcludeAll is applied later in MergeCapabilities
		expectedCaps := newTestCapabilityList(
			withTools(newTestTool("test_tool", "backend1")),
			withResources(newTestResource("test://resource", "backend1")),
			withPrompts(newTestPrompt("test_prompt", "backend1")),
			withLogging(true))

		mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).Return(expectedCaps, nil)

		// Create aggregator with ExcludeAllTools: true
		aggregationConfig := &config.AggregationConfig{
			ExcludeAllTools: true,
		}
		agg := NewDefaultAggregator(mockClient, nil, aggregationConfig, nil)
		result, err := agg.QueryCapabilities(context.Background(), backend)

		require.NoError(t, err)
		assert.Equal(t, "backend1", result.BackendID)
		// Tools should still be present (ExcludeAll is applied in MergeCapabilities)
		assert.Len(t, result.Tools, 1)
		assert.Equal(t, "test_tool", result.Tools[0].Name)
		// Resources and prompts should be preserved
		assert.Len(t, result.Resources, 1)
		assert.Len(t, result.Prompts, 1)
		assert.True(t, result.SupportsLogging)
	})

	t.Run("global excludeAllTools false allows tools through", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mocks.NewMockBackendClient(ctrl)
		backend := newTestBackend("backend1", withBackendName("Backend 1"))

		expectedCaps := newTestCapabilityList(
			withTools(newTestTool("test_tool", "backend1")))

		mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).Return(expectedCaps, nil)

		// Create aggregator with ExcludeAllTools: false (default)
		aggregationConfig := &config.AggregationConfig{
			ExcludeAllTools: false,
		}
		agg := NewDefaultAggregator(mockClient, nil, aggregationConfig, nil)
		result, err := agg.QueryCapabilities(context.Background(), backend)

		require.NoError(t, err)
		// Tools should come through
		assert.Len(t, result.Tools, 1)
		assert.Equal(t, "test_tool", result.Tools[0].Name)
	})

	t.Run("nil aggregationConfig allows tools through", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mocks.NewMockBackendClient(ctrl)
		backend := newTestBackend("backend1", withBackendName("Backend 1"))

		expectedCaps := newTestCapabilityList(
			withTools(newTestTool("test_tool", "backend1")))

		mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).Return(expectedCaps, nil)

		// Create aggregator with nil aggregationConfig (default behavior)
		agg := NewDefaultAggregator(mockClient, nil, nil, nil)
		result, err := agg.QueryCapabilities(context.Background(), backend)

		require.NoError(t, err)
		// Tools should come through
		assert.Len(t, result.Tools, 1)
		assert.Equal(t, "test_tool", result.Tools[0].Name)
	})
}

func TestDefaultAggregator_ExcludeAllPreservesRoutingTableForCompositeTools(t *testing.T) {
	t.Parallel()

	// This test verifies that ExcludeAll only affects the advertised tools list,
	// NOT the routing table. This is important because composite tools need to
	// route to backend tools that may be excluded from direct client access.
	//
	// Use case: A vMCP server may want to hide raw backend tools from MCP clients
	// (using ExcludeAll) while still allowing curated composite tool workflows
	// to use those backend tools internally.

	t.Run("per-workload excludeAll preserves routing table for composite tools", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mocks.NewMockBackendClient(ctrl)
		backends := []vmcp.Backend{
			newTestBackend("github", withBackendName("GitHub")),
		}

		// Backend has tools that should be available for composite tools
		caps := newTestCapabilityList(
			withTools(
				newTestTool("create_issue", "github"),
				newTestTool("list_issues", "github"),
			),
		)

		mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).Return(caps, nil)

		// Configure ExcludeAll for the github backend
		aggregationConfig := &config.AggregationConfig{
			Tools: []*config.WorkloadToolConfig{
				{
					Workload:   "github",
					ExcludeAll: true,
				},
			},
		}

		agg := NewDefaultAggregator(mockClient, nil, aggregationConfig, nil)
		result, err := agg.AggregateCapabilities(context.Background(), backends)

		require.NoError(t, err)
		assert.NotNil(t, result)

		// Advertised tools should be empty (excluded from MCP clients)
		assert.Empty(t, result.Tools, "ExcludeAll should hide tools from MCP clients")

		// BUT the routing table should still contain the tools (for composite tools)
		assert.NotNil(t, result.RoutingTable)
		assert.Contains(t, result.RoutingTable.Tools, "create_issue",
			"Routing table should contain excluded tools for composite tool use")
		assert.Contains(t, result.RoutingTable.Tools, "list_issues",
			"Routing table should contain excluded tools for composite tool use")

		// Verify the routing targets are properly configured
		createIssueTarget := result.RoutingTable.Tools["create_issue"]
		assert.NotNil(t, createIssueTarget)
		assert.Equal(t, "github", createIssueTarget.WorkloadID)
	})

	t.Run("global excludeAllTools preserves routing table for composite tools", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mocks.NewMockBackendClient(ctrl)
		backends := []vmcp.Backend{
			newTestBackend("slack", withBackendName("Slack")),
		}

		// Backend has tools
		caps := newTestCapabilityList(
			withTools(
				newTestTool("send_message", "slack"),
				newTestTool("list_channels", "slack"),
			),
		)

		mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).Return(caps, nil)

		// Configure global ExcludeAllTools
		aggregationConfig := &config.AggregationConfig{
			ExcludeAllTools: true,
		}

		agg := NewDefaultAggregator(mockClient, nil, aggregationConfig, nil)
		result, err := agg.AggregateCapabilities(context.Background(), backends)

		require.NoError(t, err)
		assert.NotNil(t, result)

		// Advertised tools should be empty
		assert.Empty(t, result.Tools, "Global ExcludeAllTools should hide all tools from MCP clients")

		// BUT routing table should still contain tools for composite tools
		assert.NotNil(t, result.RoutingTable)
		assert.Contains(t, result.RoutingTable.Tools, "send_message",
			"Routing table should contain globally excluded tools for composite tool use")
		assert.Contains(t, result.RoutingTable.Tools, "list_channels",
			"Routing table should contain globally excluded tools for composite tool use")
	})
}

// TestDefaultAggregator_FilterRemovesToolsFromRoutingTable demonstrates the bug where
// Filter removes tools from BOTH the advertised list AND the routing table, unlike
// ExcludeAll which only removes from the advertised list.
//
// This is a bug - Filter should behave like ExcludeAll and preserve tools in the
// routing table so composite tools can still use them.
// See: https://github.com/stacklok/toolhive/issues/3636
func TestDefaultAggregator_FilterPreservesRoutingTableForCompositeTools(t *testing.T) {
	t.Parallel()

	t.Run("filter hides tools from MCP clients but preserves routing table for composite tools", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mocks.NewMockBackendClient(ctrl)
		backends := []vmcp.Backend{
			newTestBackend("arxiv", withBackendName("ArXiv")),
		}

		// Backend has multiple tools
		caps := newTestCapabilityList(
			withTools(
				newTestTool("search_papers", "arxiv"),
				newTestTool("download_paper", "arxiv"),
				newTestTool("read_paper", "arxiv"),
			),
		)

		mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).Return(caps, nil)

		// Configure Filter to only expose "research_topic" (a composite tool name)
		// This simulates the user's use case from issue #3636
		aggregationConfig := &config.AggregationConfig{
			Tools: []*config.WorkloadToolConfig{
				{
					Workload: "arxiv",
					// Filter to only show a composite tool (not the backend tools)
					// Note: "research_topic" wouldn't match any backend tool
					Filter: []string{"research_topic"},
				},
			},
		}

		agg := NewDefaultAggregator(mockClient, nil, aggregationConfig, nil)
		result, err := agg.AggregateCapabilities(context.Background(), backends)

		require.NoError(t, err)
		assert.NotNil(t, result)

		// Advertised tools should be empty (filtered out) - Filter hides from MCP clients
		assert.Empty(t, result.Tools, "Filter should hide tools from MCP clients")

		// CORRECT: The routing table DOES contain the tools for composite tool use
		// (Fix for issue #3636 - Filter now behaves like ExcludeAll for routing)
		assert.NotNil(t, result.RoutingTable)

		// Filtered tools ARE in the routing table, so composite tools CAN use them
		assert.Contains(t, result.RoutingTable.Tools, "search_papers",
			"Filter preserves tools in routing table for composite tools")
		assert.Contains(t, result.RoutingTable.Tools, "download_paper",
			"Filter preserves tools in routing table for composite tools")
		assert.Contains(t, result.RoutingTable.Tools, "read_paper",
			"Filter preserves tools in routing table for composite tools")

		// Routing table has all tools available for composite workflows
		assert.Len(t, result.RoutingTable.Tools, 3,
			"Filter keeps all tools in routing table for composite tools")
	})

	t.Run("contrast with excludeAll which preserves routing table", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mocks.NewMockBackendClient(ctrl)
		backends := []vmcp.Backend{
			newTestBackend("arxiv", withBackendName("ArXiv")),
		}

		// Same backend with same tools
		caps := newTestCapabilityList(
			withTools(
				newTestTool("search_papers", "arxiv"),
				newTestTool("download_paper", "arxiv"),
				newTestTool("read_paper", "arxiv"),
			),
		)

		mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).Return(caps, nil)

		// Use ExcludeAll instead of Filter - this is the workaround
		aggregationConfig := &config.AggregationConfig{
			Tools: []*config.WorkloadToolConfig{
				{
					Workload:   "arxiv",
					ExcludeAll: true,
				},
			},
		}

		agg := NewDefaultAggregator(mockClient, nil, aggregationConfig, nil)
		result, err := agg.AggregateCapabilities(context.Background(), backends)

		require.NoError(t, err)
		assert.NotNil(t, result)

		// Advertised tools should be empty (excluded from MCP clients)
		assert.Empty(t, result.Tools, "ExcludeAll should hide tools from MCP clients")

		// CORRECT: The routing table DOES contain the tools for composite tool use
		assert.NotNil(t, result.RoutingTable)
		assert.Contains(t, result.RoutingTable.Tools, "search_papers",
			"ExcludeAll preserves tools in routing table for composite tools")
		assert.Contains(t, result.RoutingTable.Tools, "download_paper",
			"ExcludeAll preserves tools in routing table for composite tools")
		assert.Contains(t, result.RoutingTable.Tools, "read_paper",
			"ExcludeAll preserves tools in routing table for composite tools")

		// Routing table has all tools available for composite workflows
		assert.Len(t, result.RoutingTable.Tools, 3,
			"ExcludeAll keeps all tools in routing table for composite tools")
	})

	t.Run("filter with partial matches advertises only matching tools", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mocks.NewMockBackendClient(ctrl)
		backends := []vmcp.Backend{
			newTestBackend("arxiv", withBackendName("ArXiv")),
		}

		// Backend has multiple tools
		caps := newTestCapabilityList(
			withTools(
				newTestTool("search_papers", "arxiv"),
				newTestTool("download_paper", "arxiv"),
				newTestTool("read_paper", "arxiv"),
			),
		)

		mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).Return(caps, nil)

		// Filter to only expose search_papers (partial match)
		aggregationConfig := &config.AggregationConfig{
			Tools: []*config.WorkloadToolConfig{
				{
					Workload: "arxiv",
					Filter:   []string{"search_papers"},
				},
			},
		}

		agg := NewDefaultAggregator(mockClient, nil, aggregationConfig, nil)
		result, err := agg.AggregateCapabilities(context.Background(), backends)

		require.NoError(t, err)
		assert.NotNil(t, result)

		// Only search_papers should be advertised
		assert.Len(t, result.Tools, 1, "Only matching tool should be advertised")
		assert.Equal(t, "search_papers", result.Tools[0].Name)

		// ALL tools should still be in routing table for composite tools
		assert.NotNil(t, result.RoutingTable)
		assert.Contains(t, result.RoutingTable.Tools, "search_papers")
		assert.Contains(t, result.RoutingTable.Tools, "download_paper")
		assert.Contains(t, result.RoutingTable.Tools, "read_paper")
		assert.Len(t, result.RoutingTable.Tools, 3,
			"All tools should be in routing table regardless of filter")
	})

	t.Run("global excludeAllTools takes precedence over per-workload filter", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mocks.NewMockBackendClient(ctrl)
		backends := []vmcp.Backend{
			newTestBackend("arxiv", withBackendName("ArXiv")),
		}

		caps := newTestCapabilityList(
			withTools(
				newTestTool("search_papers", "arxiv"),
				newTestTool("download_paper", "arxiv"),
			),
		)

		mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).Return(caps, nil)

		// Global ExcludeAllTools + per-workload Filter
		// ExcludeAllTools should take precedence
		aggregationConfig := &config.AggregationConfig{
			ExcludeAllTools: true, // Global exclusion
			Tools: []*config.WorkloadToolConfig{
				{
					Workload: "arxiv",
					Filter:   []string{"search_papers"}, // Would allow search_papers
				},
			},
		}

		agg := NewDefaultAggregator(mockClient, nil, aggregationConfig, nil)
		result, err := agg.AggregateCapabilities(context.Background(), backends)

		require.NoError(t, err)
		assert.NotNil(t, result)

		// NO tools should be advertised because global ExcludeAllTools takes precedence
		assert.Empty(t, result.Tools,
			"Global ExcludeAllTools should take precedence over per-workload Filter")

		// ALL tools should still be in routing table
		assert.Len(t, result.RoutingTable.Tools, 2)
	})
}

func TestDefaultAggregator_ProcessPreQueriedCapabilities(t *testing.T) {
	t.Parallel()

	// newTarget is a helper that builds a minimal BackendTarget for a given backend.
	newTarget := func(backendID string) *vmcp.BackendTarget {
		return &vmcp.BackendTarget{
			WorkloadID:    backendID,
			WorkloadName:  backendID + "-name",
			BaseURL:       "http://" + backendID + ":8080",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
		}
	}

	t.Run("happy path: tools appear in both advertised list and routing table", func(t *testing.T) {
		t.Parallel()

		toolsByBackend := map[string][]vmcp.Tool{
			"backend1": {newTestTool("tool1", "backend1")},
			"backend2": {newTestTool("tool2", "backend2")},
		}
		targets := map[string]*vmcp.BackendTarget{
			"backend1": newTarget("backend1"),
			"backend2": newTarget("backend2"),
		}

		agg := NewDefaultAggregator(nil, nil, nil, nil)
		advertised, routingTable, err := agg.ProcessPreQueriedCapabilities(
			context.Background(), toolsByBackend, targets,
		)

		require.NoError(t, err)
		// Both tools must be advertised.
		advertisedNames := make([]string, 0, len(advertised))
		for _, t := range advertised {
			advertisedNames = append(advertisedNames, t.Name)
		}
		assert.Contains(t, advertisedNames, "tool1")
		assert.Contains(t, advertisedNames, "tool2")
		// Both tools must be in the routing table.
		assert.Contains(t, routingTable, "tool1")
		assert.Contains(t, routingTable, "tool2")
		assert.Equal(t, "backend1", routingTable["tool1"].WorkloadID)
		assert.Equal(t, "backend2", routingTable["tool2"].WorkloadID)
	})

	t.Run("OriginalCapabilityName is set in routing table entries", func(t *testing.T) {
		t.Parallel()

		toolsByBackend := map[string][]vmcp.Tool{
			"backend1": {newTestTool("my_tool", "backend1")},
		}
		targets := map[string]*vmcp.BackendTarget{
			"backend1": newTarget("backend1"),
		}

		agg := NewDefaultAggregator(nil, nil, nil, nil)
		_, routingTable, err := agg.ProcessPreQueriedCapabilities(
			context.Background(), toolsByBackend, targets,
		)

		require.NoError(t, err)
		require.Contains(t, routingTable, "my_tool")
		// OriginalCapabilityName must be set so GetBackendCapabilityName() works correctly.
		assert.Equal(t, "my_tool", routingTable["my_tool"].OriginalCapabilityName,
			"OriginalCapabilityName must be wired to the original backend tool name")
	})

	t.Run("override rename: routing table keyed by overridden name with OriginalCapabilityName set", func(t *testing.T) {
		t.Parallel()

		aggCfg := &config.AggregationConfig{
			Tools: []*config.WorkloadToolConfig{
				{
					Workload: "backend1",
					Overrides: map[string]*config.ToolOverride{
						"raw_tool": {Name: "fancy_tool"},
					},
				},
			},
		}

		toolsByBackend := map[string][]vmcp.Tool{
			"backend1": {{Name: "raw_tool", Description: "raw", BackendID: "backend1"}},
		}
		targets := map[string]*vmcp.BackendTarget{
			"backend1": newTarget("backend1"),
		}

		agg := NewDefaultAggregator(nil, nil, aggCfg, nil)
		advertised, routingTable, err := agg.ProcessPreQueriedCapabilities(
			context.Background(), toolsByBackend, targets,
		)

		require.NoError(t, err)
		// Routing table must use the overridden name as the key.
		require.Contains(t, routingTable, "fancy_tool",
			"routing table should be keyed by the overridden tool name")
		assert.NotContains(t, routingTable, "raw_tool",
			"pre-override name should not appear as a routing table key")
		// OriginalCapabilityName must be the actual backend name (pre-override) so that
		// GetBackendCapabilityName translates the resolved name back to what the backend
		// actually exposes. Forwarding the overridden user-visible name ("fancy_tool")
		// would cause the backend call to fail.
		assert.Equal(t, "raw_tool", routingTable["fancy_tool"].OriginalCapabilityName,
			"OriginalCapabilityName must be the actual backend capability name, not the overridden name")
		// Advertised list must also use the overridden name.
		require.Len(t, advertised, 1)
		assert.Equal(t, "fancy_tool", advertised[0].Name)
	})

	t.Run("conflict resolution: one tool wins when two backends share a name", func(t *testing.T) {
		t.Parallel()

		toolsByBackend := map[string][]vmcp.Tool{
			"backend1": {newTestTool("shared", "backend1")},
			"backend2": {newTestTool("shared", "backend2")},
		}
		targets := map[string]*vmcp.BackendTarget{
			"backend1": newTarget("backend1"),
			"backend2": newTarget("backend2"),
		}

		agg := NewDefaultAggregator(nil, nil, nil, nil)
		advertised, routingTable, err := agg.ProcessPreQueriedCapabilities(
			context.Background(), toolsByBackend, targets,
		)

		require.NoError(t, err)
		// Default resolver: one backend wins; the key appears exactly once.
		assert.Contains(t, routingTable, "shared",
			"shared tool must still be in the routing table")
		winnerBackend := routingTable["shared"].WorkloadID
		assert.True(t, winnerBackend == "backend1" || winnerBackend == "backend2",
			"winning backend must be either backend1 or backend2, got: %s", winnerBackend)
		// Exactly one advertised entry for the shared name.
		count := 0
		for _, tool := range advertised {
			if tool.Name == "shared" {
				count++
			}
		}
		assert.Equal(t, 1, count, "shared tool should appear exactly once in the advertised list")
	})

	t.Run("global ExcludeAllTools: routing table populated, advertised list empty", func(t *testing.T) {
		t.Parallel()

		aggCfg := &config.AggregationConfig{ExcludeAllTools: true}

		toolsByBackend := map[string][]vmcp.Tool{
			"backend1": {newTestTool("tool1", "backend1"), newTestTool("tool2", "backend1")},
		}
		targets := map[string]*vmcp.BackendTarget{
			"backend1": newTarget("backend1"),
		}

		agg := NewDefaultAggregator(nil, nil, aggCfg, nil)
		advertised, routingTable, err := agg.ProcessPreQueriedCapabilities(
			context.Background(), toolsByBackend, targets,
		)

		require.NoError(t, err)
		assert.Empty(t, advertised,
			"ExcludeAllTools must produce an empty advertised list")
		// Tools must still be routable (composite tools need them).
		assert.Contains(t, routingTable, "tool1",
			"excluded tools must remain in the routing table for composite tool use")
		assert.Contains(t, routingTable, "tool2",
			"excluded tools must remain in the routing table for composite tool use")
	})

	t.Run("per-workload filter: matching tools advertised, non-matching tools routing-table-only", func(t *testing.T) {
		t.Parallel()

		aggCfg := &config.AggregationConfig{
			Tools: []*config.WorkloadToolConfig{
				{
					Workload: "backend1",
					Filter:   []string{"allowed_tool"},
				},
			},
		}

		toolsByBackend := map[string][]vmcp.Tool{
			"backend1": {
				newTestTool("allowed_tool", "backend1"),
				newTestTool("hidden_tool", "backend1"),
			},
		}
		targets := map[string]*vmcp.BackendTarget{
			"backend1": newTarget("backend1"),
		}

		agg := NewDefaultAggregator(nil, nil, aggCfg, nil)
		advertised, routingTable, err := agg.ProcessPreQueriedCapabilities(
			context.Background(), toolsByBackend, targets,
		)

		require.NoError(t, err)
		// Only the allowed tool is advertised.
		advertisedNames := make([]string, 0, len(advertised))
		for _, tool := range advertised {
			advertisedNames = append(advertisedNames, tool.Name)
		}
		assert.Equal(t, []string{"allowed_tool"}, advertisedNames,
			"only tools matching the filter should be advertised")
		// Both tools remain routable (composite tools can call hidden_tool).
		assert.Contains(t, routingTable, "allowed_tool",
			"filtered-in tool should be in routing table")
		assert.Contains(t, routingTable, "hidden_tool",
			"filtered-out tool must still be in routing table for composite tool use")
	})

	t.Run("missing target: tool skipped when backend has no entry in targets map", func(t *testing.T) {
		t.Parallel()

		toolsByBackend := map[string][]vmcp.Tool{
			"backend1": {newTestTool("tool1", "backend1")},
			"backend2": {newTestTool("tool2", "backend2")}, // no matching target
		}
		targets := map[string]*vmcp.BackendTarget{
			"backend1": newTarget("backend1"),
			// backend2 intentionally omitted
		}

		agg := NewDefaultAggregator(nil, nil, nil, nil)
		advertised, routingTable, err := agg.ProcessPreQueriedCapabilities(
			context.Background(), toolsByBackend, targets,
		)

		require.NoError(t, err)
		// backend1's tool is present in both lists.
		assert.Contains(t, routingTable, "tool1")
		advertisedNames := make([]string, 0, len(advertised))
		for _, tool := range advertised {
			advertisedNames = append(advertisedNames, tool.Name)
		}
		assert.Contains(t, advertisedNames, "tool1")
		// backend2's tool is absent because no target was provided.
		assert.NotContains(t, routingTable, "tool2",
			"tool from backend with no target must be absent from routing table")
		assert.NotContains(t, advertisedNames, "tool2",
			"tool from backend with no target must be absent from advertised list")
	})

	t.Run("empty input: returns empty results without error", func(t *testing.T) {
		t.Parallel()

		agg := NewDefaultAggregator(nil, nil, nil, nil)
		advertised, routingTable, err := agg.ProcessPreQueriedCapabilities(
			context.Background(),
			map[string][]vmcp.Tool{},
			map[string]*vmcp.BackendTarget{},
		)

		require.NoError(t, err)
		assert.Empty(t, advertised)
		assert.Empty(t, routingTable)
	})
}
