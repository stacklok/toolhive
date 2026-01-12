package status

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

func TestNewNoOpReporter(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	require.NotNil(t, reporter)
}

func TestNoOpReporter_Report(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	ctx := context.Background()

	// Create a sample status
	status := &RuntimeStatus{
		Phase:   PhaseReady,
		Message: "All systems operational",
		Conditions: []Condition{
			{
				Type:               ConditionServerReady,
				Status:             ConditionTrue,
				LastTransitionTime: time.Now(),
				Reason:             "ServerStarted",
				Message:            "Server is ready",
			},
		},
		DiscoveredBackends: []DiscoveredBackend{
			{
				ID:           "backend-1",
				Name:         "Test Backend",
				HealthStatus: vmcp.BackendHealthy,
				BaseURL:      "http://localhost:8080",
				ToolCount:    5,
			},
		},
		TotalToolCount:        5,
		HealthyBackendCount:   1,
		UnhealthyBackendCount: 0,
		LastUpdateTime:        time.Now(),
	}

	// Report should return nil without error
	err := reporter.Report(ctx, status)
	assert.NoError(t, err)
}

func TestNoOpReporter_Report_NilStatus(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	ctx := context.Background()

	// Report with nil status should not panic or error
	err := reporter.Report(ctx, nil)
	assert.NoError(t, err)
}

func TestNoOpReporter_Report_CancelledContext(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	status := &RuntimeStatus{
		Phase:   PhaseReady,
		Message: "Test",
	}

	// Report should return nil even with cancelled context
	err := reporter.Report(ctx, status)
	assert.NoError(t, err)
}

func TestNoOpReporter_Start(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	ctx := context.Background()

	callCount := 0
	statusFunc := func() *RuntimeStatus {
		callCount++
		return &RuntimeStatus{
			Phase:   PhaseReady,
			Message: "Test status",
		}
	}

	// Start should return nil without error
	err := reporter.Start(ctx, 100*time.Millisecond, statusFunc)
	assert.NoError(t, err)

	// Wait a bit to ensure statusFunc is not called
	time.Sleep(300 * time.Millisecond)

	// statusFunc should never be called by NoOpReporter
	assert.Equal(t, 0, callCount, "statusFunc should not be called by NoOpReporter")
}

func TestNoOpReporter_Start_NilStatusFunc(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	ctx := context.Background()

	// Start with nil statusFunc should not panic or error
	err := reporter.Start(ctx, 100*time.Millisecond, nil)
	assert.NoError(t, err)
}

func TestNoOpReporter_Start_ZeroInterval(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	ctx := context.Background()

	statusFunc := func() *RuntimeStatus {
		return &RuntimeStatus{Phase: PhaseReady}
	}

	// Start with zero interval should not panic or error
	err := reporter.Start(ctx, 0, statusFunc)
	assert.NoError(t, err)
}

func TestNoOpReporter_Start_CancelledContext(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	statusFunc := func() *RuntimeStatus {
		return &RuntimeStatus{Phase: PhaseReady}
	}

	// Start should return nil even with cancelled context
	err := reporter.Start(ctx, 100*time.Millisecond, statusFunc)
	assert.NoError(t, err)
}

func TestNoOpReporter_Stop(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()

	// Stop should not panic
	assert.NotPanics(t, func() {
		reporter.Stop()
	})
}

func TestNoOpReporter_Stop_WithoutStart(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()

	// Stop without Start should not panic
	assert.NotPanics(t, func() {
		reporter.Stop()
	})
}

func TestNoOpReporter_Stop_Multiple(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()

	// Multiple Stop calls should not panic
	assert.NotPanics(t, func() {
		reporter.Stop()
		reporter.Stop()
		reporter.Stop()
	})
}

func TestNoOpReporter_FullLifecycle(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	ctx := context.Background()

	// Start
	statusFunc := func() *RuntimeStatus {
		return &RuntimeStatus{
			Phase:          PhaseReady,
			Message:        "Operational",
			LastUpdateTime: time.Now(),
		}
	}

	err := reporter.Start(ctx, 100*time.Millisecond, statusFunc)
	assert.NoError(t, err)

	// Report a few times
	for range 3 {
		status := statusFunc()
		err := reporter.Report(ctx, status)
		assert.NoError(t, err)
		time.Sleep(50 * time.Millisecond)
	}

	// Stop
	assert.NotPanics(t, func() {
		reporter.Stop()
	})
}

