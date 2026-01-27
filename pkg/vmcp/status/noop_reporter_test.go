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

	vmcp "github.com/stacklok/toolhive/pkg/vmcp"
)

func TestNewNoOpReporter(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	require.NotNil(t, reporter)
}

func TestNoOpReporter_ReportStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status *vmcp.Status
	}{
		{
			name: "valid status",
			status: &vmcp.Status{
				Phase:     vmcp.PhaseReady,
				Message:   "Test message",
				Timestamp: time.Now(),
				Conditions: []metav1.Condition{
					{
						Type:               "Ready",
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             "AllHealthy",
						Message:            "All backends healthy",
					},
				},
				DiscoveredBackends: []vmcp.DiscoveredBackend{
					{
						Name:            "backend-1",
						Status:          "ready",
						URL:             "http://backend-1:8080",
						LastHealthCheck: metav1.Now(),
					},
				},
			},
		},
		{
			name:   "nil status",
			status: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reporter := NewNoOpReporter()
			ctx := context.Background()

			err := reporter.ReportStatus(ctx, tt.status)
			assert.NoError(t, err)
		})
	}
}

func TestNoOpReporter_Start(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	ctx := context.Background()

	shutdown, err := reporter.Start(ctx)
	require.NoError(t, err)
	require.NotNil(t, shutdown)

	// Shutdown should be idempotent
	err = shutdown(ctx)
	assert.NoError(t, err)

	err = shutdown(ctx)
	assert.NoError(t, err)
}

func TestNoOpReporter_FullLifecycle(t *testing.T) {
	t.Parallel()

	reporter := NewNoOpReporter()
	ctx := context.Background()

	// Start reporter
	shutdown, err := reporter.Start(ctx)
	require.NoError(t, err)
	require.NotNil(t, shutdown)

	// Report status multiple times
	for i := 0; i < 5; i++ {
		status := &vmcp.Status{
			Phase:     vmcp.PhaseReady,
			Message:   "Operational",
			Timestamp: time.Now(),
		}
		err := reporter.ReportStatus(ctx, status)
		assert.NoError(t, err)
	}

	// Shutdown
	err = shutdown(ctx)
	assert.NoError(t, err)
}

func TestNoOpReporter_ImplementsInterface(t *testing.T) {
	t.Parallel()

	var _ Reporter = (*NoOpReporter)(nil)
}
