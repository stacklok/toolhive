package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

// Middleware type constant
const (
	MiddlewareType = "auth"
)

// MiddlewareParams represents the parameters for authentication middleware
type MiddlewareParams struct {
	OIDCConfig *TokenValidatorConfig `json:"oidc_config,omitempty"`
}

// Middleware wraps authentication middleware functionality
type Middleware struct {
	middleware      types.MiddlewareFunction
	authInfoHandler http.Handler
}

// Handler returns the middleware function used by the proxy.
func (m *Middleware) Handler() types.MiddlewareFunction {
	return m.middleware
}

// Close cleans up any resources used by the middleware.
func (*Middleware) Close() error {
	// Auth middleware doesn't need cleanup
	return nil
}

// AuthInfoHandler returns the authentication info handler.
func (m *Middleware) AuthInfoHandler() http.Handler {
	return m.authInfoHandler
}

// CreateMiddleware factory function for authentication middleware
func CreateMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {

	var params MiddlewareParams
	if err := json.Unmarshal(config.Parameters, &params); err != nil {
		return fmt.Errorf("failed to unmarshal auth middleware parameters: %w", err)
	}

	middleware, authInfoHandler, err := GetAuthenticationMiddleware(context.Background(), params.OIDCConfig)
	if err != nil {
		return fmt.Errorf("failed to create authentication middleware: %w", err)
	}

	authMw := &Middleware{
		middleware:      middleware,
		authInfoHandler: authInfoHandler,
	}

	// Add middleware to runner
	runner.AddMiddleware(authMw)

	// Set auth info handler if present
	if authInfoHandler != nil {
		runner.SetAuthInfoHandler(authInfoHandler)
	}

	return nil
}
