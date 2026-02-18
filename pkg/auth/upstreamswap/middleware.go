// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package upstreamswap provides middleware for exchanging embedded auth server
// access tokens with upstream IdP tokens.
package upstreamswap

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
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

// StorageGetter is a function that returns upstream token storage.
// This allows lazy access to the storage, which may not be available at middleware creation time.
type StorageGetter func() storage.UpstreamTokenStorage

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

	// Get storage getter from runner.
	// The storage getter is a lazy accessor that checks storage availability at request time,
	// so it's always non-nil. Actual storage availability is verified when processing requests.
	storageGetter := runner.GetUpstreamTokenStorage()

	middleware := createMiddlewareFunc(cfg, storageGetter)

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
func createMiddlewareFunc(cfg *Config, storageGetter StorageGetter) types.MiddlewareFunction {
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
			tsid, ok := identity.Claims[session.TokenSessionIDClaimKey].(string)
			if !ok || tsid == "" {
				slog.Debug("No tsid claim in identity, proceeding without swap",
					"middleware", "upstreamswap")
				next.ServeHTTP(w, r)
				return
			}

			// 3. Get storage
			stor := storageGetter()
			if stor == nil {
				slog.Warn("Storage unavailable, proceeding without swap",
					"middleware", "upstreamswap")
				next.ServeHTTP(w, r)
				return
			}

			// 4. Lookup upstream tokens
			tokens, err := stor.GetUpstreamTokens(r.Context(), tsid)
			if err != nil {
				slog.Warn("Failed to get upstream tokens",
					"middleware", "upstreamswap", "error", err)
				next.ServeHTTP(w, r)
				return
			}

			// 5. Check if expired (MVP: just log warning, continue with token)
			if tokens.IsExpired(time.Now()) {
				slog.Warn("Upstream tokens expired",
					"middleware", "upstreamswap")
				// Continue with expired token - backend will reject if needed
			}

			// 6. Inject access token
			if tokens.AccessToken == "" {
				slog.Warn("Access token is empty",
					"middleware", "upstreamswap")
				next.ServeHTTP(w, r)
				return
			}

			injectToken(r, tokens.AccessToken)
			slog.Debug("Injected upstream access token",
				"middleware", "upstreamswap")

			next.ServeHTTP(w, r)
		})
	}
}
