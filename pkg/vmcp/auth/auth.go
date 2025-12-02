// Package auth provides authentication for Virtual MCP Server.
//
// This package defines:
//   - OutgoingAuthRegistry: Registry for managing backend authentication strategies
//   - Strategy: Pluggable authentication strategies for backends
//
// Incoming authentication uses pkg/auth middleware (OIDC, local, anonymous)
// which directly creates pkg/auth.Identity in context.
package auth

//go:generate mockgen -destination=mocks/mock_strategy.go -package=mocks github.com/stacklok/toolhive/pkg/vmcp/auth Strategy

import (
	"context"
	"net/http"

	"github.com/stacklok/toolhive/pkg/auth"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// OutgoingAuthRegistry manages authentication strategies for outgoing requests to backend MCP servers.
// This is a registry that stores and retrieves Strategy implementations.
//
// The registry supports dynamic strategy registration, allowing custom authentication
// strategies to be added at runtime. Once registered, strategies can be retrieved
// by name and used to authenticate requests to backends.
//
// Responsibilities:
//   - Maintain registry of available strategies
//   - Retrieve strategies by name
//   - Register new strategies dynamically
//
// This registry does NOT perform authentication itself. Authentication is performed
// by Strategy implementations retrieved from this registry.
//
// Usage Pattern:
//  1. Register strategies during application initialization
//  2. Resolve strategy once at client creation time (cold path)
//  3. Call strategy.Authenticate() directly per-request (hot path)
//
// Thread-safety: Implementations must be safe for concurrent access.
type OutgoingAuthRegistry interface {
	// GetStrategy retrieves an authentication strategy by name.
	// Returns an error if the strategy is not found.
	GetStrategy(name string) (Strategy, error)

	// RegisterStrategy registers a new authentication strategy.
	// The strategy name must match the name returned by strategy.Name().
	// Returns an error if:
	//   - name is empty
	//   - strategy is nil
	//   - a strategy with the same name is already registered
	//   - strategy.Name() does not match the registration name
	RegisterStrategy(name string, strategy Strategy) error
}

// Strategy defines how to authenticate to a backend.
// This interface enables pluggable authentication strategies.
type Strategy interface {
	// Name returns the strategy identifier.
	Name() string

	// Authenticate performs authentication and modifies the request.
	// The strategy parameter contains strategy-specific configuration.
	Authenticate(ctx context.Context, req *http.Request, strategy *authtypes.BackendAuthStrategy) error

	// Validate checks if the strategy configuration is valid.
	Validate(strategy *authtypes.BackendAuthStrategy) error
}

// Authorizer handles authorization decisions.
// This integrates with ToolHive's existing Cedar-based authorization.
type Authorizer interface {
	// Authorize checks if an identity is authorized to perform an action on a resource.
	Authorize(ctx context.Context, identity *auth.Identity, action string, resource string) error

	// AuthorizeToolCall checks if an identity can call a specific tool.
	AuthorizeToolCall(ctx context.Context, identity *auth.Identity, toolName string) error

	// AuthorizeResourceAccess checks if an identity can access a specific resource.
	AuthorizeResourceAccess(ctx context.Context, identity *auth.Identity, resourceURI string) error
}
