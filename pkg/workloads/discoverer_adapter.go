// Package workloads contains high-level logic for managing the lifecycle of
// ToolHive-managed containers.
package workloads

import (
	"context"

	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpworkloads "github.com/stacklok/toolhive/pkg/vmcp/workloads"
)

// DiscovererAdapter wraps a DefaultManager to implement vmcpworkloads.Discoverer interface.
// This adapter is used in CLI context where only MCPServer workloads exist,
// converting the string-based Manager interface to the WorkloadInfo-based Discoverer interface.
type DiscovererAdapter struct {
	manager *DefaultManager
}

// NewDiscovererAdapter creates a new DiscovererAdapter wrapping the given DefaultManager.
func NewDiscovererAdapter(manager *DefaultManager) vmcpworkloads.Discoverer {
	return &DiscovererAdapter{manager: manager}
}

// ListWorkloadsInGroup returns all workloads that belong to the specified group.
// In CLI context, all workloads are MCPServers.
func (a *DiscovererAdapter) ListWorkloadsInGroup(ctx context.Context, groupName string) ([]vmcpworkloads.TypedWorkload, error) {
	names, err := a.manager.ListWorkloadsInGroup(ctx, groupName)
	if err != nil {
		return nil, err
	}

	workloads := make([]vmcpworkloads.TypedWorkload, len(names))
	for i, name := range names {
		workloads[i] = vmcpworkloads.TypedWorkload{
			Name: name,
			Type: vmcpworkloads.WorkloadTypeMCPServer,
		}
	}
	return workloads, nil
}

// GetWorkloadAsVMCPBackend retrieves workload details and converts it to a vmcp.Backend.
func (a *DiscovererAdapter) GetWorkloadAsVMCPBackend(
	ctx context.Context,
	workload vmcpworkloads.TypedWorkload) (*vmcp.Backend, error) {
	// In CLI context, we only have the name - the type is always MCPServer
	return a.manager.GetWorkloadAsVMCPBackend(ctx, workload.Name)
}
