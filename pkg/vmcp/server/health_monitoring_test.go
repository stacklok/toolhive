package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	discoverymocks "github.com/stacklok/toolhive/pkg/vmcp/discovery/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	routermocks "github.com/stacklok/toolhive/pkg/vmcp/router/mocks"
)

// TestServer_HealthMonitoring_Disabled verifies behavior when health monitoring is disabled.
func TestServer_HealthMonitoring_Disabled(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRouter := routermocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoverymocks.NewMockManager(ctrl)

	backends := []vmcp.Backend{
		{ID: "backend-1", Name: "Backend 1", BaseURL: "http://localhost:8080"},
	}

	// Create server WITHOUT health monitoring config
	cfg := &Config{
		Name:                "test-server",
		Version:             "1.0.0",
		Host:                "127.0.0.1",
		Port:                0,
		HealthMonitorConfig: nil, // Health monitoring disabled
	}

	srv, err := New(context.Background(), cfg, mockRouter, mockBackendClient, mockDiscoveryMgr, backends, nil)
	require.NoError(t, err)
	require.NotNil(t, srv)

	// Verify health monitor is nil
	assert.Nil(t, srv.healthMonitor)

	// Verify getter methods return appropriate responses when disabled
	status, err := srv.GetBackendHealthStatus("backend-1")
	assert.Error(t, err)
	assert.Equal(t, vmcp.BackendUnknown, status)
	assert.Contains(t, err.Error(), "health monitoring is disabled")

	state, err := srv.GetBackendHealthState("backend-1")
	assert.Error(t, err)
	assert.Nil(t, state)
	assert.Contains(t, err.Error(), "health monitoring is disabled")

	allStates := srv.GetAllBackendHealthStates()
	assert.NotNil(t, allStates)
	assert.Empty(t, allStates)

	summary := srv.GetHealthSummary()
	assert.Equal(t, health.Summary{}, summary)
}

// TestServer_HealthMonitoring_Enabled verifies health monitoring works correctly when enabled.
func TestServer_HealthMonitoring_Enabled(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRouter := routermocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoverymocks.NewMockManager(ctrl)

	backends := []vmcp.Backend{
		{ID: "backend-1", Name: "Backend 1", BaseURL: "http://localhost:8080", TransportType: "sse"},
		{ID: "backend-2", Name: "Backend 2", BaseURL: "http://localhost:8081", TransportType: "sse"},
	}

	// Mock health checks - backend-1 healthy, backend-2 unhealthy
	mockBackendClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, target *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
			if target.WorkloadID == "backend-1" {
				return &vmcp.CapabilityList{}, nil
			}
			return nil, assert.AnError
		}).
		AnyTimes()

	// Create server WITH health monitoring config
	cfg := &Config{
		Name:    "test-server",
		Version: "1.0.0",
		Host:    "127.0.0.1",
		Port:    0,
		HealthMonitorConfig: &health.MonitorConfig{
			CheckInterval:      50 * time.Millisecond,
			UnhealthyThreshold: 1,
			Timeout:            10 * time.Millisecond,
			DegradedThreshold:  5 * time.Millisecond,
		},
	}

	srv, err := New(context.Background(), cfg, mockRouter, mockBackendClient, mockDiscoveryMgr, backends, nil)
	require.NoError(t, err)
	require.NotNil(t, srv)

	// Verify health monitor is created
	assert.NotNil(t, srv.healthMonitor)

	// Start server in background
	mockDiscoveryMgr.EXPECT().Stop().AnyTimes()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Start(ctx); err != nil {
			errCh <- err
		}
	}()

	// Wait for server to be ready
	select {
	case <-srv.Ready():
	case err := <-errCh:
		t.Fatalf("server failed to start: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for server to start")
	}

	// Wait for health checks to run
	time.Sleep(200 * time.Millisecond)

	// Test GetBackendHealthStatus
	status, err := srv.GetBackendHealthStatus("backend-1")
	assert.NoError(t, err)
	assert.Equal(t, vmcp.BackendHealthy, status)

	status, err = srv.GetBackendHealthStatus("backend-2")
	assert.NoError(t, err)
	assert.Equal(t, vmcp.BackendUnhealthy, status)

	// Test GetBackendHealthState
	state, err := srv.GetBackendHealthState("backend-1")
	assert.NoError(t, err)
	assert.NotNil(t, state)
	assert.Equal(t, vmcp.BackendHealthy, state.Status)

	// Test GetAllBackendHealthStates
	allStates := srv.GetAllBackendHealthStates()
	assert.Len(t, allStates, 2)
	assert.Contains(t, allStates, "backend-1")
	assert.Contains(t, allStates, "backend-2")

	// Test GetHealthSummary
	summary := srv.GetHealthSummary()
	assert.Equal(t, 2, summary.Total)
	assert.Equal(t, 1, summary.Healthy)
	assert.Equal(t, 1, summary.Unhealthy)

	// Stop server
	cancel()
	time.Sleep(100 * time.Millisecond)
}

