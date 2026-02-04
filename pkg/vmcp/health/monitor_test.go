// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
)

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

			monitor, err := NewMonitor(mockClient, backends, tt.config, "")
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

	monitor, err := NewMonitor(mockClient, backends, config, "")
	require.NoError(t, err)

	// Start monitor
	ctx := context.Background()
	err = monitor.Start(ctx)
	require.NoError(t, err)

	// Wait for at least one health check to complete
	require.Eventually(t, func() bool {
		return monitor.IsBackendHealthy("backend-1")
	}, 500*time.Millisecond, 10*time.Millisecond, "backend should become healthy")

	// Stop monitor
	err = monitor.Stop()
	require.NoError(t, err)

	// Verify cannot start again without recreating
	err = monitor.Start(ctx)
	assert.Error(t, err)
}

func TestMonitor_StartErrors(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockClient := mocks.NewMockBackendClient(ctrl)
	backends := []vmcp.Backend{
		{ID: "backend-1", Name: "Backend 1", BaseURL: "http://localhost:8080"},
	}

	config := MonitorConfig{
		CheckInterval:      100 * time.Millisecond,
		UnhealthyThreshold: 3,
		Timeout:            50 * time.Millisecond,
	}

	tests := []struct {
		name      string
		setupFunc func(*Monitor) error
		expectErr bool
	}{
		{
			name: "nil context",
			setupFunc: func(m *Monitor) error {
				return m.Start(nil) //nolint:staticcheck // Testing nil context error handling
			},
			expectErr: true,
		},
		{
			name: "already started",
			setupFunc: func(m *Monitor) error {
				mockClient.EXPECT().
					ListCapabilities(gomock.Any(), gomock.Any()).
					Return(&vmcp.CapabilityList{}, nil).
					AnyTimes()

				ctx := context.Background()
				if err := m.Start(ctx); err != nil {
					return err
				}
				// Try to start again - should return error
				err := m.Start(ctx)
				// Stop the monitor since it was started successfully the first time
				_ = m.Stop()
				return err
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			monitor, err := NewMonitor(mockClient, backends, config, "")
			require.NoError(t, err)

			err = tt.setupFunc(monitor)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestMonitor_StopWithoutStart(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	backends := []vmcp.Backend{
		{ID: "backend-1", Name: "Backend 1", BaseURL: "http://localhost:8080"},
	}

	config := MonitorConfig{
		CheckInterval:      100 * time.Millisecond,
		UnhealthyThreshold: 3,
		Timeout:            50 * time.Millisecond,
	}

	monitor, err := NewMonitor(mockClient, backends, config, "")
	require.NoError(t, err)

	// Try to stop without starting
	err = monitor.Stop()
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

	monitor, err := NewMonitor(mockClient, backends, config, "")
	require.NoError(t, err)

	ctx := context.Background()
	err = monitor.Start(ctx)
	require.NoError(t, err)
	defer func() {
		_ = monitor.Stop()
	}()

	// Wait for threshold to be exceeded (2 failures)
	require.Eventually(t, func() bool {
		status, err := monitor.GetBackendStatus("backend-1")
		return err == nil && status == vmcp.BackendUnhealthy
	}, 500*time.Millisecond, 10*time.Millisecond, "backend should become unhealthy after threshold")

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

	monitor, err := NewMonitor(mockClient, backends, config, "")
	require.NoError(t, err)

	ctx := context.Background()
	err = monitor.Start(ctx)
	require.NoError(t, err)
	defer func() {
		_ = monitor.Stop()
	}()

	// Wait for health checks to complete
	require.Eventually(t, func() bool {
		summary := monitor.GetHealthSummary()
		return summary.Healthy == 1 && summary.Unhealthy == 1
	}, 500*time.Millisecond, 10*time.Millisecond, "summary should show 1 healthy and 1 unhealthy")

	summary := monitor.GetHealthSummary()
	assert.Equal(t, 2, summary.Total)
	assert.Equal(t, 1, summary.Healthy)
	assert.Equal(t, 1, summary.Unhealthy)
}

func TestMonitor_GetBackendStatus(t *testing.T) {
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

	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(&vmcp.CapabilityList{}, nil).
		AnyTimes()

	monitor, err := NewMonitor(mockClient, backends, config, "")
	require.NoError(t, err)

	ctx := context.Background()
	err = monitor.Start(ctx)
	require.NoError(t, err)
	defer func() {
		_ = monitor.Stop()
	}()

	// Wait for initial health check to complete
	require.Eventually(t, func() bool {
		status, err := monitor.GetBackendStatus("backend-1")
		return err == nil && status == vmcp.BackendHealthy
	}, 500*time.Millisecond, 10*time.Millisecond, "backend status should be available and healthy")

	// Test getting status for existing backend
	status, err := monitor.GetBackendStatus("backend-1")
	assert.NoError(t, err)
	assert.Equal(t, vmcp.BackendHealthy, status)

	// Test getting status for non-existent backend
	status, err = monitor.GetBackendStatus("nonexistent")
	assert.Error(t, err)
	assert.Equal(t, vmcp.BackendUnknown, status)
}

func TestMonitor_GetBackendState(t *testing.T) {
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

	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(&vmcp.CapabilityList{}, nil).
		AnyTimes()

	monitor, err := NewMonitor(mockClient, backends, config, "")
	require.NoError(t, err)

	ctx := context.Background()
	err = monitor.Start(ctx)
	require.NoError(t, err)
	defer func() {
		_ = monitor.Stop()
	}()

	// Wait for initial health check to complete
	require.Eventually(t, func() bool {
		state, err := monitor.GetBackendState("backend-1")
		return err == nil && state != nil && state.Status == vmcp.BackendHealthy
	}, 500*time.Millisecond, 10*time.Millisecond, "backend state should be available and healthy")

	// Test getting state for existing backend
	state, err := monitor.GetBackendState("backend-1")
	assert.NoError(t, err)
	assert.NotNil(t, state)
	assert.Equal(t, vmcp.BackendHealthy, state.Status)

	// Test getting state for non-existent backend
	state, err = monitor.GetBackendState("nonexistent")
	assert.Error(t, err)
	assert.Nil(t, state)
}

func TestMonitor_GetAllBackendStates(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	backends := []vmcp.Backend{
		{ID: "backend-1", Name: "Backend 1", BaseURL: "http://localhost:8080", TransportType: "sse"},
		{ID: "backend-2", Name: "Backend 2", BaseURL: "http://localhost:8081", TransportType: "sse"},
	}

	config := MonitorConfig{
		CheckInterval:      100 * time.Millisecond,
		UnhealthyThreshold: 3,
		Timeout:            50 * time.Millisecond,
	}

	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(&vmcp.CapabilityList{}, nil).
		AnyTimes()

	monitor, err := NewMonitor(mockClient, backends, config, "")
	require.NoError(t, err)

	ctx := context.Background()
	err = monitor.Start(ctx)
	require.NoError(t, err)
	defer func() {
		_ = monitor.Stop()
	}()

	// Wait for initial health checks to complete for both backends
	require.Eventually(t, func() bool {
		allStates := monitor.GetAllBackendStates()
		return len(allStates) == 2
	}, 500*time.Millisecond, 10*time.Millisecond, "all backend states should be available")

	allStates := monitor.GetAllBackendStates()
	assert.Len(t, allStates, 2)
	assert.Contains(t, allStates, "backend-1")
	assert.Contains(t, allStates, "backend-2")
}

func TestMonitor_ContextCancellation(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	backends := []vmcp.Backend{
		{ID: "backend-1", Name: "Backend 1", BaseURL: "http://localhost:8080", TransportType: "sse"},
	}

	config := MonitorConfig{
		CheckInterval:      50 * time.Millisecond,
		UnhealthyThreshold: 3,
		Timeout:            10 * time.Millisecond,
	}

	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(&vmcp.CapabilityList{}, nil).
		AnyTimes()

	monitor, err := NewMonitor(mockClient, backends, config, "")
	require.NoError(t, err)

	// Start with cancellable context
	ctx, cancel := context.WithCancel(context.Background())
	err = monitor.Start(ctx)
	require.NoError(t, err)

	// Wait for a few health checks to run
	require.Eventually(t, func() bool {
		return monitor.IsBackendHealthy("backend-1")
	}, 500*time.Millisecond, 10*time.Millisecond, "backend should have completed at least one health check")

	// Cancel context
	cancel()

	// Give goroutines time to observe cancellation
	// Note: We can't easily poll for goroutine completion, so a short sleep is acceptable here
	time.Sleep(100 * time.Millisecond)

	// Monitor should still be running (context cancellation stops checks but doesn't stop the monitor)
	// Stop explicitly
	err = monitor.Stop()
	assert.NoError(t, err)
}

func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	config := DefaultConfig()
	assert.Equal(t, 30*time.Second, config.CheckInterval)
	assert.Equal(t, 3, config.UnhealthyThreshold)
	assert.Equal(t, 10*time.Second, config.Timeout)
	assert.Equal(t, 5*time.Second, config.DegradedThreshold)
}

func TestSummary_String(t *testing.T) {
	t.Parallel()

	summary := Summary{
		Total:           10,
		Healthy:         5,
		Degraded:        1,
		Unhealthy:       2,
		Unknown:         1,
		Unauthenticated: 1,
	}

	str := summary.String()
	assert.Contains(t, str, "total=10")
	assert.Contains(t, str, "healthy=5")
	assert.Contains(t, str, "degraded=1")
	assert.Contains(t, str, "unhealthy=2")
	assert.Contains(t, str, "unknown=1")
	assert.Contains(t, str, "unauthenticated=1")
}

// testContextKey is a custom type for context keys in tests
type testContextKey string

// TestWithHealthCheckMarker tests the WithHealthCheckMarker function
func TestWithHealthCheckMarker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		setupCtx              func() context.Context
		expectPanic           bool
		originalAlreadyMarked bool // Set to true for idempotent test case
	}{
		{
			name:                  "marks background context",
			setupCtx:              func() context.Context { return context.Background() },
			expectPanic:           false,
			originalAlreadyMarked: false,
		},
		{
			name:                  "marks TODO context",
			setupCtx:              func() context.Context { return context.TODO() },
			expectPanic:           false,
			originalAlreadyMarked: false,
		},
		{
			name: "marks context with existing values",
			setupCtx: func() context.Context {
				ctx := context.Background()
				ctx = context.WithValue(ctx, testContextKey("custom-key"), "custom-value")
				return ctx
			},
			expectPanic:           false,
			originalAlreadyMarked: false,
		},
		{
			name: "marks already marked context (idempotent)",
			setupCtx: func() context.Context {
				ctx := context.Background()
				return WithHealthCheckMarker(ctx)
			},
			expectPanic:           false,
			originalAlreadyMarked: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.expectPanic {
				assert.Panics(t, func() {
					WithHealthCheckMarker(tt.setupCtx())
				})
				return
			}

			ctx := tt.setupCtx()
			markedCtx := WithHealthCheckMarker(ctx)

			// Verify marked context is not nil
			assert.NotNil(t, markedCtx, "marked context should not be nil")

			// Verify marked context can be checked
			assert.True(t, IsHealthCheck(markedCtx), "marked context should be identified as health check")

			// Verify original context state matches expectations
			if tt.originalAlreadyMarked {
				assert.True(t, IsHealthCheck(ctx), "original context should remain marked")
			} else {
				assert.False(t, IsHealthCheck(ctx), "original context should not be marked")
			}
		})
	}
}

