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
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
	discoverermocks "github.com/stacklok/toolhive/pkg/vmcp/workloads/mocks"
	statusmocks "github.com/stacklok/toolhive/pkg/workloads/statuses/mocks"
)

const testGroupName = "test-group"

func TestBackendDiscoverer_Discover(t *testing.T) {
	t.Parallel()

	t.Run("successful discovery with multiple backends", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		backend1 := &vmcp.Backend{
			ID:            "workload1",
			Name:          "workload1",
			BaseURL:       "http://localhost:8080/mcp",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
			Metadata: map[string]string{
				"tool_type":       "github",
				"workload_status": "running",
				"env":             "prod",
			},
		}
		backend2 := &vmcp.Backend{
			ID:            "workload2",
			Name:          "workload2",
			BaseURL:       "http://localhost:8081/mcp",
			TransportType: "sse",
			HealthStatus:  vmcp.BackendHealthy,
			Metadata: map[string]string{
				"tool_type":       "jira",
				"workload_status": "running",
			},
		}

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"workload1", "workload2"}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkload(gomock.Any(), "workload1").Return(backend1, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkload(gomock.Any(), "workload2").Return(backend2, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
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

	t.Run("discovers workloads with different health statuses", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		healthyBackend := &vmcp.Backend{
			ID:            "healthy-workload",
			Name:          "healthy-workload",
			BaseURL:       "http://localhost:8080/mcp",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
			Metadata:      map[string]string{"workload_status": "running"},
		}
		unhealthyBackend := &vmcp.Backend{
			ID:            "unhealthy-workload",
			Name:          "unhealthy-workload",
			BaseURL:       "http://localhost:8081/mcp",
			TransportType: "sse",
			HealthStatus:  vmcp.BackendUnhealthy,
			Metadata:      map[string]string{"workload_status": "stopped"},
		}

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"healthy-workload", "unhealthy-workload"}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkload(gomock.Any(), "healthy-workload").Return(healthyBackend, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkload(gomock.Any(), "unhealthy-workload").Return(unhealthyBackend, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 2)
		assert.Equal(t, "healthy-workload", backends[0].ID)
		assert.Equal(t, vmcp.BackendHealthy, backends[0].HealthStatus)
		assert.Equal(t, "unhealthy-workload", backends[1].ID)
		assert.Equal(t, vmcp.BackendUnhealthy, backends[1].HealthStatus)
		assert.Equal(t, "stopped", backends[1].Metadata["workload_status"])
	})

	t.Run("filters out workloads without URL", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		backendWithURL := &vmcp.Backend{
			ID:            "workload1",
			Name:          "workload1",
			BaseURL:       "http://localhost:8080/mcp",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
			Metadata:      map[string]string{},
		}

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"workload1", "workload2"}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkload(gomock.Any(), "workload1").Return(backendWithURL, nil)
		// workload2 has no URL, so GetWorkload returns nil
		mockWorkloadDiscoverer.EXPECT().GetWorkload(gomock.Any(), "workload2").Return(nil, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 1)
		assert.Equal(t, "workload1", backends[0].ID)
	})

	t.Run("returns empty list when all workloads lack URLs", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"workload1", "workload2"}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkload(gomock.Any(), "workload1").Return(nil, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkload(gomock.Any(), "workload2").Return(nil, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		assert.Empty(t, backends)
	})

	t.Run("returns error when group does not exist", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		mockGroups.EXPECT().Exists(gomock.Any(), "nonexistent-group").Return(false, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), "nonexistent-group")

		require.Error(t, err)
		assert.Nil(t, backends)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("returns error when group check fails", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(false, errors.New("database error"))

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.Error(t, err)
		assert.Nil(t, backends)
		assert.Contains(t, err.Error(), "failed to check if group exists")
	})

	t.Run("returns empty list when group is empty", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		mockGroups.EXPECT().Exists(gomock.Any(), "empty-group").Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), "empty-group").Return([]string{}, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), "empty-group")

		require.NoError(t, err)
		assert.Empty(t, backends)
	})

	t.Run("gracefully handles workload get failures", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		goodBackend := &vmcp.Backend{
			ID:            "good-workload",
			Name:          "good-workload",
			BaseURL:       "http://localhost:8080/mcp",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
			Metadata:      map[string]string{},
		}

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"good-workload", "failing-workload"}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkload(gomock.Any(), "good-workload").Return(goodBackend, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkload(gomock.Any(), "failing-workload").
			Return(nil, errors.New("workload query failed"))

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 1)
		assert.Equal(t, "good-workload", backends[0].ID)
	})

	t.Run("returns error when list workloads fails", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return(nil, errors.New("failed to list workloads"))

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.Error(t, err)
		assert.Nil(t, backends)
		assert.Contains(t, err.Error(), "failed to list workloads in group")
	})

	t.Run("applies authentication configuration", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		backend := &vmcp.Backend{
			ID:            "workload1",
			Name:          "workload1",
			BaseURL:       "http://localhost:8080/mcp",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
			Metadata:      map[string]string{},
		}

		authConfig := &config.OutgoingAuthConfig{
			Backends: map[string]*config.BackendAuthStrategy{
				"workload1": {
					Type: "bearer",
					Metadata: map[string]any{
						"token": "test-token",
					},
				},
			},
		}

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"workload1"}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkload(gomock.Any(), "workload1").Return(backend, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, authConfig)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 1)
		assert.Equal(t, "bearer", backends[0].AuthStrategy)
		assert.Equal(t, "test-token", backends[0].AuthMetadata["token"])
	})
}

