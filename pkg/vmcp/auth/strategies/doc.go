// Package strategies provides authentication strategy implementations for
// Virtual MCP Server backend authentication.
//
// This package contains concrete implementations of the auth.Strategy interface
// for different authentication methods that a Virtual MCP Server can use to
// authenticate with backend MCP servers.
//
// # Available Strategies
//
// # PassThroughStrategy
//
// Forwards the client's authentication token to the backend without modification.
// This is the simplest strategy and is appropriate when the backend and vMCP server
// share the same identity provider and trust relationship.
//
// Use when:
//   - Backend and vMCP server share the same identity provider
//   - Backend can validate the same token format (e.g., JWT from same issuer)
//   - No token transformation or exchange is required
//
// Configuration: No metadata required.
//
// Example:
//
//	strategy := strategies.NewPassThroughStrategy()
//	authenticator.RegisterStrategy(strategy.Name(), strategy)
//
// # HeaderInjectionStrategy
//
// Injects a static header value into request headers. This is a general-purpose
// strategy that can inject any header with any value, commonly used for API keys,
// bearer tokens, or custom authentication headers.
//
// Use when:
//   - Backend requires a static header value for authentication
//   - The header value is stored securely in the vMCP configuration
//   - No dynamic token exchange or user-specific authentication is required
//
// Configuration metadata:
//   - header_name (required): HTTP header name (e.g., "X-API-Key", "Authorization")
//   - api_key (required): The header value to inject (can be an API key, token, or any value)
//
// Example:
//
//	strategy := strategies.NewHeaderInjectionStrategy()
//	authenticator.RegisterStrategy(strategy.Name(), strategy)
//
//	metadata := map[string]any{
//	    "header_name": "X-API-Key",
//	    "api_key": "secret-key-123",
//	}
//
// # TokenExchangeStrategy
//
// Exchanges the client's token for a backend-specific token using OAuth 2.0
// Token Exchange (RFC 8693). This strategy is used when the backend uses a
// different identity provider than the vMCP server.
//
// Use when:
//   - Backend uses a different identity provider than vMCP server
//   - Token exchange relationships are configured between identity providers
//   - Per-user token exchange is required (not static credentials)
//
// Configuration metadata:
//   - token_url (required): OAuth 2.0 token endpoint URL for token exchange
//   - client_id (optional): OAuth 2.0 client identifier
//   - client_secret (optional): OAuth 2.0 client secret (requires client_id)
//   - audience (optional): Target audience for the exchanged token
//   - scopes (optional): Array of scope strings to request
//   - subject_token_type (optional): Type of subject token ("access_token", "id_token", "jwt")
//
// Example:
//
//	strategy := strategies.NewTokenExchangeStrategy()
//	authenticator.RegisterStrategy(strategy.Name(), strategy)
//
//	metadata := map[string]any{
//	    "token_url": "https://auth.example.com/token",
//	    "audience": "https://backend.example.com",
//	    "scopes": []string{"read", "write"},
//	}
//
// # Architecture
//
// All strategies implement the auth.Strategy interface:
//
//	type Strategy interface {
//	    Name() string
//	    Authenticate(ctx context.Context, req *http.Request, metadata map[string]any) error
//	    Validate(metadata map[string]any) error
//	}
//
// The strategies are designed to be:
//   - Pluggable: Register new strategies at runtime via OutgoingAuthenticator
//   - Stateless: Can be safely shared across goroutines (except TokenExchangeStrategy caching)
//   - Testable: Clean interfaces enable comprehensive testing
//   - Extensible: New strategies can be added without modifying existing code
//
// # Thread Safety
//
// All strategies are safe for concurrent use:
//   - PassThroughStrategy: Fully stateless, no shared state
//   - HeaderInjectionStrategy: Fully stateless, no shared state
//   - TokenExchangeStrategy: Uses sync.RWMutex for cache access
//
// # Error Handling
//
// All strategies follow consistent error handling patterns:
//   - Configuration errors: Returned during Validate() to fail fast
//   - Authentication errors: Returned during Authenticate() with context
//   - Errors are wrapped with fmt.Errorf for error chain inspection
//
// # Future Enhancements
//
// Potential future strategy implementations:
//   - mTLS authentication
//   - HMAC signature-based authentication
//   - Custom authentication protocols
//   - Secret reference resolution for HeaderInjectionStrategy
//   - Token refresh and rotation for TokenExchangeStrategy
package strategies
