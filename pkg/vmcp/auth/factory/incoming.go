package factory

import (
	"context"
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// NewIncomingAuthMiddleware creates HTTP middleware for incoming authentication
// based on the vMCP configuration.
//
// This factory handles all incoming auth types:
//   - "oidc": OIDC token validation
//   - "local": Local OS user authentication
//   - "anonymous": Anonymous user (no authentication required)
//
// All middleware types now directly create and inject Identity into the context,
// eliminating the need for a separate conversion layer.
//
// Returns:
//   - Authentication middleware function
//   - AuthInfo handler (for /.well-known/oauth-protected-resource endpoint, may be nil)
//   - Error if middleware creation fails
func NewIncomingAuthMiddleware(
	ctx context.Context,
	cfg *config.IncomingAuthConfig,
) (func(http.Handler) http.Handler, http.Handler, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("incoming auth config is required")
	}

	var authMiddleware func(http.Handler) http.Handler
	var authInfoHandler http.Handler
	var err error

	switch cfg.Type {
	case "oidc":
		authMiddleware, authInfoHandler, err = newOIDCAuthMiddleware(ctx, cfg.OIDC)
	case "local":
		authMiddleware, authInfoHandler, err = newLocalAuthMiddleware(ctx)
	case "anonymous":
		authMiddleware, authInfoHandler, err = newAnonymousAuthMiddleware()
	default:
		return nil, nil, fmt.Errorf("unsupported incoming auth type: %s (supported: oidc, local, anonymous)", cfg.Type)
	}

	if err != nil {
		return nil, nil, err
	}

	return authMiddleware, authInfoHandler, nil
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

	logger.Info("Creating OIDC incoming authentication middleware")

	// Use Resource field if specified, otherwise fall back to Audience
	if oidcCfg.Resource == "" {
		logger.Warn("No Resource defined in OIDC configuration")
	}

	oidcConfig := &auth.TokenValidatorConfig{
		Issuer:      oidcCfg.Issuer,
		ClientID:    oidcCfg.ClientID,
		Audience:    oidcCfg.Audience,
		ResourceURL: oidcCfg.Resource,
	}

	// pkg/auth.GetAuthenticationMiddleware now returns middleware that creates Identity
	authMw, authInfo, err := auth.GetAuthenticationMiddleware(ctx, oidcConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create OIDC authentication middleware: %w", err)
	}

	logger.Infof("OIDC authentication configured (issuer: %s, client_id: %s, resource: %s)",
		oidcCfg.Issuer, oidcCfg.ClientID, oidcCfg.Resource)

	return authMw, authInfo, nil
}

// newLocalAuthMiddleware creates local OS user authentication middleware.
// Reuses pkg/auth.GetAuthenticationMiddleware with nil config to trigger local auth mode.
// The middleware now directly creates Identity in context (no separate conversion needed).
func newLocalAuthMiddleware(ctx context.Context) (func(http.Handler) http.Handler, http.Handler, error) {
	logger.Info("Creating local user authentication middleware")

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
	logger.Info("Creating anonymous authentication middleware")

	return auth.AnonymousMiddleware, nil, nil
}
