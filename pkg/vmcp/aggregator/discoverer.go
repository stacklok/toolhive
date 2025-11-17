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

	ct "github.com/stacklok/toolhive/pkg/container"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
	"github.com/stacklok/toolhive/pkg/workloads/statuses"
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
		k8sDiscoverer, err := workloads.NewK8SDiscoverer()
		if err != nil {
			return nil, fmt.Errorf("failed to create Kubernetes workload discoverer: %w", err)
		}
		workloadDiscoverer = k8sDiscoverer
	} else {
		// Create runtime and status manager for CLI workloads
		runtime, err := ct.NewFactory().Create(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create runtime: %w", err)
		}

		statusManager, err := statuses.NewStatusManager(runtime)
		if err != nil {
			return nil, fmt.Errorf("failed to create status manager: %w", err)
		}

		workloadDiscoverer = workloads.NewCLIDiscoverer(statusManager)
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
		backend, err := d.workloadsManager.GetWorkload(ctx, name)
		if err != nil {
			logger.Warnf("Failed to get workload %s: %v, skipping", name, err)
			continue
		}

		// Skip workloads that are not accessible (GetWorkload returns nil)
		if backend == nil {
			continue
		}

		// Apply authentication configuration if provided
		authStrategy, authMetadata := d.authConfig.ResolveForBackend(name)
		backend.AuthStrategy = authStrategy
		backend.AuthMetadata = authMetadata
		if authStrategy != "" {
			logger.Debugf("Backend %s configured with auth strategy: %s", name, authStrategy)
		}

		// Set group metadata (override user labels to prevent conflicts)
		backend.Metadata["group"] = groupRef

		logger.Debugf("Discovered backend %s: %s (%s) with health status %s",
			backend.ID, backend.BaseURL, backend.TransportType, backend.HealthStatus)

		backends = append(backends, *backend)
	}

	if len(backends) == 0 {
		logger.Infof("No accessible backends found in group %s (all workloads lack URLs)", groupRef)
		return []vmcp.Backend{}, nil
	}

	logger.Infof("Discovered %d backends in group %s", len(backends), groupRef)
	return backends, nil
}
