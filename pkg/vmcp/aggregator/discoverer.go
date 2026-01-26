// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
	"sort"

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
	staticBackends   []config.StaticBackendConfig // Pre-configured backends for static mode
	groupRef         string                       // Group reference for static mode metadata
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
		staticBackends:   nil, // Dynamic mode - discover backends at runtime
	}
}

// NewUnifiedBackendDiscovererWithStaticBackends creates a backend discoverer for static mode
// with pre-configured backends, eliminating the need for K8s API access.
func NewUnifiedBackendDiscovererWithStaticBackends(
	staticBackends []config.StaticBackendConfig,
	authConfig *config.OutgoingAuthConfig,
	groupRef string,
) BackendDiscoverer {
	return &backendDiscoverer{
		workloadsManager: nil, // Not needed in static mode
		groupsManager:    nil, // Not needed in static mode
		authConfig:       authConfig,
		staticBackends:   staticBackends,
		groupRef:         groupRef,
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
		// Wrap CLI manager with adapter to implement Discoverer interface
		workloadDiscoverer = workloadsmgr.NewDiscovererAdapter(manager)
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
//
// In static mode (when staticBackends are configured), this returns pre-configured backends
// without any K8s API access. In dynamic mode, it discovers backends at runtime.
//
// Results are always sorted alphabetically by backend name to ensure deterministic ordering.
// This prevents non-deterministic ConfigMap content that would cause unnecessary
// deployment rollouts (pod cycling). See: https://github.com/stacklok/toolhive/issues/3448
func (d *backendDiscoverer) Discover(ctx context.Context, groupRef string) (backends []vmcp.Backend, err error) {
	// Sort backends by name before returning to ensure deterministic ordering
	defer func() {
		if len(backends) > 1 {
			sort.Slice(backends, func(i, j int) bool {
				return backends[i].Name < backends[j].Name
			})
		}
	}()

	logger.Infof("Discovering backends in group %s", groupRef)

	// Static mode: Use pre-configured backends if available
	if len(d.staticBackends) > 0 {
		logger.Infof("Using %d pre-configured static backends (no K8s API access)", len(d.staticBackends))
		return d.discoverFromStaticConfig()
	}

	// If staticBackends was explicitly set (even if empty), but groupsManager is nil,
	// this discoverer was created for static mode with an empty backend list.
	// Return empty list instead of falling through to dynamic mode which would panic.
	if d.staticBackends != nil && d.groupsManager == nil {
		logger.Infof("Static mode with empty backend list, returning no backends")
		return []vmcp.Backend{}, nil
	}

	// Dynamic mode: Discover backends from K8s API at runtime
	logger.Infof("Dynamic mode: discovering backends from K8s API")

	// Verify that the group exists
	exists, err := d.groupsManager.Exists(ctx, groupRef)
	if err != nil {
		return nil, fmt.Errorf("failed to check if group exists: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("group %s not found", groupRef)
	}

	// Get all typedWorkloads in the group
	typedWorkloads, err := d.workloadsManager.ListWorkloadsInGroup(ctx, groupRef)
	if err != nil {
		return nil, fmt.Errorf("failed to list workloads in group: %w", err)
	}

	if len(typedWorkloads) == 0 {
		logger.Infof("No workloads found in group %s", groupRef)
		return []vmcp.Backend{}, nil
	}

	logger.Debugf("Found %d workloads in group %s, discovering backends", len(typedWorkloads), groupRef)

	// Query each workload and convert to backend
	for _, workload := range typedWorkloads {
		backend, err := d.workloadsManager.GetWorkloadAsVMCPBackend(ctx, workload)
		if err != nil {
			logger.Warnf("Failed to get workload %s: %v, skipping", workload.Name, err)
			continue
		}

		// Skip workloads that are not accessible (GetWorkload returns nil)
		if backend == nil {
			continue
		}

		// Apply authentication configuration to backend
		d.applyAuthConfigToBackend(backend, workload.Name)

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
		useDiscoveredAuth = backend.AuthConfig != nil
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
		logger.Debugf("Backend %s using discovered auth strategy: %s", backendName, backend.AuthConfig.Type)
	} else {
		// Use auth from config (inline mode)
		authConfig := d.authConfig.ResolveForBackend(backendName)
		if authConfig != nil {
			backend.AuthConfig = authConfig
			logger.Debugf("Backend %s configured with auth strategy from config: %s", backendName, authConfig.Type)
		}
	}
}

// discoverFromStaticConfig converts pre-configured static backends into vmcp.Backend objects
// for use in static mode where no K8s API access is available.
func (d *backendDiscoverer) discoverFromStaticConfig() ([]vmcp.Backend, error) {
	backends := make([]vmcp.Backend, 0, len(d.staticBackends))

	for _, staticBackend := range d.staticBackends {
		backend := vmcp.Backend{
			ID:            staticBackend.Name,
			Name:          staticBackend.Name,
			BaseURL:       staticBackend.URL,
			TransportType: staticBackend.Transport,
			HealthStatus:  vmcp.BackendHealthy, // Assume healthy, actual health check happens later
			Metadata:      staticBackend.Metadata,
		}

		// Apply auth configuration from OutgoingAuthConfig
		d.applyAuthConfigToBackend(&backend, staticBackend.Name)

		// Set group metadata (reserved key, always overridden)
		if backend.Metadata == nil {
			backend.Metadata = make(map[string]string)
		}
		// Warn if user provided a conflicting group value
		if existingGroup, exists := backend.Metadata["group"]; exists && existingGroup != d.groupRef {
			logger.Warnf("Backend %s has user-provided group metadata '%s' which will be overridden with '%s'",
				staticBackend.Name, existingGroup, d.groupRef)
		}
		backend.Metadata["group"] = d.groupRef

		backends = append(backends, backend)
		logger.Infof("Loaded static backend: %s (url=%s, transport=%s)",
			staticBackend.Name, staticBackend.URL, staticBackend.Transport)
	}

	return backends, nil
}
