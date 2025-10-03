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

	// RegistryAPIPort is the port number used by the registry API container
	RegistryAPIPort = 8080
	// RegistryAPIPortName is the name assigned to the registry API port
	RegistryAPIPortName = "http"

	// DefaultCPURequest is the default CPU request for the registry API container
	DefaultCPURequest = "100m"
	// DefaultMemoryRequest is the default memory request for the registry API container
	DefaultMemoryRequest = "128Mi"
	// DefaultCPULimit is the default CPU limit for the registry API container
	DefaultCPULimit = "500m"
	// DefaultMemoryLimit is the default memory limit for the registry API container
	DefaultMemoryLimit = "512Mi"

	// HealthCheckPath is the HTTP path for liveness probe checks
	HealthCheckPath = "/health"
	// ReadinessCheckPath is the HTTP path for readiness probe checks
	ReadinessCheckPath = "/readiness"
	// LivenessInitialDelay is the initial delay in seconds for liveness probes
	LivenessInitialDelay = 30
	// LivenessPeriod is the period in seconds for liveness probe checks
	LivenessPeriod = 10
	// ReadinessInitialDelay is the initial delay in seconds for readiness probes
	ReadinessInitialDelay = 5
	// ReadinessPeriod is the period in seconds for readiness probe checks
	ReadinessPeriod = 5

	// RegistryDataVolumeName is the name of the volume used for registry data
	RegistryDataVolumeName = "registry-data"
	// RegistryDataMountPath is the mount path for registry data in containers
	RegistryDataMountPath = "/data/registry"

	// DefaultServiceAccountName is the default service account used by registry API pods
	DefaultServiceAccountName = "toolhive-registry-api"
	// ServeCommand is the command used to start the registry API server
	ServeCommand = "serve"

	// DefaultReplicas is the default number of replicas for the registry API deployment
	DefaultReplicas = 1
)

//go:generate mockgen -destination=mocks/mock_manager.go -package=mocks -source=types.go Manager

// Manager handles registry API deployment operations
type Manager interface {
	// ReconcileAPIService orchestrates the deployment, service creation, and readiness checking for the registry API
	ReconcileAPIService(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) *mcpregistrystatus.Error

	// CheckAPIReadiness verifies that the deployed registry-API Deployment is ready
	CheckAPIReadiness(ctx context.Context, deployment *appsv1.Deployment) bool

	// IsAPIReady checks if the registry API deployment is ready and serving requests
	IsAPIReady(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) bool
}
