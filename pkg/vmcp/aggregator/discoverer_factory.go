package aggregator

import (
	"context"
	"fmt"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/workloads"
	"github.com/stacklok/toolhive/pkg/workloads/k8s"
)

// NewBackendDiscoverer creates a BackendDiscoverer based on the runtime environment.
// It automatically detects whether to use CLI (Docker/Podman) or Kubernetes discoverer
// and creates the appropriate workloads manager.
//
// Parameters:
//   - ctx: Context for creating managers
//   - groupsManager: Manager for group operations (must already be initialized)
//   - authConfig: Outgoing authentication configuration for discovered backends
//
// Returns:
//   - BackendDiscoverer: The appropriate discoverer for the current runtime
//   - error: If manager creation fails
func NewBackendDiscoverer(
	ctx context.Context,
	groupsManager groups.Manager,
	authConfig *config.OutgoingAuthConfig,
) (BackendDiscoverer, error) {
	if rt.IsKubernetesRuntime() {
		k8sWorkloadsManager, err := k8s.NewManagerFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create Kubernetes workloads manager: %w", err)
		}
		return NewK8SBackendDiscoverer(k8sWorkloadsManager, groupsManager, authConfig), nil
	}

	cliWorkloadsManager, err := workloads.NewManager(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create CLI workloads manager: %w", err)
	}
	return NewCLIBackendDiscoverer(cliWorkloadsManager, groupsManager, authConfig), nil
}
