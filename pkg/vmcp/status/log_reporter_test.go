package status

import (
	"context"
	"testing"
	"time"
)

// TestNewLogReporter verifies that NewLogReporter creates a valid instance
func TestNewLogReporter(t *testing.T) {
	t.Parallel()
	reporter := NewLogReporter("test-server")

	if reporter == nil {
		t.Fatal("NewLogReporter returned nil")
	}

	if reporter.name != "test-server" {
		t.Errorf("Expected name 'test-server', got '%s'", reporter.name)
	}

	if reporter.stopCh == nil {
		t.Error("stopCh should be initialized")
	}

	if reporter.doneCh == nil {
		t.Error("doneCh should be initialized")
	}
}

// TestLogReporter_Report verifies that Report logs status without errors
func TestLogReporter_Report(t *testing.T) {
	t.Parallel()

	reporter := NewLogReporter("test-server")
	ctx := context.Background()

	status := &RuntimeStatus{
		Phase:             PhaseReady,
		Message:           "Test message",
		TotalToolCount:    10,
		HealthyBackends:   2,
		UnhealthyBackends: 0,
		Backends: []BackendHealthReport{
			{
				Name:        "backend1",
				Healthy:     true,
				Message:     "OK",
				LastChecked: time.Now(),
			},
			{
				Name:        "backend2",
				Healthy:     true,
				Message:     "OK",
				LastChecked: time.Now(),
			},
		},
		LastDiscoveryTime: time.Now(),
	}

	err := reporter.Report(ctx, status)
	if err != nil {
		t.Errorf("Report should not return error, got: %v", err)
	}
}

// TestLogReporter_StartStop verifies the start/stop lifecycle
func TestLogReporter_StartStop(t *testing.T) {
	t.Parallel()

	reporter := NewLogReporter("test-server")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the reporter
	err := reporter.Start(ctx, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("Start should not return error, got: %v", err)
	}

	// Let it run for a short time
	time.Sleep(250 * time.Millisecond)

	// Stop the reporter
	reporter.Stop()

	// Verify we can call Stop multiple times (should be safe)
	reporter.Stop()
}

// TestLogReporter_ContextCancellation verifies that context cancellation stops the reporter
func TestLogReporter_ContextCancellation(t *testing.T) {
	t.Parallel()

	reporter := NewLogReporter("test-server")
	ctx, cancel := context.WithCancel(context.Background())

	// Start the reporter
	err := reporter.Start(ctx, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("Start should not return error, got: %v", err)
	}

	// Cancel context
	cancel()

	// Wait a bit to let goroutine exit
	time.Sleep(50 * time.Millisecond)

	// Stop should not block (goroutine should already be stopped)
	done := make(chan struct{})
	go func() {
		reporter.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Success - Stop() completed quickly
	case <-time.After(1 * time.Second):
		t.Error("Stop() blocked, goroutine may not have exited")
	}
}

// TestLogReporter_EmptyBackends verifies handling of status with no backends
func TestLogReporter_EmptyBackends(t *testing.T) {
	t.Parallel()

	reporter := NewLogReporter("test-server")
	ctx := context.Background()

	status := &RuntimeStatus{
		Phase:             PhasePending,
		Message:           "Starting up",
		TotalToolCount:    0,
		HealthyBackends:   0,
		UnhealthyBackends: 0,
		Backends:          []BackendHealthReport{}, // Empty
		LastDiscoveryTime: time.Now(),
	}

	err := reporter.Report(ctx, status)
	if err != nil {
		t.Errorf("Report should handle empty backends, got error: %v", err)
	}
}

// TestLogReporter_DegradedStatus verifies logging of degraded status
func TestLogReporter_DegradedStatus(t *testing.T) {
	t.Parallel()

	reporter := NewLogReporter("test-server")
	ctx := context.Background()

	status := &RuntimeStatus{
		Phase:             PhaseDegraded,
		Message:           "One backend unhealthy",
		TotalToolCount:    10,
		HealthyBackends:   1,
		UnhealthyBackends: 1,
		Backends: []BackendHealthReport{
			{Name: "backend1", Healthy: true, Message: "OK"},
			{Name: "backend2", Healthy: false, Message: "Connection timeout"},
		},
		LastDiscoveryTime: time.Now(),
	}

	err := reporter.Report(ctx, status)
	if err != nil {
		t.Errorf("Report should handle degraded status, got error: %v", err)
	}
}
