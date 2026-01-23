// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"strings"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// containsAuthError checks if a lowercase error string contains authentication-related patterns.
// This consolidates authentication error detection to avoid repeated string.Contains checks.
func containsAuthError(errLower string) bool {
	return strings.Contains(errLower, "authentication failed") ||
		strings.Contains(errLower, "authentication error") ||
		strings.Contains(errLower, "401 unauthorized") ||
		strings.Contains(errLower, "403 forbidden") ||
		strings.Contains(errLower, "http 401") ||
		strings.Contains(errLower, "http 403") ||
		strings.Contains(errLower, "status code 401") ||
		strings.Contains(errLower, "status code 403") ||
		strings.Contains(errLower, "request unauthenticated") ||
		strings.Contains(errLower, "request unauthorized") ||
		strings.Contains(errLower, "access denied")
}

// containsTimeoutError checks if a lowercase error string contains timeout-related patterns.
func containsTimeoutError(errLower string) bool {
	return strings.Contains(errLower, "timeout") ||
		strings.Contains(errLower, "deadline exceeded")
}

// containsConnectionError checks if a lowercase error string contains connection-related patterns.
func containsConnectionError(errLower string) bool {
	return strings.Contains(errLower, "connection refused") ||
		strings.Contains(errLower, "connection reset") ||
		strings.Contains(errLower, "no route to host") ||
		strings.Contains(errLower, "network is unreachable")
}

// categorizeConnectionErrorByString determines the specific connection error reason
// based on error string patterns. This provides more granular categorization than
// the generic "connection error" classification.
//
// Returns:
//   - ReasonConnectionRefused: For "connection refused" errors
//   - ReasonNetworkUnreachable: For network routing issues
//   - ReasonHealthCheckFailed: For other connection errors (reset, broken pipe, etc.)
func categorizeConnectionErrorByString(errLower string) vmcp.BackendHealthReason {
	if strings.Contains(errLower, "connection refused") {
		return vmcp.ReasonConnectionRefused
	}

	// Fix: Add explicit parentheses to avoid boolean precedence confusion
	// This checks: (network AND unreachable) OR (no route to host)
	if (strings.Contains(errLower, "network") && strings.Contains(errLower, "unreachable")) ||
		strings.Contains(errLower, "no route to host") {
		return vmcp.ReasonNetworkUnreachable
	}

	// Other connection errors (reset, broken pipe, etc.)
	return vmcp.ReasonHealthCheckFailed
}
