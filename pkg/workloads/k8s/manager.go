// Package k8s provides Kubernetes-specific workload management.
package k8s

import (
	"context"
)

// Manager manages MCPServer CRD workloads in Kubernetes.
// This interface is separate from workloads.Manager to avoid coupling Kubernetes workloads
// to the CLI container runtime interface.
//
//go:generate mockgen -destination=mocks/mock_manager.go -package=mocks github.com/stacklok/toolhive/pkg/workloads/k8s Manager
type Manager interface {
	// GetWorkload retrieves an MCPServer CRD by name
	GetWorkload(ctx context.Context, workloadName string) (Workload, error)

	// ListWorkloads lists all MCPServer CRDs, optionally filtered by labels
	// The `listAll` parameter determines whether to include workloads that are not running
	// The optional `labelFilters` parameter allows filtering workloads by labels (format: key=value)
	ListWorkloads(ctx context.Context, listAll bool, labelFilters ...string) ([]Workload, error)

	// ListWorkloadsInGroup returns all workload names that belong to the specified group
	ListWorkloadsInGroup(ctx context.Context, groupName string) ([]string, error)

	// DoesWorkloadExist checks if an MCPServer CRD with the given name exists
	DoesWorkloadExist(ctx context.Context, workloadName string) (bool, error)

	// MoveToGroup moves the specified workloads from one group to another by updating their GroupRef
	MoveToGroup(ctx context.Context, workloadNames []string, groupFrom string, groupTo string) error

	// The following operations are not supported in Kubernetes mode (operator manages lifecycle):
	// - RunWorkload: Workloads are created via MCPServer CRDs
	// - RunWorkloadDetached: Workloads are created via MCPServer CRDs
	// - StopWorkloads: Use kubectl to manage MCPServer CRDs
	// - DeleteWorkloads: Use kubectl to manage MCPServer CRDs
	// - RestartWorkloads: Use kubectl to manage MCPServer CRDs
	// - UpdateWorkload: Update MCPServer CRD directly
	// - GetLogs: Use 'kubectl logs <pod-name> -n <namespace>' to retrieve logs
	// - GetProxyLogs: Use 'kubectl logs <pod-name> -c proxy -n <namespace>' to retrieve proxy logs
}
