// Package recovery provides panic recovery middleware for HTTP handlers.
package recovery

import (
	"net/http"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// MiddlewareType is the type constant for recovery middleware
const MiddlewareType = "recovery"

// Middleware is an HTTP middleware that recovers from panics.
// When a panic occurs, it logs the error and returns
// a 500 Internal Server Error response to the client.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Errorf("Panic recovered: %v", rec)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// FactoryMiddleware wraps recovery middleware functionality for the factory pattern.
type FactoryMiddleware struct{}

// Handler returns the middleware function used by the proxy.
func (FactoryMiddleware) Handler() types.MiddlewareFunction {
	return Middleware
}

// Close cleans up any resources used by the middleware.
func (FactoryMiddleware) Close() error {
	// Recovery middleware doesn't need cleanup
	return nil
}

// CreateMiddleware is the factory function for recovery middleware.
// It creates and registers the recovery middleware with the runner.
func CreateMiddleware(_ *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	recoveryMw := &FactoryMiddleware{}
	runner.AddMiddleware(MiddlewareType, recoveryMw)
	return nil
}
