package health

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

const (
	testBackendID   = "test-backend"
	testBackendName = "Test Backend"
)

func TestCircuitBreaker_OpenAfterThreshold(t *testing.T) {
	t.Parallel()
	// Setup: Create status tracker with circuit breaker enabled
	cbConfig := &CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 3,
		Timeout:          60 * time.Second,
	}
	tracker := newStatusTracker(5, cbConfig)

	backendID := testBackendID
	backendName := testBackendName

	// Record failures up to threshold
	for i := 0; i < 3; i++ {
		tracker.RecordFailure(backendID, backendName, vmcp.BackendUnhealthy, assert.AnError)
	}

	// Verify circuit is open
	assert.True(t, tracker.isCircuitOpen(backendID), "Circuit should be open after threshold failures")

	// Verify state
	state, exists := tracker.GetState(backendID)
	require.True(t, exists, "Backend state should exist")
	assert.Equal(t, vmcp.BackendUnhealthy, state.Status, "Status should be unhealthy")
	require.NotNil(t, state.CircuitBreakerState, "Circuit breaker state should be populated")
	assert.Equal(t, "open", state.CircuitBreakerState.State, "Circuit should be in open state")
	assert.Equal(t, 3, state.CircuitBreakerState.FailureCount, "Failure count should match")
	assert.False(t, state.CircuitBreakerState.OpenTime.IsZero(), "Open time should be set")
}

func TestCircuitBreaker_HalfOpenTransition(t *testing.T) {
	t.Parallel()
	// Setup: Create status tracker with short timeout for testing
	cbConfig := &CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 2,
		Timeout:          100 * time.Millisecond,
	}
	tracker := newStatusTracker(5, cbConfig)

	backendID := testBackendID
	backendName := testBackendName

	// Open the circuit
	tracker.RecordFailure(backendID, backendName, vmcp.BackendUnhealthy, assert.AnError)
	tracker.RecordFailure(backendID, backendName, vmcp.BackendUnhealthy, assert.AnError)

	assert.True(t, tracker.isCircuitOpen(backendID), "Circuit should be open")

	// Wait for timeout to elapse
	time.Sleep(150 * time.Millisecond)

	// Trigger half-open transition
	tracker.checkHalfOpenTransition(backendID)

	// Verify circuit is not open (it's half-open)
	assert.False(t, tracker.isCircuitOpen(backendID), "Circuit should not be open after timeout")

	// Verify state
	state, exists := tracker.GetState(backendID)
	require.True(t, exists, "Backend state should exist")
	assert.Equal(t, vmcp.BackendDegraded, state.Status, "Status should be degraded in half-open")
	require.NotNil(t, state.CircuitBreakerState, "Circuit breaker state should be populated")
	assert.Equal(t, "halfopen", state.CircuitBreakerState.State, "Circuit should be in half-open state")
}

func TestCircuitBreaker_CloseOnSuccessfulRecovery(t *testing.T) {
	t.Parallel()
	// Setup: Create status tracker
	cbConfig := &CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 2,
		Timeout:          50 * time.Millisecond,
	}
	tracker := newStatusTracker(5, cbConfig)

	backendID := testBackendID
	backendName := testBackendName

	// Open the circuit
	tracker.RecordFailure(backendID, backendName, vmcp.BackendUnhealthy, assert.AnError)
	tracker.RecordFailure(backendID, backendName, vmcp.BackendUnhealthy, assert.AnError)
	assert.True(t, tracker.isCircuitOpen(backendID), "Circuit should be open")

	// Wait and transition to half-open
	time.Sleep(60 * time.Millisecond)
	tracker.checkHalfOpenTransition(backendID)

	// Record success - should close circuit
	tracker.RecordSuccess(backendID, backendName, vmcp.BackendHealthy)

	// Verify circuit is closed
	assert.False(t, tracker.isCircuitOpen(backendID), "Circuit should be closed after successful recovery")

	// Verify state
	state, exists := tracker.GetState(backendID)
	require.True(t, exists, "Backend state should exist")
	require.NotNil(t, state.CircuitBreakerState, "Circuit breaker state should be populated")
	assert.Equal(t, "closed", state.CircuitBreakerState.State, "Circuit should be in closed state")
	assert.Equal(t, 0, state.CircuitBreakerState.FailureCount, "Failure count should be reset")
}

