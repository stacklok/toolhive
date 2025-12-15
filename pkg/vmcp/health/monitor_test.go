package health

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
)

func TestNewHealthChecker(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	timeout := 5 * time.Second

	checker := NewHealthChecker(mockClient, timeout)
	require.NotNil(t, checker)

	// Type assert to access internals for verification
	hc, ok := checker.(*healthChecker)
	require.True(t, ok)
	assert.Equal(t, mockClient, hc.client)
	assert.Equal(t, timeout, hc.timeout)
}

func TestHealthChecker_CheckHealth_Success(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(&vmcp.CapabilityList{}, nil).
		Times(1)

	checker := NewHealthChecker(mockClient, 5*time.Second)
	target := &vmcp.BackendTarget{
		WorkloadID:   "backend-1",
		WorkloadName: "test-backend",
		BaseURL:      "http://localhost:8080",
	}

	status, err := checker.CheckHealth(context.Background(), target)
	assert.NoError(t, err)
	assert.Equal(t, vmcp.BackendHealthy, status)
}

func TestHealthChecker_CheckHealth_Failure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		err            error
		expectedStatus vmcp.BackendHealthStatus
	}{
		{
			name:           "timeout error",
			err:            fmt.Errorf("context deadline exceeded"),
			expectedStatus: vmcp.BackendUnhealthy,
		},
		{
			name:           "connection refused",
			err:            fmt.Errorf("connection refused"),
			expectedStatus: vmcp.BackendUnhealthy,
		},
		{
			name:           "authentication failed",
			err:            fmt.Errorf("authentication failed: invalid token"),
			expectedStatus: vmcp.BackendUnauthenticated,
		},
		{
			name:           "unauthorized",
			err:            fmt.Errorf("unauthorized: 401"),
			expectedStatus: vmcp.BackendUnauthenticated,
		},
		{
			name:           "generic error",
			err:            fmt.Errorf("unknown error"),
			expectedStatus: vmcp.BackendUnhealthy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockClient := mocks.NewMockBackendClient(ctrl)
			mockClient.EXPECT().
				ListCapabilities(gomock.Any(), gomock.Any()).
				Return(nil, tt.err).
				Times(1)

			checker := NewHealthChecker(mockClient, 5*time.Second)
			target := &vmcp.BackendTarget{
				WorkloadID:   "backend-1",
				WorkloadName: "test-backend",
				BaseURL:      "http://localhost:8080",
			}

			status, err := checker.CheckHealth(context.Background(), target)
			assert.Error(t, err)
			assert.Equal(t, tt.expectedStatus, status)
		})
	}
}

func TestHealthChecker_CheckHealth_Timeout(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, _ *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
			// Simulate slow backend
			select {
			case <-time.After(2 * time.Second):
				return &vmcp.CapabilityList{}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}).
		Times(1)

	checker := NewHealthChecker(mockClient, 100*time.Millisecond)
	target := &vmcp.BackendTarget{
		WorkloadID:   "backend-1",
		WorkloadName: "test-backend",
		BaseURL:      "http://localhost:8080",
	}

	status, err := checker.CheckHealth(context.Background(), target)
	assert.Error(t, err)
	assert.Equal(t, vmcp.BackendUnhealthy, status)
}

func TestStatusTracker_RecordSuccess(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(3)

	// Record success for new backend
	tracker.RecordSuccess("backend-1", "Backend 1")

	status, exists := tracker.GetStatus("backend-1")
	assert.True(t, exists)
	assert.Equal(t, vmcp.BackendHealthy, status)

	state, exists := tracker.GetState("backend-1")
	assert.True(t, exists)
	assert.Equal(t, vmcp.BackendHealthy, state.Status)
	assert.Equal(t, 0, state.ConsecutiveFailures)
	assert.Nil(t, state.LastError)
}

