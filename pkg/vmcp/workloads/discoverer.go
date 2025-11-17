// Package workloads provides the WorkloadDiscoverer interface for discovering
// backend workloads in both CLI and Kubernetes environments.
package workloads

import (
	"context"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// Discoverer is the interface for workload managers used by vmcp.
// This interface contains only the methods needed for backend discovery,
// allowing both CLI and Kubernetes managers to implement it.
//
//go:generate mockgen -destination=mocks/mock_discoverer.go -package=mocks github.com/stacklok/toolhive/pkg/vmcp/workloads Discoverer
type Discoverer interface {
	// ListWorkloadsInGroup returns all workload names that belong to the specified group
	ListWorkloadsInGroup(ctx context.Context, groupName string) ([]string, error)

	// GetWorkload retrieves workload details by name and converts it to a vmcp.Backend.
	// The returned Backend should have all fields populated except AuthStrategy and AuthMetadata,
	// which will be set by the discoverer based on the auth configuration.
	// Returns nil if the workload exists but is not accessible (e.g., no URL).
	GetWorkload(ctx context.Context, workloadName string) (*vmcp.Backend, error)
}
