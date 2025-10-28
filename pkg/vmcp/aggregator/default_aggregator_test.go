package aggregator

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
)

func TestDefaultAggregator_QueryCapabilities(t *testing.T) {
	t.Parallel()

	t.Run("successful query", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mocks.NewMockBackendClient(ctrl)
		backend := vmcp.Backend{
			ID:            "backend1",
			Name:          "Backend 1",
			BaseURL:       "http://localhost:8080",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
		}

		expectedCaps := &vmcp.CapabilityList{
			Tools: []vmcp.Tool{
				{
					Name:        "test_tool",
					Description: "A test tool",
					InputSchema: map[string]any{"type": "object"},
					BackendID:   "backend1",
				},
			},
			Resources: []vmcp.Resource{
				{
					URI:       "test://resource",
					Name:      "Test Resource",
					BackendID: "backend1",
				},
			},
			Prompts: []vmcp.Prompt{
				{
					Name:      "test_prompt",
					BackendID: "backend1",
				},
			},
			SupportsLogging:  true,
			SupportsSampling: false,
		}

		mockClient.EXPECT().
			ListCapabilities(gomock.Any(), gomock.Any()).
			Return(expectedCaps, nil).
			Times(1)

		agg := NewDefaultAggregator(mockClient)
		result, err := agg.QueryCapabilities(context.Background(), backend)

		require.NoError(t, err)
		assert.Equal(t, "backend1", result.BackendID)
		assert.Len(t, result.Tools, 1)
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
		backend := vmcp.Backend{
			ID:            "backend1",
			Name:          "Backend 1",
			BaseURL:       "http://localhost:8080",
			TransportType: "streamable-http",
		}

		mockClient.EXPECT().
			ListCapabilities(gomock.Any(), gomock.Any()).
			Return(nil, errors.New("connection failed")).
			Times(1)

		agg := NewDefaultAggregator(mockClient)
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
			{
				ID:            "backend1",
				Name:          "Backend 1",
				BaseURL:       "http://localhost:8080",
				TransportType: "streamable-http",
			},
			{
				ID:            "backend2",
				Name:          "Backend 2",
				BaseURL:       "http://localhost:8081",
				TransportType: "sse",
			},
		}

		caps1 := &vmcp.CapabilityList{
			Tools: []vmcp.Tool{
				{Name: "tool1", BackendID: "backend1"},
			},
		}

		caps2 := &vmcp.CapabilityList{
			Tools: []vmcp.Tool{
				{Name: "tool2", BackendID: "backend2"},
			},
		}

		mockClient.EXPECT().
			ListCapabilities(gomock.Any(), gomock.Any()).
			Return(caps1, nil).
			Times(1)

		mockClient.EXPECT().
			ListCapabilities(gomock.Any(), gomock.Any()).
			Return(caps2, nil).
			Times(1)

		agg := NewDefaultAggregator(mockClient)
		result, err := agg.QueryAllCapabilities(context.Background(), backends)

		require.NoError(t, err)
		assert.Len(t, result, 2)
		assert.Contains(t, result, "backend1")
		assert.Contains(t, result, "backend2")
	})

	t.Run("graceful handling of partial failures", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mocks.NewMockBackendClient(ctrl)
		backends := []vmcp.Backend{
			{ID: "backend1", BaseURL: "http://localhost:8080", TransportType: "streamable-http"},
			{ID: "backend2", BaseURL: "http://localhost:8081", TransportType: "streamable-http"},
		}

		caps1 := &vmcp.CapabilityList{
			Tools: []vmcp.Tool{{Name: "tool1", BackendID: "backend1"}},
		}

		// Mock will be called twice - we need to handle both backends
		// Use DoAndReturn to make one succeed and one fail based on the target
		mockClient.EXPECT().
			ListCapabilities(gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, target *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
				// Backend1 succeeds, backend2 fails
				if target.WorkloadID == "backend1" {
					return caps1, nil
				}
				return nil, errors.New("connection timeout")
			}).
			Times(2)

		agg := NewDefaultAggregator(mockClient)
		result, err := agg.QueryAllCapabilities(context.Background(), backends)

		// Should succeed with partial results
		require.NoError(t, err)
		assert.Len(t, result, 1)
		assert.Contains(t, result, "backend1", "backend1 should have succeeded")
		assert.NotContains(t, result, "backend2", "backend2 should have failed")
	})

	t.Run("all backends fail", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockClient := mocks.NewMockBackendClient(ctrl)
		backends := []vmcp.Backend{
			{ID: "backend1", BaseURL: "http://localhost:8080", TransportType: "streamable-http"},
		}

		mockClient.EXPECT().
			ListCapabilities(gomock.Any(), gomock.Any()).
			Return(nil, errors.New("connection failed")).
			Times(1)

		agg := NewDefaultAggregator(mockClient)
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

		agg := NewDefaultAggregator(nil)
		resolved, err := agg.ResolveConflicts(context.Background(), capabilities)

		require.NoError(t, err)
		assert.NotNil(t, resolved)
		// In Phase 1, we just collect tools - conflict is detected but first one wins
		assert.Contains(t, resolved.Tools, "tool1")
		assert.Contains(t, resolved.Tools, "tool2")
		assert.Contains(t, resolved.Tools, "shared_tool")
		// Shared tool should have one backend (first encountered)
		assert.Equal(t, "backend1", resolved.Tools["shared_tool"].BackendID)
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

		agg := NewDefaultAggregator(nil)
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

		agg := NewDefaultAggregator(nil)
		aggregated, err := agg.MergeCapabilities(context.Background(), resolved)

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
			{ID: "backend1", Name: "Backend 1", BaseURL: "http://localhost:8080", TransportType: "streamable-http"},
			{ID: "backend2", Name: "Backend 2", BaseURL: "http://localhost:8081", TransportType: "sse"},
		}

		caps1 := &vmcp.CapabilityList{
			Tools: []vmcp.Tool{
				{Name: "tool1", Description: "Tool 1", BackendID: "backend1", InputSchema: map[string]any{"type": "object"}},
			},
			Resources: []vmcp.Resource{
				{URI: "test://resource1", Name: "Resource 1", BackendID: "backend1"},
			},
			SupportsLogging: true,
		}

		caps2 := &vmcp.CapabilityList{
			Tools: []vmcp.Tool{
				{Name: "tool2", Description: "Tool 2", BackendID: "backend2", InputSchema: map[string]any{"type": "object"}},
			},
			SupportsSampling: true,
		}

		mockClient.EXPECT().
			ListCapabilities(gomock.Any(), gomock.Any()).
			Return(caps1, nil).
			Times(1)

		mockClient.EXPECT().
			ListCapabilities(gomock.Any(), gomock.Any()).
			Return(caps2, nil).
			Times(1)

		agg := NewDefaultAggregator(mockClient)
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
