package status

import (
	"testing"
	"time"
)

// TestPhaseConstants verifies phase constants are defined correctly
func TestPhaseConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		phase Phase
		value string
	}{
		{PhaseReady, "Ready"},
		{PhaseDegraded, "Degraded"},
		{PhaseFailed, "Failed"},
		{PhasePending, "Pending"},
	}

	for _, tt := range tests {
		t.Run(string(tt.phase), func(t *testing.T) {
			t.Parallel()
			if string(tt.phase) != tt.value {
				t.Errorf("Phase %s has wrong value: got %s, want %s",
					tt.phase, string(tt.phase), tt.value)
			}
		})
	}
}

// TestBackendHealthReport_Creation verifies struct initialization
func TestBackendHealthReport_Creation(t *testing.T) {
	t.Parallel()

	now := time.Now()

	report := BackendHealthReport{
		Name:        "test-backend",
		Healthy:     true,
		Message:     "All good",
		LastChecked: now,
	}

	if report.Name != "test-backend" {
		t.Errorf("Name: got %s, want test-backend", report.Name)
	}
	if !report.Healthy {
		t.Error("Healthy should be true")
	}
	if report.Message != "All good" {
		t.Errorf("Message: got %s, want 'All good'", report.Message)
	}
	if report.LastChecked != now {
		t.Error("LastChecked timestamp mismatch")
	}
}

// TestRuntimeStatus_Creation verifies struct initialization
func TestRuntimeStatus_Creation(t *testing.T) {
	t.Parallel()

	backends := []BackendHealthReport{
		{Name: "b1", Healthy: true},
		{Name: "b2", Healthy: false},
	}

	status := RuntimeStatus{
		Phase:             PhaseReady,
		Message:           "Test",
		Backends:          backends,
		TotalToolCount:    100,
		HealthyBackends:   1,
		UnhealthyBackends: 1,
		LastDiscoveryTime: time.Now(),
	}

	if status.Phase != PhaseReady {
		t.Errorf("Phase: got %s, want Ready", status.Phase)
	}
	if len(status.Backends) != 2 {
		t.Errorf("Backends count: got %d, want 2", len(status.Backends))
	}
	if status.TotalToolCount != 100 {
		t.Errorf("TotalToolCount: got %d, want 100", status.TotalToolCount)
	}
	if status.HealthyBackends != 1 {
		t.Errorf("HealthyBackends: got %d, want 1", status.HealthyBackends)
	}
	if status.UnhealthyBackends != 1 {
		t.Errorf("UnhealthyBackends: got %d, want 1", status.UnhealthyBackends)
	}
}

// TestRuntimeStatus_EmptyBackends verifies handling of empty backend list
func TestRuntimeStatus_EmptyBackends(t *testing.T) {
	t.Parallel()

	status := RuntimeStatus{
		Phase:             PhasePending,
		Backends:          []BackendHealthReport{},
		TotalToolCount:    0,
		HealthyBackends:   0,
		UnhealthyBackends: 0,
	}

	if status.Backends == nil {
		t.Error("Backends should not be nil")
	}
	if len(status.Backends) != 0 {
		t.Errorf("Backends should be empty, got %d items", len(status.Backends))
	}
}
