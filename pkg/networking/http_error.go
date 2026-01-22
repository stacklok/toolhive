// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package networking

import (
	"errors"
	"fmt"
)

// HTTPError represents an HTTP error response with status code, URL, and message.
type HTTPError struct {
	// StatusCode is the HTTP status code.
	StatusCode int

	// Message is a description of the error (may be a preview of the response body).
	Message string

	// URL is the requested URL.
	URL string
}

// Error implements the error interface.
func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d for URL %s: %s", e.StatusCode, e.URL, e.Message)
}

// NewHTTPError creates a new HTTP error.
func NewHTTPError(statusCode int, url, message string) error {
	return &HTTPError{
		StatusCode: statusCode,
		URL:        url,
		Message:    message,
	}
}

// IsHTTPError checks if an error is an HTTPError with the specified status code.
// If statusCode is 0, it matches any HTTPError.
func IsHTTPError(err error, statusCode int) bool {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	if statusCode == 0 {
		return true
	}
	return httpErr.StatusCode == statusCode
}
