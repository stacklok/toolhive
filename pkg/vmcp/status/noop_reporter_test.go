package status

import (
	"context"
	"testing"
	"time"
)

func TestNoOpReporter_ReportStatus(t *testing.T) {
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reporter := NewNoOpReporter(tt.logUpdates)
			ctx := context.Background()

			// Should not return error
			err := reporter.ReportStatus(ctx, tt.status)
			if err != nil {
				t.Errorf("NoOpReporter.ReportStatus() unexpected error = %v", err)
			}
		})
	}
}

func TestNoOpReporter_StartStop(t *testing.T) {
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
			reporter := NewNoOpReporter(tt.logUpdates)
			ctx := context.Background()

			// Start should not return error
			err := reporter.Start(ctx)
			if err != nil {
				t.Errorf("NoOpReporter.Start() unexpected error = %v", err)
			}

			// Stop should not return error
			err = reporter.Stop(ctx)
			if err != nil {
				t.Errorf("NoOpReporter.Stop() unexpected error = %v", err)
			}
		})
	}
}

func TestNoOpReporter_ImplementsInterface(_ *testing.T) {
	var _ Reporter = (*NoOpReporter)(nil)
}
