// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/logger"
)

// CircuitState represents the state of a circuit breaker
type CircuitState string

const (
	// CircuitClosed indicates normal operation - requests pass through
	CircuitClosed CircuitState = "closed"
	// CircuitOpen indicates failing state - requests fail immediately
	CircuitOpen CircuitState = "open"
	// CircuitHalfOpen indicates recovery testing - limited requests allowed
	CircuitHalfOpen CircuitState = "half_open"
)

// circuitBreaker manages circuit breaker state for a single backend.
// It implements the circuit breaker pattern to prevent cascading failures
// by tracking failures and transitioning through states:
// Closed → Open → HalfOpen → Closed
type circuitBreaker struct {
	mu sync.Mutex

	// name is used for logging purposes to identify which backend this circuit breaker belongs to
	name string

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
		cb.state = CircuitClosed
		cb.lastStateChange = time.Now()

		// Log successful recovery
		if previousState == CircuitHalfOpen {
			logger.Infof("Circuit breaker for backend %s CLOSED (recovery successful)", cb.name)
		}
	}
}

// RecordFailure records a failed operation.
// Increments failure count and transitions to Open if threshold exceeded.
func (cb *circuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	previousState := cb.state
	cb.failureCount++
	cb.lastFailureTime = time.Now()
	cb.halfOpenTestInProgress = false

	if cb.state == CircuitClosed && cb.failureCount >= cb.failureThreshold {
		cb.state = CircuitOpen
		cb.lastStateChange = time.Now()
		logger.Warnf("Circuit breaker for backend %s OPENED (threshold exceeded)", cb.name)
	} else if cb.state == CircuitHalfOpen {
		// Failed in half-open state, go back to open
		cb.state = CircuitOpen
		cb.lastStateChange = time.Now()
		if previousState == CircuitHalfOpen {
			logger.Warnf("Circuit breaker for backend %s returned to OPEN from half-open (recovery failed)", cb.name)
		}
	}
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
		if time.Since(cb.lastStateChange) >= cb.timeout {
			cb.state = CircuitHalfOpen
			cb.lastStateChange = time.Now()
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
