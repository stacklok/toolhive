// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

func TestNewStatusTracker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		threshold         int
		expectedThreshold int
		description       string
	}{
		{
			name:              "valid threshold",
			threshold:         3,
			expectedThreshold: 3,
			description:       "should use provided threshold",
		},
		{
			name:              "threshold of 1",
			threshold:         1,
			expectedThreshold: 1,
			description:       "should allow threshold of 1",
		},
		{
			name:              "invalid threshold (0)",
			threshold:         0,
			expectedThreshold: 1,
			description:       "should adjust invalid threshold to 1",
		},
		{
			name:              "invalid threshold (-1)",
			threshold:         -1,
			expectedThreshold: 1,
			description:       "should adjust negative threshold to 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tracker := newStatusTracker(tt.threshold, nil)
			require.NotNil(t, tracker)
			assert.Equal(t, tt.expectedThreshold, tracker.unhealthyThreshold, tt.description)
			assert.NotNil(t, tracker.states)
		})
	}
}

func TestStatusTracker_RecordSuccess(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(3, nil)

	// Record success for new backend
	tracker.RecordSuccess("backend-1", "Backend 1", vmcp.BackendHealthy)

	status, exists := tracker.GetStatus("backend-1")
	assert.True(t, exists)
	assert.Equal(t, vmcp.BackendHealthy, status)

	state, exists := tracker.GetState("backend-1")
	assert.True(t, exists)
	assert.Equal(t, vmcp.BackendHealthy, state.Status)
	assert.Equal(t, 0, state.ConsecutiveFailures)
	assert.Nil(t, state.LastError)
	assert.False(t, state.LastCheckTime.IsZero())
	assert.False(t, state.LastTransitionTime.IsZero())
}

func TestStatusTracker_RecordSuccess_AfterFailures(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(3, nil)
	testErr := errors.New("health check failed")

	// Record multiple failures
	for i := 0; i < 5; i++ {
		tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, testErr)
	}

	state, _ := tracker.GetState("backend-1")
	assert.Equal(t, vmcp.BackendUnhealthy, state.Status)
	assert.Equal(t, 5, state.ConsecutiveFailures)

	// Record success - should mark as degraded due to recovering from failures
	tracker.RecordSuccess("backend-1", "Backend 1", vmcp.BackendHealthy)

	state, _ = tracker.GetState("backend-1")
	assert.Equal(t, vmcp.BackendDegraded, state.Status) // Degraded because recovering from failures
	assert.Equal(t, 0, state.ConsecutiveFailures)
	assert.Nil(t, state.LastError)
}

func TestStatusTracker_RecordFailure_BelowThreshold(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(3, nil)
	testErr := errors.New("health check failed")

	// First failure - should initialize with unknown status (below threshold)
	tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, testErr)

	state, exists := tracker.GetState("backend-1")
	assert.True(t, exists)
	assert.Equal(t, vmcp.BackendUnknown, state.Status)
	assert.Equal(t, 1, state.ConsecutiveFailures)
	assert.NotNil(t, state.LastError)

	// Second failure - still below threshold, status remains unknown
	tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, testErr)
	state, _ = tracker.GetState("backend-1")
	assert.Equal(t, vmcp.BackendUnknown, state.Status)
	assert.Equal(t, 2, state.ConsecutiveFailures)
}

func TestStatusTracker_RecordFailure_ReachThreshold(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(3, nil)
	testErr := errors.New("health check failed")

	// Record failures up to threshold
	for i := 0; i < 3; i++ {
		tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, testErr)
	}

	state, _ := tracker.GetState("backend-1")
	assert.Equal(t, vmcp.BackendUnhealthy, state.Status)
	assert.Equal(t, 3, state.ConsecutiveFailures)
	assert.NotNil(t, state.LastError)
	assert.False(t, state.LastTransitionTime.IsZero())
}

func TestStatusTracker_RecordFailure_StatusTransitions(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(2, nil)

	// Start with healthy
	tracker.RecordSuccess("backend-1", "Backend 1", vmcp.BackendHealthy)
	status, _ := tracker.GetStatus("backend-1")
	assert.Equal(t, vmcp.BackendHealthy, status)

	// First failure - still healthy
	tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, errors.New("error 1"))
	status, _ = tracker.GetStatus("backend-1")
	assert.Equal(t, vmcp.BackendHealthy, status)

	// Second failure - should transition to unhealthy
	tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, errors.New("error 2"))
	status, _ = tracker.GetStatus("backend-1")
	assert.Equal(t, vmcp.BackendUnhealthy, status)

	// Transition to unauthenticated
	tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnauthenticated, errors.New("auth error"))
	tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnauthenticated, errors.New("auth error"))
	status, _ = tracker.GetStatus("backend-1")
	assert.Equal(t, vmcp.BackendUnauthenticated, status)
}

