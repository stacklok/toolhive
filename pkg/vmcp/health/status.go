// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"errors"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// backendHealthState tracks the health state of a single backend.
type backendHealthState struct {
	// status is the current health status.
	status vmcp.BackendHealthStatus

	// consecutiveFailures is the number of consecutive failed health checks.
	consecutiveFailures int

	// lastCheckTime is when the last health check was performed.
	lastCheckTime time.Time

	// lastError is the last error encountered during health check (if any).
	lastError error

	// lastTransitionTime is when the status last changed.
	lastTransitionTime time.Time

	// circuitBreaker manages circuit breaker state for this backend.
	// nil if circuit breaker is disabled.
	circuitBreaker *circuitBreaker
}

// statusTracker tracks health status for multiple backends.
// It provides thread-safe access to backend health states and handles
// status transitions with configurable unhealthy thresholds.
type statusTracker struct {
	mu sync.RWMutex

	// states maps backend ID to its health state.
	states map[string]*backendHealthState

	// removedBackends tracks backends that were explicitly removed to prevent
	// race conditions where in-flight health checks re-create removed backends.
	removedBackends map[string]bool

	// unhealthyThreshold is the number of consecutive failures before marking unhealthy.
	unhealthyThreshold int

	// circuitBreakerConfig contains circuit breaker configuration.
	// nil means circuit breaker is disabled.
	circuitBreakerConfig *CircuitBreakerConfig
}

// newStatusTracker creates a new status tracker.
//
// Parameters:
//   - unhealthyThreshold: Number of consecutive failures before marking backend unhealthy.
//     Must be >= 1. Recommended: 3 failures.
//   - circuitBreakerConfig: Circuit breaker configuration. nil to disable circuit breaker.
//
// Returns a new status tracker instance.
func newStatusTracker(unhealthyThreshold int, circuitBreakerConfig *CircuitBreakerConfig) *statusTracker {
	if unhealthyThreshold < 1 {
		logger.Warnf("Invalid unhealthyThreshold %d (must be >= 1), adjusting to 1", unhealthyThreshold)
		unhealthyThreshold = 1
	}

	return &statusTracker{
		states:               make(map[string]*backendHealthState),
		removedBackends:      make(map[string]bool),
		unhealthyThreshold:   unhealthyThreshold,
		circuitBreakerConfig: circuitBreakerConfig,
	}
}

// isRemoved checks if a backend has been explicitly removed.
// Must be called with lock held.
func (t *statusTracker) isRemoved(backendID string) bool {
	return t.removedBackends[backendID]
}

// sanitizeError returns a sanitized error category string based on error type.
// This prevents exposing sensitive error details (paths, URLs, credentials) in API responses.
// Returns empty string if err is nil.
func sanitizeError(err error) string {
	if err == nil {
		return ""
	}

	// Authentication/Authorization errors
	if errors.Is(err, vmcp.ErrAuthenticationFailed) || errors.Is(err, vmcp.ErrAuthorizationFailed) {
		return "authentication_failed"
	}
	if vmcp.IsAuthenticationError(err) {
		return "authentication_failed"
	}

	// Timeout errors
	if errors.Is(err, vmcp.ErrTimeout) {
		return "timeout"
	}
	if vmcp.IsTimeoutError(err) {
		return "timeout"
	}

	// Cancellation errors
	if errors.Is(err, vmcp.ErrCancelled) {
		return "cancelled"
	}

	// Connection/availability errors
	if errors.Is(err, vmcp.ErrBackendUnavailable) {
		return "backend_unavailable"
	}
	if vmcp.IsConnectionError(err) {
		return "connection_failed"
	}

	// Generic fallback
	return "health_check_failed"
}

// copyState creates an immutable copy of a backend health state.
// Must be called with lock held.
func (*statusTracker) copyState(state *backendHealthState) *State {
	result := &State{
		Status:              state.status,
		ConsecutiveFailures: state.consecutiveFailures,
		LastCheckTime:       state.lastCheckTime,
		LastErrorCategory:   sanitizeError(state.lastError),
		LastError:           state.lastError,
		LastTransitionTime:  state.lastTransitionTime,
	}

	// Include circuit breaker state if enabled
	if state.circuitBreaker != nil {
		snapshot := state.circuitBreaker.GetSnapshot()
		result.CircuitState = snapshot.State
		result.CircuitLastChanged = snapshot.LastStateChange
	}

	return result
}

