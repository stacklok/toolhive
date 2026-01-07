// Package common provides shared utilities for proxy implementations.
package common

import (
	"net/http"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

// ApplyMiddlewares applies a chain of middlewares to an HTTP handler.
// Middlewares are applied in reverse order (last middleware is applied first)
// so that the first middleware in the slice is the outermost handler.
func ApplyMiddlewares(handler http.Handler, middlewares ...types.NamedMiddleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i].Function(handler)
	}
	return handler
}