func TestStatusTracker_RecordFailure_DifferentStatusTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		failureStatus  vmcp.BackendHealthStatus
		expectedStatus vmcp.BackendHealthStatus
	}{
		{
			name:           "unhealthy failures",
			failureStatus:  vmcp.BackendUnhealthy,
			expectedStatus: vmcp.BackendUnhealthy,
		},
		{
			name:           "unauthenticated failures",
			failureStatus:  vmcp.BackendUnauthenticated,
			expectedStatus: vmcp.BackendUnauthenticated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tracker := newStatusTracker(2, nil)
			testErr := errors.New("test error")

			// Record failures to reach threshold
			for i := 0; i < 2; i++ {
				tracker.RecordFailure("backend-1", "Backend 1", tt.failureStatus, testErr)
			}

			status, _ := tracker.GetStatus("backend-1")
			assert.Equal(t, tt.expectedStatus, status)
		})
	}
}

func TestStatusTracker_GetStatus_NonExistent(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(3, nil)

	status, exists := tracker.GetStatus("nonexistent")
	assert.False(t, exists)
	assert.Equal(t, vmcp.BackendUnknown, status)
}

func TestStatusTracker_GetState_NonExistent(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(3, nil)

	state, exists := tracker.GetState("nonexistent")
	assert.False(t, exists)
	assert.Nil(t, state)
}

func TestStatusTracker_GetAllStates(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(3, nil)

	// Add multiple backends with different states
	tracker.RecordSuccess("backend-1", "Backend 1", vmcp.BackendHealthy)

	// Record enough failures to reach threshold for backend-2
	for i := 0; i < 3; i++ {
		tracker.RecordFailure("backend-2", "Backend 2", vmcp.BackendUnhealthy, errors.New("failed"))
	}

	tracker.RecordSuccess("backend-3", "Backend 3", vmcp.BackendHealthy)

	allStates := tracker.GetAllStates()
	assert.Len(t, allStates, 3)

	assert.Equal(t, vmcp.BackendHealthy, allStates["backend-1"].Status)
	assert.Equal(t, vmcp.BackendUnhealthy, allStates["backend-2"].Status)
	assert.Equal(t, vmcp.BackendHealthy, allStates["backend-3"].Status)
}

func TestStatusTracker_GetAllStates_Empty(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(3, nil)

	allStates := tracker.GetAllStates()
	assert.NotNil(t, allStates)
	assert.Len(t, allStates, 0)
}

func TestStatusTracker_GetAllStates_Immutability(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(3, nil)
	tracker.RecordSuccess("backend-1", "Backend 1", vmcp.BackendHealthy)

	// Get states
	states1 := tracker.GetAllStates()
	states2 := tracker.GetAllStates()

	// Verify they are different copies
	assert.NotSame(t, states1["backend-1"], states2["backend-1"])

	// Modify one copy - should not affect the other
	states1["backend-1"].Status = vmcp.BackendUnhealthy
	assert.Equal(t, vmcp.BackendHealthy, states2["backend-1"].Status)
}

func TestStatusTracker_IsHealthy(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(3, nil)

	// Healthy backend
	tracker.RecordSuccess("backend-healthy", "Healthy Backend", vmcp.BackendHealthy)
	assert.True(t, tracker.IsHealthy("backend-healthy"))

	// Unhealthy backend
	tracker.RecordFailure("backend-unhealthy", "Unhealthy Backend",
		vmcp.BackendUnhealthy, errors.New("failed"))
	assert.False(t, tracker.IsHealthy("backend-unhealthy"))

	// Non-existent backend
	assert.False(t, tracker.IsHealthy("backend-nonexistent"))
}