// TestServer_HealthMonitoring_StartupFailure verifies graceful degradation when health monitor fails to start.
func TestServer_HealthMonitoring_StartupFailure(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRouter := routermocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoverymocks.NewMockManager(ctrl)

	backends := []vmcp.Backend{
		{ID: "backend-1", Name: "Backend 1", BaseURL: "http://localhost:8080"},
	}

	// Create server WITH health monitoring config but invalid health monitor config to trigger monitor failure
	cfg := &Config{
		Name:    "test-server",
		Version: "1.0.0",
		Host:    "127.0.0.1",
		Port:    0,
		HealthMonitorConfig: &health.MonitorConfig{
			CheckInterval:      100 * time.Millisecond,
			UnhealthyThreshold: 0, // Invalid config - will cause monitor creation to fail
			Timeout:            50 * time.Millisecond,
		},
	}

	// This should fail during New() because of invalid health monitor config
	srv, err := New(context.Background(), cfg, mockRouter, mockBackendClient, mockDiscoveryMgr, backends, nil)
	require.Error(t, err)
	require.Nil(t, srv)
	assert.Contains(t, err.Error(), "failed to create health monitor")
}

// TestServer_HandleBackendHealth_Disabled verifies /api/backends/health when monitoring is disabled.
func TestServer_HandleBackendHealth_Disabled(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRouter := routermocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoverymocks.NewMockManager(ctrl)

	backends := []vmcp.Backend{
		{ID: "backend-1", Name: "Backend 1", BaseURL: "http://localhost:8080"},
	}

	// Create server WITHOUT health monitoring
	cfg := &Config{
		Name:                "test-server",
		Version:             "1.0.0",
		Host:                "127.0.0.1",
		Port:                0,
		HealthMonitorConfig: nil,
	}

	srv, err := New(context.Background(), cfg, mockRouter, mockBackendClient, mockDiscoveryMgr, backends, nil)
	require.NoError(t, err)

	// Create test request
	req := httptest.NewRequest(http.MethodGet, "/api/backends/health", nil)
	w := httptest.NewRecorder()

	// Call handler
	srv.handleBackendHealth(w, req)

	// Verify response
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var response BackendHealthResponse
	err = json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)

	assert.False(t, response.MonitoringEnabled)
	assert.Nil(t, response.Summary)
	assert.Nil(t, response.Backends)
}

