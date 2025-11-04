// Package auth provides authentication for Virtual MCP Server.
//
// This package defines:
//   - OutgoingAuthenticator: Authenticates vMCP to backend servers
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
)

// OutgoingAuthenticator handles authentication to backend MCP servers.
// This is responsible for obtaining and injecting appropriate credentials
// for each backend based on its authentication strategy.
//
// The specific authentication strategies and their behavior will be defined
// during implementation based on the design decisions documented in the
// Virtual MCP Server proposal.
type OutgoingAuthenticator interface {
	// AuthenticateRequest adds authentication to an outgoing backend request.
	// The strategy and metadata are provided in the BackendTarget.
	AuthenticateRequest(ctx context.Context, req *http.Request, strategy string, metadata map[string]any) error

	// GetStrategy returns the authentication strategy handler for a given strategy name.
	// This enables extensibility - new strategies can be registered.
	GetStrategy(name string) (Strategy, error)

	// RegisterStrategy registers a new authentication strategy.
	// This allows custom auth strategies to be added at runtime.
	RegisterStrategy(name string, strategy Strategy) error
}

// Strategy defines how to authenticate to a backend.
// This interface enables pluggable authentication strategies.
type Strategy interface {
	// Name returns the strategy identifier.
	Name() string

	// Authenticate performs authentication and modifies the request.
	// The metadata contains strategy-specific configuration.
	Authenticate(ctx context.Context, req *http.Request, metadata map[string]any) error

	// Validate checks if the strategy configuration is valid.
	Validate(metadata map[string]any) error
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
