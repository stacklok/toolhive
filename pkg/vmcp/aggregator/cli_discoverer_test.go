package aggregator

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/groups/mocks"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/vmcp"
	workloadmocks "github.com/stacklok/toolhive/pkg/workloads/mocks"
)

const testGroupName = "test-group"

func TestCLIBackendDiscoverer_Discover(t *testing.T) {
	t.Parallel()

	t.Run("successful discovery with multiple backends", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		groupName := testGroupName

		// Group exists check
		mockGroups.EXPECT().
			Exists(gomock.Any(), groupName).
			Return(true, nil).
			Times(1)

		// List workloads in group
		mockWorkloads.EXPECT().
			ListWorkloadsInGroup(gomock.Any(), groupName).
			Return([]string{"workload1", "workload2"}, nil).
			Times(1)

		// Get workload details
		workload1 := core.Workload{
			Name:          "workload1",
			Status:        runtime.WorkloadStatusRunning,
			URL:           "http://localhost:8080/mcp",
			TransportType: types.TransportTypeStreamableHTTP,
			ToolType:      "github",
			Group:         groupName,
			Labels: map[string]string{
				"env": "prod",
			},
		}

		workload2 := core.Workload{
			Name:          "workload2",
			Status:        runtime.WorkloadStatusRunning,
			URL:           "http://localhost:8081/mcp",
			TransportType: types.TransportTypeSSE,
			ToolType:      "jira",
			Group:         groupName,
		}

		mockWorkloads.EXPECT().
			GetWorkload(gomock.Any(), "workload1").
			Return(workload1, nil).
			Times(1)

		mockWorkloads.EXPECT().
			GetWorkload(gomock.Any(), "workload2").
			Return(workload2, nil).
			Times(1)

		discoverer := NewCLIBackendDiscoverer(mockWorkloads, mockGroups)
		backends, err := discoverer.Discover(context.Background(), groupName)

		require.NoError(t, err)
		assert.Len(t, backends, 2)

		// Check first backend
		assert.Equal(t, "workload1", backends[0].ID)
		assert.Equal(t, "workload1", backends[0].Name)
		assert.Equal(t, "http://localhost:8080/mcp", backends[0].BaseURL)
		assert.Equal(t, "streamable-http", backends[0].TransportType)
		assert.Equal(t, vmcp.BackendHealthy, backends[0].HealthStatus)
		assert.Equal(t, groupName, backends[0].Metadata["group"])
		assert.Equal(t, "github", backends[0].Metadata["tool_type"])
		assert.Equal(t, "prod", backends[0].Metadata["env"])

		// Check second backend
		assert.Equal(t, "workload2", backends[1].ID)
		assert.Equal(t, "sse", backends[1].TransportType)
	})

	t.Run("discovers workloads with different statuses", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		groupName := testGroupName

		mockGroups.EXPECT().
			Exists(gomock.Any(), groupName).
			Return(true, nil)

		mockWorkloads.EXPECT().
			ListWorkloadsInGroup(gomock.Any(), groupName).
			Return([]string{"running-workload", "stopped-workload"}, nil)

		runningWorkload := core.Workload{
			Name:          "running-workload",
			Status:        runtime.WorkloadStatusRunning,
			URL:           "http://localhost:8080/mcp",
			TransportType: types.TransportTypeStreamableHTTP,
			Group:         groupName,
		}

		stoppedWorkload := core.Workload{
			Name:          "stopped-workload",
			Status:        runtime.WorkloadStatusStopped,
			URL:           "http://localhost:8081/mcp",
			TransportType: types.TransportTypeSSE,
			Group:         groupName,
		}

		mockWorkloads.EXPECT().
			GetWorkload(gomock.Any(), "running-workload").
			Return(runningWorkload, nil)

		mockWorkloads.EXPECT().
			GetWorkload(gomock.Any(), "stopped-workload").
			Return(stoppedWorkload, nil)

		discoverer := NewCLIBackendDiscoverer(mockWorkloads, mockGroups)
		backends, err := discoverer.Discover(context.Background(), groupName)

		require.NoError(t, err)
		assert.Len(t, backends, 2)
		assert.Equal(t, "running-workload", backends[0].ID)
		assert.Equal(t, vmcp.BackendHealthy, backends[0].HealthStatus)
		assert.Equal(t, "stopped-workload", backends[1].ID)
		assert.Equal(t, vmcp.BackendUnhealthy, backends[1].HealthStatus)
		assert.Equal(t, "stopped", backends[1].Metadata["workload_status"])
	})

	t.Run("filters out workloads without URL", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		groupName := testGroupName

		mockGroups.EXPECT().
			Exists(gomock.Any(), groupName).
			Return(true, nil)

		mockWorkloads.EXPECT().
			ListWorkloadsInGroup(gomock.Any(), groupName).
			Return([]string{"workload1", "workload2"}, nil)

		workloadWithURL := core.Workload{
			Name:          "workload1",
			Status:        runtime.WorkloadStatusRunning,
			URL:           "http://localhost:8080/mcp",
			TransportType: types.TransportTypeStreamableHTTP,
			Group:         groupName,
		}

		workloadWithoutURL := core.Workload{
			Name:          "workload2",
			Status:        runtime.WorkloadStatusRunning,
			URL:           "", // No URL
			TransportType: types.TransportTypeStreamableHTTP,
			Group:         groupName,
		}

		mockWorkloads.EXPECT().
			GetWorkload(gomock.Any(), "workload1").
			Return(workloadWithURL, nil)

		mockWorkloads.EXPECT().
			GetWorkload(gomock.Any(), "workload2").
			Return(workloadWithoutURL, nil)

		discoverer := NewCLIBackendDiscoverer(mockWorkloads, mockGroups)
		backends, err := discoverer.Discover(context.Background(), groupName)

		require.NoError(t, err)
		assert.Len(t, backends, 1)
		assert.Equal(t, "workload1", backends[0].ID)
	})

	t.Run("returns empty list when all workloads lack URLs", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		groupName := testGroupName

		mockGroups.EXPECT().
			Exists(gomock.Any(), groupName).
			Return(true, nil)

		mockWorkloads.EXPECT().
			ListWorkloadsInGroup(gomock.Any(), groupName).
			Return([]string{"workload1", "workload2"}, nil)

		workload1 := core.Workload{
			Name:   "workload1",
			Status: runtime.WorkloadStatusRunning,
			URL:    "", // No URL
			Group:  groupName,
		}

		workload2 := core.Workload{
			Name:   "workload2",
			Status: runtime.WorkloadStatusStopped,
			URL:    "", // No URL
			Group:  groupName,
		}

		mockWorkloads.EXPECT().
			GetWorkload(gomock.Any(), "workload1").
			Return(workload1, nil)

		mockWorkloads.EXPECT().
			GetWorkload(gomock.Any(), "workload2").
			Return(workload2, nil)

		discoverer := NewCLIBackendDiscoverer(mockWorkloads, mockGroups)
		backends, err := discoverer.Discover(context.Background(), groupName)

		require.NoError(t, err)
		assert.Empty(t, backends)
	})

	t.Run("returns error when group does not exist", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		groupName := "nonexistent-group"

		mockGroups.EXPECT().
			Exists(gomock.Any(), groupName).
			Return(false, nil).
			Times(1)

		discoverer := NewCLIBackendDiscoverer(mockWorkloads, mockGroups)
		backends, err := discoverer.Discover(context.Background(), groupName)

		require.Error(t, err)
		assert.Nil(t, backends)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("returns error when group check fails", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		groupName := testGroupName

		mockGroups.EXPECT().
			Exists(gomock.Any(), groupName).
			Return(false, errors.New("database error")).
			Times(1)

		discoverer := NewCLIBackendDiscoverer(mockWorkloads, mockGroups)
		backends, err := discoverer.Discover(context.Background(), groupName)

		require.Error(t, err)
		assert.Nil(t, backends)
		assert.Contains(t, err.Error(), "failed to check if group exists")
	})

	t.Run("returns empty list when group is empty", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		groupName := "empty-group"

		mockGroups.EXPECT().
			Exists(gomock.Any(), groupName).
			Return(true, nil)

		mockWorkloads.EXPECT().
			ListWorkloadsInGroup(gomock.Any(), groupName).
			Return([]string{}, nil)

		discoverer := NewCLIBackendDiscoverer(mockWorkloads, mockGroups)
		backends, err := discoverer.Discover(context.Background(), groupName)

		require.NoError(t, err)
		assert.Empty(t, backends)
	})

	t.Run("discovers all workloads regardless of health status", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		groupName := testGroupName

		mockGroups.EXPECT().
			Exists(gomock.Any(), groupName).
			Return(true, nil)

		mockWorkloads.EXPECT().
			ListWorkloadsInGroup(gomock.Any(), groupName).
			Return([]string{"stopped1", "error1"}, nil)

		stoppedWorkload := core.Workload{
			Name:          "stopped1",
			Status:        runtime.WorkloadStatusStopped,
			URL:           "http://localhost:8080/mcp",
			TransportType: types.TransportTypeStreamableHTTP,
			Group:         groupName,
		}

		errorWorkload := core.Workload{
			Name:          "error1",
			Status:        runtime.WorkloadStatusError,
			URL:           "http://localhost:8081/mcp",
			TransportType: types.TransportTypeSSE,
			Group:         groupName,
		}

		mockWorkloads.EXPECT().
			GetWorkload(gomock.Any(), "stopped1").
			Return(stoppedWorkload, nil)

		mockWorkloads.EXPECT().
			GetWorkload(gomock.Any(), "error1").
			Return(errorWorkload, nil)

		discoverer := NewCLIBackendDiscoverer(mockWorkloads, mockGroups)
		backends, err := discoverer.Discover(context.Background(), groupName)

		require.NoError(t, err)
		assert.Len(t, backends, 2)
		assert.Equal(t, vmcp.BackendUnhealthy, backends[0].HealthStatus)
		assert.Equal(t, vmcp.BackendUnhealthy, backends[1].HealthStatus)
	})

	t.Run("gracefully handles workload get failures", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		groupName := testGroupName

		mockGroups.EXPECT().
			Exists(gomock.Any(), groupName).
			Return(true, nil)

		mockWorkloads.EXPECT().
			ListWorkloadsInGroup(gomock.Any(), groupName).
			Return([]string{"good-workload", "failing-workload"}, nil)

		goodWorkload := core.Workload{
			Name:          "good-workload",
			Status:        runtime.WorkloadStatusRunning,
			URL:           "http://localhost:8080/mcp",
			TransportType: types.TransportTypeStreamableHTTP,
			Group:         groupName,
		}

		mockWorkloads.EXPECT().
			GetWorkload(gomock.Any(), "good-workload").
			Return(goodWorkload, nil)

		mockWorkloads.EXPECT().
			GetWorkload(gomock.Any(), "failing-workload").
			Return(core.Workload{}, errors.New("workload query failed"))

		discoverer := NewCLIBackendDiscoverer(mockWorkloads, mockGroups)
		backends, err := discoverer.Discover(context.Background(), groupName)

		// Should succeed with partial results
		require.NoError(t, err)
		assert.Len(t, backends, 1)
		assert.Equal(t, "good-workload", backends[0].ID)
	})
}
