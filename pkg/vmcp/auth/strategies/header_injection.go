// Package strategies provides authentication strategy implementations for Virtual MCP Server.
package strategies

import (
	"context"
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive/pkg/validation"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// HeaderInjectionStrategy injects a static header value into request headers.
// This is a general-purpose strategy that can inject any header with any value,
// commonly used for API keys, bearer tokens, or custom authentication headers.
//
// The strategy uses the typed HeaderInjectionConfig from BackendAuthStrategy
// and injects the configured header into the backend request.
//
// Required configuration (in BackendAuthStrategy.HeaderInjection):
//   - HeaderName: The HTTP header name to use (e.g., "X-API-Key", "Authorization")
//   - HeaderValue: The header value to inject (can be an API key, token, or any value)
//     Note: In YAML configuration, use either header_value (literal) or header_value_env (from environment).
//     The value is resolved at config load time and stored in HeaderValue.
//
// This strategy is appropriate when:
//   - The backend requires a static header value for authentication
//   - The header value is stored securely in the vMCP configuration
//   - No dynamic token exchange or user-specific authentication is required
//
// Future enhancements may include:
//   - Support for multiple header formats (e.g., "Bearer <key>")
//   - Value rotation and refresh mechanisms
type HeaderInjectionStrategy struct{}

// NewHeaderInjectionStrategy creates a new HeaderInjectionStrategy instance.
func NewHeaderInjectionStrategy() *HeaderInjectionStrategy {
	return &HeaderInjectionStrategy{}
}

// Name returns the strategy identifier.
func (*HeaderInjectionStrategy) Name() string {
	return StrategyTypeHeaderInjection
}

// Authenticate injects the header value from the typed config into the request header.
//
// This method:
//  1. Validates that HeaderInjection config is present with HeaderName and HeaderValue
//  2. Sets the specified header with the provided value
//
// Parameters:
//   - ctx: Request context (currently unused, reserved for future secret resolution)
//   - req: The HTTP request to authenticate
//   - strategy: The typed BackendAuthStrategy containing HeaderInjection configuration
//
// Returns an error if:
//   - strategy or HeaderInjection config is nil
//   - HeaderName is missing or empty
//   - HeaderValue is missing or empty
func (*HeaderInjectionStrategy) Authenticate(_ context.Context, req *http.Request, strategy *authtypes.BackendAuthStrategy) error {
	if strategy == nil || strategy.HeaderInjection == nil {
		return fmt.Errorf("header_injection configuration required")
	}

	cfg := strategy.HeaderInjection
	if cfg.HeaderName == "" {
		return fmt.Errorf("header_name required in header_injection configuration")
	}

	if cfg.HeaderValue == "" {
		return fmt.Errorf("header_value required in header_injection configuration")
	}

	req.Header.Set(cfg.HeaderName, cfg.HeaderValue)
	return nil
}

// Validate checks if the required configuration fields are present and valid.
//
// This method verifies that:
//   - strategy and HeaderInjection config are not nil
//   - HeaderName is present and non-empty
//   - HeaderValue is present and non-empty
//   - HeaderName is a valid HTTP header name (prevents CRLF injection)
//   - HeaderValue is a valid HTTP header value (prevents CRLF injection)
//
// This validation is typically called during configuration parsing to fail fast
// if the strategy is misconfigured.
func (*HeaderInjectionStrategy) Validate(strategy *authtypes.BackendAuthStrategy) error {
	if strategy == nil || strategy.HeaderInjection == nil {
		return fmt.Errorf("header_injection configuration required")
	}

	cfg := strategy.HeaderInjection
	if cfg.HeaderName == "" {
		return fmt.Errorf("header_name required in header_injection configuration")
	}

	if cfg.HeaderValue == "" {
		return fmt.Errorf("header_value required in header_injection configuration")
	}

	// Validate header name to prevent injection attacks
	if err := validation.ValidateHTTPHeaderName(cfg.HeaderName); err != nil {
		return fmt.Errorf("invalid header_name: %w", err)
	}

	// Validate header value to prevent injection attacks
	if err := validation.ValidateHTTPHeaderValue(cfg.HeaderValue); err != nil {
		return fmt.Errorf("invalid header_value: %w", err)
	}

	return nil
}
