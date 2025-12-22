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

	// Circuit breaker fields (only used when circuit breaker is enabled)

	// circuitState is the current circuit breaker state (closed, open, halfopen).
	circuitState CircuitState

	// circuitOpenTime is when the circuit was opened (zero if not open).
	circuitOpenTime time.Time

	// circuitFailures is the number of failures counted for circuit breaker logic.
	// This is separate from consecutiveFailures which is used for unhealthy threshold.
	circuitFailures int
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

	// Circuit breaker configuration (nil if disabled)

	// circuitBreakerEnabled indicates if circuit breaker is active.
	circuitBreakerEnabled bool

	// circuitBreakerThreshold is the number of failures before opening the circuit.
	circuitBreakerThreshold int

	// circuitBreakerTimeout is how long the circuit stays open before transitioning to half-open.
	circuitBreakerTimeout time.Duration
}

// newStatusTracker creates a new status tracker.
//
// Parameters:
//   - unhealthyThreshold: Number of consecutive failures before marking backend unhealthy.
//     Must be >= 1. Recommended: 3 failures.
//   - cbConfig: Optional circuit breaker configuration. If nil or disabled, circuit breaker is not used.
//
// Returns a new status tracker instance.
func newStatusTracker(unhealthyThreshold int, cbConfig *CircuitBreakerConfig) *statusTracker {
	if unhealthyThreshold < 1 {
		logger.Warnf("Invalid unhealthyThreshold %d (must be >= 1), adjusting to 1", unhealthyThreshold)
		unhealthyThreshold = 1
	}

	tracker := &statusTracker{
		states:             make(map[string]*backendHealthState),
		unhealthyThreshold: unhealthyThreshold,
	}

	// Configure circuit breaker if provided and enabled
	if cbConfig != nil && cbConfig.Enabled {
		tracker.circuitBreakerEnabled = true
		tracker.circuitBreakerThreshold = cbConfig.FailureThreshold
		tracker.circuitBreakerTimeout = cbConfig.Timeout
		logger.Infof("Circuit breaker enabled (threshold: %d, timeout: %v)",
			cbConfig.FailureThreshold, cbConfig.Timeout)
	}

	return tracker
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
			// Circuit breaker fields
			circuitState:    CircuitClosed,
			circuitOpenTime: time.Time{},
			circuitFailures: 0,
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

	// Update circuit breaker state after recording success
	t.updateCircuitState(backendID, backendName, state, true)
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
			// Circuit breaker fields
			circuitState:    CircuitClosed,
			circuitOpenTime: time.Time{},
			circuitFailures: 0, // Will be incremented by updateCircuitState
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

		// Update circuit breaker state for initial failure
		t.updateCircuitState(backendID, backendName, state, false)
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

	// Update circuit breaker state after recording failure
	t.updateCircuitState(backendID, backendName, state, false)
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

	// Build circuit breaker state if enabled
	var cbState *CircuitBreakerState
	if t.circuitBreakerEnabled {
		cbState = &CircuitBreakerState{
			State:        state.circuitState.String(),
			OpenTime:     state.circuitOpenTime,
			FailureCount: state.circuitFailures,
		}
	}

	// Return a copy to avoid race conditions
	return &State{
		Status:              state.status,
		ConsecutiveFailures: state.consecutiveFailures,
		LastCheckTime:       state.lastCheckTime,
		LastError:           state.lastError,
		LastTransitionTime:  state.lastTransitionTime,
		CircuitBreakerState: cbState,
	}, true
}

