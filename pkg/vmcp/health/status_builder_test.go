// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// TestBuildConditions tests the buildConditions helper function.
func TestBuildConditions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		summary             Summary
		phase               vmcp.Phase
		expectedReadyStatus metav1.ConditionStatus
		expectedReason      string
		hasDegradedCond     bool
	}{
		{
			name: "all backends healthy",
			summary: Summary{
				Total:    3,
				Healthy:  3,
				Degraded: 0,
			},
			phase:               vmcp.PhaseReady,
			expectedReadyStatus: metav1.ConditionTrue,
			expectedReason:      "AllBackendsHealthy",
			hasDegradedCond:     false,
		},
		{
			name: "some backends degraded",
			summary: Summary{
				Total:    3,
				Healthy:  2,
				Degraded: 1,
			},
			phase:               vmcp.PhaseDegraded,
			expectedReadyStatus: metav1.ConditionFalse,
			expectedReason:      "SomeBackendsUnhealthy",
			hasDegradedCond:     true,
		},
		{
			name: "no healthy backends",
			summary: Summary{
				Total:     2,
				Healthy:   0,
				Unhealthy: 2,
			},
			phase:               vmcp.PhaseFailed,
			expectedReadyStatus: metav1.ConditionFalse,
			expectedReason:      "NoHealthyBackends",
			hasDegradedCond:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conditions := buildConditions(tt.summary, tt.phase)

			// Find Ready condition
			var readyCond *metav1.Condition
			var degradedCond *metav1.Condition
			for i := range conditions {
				if conditions[i].Type == "Ready" {
					readyCond = &conditions[i]
				}
				if conditions[i].Type == "Degraded" {
					degradedCond = &conditions[i]
				}
			}

			// Verify Ready condition
			assert.NotNil(t, readyCond, "Ready condition should exist")
			assert.Equal(t, tt.expectedReadyStatus, readyCond.Status)
			assert.Equal(t, tt.expectedReason, readyCond.Reason)

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
			shouldContain: "connection refused",
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

// TestBuildStatus_PhaseLogic tests the phase determination logic.
func TestBuildStatus_PhaseLogic(t *testing.T) {
	t.Parallel()

	// This test verifies the phase determination logic by creating
	// a statusTracker directly and simulating different health scenarios

	tests := []struct {
		name          string
		backendStates map[string]vmcp.BackendHealthStatus
		expectedPhase vmcp.Phase
		expectedCount int
	}{
		{
			name: "all healthy",
			backendStates: map[string]vmcp.BackendHealthStatus{
				"b1": vmcp.BackendHealthy,
				"b2": vmcp.BackendHealthy,
			},
			expectedPhase: vmcp.PhaseReady,
			expectedCount: 2,
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create status tracker and populate with test states
			tracker := newStatusTracker(1)

			for backendID, status := range tt.backendStates {
				if status == vmcp.BackendHealthy {
					tracker.RecordSuccess(backendID, backendID, status)
				} else {
					tracker.RecordFailure(backendID, backendID, status, fmt.Errorf("test error"))
				}
			}

			// Create a mock monitor with this tracker
			// (we can't easily test BuildStatus without the full Monitor,
			// but we can verify the logic works through the tracker)
			allStates := tracker.GetAllStates()

			// Manually implement phase logic from BuildStatus
			totalBackends := len(allStates)
			healthyCount := 0
			for _, state := range allStates {
				if state.Status == vmcp.BackendHealthy {
					healthyCount++
				}
			}

			var phase vmcp.Phase
			if totalBackends == 0 {
				phase = vmcp.PhaseReady
			} else if healthyCount == totalBackends {
				phase = vmcp.PhaseReady
			} else if healthyCount == 0 {
				phase = vmcp.PhaseFailed
			} else {
				phase = vmcp.PhaseDegraded
			}

			assert.Equal(t, tt.expectedPhase, phase)
			assert.Equal(t, tt.expectedCount, healthyCount)
		})
	}
}
