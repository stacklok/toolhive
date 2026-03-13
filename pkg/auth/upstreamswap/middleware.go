// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package upstreamswap provides middleware for exchanging embedded auth server
// access tokens with upstream IdP tokens.
package upstreamswap

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// MiddlewareType is the type identifier for upstream swap middleware.
const MiddlewareType = "upstreamswap"

// Header injection strategy constants
const (
	// HeaderStrategyReplace replaces the Authorization header with the upstream token.
	HeaderStrategyReplace = "replace"
	// HeaderStrategyCustom adds the upstream token to a custom header.
	HeaderStrategyCustom = "custom"
)

// Config holds configuration for upstream swap middleware.
type Config struct {
	// HeaderStrategy determines how to inject the token: "replace" (default) or "custom".
	HeaderStrategy string `json:"header_strategy,omitempty" yaml:"header_strategy,omitempty"`

	// CustomHeaderName is the header name when HeaderStrategy is "custom".
	CustomHeaderName string `json:"custom_header_name,omitempty" yaml:"custom_header_name,omitempty"`
}

// MiddlewareParams represents the JSON parameters for the middleware factory.
type MiddlewareParams struct {
	Config *Config `json:"config,omitempty"`
}

// ServiceGetter is a function that returns an upstream token service.
// It returns nil when the service is unavailable (e.g., auth server not configured).
type ServiceGetter func() upstreamtoken.Service

// Middleware wraps the upstream swap middleware functionality.
type Middleware struct {
	middleware types.MiddlewareFunction
}

// Handler returns the middleware function used by the proxy.
func (m *Middleware) Handler() types.MiddlewareFunction {
	return m.middleware
}

// Close cleans up any resources used by the middleware.
func (*Middleware) Close() error {
	return nil
}

// CreateMiddleware is the factory function for upstream swap middleware.
func CreateMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	var params MiddlewareParams
	if err := json.Unmarshal(config.Parameters, &params); err != nil {
		return fmt.Errorf("failed to unmarshal upstream swap middleware parameters: %w", err)
	}

	// Config is optional; use defaults if not provided
	cfg := params.Config
	if cfg == nil {
		cfg = &Config{}
	}

	// Validate configuration
	if err := validateConfig(cfg); err != nil {
		return fmt.Errorf("invalid upstream swap configuration: %w", err)
	}

	// Get the lazy service accessor from the runner.
	serviceGetter := ServiceGetter(runner.GetUpstreamTokenService())

	middleware := createMiddlewareFunc(cfg, serviceGetter)

	upstreamSwapMw := &Middleware{
		middleware: middleware,
	}

	runner.AddMiddleware(config.Type, upstreamSwapMw)
	return nil
}

// validateConfig validates the upstream swap configuration.
func validateConfig(cfg *Config) error {
	// Validate header strategy
	if cfg.HeaderStrategy != "" &&
		cfg.HeaderStrategy != HeaderStrategyReplace &&
		cfg.HeaderStrategy != HeaderStrategyCustom {
		return fmt.Errorf("invalid header_strategy: %s (valid values: '%s', '%s')",
			cfg.HeaderStrategy, HeaderStrategyReplace, HeaderStrategyCustom)
	}

	// Custom header name is required when using custom strategy
	if cfg.HeaderStrategy == HeaderStrategyCustom && cfg.CustomHeaderName == "" {
		return fmt.Errorf("custom_header_name must be specified when header_strategy is '%s'", HeaderStrategyCustom)
	}

	return nil
}

// writeUpstreamAuthRequired writes a 401 response with a WWW-Authenticate Bearer
// challenge per RFC 6750 Section 3.1, signalling that the caller must re-authenticate
// with the upstream IdP.
func writeUpstreamAuthRequired(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate",
		`Bearer error="invalid_token", error_description="upstream token is no longer valid; re-authentication required"`)
	http.Error(w, "upstream authentication required", http.StatusUnauthorized)
}

// injectionFunc is a function that injects a token into an HTTP request.
type injectionFunc func(*http.Request, string)

// createReplaceInjector creates an injection function that replaces the Authorization header.
func createReplaceInjector() injectionFunc {
	return func(r *http.Request, token string) {
		r.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	}
}

// createCustomInjector creates an injection function that adds the token to a custom header.
func createCustomInjector(headerName string) injectionFunc {
	return func(r *http.Request, token string) {
		r.Header.Set(headerName, fmt.Sprintf("Bearer %s", token))
	}
}

// createMiddlewareFunc creates the actual middleware function.
func createMiddlewareFunc(cfg *Config, serviceGetter ServiceGetter) types.MiddlewareFunction {
	// Determine injection strategy at startup time
	strategy := cfg.HeaderStrategy
	if strategy == "" {
		strategy = HeaderStrategyReplace
	}

	var injectToken injectionFunc
	switch strategy {
	case HeaderStrategyReplace:
		injectToken = createReplaceInjector()
	case HeaderStrategyCustom:
		injectToken = createCustomInjector(cfg.CustomHeaderName)
	default:
		// This shouldn't happen due to validation, but default to replace
		injectToken = createReplaceInjector()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 1. Get identity from auth middleware
			identity, ok := auth.IdentityFromContext(r.Context())
			if !ok {
				slog.Debug("No identity in context, proceeding without swap",
					"middleware", "upstreamswap")
				next.ServeHTTP(w, r)
				return
			}

			// 2. Extract tsid from claims
			tsid, ok := identity.Claims[upstreamtoken.TokenSessionIDClaimKey].(string)
			if !ok || tsid == "" {
				slog.Debug("No tsid claim in identity, proceeding without swap",
					"middleware", "upstreamswap")
				next.ServeHTTP(w, r)
				return
			}

			// 3. Get token service — fail closed if unavailable.
			// The tsid claim confirms this request expects upstream token injection;
			// passing through with the original JWT would leak it to the backend.
			svc := serviceGetter()
			if svc == nil {
				slog.Warn("Token service unavailable, cannot perform required upstream swap",
					"middleware", "upstreamswap")
				http.Error(w, "authentication service temporarily unavailable", http.StatusServiceUnavailable)
				return
			}

			// 4. Get valid upstream tokens (with transparent refresh)
			cred, err := svc.GetValidTokens(r.Context(), tsid)
			if err != nil {
				slog.Warn("Failed to get upstream tokens",
					"middleware", "upstreamswap", "error", err)

				// Client-attributable errors require re-authentication.
				if errors.Is(err, upstreamtoken.ErrSessionNotFound) ||
					errors.Is(err, upstreamtoken.ErrNoRefreshToken) ||
					errors.Is(err, upstreamtoken.ErrRefreshFailed) ||
					errors.Is(err, upstreamtoken.ErrInvalidBinding) {
					writeUpstreamAuthRequired(w)
					return
				}
				// Other errors: fail closed to avoid bypassing the token swap
				http.Error(w, "authentication service temporarily unavailable", http.StatusServiceUnavailable)
				return
			}

			// 5. Inject access token — fail closed if empty to prevent bypassing the swap
			if cred.AccessToken == "" {
				slog.Warn("Upstream token service returned empty access token",
					"middleware", "upstreamswap")
				http.Error(w, "authentication service temporarily unavailable", http.StatusServiceUnavailable)
				return
			}

			injectToken(r, cred.AccessToken)
			slog.Debug("Injected upstream access token",
				"middleware", "upstreamswap")

			next.ServeHTTP(w, r)
		})
	}
}
