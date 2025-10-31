package auth

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
// All auth types are composed with IdentityMiddleware to provide consistent
// Identity context injection.
//
// Flow (all types):
//  1. AuthMiddleware validates/creates Claims and stores in context
//  2. IdentityMiddleware converts Claims → Identity and stores in context
//
// Returns:
//   - Composed middleware function (AuthMiddleware → IdentityMiddleware)
//   - AuthInfo handler (for /.well-known/oauth-protected-resource endpoint, may be nil)
//   - Error if middleware creation fails
func NewIncomingAuthMiddleware(
	ctx context.Context,
	cfg *config.IncomingAuthConfig,
) (func(http.Handler) http.Handler, http.Handler, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("incoming auth config is required")
	}

	var baseAuthMiddleware func(http.Handler) http.Handler
	var authInfoHandler http.Handler
	var err error

	switch cfg.Type {
	case "oidc":
		baseAuthMiddleware, authInfoHandler, err = newOIDCAuthMiddleware(ctx, cfg.OIDC)
	case "local":
		baseAuthMiddleware, authInfoHandler, err = newLocalAuthMiddleware(ctx)
	case "anonymous":
		baseAuthMiddleware, authInfoHandler, err = newAnonymousAuthMiddleware()
	default:
		return nil, nil, fmt.Errorf("unsupported incoming auth type: %s (supported: oidc, local, anonymous)", cfg.Type)
	}

	if err != nil {
		return nil, nil, err
	}

	// Compose: AuthMiddleware → IdentityMiddleware
	// All auth types create Claims in context; IdentityMiddleware converts Claims → Identity
	composed := func(next http.Handler) http.Handler {
		return baseAuthMiddleware(IdentityMiddleware(next))
	}

	return composed, authInfoHandler, nil
}

// newOIDCAuthMiddleware creates OIDC authentication middleware.
// Reuses pkg/auth.GetAuthenticationMiddleware for OIDC token validation.
func newOIDCAuthMiddleware(
	ctx context.Context,
	oidcCfg *config.OIDCConfig,
) (func(http.Handler) http.Handler, http.Handler, error) {
	if oidcCfg == nil {
		return nil, nil, fmt.Errorf("OIDC configuration required when Type='oidc'")
	}

	logger.Info("Creating OIDC incoming authentication middleware")

	oidcConfig := &auth.TokenValidatorConfig{
		Issuer:      oidcCfg.Issuer,
		ClientID:    oidcCfg.ClientID,
		Audience:    oidcCfg.Audience,
		ResourceURL: oidcCfg.Audience,
	}

	// Reuse pkg/auth.GetAuthenticationMiddleware for OIDC
	authMw, authInfo, err := auth.GetAuthenticationMiddleware(ctx, oidcConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create OIDC authentication middleware: %w", err)
	}

	logger.Infof("OIDC authentication configured (issuer: %s, client_id: %s)",
		oidcCfg.Issuer, oidcCfg.ClientID)

	return authMw, authInfo, nil
}

// newLocalAuthMiddleware creates local OS user authentication middleware.
// Reuses pkg/auth.GetAuthenticationMiddleware with nil config to trigger local auth mode.
func newLocalAuthMiddleware(ctx context.Context) (func(http.Handler) http.Handler, http.Handler, error) {
	logger.Info("Creating local user authentication middleware")

	// Passing nil to GetAuthenticationMiddleware triggers local auth mode
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
