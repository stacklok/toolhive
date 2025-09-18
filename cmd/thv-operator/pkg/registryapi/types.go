package registryapi

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/mcpregistrystatus"
)

const (
	// registryAPIContainerName is the name of the registry-api container in deployments
	registryAPIContainerName = "registry-api"
)

// Manager handles registry API deployment operations
type Manager interface {
	// ReconcileAPIService orchestrates the deployment, service creation, and readiness checking for the registry API
	ReconcileAPIService(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry, statusCollector mcpregistrystatus.Collector) error

	// CheckAPIReadiness verifies that the deployed registry-API Deployment is ready
	CheckAPIReadiness(ctx context.Context, deployment *appsv1.Deployment) bool

	// IsAPIReady checks if the registry API deployment is ready and serving requests
	IsAPIReady(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) bool
}
