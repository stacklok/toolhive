// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package strategies provides authentication strategy implementations for Virtual MCP Server.
package strategies

import (
	"context"
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive/pkg/validation"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
)

// HeaderInjectionStrategy injects a static header value into request headers.
// This is a general-purpose strategy that can inject any header with any value,
// commonly used for API keys, bearer tokens, or custom authentication headers.
//
// The strategy extracts the header name and value from the typed HeaderInjection
// configuration and injects them into the backend request headers.
//
// Required configuration fields (in BackendAuthStrategy.HeaderInjection):
//   - HeaderName: The HTTP header name to use (e.g., "X-API-Key", "Authorization")
//   - HeaderValue: The header value to inject (can be an API key, token, or any value)
//     Note: In YAML configuration, use either header_value (literal) or header_value_env (from environment).
//     The value is resolved at config load time and passed here as HeaderValue.
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
	return authtypes.StrategyTypeHeaderInjection
}

// Authenticate injects the header value from the strategy config into the request header.
//
// This method:
//  1. Skips authentication if this is a health check request
//  2. Validates that HeaderName and HeaderValue are present in the strategy config
//  3. Sets the specified header with the provided value
//
// Parameters:
//   - ctx: Request context (used to check for health check marker)
//   - req: The HTTP request to authenticate
//   - strategy: The backend auth strategy configuration containing HeaderInjection
//
// Returns an error if:
//   - HeaderName is missing or empty
//   - HeaderValue is missing or empty
func (*HeaderInjectionStrategy) Authenticate(
	ctx context.Context, req *http.Request, strategy *authtypes.BackendAuthStrategy,
) error {
	// Skip authentication for health checks
	if health.IsHealthCheck(ctx) {
		return nil
	}

	if strategy == nil || strategy.HeaderInjection == nil {
		return fmt.Errorf("header_injection configuration required")
	}

	headerName := strategy.HeaderInjection.HeaderName
	if headerName == "" {
		return fmt.Errorf("header_name required in configuration")
	}

	headerValue := strategy.HeaderInjection.HeaderValue
	if headerValue == "" {
		return fmt.Errorf("header_value required in configuration")
	}

	req.Header.Set(headerName, headerValue)
	return nil
}

// Validate checks if the required strategy configuration fields are present and valid.
//
// This method verifies that:
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

	headerName := strategy.HeaderInjection.HeaderName
	if headerName == "" {
		return fmt.Errorf("header_name required in configuration")
	}

	headerValue := strategy.HeaderInjection.HeaderValue
	if headerValue == "" {
		return fmt.Errorf("header_value required in configuration")
	}

	// Validate header name to prevent injection attacks
	if err := validation.ValidateHTTPHeaderName(headerName); err != nil {
		return fmt.Errorf("invalid header_name: %w", err)
	}

	// Validate header value to prevent injection attacks
	if err := validation.ValidateHTTPHeaderValue(headerValue); err != nil {
		return fmt.Errorf("invalid header_value: %w", err)
	}

	return nil
}
