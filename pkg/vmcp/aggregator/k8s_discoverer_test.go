package aggregator

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/groups/mocks"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/workloads/k8s"
	workloadmocks "github.com/stacklok/toolhive/pkg/workloads/mocks"
)

func TestK8SBackendDiscoverer_Discover(t *testing.T) {
	t.Parallel()

	t.Run("successful discovery with multiple backends", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockK8SManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		workload1 := newTestK8SWorkload("workload1",
			withK8SToolType("github"),
			withK8SLabels(map[string]string{"env": "prod"}),
			withK8SNamespace("toolhive-system"))

		workload2 := newTestK8SWorkload("workload2",
			withK8SURL("http://localhost:8081/mcp"),
			withK8STransport(types.TransportTypeSSE),
			withK8SToolType("jira"),
			withK8SNamespace("toolhive-system"))

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloads.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"workload1", "workload2"}, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "workload1").Return(workload1, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "workload2").Return(workload2, nil)

		discoverer := NewK8SBackendDiscoverer(mockWorkloads, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 2)
		assert.Equal(t, "workload1", backends[0].ID)
		assert.Equal(t, "http://localhost:8080/mcp", backends[0].BaseURL)
		assert.Equal(t, vmcp.BackendHealthy, backends[0].HealthStatus)
		assert.Equal(t, "github", backends[0].Metadata["tool_type"])
		assert.Equal(t, "prod", backends[0].Metadata["env"])
		assert.Equal(t, "toolhive-system", backends[0].Metadata["namespace"])
		assert.Equal(t, "workload2", backends[1].ID)
		assert.Equal(t, "sse", backends[1].TransportType)
	})

	t.Run("discovers workloads with different phases", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockK8SManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		runningWorkload := newTestK8SWorkload("running-workload",
			withK8SPhase(mcpv1alpha1.MCPServerPhaseRunning))
		failedWorkload := newTestK8SWorkload("failed-workload",
			withK8SPhase(mcpv1alpha1.MCPServerPhaseFailed),
			withK8SURL("http://localhost:8081/mcp"),
			withK8STransport(types.TransportTypeSSE))

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloads.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"running-workload", "failed-workload"}, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "running-workload").Return(runningWorkload, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "failed-workload").Return(failedWorkload, nil)

		discoverer := NewK8SBackendDiscoverer(mockWorkloads, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 2)
		assert.Equal(t, "running-workload", backends[0].ID)
		assert.Equal(t, vmcp.BackendHealthy, backends[0].HealthStatus)
		assert.Equal(t, "failed-workload", backends[1].ID)
		assert.Equal(t, vmcp.BackendUnhealthy, backends[1].HealthStatus)
		assert.Equal(t, string(mcpv1alpha1.MCPServerPhaseFailed), backends[1].Metadata["workload_phase"])
	})

	t.Run("filters out workloads without URL", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockK8SManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		workloadWithURL := newTestK8SWorkload("workload1")
		workloadWithoutURL := newTestK8SWorkload("workload2", withK8SURL(""))

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloads.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"workload1", "workload2"}, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "workload1").Return(workloadWithURL, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "workload2").Return(workloadWithoutURL, nil)

		discoverer := NewK8SBackendDiscoverer(mockWorkloads, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 1)
		assert.Equal(t, "workload1", backends[0].ID)
	})

	t.Run("returns empty list when all workloads lack URLs", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockK8SManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		workload1 := newTestK8SWorkload("workload1", withK8SURL(""))
		workload2 := newTestK8SWorkload("workload2",
			withK8SPhase(mcpv1alpha1.MCPServerPhaseTerminating),
			withK8SURL(""))

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloads.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"workload1", "workload2"}, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "workload1").Return(workload1, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "workload2").Return(workload2, nil)

		discoverer := NewK8SBackendDiscoverer(mockWorkloads, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		assert.Empty(t, backends)
	})

	t.Run("returns error when group does not exist", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockK8SManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		mockGroups.EXPECT().Exists(gomock.Any(), "nonexistent-group").Return(false, nil)

		discoverer := NewK8SBackendDiscoverer(mockWorkloads, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), "nonexistent-group")

		require.Error(t, err)
		assert.Nil(t, backends)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("returns error when group check fails", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockK8SManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(false, errors.New("database error"))

		discoverer := NewK8SBackendDiscoverer(mockWorkloads, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.Error(t, err)
		assert.Nil(t, backends)
		assert.Contains(t, err.Error(), "failed to check if group exists")
	})

	t.Run("returns empty list when group is empty", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockK8SManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		mockGroups.EXPECT().Exists(gomock.Any(), "empty-group").Return(true, nil)
		mockWorkloads.EXPECT().ListWorkloadsInGroup(gomock.Any(), "empty-group").Return([]string{}, nil)

		discoverer := NewK8SBackendDiscoverer(mockWorkloads, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), "empty-group")

		require.NoError(t, err)
		assert.Empty(t, backends)
	})

	t.Run("discovers all workloads regardless of phase", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockK8SManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		terminatingWorkload := newTestK8SWorkload("terminating1",
			withK8SPhase(mcpv1alpha1.MCPServerPhaseTerminating))
		failedWorkload := newTestK8SWorkload("failed1",
			withK8SPhase(mcpv1alpha1.MCPServerPhaseFailed),
			withK8SURL("http://localhost:8081/mcp"),
			withK8STransport(types.TransportTypeSSE))

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloads.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"terminating1", "failed1"}, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "terminating1").Return(terminatingWorkload, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "failed1").Return(failedWorkload, nil)

		discoverer := NewK8SBackendDiscoverer(mockWorkloads, mockGroups, nil)
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

		mockWorkloads := workloadmocks.NewMockK8SManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		goodWorkload := newTestK8SWorkload("good-workload")

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloads.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"good-workload", "failing-workload"}, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "good-workload").Return(goodWorkload, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "failing-workload").
			Return(k8s.Workload{}, errors.New("MCPServer query failed"))

		discoverer := NewK8SBackendDiscoverer(mockWorkloads, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 1)
		assert.Equal(t, "good-workload", backends[0].ID)
	})

	t.Run("returns error when list workloads fails", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockK8SManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloads.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return(nil, errors.New("failed to list workloads"))

		discoverer := NewK8SBackendDiscoverer(mockWorkloads, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.Error(t, err)
		assert.Nil(t, backends)
		assert.Contains(t, err.Error(), "failed to list workloads in group")
	})

	t.Run("handles pending phase correctly", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockK8SManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		pendingWorkload := newTestK8SWorkload("pending-workload",
			withK8SPhase(mcpv1alpha1.MCPServerPhasePending))

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloads.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"pending-workload"}, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "pending-workload").Return(pendingWorkload, nil)

		discoverer := NewK8SBackendDiscoverer(mockWorkloads, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 1)
		assert.Equal(t, vmcp.BackendUnknown, backends[0].HealthStatus)
		assert.Equal(t, string(mcpv1alpha1.MCPServerPhasePending), backends[0].Metadata["workload_phase"])
	})

	t.Run("includes namespace in metadata", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockWorkloads := workloadmocks.NewMockK8SManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		workload := newTestK8SWorkload("workload1",
			withK8SNamespace("custom-namespace"))

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloads.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"workload1"}, nil)
		mockWorkloads.EXPECT().GetWorkload(gomock.Any(), "workload1").Return(workload, nil)

		discoverer := NewK8SBackendDiscoverer(mockWorkloads, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 1)
		assert.Equal(t, "custom-namespace", backends[0].Metadata["namespace"])
	})
}