// TestIsHealthCheck tests the IsHealthCheck function
func TestIsHealthCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		setupCtx func() context.Context
		expected bool
	}{
		{
			name:     "returns true for marked context",
			setupCtx: func() context.Context { return WithHealthCheckMarker(context.Background()) },
			expected: true,
		},
		{
			name:     "returns false for unmarked background context",
			setupCtx: func() context.Context { return context.Background() },
			expected: false,
		},
		{
			name:     "returns false for unmarked TODO context",
			setupCtx: func() context.Context { return context.TODO() },
			expected: false,
		},
		{
			name:     "returns false for nil context",
			setupCtx: func() context.Context { return nil },
			expected: false,
		},
		{
			name: "returns false for context with different key",
			setupCtx: func() context.Context {
				return context.WithValue(context.Background(), testContextKey("other-key"), true)
			},
			expected: false,
		},
		{
			name: "returns false for context with wrong value type",
			setupCtx: func() context.Context {
				return context.WithValue(context.Background(), healthCheckContextKey{}, "not-a-bool")
			},
			expected: false,
		},
		{
			name: "returns false for context with false value",
			setupCtx: func() context.Context {
				return context.WithValue(context.Background(), healthCheckContextKey{}, false)
			},
			expected: false,
		},
		{
			name: "returns true when nested in parent context",
			setupCtx: func() context.Context {
				markedCtx := WithHealthCheckMarker(context.Background())
				return context.WithValue(markedCtx, testContextKey("custom-key"), "value")
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := tt.setupCtx()
			result := IsHealthCheck(ctx)
			assert.Equal(t, tt.expected, result, "IsHealthCheck returned unexpected value")
		})
	}
}

