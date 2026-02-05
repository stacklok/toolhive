// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCircuitBreaker_InitialState(t *testing.T) {
	t.Parallel()

	cb := newCircuitBreaker(5, 60*time.Second, "")

	assert.Equal(t, CircuitClosed, cb.GetState())
	assert.Equal(t, 0, cb.GetFailureCount())
	assert.True(t, cb.CanAttempt())
}

func TestCircuitBreaker_ClosedToOpen(t *testing.T) {
	t.Parallel()

	threshold := 3
	cb := newCircuitBreaker(threshold, 60*time.Second, "")

	// Record failures below threshold - should stay closed
	for i := 0; i < threshold-1; i++ {
		cb.RecordFailure()
		assert.Equal(t, CircuitClosed, cb.GetState())
		assert.True(t, cb.CanAttempt())
	}

	// One more failure should open the circuit
	cb.RecordFailure()
	assert.Equal(t, CircuitOpen, cb.GetState())
	assert.Equal(t, threshold, cb.GetFailureCount())
	assert.False(t, cb.CanAttempt())
}

func TestCircuitBreaker_OpenToHalfOpen(t *testing.T) {
	t.Parallel()

	timeout := 100 * time.Millisecond
	cb := newCircuitBreaker(3, timeout, "")

	// Open the circuit
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}
	assert.Equal(t, CircuitOpen, cb.GetState())
	assert.False(t, cb.CanAttempt())

	// Wait for timeout
	time.Sleep(timeout + 50*time.Millisecond)

	// Next CanAttempt should transition to half-open
	assert.True(t, cb.CanAttempt())
	assert.Equal(t, CircuitHalfOpen, cb.GetState())

	// Subsequent attempts should be blocked until test completes
	assert.False(t, cb.CanAttempt())
}

func TestCircuitBreaker_HalfOpenToClosed(t *testing.T) {
	t.Parallel()

	timeout := 50 * time.Millisecond
	cb := newCircuitBreaker(3, timeout, "")

	// Open the circuit
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}

	// Wait and transition to half-open
	time.Sleep(timeout + 50*time.Millisecond)
	assert.True(t, cb.CanAttempt())
	assert.Equal(t, CircuitHalfOpen, cb.GetState())

	// Record success - should close the circuit
	cb.RecordSuccess()
	assert.Equal(t, CircuitClosed, cb.GetState())
	assert.Equal(t, 0, cb.GetFailureCount())
	assert.True(t, cb.CanAttempt())
}

func TestCircuitBreaker_HalfOpenToOpen(t *testing.T) {
	t.Parallel()

	timeout := 50 * time.Millisecond
	cb := newCircuitBreaker(3, timeout, "")

	// Open the circuit
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}

	// Wait and transition to half-open
	time.Sleep(timeout + 50*time.Millisecond)
	assert.True(t, cb.CanAttempt())
	assert.Equal(t, CircuitHalfOpen, cb.GetState())

	// Record failure - should go back to open
	cb.RecordFailure()
	assert.Equal(t, CircuitOpen, cb.GetState())
	assert.False(t, cb.CanAttempt())
}

func TestCircuitBreaker_ResetOnSuccess(t *testing.T) {
	t.Parallel()

	cb := newCircuitBreaker(5, 60*time.Second, "")

	// Record some failures
	cb.RecordFailure()
	cb.RecordFailure()
	assert.Equal(t, 2, cb.GetFailureCount())
	assert.Equal(t, CircuitClosed, cb.GetState())

	// Record success - should reset count
	cb.RecordSuccess()
	assert.Equal(t, 0, cb.GetFailureCount())
	assert.Equal(t, CircuitClosed, cb.GetState())
}

func TestCircuitBreaker_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	cb := newCircuitBreaker(100, 100*time.Millisecond, "")
	iterations := 1000

	var wg sync.WaitGroup

	// Concurrent failures
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			cb.RecordFailure()
		}
	}()

	// Concurrent successes
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			cb.RecordSuccess()
		}
	}()

	// Concurrent state checks
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = cb.GetState()
			_ = cb.CanAttempt()
		}
	}()

	wg.Wait()

	// Should not crash and should have a valid state
	state := cb.GetState()
	assert.True(t, state == CircuitClosed || state == CircuitOpen || state == CircuitHalfOpen)
}

func TestCircuitBreaker_StateTransitionTimestamps(t *testing.T) {
	t.Parallel()

	cb := newCircuitBreaker(2, 50*time.Millisecond, "")

	initialTime := cb.GetLastStateChange()
	require.False(t, initialTime.IsZero())

	// Transition to open
	time.Sleep(10 * time.Millisecond)
	cb.RecordFailure()
	cb.RecordFailure()
	openTime := cb.GetLastStateChange()
	assert.True(t, openTime.After(initialTime))

	// Transition to half-open
	time.Sleep(60 * time.Millisecond)
	cb.CanAttempt()
	halfOpenTime := cb.GetLastStateChange()
	assert.True(t, halfOpenTime.After(openTime))

	// Transition to closed
	cb.RecordSuccess()
	closedTime := cb.GetLastStateChange()
	assert.True(t, closedTime.After(halfOpenTime))
}