func TestStatusTracker_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(3, nil)
	numGoroutines := 10
	numOperations := 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines * 3)

	// Concurrent RecordSuccess
	for i := 0; i < numGoroutines; i++ {
		go func(_ int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				tracker.RecordSuccess("backend-success", "Backend Success", vmcp.BackendHealthy)
			}
		}(i)
	}

	// Concurrent RecordFailure
	for i := 0; i < numGoroutines; i++ {
		go func(_ int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				tracker.RecordFailure("backend-failure", "Backend Failure",
					vmcp.BackendUnhealthy, errors.New("concurrent error"))
			}
		}(i)
	}

	// Concurrent reads
	for i := 0; i < numGoroutines; i++ {
		go func(_ int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				_, _ = tracker.GetStatus("backend-success")
				_, _ = tracker.GetState("backend-failure")
				_ = tracker.GetAllStates()
				_ = tracker.IsHealthy("backend-success")
			}
		}(i)
	}

	wg.Wait()

	// Verify states are consistent
	status1, exists1 := tracker.GetStatus("backend-success")
	assert.True(t, exists1)
	assert.Equal(t, vmcp.BackendHealthy, status1)

	status2, exists2 := tracker.GetStatus("backend-failure")
	assert.True(t, exists2)
	assert.Equal(t, vmcp.BackendUnhealthy, status2)
}

func TestStatusTracker_StateTimestamps(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(2, nil)
	testErr := errors.New("test error")

	// Initial success
	tracker.RecordSuccess("backend-1", "Backend 1", vmcp.BackendHealthy)
	state1, _ := tracker.GetState("backend-1")
	initialTransitionTime := state1.LastTransitionTime

	// Wait a bit to ensure time difference
	time.Sleep(10 * time.Millisecond)

	// Record failure (no status change yet, below threshold)
	tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, testErr)
	state2, _ := tracker.GetState("backend-1")

	// LastCheckTime should be updated
	assert.True(t, state2.LastCheckTime.After(state1.LastCheckTime))
	// LastTransitionTime should NOT change (no status transition)
	assert.Equal(t, initialTransitionTime, state2.LastTransitionTime)

	// Wait again
	time.Sleep(10 * time.Millisecond)

	// Second failure - should trigger transition
	tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, testErr)
	state3, _ := tracker.GetState("backend-1")

	// LastTransitionTime should be updated (status changed)
	assert.True(t, state3.LastTransitionTime.After(initialTransitionTime))
}

func TestStatusTracker_MultipleBackends(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(2, nil)

	// Backend 1: Healthy
	tracker.RecordSuccess("backend-1", "Backend 1", vmcp.BackendHealthy)

	// Backend 2: Unhealthy
	for i := 0; i < 2; i++ {
		tracker.RecordFailure("backend-2", "Backend 2", vmcp.BackendUnhealthy, errors.New("error"))
	}

	// Backend 3: Unauthenticated
	for i := 0; i < 2; i++ {
		tracker.RecordFailure("backend-3", "Backend 3", vmcp.BackendUnauthenticated, errors.New("auth error"))
	}

	// Verify each backend independently
	assert.True(t, tracker.IsHealthy("backend-1"))
	assert.False(t, tracker.IsHealthy("backend-2"))
	assert.False(t, tracker.IsHealthy("backend-3"))

	status2, _ := tracker.GetStatus("backend-2")
	assert.Equal(t, vmcp.BackendUnhealthy, status2)

	status3, _ := tracker.GetStatus("backend-3")
	assert.Equal(t, vmcp.BackendUnauthenticated, status3)
}

func TestStatusTracker_RecoveryAfterFailures(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(3, nil)
	testErr := errors.New("health check failed")

	// Record 5 failures (well over threshold)
	for i := 0; i < 5; i++ {
		tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, testErr)
	}

	state, _ := tracker.GetState("backend-1")
	assert.Equal(t, vmcp.BackendUnhealthy, state.Status)
	assert.Equal(t, 5, state.ConsecutiveFailures)
	beforeRecoveryTransitionTime := state.LastTransitionTime

	// Wait a bit
	time.Sleep(10 * time.Millisecond)

	// Single success should mark as degraded (recovering from failures)
	tracker.RecordSuccess("backend-1", "Backend 1", vmcp.BackendHealthy)

	state, _ = tracker.GetState("backend-1")
	assert.Equal(t, vmcp.BackendDegraded, state.Status) // Degraded because recovering from failures
	assert.Equal(t, 0, state.ConsecutiveFailures)
	assert.Nil(t, state.LastError)
	assert.True(t, state.LastTransitionTime.After(beforeRecoveryTransitionTime))
}