// TestHealthCheckMarker_Integration tests the integration of marker functions
func TestHealthCheckMarker_Integration(t *testing.T) {
	t.Parallel()

	t.Run("marker persists through context chain", func(t *testing.T) {
		t.Parallel()

		// Create base context
		baseCtx := context.Background()
		assert.False(t, IsHealthCheck(baseCtx))

		// Mark as health check
		healthCtx := WithHealthCheckMarker(baseCtx)
		assert.True(t, IsHealthCheck(healthCtx))

		// Add more values to context
		ctx1 := context.WithValue(healthCtx, testContextKey("key1"), "value1")
		assert.True(t, IsHealthCheck(ctx1), "marker should persist through WithValue")

		ctx2 := context.WithValue(ctx1, testContextKey("key2"), "value2")
		assert.True(t, IsHealthCheck(ctx2), "marker should persist through multiple WithValue")
	})

	t.Run("marker persists through context with cancel", func(t *testing.T) {
		t.Parallel()

		healthCtx := WithHealthCheckMarker(context.Background())
		cancelCtx, cancel := context.WithCancel(healthCtx)
		defer cancel()

		assert.True(t, IsHealthCheck(cancelCtx), "marker should persist through WithCancel")
	})

	t.Run("marker persists through context with timeout", func(t *testing.T) {
		t.Parallel()

		healthCtx := WithHealthCheckMarker(context.Background())
		timeoutCtx, cancel := context.WithTimeout(healthCtx, time.Second)
		defer cancel()

		assert.True(t, IsHealthCheck(timeoutCtx), "marker should persist through WithTimeout")
	})

	t.Run("multiple markers don't interfere", func(t *testing.T) {
		t.Parallel()

		// Mark same context twice
		ctx1 := WithHealthCheckMarker(context.Background())
		ctx2 := WithHealthCheckMarker(ctx1)

		assert.True(t, IsHealthCheck(ctx1))
		assert.True(t, IsHealthCheck(ctx2))
	})

	t.Run("marker is request-scoped and doesn't leak", func(t *testing.T) {
		t.Parallel()

		// Create two independent contexts
		baseCtx := context.Background()

		// Mark one but not the other
		markedCtx := WithHealthCheckMarker(baseCtx)
		unmarkedCtx := context.WithValue(baseCtx, testContextKey("some-key"), "some-value")

		// Verify independence
		assert.True(t, IsHealthCheck(markedCtx), "marked context should be health check")
		assert.False(t, IsHealthCheck(unmarkedCtx), "unmarked context should not be health check")
		assert.False(t, IsHealthCheck(baseCtx), "base context should not be health check")
	})
}
