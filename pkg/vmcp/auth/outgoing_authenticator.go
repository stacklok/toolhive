package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
)

// DefaultOutgoingAuthenticator is a thread-safe implementation of OutgoingAuthenticator
// that maintains a registry of authentication strategies.
//
// Thread-safety: Safe for concurrent calls to RegisterStrategy and AuthenticateRequest.
// Strategy implementations must be thread-safe as they are called concurrently.
// It uses sync.RWMutex for thread-safety as HTTP servers are inherently concurrent.
//
// This authenticator supports dynamic registration of strategies and dispatches
// authentication requests to the appropriate strategy based on the strategy name.
//
// Example usage:
//
//	auth := NewDefaultOutgoingAuthenticator()
//	auth.RegisterStrategy("bearer", NewBearerStrategy())
//	err := auth.AuthenticateRequest(ctx, req, "bearer", metadata)
type DefaultOutgoingAuthenticator struct {
	strategies map[string]Strategy
	mu         sync.RWMutex
}

// NewDefaultOutgoingAuthenticator creates a new DefaultOutgoingAuthenticator
// with an empty strategy registry.
//
// Strategies must be registered using RegisterStrategy before they can be used
// for authentication.
func NewDefaultOutgoingAuthenticator() *DefaultOutgoingAuthenticator {
	return &DefaultOutgoingAuthenticator{
		strategies: make(map[string]Strategy),
	}
}

// RegisterStrategy registers a new authentication strategy.
//
// This method is thread-safe and validates that:
//   - name is not empty
//   - strategy is not nil
//   - no strategy is already registered with the same name
//
// Parameters:
//   - name: The unique identifier for this strategy
//   - strategy: The Strategy implementation to register
//
// Returns an error if validation fails or a strategy with the same name
// already exists.
func (a *DefaultOutgoingAuthenticator) RegisterStrategy(name string, strategy Strategy) error {
	if name == "" {
		return errors.New("strategy name cannot be empty")
	}
	if strategy == nil {
		return errors.New("strategy cannot be nil")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if _, exists := a.strategies[name]; exists {
		return fmt.Errorf("strategy %q is already registered", name)
	}

	a.strategies[name] = strategy
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
func (a *DefaultOutgoingAuthenticator) GetStrategy(name string) (Strategy, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	strategy, exists := a.strategies[name]
	if !exists {
		return nil, fmt.Errorf("strategy %q not found", name)
	}

	return strategy, nil
}

// AuthenticateRequest adds authentication to an outgoing backend request.
//
// This method retrieves the specified strategy and delegates authentication
// to it. The strategy modifies the request by adding appropriate headers,
// tokens, or other authentication artifacts.
//
// Parameters:
//   - ctx: Request context (may contain identity for pass-through auth)
//   - req: The HTTP request to authenticate
//   - strategyName: The name of the strategy to use
//   - metadata: Strategy-specific configuration
//
// Returns an error if:
//   - The strategy is not found
//   - The metadata validation fails
//   - The strategy's Authenticate method fails
func (a *DefaultOutgoingAuthenticator) AuthenticateRequest(
	ctx context.Context,
	req *http.Request,
	strategyName string,
	metadata map[string]any,
) error {
	strategy, err := a.GetStrategy(strategyName)
	if err != nil {
		return err
	}

	// Validate metadata before using it
	if err := strategy.Validate(metadata); err != nil {
		return fmt.Errorf("invalid metadata for strategy %q: %w", strategyName, err)
	}

	return strategy.Authenticate(ctx, req, metadata)
}
