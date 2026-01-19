// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package health provides health monitoring for vMCP backend MCP servers.
//
// This package implements the HealthChecker interface and provides periodic
// health monitoring with configurable intervals and failure thresholds.
package health

import (
	"context"
	"errors"
	"fmt"
	"net/url"
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

	// degradedThreshold is the response time threshold for marking a backend as degraded.
	// If a health check succeeds but takes longer than this duration, the backend is marked degraded.
	// Zero means disabled (backends will never be marked degraded based on response time alone).
	degradedThreshold time.Duration

	// selfURL is the server's own URL. If a health check targets this URL, it's short-circuited.
	// This prevents the server from trying to health check itself.
	selfURL string
}

// NewHealthChecker creates a new health checker that uses BackendClient.ListCapabilities
// as the health check mechanism. This validates the full MCP communication stack:
// network connectivity, MCP protocol compliance, authentication, and responsiveness.
//
// Parameters:
//   - client: BackendClient for communicating with backend MCP servers
//   - timeout: Maximum duration for health check operations (0 = no timeout)
//   - degradedThreshold: Response time threshold for marking backend as degraded (0 = disabled)
//   - selfURL: Optional server's own URL. If provided, health checks targeting this URL are short-circuited.
//
// Returns a new HealthChecker implementation.
func NewHealthChecker(
	client vmcp.BackendClient,
	timeout time.Duration,
	degradedThreshold time.Duration,
	selfURL string,
) vmcp.HealthChecker {
	return &healthChecker{
		client:            client,
		timeout:           timeout,
		degradedThreshold: degradedThreshold,
		selfURL:           selfURL,
	}
}

// CheckHealth performs a health check on a backend by calling ListCapabilities.
// This validates the full MCP communication stack and returns the backend's health status.
//
// Health determination logic:
//   - Success with fast response: Backend is healthy (BackendHealthy)
//   - Success with slow response (> degradedThreshold): Backend is degraded (BackendDegraded)
//   - Authentication error: Backend is unauthenticated (BackendUnauthenticated)
//   - Timeout or connection error: Backend is unhealthy (BackendUnhealthy)
//   - Other errors: Backend is unhealthy (BackendUnhealthy)
//
// The error return is informational and provides context about what failed.
// The BackendHealthStatus return indicates the categorized health state.
func (h *healthChecker) CheckHealth(ctx context.Context, target *vmcp.BackendTarget) (vmcp.BackendHealthStatus, error) {
	// Mark context as health check to bypass authentication logging
	// Health checks verify backend availability and should not require user credentials
	healthCheckCtx := WithHealthCheckMarker(ctx)

	// Apply timeout if configured (after adding health check marker)
	checkCtx := healthCheckCtx
	var cancel context.CancelFunc
	if h.timeout > 0 {
		checkCtx, cancel = context.WithTimeout(healthCheckCtx, h.timeout)
		defer cancel()
	}

	logger.Debugf("Performing health check for backend %s (%s)", target.WorkloadName, target.BaseURL)

	// Short-circuit health check if targeting ourselves
	// This prevents the server from trying to health check itself, which would work
	// but is wasteful and can cause connection issues during startup
	if h.selfURL != "" && h.isSelfCheck(target.BaseURL) {
		logger.Debugf("Skipping health check for backend %s - this is the server itself", target.WorkloadName)
		return vmcp.BackendHealthy, nil
	}

	// Track response time for degraded detection
	startTime := time.Now()

	// Use ListCapabilities as the health check - it performs:
	// 1. Client creation with transport setup
	// 2. MCP protocol initialization handshake
	// 3. Capabilities query (tools, resources, prompts)
	// This validates the full communication stack
	_, err := h.client.ListCapabilities(checkCtx, target)
	responseDuration := time.Since(startTime)

	if err != nil {
		// Categorize the error to determine health status
		status := categorizeError(err)
		logger.Debugf("Health check failed for backend %s: %v (status: %s, duration: %v)",
			target.WorkloadName, err, status, responseDuration)
		return status, fmt.Errorf("health check failed: %w", err)
	}

	// Check if response time indicates degraded performance
	if h.degradedThreshold > 0 && responseDuration > h.degradedThreshold {
		logger.Warnf("Health check succeeded for backend %s but response was slow: %v (threshold: %v) - marking as degraded",
			target.WorkloadName, responseDuration, h.degradedThreshold)
		return vmcp.BackendDegraded, nil
	}

	logger.Debugf("Health check succeeded for backend %s (duration: %v)", target.WorkloadName, responseDuration)
	return vmcp.BackendHealthy, nil
}

// categorizeError determines the appropriate health status based on the error type.
// This uses sentinel error checking with errors.Is() for type-safe error categorization.
// Falls back to string-based detection for backwards compatibility with non-wrapped errors.
func categorizeError(err error) vmcp.BackendHealthStatus {
	if err == nil {
		return vmcp.BackendHealthy
	}

	// 1. Type-safe detection: Check for sentinel errors using errors.Is()
	// BackendClient now wraps all errors with appropriate sentinel errors
	if errors.Is(err, vmcp.ErrAuthenticationFailed) || errors.Is(err, vmcp.ErrAuthorizationFailed) {
		return vmcp.BackendUnauthenticated
	}

	if errors.Is(err, vmcp.ErrTimeout) || errors.Is(err, vmcp.ErrCancelled) {
		return vmcp.BackendUnhealthy
	}

	if errors.Is(err, vmcp.ErrBackendUnavailable) {
		return vmcp.BackendUnhealthy
	}

	// 2. String-based detection: Fallback for backwards compatibility
	// This handles errors from sources that don't wrap with sentinel errors
	if vmcp.IsAuthenticationError(err) {
		return vmcp.BackendUnauthenticated
	}

	if vmcp.IsTimeoutError(err) || vmcp.IsConnectionError(err) {
		return vmcp.BackendUnhealthy
	}

	// Default to unhealthy for unknown errors
	return vmcp.BackendUnhealthy
}

// isSelfCheck checks if a backend URL matches the server's own URL.
// URLs are normalized before comparison to handle variations like:
// - http://127.0.0.1:PORT vs http://localhost:PORT
// - http://HOST:PORT vs http://HOST:PORT/
func (h *healthChecker) isSelfCheck(backendURL string) bool {
	if h.selfURL == "" || backendURL == "" {
		return false
	}

	// Normalize both URLs for comparison
	backendNormalized, err := NormalizeURLForComparison(backendURL)
	if err != nil {
		return false
	}

	selfNormalized, err := NormalizeURLForComparison(h.selfURL)
	if err != nil {
		return false
	}

	return backendNormalized == selfNormalized
}

// NormalizeURLForComparison normalizes a URL for comparison by:
// - Parsing and reconstructing the URL
// - Converting localhost/127.0.0.1 to a canonical form
// - Comparing only scheme://host:port (ignoring path, query, fragment)
// - Lowercasing scheme and host
// Exported for testing purposes
func NormalizeURLForComparison(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	// Validate that we have a scheme and host (basic URL validation)
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid URL: missing scheme or host")
	}

	// Normalize host: convert localhost to 127.0.0.1 for consistency
	host := strings.ToLower(u.Hostname())
	if host == "localhost" {
		host = "127.0.0.1"
	}

	// Reconstruct URL with normalized components (scheme://host:port only)
	// We ignore path, query, and fragment for comparison
	normalized := &url.URL{
		Scheme: strings.ToLower(u.Scheme),
	}
	if u.Port() != "" {
		normalized.Host = host + ":" + u.Port()
	} else {
		normalized.Host = host
	}

	return normalized.String(), nil
}
