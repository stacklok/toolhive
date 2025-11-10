// Package aggregator provides platform-agnostic backend discovery.
// This file contains the CLI-specific discoverer implementation.
package aggregator

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// cliBackendDiscoverer discovers backend MCP servers from CLI workloads (containers).
// It works with workloads.Manager and core.Workload.
type cliBackendDiscoverer struct {
	workloadsManager workloads.Manager
	groupsManager    groups.Manager
	authConfig       *config.OutgoingAuthConfig
}

// NewCLIBackendDiscoverer creates a new CLI backend discoverer.
func NewCLIBackendDiscoverer(
	workloadsManager workloads.Manager,
	groupsManager groups.Manager,
	authConfig *config.OutgoingAuthConfig,
) BackendDiscoverer {
	return &cliBackendDiscoverer{
		workloadsManager: workloadsManager,
		groupsManager:    groupsManager,
		authConfig:       authConfig,
	}
}

// Discover finds all backend workloads in the specified group.
func (d *cliBackendDiscoverer) Discover(ctx context.Context, groupRef string) ([]vmcp.Backend, error) {
	logger.Infof("Discovering CLI backends in group %s", groupRef)

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

		backend := d.convertCoreWorkload(workload, groupRef)
		if backend != nil {
			backends = append(backends, *backend)
		}
	}

	if len(backends) == 0 {
		logger.Infof("No accessible backends found in group %s (all workloads lack URLs)", groupRef)
		return []vmcp.Backend{}, nil
	}

	logger.Infof("Discovered %d backends in group %s", len(backends), groupRef)
	return backends, nil
}

// convertCoreWorkload converts a core.Workload to a vmcp.Backend.
func (d *cliBackendDiscoverer) convertCoreWorkload(workload core.Workload, groupRef string) *vmcp.Backend {
	// Skip workloads without a URL (not accessible)
	if workload.URL == "" {
		logger.Debugf("Skipping workload %s without URL", workload.Name)
		return nil
	}

	// Map workload status to backend health status
	healthStatus := mapWorkloadStatusToHealth(workload.Status)

	// Convert core.Workload to vmcp.Backend
	transportType := workload.ProxyMode
	if transportType == "" {
		// Fallback to TransportType if ProxyMode is not set (for direct transports)
		transportType = workload.TransportType.String()
	}

	backend := vmcp.Backend{
		ID:            workload.Name,
		Name:          workload.Name,
		BaseURL:       workload.URL,
		TransportType: transportType,
		HealthStatus:  healthStatus,
		Metadata:      make(map[string]string),
	}

	// Apply authentication configuration if provided
	authStrategy, authMetadata := d.authConfig.ResolveForBackend(workload.Name)
	backend.AuthStrategy = authStrategy
	backend.AuthMetadata = authMetadata
	if authStrategy != "" {
		logger.Debugf("Backend %s configured with auth strategy: %s", workload.Name, authStrategy)
	}

	// Copy user labels to metadata first
	for k, v := range workload.Labels {
		backend.Metadata[k] = v
	}

	// Set system metadata (these override user labels to prevent conflicts)
	backend.Metadata["group"] = groupRef
	backend.Metadata["tool_type"] = workload.ToolType
	backend.Metadata["workload_status"] = string(workload.Status)

	logger.Debugf("Discovered backend %s: %s (%s) with health status %s",
		backend.ID, backend.BaseURL, backend.TransportType, backend.HealthStatus)

	return &backend
}
