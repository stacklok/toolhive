// Package aggregator provides platform-agnostic backend discovery.
//
// The BackendDiscoverer interface is defined in aggregator.go.
// The unified implementation (works for both CLI and Kubernetes) is in this file.
package aggregator

import (
	"context"
	"fmt"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// backendDiscoverer discovers backend MCP servers from workloads in a group.
// It works with both CLI (Docker/Podman) and Kubernetes environments via the unified workloads.Manager interface.
// This is a platform-agnostic implementation that automatically adapts to the runtime environment.
type backendDiscoverer struct {
	workloadsManager workloads.Manager
	groupsManager    groups.Manager
	authConfig       *config.OutgoingAuthConfig
}

// NewBackendDiscoverer creates a new backend discoverer.
// It discovers workloads from containers (CLI) or MCPServer CRDs (Kubernetes) managed by ToolHive.
// The workloads.Manager automatically selects the appropriate storage driver based on the runtime environment.
//
// The authConfig parameter configures authentication for discovered backends.
// If nil, backends will have no authentication configured.
func NewBackendDiscoverer(
	workloadsManager workloads.Manager,
	groupsManager groups.Manager,
	authConfig *config.OutgoingAuthConfig,
) BackendDiscoverer {
	return &backendDiscoverer{
		workloadsManager: workloadsManager,
		groupsManager:    groupsManager,
		authConfig:       authConfig,
	}
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
		workload, err := d.workloadsManager.GetWorkload(ctx, name)
		if err != nil {
			logger.Warnf("Failed to get workload %s: %v, skipping", name, err)
			continue
		}

		// Skip workloads without a URL (not accessible)
		if workload.URL == "" {
			logger.Debugf("Skipping workload %s without URL", name)
			continue
		}

		// Map workload status to backend health status
		healthStatus := mapWorkloadStatusToHealth(workload.Status)

		// Convert core.Workload to vmcp.Backend
		// Use ProxyMode instead of TransportType to reflect how ToolHive is exposing the workload.
		// For stdio MCP servers, ToolHive proxies them via SSE or streamable-http.
		// ProxyMode tells us which transport the vmcp client should use.
		transportType := workload.ProxyMode
		if transportType == "" {
			// Fallback to TransportType if ProxyMode is not set (for direct transports)
			transportType = workload.TransportType.String()
		}

		backend := vmcp.Backend{
			ID:            name,
			Name:          name,
			BaseURL:       workload.URL,
			TransportType: transportType,
			HealthStatus:  healthStatus,
			Metadata:      make(map[string]string),
		}

		// Apply authentication configuration if provided
		authStrategy, authMetadata := d.authConfig.ResolveForBackend(name)
		backend.AuthStrategy = authStrategy
		backend.AuthMetadata = authMetadata
		if authStrategy != "" {
			logger.Debugf("Backend %s configured with auth strategy: %s", name, authStrategy)
		}

		// Copy user labels to metadata first
		for k, v := range workload.Labels {
			backend.Metadata[k] = v
		}

		// Set system metadata (these override user labels to prevent conflicts)
		backend.Metadata["group"] = groupRef
		backend.Metadata["tool_type"] = workload.ToolType
		backend.Metadata["workload_status"] = string(workload.Status)

		backends = append(backends, backend)
		logger.Debugf("Discovered backend %s: %s (%s) with health status %s",
			backend.ID, backend.BaseURL, backend.TransportType, backend.HealthStatus)
	}

	if len(backends) == 0 {
		logger.Infof("No accessible backends found in group %s (all workloads lack URLs)", groupRef)
		return []vmcp.Backend{}, nil
	}

	logger.Infof("Discovered %d backends in group %s", len(backends), groupRef)
	return backends, nil
}

// mapWorkloadStatusToHealth converts a workload status to a backend health status.
func mapWorkloadStatusToHealth(status rt.WorkloadStatus) vmcp.BackendHealthStatus {
	switch status {
	case rt.WorkloadStatusRunning:
		return vmcp.BackendHealthy
	case rt.WorkloadStatusUnhealthy:
		return vmcp.BackendUnhealthy
	case rt.WorkloadStatusStopped, rt.WorkloadStatusError, rt.WorkloadStatusStopping, rt.WorkloadStatusRemoving:
		return vmcp.BackendUnhealthy
	case rt.WorkloadStatusStarting, rt.WorkloadStatusUnknown:
		return vmcp.BackendUnknown
	default:
		return vmcp.BackendUnknown
	}
}
