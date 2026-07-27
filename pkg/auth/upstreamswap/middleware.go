// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package upstreamswap provides middleware for exchanging embedded auth server
// access tokens with upstream IdP tokens.
package upstreamswap

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/stacklok/toolhive/pkg/auth"
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

	// ProviderName identifies which upstream provider's tokens to retrieve for injection.
	// This is required and must match a configured upstream provider name.
	ProviderName string `json:"provider_name" yaml:"provider_name"`

	// AuthorizeURL is the ToolHive authorization server's authorize-endpoint URL
	// ({issuer}/oauth/authorize). When set, the 401 consent response includes it
	// so clients can direct the user to consent. It is only an endpoint: the
	// client merges its own client_id, redirect_uri, and PKCE parameters. It
	// must never be derived from the request Host header (attacker-controlled).
	// Optional; when empty the 401 body omits authorize_url.
	AuthorizeURL string `json:"authorize_url,omitempty" yaml:"authorize_url,omitempty"`
}

// MiddlewareParams represents the JSON parameters for the middleware factory.
type MiddlewareParams struct {
	Config *Config `json:"config,omitempty"`
}

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

	middleware := createMiddlewareFunc(cfg)

	upstreamSwapMw := &Middleware{
		middleware: middleware,
	}

	runner.AddMiddleware(config.Type, upstreamSwapMw)
	return nil
}

// validateConfig validates the upstream swap configuration.
func validateConfig(cfg *Config) error {
	// ProviderName is required to identify which upstream provider's tokens to retrieve
	if cfg.ProviderName == "" {
		return fmt.Errorf("provider_name is required")
	}

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

	// AuthorizeURL, when set, must be an absolute https URL. It is emitted to
	// clients in the 401 consent response, so it must point at the configured
	// authorization server — never a relative or plain-HTTP address.
	if cfg.AuthorizeURL != "" {
		u, err := url.Parse(cfg.AuthorizeURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("authorize_url must be an absolute https:// URL")
		}
	}

	return nil
}

// consentRequiredBody is the JSON body of the 401 consent response.
type consentRequiredBody struct {
	Error        string `json:"error"`
	Provider     string `json:"provider"`
	AuthorizeURL string `json:"authorize_url,omitempty"`
}

// writeUpstreamAuthRequired writes a 401 response with a WWW-Authenticate Bearer
// challenge per RFC 6750 Section 3.1, signalling that the caller must re-authenticate
// with the upstream IdP, plus a JSON body carrying the provider and (when
// configured) the authorize endpoint so the client can render an actionable
// "connect your account" prompt. The body never contains token material.
func writeUpstreamAuthRequired(w http.ResponseWriter, provider, authorizeURL string) {
	w.Header().Set("WWW-Authenticate",
		`Bearer error="invalid_token", error_description="upstream token is no longer valid; re-authentication required"`)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	// Marshal errors are impossible for this fixed-shape struct; on failure the
	// client still has the RFC 6750 challenge header to act on.
	_ = json.NewEncoder(w).Encode(consentRequiredBody{
		Error:        "upstream_consent_required",
		Provider:     provider,
		AuthorizeURL: authorizeURL,
	})
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
//
// It also strips the client's original Authorization header (the ToolHive-issued
// JWT) so it is never forwarded to the backend: that token was minted for the
// proxy, not the upstream, and leaking it is credential passthrough (#5504). The
// "replace" strategy avoids this implicitly because it overwrites Authorization;
// the custom strategy must strip it explicitly. When the custom header name is
// itself Authorization (case-insensitive), the Set above already replaced the JWT
// with the upstream token, so it must be left in place.
func createCustomInjector(headerName string) injectionFunc {
	stripAuthorization := http.CanonicalHeaderKey(headerName) != "Authorization"
	return func(r *http.Request, token string) {
		r.Header.Set(headerName, fmt.Sprintf("Bearer %s", token))
		if stripAuthorization {
			r.Header.Del("Authorization")
		}
	}
}

// createMiddlewareFunc creates the actual middleware function.
// It reads upstream tokens from Identity.UpstreamTokens, which are populated
// during JWT validation by the auth middleware (Step 3).
func createMiddlewareFunc(cfg *Config) types.MiddlewareFunction {
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
			identity, ok := auth.IdentityFromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			token, exists := identity.UpstreamTokens[cfg.ProviderName]
			if !exists || token == "" {
				writeUpstreamAuthRequired(w, cfg.ProviderName, cfg.AuthorizeURL)
				return
			}

			injectToken(r, token)
			next.ServeHTTP(w, r)
		})
	}
}
