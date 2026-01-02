// Package discovery provides lazy per-user capability discovery for vMCP servers.
package discovery

import (
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
)

// FilterHealthyBackends filters backends based on health status and partial failure mode.
// Backends that are unhealthy or unauthenticated are excluded from the returned list.
//
// When healthProvider is nil (health monitoring disabled), all backends are returned.
//
// Filtering logic depends on the partial failure mode:
//   - "fail" mode (strict): Include Healthy, Unknown; Exclude Degraded, Unhealthy, Unauthenticated
//   - "best_effort" mode (lenient): Include Healthy, Degraded, Unknown; Exclude Unhealthy, Unauthenticated
//
// Returns the filtered backend list and logs excluded backends.
func FilterHealthyBackends(
	backends []vmcp.Backend,
	healthProvider health.StatusProvider,
	mode string,
) []vmcp.Backend {
	// If health monitoring is disabled, return all backends
	// Note: Check both interface nil and typed nil (Go interface semantics)
	if !health.IsProviderInitialized(healthProvider) {
		return backends
	}

	// Default mode if not specified
	if mode == "" {
		mode = "fail" // Default to strict mode for safety
		logger.Debugf("No partial failure mode specified, using default: %s", mode)
	}

	filtered := make([]vmcp.Backend, 0, len(backends))
	excludedCount := 0

	for i := range backends {
		backend := &backends[i]
		status, err := healthProvider.GetBackendStatus(backend.ID)

		// Include backend if:
		// 1. Health status cannot be determined (err != nil) - assume healthy during transitions
		// 2. Status indicates backend can handle requests in the given mode
		if err != nil {
			// Backend not found in health monitor yet (new backend) - include it
			logger.Debugf("Backend %s not found in health monitor, including in capabilities", backend.Name)
			filtered = append(filtered, *backend)
			continue
		}

		if health.IsBackendUsableInMode(status, mode) {
			// Include usable backends based on mode
			filtered = append(filtered, *backend)
		} else {
			// Exclude unusable backends
			logger.Infof("Excluding backend %s from capabilities (status: %s, mode: %s)",
				backend.Name, status, mode)
			excludedCount++
		}
	}

	if excludedCount > 0 {
		logger.Infof("Health filtering (%s mode): %d backends included, %d backends excluded",
			mode, len(filtered), excludedCount)
	}

	return filtered
}
