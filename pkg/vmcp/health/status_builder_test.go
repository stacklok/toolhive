// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stacklok/toolhive/pkg/vmcp"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

const (
	conditionTypeReady    = "Ready"
	conditionTypeDegraded = "Degraded"
)

// TestBuildConditions tests the buildConditions helper function.
func TestBuildConditions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		summary                Summary
		phase                  vmcp.Phase
		configuredBackendCount int
		expectedReadyStatus    metav1.ConditionStatus
		expectedReason         string
		expectedMessage        string
		hasDegradedCond        bool
	}{
		{
			name: "all backends healthy",
			summary: Summary{
				Total:    3,
				Healthy:  3,
				Degraded: 0,
			},
			phase:                  vmcp.PhaseReady,
			configuredBackendCount: 3,
			expectedReadyStatus:    metav1.ConditionTrue,
			expectedReason:         "AllBackendsHealthy",
			expectedMessage:        "All 3 backends are healthy",
			hasDegradedCond:        false,
		},
		{
			name: "empty backends (cold start)",
			summary: Summary{
				Total:   0,
				Healthy: 0,
			},
			phase:                  vmcp.PhaseReady,
			configuredBackendCount: 0,
			expectedReadyStatus:    metav1.ConditionTrue,
			expectedReason:         "AllBackendsHealthy",
			expectedMessage:        "Ready, no backends configured",
			hasDegradedCond:        false,
		},
		{
			name: "some backends degraded",
			summary: Summary{
				Total:    3,
				Healthy:  2,
				Degraded: 1,
			},
			phase:                  vmcp.PhaseDegraded,
			configuredBackendCount: 3,
			expectedReadyStatus:    metav1.ConditionFalse,
			expectedReason:         "SomeBackendsUnhealthy",
			hasDegradedCond:        true,
		},
		{
			name: "no healthy backends",
			summary: Summary{
				Total:     2,
				Healthy:   0,
				Unhealthy: 2,
			},
			phase:                  vmcp.PhaseFailed,
			configuredBackendCount: 2,
			expectedReadyStatus:    metav1.ConditionFalse,
			expectedReason:         "NoHealthyBackends",
			hasDegradedCond:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conditions := buildConditions(tt.summary, tt.phase, tt.configuredBackendCount)

			// Find Ready condition
			var readyCond *metav1.Condition
			var degradedCond *metav1.Condition
			for i := range conditions {
				if conditions[i].Type == conditionTypeReady {
					readyCond = &conditions[i]
				}
				if conditions[i].Type == conditionTypeDegraded {
					degradedCond = &conditions[i]
				}
			}

			// Verify Ready condition
			assert.NotNil(t, readyCond, "Ready condition should exist")
			assert.Equal(t, tt.expectedReadyStatus, readyCond.Status)
			assert.Equal(t, tt.expectedReason, readyCond.Reason)
			if tt.expectedMessage != "" {
				assert.Equal(t, tt.expectedMessage, readyCond.Message)
			}

			// Verify Degraded condition
			if tt.hasDegradedCond {
				assert.NotNil(t, degradedCond, "Degraded condition should exist")
				assert.Equal(t, metav1.ConditionTrue, degradedCond.Status)
			} else {
				assert.Nil(t, degradedCond, "Degraded condition should not exist")
			}
		})
	}
}

// TestFormatBackendMessage tests the formatBackendMessage helper function.
func TestFormatBackendMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		state         *State
		expectedMsg   string
		shouldContain string // substring check
	}{
		{
			name: "healthy backend",
			state: &State{
				Status:              vmcp.BackendHealthy,
				ConsecutiveFailures: 0,
				LastError:           nil,
			},
			expectedMsg: "Healthy",
		},
		{
			name: "degraded with failures",
			state: &State{
				Status:              vmcp.BackendDegraded,
				ConsecutiveFailures: 2,
				LastError:           nil,
			},
			shouldContain: "Recovering from 2 failures",
		},
		{
			name: "degraded without failures",
			state: &State{
				Status:              vmcp.BackendDegraded,
				ConsecutiveFailures: 0,
				LastError:           nil,
			},
			expectedMsg: "Degraded performance",
		},
		{
			name: "unhealthy with error",
			state: &State{
				Status:              vmcp.BackendUnhealthy,
				ConsecutiveFailures: 3,
				LastError:           fmt.Errorf("connection refused"),
			},
			shouldContain: "Connection failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := formatBackendMessage(tt.state)

			if tt.expectedMsg != "" {
				assert.Equal(t, tt.expectedMsg, result)
			}
			if tt.shouldContain != "" {
				assert.Contains(t, result, tt.shouldContain)
			}
		})
	}
}

