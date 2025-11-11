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

func TestBackendDiscoverer_Discover(t *testing.T) {
	t.Parallel()

	t.Run("successful discovery with multiple backends", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		workload1 := newTestWorkload("workload1",
			withToolType("github"),
			withLabels(map[string]string{"env": "prod"}))

		workload2 := newTestWorkload("workload2",
			withURL("http://localhost:8081/mcp"),
			withTransport(types.TransportTypeSSE),
			withToolType("jira"))

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloads.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"workload1", "workload2"}, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "workload1").Return(workload1, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "workload2").Return(workload2, nil)

		discoverer := NewCLIBackendDiscoverer(mockWorkloads, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 2)
		assert.Equal(t, "workload1", backends[0].ID)
		assert.Equal(t, "http://localhost:8080/mcp", backends[0].BaseURL)
		assert.Equal(t, vmcp.BackendHealthy, backends[0].HealthStatus)
		assert.Equal(t, "github", backends[0].Metadata["tool_type"])
		assert.Equal(t, "prod", backends[0].Metadata["env"])
		assert.Equal(t, "workload2", backends[1].ID)
		assert.Equal(t, "sse", backends[1].TransportType)
	})

	t.Run("discovers workloads with different statuses", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		runningWorkload := newTestWorkload("running-workload")
		stoppedWorkload := newTestWorkload("stopped-workload",
			withStatus(runtime.WorkloadStatusStopped),
			withURL("http://localhost:8081/mcp"),
			withTransport(types.TransportTypeSSE))

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloads.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"running-workload", "stopped-workload"}, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "running-workload").Return(runningWorkload, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "stopped-workload").Return(stoppedWorkload, nil)

		discoverer := NewCLIBackendDiscoverer(mockWorkloads, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 2)
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

		workloadWithURL := newTestWorkload("workload1")
		workloadWithoutURL := newTestWorkload("workload2", withURL(""))

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloads.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"workload1", "workload2"}, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "workload1").Return(workloadWithURL, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "workload2").Return(workloadWithoutURL, nil)

		discoverer := NewCLIBackendDiscoverer(mockWorkloads, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 1)
		assert.Equal(t, "workload1", backends[0].ID)
	})

	t.Run("returns empty list when all workloads lack URLs", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		workload1 := newTestWorkload("workload1", withURL(""))
		workload2 := newTestWorkload("workload2", withStatus(runtime.WorkloadStatusStopped), withURL(""))

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloads.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"workload1", "workload2"}, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "workload1").Return(workload1, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "workload2").Return(workload2, nil)

		discoverer := NewCLIBackendDiscoverer(mockWorkloads, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		assert.Empty(t, backends)
	})

	t.Run("returns error when group does not exist", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		mockGroups.EXPECT().Exists(gomock.Any(), "nonexistent-group").Return(false, nil)

		discoverer := NewCLIBackendDiscoverer(mockWorkloads, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), "nonexistent-group")

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

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(false, errors.New("database error"))

		discoverer := NewCLIBackendDiscoverer(mockWorkloads, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

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

		mockGroups.EXPECT().Exists(gomock.Any(), "empty-group").Return(true, nil)
		mockWorkloads.EXPECT().ListWorkloadsInGroup(gomock.Any(), "empty-group").Return([]string{}, nil)

		discoverer := NewCLIBackendDiscoverer(mockWorkloads, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), "empty-group")

		require.NoError(t, err)
		assert.Empty(t, backends)
	})

	t.Run("discovers all workloads regardless of health status", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		stoppedWorkload := newTestWorkload("stopped1", withStatus(runtime.WorkloadStatusStopped))
		errorWorkload := newTestWorkload("error1",
			withStatus(runtime.WorkloadStatusError),
			withURL("http://localhost:8081/mcp"),
			withTransport(types.TransportTypeSSE))

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloads.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"stopped1", "error1"}, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "stopped1").Return(stoppedWorkload, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "error1").Return(errorWorkload, nil)

		discoverer := NewCLIBackendDiscoverer(mockWorkloads, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 2)
		assert.Equal(t, vmcp.BackendUnhealthy, backends[0].HealthStatus)
		assert.Equal(t, vmcp.BackendUnhealthy, backends[1].HealthStatus)
	})

	t.Run("gracefully handles workload get failures", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		goodWorkload := newTestWorkload("good-workload")

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloads.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"good-workload", "failing-workload"}, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "good-workload").Return(goodWorkload, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "failing-workload").
			Return(core.Workload{}, errors.New("workload query failed"))

		discoverer := NewCLIBackendDiscoverer(mockWorkloads, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 1)
		assert.Equal(t, "good-workload", backends[0].ID)
	})
}
