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
	"log/slog"
	"sort"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/headerforward/wirefmt"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
	workloadsmgr "github.com/stacklok/toolhive/pkg/workloads"
)

// backendDiscoverer discovers backend MCP servers using a WorkloadDiscoverer.
// This is a unified discoverer that works with both CLI and Kubernetes workloads.
type backendDiscoverer struct {
	workloadsManager workloads.Discoverer
	groupsManager    groups.Manager
	authConfig       *config.OutgoingAuthConfig
	excludedBackends map[string]struct{}
	staticBackends   []config.StaticBackendConfig // Pre-configured backends for static mode
	groupRef         string                       // Group reference for static mode metadata

	// headerForwardByBackend is keyed by the NORMALIZED backend name (the
	// suffix the operator emits in TOOLHIVE_HEADER_FORWARD_<entry>). The
	// canonical backend name from the static config is normalized on
	// lookup via wirefmt.NormalizeForEnvVar so the keying matches
	// the operator-side env-var encoding. Nil/empty when no entry declared
	// headerForward. Only populated in static mode — dynamic mode reads
	// headerForward directly from the MCPServerEntry CRD.
	headerForwardByBackend map[string]*vmcp.HeaderForwardConfig
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
		excludedBackends: outgoingAuthExclusionSet(authConfig),
		staticBackends:   nil, // Dynamic mode - discover backends at runtime
	}
}

// NewUnifiedBackendDiscovererWithStaticBackends creates a backend discoverer for static mode
// with pre-configured backends, eliminating the need for K8s API access.
//
// headerForwardByBackend carries per-backend HeaderForwardConfig (keyed by
// backend name) constructed at startup from the operator-emitted env vars.
// Pass nil when no entry in the group declares headerForward — the discoverer
// will simply leave Backend.HeaderForward nil for every backend.
func NewUnifiedBackendDiscovererWithStaticBackends(
	staticBackends []config.StaticBackendConfig,
	authConfig *config.OutgoingAuthConfig,
	groupRef string,
	headerForwardByBackend map[string]*vmcp.HeaderForwardConfig,
) BackendDiscoverer {
	return &backendDiscoverer{
		workloadsManager:       nil, // Not needed in static mode
		groupsManager:          nil, // Not needed in static mode
		authConfig:             authConfig,
		excludedBackends:       outgoingAuthExclusionSet(authConfig),
		staticBackends:         staticBackends,
		groupRef:               groupRef,
		headerForwardByBackend: headerForwardByBackend,
	}
}

// outgoingAuthExclusionSet builds the runtime deny set emitted by the
// operator for backends whose explicit authentication failed to resolve.
// Empty names are ignored because they cannot identify a workload.
func outgoingAuthExclusionSet(authConfig *config.OutgoingAuthConfig) map[string]struct{} {
	if authConfig == nil || len(authConfig.ExcludedBackends) == 0 {
		return nil
	}

	excluded := make(map[string]struct{}, len(authConfig.ExcludedBackends))
	for _, name := range authConfig.ExcludedBackends {
		if name != "" {
			excluded[name] = struct{}{}
		}
	}
	return excluded
}

func (d *backendDiscoverer) isBackendExcluded(name string) bool {
	_, excluded := d.excludedBackends[name]
	return excluded
}

