// Package health provides health monitoring for vMCP backend MCP servers.
//
// This package implements the HealthChecker interface and provides periodic
// health monitoring with configurable intervals and failure thresholds.
package health

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// healthChecker implements vmcp.HealthChecker using ListCapabilities as the health check.
type healthChecker struct {
	// client is the backend client used to communicate with backends.
	client vmcp.BackendClient

	// timeout is the timeout for health check operations.
	timeout time.Duration
}

// NewHealthChecker creates a new health checker that uses BackendClient.ListCapabilities
// as the health check mechanism. This validates the full MCP communication stack:
// network connectivity, MCP protocol compliance, authentication, and responsiveness.
//
// Parameters:
//   - client: BackendClient for communicating with backend MCP servers
//   - timeout: Maximum duration for health check operations (0 = no timeout)
//
// Returns a new HealthChecker implementation.
func NewHealthChecker(client vmcp.BackendClient, timeout time.Duration) vmcp.HealthChecker {
	return &healthChecker{
		client:  client,
		timeout: timeout,
	}
}

// CheckHealth performs a health check on a backend by calling ListCapabilities.
// This validates the full MCP communication stack and returns the backend's health status.
//
// Health determination logic:
//   - Success: Backend is healthy (BackendHealthy)
//   - Authentication error: Backend is unauthenticated (BackendUnauthenticated)
//   - Timeout or connection error: Backend is unhealthy (BackendUnhealthy)
//   - Other errors: Backend is unhealthy (BackendUnhealthy)
//
// The error return is informational and provides context about what failed.
// The BackendHealthStatus return indicates the categorized health state.
func (h *healthChecker) CheckHealth(ctx context.Context, target *vmcp.BackendTarget) (vmcp.BackendHealthStatus, error) {
	// Apply timeout if configured
	checkCtx := ctx
	var cancel context.CancelFunc
	if h.timeout > 0 {
		checkCtx, cancel = context.WithTimeout(ctx, h.timeout)
		defer cancel()
	}

	logger.Debugf("Performing health check for backend %s (%s)", target.WorkloadName, target.BaseURL)

	// Use ListCapabilities as the health check - it performs:
	// 1. Client creation with transport setup
	// 2. MCP protocol initialization handshake
	// 3. Capabilities query (tools, resources, prompts)
	// This validates the full communication stack
	_, err := h.client.ListCapabilities(checkCtx, target)
	if err != nil {
		// Categorize the error to determine health status
		status := categorizeError(err)
		logger.Debugf("Health check failed for backend %s: %v (status: %s)",
			target.WorkloadName, err, status)
		return status, fmt.Errorf("health check failed: %w", err)
	}

	logger.Debugf("Health check succeeded for backend %s", target.WorkloadName)
	return vmcp.BackendHealthy, nil
}

// categorizeError determines the appropriate health status based on the error type.
// This helps distinguish between different failure modes (auth, timeout, connectivity, etc.).
func categorizeError(err error) vmcp.BackendHealthStatus {
	if err == nil {
		return vmcp.BackendHealthy
	}

	// Check error message for common patterns
	errMsg := err.Error()

	// Authentication failures
	if isAuthError(errMsg) {
		return vmcp.BackendUnauthenticated
	}

	// Timeout and connection errors
	if isTimeoutError(errMsg) || isConnectionError(errMsg) {
		return vmcp.BackendUnhealthy
	}

	// Default to unhealthy for unknown errors
	return vmcp.BackendUnhealthy
}

// isAuthError checks if the error message indicates an authentication failure.
func isAuthError(errMsg string) bool {
	return contains(errMsg, "authentication failed") ||
		contains(errMsg, "unauthenticated") ||
		contains(errMsg, "unauthorized") ||
		contains(errMsg, "401")
}

// isTimeoutError checks if the error message indicates a timeout.
func isTimeoutError(errMsg string) bool {
	return contains(errMsg, "timeout") ||
		contains(errMsg, "deadline exceeded") ||
		contains(errMsg, "context deadline exceeded")
}

// isConnectionError checks if the error message indicates a connection failure.
func isConnectionError(errMsg string) bool {
	return contains(errMsg, "connection refused") ||
		contains(errMsg, "connection reset") ||
		contains(errMsg, "no route to host") ||
		contains(errMsg, "network is unreachable")
}

// contains checks if a string contains a substring.
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
