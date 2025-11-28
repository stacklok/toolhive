// Package aggregator provides platform-specific backend discovery implementations.
//
// This file contains:
//   - Unified backend discoverer implementation (works with both CLI and Kubernetes)
//   - Factory function to create BackendDiscoverer based on runtime environment
//   - WorkloadDiscoverer interface and implementations are in pkg/vmcp/workloads
//
// The BackendDiscoverer interface is defined in aggregator.go.
package aggregator

import (
	"context"
	"fmt"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
	workloadsmgr "github.com/stacklok/toolhive/pkg/workloads"
)

// backendDiscoverer discovers backend MCP servers using a WorkloadDiscoverer.
// This is a unified discoverer that works with both CLI and Kubernetes workloads.
type backendDiscoverer struct {
	workloadsManager workloads.Discoverer
	groupsManager    groups.Manager
	authConfig       *config.OutgoingAuthConfig
}

// NewUnifiedBackendDiscoverer creates a unified backend discoverer that works with both
// CLI and Kubernetes workloads through the WorkloadDiscoverer interface.
//
// The authConfig parameter configures authentication for discovered backends.
// If nil, backends will have no authentication configured.
func NewUnifiedBackendDiscoverer(
	workloadsManager workloads.Discoverer,
	groupsManager groups.Manager,
	authConfig *config.OutgoingAuthConfig,
) BackendDiscoverer {
	return &backendDiscoverer{
		workloadsManager: workloadsManager,
		groupsManager:    groupsManager,
		authConfig:       authConfig,
	}
}

// NewBackendDiscoverer creates a unified BackendDiscoverer based on the runtime environment.
// It automatically detects whether to use CLI (Docker/Podman) or Kubernetes workloads
// and creates the appropriate WorkloadDiscoverer implementation.
//
// Parameters:
//   - ctx: Context for creating managers
//   - groupsManager: Manager for group operations (must already be initialized)
//   - authConfig: Outgoing authentication configuration for discovered backends
//
// Returns:
//   - BackendDiscoverer: A unified discoverer that works with both CLI and Kubernetes workloads
//   - error: If manager creation fails
func NewBackendDiscoverer(
	ctx context.Context,
	groupsManager groups.Manager,
	authConfig *config.OutgoingAuthConfig,
) (BackendDiscoverer, error) {
	var workloadDiscoverer workloads.Discoverer

	if rt.IsKubernetesRuntime() {
		k8sDiscoverer, err := workloads.NewK8SDiscoverer() // Uses detected namespace for CLI usage
		if err != nil {
			return nil, fmt.Errorf("failed to create Kubernetes workload discoverer: %w", err)
		}
		workloadDiscoverer = k8sDiscoverer
	} else {
		manager, err := workloadsmgr.NewManager(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create workload manager: %w", err)
		}
		workloadDiscoverer = manager
	}
	return NewUnifiedBackendDiscoverer(workloadDiscoverer, groupsManager, authConfig), nil
}

// NewBackendDiscovererWithManager creates a unified BackendDiscoverer with a pre-configured
// WorkloadDiscoverer. This is useful for testing or when you already have a workload manager.
func NewBackendDiscovererWithManager(
	workloadManager workloads.Discoverer,
	groupsManager groups.Manager,
	authConfig *config.OutgoingAuthConfig,
) BackendDiscoverer {
	return NewUnifiedBackendDiscoverer(workloadManager, groupsManager, authConfig)
}