func TestStatusTracker_RecordFailure(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(3)
	testErr := errors.New("health check failed")

	// Record first failure
	tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, testErr)

	state, exists := tracker.GetState("backend-1")
	assert.True(t, exists)
	assert.Equal(t, 1, state.ConsecutiveFailures)
	// Status should still be BackendUnhealthy but not yet transitioned
	assert.Equal(t, vmcp.BackendUnhealthy, state.Status)

	// Record second failure
	tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, testErr)
	state, _ = tracker.GetState("backend-1")
	assert.Equal(t, 2, state.ConsecutiveFailures)

	// Record third failure - should trigger transition
	tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, testErr)
	state, _ = tracker.GetState("backend-1")
	assert.Equal(t, 3, state.ConsecutiveFailures)
	assert.Equal(t, vmcp.BackendUnhealthy, state.Status)
	assert.NotNil(t, state.LastError)
}

func TestStatusTracker_RecoveryAfterFailures(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(3)
	testErr := errors.New("health check failed")

	// Record 3 failures to mark unhealthy
	for i := 0; i < 3; i++ {
		tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, testErr)
	}

	state, _ := tracker.GetState("backend-1")
	assert.Equal(t, vmcp.BackendUnhealthy, state.Status)
	assert.Equal(t, 3, state.ConsecutiveFailures)

	// Record success - should recover
	tracker.RecordSuccess("backend-1", "Backend 1")

	state, _ = tracker.GetState("backend-1")
	assert.Equal(t, vmcp.BackendHealthy, state.Status)
	assert.Equal(t, 0, state.ConsecutiveFailures)
	assert.Nil(t, state.LastError)
}

func TestStatusTracker_GetAllStates(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(3)

	tracker.RecordSuccess("backend-1", "Backend 1")
	tracker.RecordFailure("backend-2", "Backend 2", vmcp.BackendUnhealthy, errors.New("failed"))

	allStates := tracker.GetAllStates()
	assert.Len(t, allStates, 2)

	assert.Equal(t, vmcp.BackendHealthy, allStates["backend-1"].Status)
	assert.Equal(t, vmcp.BackendUnhealthy, allStates["backend-2"].Status)
}

func TestStatusTracker_IsHealthy(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(3)

	tracker.RecordSuccess("backend-1", "Backend 1")
	tracker.RecordFailure("backend-2", "Backend 2", vmcp.BackendUnhealthy, errors.New("failed"))

	assert.True(t, tracker.IsHealthy("backend-1"))
	assert.False(t, tracker.IsHealthy("backend-2"))
	assert.False(t, tracker.IsHealthy("backend-nonexistent"))
}

func TestNewMonitor_Validation(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockClient := mocks.NewMockBackendClient(ctrl)
	backends := []vmcp.Backend{
		{ID: "backend-1", Name: "Backend 1", BaseURL: "http://localhost:8080"},
	}

	tests := []struct {
		name        string
		config      MonitorConfig
		expectError bool
	}{
		{
			name: "valid config",
			config: MonitorConfig{
				CheckInterval:      30 * time.Second,
				UnhealthyThreshold: 3,
				Timeout:            10 * time.Second,
			},
			expectError: false,
		},
		{
			name: "invalid check interval",
			config: MonitorConfig{
				CheckInterval:      0,
				UnhealthyThreshold: 3,
				Timeout:            10 * time.Second,
			},
			expectError: true,
		},
		{
			name: "invalid unhealthy threshold",
			config: MonitorConfig{
				CheckInterval:      30 * time.Second,
				UnhealthyThreshold: 0,
				Timeout:            10 * time.Second,
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			monitor, err := NewMonitor(mockClient, backends, tt.config)
			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, monitor)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, monitor)
			}
		})
	}
}

func TestMonitor_StartStop(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	backends := []vmcp.Backend{
		{ID: "backend-1", Name: "Backend 1", BaseURL: "http://localhost:8080", TransportType: "sse"},
	}

	config := MonitorConfig{
		CheckInterval:      100 * time.Millisecond,
		UnhealthyThreshold: 3,
		Timeout:            50 * time.Millisecond,
	}

	// Mock health check calls
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(&vmcp.CapabilityList{}, nil).
		AnyTimes()

	monitor, err := NewMonitor(mockClient, backends, config)
	require.NoError(t, err)

	// Start monitor
	ctx := context.Background()
	err = monitor.Start(ctx)
	require.NoError(t, err)

	// Wait for at least one health check
	time.Sleep(150 * time.Millisecond)

	// Verify backend is healthy
	assert.True(t, monitor.IsBackendHealthy("backend-1"))

	// Stop monitor
	err = monitor.Stop()
	require.NoError(t, err)

	// Verify cannot start again without recreating
	err = monitor.Start(ctx)
	assert.Error(t, err)
}

