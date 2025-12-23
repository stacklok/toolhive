// Package discovery provides lazy per-user capability discovery for vMCP servers.
package discovery

import (
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
)

// FilterHealthyBackends filters backends to only include those that can handle requests.
// Backends that are unhealthy or unauthenticated are excluded from the returned list.
//
// When healthProvider is nil (health monitoring disabled), all backends are returned.
//
// Filtering logic (uses health.IsBackendUsable):
//   - Include: Healthy, Degraded (slow but functional), Unknown (not yet determined)
//   - Exclude: Unhealthy, Unauthenticated (cannot be used)
//
// Returns the filtered backend list and logs excluded backends.
func FilterHealthyBackends(backends []vmcp.Backend, healthProvider health.StatusProvider) []vmcp.Backend {
	// If health monitoring is disabled, return all backends
	if healthProvider == nil {
		return backends
	}

	filtered := make([]vmcp.Backend, 0, len(backends))
	excludedCount := 0

	for i := range backends {
		backend := &backends[i]
		status, err := healthProvider.GetBackendStatus(backend.ID)

		// Include backend if:
		// 1. Health status cannot be determined (err != nil) - assume healthy during transitions
		// 2. Status indicates backend can handle requests (uses health.IsBackendUsable)
		if err != nil {
			// Backend not found in health monitor yet (new backend) - include it
			logger.Debugf("Backend %s not found in health monitor, including in capabilities", backend.Name)
			filtered = append(filtered, *backend)
			continue
		}

		if health.IsBackendUsable(status) {
			// Include usable backends (Healthy, Degraded, Unknown)
			filtered = append(filtered, *backend)
		} else {
			// Exclude unusable backends (Unhealthy, Unauthenticated)
			logger.Infof("Excluding backend %s from capabilities (status: %s)", backend.Name, status)
			excludedCount++
		}
	}

	if excludedCount > 0 {
		logger.Infof("Health filtering: %d backends included, %d backends excluded", len(filtered), excludedCount)
	}

	return filtered
}