// RecordSuccess records a successful health check for a backend.
// This may mark the backend as healthy or degraded depending on recent failure history.
// If the backend had recent failures, it's marked as degraded (recovering state).
// If the backend was previously unhealthy, this transition is logged.
//
// Parameters:
//   - backendID: Unique identifier for the backend
//   - backendName: Human-readable name for logging
//   - status: The health status returned by the health check (healthy or degraded)
func (t *statusTracker) RecordSuccess(backendID string, backendName string, status vmcp.BackendHealthStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Ignore removed backends to prevent race conditions with in-flight health checks
	if t.isRemoved(backendID) {
		logger.Debugf("Ignoring health check result for removed backend %s", backendName)
		return
	}

	state, exists := t.states[backendID]
	if !exists {
		// Initialize new state - no failure history, so accept status as-is
		state = &backendHealthState{
			status:              status,
			consecutiveFailures: 0,
			lastCheckTime:       time.Now(),
			lastError:           nil,
			lastTransitionTime:  time.Now(),
		}
		t.states[backendID] = state
		logger.Debugf("Backend %s initialized as %s", backendName, status)

		// Lazily initialize circuit breaker for new state
		t.ensureCircuitBreaker(backendID, backendName)
		if state.circuitBreaker != nil {
			state.circuitBreaker.RecordSuccess()
		}
		return
	}

	// Check for status transition
	previousStatus := state.status
	previousFailures := state.consecutiveFailures

	// If backend had recent failures, mark as degraded (recovering state)
	// This takes precedence over the health check's status determination
	if previousFailures > 0 {
		state.status = vmcp.BackendDegraded
		logger.Infof("Backend %s recovering from failures: %s → %s (had %d consecutive failures)",
			backendName, previousStatus, vmcp.BackendDegraded, previousFailures)
	} else {
		// No recent failures, use the status from health check (healthy or degraded from slow response)
		state.status = status
		if previousStatus != status {
			logger.Infof("Backend %s status changed: %s → %s", backendName, previousStatus, status)
		}
	}

	state.consecutiveFailures = 0
	state.lastCheckTime = time.Now()
	state.lastError = nil

	// Update transition time if status changed
	if previousStatus != state.status {
		state.lastTransitionTime = time.Now()
	}

	// Lazily initialize and update circuit breaker (logging handled internally)
	t.ensureCircuitBreaker(backendID, backendName)
	if state.circuitBreaker != nil {
		state.circuitBreaker.RecordSuccess()
	}
}

// RecordFailure records a failed health check for a backend.
// This increments the consecutive failure count and may transition the backend to unhealthy
// if the threshold is exceeded. Status transitions are logged.
//
// Parameters:
//   - backendID: Unique identifier for the backend
//   - backendName: Human-readable name for logging
//   - status: The health status returned by the health check (unhealthy, unauthenticated, etc.)
//   - err: The error encountered during health check
func (t *statusTracker) RecordFailure(backendID string, backendName string, status vmcp.BackendHealthStatus, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Ignore removed backends to prevent race conditions with in-flight health checks
	if t.isRemoved(backendID) {
		logger.Debugf("Ignoring health check result for removed backend %s", backendName)
		return
	}

	state, exists := t.states[backendID]
	if !exists {
		// Initialize new state
		state = &backendHealthState{
			status:              vmcp.BackendUnknown,
			consecutiveFailures: 1,
			lastCheckTime:       time.Now(),
			lastError:           err,
			lastTransitionTime:  time.Now(),
		}
		t.states[backendID] = state

		// Check if threshold is reached on initialization (e.g., threshold of 1)
		if state.consecutiveFailures >= t.unhealthyThreshold {
			state.status = status
			logger.Warnf("Backend %s initialized with failure and reached threshold: %s (%d/%d failures): %v",
				backendName, status, state.consecutiveFailures, t.unhealthyThreshold, err)
		} else {
			logger.Warnf("Backend %s initialized with failure (1/%d failures, status: %s): %v",
				backendName, t.unhealthyThreshold, vmcp.BackendUnknown, err)
		}

		// Lazily initialize circuit breaker for new state
		t.ensureCircuitBreaker(backendID, backendName)
		if state.circuitBreaker != nil {
			state.circuitBreaker.RecordFailure()
		}
		return
	}

	// Record the failure
	previousStatus := state.status
	state.consecutiveFailures++
	state.lastCheckTime = time.Now()
	state.lastError = err

	// Check if threshold is reached and status has changed
	thresholdReached := state.consecutiveFailures >= t.unhealthyThreshold
	statusChanged := previousStatus != status

	if thresholdReached && statusChanged {
		// Transition to new unhealthy status
		state.status = status
		state.lastTransitionTime = time.Now()
		logger.Warnf("Backend %s health degraded: %s → %s (%d consecutive failures, threshold: %d) - last error: %v",
			backendName, previousStatus, status, state.consecutiveFailures, t.unhealthyThreshold, err)
	} else if thresholdReached {
		// Already at threshold with same status - no transition needed
		logger.Warnf("Backend %s remains %s (%d consecutive failures, incoming: %s): %v",
			backendName, state.status, state.consecutiveFailures, status, err)
	} else {
		// Below threshold - accumulating failures but not yet unhealthy
		logger.Debugf("Backend %s health check failed (%d/%d consecutive failures, current status: %s, incoming: %s): %v",
			backendName, state.consecutiveFailures, t.unhealthyThreshold, state.status, status, err)
	}

	// Lazily initialize and update circuit breaker (logging handled internally)
	t.ensureCircuitBreaker(backendID, backendName)
	if state.circuitBreaker != nil {
		state.circuitBreaker.RecordFailure()
	}
}