// TestSummary_Aggregation tests that Summary correctly aggregates backend counts.
func TestSummary_Aggregation(t *testing.T) {
	t.Parallel()

	summary := Summary{
		Total:           10,
		Healthy:         5,
		Degraded:        2,
		Unhealthy:       1,
		Unknown:         1,
		Unauthenticated: 1,
	}

	// Verify string representation
	str := summary.String()
	assert.Contains(t, str, "total=10")
	assert.Contains(t, str, "healthy=5")
	assert.Contains(t, str, "degraded=2")
	assert.Contains(t, str, "unhealthy=1")
	assert.Contains(t, str, "unknown=1")
	assert.Contains(t, str, "unauthenticated=1")
}

// TestComputeSummary tests that computeSummary correctly aggregates states.
func TestComputeSummary(t *testing.T) {
	t.Parallel()

	states := map[string]*State{
		"b1": {Status: vmcp.BackendHealthy},
		"b2": {Status: vmcp.BackendHealthy},
		"b3": {Status: vmcp.BackendDegraded},
		"b4": {Status: vmcp.BackendUnhealthy},
		"b5": {Status: vmcp.BackendUnknown},
		"b6": {Status: vmcp.BackendUnauthenticated},
	}

	summary := computeSummary(states)

	assert.Equal(t, 6, summary.Total)
	assert.Equal(t, 2, summary.Healthy)
	assert.Equal(t, 1, summary.Degraded)
	assert.Equal(t, 1, summary.Unhealthy)
	assert.Equal(t, 1, summary.Unknown)
	assert.Equal(t, 1, summary.Unauthenticated)
}

// TestComputeSummary_EmptyStates tests computeSummary with no states.
func TestComputeSummary_EmptyStates(t *testing.T) {
	t.Parallel()

	states := map[string]*State{}
	summary := computeSummary(states)

	assert.Equal(t, 0, summary.Total)
	assert.Equal(t, 0, summary.Healthy)
	assert.Equal(t, 0, summary.Degraded)
	assert.Equal(t, 0, summary.Unhealthy)
	assert.Equal(t, 0, summary.Unknown)
	assert.Equal(t, 0, summary.Unauthenticated)
}

// TestExtractAuthInfo tests the extractAuthInfo helper function.
func TestExtractAuthInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		backend               vmcp.Backend
		expectedAuthConfigRef string
		expectedAuthType      string
	}{
		{
			name: "backend with auth config and ref",
			backend: vmcp.Backend{
				Name:          "backend1",
				AuthConfigRef: "my-external-auth-config",
				AuthConfig: &authtypes.BackendAuthStrategy{
					Type: "bearer",
				},
			},
			expectedAuthConfigRef: "my-external-auth-config",
			expectedAuthType:      "bearer",
		},
		{
			name: "backend with auth config but no ref",
			backend: vmcp.Backend{
				Name:          "backend2",
				AuthConfigRef: "",
				AuthConfig: &authtypes.BackendAuthStrategy{
					Type: "api-key",
				},
			},
			expectedAuthConfigRef: "",
			expectedAuthType:      "api-key",
		},
		{
			name: "backend with no auth config",
			backend: vmcp.Backend{
				Name:       "backend3",
				AuthConfig: nil,
			},
			expectedAuthConfigRef: "",
			expectedAuthType:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			authConfigRef, authType := extractAuthInfo(tt.backend)

			assert.Equal(t, tt.expectedAuthConfigRef, authConfigRef,
				"AuthConfigRef should match expected")
			assert.Equal(t, tt.expectedAuthType, authType,
				"AuthType should match expected")
		})
	}
}