func TestState_Immutability(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(3, nil)
	testErr := errors.New("test error")

	tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, testErr)

	// Get state copy
	state, exists := tracker.GetState("backend-1")
	assert.True(t, exists)
	assert.NotNil(t, state)

	// Modify the returned state
	originalStatus := state.Status
	state.Status = vmcp.BackendHealthy
	state.ConsecutiveFailures = 0

	// Get state again - should be unchanged
	state2, _ := tracker.GetState("backend-1")
	assert.Equal(t, originalStatus, state2.Status)
	assert.NotEqual(t, 0, state2.ConsecutiveFailures)
}

func TestStatusTracker_ThresholdOf1(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(1, nil)
	testErr := errors.New("test error")

	// First failure should immediately mark as unhealthy
	tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, testErr)

	status, _ := tracker.GetStatus("backend-1")
	assert.Equal(t, vmcp.BackendUnhealthy, status)

	state, _ := tracker.GetState("backend-1")
	assert.Equal(t, 1, state.ConsecutiveFailures)
}

func TestStatusTracker_CircuitBreakerInitialization(t *testing.T) {
	t.Parallel()

	cbConfig := &CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 5,
		Timeout:          60 * time.Second,
	}
	tracker := newStatusTracker(3, cbConfig)

	// Circuit breaker is lazily initialized on first health check
	// Record a success to trigger initialization
	tracker.RecordSuccess("backend-1", "Backend 1", vmcp.BackendHealthy)

	// Verify circuit breaker exists and is in closed state
	cbState, exists := tracker.GetCircuitBreakerState("backend-1")
	assert.True(t, exists)
	assert.Equal(t, CircuitClosed, cbState)

	// Verify CanAttemptHealthCheck returns true initially
	assert.True(t, tracker.CanAttemptHealthCheck("backend-1"))
	assert.False(t, tracker.IsCircuitOpen("backend-1"))
}

func TestStatusTracker_CircuitBreakerRecordSuccess(t *testing.T) {
	t.Parallel()

	cbConfig := &CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 2,
		Timeout:          60 * time.Second,
	}
	tracker := newStatusTracker(3, cbConfig)

	// Record failure to increment circuit breaker count
	tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, errors.New("test"))

	cbState, _ := tracker.GetCircuitBreakerState("backend-1")
	assert.Equal(t, CircuitClosed, cbState)

	// Record success - should reset circuit breaker
	tracker.RecordSuccess("backend-1", "Backend 1", vmcp.BackendHealthy)

	state, _ := tracker.GetState("backend-1")
	assert.Equal(t, CircuitClosed, state.CircuitState)
	assert.Equal(t, 0, state.ConsecutiveFailures)
}

func TestStatusTracker_CircuitBreakerRecordFailure(t *testing.T) {
	t.Parallel()

	cbConfig := &CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 2,
		Timeout:          60 * time.Second,
	}
	tracker := newStatusTracker(3, cbConfig)

	testErr := errors.New("health check failed")

	// Record first failure - should stay closed
	tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, testErr)
	cbState, _ := tracker.GetCircuitBreakerState("backend-1")
	assert.Equal(t, CircuitClosed, cbState)
	assert.True(t, tracker.CanAttemptHealthCheck("backend-1"))

	// Record second failure - should open circuit
	tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, testErr)
	cbState, _ = tracker.GetCircuitBreakerState("backend-1")
	assert.Equal(t, CircuitOpen, cbState)
	assert.False(t, tracker.CanAttemptHealthCheck("backend-1"))
	assert.True(t, tracker.IsCircuitOpen("backend-1"))
}

func TestStatusTracker_CircuitBreakerStateInSnapshot(t *testing.T) {
	t.Parallel()

	cbConfig := &CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 2,
		Timeout:          60 * time.Second,
	}
	tracker := newStatusTracker(3, cbConfig)

	// Record initial failure to create circuit breaker
	tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, errors.New("test"))

	// Get initial state snapshot
	state, exists := tracker.GetState("backend-1")
	assert.True(t, exists)
	assert.Equal(t, CircuitClosed, state.CircuitState)
	assert.False(t, state.CircuitLastChanged.IsZero())

	// Open circuit with second failure
	tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, errors.New("test"))

	// Get state snapshot after opening
	state2, _ := tracker.GetState("backend-1")
	assert.Equal(t, CircuitOpen, state2.CircuitState)
	assert.True(t, state2.CircuitLastChanged.After(state.CircuitLastChanged))
}

