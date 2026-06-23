// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
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
// The serverName parameter is the VirtualMCPServer name and is used as the Cedar
// resource entity name in authorization policy evaluation. It must match the
// resource name used when compiling Cedar policies for this server.
//
// The passThroughTools parameter is optional (pass nil for none). Tool names in
// this set bypass the response filter's policy check in tools/list responses.
// This is used when the optimizer is enabled: its meta-tools (find_tool, call_tool)
// would otherwise be rejected by Cedar default-deny since no policy references them
// by name. Authorization for the underlying backend tools is enforced by the
// middleware's call_tool interception.
//
// Returns:
//   - authMw: Composed auth + MCP parser middleware (auth runs first, then parser)
//   - authzMw: Authorization middleware (nil if authz is not configured)
//   - authInfoHandler: Handler for /.well-known/oauth-protected-resource endpoint (may be nil)
//   - err: Error if middleware creation fails
func NewIncomingAuthMiddleware(
	ctx context.Context,
	cfg *config.IncomingAuthConfig,
	serverName string,
	passThroughTools map[string]struct{},
	upstreamReader upstreamtoken.TokenReader,
	keyProvider keys.PublicKeyProvider,
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
		authMiddleware, authInfoHandler, err = newOIDCAuthMiddleware(ctx, cfg.OIDC, upstreamReader, keyProvider)
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
		authzMiddleware, err = newCedarAuthzMiddleware(cfg.Authz, serverName, passThroughTools)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to create authorization middleware: %w", err)
		}
		slog.Debug("authorization middleware enabled with Cedar policies", "server_name", serverName)
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
// serverName is forwarded to CreateMiddlewareFromConfig as the Cedar resource entity name.
func newCedarAuthzMiddleware(
	authzCfg *config.AuthzConfig, serverName string, passThroughTools map[string]struct{},
) (func(http.Handler) http.Handler, error) {
	if authzCfg == nil || len(authzCfg.Policies) == 0 {
		return nil, fmt.Errorf("cedar authorization requires at least one policy")
	}
	if serverName == "" {
		return nil, fmt.Errorf("serverName must not be empty: Cedar resource-scoped policies require a non-empty server name")
	}

	slog.Info("creating Cedar authorization middleware", "policies", len(authzCfg.Policies))

	authzConfig, err := buildCedarAuthzConfig(authzCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create authz config: %w", err)
	}

	// Create the middleware using the existing factory
	middlewareFn, err := authz.CreateMiddlewareFromConfig(authzConfig, serverName, passThroughTools)
	if err != nil {
		return nil, fmt.Errorf("failed to create Cedar middleware: %w", err)
	}

	return middlewareFn, nil
}

// BuildAuthzConfig builds the authorizer-agnostic *authz.Config that the vMCP core
// admission seam (core.Config.Authz) consumes from the incoming-auth config, or
// (nil, nil) when no Cedar policies are configured.
//
// It is the SAME config newCedarAuthzMiddleware builds the HTTP authz middleware from,
// surfaced so the composition root can feed it to core.New via server.Config.Authz once
// server.New routes through Serve. The nil return mirrors that middleware's nil result
// for the no-policies case, preserving allow-all parity (a nil core Authz is allow-all).
func BuildAuthzConfig(authzCfg *config.AuthzConfig) (*authz.Config, error) {
	if authzCfg == nil || authzCfg.Type != "cedar" || len(authzCfg.Policies) == 0 {
		return nil, nil
	}
	return buildCedarAuthzConfig(authzCfg)
}

// buildCedarAuthzConfig converts the vMCP Cedar authz config into the authorizer-agnostic
// authorizers.Config (aliased as authz.Config) consumed by both the HTTP authz middleware
// (newCedarAuthzMiddleware) and the core admission seam (via BuildAuthzConfig). Callers
// guarantee authzCfg is non-nil with at least one policy.
func buildCedarAuthzConfig(authzCfg *config.AuthzConfig) (*authz.Config, error) {
	// Default EntitiesJSON to "[]" when the operator/CLI did not set it. Cedar
	// requires a valid JSON array; an empty string would fail to parse.
	entitiesJSON := authzCfg.EntitiesJSON
	if entitiesJSON == "" {
		entitiesJSON = "[]"
	}

	// Build the Cedar config structure expected by the authorizer factory.
	// PrimaryUpstreamProvider is forwarded so Cedar evaluates claims from the
	// upstream IDP token when the embedded auth server is active.
	// GroupClaimName, RoleClaimName, and GroupEntityType plumb the enterprise
	// JWT-to-entity mapping (groups/roles claims → Cedar parent UIDs) through
	// to the authorizer.
	cedarConfig := cedar.Config{
		Version: "1.0",
		Type:    cedar.ConfigType,
		Options: &cedar.ConfigOptions{
			Policies:                authzCfg.Policies,
			EntitiesJSON:            entitiesJSON,
			PrimaryUpstreamProvider: authzCfg.PrimaryUpstreamProvider,
			GroupClaimName:          authzCfg.GroupClaimName,
			RoleClaimName:           authzCfg.RoleClaimName,
			GroupEntityType:         authzCfg.GroupEntityType,
		},
	}

	return authorizers.NewConfig(cedarConfig)
}

// newOIDCAuthMiddleware creates OIDC authentication middleware.
// Reuses pkg/auth.GetAuthenticationMiddleware for OIDC token validation.
// The middleware now directly creates Identity in context (no separate conversion needed).
//
// The reader parameter, when non-nil, enables the JWT validator to load upstream
// provider tokens from the embedded auth server's storage. This is required for
// upstream_inject outgoing auth to work with an embedded auth server.
func newOIDCAuthMiddleware(
	ctx context.Context,
	oidcCfg *config.OIDCConfig,
	reader upstreamtoken.TokenReader,
	keyProvider keys.PublicKeyProvider,
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
		JWKSURL:           oidcCfg.JWKSURL,
		IntrospectionURL:  oidcCfg.IntrospectionURL,
		AllowPrivateIP:    oidcCfg.ProtectedResourceAllowPrivateIP || oidcCfg.JwksAllowPrivateIP,
		InsecureAllowHTTP: oidcCfg.InsecureAllowHTTP,
		Scopes:            oidcCfg.Scopes,
	}

	// Wire optional dependencies from the embedded auth server so the JWT
	// validator can (a) resolve JWKS keys in-process instead of self-referential
	// HTTP calls, and (b) enrich Identity with upstream provider tokens.
	var opts []auth.TokenValidatorOption
	if keyProvider != nil {
		opts = append(opts, auth.WithKeyProvider(keyProvider))
	}
	if reader != nil {
		opts = append(opts, auth.WithUpstreamTokenReader(reader))
	}

	// pkg/auth.GetAuthenticationMiddleware now returns middleware that creates Identity
	authMw, authInfo, err := auth.GetAuthenticationMiddleware(ctx, oidcConfig, opts...)
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
