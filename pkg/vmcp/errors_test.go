// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsAuthenticationError pins IsAuthenticationError's exact matching
// boundary (§13): it must recognize the specific phrase patterns the
// function checks for (e.g. "401 unauthorized", "unauthorized (401)",
// "403 forbidden", "authorization required", "access denied") but must NOT
// fire on a bare status code or a bare keyword with no surrounding context —
// otherwise unrelated errors that merely mention "401" or "unauthorized" in
// passing would be misclassified as authentication failures.
func TestIsAuthenticationError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		// --- negatives: bare substrings and unrelated errors must not match ---
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "bare 401 with no surrounding phrase",
			err:  errors.New("401"),
			want: false,
		},
		{
			name: "bare unauthorized with no surrounding phrase",
			err:  errors.New("unauthorized"),
			want: false,
		},
		{
			name: "unrelated error that merely contains 401 as a numeric substring",
			err:  errors.New("error 4010 occurred"),
			want: false,
		},
		{
			name: "hostname that merely contains 401",
			err:  errors.New("http://backend401.example.com"),
			want: false,
		},
		{
			name: "connection refused",
			err:  errors.New("connection refused"),
			want: false,
		},
		{
			name: "request timeout",
			err:  errors.New("request timeout"),
			want: false,
		},
		{
			name: "404 not found",
			err:  errors.New("404 not found"),
			want: false,
		},
		{
			name: "500 internal server error",
			err:  errors.New("500 internal server error"),
			want: false,
		},
		{
			// Pins against accidental loosening of the "authorization required"
			// matcher: "field 'authorization' required" must NOT match. The
			// matcher looks for the contiguous substring "authorization
			// required"; a future change allowing arbitrary whitespace between
			// the words would silently regress this.
			name: "validation message with 'authorization' and 'required' separated",
			err:  errors.New("field 'authorization' required"),
			want: false,
		},

		// --- positives: recognized phrase patterns ---
		{
			name: "authentication failed",
			err:  errors.New("authentication failed"),
			want: true,
		},
		{
			name: "Authentication Failed (case-insensitive)",
			err:  errors.New("Authentication Failed"),
			want: true,
		},
		{
			name: "authentication error phrase",
			err:  errors.New("authentication error: bad token"),
			want: true,
		},
		{
			name: "401 Unauthorized phrase",
			err:  errors.New("401 Unauthorized"),
			want: true,
		},
		{
			name: "unauthorized (401) reversed phrase (mcp-go ErrUnauthorized form)",
			err:  errors.New("unauthorized (401)"),
			want: true,
		},
		{
			name: "403 forbidden phrase",
			err:  errors.New("403 forbidden"),
			want: true,
		},
		{
			name: "HTTP 401 phrase",
			err:  errors.New("HTTP 401"),
			want: true,
		},
		{
			name: "HTTP 403 phrase",
			err:  errors.New("HTTP 403"),
			want: true,
		},
		{
			name: "status code 401 phrase",
			err:  errors.New("status code 401"),
			want: true,
		},
		{
			name: "status code 403 phrase",
			err:  errors.New("status code 403"),
			want: true,
		},
		{
			name: "request unauthenticated phrase",
			err:  errors.New("request unauthenticated"),
			want: true,
		},
		{
			name: "request unauthorized phrase",
			err:  errors.New("request unauthorized"),
			want: true,
		},
		{
			name: "authorization required phrase",
			err:  errors.New("authorization required"),
			want: true,
		},
		{
			name: "access denied phrase",
			err:  errors.New("access denied"),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsAuthenticationError(tt.err))
		})
	}
}
