// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp

import (
	"errors"
	"strings"
)

// Common domain errors used across vmcp subpackages.
// Following DDD principles, domain errors are defined at the package root.
// These errors should be checked using errors.Is().

var (
	// ErrNotFound indicates a requested resource (tool, resource, prompt, workflow) was not found.
	// Wrapping errors should provide specific details about what was not found.
	ErrNotFound = errors.New("not found")

	// ErrInvalidConfig indicates invalid configuration was provided.
	// Wrapping errors should provide specific details about what is invalid.
	ErrInvalidConfig = errors.New("invalid configuration")

	// ErrAuthenticationFailed indicates authentication failure.
	// Wrapping errors should include the underlying authentication error.
	ErrAuthenticationFailed = errors.New("authentication failed")

	// ErrAuthorizationFailed indicates authorization failure.
	// Wrapping errors should include the policy or permission that was denied.
	ErrAuthorizationFailed = errors.New("authorization failed")

	// ErrWorkflowFailed indicates workflow execution failed.
	// Wrapping errors should include the step ID and failure reason.
	ErrWorkflowFailed = errors.New("workflow execution failed")

	// ErrTimeout indicates an operation timed out.
	// Wrapping errors should include the operation type and timeout duration.
	ErrTimeout = errors.New("operation timed out")

	// ErrCancelled indicates an operation was cancelled.
	// Context cancellation should wrap this error with context.Cause().
	ErrCancelled = errors.New("operation cancelled")

	// ErrInvalidInput indicates invalid input parameters.
	// Wrapping errors should specify which parameter is invalid and why.
	ErrInvalidInput = errors.New("invalid input")

	// ErrUnsupportedTransport indicates an unsupported MCP transport type.
	// Wrapping errors should specify which transport type is not supported.
	ErrUnsupportedTransport = errors.New("unsupported transport type")

	// ErrToolExecutionFailed indicates an MCP tool execution failed (domain error).
	// This represents the tool running but returning an error result (IsError=true in MCP).
	// These errors should be forwarded to the client transparently as the LLM needs to see them.
	// Wrapping errors should include the tool name and error message from MCP.
	ErrToolExecutionFailed = errors.New("tool execution failed")

	// ErrBackendUnavailable indicates a backend MCP server is unreachable (operational error).
	// This represents infrastructure issues (network down, server not responding, etc.).
	// These errors may be retried, circuit-broken, or handled differently from domain errors.
	// Wrapping errors should include the backend ID and underlying cause.
	ErrBackendUnavailable = errors.New("backend unavailable")

	// ErrToolNameConflict indicates a composite tool name conflicts with a backend tool name.
	// This prevents ambiguity in routing/execution where the same name could refer to
	// either a backend tool or a composite workflow tool.
	// Wrapping errors should list the conflicting tool names.
	ErrToolNameConflict = errors.New("tool name conflict")
)

// Error Categorization Helpers
//
// These functions categorize errors by examining error message strings.
// They serve as a fallback mechanism for error detection when:
//
// 1. Errors come from external libraries that use their own error types and formats
// 2. Legacy code paths don't wrap errors with sentinel errors
// 3. Backwards compatibility is needed for error detection
//
// Note: BackendClient now wraps all errors with appropriate sentinel errors
// (ErrAuthenticationFailed, ErrTimeout, ErrBackendUnavailable). Health monitoring
// code should prefer errors.Is() checks over these string-based functions.
// These functions remain for backwards compatibility and as a fallback mechanism.

// IsAuthenticationError checks if an error message indicates an authentication failure.
// Uses case-insensitive pattern matching to detect various auth error formats from
// HTTP libraries, MCP protocol errors, and authentication middleware.
func IsAuthenticationError(err error) bool {
	if err == nil {
		return false
	}

	errLower := strings.ToLower(err.Error())

	// Check for explicit authentication failure messages
	if strings.Contains(errLower, "authentication failed") ||
		strings.Contains(errLower, "authentication error") {
		return true
	}

	// Check for HTTP 401/403 status codes with context
	// Match patterns like "401 Unauthorized", "HTTP 401", "status code 401"
	if strings.Contains(errLower, "401 unauthorized") ||
		strings.Contains(errLower, "403 forbidden") ||
		strings.Contains(errLower, "http 401") ||
		strings.Contains(errLower, "http 403") ||
		strings.Contains(errLower, "status code 401") ||
		strings.Contains(errLower, "status code 403") {
		return true
	}

	// Check for explicit unauthenticated/unauthorized errors
	if strings.Contains(errLower, "request unauthenticated") ||
		strings.Contains(errLower, "request unauthorized") ||
		strings.Contains(errLower, "access denied") {
		return true
	}

	return false
}

// IsTimeoutError checks if an error message indicates a timeout.
// Detects various timeout formats from context deadlines, HTTP timeouts,
// and network timeout errors.
func IsTimeoutError(err error) bool {
	if err == nil {
		return false
	}

	errLower := strings.ToLower(err.Error())
	return strings.Contains(errLower, "timeout") ||
		strings.Contains(errLower, "deadline exceeded") ||
		strings.Contains(errLower, "context deadline exceeded")
}

// IsConnectionError checks if an error message indicates a connection failure.
// Detects network-level errors like connection refused, reset, unreachable, etc.
// Also detects broken pipes, EOF errors, and HTTP 5xx server errors that indicate
// backend unavailability.
func IsConnectionError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	errLower := strings.ToLower(errStr)

	// Check against list of known connection error patterns
	networkPatterns := []string{
		"connection refused", "connection reset", "no route to host",
		"network is unreachable", "broken pipe", "connection closed",
	}
	for _, pattern := range networkPatterns {
		if strings.Contains(errLower, pattern) {
			return true
		}
	}

	// EOF errors (be specific - check exact case to avoid false positives)
	if strings.Contains(errStr, "EOF") {
		return true
	}

	// HTTP 5xx server errors
	httpErrorPatterns := []string{
		"500 internal server error", "502 bad gateway",
		"503 service unavailable", "504 gateway timeout",
		"status code 500", "status code 502",
		"status code 503", "status code 504",
	}
	for _, pattern := range httpErrorPatterns {
		if strings.Contains(errLower, pattern) {
			return true
		}
	}

	return false
}
