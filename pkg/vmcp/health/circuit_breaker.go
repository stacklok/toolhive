// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"log/slog"
	"sync"
	"time"
)

// CircuitState represents the state of a circuit breaker
type CircuitState string

const (
	// CircuitClosed indicates normal operation - requests pass through
	CircuitClosed CircuitState = "closed"
	// CircuitOpen indicates failing state - requests fail immediately
	CircuitOpen CircuitState = "open"
	// CircuitHalfOpen indicates recovery testing - limited requests allowed
	CircuitHalfOpen CircuitState = "half-open"
)

// CircuitBreaker defines the interface for circuit breaker implementations.
type CircuitBreaker interface {
	// RecordSuccess records a successful operation
	RecordSuccess()
	// RecordFailure records a failed operation
	RecordFailure()
	// CanAttempt checks if an operation should be allowed based on circuit state
	CanAttempt() bool
	// GetState returns the current state of the circuit breaker
	GetState() CircuitState
	// GetLastStateChange returns the time when the state last changed
	GetLastStateChange() time.Time
	// GetFailureCount returns the current failure count
	GetFailureCount() int
	// GetSnapshot returns an immutable snapshot of the circuit breaker state
	GetSnapshot() circuitBreakerSnapshot
}

// circuitBreaker manages circuit breaker state for a single backend.
// It implements the circuit breaker pattern to prevent cascading failures
// by tracking failures and transitioning through states:
// Closed → Open → HalfOpen → Closed
type circuitBreaker struct {
	// name is used for logging purposes to identify which backend this circuit breaker belongs to
	// name is immutable after initialization and doesn't require mutex protection
	name string

	mu sync.Mutex
	// Fields below are protected by mu
	state            CircuitState
	failureCount     int
	failureThreshold int
	timeout          time.Duration

	lastStateChange time.Time
	lastFailureTime time.Time

	// For half-open state management
	halfOpenTestInProgress bool
}

// newCircuitBreaker creates a new circuit breaker with the specified configuration.
// The name parameter is optional and used for logging (can be empty string).
func newCircuitBreaker(failureThreshold int, timeout time.Duration, name string) *circuitBreaker {
	return &circuitBreaker{
		name:             name,
		state:            CircuitClosed,
		failureThreshold: failureThreshold,
		timeout:          timeout,
		lastStateChange:  time.Now(),
	}
}

// RecordSuccess records a successful operation.
// Resets failure count and transitions to Closed state if not already there.
func (cb *circuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	previousState := cb.state
	cb.failureCount = 0
	cb.halfOpenTestInProgress = false

	if cb.state != CircuitClosed {
		cb.transitionTo(CircuitClosed)

		// Log successful recovery
		if previousState == CircuitHalfOpen {
			slog.Info("Circuit breaker CLOSED (recovery successful)", "backend", cb.name)
		}
	}
}

// RecordFailure records a failed operation.
// Increments failure count and transitions to Open if threshold exceeded.
func (cb *circuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount++
	cb.lastFailureTime = time.Now()
	cb.halfOpenTestInProgress = false

	if cb.state == CircuitClosed && cb.failureCount >= cb.failureThreshold {
		cb.transitionTo(CircuitOpen)
		slog.Warn("Circuit breaker OPENED (threshold exceeded)", "backend", cb.name)
	} else if cb.state == CircuitHalfOpen {
		// Failed in half-open state, go back to open
		cb.transitionTo(CircuitOpen)
		slog.Warn("Circuit breaker returned to OPEN from half-open (recovery failed)", "backend", cb.name)
	}
}

// transitionTo changes the circuit breaker state and updates the lastStateChange timestamp.
// Must be called with lock held.
func (cb *circuitBreaker) transitionTo(newState CircuitState) {
	cb.state = newState
	cb.lastStateChange = time.Now()
}

// tryTransitionOpenToHalfOpen checks if the circuit is OPEN and timeout has elapsed,
// and transitions to HALF_OPEN if so. Returns true if transition occurred.
// Must be called with lock held.
func (cb *circuitBreaker) tryTransitionOpenToHalfOpen() bool {
	if cb.state == CircuitOpen && time.Since(cb.lastStateChange) >= cb.timeout {
		cb.transitionTo(CircuitHalfOpen)
		return true
	}
	return false
}

// CanAttempt checks if an operation should be allowed based on circuit state.
// Returns true if the operation can proceed, false if it should be rejected.
func (cb *circuitBreaker) CanAttempt() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitClosed:
		return true

	case CircuitOpen:
		// Check if timeout has elapsed to transition to half-open
		if cb.tryTransitionOpenToHalfOpen() {
			cb.halfOpenTestInProgress = true
			return true
		}
		return false

	case CircuitHalfOpen:
		// Only allow one test request at a time in half-open state
		if cb.halfOpenTestInProgress {
			return false
		}
		cb.halfOpenTestInProgress = true
		return true

	default:
		return false
	}
}

// GetState returns the current state of the circuit breaker.
// Returns a copy to ensure thread-safety.
func (cb *circuitBreaker) GetState() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// GetLastStateChange returns the time when the state last changed.
func (cb *circuitBreaker) GetLastStateChange() time.Time {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.lastStateChange
}

// GetFailureCount returns the current failure count.
func (cb *circuitBreaker) GetFailureCount() int {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.failureCount
}

// GetSnapshot returns an immutable snapshot of the circuit breaker state.
// This is a read-only operation that does not trigger state transitions.
// The snapshot reflects the current state at the time of the call.
// Note: If the circuit is OPEN and the timeout has elapsed, the snapshot will still
// show OPEN until the next call to CanAttempt() triggers the transition to HALF_OPEN.
func (cb *circuitBreaker) GetSnapshot() circuitBreakerSnapshot {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	return circuitBreakerSnapshot{
		State:           cb.state,
		FailureCount:    cb.failureCount,
		LastStateChange: cb.lastStateChange,
		LastFailureTime: cb.lastFailureTime,
	}
}

// circuitBreakerSnapshot represents an immutable snapshot of circuit breaker state
type circuitBreakerSnapshot struct {
	State           CircuitState
	FailureCount    int
	LastStateChange time.Time
	LastFailureTime time.Time
}

// alwaysClosedCircuit is a no-op circuit breaker implementation that always allows operations.
// Used when circuit breaker is disabled.
type alwaysClosedCircuit struct{}

// RecordSuccess is a no-op for the always-closed circuit.
func (*alwaysClosedCircuit) RecordSuccess() {}

// RecordFailure is a no-op for the always-closed circuit.
func (*alwaysClosedCircuit) RecordFailure() {}

// CanAttempt always returns true for the always-closed circuit.
func (*alwaysClosedCircuit) CanAttempt() bool {
	return true
}

// GetState always returns CircuitClosed.
func (*alwaysClosedCircuit) GetState() CircuitState {
	return CircuitClosed
}

// GetLastStateChange returns zero time since the circuit never changes state.
func (*alwaysClosedCircuit) GetLastStateChange() time.Time {
	return time.Time{}
}

// GetFailureCount always returns 0.
func (*alwaysClosedCircuit) GetFailureCount() int {
	return 0
}

// GetSnapshot returns a snapshot representing a closed circuit with no failures.
func (*alwaysClosedCircuit) GetSnapshot() circuitBreakerSnapshot {
	return circuitBreakerSnapshot{
		State:           CircuitClosed,
		FailureCount:    0,
		LastStateChange: time.Time{},
		LastFailureTime: time.Time{},
	}
}