func TestNoOpReporter_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	ctx := context.Background()

	// Concurrent Start calls
	for range 10 {
		go func() {
			statusFunc := func() *RuntimeStatus {
				return &RuntimeStatus{Phase: PhaseReady}
			}
			_ = reporter.Start(ctx, 100*time.Millisecond, statusFunc)
		}()
	}

	// Concurrent Report calls
	for range 10 {
		go func() {
			status := &RuntimeStatus{
				Phase:   PhaseReady,
				Message: "Concurrent test",
			}
			_ = reporter.Report(ctx, status)
		}()
	}

	// Concurrent Stop calls
	for range 10 {
		go func() {
			reporter.Stop()
		}()
	}

	// Wait for goroutines to complete
	time.Sleep(200 * time.Millisecond)
}

func TestNoOpReporter_ImplementsInterface(t *testing.T) {
	t.Parallel()

	// Verify NoOpReporter implements Reporter interface
	var _ Reporter = (*NoOpReporter)(nil)
}

func TestNoOpReporter_ComplexStatus(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	ctx := context.Background()

	// Create a complex status with multiple conditions and backends
	status := &RuntimeStatus{
		Phase:   PhaseDegraded,
		Message: "Some backends are unhealthy",
		Conditions: []Condition{
			{
				Type:               ConditionServerReady,
				Status:             ConditionTrue,
				LastTransitionTime: time.Now(),
				Reason:             "ServerStarted",
				Message:            "Server is accepting requests",
			},
			{
				Type:               ConditionBackendsDiscovered,
				Status:             ConditionTrue,
				LastTransitionTime: time.Now(),
				Reason:             "DiscoveryComplete",
				Message:            "All backends discovered",
			},
			{
				Type:               ConditionBackendsHealthy,
				Status:             ConditionFalse,
				LastTransitionTime: time.Now(),
				Reason:             "BackendUnhealthy",
				Message:            "One or more backends are unhealthy",
			},
			{
				Type:               ConditionCapabilitiesAggregated,
				Status:             ConditionTrue,
				LastTransitionTime: time.Now(),
				Reason:             "AggregationComplete",
				Message:            "Capabilities aggregated successfully",
			},
		},
		DiscoveredBackends: []DiscoveredBackend{
			{
				ID:            "backend-1",
				Name:          "Healthy Backend",
				HealthStatus:  vmcp.BackendHealthy,
				BaseURL:       "http://backend1:8080",
				TransportType: "http",
				ToolCount:     10,
				ResourceCount: 5,
				PromptCount:   3,
				LastCheckTime: time.Now(),
			},
			{
				ID:            "backend-2",
				Name:          "Unhealthy Backend",
				HealthStatus:  vmcp.BackendUnhealthy,
				BaseURL:       "http://backend2:8080",
				TransportType: "sse",
				ToolCount:     0,
				ResourceCount: 0,
				PromptCount:   0,
				LastCheckTime: time.Now(),
			},
			{
				ID:            "backend-3",
				Name:          "Degraded Backend",
				HealthStatus:  vmcp.BackendDegraded,
				BaseURL:       "http://backend3:8080",
				TransportType: "stdio",
				ToolCount:     7,
				ResourceCount: 2,
				PromptCount:   1,
				LastCheckTime: time.Now(),
			},
		},
		TotalToolCount:        17,
		TotalResourceCount:    7,
		TotalPromptCount:      4,
		HealthyBackendCount:   1,
		UnhealthyBackendCount: 1,
		DegradedBackendCount:  1,
		LastDiscoveryTime:     time.Now().Add(-5 * time.Minute),
		LastUpdateTime:        time.Now(),
	}

	// Report should handle complex status without error
	err := reporter.Report(ctx, status)
	assert.NoError(t, err)
}