// GetAllStates returns a copy of all backend health states.
// Returns a map of backend ID to State.
func (t *statusTracker) GetAllStates() map[string]*State {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make(map[string]*State, len(t.states))
	for backendID, state := range t.states {
		// Build circuit breaker state if enabled
		var cbState *CircuitBreakerState
		if t.circuitBreakerEnabled {
			cbState = &CircuitBreakerState{
				State:        state.circuitState.String(),
				OpenTime:     state.circuitOpenTime,
				FailureCount: state.circuitFailures,
			}
		}

		result[backendID] = &State{
			Status:              state.status,
			ConsecutiveFailures: state.consecutiveFailures,
			LastCheckTime:       state.lastCheckTime,
			LastError:           state.lastError,
			LastTransitionTime:  state.lastTransitionTime,
			CircuitBreakerState: cbState,
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

// Circuit breaker state machine methods

// isCircuitOpen returns true if the circuit is currently open for the given backend.
// This method is thread-safe and returns false if circuit breaker is disabled or backend not found.
func (t *statusTracker) isCircuitOpen(backendID string) bool {
	if !t.circuitBreakerEnabled {
		return false
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	state, exists := t.states[backendID]
	if !exists {
		return false
	}

	return state.circuitState == CircuitOpen
}

// checkHalfOpenTransition checks if a backend's circuit should transition from open to half-open.
// This is called before performing a health check to allow testing recovery after timeout.
// This method is thread-safe and does nothing if circuit breaker is disabled.
func (t *statusTracker) checkHalfOpenTransition(backendID string) {
	if !t.circuitBreakerEnabled {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	state, exists := t.states[backendID]
	if !exists {
		return
	}

	// Only transition if circuit is open and timeout has elapsed
	if state.circuitState == CircuitOpen {
		if time.Since(state.circuitOpenTime) >= t.circuitBreakerTimeout {
			logger.Infof("Circuit breaker for backend %s transitioning: open → half-open (timeout elapsed)",
				backendID)
			state.circuitState = CircuitHalfOpen
			state.status = vmcp.BackendDegraded
			state.lastTransitionTime = time.Now()
		}
	}
}

// shouldOpenCircuit checks if the circuit should transition to open based on failure count.
// Must be called with lock held.
func (t *statusTracker) shouldOpenCircuit(state *backendHealthState) bool {
	if !t.circuitBreakerEnabled {
		return false
	}

	return state.circuitFailures >= t.circuitBreakerThreshold
}

// updateCircuitState handles circuit breaker state transitions after a health check.
// This implements the circuit breaker state machine:
//   - Closed → Open: After threshold failures
//   - Open → HalfOpen: After timeout (handled by checkHalfOpenTransition)
//   - HalfOpen → Closed: On successful health check
//   - HalfOpen → Open: On failed health check
//
// Must be called with lock held. Does nothing if circuit breaker is disabled.
func (t *statusTracker) updateCircuitState(_ string, backendName string, state *backendHealthState, success bool) {
	if !t.circuitBreakerEnabled {
		return
	}

	previousCircuitState := state.circuitState

	if success {
		// Success: reset circuit failures
		state.circuitFailures = 0

		// Handle state transitions based on current circuit state
		switch state.circuitState {
		case CircuitHalfOpen:
			// Recovery successful: close the circuit
			logger.Infof("Circuit breaker for backend %s: half-open → closed (recovery confirmed)",
				backendName)
			state.circuitState = CircuitClosed
			// Status will be set by RecordSuccess based on recovery logic

		case CircuitOpen:
			// This shouldn't happen (open circuits skip health checks via fast-fail)
			// But handle it defensively
			logger.Warnf("Circuit breaker for backend %s: unexpected success while open, closing circuit",
				backendName)
			state.circuitState = CircuitClosed

		case CircuitClosed:
			// Already closed, stay closed
			// No logging needed - this is the normal case
		}
	} else {
		// Failure: increment circuit failure count
		state.circuitFailures++

		// Handle state transitions based on current circuit state
		switch state.circuitState {
		case CircuitClosed:
			// Check if we should open the circuit
			if t.shouldOpenCircuit(state) {
				logger.Warnf("Circuit breaker for backend %s: closed → open (%d failures, threshold: %d)",
					backendName, state.circuitFailures, t.circuitBreakerThreshold)
				state.circuitState = CircuitOpen
				state.circuitOpenTime = time.Now()
				state.status = vmcp.BackendUnhealthy
				state.lastTransitionTime = time.Now()
			}

		case CircuitHalfOpen:
			// Recovery failed: reopen the circuit
			logger.Warnf("Circuit breaker for backend %s: half-open → open (recovery failed)",
				backendName)
			state.circuitState = CircuitOpen
			state.circuitOpenTime = time.Now()
			state.status = vmcp.BackendUnhealthy
			state.lastTransitionTime = time.Now()

		case CircuitOpen:
			// Already open, stay open
			// This shouldn't happen (open circuits skip health checks via fast-fail)
			logger.Debugf("Circuit breaker for backend %s: remains open (%d failures)",
				backendName, state.circuitFailures)
		}
	}

	// Log circuit state changes for observability
	if previousCircuitState != state.circuitState {
		logger.Infof("Backend %s circuit state changed: %s → %s",
			backendName, previousCircuitState.String(), state.circuitState.String())
	}
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

	// CircuitBreakerState contains circuit breaker information (nil if circuit breaker is disabled).
	CircuitBreakerState *CircuitBreakerState
}

// CircuitBreakerState contains circuit breaker state information for a backend.
// This is included in State when circuit breaker is enabled.
type CircuitBreakerState struct {
	// State is the current circuit state (closed, open, halfopen).
	State string

	// OpenTime is when the circuit was opened (zero if not currently open).
	OpenTime time.Time

	// FailureCount is the number of failures counted for circuit breaker logic.
	FailureCount int
}
