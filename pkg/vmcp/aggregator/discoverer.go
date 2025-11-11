// Package aggregator provides platform-agnostic backend discovery.
//
// The BackendDiscoverer interface is defined in aggregator.go.
// This file contains the factory function that selects the appropriate discoverer
// based on the runtime environment (CLI or Kubernetes).
package aggregator

import (
	"fmt"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// NewBackendDiscoverer creates a new backend discoverer based on the runtime environment.
// It accepts interface{} for workloadsManager to handle both workloads.Manager (CLI) and workloads.K8SManager (Kubernetes).
// Type assertion happens once in this factory, not in discovery logic.
//
// The authConfig parameter configures authentication for discovered backends.
// If nil, backends will have no authentication configured.
func NewBackendDiscoverer(
	workloadsManager interface{},
	groupsManager groups.Manager,
	authConfig *config.OutgoingAuthConfig,
) (BackendDiscoverer, error) {
	if rt.IsKubernetesRuntime() {
		k8sMgr, ok := workloadsManager.(workloads.K8SManager)
		if !ok {
			return nil, fmt.Errorf("expected workloads.K8SManager in Kubernetes mode, got %T", workloadsManager)
		}
		return NewK8SBackendDiscoverer(k8sMgr, groupsManager, authConfig), nil
	}

	cliMgr, ok := workloadsManager.(workloads.Manager)
	if !ok {
		return nil, fmt.Errorf("expected workloads.Manager in CLI mode, got %T", workloadsManager)
	}
	return NewCLIBackendDiscoverer(cliMgr, groupsManager, authConfig), nil
}
