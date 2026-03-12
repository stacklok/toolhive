// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// NewIncomingAuthMiddleware creates HTTP middleware for incoming authentication
// and authorization based on the vMCP configuration.
//
// This factory handles all incoming auth types:
//   - "oidc": OIDC token validation
//   - "local": Local OS user authentication
//   - "anonymous": Anonymous user (no authentication required)
//
// Authentication and authorization are returned as separate middleware to allow
// the caller to insert discovery and annotation-enrichment middleware between them.
// This ensures the authz middleware can access tool annotations populated by
// the discovery pipeline.
//
// All middleware types now directly create and inject Identity into the context,
// eliminating the need for a separate conversion layer.
//
// Returns:
//   - authMw: Composed auth + MCP parser middleware (auth runs first, then parser)
//   - authzMw: Authorization middleware (nil if authz is not configured)
//   - authInfoHandler: Handler for /.well-known/oauth-protected-resource endpoint (may be nil)
//   - err: Error if middleware creation fails
func NewIncomingAuthMiddleware(
	ctx context.Context,
	cfg *config.IncomingAuthConfig,
) (
	authMw func(http.Handler) http.Handler,
	authzMw func(http.Handler) http.Handler,
	authInfoHandler http.Handler,
	err error,
) {
	if cfg == nil {
		return nil, nil, nil, fmt.Errorf("incoming auth config is required")
	}

	var authMiddleware func(http.Handler) http.Handler

	switch cfg.Type {
	case "oidc":
		authMiddleware, authInfoHandler, err = newOIDCAuthMiddleware(ctx, cfg.OIDC)
	case "local":
		authMiddleware, authInfoHandler, err = newLocalAuthMiddleware(ctx)
	case "anonymous":
		authMiddleware, authInfoHandler, err = newAnonymousAuthMiddleware()
	default:
		return nil, nil, nil, fmt.Errorf("unsupported incoming auth type: %s (supported: oidc, local, anonymous)", cfg.Type)
	}

	if err != nil {
		return nil, nil, nil, err
	}

	// If authorization is configured, create authz middleware separately.
	// Authz is returned as its own middleware so the caller can place it after
	// discovery and annotation-enrichment in the middleware chain, giving
	// Cedar policies access to discovered tool annotations.
	var authzMiddleware func(http.Handler) http.Handler
	if cfg.Authz != nil && cfg.Authz.Type == "cedar" && len(cfg.Authz.Policies) > 0 {
		authzMiddleware, err = newCedarAuthzMiddleware(cfg.Authz)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to create authorization middleware: %w", err)
		}
		slog.Info("authorization middleware enabled with Cedar policies")
	}

	// Auth middleware composes auth + parser.
	// The parser is included because downstream middleware (audit, authz) reads
	// parsed MCP data from context.
	composedAuth := func(next http.Handler) http.Handler {
		withParser := mcp.ParsingMiddleware(next)
		return authMiddleware(withParser)
	}

	return composedAuth, authzMiddleware, authInfoHandler, nil
}

// newCedarAuthzMiddleware creates Cedar authorization middleware from vMCP config.
func newCedarAuthzMiddleware(authzCfg *config.AuthzConfig) (func(http.Handler) http.Handler, error) {
	if authzCfg == nil || len(authzCfg.Policies) == 0 {
		return nil, fmt.Errorf("cedar authorization requires at least one policy")
	}

	slog.Info("creating Cedar authorization middleware", "policies", len(authzCfg.Policies))

	// Build the Cedar config structure expected by the authorizer factory
	cedarConfig := cedar.Config{
		Version: "1.0",
		Type:    cedar.ConfigType,
		Options: &cedar.ConfigOptions{
			Policies:     authzCfg.Policies,
			EntitiesJSON: "[]",
		},
	}

	// Create the authz Config using the factory method
	authzConfig, err := authorizers.NewConfig(cedarConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create authz config: %w", err)
	}

	// Create the middleware using the existing factory
	middlewareFn, err := authz.CreateMiddlewareFromConfig(authzConfig, "vmcp")
	if err != nil {
		return nil, fmt.Errorf("failed to create Cedar middleware: %w", err)
	}

	return middlewareFn, nil
}

// newOIDCAuthMiddleware creates OIDC authentication middleware.
// Reuses pkg/auth.GetAuthenticationMiddleware for OIDC token validation.
// The middleware now directly creates Identity in context (no separate conversion needed).
func newOIDCAuthMiddleware(
	ctx context.Context,
	oidcCfg *config.OIDCConfig,
) (func(http.Handler) http.Handler, http.Handler, error) {
	if oidcCfg == nil {
		return nil, nil, fmt.Errorf("OIDC configuration required when Type='oidc'")
	}

	slog.Info("creating OIDC incoming authentication middleware")

	// Use Resource field if specified, otherwise fall back to Audience
	if oidcCfg.Resource == "" {
		slog.Warn("no Resource defined in OIDC configuration")
	}

	oidcConfig := &auth.TokenValidatorConfig{
		Issuer:            oidcCfg.Issuer,
		ClientID:          oidcCfg.ClientID,
		Audience:          oidcCfg.Audience,
		ResourceURL:       oidcCfg.Resource,
		AllowPrivateIP:    oidcCfg.ProtectedResourceAllowPrivateIP,
		InsecureAllowHTTP: oidcCfg.InsecureAllowHTTP,
		Scopes:            oidcCfg.Scopes,
	}

	// pkg/auth.GetAuthenticationMiddleware now returns middleware that creates Identity
	authMw, authInfo, err := auth.GetAuthenticationMiddleware(ctx, oidcConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create OIDC authentication middleware: %w", err)
	}

	slog.Info("oIDC authentication configured",
		"issuer", oidcCfg.Issuer, "client_id", oidcCfg.ClientID, "resource", oidcCfg.Resource)

	return authMw, authInfo, nil
}

// newLocalAuthMiddleware creates local OS user authentication middleware.
// Reuses pkg/auth.GetAuthenticationMiddleware with nil config to trigger local auth mode.
// The middleware now directly creates Identity in context (no separate conversion needed).
func newLocalAuthMiddleware(ctx context.Context) (func(http.Handler) http.Handler, http.Handler, error) {
	slog.Info("creating local user authentication middleware")

	// Passing nil to GetAuthenticationMiddleware triggers local auth mode
	// pkg/auth.GetAuthenticationMiddleware now returns middleware that creates Identity
	authMw, authInfo, err := auth.GetAuthenticationMiddleware(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create local authentication middleware: %w", err)
	}

	return authMw, authInfo, nil
}

// newAnonymousAuthMiddleware creates anonymous authentication middleware.
// Calls pkg/auth.AnonymousMiddleware directly since GetAuthenticationMiddleware doesn't support anonymous.
func newAnonymousAuthMiddleware() (func(http.Handler) http.Handler, http.Handler, error) {
	slog.Info("creating anonymous authentication middleware")

	return auth.AnonymousMiddleware, nil, nil
}