// TestCLIWorkloadDiscoverer tests the CLI workload discoverer implementation
// to ensure it correctly converts CLI workloads to backends.
func TestCLIWorkloadDiscoverer(t *testing.T) {
	t.Parallel()

	t.Run("converts CLI workload to backend correctly", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockStatusManager := statusmocks.NewMockStatusManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		workload := newTestWorkload("workload1",
			withToolType("github"),
			withLabels(map[string]string{"env": "prod"}))

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockStatusManager.EXPECT().ListWorkloads(gomock.Any(), true, nil).
			Return([]core.Workload{workload}, nil)
		mockStatusManager.EXPECT().GetWorkload(gomock.Any(), "workload1").Return(workload, nil)

		cliDiscoverer := workloads.NewCLIDiscoverer(mockStatusManager)
		discoverer := NewUnifiedBackendDiscoverer(cliDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 1)
		assert.Equal(t, "workload1", backends[0].ID)
		assert.Equal(t, "http://localhost:8080/mcp", backends[0].BaseURL)
		assert.Equal(t, vmcp.BackendHealthy, backends[0].HealthStatus)
		assert.Equal(t, "github", backends[0].Metadata["tool_type"])
		assert.Equal(t, "prod", backends[0].Metadata["env"])
	})

	t.Run("maps CLI workload statuses to health correctly", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockStatusManager := statusmocks.NewMockStatusManager(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		runningWorkload := newTestWorkload("running-workload")
		stoppedWorkload := newTestWorkload("stopped-workload",
			withStatus(runtime.WorkloadStatusStopped),
			withURL("http://localhost:8081/mcp"),
			withTransport(types.TransportTypeSSE))

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockStatusManager.EXPECT().ListWorkloads(gomock.Any(), true, nil).
			Return([]core.Workload{runningWorkload, stoppedWorkload}, nil)
		mockStatusManager.EXPECT().GetWorkload(gomock.Any(), "running-workload").Return(runningWorkload, nil)
		mockStatusManager.EXPECT().GetWorkload(gomock.Any(), "stopped-workload").Return(stoppedWorkload, nil)

		cliDiscoverer := workloads.NewCLIDiscoverer(mockStatusManager)
		discoverer := NewUnifiedBackendDiscoverer(cliDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 2)
		assert.Equal(t, vmcp.BackendHealthy, backends[0].HealthStatus)
		assert.Equal(t, vmcp.BackendUnhealthy, backends[1].HealthStatus)
	})
}
