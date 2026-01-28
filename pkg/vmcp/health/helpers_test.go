// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

func TestContainsAuthError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		errLower string
		expected bool
	}{
		// Positive cases
		{name: "authentication failed", errLower: "authentication failed", expected: true},
		{name: "authentication error", errLower: "authentication error: bad token", expected: true},
		{name: "401 unauthorized", errLower: "401 unauthorized", expected: true},
		{name: "403 forbidden", errLower: "403 forbidden", expected: true},
		{name: "http 401", errLower: "http 401", expected: true},
		{name: "http 403", errLower: "http 403", expected: true},
		{name: "status code 401", errLower: "status code 401", expected: true},
		{name: "status code 403", errLower: "status code 403", expected: true},
		{name: "request unauthenticated", errLower: "request unauthenticated", expected: true},
		{name: "request unauthorized", errLower: "request unauthorized", expected: true},
		{name: "access denied", errLower: "access denied", expected: true},

		// Negative cases
		{name: "connection refused", errLower: "connection refused", expected: false},
		{name: "timeout", errLower: "request timeout", expected: false},
		{name: "generic error", errLower: "something went wrong", expected: false},
		{name: "404 not found", errLower: "404 not found", expected: false},
		{name: "500 error", errLower: "500 internal server error", expected: false},
		{name: "hostname with 401", errLower: "http://backend401.example.com", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := containsAuthError(tt.errLower)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestContainsTimeoutError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		errLower string
		expected bool
	}{
		// Positive cases
		{name: "timeout", errLower: "request timeout", expected: true},
		{name: "deadline exceeded", errLower: "deadline exceeded", expected: true},
		{name: "context deadline exceeded", errLower: "context deadline exceeded", expected: true},

		// Negative cases
		{name: "connection refused", errLower: "connection refused", expected: false},
		{name: "authentication failed", errLower: "authentication failed", expected: false},
		{name: "generic error", errLower: "something went wrong", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := containsTimeoutError(tt.errLower)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestContainsConnectionError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		errLower string
		expected bool
	}{
		// Positive cases
		{name: "connection refused", errLower: "connection refused", expected: true},
		{name: "connection reset", errLower: "connection reset by peer", expected: true},
		{name: "no route to host", errLower: "no route to host", expected: true},
		{name: "network is unreachable", errLower: "network is unreachable", expected: true},

		// Negative cases
		{name: "timeout", errLower: "request timeout", expected: false},
		{name: "authentication failed", errLower: "authentication failed", expected: false},
		{name: "generic error", errLower: "something went wrong", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := containsConnectionError(tt.errLower)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCategorizeConnectionErrorByString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		errLower       string
		expectedReason vmcp.BackendHealthReason
	}{
		{
			name:           "connection refused",
			errLower:       "connection refused",
			expectedReason: vmcp.ReasonConnectionRefused,
		},
		{
			name:           "network is unreachable",
			errLower:       "network is unreachable",
			expectedReason: vmcp.ReasonNetworkUnreachable,
		},
		{
			name:           "no route to host",
			errLower:       "no route to host",
			expectedReason: vmcp.ReasonNetworkUnreachable,
		},
		{
			name:           "connection reset",
			errLower:       "connection reset by peer",
			expectedReason: vmcp.ReasonHealthCheckFailed,
		},
		{
			name:           "broken pipe",
			errLower:       "broken pipe",
			expectedReason: vmcp.ReasonHealthCheckFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reason := categorizeConnectionErrorByString(tt.errLower)
			assert.Equal(t, tt.expectedReason, reason)
		})
	}
}

func TestCategorizeConnectionErrorByString_BooleanPrecedence(t *testing.T) {
	t.Parallel()

	// This test verifies the fix for the boolean precedence bug
	// Without parentheses: A && B || C evaluates as (A && B) || C
	// With fix: (A && B) || C is explicit

	tests := []struct {
		name           string
		errLower       string
		expectedReason vmcp.BackendHealthReason
		description    string
	}{
		{
			name:           "network unreachable (both keywords)",
			errLower:       "network is unreachable",
			expectedReason: vmcp.ReasonNetworkUnreachable,
			description:    "Should match: network AND unreachable",
		},
		{
			name:           "no route to host",
			errLower:       "no route to host",
			expectedReason: vmcp.ReasonNetworkUnreachable,
			description:    "Should match: no route to host (OR condition)",
		},
		{
			name:           "just network (not unreachable)",
			errLower:       "network error occurred",
			expectedReason: vmcp.ReasonHealthCheckFailed,
			description:    "Should NOT match: network without unreachable",
		},
		{
			name:           "just unreachable (not network)",
			errLower:       "service unreachable",
			expectedReason: vmcp.ReasonHealthCheckFailed,
			description:    "Should NOT match: unreachable without network",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reason := categorizeConnectionErrorByString(tt.errLower)
			assert.Equal(t, tt.expectedReason, reason, tt.description)
		})
	}
}