func TestCircuitBreaker_ReopenOnFailedRecovery(t *testing.T) {
	t.Parallel()
	// Setup: Create status tracker
	cbConfig := &CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 2,
		Timeout:          50 * time.Millisecond,
	}
	tracker := newStatusTracker(5, cbConfig)

	backendID := testBackendID
	backendName := testBackendName

	// Open the circuit
	tracker.RecordFailure(backendID, backendName, vmcp.BackendUnhealthy, assert.AnError)
	tracker.RecordFailure(backendID, backendName, vmcp.BackendUnhealthy, assert.AnError)
	assert.True(t, tracker.isCircuitOpen(backendID), "Circuit should be open")

	// Wait and transition to half-open
	time.Sleep(60 * time.Millisecond)
	tracker.checkHalfOpenTransition(backendID)
	assert.False(t, tracker.isCircuitOpen(backendID), "Circuit should be half-open")

	// Record failure - should reopen circuit
	tracker.RecordFailure(backendID, backendName, vmcp.BackendUnhealthy, assert.AnError)

	// Verify circuit is open again
	assert.True(t, tracker.isCircuitOpen(backendID), "Circuit should be open after failed recovery")

	// Verify state
	state, exists := tracker.GetState(backendID)
	require.True(t, exists, "Backend state should exist")
	assert.Equal(t, vmcp.BackendUnhealthy, state.Status, "Status should be unhealthy")
	require.NotNil(t, state.CircuitBreakerState, "Circuit breaker state should be populated")
	assert.Equal(t, "open", state.CircuitBreakerState.State, "Circuit should be in open state")
}

func TestCircuitBreaker_Disabled(t *testing.T) {
	t.Parallel()
	// Setup: Create status tracker with circuit breaker disabled
	tracker := newStatusTracker(5, nil)

	backendID := testBackendID
	backendName := testBackendName

	// Record many failures
	for i := 0; i < 10; i++ {
		tracker.RecordFailure(backendID, backendName, vmcp.BackendUnhealthy, assert.AnError)
	}

	// Verify circuit is never open
	assert.False(t, tracker.isCircuitOpen(backendID), "Circuit should never open when disabled")

	// Verify state has no circuit breaker info
	state, exists := tracker.GetState(backendID)
	require.True(t, exists, "Backend state should exist")
	assert.Nil(t, state.CircuitBreakerState, "Circuit breaker state should be nil when disabled")
}

func TestCircuitBreaker_DisabledExplicitly(t *testing.T) {
	t.Parallel()
	// Setup: Create status tracker with circuit breaker explicitly disabled
	cbConfig := &CircuitBreakerConfig{
		Enabled:          false,
		FailureThreshold: 2,
		Timeout:          60 * time.Second,
	}
	tracker := newStatusTracker(5, cbConfig)

	backendID := testBackendID
	backendName := testBackendName

	// Record failures
	for i := 0; i < 5; i++ {
		tracker.RecordFailure(backendID, backendName, vmcp.BackendUnhealthy, assert.AnError)
	}

	// Verify circuit is never open
	assert.False(t, tracker.isCircuitOpen(backendID), "Circuit should never open when explicitly disabled")

	// Verify state has no circuit breaker info
	state, exists := tracker.GetState(backendID)
	require.True(t, exists, "Backend state should exist")
	assert.Nil(t, state.CircuitBreakerState, "Circuit breaker state should be nil when disabled")
}