// TestServer_HandleBackendHealth_Enabled verifies /api/backends/health when monitoring is enabled.
func TestServer_HandleBackendHealth_Enabled(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRouter := routermocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoverymocks.NewMockManager(ctrl)

	backends := []vmcp.Backend{
		{ID: "backend-1", Name: "Backend 1", BaseURL: "http://localhost:8080", TransportType: "sse"},
	}

	// Mock healthy backend
	mockBackendClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(&vmcp.CapabilityList{}, nil).
		AnyTimes()

	// Create server WITH health monitoring
	cfg := &Config{
		Name:    "test-server",
		Version: "1.0.0",
		Host:    "127.0.0.1",
		Port:    0,
		HealthMonitorConfig: &health.MonitorConfig{
			CheckInterval:      50 * time.Millisecond,
			UnhealthyThreshold: 3,
			Timeout:            10 * time.Millisecond,
		},
	}

	srv, err := New(context.Background(), cfg, mockRouter, mockBackendClient, mockDiscoveryMgr, backends, nil)
	require.NoError(t, err)

	// Start server
	mockDiscoveryMgr.EXPECT().Stop().AnyTimes()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = srv.Start(ctx)
	}()

	// Wait for server and health checks
	<-srv.Ready()
	time.Sleep(150 * time.Millisecond)

	// Create test request
	req := httptest.NewRequest(http.MethodGet, "/api/backends/health", nil)
	w := httptest.NewRecorder()

	// Call handler
	srv.handleBackendHealth(w, req)

	// Verify response
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var response BackendHealthResponse
	err = json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)

	assert.True(t, response.MonitoringEnabled)
	assert.NotNil(t, response.Summary)
	assert.Equal(t, 1, response.Summary.Total)
	assert.Equal(t, 1, response.Summary.Healthy)
	assert.NotNil(t, response.Backends)
	assert.Len(t, response.Backends, 1)
	assert.Contains(t, response.Backends, "backend-1")

	cancel()
	time.Sleep(100 * time.Millisecond)
}

// TestServer_Stop_StopsHealthMonitor verifies that Stop() properly cleans up the health monitor.
func TestServer_Stop_StopsHealthMonitor(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRouter := routermocks.NewMockRouter(ctrl)
	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoverymocks.NewMockManager(ctrl)

	backends := []vmcp.Backend{
		{ID: "backend-1", Name: "Backend 1", BaseURL: "http://localhost:8080", TransportType: "sse"},
	}

	// Mock health checks
	mockBackendClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(&vmcp.CapabilityList{}, nil).
		AnyTimes()

	// Create server WITH health monitoring
	cfg := &Config{
		Name:    "test-server",
		Version: "1.0.0",
		Host:    "127.0.0.1",
		Port:    0,
		HealthMonitorConfig: &health.MonitorConfig{
			CheckInterval:      50 * time.Millisecond,
			UnhealthyThreshold: 3,
			Timeout:            10 * time.Millisecond,
		},
	}

	srv, err := New(context.Background(), cfg, mockRouter, mockBackendClient, mockDiscoveryMgr, backends, nil)
	require.NoError(t, err)

	// Start server
	mockDiscoveryMgr.EXPECT().Stop().Times(1)
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	// Wait for server to be ready
	<-srv.Ready()
	time.Sleep(100 * time.Millisecond)

	// Verify health monitor is running
	srv.healthMonitorMu.RLock()
	assert.NotNil(t, srv.healthMonitor)
	srv.healthMonitorMu.RUnlock()

	// Cancel context to trigger graceful shutdown
	cancel()

	// Wait for server to stop
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for server to stop")
	}

	// Verify health monitor still exists after stop (not set to nil)
	// The monitor is stopped but the pointer remains valid
	srv.healthMonitorMu.RLock()
	assert.NotNil(t, srv.healthMonitor, "health monitor should still exist after stop")
	srv.healthMonitorMu.RUnlock()

	// Verify getter methods still work (they query the stopped monitor)
	// This ensures no panics occur when accessing a stopped monitor
	status, err := srv.GetBackendHealthStatus("backend-1")
	assert.NoError(t, err, "getter should not error after stop")
	// Status might be stale but should be valid
	assert.NotEqual(t, vmcp.BackendUnknown, status, "should return last known status")
}
