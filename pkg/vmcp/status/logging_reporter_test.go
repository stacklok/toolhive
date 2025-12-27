package status

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoggingReporter_ReportStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		logUpdates bool
		status     *Status
	}{
		{
			name:       "report status with logging enabled",
			logUpdates: true,
			status: &Status{
				Phase:   PhaseReady,
				Message: "Server is ready",
				DiscoveredBackends: []DiscoveredBackend{
					{
						Name:   "backend1",
						URL:    "http://backend1:8080",
						Status: BackendStatusReady,
					},
				},
				Timestamp: time.Now(),
			},
		},
		{
			name:       "report status with logging disabled",
			logUpdates: false,
			status: &Status{
				Phase:   PhaseDegraded,
				Message: "Some backends unavailable",
				DiscoveredBackends: []DiscoveredBackend{
					{
						Name:   "backend1",
						URL:    "http://backend1:8080",
						Status: BackendStatusReady,
					},
					{
						Name:   "backend2",
						URL:    "http://backend2:8080",
						Status: BackendStatusUnavailable,
					},
				},
				Timestamp: time.Now(),
			},
		},
		{
			name:       "report status with no backends",
			logUpdates: true,
			status: &Status{
				Phase:              PhasePending,
				Message:            "Initializing",
				DiscoveredBackends: []DiscoveredBackend{},
				Timestamp:          time.Now(),
			},
		},
		{
			name:       "report failed status",
			logUpdates: true,
			status: &Status{
				Phase:              PhaseFailed,
				Message:            "Server failed to start",
				DiscoveredBackends: []DiscoveredBackend{},
				Timestamp:          time.Now(),
			},
		},
		{
			name:       "report status with conditions",
			logUpdates: false,
			status: &Status{
				Phase:   PhaseReady,
				Message: "Server ready with 2 backends",
				Conditions: []Condition{
					{
						Type:               ConditionTypeReady,
						Status:             "True",
						Reason:             ReasonServerReady,
						Message:            "Server is ready",
						LastTransitionTime: time.Now(),
					},
					{
						Type:               ConditionTypeBackendsDiscovered,
						Status:             "True",
						Reason:             ReasonBackendDiscoverySucceeded,
						Message:            "Discovered 2 backends",
						LastTransitionTime: time.Now(),
					},
				},
				DiscoveredBackends: []DiscoveredBackend{
					{
						Name:            "backend1",
						URL:             "http://backend1:8080",
						Status:          BackendStatusReady,
						AuthConfigRef:   "auth-config-1",
						AuthType:        "oauth2",
						LastHealthCheck: time.Now(),
					},
					{
						Name:            "backend2",
						URL:             "http://backend2:8080",
						Status:          BackendStatusReady,
						AuthConfigRef:   "auth-config-2",
						AuthType:        "header_injection",
						LastHealthCheck: time.Now(),
					},
				},
				ObservedGeneration: 1,
				Timestamp:          time.Now(),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reporter := NewLoggingReporter(tt.logUpdates)
			ctx := context.Background()

			// Should not return error
			err := reporter.ReportStatus(ctx, tt.status)
			require.NoError(t, err)
		})
	}
}

func TestLoggingReporter_StartStop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		logUpdates bool
	}{
		{
			name:       "start and stop with logging enabled",
			logUpdates: true,
		},
		{
			name:       "start and stop with logging disabled",
			logUpdates: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reporter := NewLoggingReporter(tt.logUpdates)
			ctx := context.Background()

			// Start should not return error and should return a shutdown function
			shutdown, err := reporter.Start(ctx)
			require.NoError(t, err)
			require.NotNil(t, shutdown)

			// Shutdown function should not return error
			err = shutdown(ctx)
			require.NoError(t, err)
		})
	}
}

func TestLoggingReporter_ConcurrentReportStatus(t *testing.T) {
	t.Parallel()

	reporter := NewLoggingReporter(false) // Disable logging to avoid race with logger
	ctx := context.Background()

	// Create test status
	status := &Status{
		Phase:   PhaseReady,
		Message: "Concurrent test",
		DiscoveredBackends: []DiscoveredBackend{
			{
				Name:   "backend1",
				URL:    "http://backend1:8080",
				Status: BackendStatusReady,
			},
		},
		Timestamp: time.Now(),
	}

	// Run concurrent reports
	var wg sync.WaitGroup
	numGoroutines := 10
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			err := reporter.ReportStatus(ctx, status)
			require.NoError(t, err)
		}()
	}

	wg.Wait()
}

func TestLoggingReporter_ShutdownIdempotency(t *testing.T) {
	t.Parallel()

	reporter := NewLoggingReporter(false)
	ctx := context.Background()

	// Start reporter
	shutdown, err := reporter.Start(ctx)
	require.NoError(t, err)
	require.NotNil(t, shutdown)

	// Call shutdown multiple times - should be safe (idempotent)
	for i := 0; i < 3; i++ {
		err = shutdown(ctx)
		require.NoError(t, err)
	}
}

func TestLoggingReporter_NilStatus(t *testing.T) {
	t.Parallel()

	reporter := NewLoggingReporter(true)
	ctx := context.Background()

	// Even with nil status, should not panic or error
	err := reporter.ReportStatus(ctx, nil)
	require.NoError(t, err)
}

func TestNewLoggingReporter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		logUpdates bool
	}{
		{
			name:       "create with logging enabled",
			logUpdates: true,
		},
		{
			name:       "create with logging disabled",
			logUpdates: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reporter := NewLoggingReporter(tt.logUpdates)
			require.NotNil(t, reporter)
			require.Equal(t, tt.logUpdates, reporter.logUpdates)
		})
	}
}
