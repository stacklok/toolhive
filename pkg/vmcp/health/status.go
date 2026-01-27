// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
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
}

// statusTracker tracks health status for multiple backends.
// It provides thread-safe access to backend health states and handles
// status transitions with configurable unhealthy thresholds.
type statusTracker struct {
	mu sync.RWMutex

	// states maps backend ID to its health state.
	states map[string]*backendHealthState

	// unhealthyThreshold is the number of consecutive failures before marking unhealthy.
	unhealthyThreshold int
}

// newStatusTracker creates a new status tracker.
//
// Parameters:
//   - unhealthyThreshold: Number of consecutive failures before marking backend unhealthy.
//     Must be >= 1. Recommended: 3 failures.
//
// Returns a new status tracker instance.
func newStatusTracker(unhealthyThreshold int) *statusTracker {
	if unhealthyThreshold < 1 {
		logger.Warnf("Invalid unhealthyThreshold %d (must be >= 1), adjusting to 1", unhealthyThreshold)
		unhealthyThreshold = 1
	}

	return &statusTracker{
		states:             make(map[string]*backendHealthState),
		unhealthyThreshold: unhealthyThreshold,
	}
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
		logger.Debugf("Backend %s remains %s (%d consecutive failures, incoming: %s): %v",
			backendName, state.status, state.consecutiveFailures, status, err)
	} else {
		// Below threshold - accumulating failures but not yet unhealthy
		logger.Debugf("Backend %s health check failed (%d/%d consecutive failures, current status: %s, incoming: %s): %v",
			backendName, state.consecutiveFailures, t.unhealthyThreshold, state.status, status, err)
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

	// Return a copy to avoid race conditions
	return &State{
		Status:              state.status,
		ConsecutiveFailures: state.consecutiveFailures,
		LastCheckTime:       state.lastCheckTime,
		LastError:           state.lastError,
		LastTransitionTime:  state.lastTransitionTime,
	}, true
}

// GetAllStates returns a copy of all backend health states.
// Returns a map of backend ID to State.
func (t *statusTracker) GetAllStates() map[string]*State {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make(map[string]*State, len(t.states))
	for backendID, state := range t.states {
		result[backendID] = &State{
			Status:              state.status,
			ConsecutiveFailures: state.consecutiveFailures,
			LastCheckTime:       state.lastCheckTime,
			LastError:           state.lastError,
			LastTransitionTime:  state.lastTransitionTime,
		}
	}

	return result
}

// IsHealthy returns true if the backend is currently healthy.
// Returns false if the backend is unknown or not tracked.
func (t *statusTracker) IsHealthy(backendID string) bool {
	status, exists := t.GetStatus(backendID)
	return exists && status == vmcp.BackendHealthy
}

// RemoveBackend removes a backend's health state from tracking.
// This is called when a backend is removed from the monitor.
// If the backend doesn't exist, this is a no-op (idempotent).
func (t *statusTracker) RemoveBackend(backendID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.states, backendID)
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

	// LastError is the last error encountered (if any).
	LastError error

	// LastTransitionTime is when the status last changed.
	LastTransitionTime time.Time
}
