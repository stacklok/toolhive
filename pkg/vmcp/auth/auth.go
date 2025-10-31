// Package auth provides authentication for Virtual MCP Server.
//
// This package defines:
//   - Identity: Domain type representing an authenticated user/service
//   - Claims → Identity conversion (incoming authentication)
//   - OutgoingAuthenticator: Authenticates vMCP to backend servers
//   - Strategy: Pluggable authentication strategies for backends
//
// Incoming authentication uses pkg/auth.TokenValidator + IdentityMiddleware
// to validate OIDC tokens and convert Claims to Identity.
package auth

//go:generate mockgen -destination=mocks/mock_strategy.go -package=mocks github.com/stacklok/toolhive/pkg/vmcp/auth Strategy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

// Identity represents an authenticated user or service account.
type Identity struct {
	// Subject is the unique identifier for the principal.
	Subject string

	// Name is the human-readable name.
	Name string

	// Email is the email address (if available).
	Email string

	// Groups are the groups this identity belongs to.
	//
	// NOTE: This field is intentionally NOT populated by OIDCIncomingAuthenticator.
	// Authorization logic MUST extract groups from the Claims map, as group claim
	// names vary by provider (e.g., "groups", "roles", "cognito:groups").
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

// String returns a string representation of the Identity with sensitive fields redacted.
// This prevents accidental token leakage when the Identity is logged or printed.
func (i *Identity) String() string {
	if i == nil {
		return "<nil>"
	}

	token := "REDACTED"
	if i.Token == "" {
		token = "<empty>"
	}

	return fmt.Sprintf("Identity{Subject:%q, Name:%q, Email:%q, Groups:%v, Token:%s, TokenType:%q}",
		i.Subject, i.Name, i.Email, i.Groups, token, i.TokenType)
}

// MarshalJSON implements json.Marshaler to redact sensitive fields during JSON serialization.
// This prevents accidental token leakage in structured logs, API responses, or audit logs.
func (i *Identity) MarshalJSON() ([]byte, error) {
	if i == nil {
		return []byte("null"), nil
	}

	// Create a safe representation with lowercase field names and redacted token
	type SafeIdentity struct {
		Subject   string            `json:"subject"`
		Name      string            `json:"name"`
		Email     string            `json:"email"`
		Groups    []string          `json:"groups"`
		Claims    map[string]any    `json:"claims"`
		Token     string            `json:"token"`
		TokenType string            `json:"tokenType"`
		Metadata  map[string]string `json:"metadata"`
	}

	token := i.Token
	if token != "" {
		token = "REDACTED"
	}

	return json.Marshal(&SafeIdentity{
		Subject:   i.Subject,
		Name:      i.Name,
		Email:     i.Email,
		Groups:    i.Groups,
		Claims:    i.Claims,
		Token:     token,
		TokenType: i.TokenType,
		Metadata:  i.Metadata,
	})
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