func (d *backendDiscoverer) dependsOnFailedDefault(backend *vmcp.Backend) bool {
	return d.authConfig != nil && d.authConfig.DefaultAuthFailed && backend.AuthConfig == nil
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
		k8sDiscoverer, err := workloads.NewK8SDiscovererWithAuthConfig(authConfig) // Uses detected namespace for CLI usage
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

	slog.Info("discovering backends in group", "group", groupRef)

	// Static mode: Use pre-configured backends if available
	if len(d.staticBackends) > 0 {
		slog.Info("using pre-configured static backends (no K8s API access)", "count", len(d.staticBackends))
		return d.discoverFromStaticConfig(), nil
	}

	// If staticBackends was explicitly set (even if empty), but groupsManager is nil,
	// this discoverer was created for static mode with an empty backend list.
	// Return empty list instead of falling through to dynamic mode which would panic.
	if d.staticBackends != nil && d.groupsManager == nil {
		slog.Info("static mode with empty backend list, returning no backends")
		return []vmcp.Backend{}, nil
	}

	// Dynamic mode: Discover backends from K8s API at runtime
	slog.Info("dynamic mode: discovering backends from K8s API")

	// Verify that the group exists
	exists, err := d.groupsManager.Exists(ctx, groupRef)
	if err != nil {
		return nil, fmt.Errorf("failed to check if group exists: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("%w: %s", groups.ErrGroupNotFound, groupRef)
	}

	// Get all typedWorkloads in the group
	typedWorkloads, err := d.workloadsManager.ListWorkloadsInGroup(ctx, groupRef)
	if err != nil {
		return nil, fmt.Errorf("failed to list workloads in group: %w", err)
	}

	if len(typedWorkloads) == 0 {
		slog.Info("no workloads found in group", "group", groupRef)
		return []vmcp.Backend{}, nil
	}

	slog.Debug("found workloads in group, discovering backends", "count", len(typedWorkloads), "group", groupRef)

	backends = d.discoverWorkloadBackends(ctx, groupRef, typedWorkloads)

	if len(backends) == 0 {
		slog.Info("no accessible backends found in group (all workloads lack URLs)", "group", groupRef)
		return []vmcp.Backend{}, nil
	}

	slog.Info("discovered backends in group", "count", len(backends), "group", groupRef)
	return backends, nil
}

// discoverWorkloadBackends converts the listed group members while enforcing
// the same outgoing-auth exclusions and fallback rules used by the watch path.
func (d *backendDiscoverer) discoverWorkloadBackends(
	ctx context.Context,
	groupRef string,
	typedWorkloads []workloads.TypedWorkload,
) []vmcp.Backend {
	backends := make([]vmcp.Backend, 0, len(typedWorkloads))
	for _, workload := range typedWorkloads {
		if d.isBackendExcluded(workload.Name) {
			slog.Warn("skipping backend with unresolved outgoing authentication", "workload", workload.Name)
			continue
		}

		backend, err := d.workloadsManager.GetWorkloadAsVMCPBackend(ctx, workload)
		if err != nil {
			slog.Warn("failed to get workload, skipping", "workload", workload.Name, "error", err)
			continue
		}

		// Skip workloads that are not accessible (GetWorkload returns nil)
		if backend == nil {
			continue
		}

		// Apply authentication configuration to backend
		d.applyAuthConfigToBackend(backend, workload.Name)
		if d.dependsOnFailedDefault(backend) {
			slog.Warn("skipping backend that depends on unresolved default authentication", "workload", workload.Name)
			continue
		}

		// Set group metadata (override user labels to prevent conflicts)
		if backend.Metadata == nil {
			backend.Metadata = make(map[string]string)
		}
		backend.Metadata["group"] = groupRef

		backends = append(backends, *backend)
	}
	return backends
}

// applyAuthConfigToBackend applies authentication configuration to a backend based on the source mode.
// It determines whether to use discovered auth from the MCPServer or auth from the vMCP config.
//
// Auth resolution logic:
//   - "discovered" mode: Use a valid explicit per-backend override first, then
//     discovered auth, then the configured backend/default fallback
//   - "inline" mode (or ""): Always use config-based auth, ignore discovered auth
//   - unknown mode: Default to config-based auth for safety
//
// Config fallback resolution handles:
// 1. Backend-specific config (d.authConfig.Backends[backendName])
// 2. Default config fallback (d.authConfig.Default)
// 3. No auth if neither is configured
func (d *backendDiscoverer) applyAuthConfigToBackend(backend *vmcp.Backend, backendName string) {
	if d.authConfig == nil {
		return
	}

	switch d.authConfig.Source {
	case "discovered":
		if explicitAuth, ok := d.authConfig.ResolveExplicitForBackend(backendName); ok {
			backend.AuthConfig = explicitAuth
			slog.Debug("backend configured with explicit auth override",
				"backend", backendName, "strategy", explicitAuth.Type)
			return
		}

		if backend.AuthConfig != nil {
			// Keep the auth discovered from the backend resource.
			slog.Debug("backend using discovered auth strategy", "backend", backendName, "strategy", backend.AuthConfig.Type)
			return
		}

		// No backend auth was discovered. Fall back to the converted backend
		// entry (including type="discovered"'s unauthenticated fallback), then
		// Default, then no auth.
		if authConfig := d.authConfig.ResolveForBackend(backendName); authConfig != nil {
			backend.AuthConfig = authConfig
			slog.Debug("backend configured with fallback auth strategy from config",
				"backend", backendName, "strategy", authConfig.Type)
		}
		return
	case "inline", "":
		// For inline mode or empty source, always use config-based auth
		// Ignore any discovered auth from backends
	default:
		// Unknown source mode - default to config-based auth for safety
		slog.Warn("unknown auth source mode, defaulting to config-based auth", "source", d.authConfig.Source)
	}

	// Inline and unknown modes use config-based auth.
	if authConfig := d.authConfig.ResolveForBackend(backendName); authConfig != nil {
		backend.AuthConfig = authConfig
		slog.Debug("backend configured with auth strategy from config", "backend", backendName, "strategy", authConfig.Type)
	}
}

// discoverFromStaticConfig converts pre-configured static backends into vmcp.Backend objects
// for use in static mode where no K8s API access is available.
func (d *backendDiscoverer) discoverFromStaticConfig() []vmcp.Backend {
	backends := make([]vmcp.Backend, 0, len(d.staticBackends))

	for _, staticBackend := range d.staticBackends {
		if d.isBackendExcluded(staticBackend.Name) {
			slog.Warn("skipping static backend with unresolved outgoing authentication", "backend", staticBackend.Name)
			continue
		}

		backend := vmcp.Backend{
			ID:            staticBackend.Name,
			Name:          staticBackend.Name,
			BaseURL:       staticBackend.URL,
			TransportType: staticBackend.Transport,
			Type:          vmcp.BackendType(staticBackend.Type),
			CABundlePath:  staticBackend.CABundlePath,
			HealthStatus:  vmcp.BackendHealthy, // Assume healthy, actual health check happens later
			HeaderForward: d.headerForwardByBackend[wirefmt.NormalizeForEnvVar(staticBackend.Name)],
			Metadata:      staticBackend.Metadata,
		}

		// Apply auth configuration from OutgoingAuthConfig
		d.applyAuthConfigToBackend(&backend, staticBackend.Name)
		if d.dependsOnFailedDefault(&backend) {
			slog.Warn("skipping static backend that depends on unresolved default authentication",
				"backend", staticBackend.Name)
			continue
		}

		// Set group metadata (reserved key, always overridden)
		if backend.Metadata == nil {
			backend.Metadata = make(map[string]string)
		}
		// Warn if user provided a conflicting group value
		if existingGroup, exists := backend.Metadata["group"]; exists && existingGroup != d.groupRef {
			slog.Warn("backend has user-provided group metadata which will be overridden",
				"backend", staticBackend.Name, "existing_group", existingGroup, "new_group", d.groupRef)
		}
		backend.Metadata["group"] = d.groupRef

		backends = append(backends, backend)
		slog.Info("loaded static backend", "name", staticBackend.Name, "url", staticBackend.URL, "transport", staticBackend.Transport)
	}

	return backends
}
