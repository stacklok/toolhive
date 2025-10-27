// Package auth provides authentication interfaces for Virtual MCP Server.
//
// This package defines two authentication boundaries:
//  1. IncomingAuthenticator - validates client requests to virtual MCP
//  2. OutgoingAuthenticator - authenticates virtual MCP to backend servers
//
// The package supports extensible authentication strategies through the
// Strategy interface, enabling custom authentication mechanisms to be
// registered at runtime.
package auth

import (
	"context"
	"net/http"
)

// IncomingAuthenticator handles authentication for clients connecting to the virtual MCP server.
// This validates the incoming request and extracts identity information.
//
// The virtual MCP server supports multiple incoming auth strategies:
//   - OIDC: OAuth 2.0 / OpenID Connect
//   - Local: Local authentication (for development)
//   - Anonymous: No authentication required
type IncomingAuthenticator interface {
	// Authenticate validates the incoming HTTP request and returns identity information.
	// Returns an error if authentication fails.
	Authenticate(ctx context.Context, r *http.Request) (*Identity, error)

	// Middleware returns an HTTP middleware that can be applied to routes.
	// This integrates with ToolHive's existing middleware patterns.
	Middleware() func(http.Handler) http.Handler
}

// OutgoingAuthenticator handles authentication to backend MCP servers.
// This is responsible for obtaining and injecting appropriate credentials
// for each backend based on its authentication strategy.
//
// Supported strategies (extensible):
//   - pass_through: Forward client credentials unchanged
//   - token_exchange: RFC 8693 token exchange for backend-specific tokens
//   - client_credentials: OAuth 2.0 client credentials flow
//   - service_account: Static service account credentials
//   - header_injection: Inject static headers
//   - oauth_proxy: Use an OAuth proxy for token management
type OutgoingAuthenticator interface {
	// AuthenticateRequest adds authentication to an outgoing backend request.
	// The strategy and metadata are provided in the BackendTarget.
	//
	// For token exchange, this:
	//   1. Checks the token cache
	//   2. Performs exchange if needed
	//   3. Injects the token into the request
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

// Identity represents an authenticated user or service account.
type Identity struct {
	// Subject is the unique identifier for the principal.
	Subject string

	// Name is the human-readable name.
	Name string

	// Email is the email address (if available).
	Email string

	// Groups are the groups this identity belongs to.
	Groups []string

	// Claims contains additional claims from the auth token.
	Claims map[string]any

	// Token is the original authentication token (for pass-through).
	Token string

	// TokenType is the type of token (e.g., "Bearer", "JWT").
	TokenType string

	// Metadata stores additional identity information.
	Metadata map[string]string
}

// Authorizer handles authorization decisions.
// This integrates with ToolHive's existing Cedar-based authorization.
type Authorizer interface {
	// Authorize checks if an identity is authorized to perform an action on a resource.
	Authorize(ctx context.Context, identity *Identity, action string, resource string) error

	// AuthorizeToolCall checks if an identity can call a specific tool.
	AuthorizeToolCall(ctx context.Context, identity *Identity, toolName string) error

	// AuthorizeResourceAccess checks if an identity can access a specific resource.
	AuthorizeResourceAccess(ctx context.Context, identity *Identity, resourceURI string) error
}