// GetStatus returns the current health status for a backend.
// Returns (status, exists) where exists indicates if the backend is being tracked.
// If the backend is not being tracked, returns (BackendUnknown, false).
func (t *statusTracker) GetStatus(backendID string) (vmcp.BackendHealthStatus, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	state, exists := t.states[backendID]
	if !exists {
		return vmcp.BackendUnknown, false
	}

	return state.status, true
}

// GetState returns a copy of the full health state for a backend.
// Returns (state, exists) where exists indicates if the backend is being tracked.
func (t *statusTracker) GetState(backendID string) (*State, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	state, exists := t.states[backendID]
	if !exists {
		return nil, false
	}

	return t.copyState(state), true
}

// GetAllStates returns a copy of all backend health states.
// Returns a map of backend ID to State.
func (t *statusTracker) GetAllStates() map[string]*State {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make(map[string]*State, len(t.states))
	for backendID, state := range t.states {
		result[backendID] = t.copyState(state)
	}

	return result
}

// IsHealthy returns true if the backend is currently healthy.
// Returns false if the backend is unknown or not tracked.
func (t *statusTracker) IsHealthy(backendID string) bool {
	status, exists := t.GetStatus(backendID)
	return exists && status == vmcp.BackendHealthy
}

// RemoveBackend removes a backend from the status tracker.
// The backend is marked as removed to prevent race conditions where in-flight
// health checks might try to re-create the backend state.
func (t *statusTracker) RemoveBackend(backendID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.states, backendID)
	t.removedBackends[backendID] = true
}

// ClearRemovedFlag clears the "removed" flag for a backend.
// This should be called when starting to monitor a backend that was previously removed,
// allowing health check results to be recorded again.
func (t *statusTracker) ClearRemovedFlag(backendID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.removedBackends, backendID)
}

// ensureCircuitBreaker lazily initializes a circuit breaker for a backend if needed.
// This is called automatically when recording health check results.
// Must be called with lock held.
func (t *statusTracker) ensureCircuitBreaker(backendID, backendName string) {
	// Check if circuit breaker is enabled
	if t.circuitBreakerConfig == nil || !t.circuitBreakerConfig.Enabled {
		return
	}

	state, exists := t.states[backendID]
	if !exists {
		return // State doesn't exist yet, will be created when health check is recorded
	}

	// Initialize circuit breaker if not already present
	if state.circuitBreaker == nil {
		state.circuitBreaker = newCircuitBreaker(
			t.circuitBreakerConfig.FailureThreshold,
			t.circuitBreakerConfig.Timeout,
			backendName,
		)
	}
}

// InitializeCircuitBreaker initializes a circuit breaker for the specified backend.
// Deprecated: Circuit breakers are now initialized lazily. This method is kept for
// backward compatibility but is no longer necessary.
//
// Parameters:
//   - backendID: Unique identifier for the backend
//   - backendName: Human-readable name for logging purposes
//   - failureThreshold: Number of failures before opening the circuit (ignored, uses config)
//   - timeout: Duration to wait in open state before attempting recovery (ignored, uses config)
func (t *statusTracker) InitializeCircuitBreaker(backendID, backendName string, _ int, _ time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Lazy initialization handles this now
	t.ensureCircuitBreaker(backendID, backendName)
}

