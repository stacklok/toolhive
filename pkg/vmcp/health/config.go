package health

import (
	"fmt"
	"time"
)

// CircuitState represents the state of a circuit breaker.
type CircuitState int

const (
	// CircuitClosed means the circuit is closed and requests are flowing normally.
	// This maps to BackendHealthy status.
	CircuitClosed CircuitState = iota

	// CircuitOpen means the circuit is open and requests are being fast-failed.
	// This maps to BackendUnhealthy status.
	CircuitOpen

	// CircuitHalfOpen means the circuit is testing if the backend has recovered.
	// This maps to BackendDegraded status.
	CircuitHalfOpen
)

// String representations of circuit states
const (
	CircuitStateClosedStr   = "closed"
	CircuitStateOpenStr     = "open"
	CircuitStateHalfOpenStr = "halfopen"
	CircuitStateUnknownStr  = "unknown"
)

// String returns a human-readable representation of the circuit state.
func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return CircuitStateClosedStr
	case CircuitOpen:
		return CircuitStateOpenStr
	case CircuitHalfOpen:
		return CircuitStateHalfOpenStr
	default:
		return CircuitStateUnknownStr
	}
}

// CircuitBreakerConfig configures circuit breaker behavior.
// Circuit breaker is disabled by default (Enabled: false) for backward compatibility.
type CircuitBreakerConfig struct {
	// Enabled controls whether circuit breaker is active.
	// When false, all circuit breaker logic is skipped.
	Enabled bool

	// FailureThreshold is the number of consecutive failures before opening the circuit.
	// Must be >= 1 when circuit breaker is enabled.
	// Recommended: 5 failures.
	FailureThreshold int

	// Timeout is how long the circuit stays open before transitioning to half-open.
	// Must be > 0 when circuit breaker is enabled.
	// Recommended: 60s.
	Timeout time.Duration
}

// Validate validates circuit breaker configuration.
// Returns nil if configuration is valid or if circuit breaker is disabled.
// Returns an error if required fields are invalid when circuit breaker is enabled.
func (c *CircuitBreakerConfig) Validate() error {
	if c == nil || !c.Enabled {
		return nil
	}

	if c.FailureThreshold < 1 {
		return fmt.Errorf("circuit breaker failure threshold must be >= 1, got %d", c.FailureThreshold)
	}

	if c.Timeout <= 0 {
		return fmt.Errorf("circuit breaker timeout must be positive, got %v", c.Timeout)
	}

	return nil
}