func TestStatusTracker_CircuitBreakerDisabled(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(3, nil)

	// Don't initialize circuit breaker
	// CanAttemptHealthCheck should always return true
	assert.True(t, tracker.CanAttemptHealthCheck("backend-1"))

	// Record multiple failures
	for i := 0; i < 10; i++ {
		tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, errors.New("test"))
	}

	// Still should allow health checks (no circuit breaker)
	assert.True(t, tracker.CanAttemptHealthCheck("backend-1"))
	assert.False(t, tracker.IsCircuitOpen("backend-1"))

	// Circuit breaker state should not exist
	_, exists := tracker.GetCircuitBreakerState("backend-1")
	assert.False(t, exists)
}

func TestStatusTracker_CircuitBreakerHalfOpen(t *testing.T) {
	t.Parallel()

	cbConfig := &CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 2,
		Timeout:          50 * time.Millisecond,
	}
	tracker := newStatusTracker(3, cbConfig)

	testErr := errors.New("health check failed")

	// Open the circuit
	tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, testErr)
	tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, testErr)

	assert.True(t, tracker.IsCircuitOpen("backend-1"))
	assert.False(t, tracker.CanAttemptHealthCheck("backend-1"))

	// Wait for timeout
	time.Sleep(60 * time.Millisecond)

	// Next attempt should transition to half-open
	assert.True(t, tracker.CanAttemptHealthCheck("backend-1"))

	cbState, _ := tracker.GetCircuitBreakerState("backend-1")
	assert.Equal(t, CircuitHalfOpen, cbState)

	// Only one attempt allowed in half-open
	assert.False(t, tracker.CanAttemptHealthCheck("backend-1"))
}

func TestState_JSONSerialization(t *testing.T) {
	t.Parallel()

	// Test that LastError is excluded from JSON and LastErrorCategory is included
	tracker := newStatusTracker(3, nil)

	// Record a failure with a timeout error that contains sensitive information in the wrapped error
	sensitiveErr := errors.New("timeout connecting to https://internal-server.example.com:8080/api/health?token=secret123")
	wrappedErr := fmt.Errorf("%w: %v", vmcp.ErrTimeout, sensitiveErr)
	tracker.RecordFailure("backend-1", "Test Backend", vmcp.BackendUnhealthy, wrappedErr)

	// Get the state
	state, exists := tracker.GetState("backend-1")
	require.True(t, exists)
	require.NotNil(t, state)

	// Verify internal state has the error
	assert.NotNil(t, state.LastError)
	assert.Contains(t, state.LastError.Error(), "secret123", "raw error should contain sensitive data")

	// Verify LastErrorCategory is populated with sanitized value
	assert.Equal(t, "timeout", state.LastErrorCategory)

	// Marshal to JSON
	jsonData, err := json.Marshal(state)
	require.NoError(t, err)

	jsonStr := string(jsonData)

	// Verify sensitive data is NOT in JSON
	assert.NotContains(t, jsonStr, "secret123", "JSON should not contain sensitive token")
	assert.NotContains(t, jsonStr, "internal-server.example.com", "JSON should not contain internal hostname")
	assert.NotContains(t, jsonStr, `"LastError":`, "JSON should not include LastError field")

	// Verify sanitized category IS in JSON
	assert.Contains(t, jsonStr, "LastErrorCategory", "JSON should include LastErrorCategory field")
	assert.Contains(t, jsonStr, "timeout", "JSON should contain sanitized error category")

	// Unmarshal and verify structure
	var unmarshaled State
	err = json.Unmarshal(jsonData, &unmarshaled)
	require.NoError(t, err)

	// After unmarshaling, LastError should be nil (not serialized)
	assert.Nil(t, unmarshaled.LastError, "LastError should not be present after JSON roundtrip")
	assert.Equal(t, "timeout", unmarshaled.LastErrorCategory, "LastErrorCategory should be preserved")
}

func TestSanitizeError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: "",
		},
		{
			name:     "authentication error",
			err:      vmcp.ErrAuthenticationFailed,
			expected: "authentication_failed",
		},
		{
			name:     "timeout error",
			err:      vmcp.ErrTimeout,
			expected: "timeout",
		},
		{
			name:     "cancellation error",
			err:      vmcp.ErrCancelled,
			expected: "cancelled",
		},
		{
			name:     "backend unavailable",
			err:      vmcp.ErrBackendUnavailable,
			expected: "backend_unavailable",
		},
		{
			name:     "generic error",
			err:      errors.New("some random error with sensitive data"),
			expected: "health_check_failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := sanitizeError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
