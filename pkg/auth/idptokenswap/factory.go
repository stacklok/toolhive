package idptokenswap

import (
	"encoding/json"
	"fmt"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// MiddlewareParams contains parameters for config-driven middleware creation.
type MiddlewareParams struct {
	// SessionIDClaimKey is the JWT claim key containing the session ID.
	// Defaults to "tsid" if empty.
	SessionIDClaimKey string `json:"session_id_claim_key,omitempty"`
}

// IDPTokenStorageProvider is implemented by components that can provide IDP token storage.
// The runner implements this interface when an embedded auth server is configured,
// allowing the IDP token swap middleware to access the same storage.
type IDPTokenStorageProvider interface {
	GetIDPTokenStorage() storage.IDPTokenStorage
}

// Middleware wraps the IDP token swap middleware function.
type Middleware struct {
	middleware types.MiddlewareFunction
}

// Handler returns the middleware function used by the proxy.
func (m *Middleware) Handler() types.MiddlewareFunction {
	return m.middleware
}

// Close cleans up any resources used by the middleware.
// IDP token swap middleware has no resources to clean up.
func (*Middleware) Close() error {
	return nil
}

// CreateMiddleware is the factory function for config-driven middleware creation.
// The runner must implement IDPTokenStorageProvider to provide access to IDP token storage.
//
// NOTE: This middleware requires that the auth server storage is created before the middleware.
// The runner should ensure proper initialization order.
func CreateMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	var params MiddlewareParams
	if config.Parameters != nil {
		if err := json.Unmarshal(config.Parameters, &params); err != nil {
			return fmt.Errorf("failed to unmarshal IDP token swap parameters: %w", err)
		}
	}

	// Get storage from runner via the IDPTokenStorageProvider interface
	storageProvider, ok := runner.(IDPTokenStorageProvider)
	if !ok {
		return fmt.Errorf(
			"runner does not implement IDPTokenStorageProvider; " +
				"IDP token swap middleware requires an embedded auth server",
		)
	}

	idpStorage := storageProvider.GetIDPTokenStorage()
	if idpStorage == nil {
		return fmt.Errorf("IDP token storage not configured; ensure auth server is enabled")
	}

	middlewareFunc := CreateIDPTokenSwapMiddleware(Config{
		Storage:           idpStorage,
		SessionIDClaimKey: params.SessionIDClaimKey,
	})

	mw := &Middleware{middleware: middlewareFunc}
	runner.AddMiddleware(config.Type, mw)

	return nil
}

// Compile-time interface compliance check
var _ types.Middleware = (*Middleware)(nil)
