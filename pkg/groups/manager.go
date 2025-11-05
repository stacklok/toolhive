// Package groups provides functionality for managing logical groupings of MCP servers.
package groups

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	k8snamespace "github.com/stacklok/toolhive/pkg/container/kubernetes"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
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
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(mcpv1alpha1.AddToScheme(scheme))

	// Get Kubernetes config
	config, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get Kubernetes config: %w", err)
	}

	// Create controller-runtime client
	k8sClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// Detect namespace
	namespace := k8snamespace.GetCurrentNamespace()

	return NewCRDManager(k8sClient, namespace), nil
}
