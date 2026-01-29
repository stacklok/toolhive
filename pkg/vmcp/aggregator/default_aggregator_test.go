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
			newTestBackend("backend1"),
			newTestBackend("backend2", withBackendURL("http://localhost:8081")),
		}

		caps1 := newTestCapabilityList(withTools(newTestTool("tool1", "backend1")))

		mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, target *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
				if target.WorkloadID == "backend1" {
					return caps1, nil
				}
				return nil, errors.New("connection timeout")
			}).Times(2)

		agg := NewDefaultAggregator(mockClient, nil, nil, nil)
		result, err := agg.QueryAllCapabilities(context.Background(), backends)

		require.NoError(t, err)
		require.Len(t, result, 1)
		assert.Contains(t, result, "backend1")
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

	t.Run("global excludeAllTools returns empty tools", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mocks.NewMockBackendClient(ctrl)
		backend := newTestBackend("backend1", withBackendName("Backend 1"))

		// Backend returns tools, but they should be excluded
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
		// Tools should be empty due to ExcludeAllTools
		assert.Len(t, result.Tools, 0)
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
