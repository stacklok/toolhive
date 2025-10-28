package aggregator

import (
	"context"
	"fmt"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// cliBackendDiscoverer discovers backend MCP servers from Docker/Podman workloads in a group.
// This is the CLI version of BackendDiscoverer that uses the workloads.Manager.
type cliBackendDiscoverer struct {
	workloadsManager workloads.Manager
	groupsManager    groups.Manager
}

// NewCLIBackendDiscoverer creates a new CLI-based backend discoverer.
// It discovers workloads from Docker/Podman containers managed by ToolHive.
func NewCLIBackendDiscoverer(workloadsManager workloads.Manager, groupsManager groups.Manager) BackendDiscoverer {
	return &cliBackendDiscoverer{
		workloadsManager: workloadsManager,
		groupsManager:    groupsManager,
	}
}

// Discover finds all backend workloads in the specified group.
// Returns only healthy/running backends.
// The groupRef is the group name (e.g., "engineering-team").
func (d *cliBackendDiscoverer) Discover(ctx context.Context, groupRef string) ([]vmcp.Backend, error) {
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
		logger.Warnf("No workloads found in group %s", groupRef)
		return nil, ErrNoBackendsFound
	}

	logger.Debugf("Found %d workloads in group %s, filtering for healthy backends", len(workloadNames), groupRef)

	// Query each workload and filter for healthy ones
	var backends []vmcp.Backend
	for _, name := range workloadNames {
		workload, err := d.workloadsManager.GetWorkload(ctx, name)
		if err != nil {
			logger.Warnf("Failed to get workload %s: %v, skipping", name, err)
			continue
		}

		// Only include running workloads
		if workload.Status != rt.WorkloadStatusRunning {
			logger.Debugf("Skipping workload %s with status %s", name, workload.Status)
			continue
		}

		// Skip workloads without a URL (not accessible)
		if workload.URL == "" {
			logger.Debugf("Skipping workload %s without URL", name)
			continue
		}

		// Convert core.Workload to vmcp.Backend
		backend := vmcp.Backend{
			ID:            name,
			Name:          name,
			BaseURL:       workload.URL,
			TransportType: workload.TransportType.String(),
			HealthStatus:  vmcp.BackendHealthy,
			Metadata: map[string]string{
				"group":     groupRef,
				"tool_type": workload.ToolType,
			},
		}

		// Copy labels to metadata
		for k, v := range workload.Labels {
			backend.Metadata[k] = v
		}

		backends = append(backends, backend)
		logger.Debugf("Discovered backend %s: %s (%s)", backend.ID, backend.BaseURL, backend.TransportType)
	}

	if len(backends) == 0 {
		logger.Warnf("No healthy backends found in group %s", groupRef)
		return nil, ErrNoBackendsFound
	}

	logger.Infof("Discovered %d healthy backends in group %s", len(backends), groupRef)
	return backends, nil
}