func TestMonitor_PeriodicHealthChecks(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	backends := []vmcp.Backend{
		{ID: "backend-1", Name: "Backend 1", BaseURL: "http://localhost:8080", TransportType: "sse"},
	}

	config := MonitorConfig{
		CheckInterval:      50 * time.Millisecond,
		UnhealthyThreshold: 2,
		Timeout:            10 * time.Millisecond,
	}

	// Mock health check to fail
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(nil, errors.New("backend unavailable")).
		MinTimes(2)

	monitor, err := NewMonitor(mockClient, backends, config)
	require.NoError(t, err)

	ctx := context.Background()
	err = monitor.Start(ctx)
	require.NoError(t, err)
	defer func() {
		_ = monitor.Stop()
	}()

	// Wait for threshold to be exceeded (2 failures * 50ms + buffer)
	time.Sleep(200 * time.Millisecond)

	// Backend should be marked unhealthy
	status, err := monitor.GetBackendStatus("backend-1")
	assert.NoError(t, err)
	assert.Equal(t, vmcp.BackendUnhealthy, status)

	state, err := monitor.GetBackendState("backend-1")
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, state.ConsecutiveFailures, 2)
}

func TestMonitor_GetHealthSummary(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	backends := []vmcp.Backend{
		{ID: "backend-1", Name: "Backend 1", BaseURL: "http://localhost:8080", TransportType: "sse"},
		{ID: "backend-2", Name: "Backend 2", BaseURL: "http://localhost:8081", TransportType: "sse"},
	}

	config := MonitorConfig{
		CheckInterval:      50 * time.Millisecond,
		UnhealthyThreshold: 1,
		Timeout:            10 * time.Millisecond,
	}

	// Backend 1 succeeds, Backend 2 fails
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, target *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
			if target.WorkloadID == "backend-1" {
				return &vmcp.CapabilityList{}, nil
			}
			return nil, errors.New("backend unavailable")
		}).
		AnyTimes()

	monitor, err := NewMonitor(mockClient, backends, config)
	require.NoError(t, err)

	ctx := context.Background()
	err = monitor.Start(ctx)
	require.NoError(t, err)
	defer func() {
		_ = monitor.Stop()
	}()

	// Wait for health checks to complete
	time.Sleep(100 * time.Millisecond)

	summary := monitor.GetHealthSummary()
	assert.Equal(t, 2, summary.Total)
	assert.Equal(t, 1, summary.Healthy)
	assert.Equal(t, 1, summary.Unhealthy)
}

func TestCategorizeError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		err            error
		expectedStatus vmcp.BackendHealthStatus
	}{
		{
			name:           "nil error",
			err:            nil,
			expectedStatus: vmcp.BackendHealthy,
		},
		{
			name:           "authentication failed",
			err:            errors.New("authentication failed"),
			expectedStatus: vmcp.BackendUnauthenticated,
		},
		{
			name:           "unauthorized",
			err:            errors.New("unauthorized access"),
			expectedStatus: vmcp.BackendUnauthenticated,
		},
		{
			name:           "401 error",
			err:            errors.New("HTTP 401"),
			expectedStatus: vmcp.BackendUnauthenticated,
		},
		{
			name:           "timeout",
			err:            errors.New("request timeout"),
			expectedStatus: vmcp.BackendUnhealthy,
		},
		{
			name:           "deadline exceeded",
			err:            errors.New("context deadline exceeded"),
			expectedStatus: vmcp.BackendUnhealthy,
		},
		{
			name:           "connection refused",
			err:            errors.New("connection refused"),
			expectedStatus: vmcp.BackendUnhealthy,
		},
		{
			name:           "generic error",
			err:            errors.New("something went wrong"),
			expectedStatus: vmcp.BackendUnhealthy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			status := categorizeError(tt.err)
			assert.Equal(t, tt.expectedStatus, status)
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	config := DefaultConfig()
	assert.Equal(t, 30*time.Second, config.CheckInterval)
	assert.Equal(t, 3, config.UnhealthyThreshold)
	assert.Equal(t, 10*time.Second, config.Timeout)
}