func TestCircuitBreaker_GetSnapshot(t *testing.T) {
	t.Parallel()

	cb := newCircuitBreaker(3, 60*time.Second, "")

	// Record some failures
	cb.RecordFailure()
	cb.RecordFailure()

	snapshot := cb.GetSnapshot()
	assert.Equal(t, CircuitClosed, snapshot.State)
	assert.Equal(t, 2, snapshot.FailureCount)
	assert.False(t, snapshot.LastStateChange.IsZero())
	assert.False(t, snapshot.LastFailureTime.IsZero())

	// Open the circuit
	cb.RecordFailure()
	snapshot2 := cb.GetSnapshot()
	assert.Equal(t, CircuitOpen, snapshot2.State)
	assert.Equal(t, 3, snapshot2.FailureCount)
	assert.True(t, snapshot2.LastStateChange.After(snapshot.LastStateChange))
}

func TestCircuitBreaker_GetSnapshotIsReadOnly(t *testing.T) {
	t.Parallel()

	timeout := 50 * time.Millisecond
	cb := newCircuitBreaker(2, timeout, "test-backend")

	// Open the circuit
	cb.RecordFailure()
	cb.RecordFailure()
	assert.Equal(t, CircuitOpen, cb.GetState())

	// GetSnapshot before timeout - should be OPEN
	snapshot1 := cb.GetSnapshot()
	assert.Equal(t, CircuitOpen, snapshot1.State)

	// Wait for timeout to elapse
	time.Sleep(timeout + 20*time.Millisecond)

	// GetSnapshot after timeout - should STILL be OPEN (GetSnapshot is read-only)
	snapshot2 := cb.GetSnapshot()
	assert.Equal(t, CircuitOpen, snapshot2.State)
	// LastStateChange should not have changed since no transition occurred
	assert.Equal(t, snapshot1.LastStateChange, snapshot2.LastStateChange)

	// Verify GetState also shows OPEN (no transition until CanAttempt is called)
	assert.Equal(t, CircuitOpen, cb.GetState())

	// Now call CanAttempt which should trigger the OPEN -> HALF_OPEN transition
	assert.True(t, cb.CanAttempt())
	assert.Equal(t, CircuitHalfOpen, cb.GetState())

	// Now GetSnapshot should show HALF_OPEN
	snapshot3 := cb.GetSnapshot()
	assert.Equal(t, CircuitHalfOpen, snapshot3.State)
	assert.True(t, snapshot3.LastStateChange.After(snapshot1.LastStateChange))
}

func TestCircuitBreaker_HalfOpenSingleTest(t *testing.T) {
	t.Parallel()

	timeout := 50 * time.Millisecond
	cb := newCircuitBreaker(2, timeout, "")

	// Open the circuit
	cb.RecordFailure()
	cb.RecordFailure()
	assert.Equal(t, CircuitOpen, cb.GetState())

	// Wait for timeout
	time.Sleep(timeout + 50*time.Millisecond)

	// First CanAttempt should succeed and transition to half-open
	assert.True(t, cb.CanAttempt())
	assert.Equal(t, CircuitHalfOpen, cb.GetState())

	// Second CanAttempt should fail (test in progress)
	assert.False(t, cb.CanAttempt())

	// Third CanAttempt should still fail
	assert.False(t, cb.CanAttempt())

	// After recording result, should allow new tests
	cb.RecordSuccess()
	assert.Equal(t, CircuitClosed, cb.GetState())
	assert.True(t, cb.CanAttempt())
}

func TestCircuitBreaker_ZeroThreshold(t *testing.T) {
	t.Parallel()

	// Edge case: threshold of 1 should open immediately on first failure
	cb := newCircuitBreaker(1, 60*time.Second, "")

	// Should be closed initially
	assert.Equal(t, CircuitClosed, cb.GetState())

	// First failure should open the circuit
	cb.RecordFailure()
	assert.Equal(t, CircuitOpen, cb.GetState())
	assert.False(t, cb.CanAttempt())
}

func TestCircuitBreaker_MultipleOpenCloseTransitions(t *testing.T) {
	t.Parallel()

	timeout := 50 * time.Millisecond
	cb := newCircuitBreaker(2, timeout, "")

	// First cycle: open then close
	cb.RecordFailure()
	cb.RecordFailure()
	assert.Equal(t, CircuitOpen, cb.GetState())

	time.Sleep(timeout + 50*time.Millisecond)
	assert.True(t, cb.CanAttempt())
	cb.RecordSuccess()
	assert.Equal(t, CircuitClosed, cb.GetState())

	// Second cycle: open again
	cb.RecordFailure()
	cb.RecordFailure()
	assert.Equal(t, CircuitOpen, cb.GetState())

	time.Sleep(timeout + 50*time.Millisecond)
	assert.True(t, cb.CanAttempt())
	cb.RecordSuccess()
	assert.Equal(t, CircuitClosed, cb.GetState())

	// Should be fully functional
	assert.True(t, cb.CanAttempt())
	assert.Equal(t, 0, cb.GetFailureCount())
}