// CanAttemptHealthCheck checks if a health check should be attempted for a backend
// based on the circuit breaker state. Returns true if the health check should proceed.
//
// If circuit breaker is disabled (nil), always returns true.
// If circuit breaker is open, returns false to skip the health check.
// If circuit breaker is half-open, allows a single test request.
func (t *statusTracker) CanAttemptHealthCheck(backendID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	state, exists := t.states[backendID]
	if !exists {
		return true // Backend not tracked yet, allow health check
	}

	if state.circuitBreaker == nil {
		return true // Circuit breaker disabled
	}

	return state.circuitBreaker.CanAttempt()
}

// GetCircuitBreakerState returns the current circuit breaker state for a backend.
// Returns (state, exists) where exists indicates if the backend has a circuit breaker.
func (t *statusTracker) GetCircuitBreakerState(backendID string) (CircuitState, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	state, exists := t.states[backendID]
	if !exists || state.circuitBreaker == nil {
		return "", false
	}

	return state.circuitBreaker.GetState(), true
}

// IsCircuitOpen returns true if the circuit breaker is in the open state for a backend.
// Returns false if the backend is not tracked or circuit breaker is disabled.
func (t *statusTracker) IsCircuitOpen(backendID string) bool {
	state, exists := t.GetCircuitBreakerState(backendID)
	return exists && state == CircuitOpen
}

// ShouldAttemptHealthCheck determines if a health check should be attempted for a backend.
// This encapsulates circuit breaker logic and provides appropriate logging.
// Returns true if the health check should proceed, false if it should be skipped.
//
// When circuit breaker is enabled, this method:
// - Checks if a health check attempt is allowed based on circuit state
// - Logs the reason when health checks are skipped (OPEN or HALF-OPEN with test in progress)
// - Logs when attempting a recovery test in HALF-OPEN state
//
// Parameters:
//   - backendID: Unique identifier for the backend
//   - backendName: Human-readable name for logging
func (t *statusTracker) ShouldAttemptHealthCheck(backendID, backendName string) bool {
	// Check if circuit breaker allows the attempt
	if !t.CanAttemptHealthCheck(backendID) {
		// CanAttemptHealthCheck returns false in two cases:
		// 1. Circuit is OPEN - completely blocked
		// 2. Circuit is HALF-OPEN but a test is already in progress
		cbState, _ := t.GetCircuitBreakerState(backendID)
		switch cbState {
		case CircuitOpen:
			logger.Debugf("Circuit breaker OPEN for backend %s, skipping health check", backendName)
		case CircuitHalfOpen:
			logger.Debugf("Circuit breaker HALF-OPEN with test in progress for backend %s, skipping health check", backendName)
		case CircuitClosed:
			// This should not happen - circuit is closed but CanAttemptHealthCheck returned false
			logger.Debugf("Circuit breaker state inconsistency for backend %s, skipping health check", backendName)
		}
		return false
	}

	// If we reach here with a half-open circuit, we're attempting the recovery test
	if cbState, exists := t.GetCircuitBreakerState(backendID); exists && cbState == CircuitHalfOpen {
		logger.Debugf("Circuit breaker testing recovery for backend %s", backendName)
	}

	return true
}

// State is an immutable snapshot of a backend's health state.
// This is returned by GetState and GetAllStates to provide thread-safe access
// to health information without holding locks.
type State struct {
	// Status is the current health status.
	Status vmcp.BackendHealthStatus

	// ConsecutiveFailures is the number of consecutive failed health checks.
	ConsecutiveFailures int

	// LastCheckTime is when the last health check was performed.
	LastCheckTime time.Time

	// LastErrorCategory is a sanitized error category for API responses.
	// Values: "authentication_failed", "timeout", "connection_failed", "backend_unavailable", etc.
	// This field is safe to serialize and expose in API responses.
	LastErrorCategory string

	// LastError is the raw error encountered (if any).
	// DEPRECATED: This field may contain sensitive information (paths, URLs, credentials)
	// and should not be serialized to API responses. Use LastErrorCategory instead.
	// The json:"-" tag prevents this field from being included in JSON marshaling.
	LastError error `json:"-"`

	// LastTransitionTime is when the status last changed.
	LastTransitionTime time.Time

	// CircuitState is the current circuit breaker state (empty if circuit breaker disabled).
	CircuitState CircuitState

	// CircuitLastChanged is when the circuit breaker state last changed.
	CircuitLastChanged time.Time
}