// Discover finds all backend workloads in the specified group.
// Returns all accessible backends with their health status marked based on workload status.
// The groupRef is the group name (e.g., "engineering-team").
func (d *backendDiscoverer) Discover(ctx context.Context, groupRef string) ([]vmcp.Backend, error) {
	logger.Infof("Discovering backends in group %s", groupRef)

	// Verify that the group exists
	exists, err := d.groupsManager.Exists(ctx, groupRef)
	if err != nil {
		return nil, fmt.Errorf("failed to check if group exists: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("group %s not found", groupRef)
	}

	// Get all workload names in the group
	workloadNames, err := d.workloadsManager.ListWorkloadsInGroup(ctx, groupRef)
	if err != nil {
		return nil, fmt.Errorf("failed to list workloads in group: %w", err)
	}

	if len(workloadNames) == 0 {
		logger.Infof("No workloads found in group %s", groupRef)
		return []vmcp.Backend{}, nil
	}

	logger.Debugf("Found %d workloads in group %s, discovering backends", len(workloadNames), groupRef)

	// Query each workload and convert to backend
	var backends []vmcp.Backend
	for _, name := range workloadNames {
		backend, err := d.workloadsManager.GetWorkloadAsVMCPBackend(ctx, name)
		if err != nil {
			logger.Warnf("Failed to get workload %s: %v, skipping", name, err)
			continue
		}

		// Skip workloads that are not accessible (GetWorkload returns nil)
		if backend == nil {
			continue
		}

		// Apply authentication configuration to backend
		d.applyAuthConfigToBackend(backend, name)

		// Set group metadata (override user labels to prevent conflicts)
		if backend.Metadata == nil {
			backend.Metadata = make(map[string]string)
		}
		backend.Metadata["group"] = groupRef

		backends = append(backends, *backend)
	}

	if len(backends) == 0 {
		logger.Infof("No accessible backends found in group %s (all workloads lack URLs)", groupRef)
		return []vmcp.Backend{}, nil
	}

	logger.Infof("Discovered %d backends in group %s", len(backends), groupRef)
	return backends, nil
}

// applyAuthConfigToBackend applies authentication configuration to a backend based on the source mode.
// It determines whether to use discovered auth from the MCPServer or auth from the vMCP config.
//
// Auth resolution logic:
// - "discovered" mode: Use discovered auth if available, otherwise fall back to Default or backend-specific config
// - "mixed" mode: Use discovered auth unless there's an explicit backend override in config
// - "inline" mode (or ""): Always use config-based auth, ignore discovered auth
// - unknown mode: Default to config-based auth for safety
//
// When useDiscoveredAuth is false, ResolveForBackend is called which handles:
// 1. Backend-specific config (d.authConfig.Backends[backendName])
// 2. Default config fallback (d.authConfig.Default)
// 3. No auth if neither is configured
func (d *backendDiscoverer) applyAuthConfigToBackend(backend *vmcp.Backend, backendName string) {
	if d.authConfig == nil {
		return
	}

	// Determine if we should use discovered auth or config-based auth
	var useDiscoveredAuth bool
	switch d.authConfig.Source {
	case "discovered":
		// In discovered mode, use auth discovered from MCPServer (if any exists)
		// If no auth is discovered, fall back to config-based auth via ResolveForBackend
		// which will use backend-specific config, then Default, then no auth
		useDiscoveredAuth = backend.AuthStrategy != ""
	case "mixed":
		// In mixed mode, use discovered auth as default, but allow config overrides
		// If there's no explicit config for this backend, use discovered auth
		_, hasExplicitConfig := d.authConfig.Backends[backendName]
		useDiscoveredAuth = !hasExplicitConfig && backend.AuthStrategy != ""
	case "inline", "":
		// For inline mode or empty source, always use config-based auth
		// Ignore any discovered auth from backends
		useDiscoveredAuth = false
	default:
		// Unknown source mode - default to config-based auth for safety
		logger.Warnf("Unknown auth source mode: %s, defaulting to config-based auth", d.authConfig.Source)
		useDiscoveredAuth = false
	}

	if useDiscoveredAuth {
		// Keep the auth discovered from MCPServer (already populated in backend)
		logger.Debugf("Backend %s using discovered auth strategy: %s", backendName, backend.AuthStrategy)
	} else {
		// Use auth from config (inline mode or explicit override in mixed mode)
		authStrategy, authMetadata := d.authConfig.ResolveForBackend(backendName)
		if authStrategy != "" {
			backend.AuthStrategy = authStrategy
			backend.AuthMetadata = authMetadata
			logger.Debugf("Backend %s configured with auth strategy from config: %s", backendName, authStrategy)
		}
	}
}
