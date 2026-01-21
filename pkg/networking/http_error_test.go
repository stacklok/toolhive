// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package networking

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewHTTPError(t *testing.T) {
	t.Parallel()

	err := NewHTTPError(404, "http://example.com/api", "not found")

	require.Error(t, err)
	var httpErr *HTTPError
	require.True(t, errors.As(err, &httpErr))
	assert.Equal(t, 404, httpErr.StatusCode)
	assert.Equal(t, "http://example.com/api", httpErr.URL)
	assert.Equal(t, "not found", httpErr.Message)
}

func TestHTTPError_Error(t *testing.T) {
	t.Parallel()

	err := &HTTPError{
		StatusCode: 404,
		Message:    "not found",
		URL:        "http://example.com/api",
	}

	assert.Equal(t, "HTTP 404 for URL http://example.com/api: not found", err.Error())
}

func TestIsHTTPError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		statusCode int
		expected   bool
	}{
		{
			name:       "matching HTTPError",
			err:        &HTTPError{StatusCode: 404, URL: "http://example.com"},
			statusCode: 404,
			expected:   true,
		},
		{
			name:       "non-matching status code",
			err:        &HTTPError{StatusCode: 404, URL: "http://example.com"},
			statusCode: 500,
			expected:   false,
		},
		{
			name:       "any HTTPError with statusCode 0",
			err:        &HTTPError{StatusCode: 403, URL: "http://example.com"},
			statusCode: 0,
			expected:   true,
		},
		{
			name:       "non-HTTPError",
			err:        errors.New("some other error"),
			statusCode: 404,
			expected:   false,
		},
		{
			name:       "wrapped HTTPError",
			err:        fmt.Errorf("wrapped: %w", &HTTPError{StatusCode: 500, URL: "http://example.com"}),
			statusCode: 500,
			expected:   true,
		},
		{
			name:       "nil error",
			err:        nil,
			statusCode: 404,
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := IsHTTPError(tt.err, tt.statusCode)
			assert.Equal(t, tt.expected, result)
		})
	}
}
