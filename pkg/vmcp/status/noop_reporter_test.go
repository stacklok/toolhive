// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package status

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

func TestNewNoOpReporter(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	require.NotNil(t, reporter)
}

func TestNoOpReporter_ReportStatus(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	ctx := context.Background()

	// Create a sample status
	now := metav1.Now()
	status := &vmcp.Status{
		Phase:   vmcp.PhaseReady,
		Message: "All systems operational",
		Conditions: []vmcp.Condition{
			{
				Type:               vmcp.ConditionTypeReady,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             vmcp.ReasonServerReady,
				Message:            "Server is ready",
			},
		},
		DiscoveredBackends: []vmcp.DiscoveredBackend{
			{
				Name:   "test-backend",
				URL:    "http://localhost:8080",
				Status: "ready",
			},
		},
		Timestamp: time.Now(),
	}

	// ReportStatus should return nil without error
	err := reporter.ReportStatus(ctx, status)
	assert.NoError(t, err)
}

func TestNoOpReporter_ReportStatus_NilStatus(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	ctx := context.Background()

	// ReportStatus with nil status should not panic or error
	err := reporter.ReportStatus(ctx, nil)
	assert.NoError(t, err)
}

func TestNoOpReporter_ReportStatus_CancelledContext(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	status := &vmcp.Status{
		Phase:   vmcp.PhaseReady,
		Message: "Test",
	}

	// ReportStatus should return nil even with cancelled context
	err := reporter.ReportStatus(ctx, status)
	assert.NoError(t, err)
}

func TestNoOpReporter_Start(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	ctx := context.Background()

	// Start should return a shutdown function without error
	shutdown, err := reporter.Start(ctx)
	assert.NoError(t, err)
	require.NotNil(t, shutdown)

	// Shutdown should return nil without error
	err = shutdown(ctx)
	assert.NoError(t, err)
}

func TestNoOpReporter_Start_CancelledContext(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Start should return shutdown function even with cancelled context
	shutdown, err := reporter.Start(ctx)
	assert.NoError(t, err)
	require.NotNil(t, shutdown)

	// Shutdown should work
	err = shutdown(ctx)
	assert.NoError(t, err)
}

func TestNoOpReporter_Shutdown_Multiple(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	ctx := context.Background()

	shutdown, err := reporter.Start(ctx)
	require.NoError(t, err)
	require.NotNil(t, shutdown)

	// Multiple shutdown calls should not panic or error
	assert.NotPanics(t, func() {
		err := shutdown(ctx)
		assert.NoError(t, err)
		err = shutdown(ctx)
		assert.NoError(t, err)
		err = shutdown(ctx)
		assert.NoError(t, err)
	})
}

func TestNoOpReporter_FullLifecycle(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	ctx := context.Background()

	// Start
	shutdown, err := reporter.Start(ctx)
	assert.NoError(t, err)
	require.NotNil(t, shutdown)

	// ReportStatus a few times
	for range 3 {
		status := &vmcp.Status{
			Phase:     vmcp.PhaseReady,
			Message:   "Operational",
			Timestamp: time.Now(),
		}
		err := reporter.ReportStatus(ctx, status)
		assert.NoError(t, err)
		time.Sleep(50 * time.Millisecond)
	}

	// Shutdown
	err = shutdown(ctx)
	assert.NoError(t, err)
}

func TestNoOpReporter_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	ctx := context.Background()

	// Concurrent Start calls
	for range 10 {
		go func() {
			shutdown, _ := reporter.Start(ctx)
			if shutdown != nil {
				_ = shutdown(ctx)
			}
		}()
	}

	// Concurrent ReportStatus calls
	for range 10 {
		go func() {
			status := &vmcp.Status{
				Phase:   vmcp.PhaseReady,
				Message: "Concurrent test",
			}
			_ = reporter.ReportStatus(ctx, status)
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

	now := metav1.Now()
	// Create a complex status with multiple conditions and backends
	status := &vmcp.Status{
		Phase:   vmcp.PhaseDegraded,
		Message: "Some backends are unhealthy",
		Conditions: []vmcp.Condition{
			{
				Type:               vmcp.ConditionTypeReady,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             vmcp.ReasonServerReady,
				Message:            "Server is accepting requests",
			},
			{
				Type:               vmcp.ConditionTypeBackendsDiscovered,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: now,
				Reason:             vmcp.ReasonBackendDiscoverySucceeded,
				Message:            "All backends discovered",
			},
		},
		DiscoveredBackends: []vmcp.DiscoveredBackend{
			{
				Name:   "healthy-backend",
				URL:    "http://backend1:8080",
				Status: "ready",
			},
			{
				Name:   "unhealthy-backend",
				URL:    "http://backend2:8080",
				Status: "unavailable",
			},
			{
				Name:   "degraded-backend",
				URL:    "http://backend3:8080",
				Status: "degraded",
			},
		},
		Timestamp: time.Now(),
	}

	// ReportStatus should handle complex status without error
	err := reporter.ReportStatus(ctx, status)
	assert.NoError(t, err)
}
