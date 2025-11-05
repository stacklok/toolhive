package auth

import (
	"errors"
	"fmt"
	"sync"
)

// DefaultOutgoingAuthRegistry is a thread-safe implementation of OutgoingAuthRegistry
// that maintains a registry of authentication strategies.
//
// Thread-safety: Safe for concurrent calls to RegisterStrategy and GetStrategy.
// Strategy implementations must be thread-safe as they are called concurrently.
// It uses sync.RWMutex for thread-safety as HTTP servers are inherently concurrent.
//
// This registry supports dynamic registration of strategies and retrieval by name.
// It does not perform authentication itself - that is done by the Strategy implementations.
//
// Example usage:
//
//	registry := NewDefaultOutgoingAuthRegistry()
//	registry.RegisterStrategy("header_injection", NewHeaderInjectionStrategy())
//	strategy, err := registry.GetStrategy("header_injection")
//	if err == nil {
//	    err = strategy.Authenticate(ctx, req, metadata)
//	}
type DefaultOutgoingAuthRegistry struct {
	strategies map[string]Strategy
	mu         sync.RWMutex
}

// NewDefaultOutgoingAuthRegistry creates a new DefaultOutgoingAuthRegistry
// with an empty strategy registry.
//
// Strategies must be registered using RegisterStrategy before they can be used
// for authentication.
func NewDefaultOutgoingAuthRegistry() *DefaultOutgoingAuthRegistry {
	return &DefaultOutgoingAuthRegistry{
		strategies: make(map[string]Strategy),
	}
}

// RegisterStrategy registers a new authentication strategy.
//
// This method is thread-safe and validates that:
//   - name is not empty
//   - strategy is not nil
//   - strategy.Name() matches the registration name
//   - no strategy is already registered with the same name
//
// Parameters:
//   - name: The unique identifier for this strategy
//   - strategy: The Strategy implementation to register
//
// Returns an error if validation fails or a strategy with the same name
// already exists.
func (r *DefaultOutgoingAuthRegistry) RegisterStrategy(name string, strategy Strategy) error {
	if name == "" {
		return errors.New("strategy name cannot be empty")
	}
	if strategy == nil {
		return errors.New("strategy cannot be nil")
	}

	// Validate that strategy name matches registration name
	if name != strategy.Name() {
		return fmt.Errorf("strategy name mismatch: registered as %q but strategy.Name() returns %q",
			name, strategy.Name())
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.strategies[name]; exists {
		return fmt.Errorf("strategy %q is already registered", name)
	}

	r.strategies[name] = strategy
	return nil
}

// GetStrategy retrieves an authentication strategy by name.
//
// This method is thread-safe for concurrent reads. It returns the strategy
// if found, or an error if no strategy is registered with the given name.
//
// Parameters:
//   - name: The identifier of the strategy to retrieve
//
// Returns:
//   - Strategy: The registered strategy
//   - error: An error if the strategy is not found
func (r *DefaultOutgoingAuthRegistry) GetStrategy(name string) (Strategy, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	strategy, exists := r.strategies[name]
	if !exists {
		return nil, fmt.Errorf("strategy %q not found", name)
	}

	return strategy, nil
}
