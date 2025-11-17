package workloads

import (
	"context"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/workloads/statuses"
)

// cliDiscoverer is a direct implementation of Discoverer for CLI workloads.
// It uses the status manager directly instead of going through workloads.Manager.
type cliDiscoverer struct {
	statusManager statuses.StatusManager
}

// NewCLIDiscoverer creates a new CLI workload discoverer that directly uses
// the status manager to discover workloads.
func NewCLIDiscoverer(statusManager statuses.StatusManager) Discoverer {
	return &cliDiscoverer{
		statusManager: statusManager,
	}
}

// ListWorkloadsInGroup returns all workload names that belong to the specified group.
func (d *cliDiscoverer) ListWorkloadsInGroup(ctx context.Context, groupName string) ([]string, error) {
	// List all workloads (including stopped ones)
	workloads, err := d.statusManager.ListWorkloads(ctx, true, nil)
	if err != nil {
		return nil, err
	}

	// Filter workloads that belong to the specified group
	var groupWorkloads []string
	for _, workload := range workloads {
		if workload.Group == groupName {
			groupWorkloads = append(groupWorkloads, workload.Name)
		}
	}

	return groupWorkloads, nil
}

// GetWorkload retrieves workload details by name and converts it to a vmcp.Backend.
func (d *cliDiscoverer) GetWorkload(ctx context.Context, workloadName string) (*vmcp.Backend, error) {
	workload, err := d.statusManager.GetWorkload(ctx, workloadName)
	if err != nil {
		return nil, err
	}

	// Skip workloads without a URL (not accessible)
	if workload.URL == "" {
		logger.Debugf("Skipping workload %s without URL", workloadName)
		return nil, nil
	}

	// Map workload status to backend health status
	healthStatus := mapCLIWorkloadStatusToHealth(workload.Status)

	// Use ProxyMode instead of TransportType to reflect how ToolHive is exposing the workload.
	// For stdio MCP servers, ToolHive proxies them via SSE or streamable-http.
	// ProxyMode tells us which transport the vmcp client should use.
	transportType := workload.ProxyMode
	if transportType == "" {
		// Fallback to TransportType if ProxyMode is not set (for direct transports)
		transportType = workload.TransportType.String()
	}

	backend := &vmcp.Backend{
		ID:            workload.Name,
		Name:          workload.Name,
		BaseURL:       workload.URL,
		TransportType: transportType,
		HealthStatus:  healthStatus,
		Metadata:      make(map[string]string),
	}

	// Copy user labels to metadata first
	for k, v := range workload.Labels {
		backend.Metadata[k] = v
	}

	// Set system metadata (these override user labels to prevent conflicts)
	backend.Metadata["tool_type"] = workload.ToolType
	backend.Metadata["workload_status"] = string(workload.Status)

	return backend, nil
}

// mapCLIWorkloadStatusToHealth converts a CLI WorkloadStatus to a backend health status.
func mapCLIWorkloadStatusToHealth(status rt.WorkloadStatus) vmcp.BackendHealthStatus {
	switch status {
	case rt.WorkloadStatusRunning:
		return vmcp.BackendHealthy
	case rt.WorkloadStatusUnhealthy:
		return vmcp.BackendUnhealthy
	case rt.WorkloadStatusStopped, rt.WorkloadStatusError, rt.WorkloadStatusStopping, rt.WorkloadStatusRemoving:
		return vmcp.BackendUnhealthy
	case rt.WorkloadStatusStarting, rt.WorkloadStatusUnknown:
		return vmcp.BackendUnknown
	case rt.WorkloadStatusUnauthenticated:
		return vmcp.BackendUnauthenticated
	default:
		return vmcp.BackendUnknown
	}
}