func TestCircuitBreaker_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	// Setup: Create status tracker with circuit breaker enabled
	cbConfig := &CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 5,
		Timeout:          100 * time.Millisecond,
	}
	tracker := newStatusTracker(10, cbConfig)

	backendID := testBackendID
	backendName := testBackendName

	// Test concurrent access from multiple goroutines
	var wg sync.WaitGroup
	numGoroutines := 10
	operationsPerGoroutine := 20

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			for j := 0; j < operationsPerGoroutine; j++ {
				// Alternate between recording failures and successes
				if (id+j)%2 == 0 {
					tracker.RecordFailure(backendID, backendName, vmcp.BackendUnhealthy, assert.AnError)
				} else {
					tracker.RecordSuccess(backendID, backendName, vmcp.BackendHealthy)
				}

				// Check circuit state
				_ = tracker.isCircuitOpen(backendID)

				// Get state
				_, _ = tracker.GetState(backendID)

				// Small sleep to increase contention
				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	// Wait for all goroutines to complete
	wg.Wait()

	// Verify we can still get state without panic
	state, exists := tracker.GetState(backendID)
	assert.True(t, exists, "Backend state should exist after concurrent access")
	assert.NotNil(t, state, "State should not be nil")
	assert.NotNil(t, state.CircuitBreakerState, "Circuit breaker state should exist")
}

func TestCircuitBreaker_MultipleBackends(t *testing.T) {
	t.Parallel()
	// Setup: Create status tracker
	cbConfig := &CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 2,
		Timeout:          60 * time.Second,
	}
	tracker := newStatusTracker(5, cbConfig)

	// Open circuit for backend1
	tracker.RecordFailure("backend1", "Backend 1", vmcp.BackendUnhealthy, assert.AnError)
	tracker.RecordFailure("backend1", "Backend 1", vmcp.BackendUnhealthy, assert.AnError)

	// Keep backend2 healthy
	tracker.RecordSuccess("backend2", "Backend 2", vmcp.BackendHealthy)

	// Verify backend1 circuit is open
	assert.True(t, tracker.isCircuitOpen("backend1"), "Backend1 circuit should be open")

	// Verify backend2 circuit is closed
	assert.False(t, tracker.isCircuitOpen("backend2"), "Backend2 circuit should be closed")

	// Verify states are independent
	state1, exists := tracker.GetState("backend1")
	require.True(t, exists)
	assert.Equal(t, "open", state1.CircuitBreakerState.State)

	state2, exists := tracker.GetState("backend2")
	require.True(t, exists)
	assert.Equal(t, "closed", state2.CircuitBreakerState.State)
}

func TestCircuitBreaker_NoTimeoutTransitionWhenClosed(t *testing.T) {
	t.Parallel()
	// Setup: Create status tracker
	cbConfig := &CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 3,
		Timeout:          50 * time.Millisecond,
	}
	tracker := newStatusTracker(5, cbConfig)

	backendID := testBackendID
	backendName := testBackendName

	// Record one failure (not enough to open)
	tracker.RecordFailure(backendID, backendName, vmcp.BackendUnhealthy, assert.AnError)

	// Wait longer than timeout
	time.Sleep(60 * time.Millisecond)

	// Try to trigger half-open transition
	tracker.checkHalfOpenTransition(backendID)

	// Verify circuit is still closed (never opened)
	state, exists := tracker.GetState(backendID)
	require.True(t, exists)
	assert.Equal(t, "closed", state.CircuitBreakerState.State, "Circuit should remain closed")
}

func TestCircuitBreaker_FailureCountResetOnSuccess(t *testing.T) {
	t.Parallel()
	// Setup: Create status tracker
	cbConfig := &CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 5,
		Timeout:          60 * time.Second,
	}
	tracker := newStatusTracker(10, cbConfig)

	backendID := testBackendID
	backendName := testBackendName

	// Record failures below threshold
	for i := 0; i < 3; i++ {
		tracker.RecordFailure(backendID, backendName, vmcp.BackendUnhealthy, assert.AnError)
	}

	state, _ := tracker.GetState(backendID)
	assert.Equal(t, 3, state.CircuitBreakerState.FailureCount, "Should have 3 failures")

	// Record success - should reset failure count
	tracker.RecordSuccess(backendID, backendName, vmcp.BackendHealthy)

	state, _ = tracker.GetState(backendID)
	assert.Equal(t, 0, state.CircuitBreakerState.FailureCount, "Failure count should be reset")
	assert.Equal(t, "closed", state.CircuitBreakerState.State, "Circuit should remain closed")
}
