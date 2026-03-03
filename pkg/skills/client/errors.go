// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrServerUnreachable is returned when the client cannot connect to the
// ToolHive API server. The most common cause is that "thv serve" is not
// running.
var ErrServerUnreachable = errors.New("could not reach ToolHive API server — is 'thv serve' running?")

// APIError represents an error response from the Skills API.
type APIError struct {
	// StatusCode is the HTTP status code returned by the server.
	StatusCode int
	// Message is the error message from the response body.
	Message string
}

// Error implements the error interface.
func (e *APIError) Error() string {
	return fmt.Sprintf("skills API error (HTTP %d): %s", e.StatusCode, e.Message)
}

// IsNotFound reports whether err is an APIError with HTTP 404 status.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// IsConflict reports whether err is an APIError with HTTP 409 status.
func IsConflict(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict
}

// IsBadRequest reports whether err is an APIError with HTTP 400 status.
func IsBadRequest(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest
}
