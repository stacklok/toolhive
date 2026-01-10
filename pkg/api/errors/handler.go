// Package errors provides HTTP error handling utilities for the API.
package errors

import (
	"net/http"

	"github.com/stacklok/toolhive/pkg/errors"
	"github.com/stacklok/toolhive/pkg/logger"
)

// HandlerWithError is an HTTP handler that can return an error.
// This signature allows handlers to return errors instead of manually
// writing error responses, enabling centralized error handling.
type HandlerWithError func(http.ResponseWriter, *http.Request) error

// ErrorHandler wraps a HandlerWithError and converts returned errors
// into appropriate HTTP responses.
//
// The decorator:
//   - Returns early if no error is returned (handler already wrote response)
//   - Extracts HTTP status code from the error using errors.Code()
//   - For 5xx errors: logs full error details, returns generic message to client
//   - For 4xx errors: returns error message to client
//
// Usage:
//
//	r.Get("/{name}", apierrors.ErrorHandler(routes.getWorkload))
func ErrorHandler(fn HandlerWithError) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := fn(w, r)
		if err == nil {
			// No error returned, handler already wrote the response
			return
		}

		// Extract HTTP status code from the error
		code := errors.Code(err)

		// For 5xx errors, log the full error but return a generic message
		if code >= http.StatusInternalServerError {
			logger.Errorf("Internal server error: %v", err)
			http.Error(w, http.StatusText(code), code)
			return
		}

		// For 4xx errors, return the error message to the client
		http.Error(w, err.Error(), code)
	}
}