// TestBuildStatus_PhaseLogic tests the phase determination logic by calling Monitor.BuildStatus().
func TestBuildStatus_PhaseLogic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		backendStates   map[string]vmcp.BackendHealthStatus
		expectedPhase   vmcp.Phase
		expectedCount   int
		expectedMessage string
	}{
		{
			name: "all healthy",
			backendStates: map[string]vmcp.BackendHealthStatus{
				"b1": vmcp.BackendHealthy,
				"b2": vmcp.BackendHealthy,
			},
			expectedPhase:   vmcp.PhaseReady,
			expectedCount:   2,
			expectedMessage: "All 2 backends healthy",
		},
		{
			name: "mixed health",
			backendStates: map[string]vmcp.BackendHealthStatus{
				"b1": vmcp.BackendHealthy,
				"b2": vmcp.BackendDegraded,
			},
			expectedPhase: vmcp.PhaseDegraded,
			expectedCount: 1,
		},
		{
			name: "no healthy backends",
			backendStates: map[string]vmcp.BackendHealthStatus{
				"b1": vmcp.BackendUnhealthy,
				"b2": vmcp.BackendUnhealthy,
			},
			expectedPhase: vmcp.PhaseFailed,
			expectedCount: 0,
		},
		{
			name:            "no backends configured (cold start)",
			backendStates:   map[string]vmcp.BackendHealthStatus{},
			expectedPhase:   vmcp.PhaseReady,
			expectedCount:   0,
			expectedMessage: "Ready, no backends configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create status tracker and populate with test states
			tracker := newStatusTracker(1, nil)
			var backends []vmcp.Backend

			for backendID, status := range tt.backendStates {
				backends = append(backends, vmcp.Backend{ID: backendID, Name: backendID})
				if status == vmcp.BackendHealthy {
					tracker.RecordSuccess(backendID, backendID, status)
				} else {
					tracker.RecordFailure(backendID, backendID, status, fmt.Errorf("test error"))
				}
			}

			// Create minimal Monitor with the populated tracker
			monitor := &Monitor{
				statusTracker: tracker,
				backends:      backends,
			}

			// Call the actual BuildStatus method
			status := monitor.BuildStatus()

			// Verify the returned status
			assert.NotNil(t, status, "BuildStatus should return non-nil status")
			assert.Equal(t, tt.expectedPhase, status.Phase, "Phase should match expected")
			assert.Equal(t, tt.expectedCount, status.BackendCount, "BackendCount should match healthy count")
			assert.NotEmpty(t, status.Message, "Message should not be empty")
			if tt.expectedMessage != "" {
				assert.Equal(t, tt.expectedMessage, status.Message, "Message should match expected")
			}
			assert.NotNil(t, status.Conditions, "Conditions should not be nil")

			// Verify Ready condition exists
			var readyCond *metav1.Condition
			for i := range status.Conditions {
				if status.Conditions[i].Type == "Ready" {
					readyCond = &status.Conditions[i]
					break
				}
			}
			assert.NotNil(t, readyCond, "Ready condition should exist")
		})
	}
}

// TestBuildStatus_PendingPhase tests the Pending phase when backends are configured
// but no health checks have completed yet (startup scenario).
func TestBuildStatus_PendingPhase(t *testing.T) {
	t.Parallel()

	// Create tracker with no health data (simulating startup before first check)
	tracker := newStatusTracker(1, nil)

	// Configure 2 backends but don't record any health data
	backends := []vmcp.Backend{
		{ID: "backend1", Name: "backend1"},
		{ID: "backend2", Name: "backend2"},
	}

	monitor := &Monitor{
		statusTracker: tracker,
		backends:      backends,
	}

	// Call BuildStatus
	status := monitor.BuildStatus()

	// Verify Pending phase
	assert.Equal(t, vmcp.PhasePending, status.Phase,
		"Phase should be Pending when backends configured but no health data")
	assert.Equal(t, "Waiting for initial health checks (2 backends configured)", status.Message,
		"Message should indicate waiting for health checks")
	assert.Equal(t, 0, status.BackendCount,
		"BackendCount should be 0 when no health checks completed")
	assert.Empty(t, status.DiscoveredBackends,
		"DiscoveredBackends should be empty when no health checks completed")

	// Verify Ready condition
	var readyCond *metav1.Condition
	for i := range status.Conditions {
		if status.Conditions[i].Type == "Ready" {
			readyCond = &status.Conditions[i]
			break
		}
	}
	require.NotNil(t, readyCond, "Ready condition should exist")
	assert.Equal(t, metav1.ConditionFalse, readyCond.Status,
		"Ready should be False when in Pending phase")
	assert.Equal(t, "BackendsPending", readyCond.Reason)
}
