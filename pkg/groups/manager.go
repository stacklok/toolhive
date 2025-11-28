// Package groups provides functionality for managing logical groupings of MCP servers.
package groups

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/k8s"
)

const (
	// DefaultGroupName is the name of the default group
	DefaultGroupName = "default"
)

// NewManager creates a new group manager based on the runtime environment:
// - In Kubernetes mode: returns a CRD-based manager that uses MCPGroup CRDs
// - In local mode: returns a CLI/filesystem-based manager
func NewManager() (Manager, error) {
	if rt.IsKubernetesRuntime() {
		return newCRDManager()
	}
	return NewCLIManager()
}

// newCRDManager creates a CRD-based group manager for Kubernetes environments
func newCRDManager() (Manager, error) {
	// Create a scheme for controller-runtime client
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add client-go scheme: %w", err)
	}
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add MCP v1alpha1 scheme: %w", err)
	}

	// Create controller-runtime client with custom scheme
	k8sClient, err := k8s.NewControllerRuntimeClient(scheme)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// Detect namespace
	namespace := k8s.GetCurrentNamespace()

	return NewCRDManager(k8sClient, namespace), nil
}
