package server_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	discoveryMocks "github.com/stacklok/toolhive/pkg/vmcp/discovery/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	routerMocks "github.com/stacklok/toolhive/pkg/vmcp/router/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
)

//nolint:tparallel // Subtests share server instance, no benefit from parallelism
func TestServer_HealthMonitoring_Disabled(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockRouter := routerMocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)

	// Create server without health monitoring config
	config := &server.Config{
		HealthMonitorConfig: nil, // Explicitly disabled
	}

	srv, err := server.New(
		context.Background(),
		config,
		mockRouter,
		mockBackendClient,
		mockDiscoveryMgr,
		[]vmcp.Backend{},
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, srv)

	//nolint:paralleltest // Subtests share server instance
	t.Run("GetBackendHealthStatus returns error when disabled", func(t *testing.T) {
		status, err := srv.GetBackendHealthStatus("backend-1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "health monitoring is not enabled")
		assert.Equal(t, vmcp.BackendUnknown, status)
	})

	//nolint:paralleltest // Subtests share server instance
	t.Run("GetBackendHealthState returns error when disabled", func(t *testing.T) {
		state, err := srv.GetBackendHealthState("backend-1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "health monitoring is not enabled")
		assert.Nil(t, state)
	})

	//nolint:paralleltest // Subtests share server instance
	t.Run("GetAllBackendHealthStates returns nil when disabled", func(t *testing.T) {
		states := srv.GetAllBackendHealthStates()
		assert.Nil(t, states)
	})

	//nolint:paralleltest // Subtests share server instance
	t.Run("GetHealthSummary returns zero summary when disabled", func(t *testing.T) {
		summary := srv.GetHealthSummary()
		assert.Equal(t, 0, summary.Total)
		assert.Equal(t, 0, summary.Healthy)
		assert.Equal(t, 0, summary.Unhealthy)
	})
}

//nolint:tparallel // Subtests must run sequentially after health checks complete
func TestServer_HealthMonitoring_Enabled(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockRouter := routerMocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockDiscoveryMgr.EXPECT().Stop().AnyTimes()

	backends := []vmcp.Backend{
		{
			ID:            "backend-1",
			Name:          "Backend 1",
			BaseURL:       "http://localhost:8080",
			TransportType: "sse",
		},
		{
			ID:            "backend-2",
			Name:          "Backend 2",
			BaseURL:       "http://localhost:8081",
			TransportType: "sse",
		},
	}

	// Mock health checks - backend-1 succeeds, backend-2 fails
	mockBackendClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, target *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
			if target.WorkloadID == "backend-1" {
				return &vmcp.CapabilityList{}, nil
			}
			return nil, assert.AnError
		}).
		AnyTimes()

	// Create server with health monitoring enabled
	config := &server.Config{
		HealthMonitorConfig: &health.MonitorConfig{
			CheckInterval:      50 * time.Millisecond,
			UnhealthyThreshold: 1, // Mark unhealthy after 1 failure
			Timeout:            10 * time.Millisecond,
		},
	}

	srv, err := server.New(
		context.Background(),
		config,
		mockRouter,
		mockBackendClient,
		mockDiscoveryMgr,
		backends,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, srv)

	// Start server to begin health monitoring
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	// Wait for server to be ready
	<-srv.Ready()

	// Give health monitor time to perform checks
	time.Sleep(150 * time.Millisecond)

	//nolint:paralleltest // Subtests depend on timing and shared server state
	t.Run("GetBackendHealthStatus returns status for healthy backend", func(t *testing.T) {
		status, err := srv.GetBackendHealthStatus("backend-1")
		assert.NoError(t, err)
		assert.Equal(t, vmcp.BackendHealthy, status)
	})

	//nolint:paralleltest // Subtests depend on timing and shared server state
	t.Run("GetBackendHealthStatus returns status for unhealthy backend", func(t *testing.T) {
		status, err := srv.GetBackendHealthStatus("backend-2")
		assert.NoError(t, err)
		assert.Equal(t, vmcp.BackendUnhealthy, status)
	})

	//nolint:paralleltest // Subtests depend on timing and shared server state
	t.Run("GetBackendHealthStatus returns error for unknown backend", func(t *testing.T) {
		status, err := srv.GetBackendHealthStatus("unknown-backend")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
		assert.Equal(t, vmcp.BackendUnknown, status)
	})

	//nolint:paralleltest // Subtests depend on timing and shared server state
	t.Run("GetBackendHealthState returns state for monitored backend", func(t *testing.T) {
		state, err := srv.GetBackendHealthState("backend-1")
		assert.NoError(t, err)
		assert.NotNil(t, state)
		assert.Equal(t, vmcp.BackendHealthy, state.Status)
		assert.Equal(t, 0, state.ConsecutiveFailures)
	})

	//nolint:paralleltest // Subtests depend on timing and shared server state
	t.Run("GetBackendHealthState returns error for unknown backend", func(t *testing.T) {
		state, err := srv.GetBackendHealthState("unknown-backend")
		assert.Error(t, err)
		assert.Nil(t, state)
	})

	//nolint:paralleltest // Subtests depend on timing and shared server state
	t.Run("GetAllBackendHealthStates returns all backend states", func(t *testing.T) {
		states := srv.GetAllBackendHealthStates()
		assert.NotNil(t, states)
		assert.Len(t, states, 2)
		assert.Contains(t, states, "backend-1")
		assert.Contains(t, states, "backend-2")
		assert.Equal(t, vmcp.BackendHealthy, states["backend-1"].Status)
		assert.Equal(t, vmcp.BackendUnhealthy, states["backend-2"].Status)
	})

	//nolint:paralleltest // Subtests depend on timing and shared server state
	t.Run("GetHealthSummary returns aggregate statistics", func(t *testing.T) {
		summary := srv.GetHealthSummary()
		assert.Equal(t, 2, summary.Total)
		assert.Equal(t, 1, summary.Healthy)
		assert.Equal(t, 1, summary.Unhealthy)
		assert.Equal(t, 0, summary.Degraded)
		assert.Equal(t, 0, summary.Unknown)
		assert.Equal(t, 0, summary.Unauthenticated)
	})

	// Clean shutdown
	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Server did not shut down in time")
	}
}

func TestServer_HealthMonitoring_StartupFailure(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockRouter := routerMocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)

	// Create server with invalid health monitoring config (will fail validation)
	config := &server.Config{
		HealthMonitorConfig: &health.MonitorConfig{
			CheckInterval:      0, // Invalid - will cause NewMonitor to fail
			UnhealthyThreshold: 3,
			Timeout:            10 * time.Millisecond,
		},
	}

	srv, err := server.New(
		context.Background(),
		config,
		mockRouter,
		mockBackendClient,
		mockDiscoveryMgr,
		[]vmcp.Backend{},
		nil,
	)

	// Should fail during construction due to invalid config
	assert.Error(t, err)
	assert.Nil(t, srv)
	assert.Contains(t, err.Error(), "failed to create health monitor")
}
