// Package strategies provides authentication strategy implementations for Virtual MCP Server.
package strategies

import (
	"context"
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive/pkg/validation"
)

// HeaderInjectionStrategy injects a static header value into request headers.
// This is a general-purpose strategy that can inject any header with any value,
// commonly used for API keys, bearer tokens, or custom authentication headers.
//
// The strategy extracts the header name and value from the metadata
// configuration and injects them into the backend request headers.
//
// Required metadata fields:
//   - header_name: The HTTP header name to use (e.g., "X-API-Key", "Authorization")
//   - api_key: The header value to inject (can be an API key, token, or any value)
//
// This strategy is appropriate when:
//   - The backend requires a static header value for authentication
//   - The header value is stored securely in the vMCP configuration
//   - No dynamic token exchange or user-specific authentication is required
//
// Future enhancements may include:
//   - Secret reference resolution (e.g., ${SECRET_REF:...})
//   - Support for multiple header formats (e.g., "Bearer <key>")
//   - Value rotation and refresh mechanisms
type HeaderInjectionStrategy struct{}

// NewHeaderInjectionStrategy creates a new HeaderInjectionStrategy instance.
func NewHeaderInjectionStrategy() *HeaderInjectionStrategy {
	return &HeaderInjectionStrategy{}
}

// Name returns the strategy identifier.
func (*HeaderInjectionStrategy) Name() string {
	return "header_injection"
}

// Authenticate injects the header value from metadata into the request header.
//
// This method:
//  1. Validates that header_name and api_key are present in metadata
//  2. Sets the specified header with the provided value
//
// Parameters:
//   - ctx: Request context (currently unused, reserved for future secret resolution)
//   - req: The HTTP request to authenticate
//   - metadata: Strategy-specific configuration containing header_name and api_key
//
// Returns an error if:
//   - header_name is missing or empty
//   - api_key is missing or empty
func (*HeaderInjectionStrategy) Authenticate(_ context.Context, req *http.Request, metadata map[string]any) error {
	headerName, ok := metadata["header_name"].(string)
	if !ok || headerName == "" {
		return fmt.Errorf("header_name required in metadata")
	}

	apiKey, ok := metadata["api_key"].(string)
	if !ok || apiKey == "" {
		return fmt.Errorf("api_key required in metadata")
	}

	// TODO: Future enhancement - resolve secret references
	// if strings.HasPrefix(apiKey, "${SECRET_REF:") {
	//     apiKey, err = s.secretResolver.Resolve(ctx, apiKey)
	//     if err != nil {
	//         return fmt.Errorf("failed to resolve secret reference: %w", err)
	//     }
	// }

	req.Header.Set(headerName, apiKey)
	return nil
}

// Validate checks if the required metadata fields are present and valid.
//
// This method verifies that:
//   - header_name is present and non-empty
//   - api_key is present and non-empty
//   - header_name is a valid HTTP header name (prevents CRLF injection)
//   - api_key is a valid HTTP header value (prevents CRLF injection)
//
// This validation is typically called during configuration parsing to fail fast
// if the strategy is misconfigured.
func (*HeaderInjectionStrategy) Validate(metadata map[string]any) error {
	headerName, ok := metadata["header_name"].(string)
	if !ok || headerName == "" {
		return fmt.Errorf("header_name required in metadata")
	}

	apiKey, ok := metadata["api_key"].(string)
	if !ok || apiKey == "" {
		return fmt.Errorf("api_key required in metadata")
	}

	// Validate header name to prevent injection attacks
	if err := validation.ValidateHTTPHeaderName(headerName); err != nil {
		return fmt.Errorf("invalid header_name: %w", err)
	}

	// Validate API key value to prevent injection attacks
	if err := validation.ValidateHTTPHeaderValue(apiKey); err != nil {
		return fmt.Errorf("invalid api_key: %w", err)
	}

	return nil
}
