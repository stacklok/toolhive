// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registryapi

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

const (
	// RegistryAPIContainerName is the name of the registry-api container in deployments
	RegistryAPIContainerName = "registry-api"

	// RegistryAPIPort is the port number used by the registry API container
	RegistryAPIPort = 8080
	// RegistryAPIPortName is the name assigned to the registry API port
	RegistryAPIPortName = "http"
	// RegistryAPIHealthPort is the port of the registry API's internal HTTP
	// listener that serves liveness and readiness probes. Introduced in
	// toolhive-registry-server v1.1.0 to separate probe traffic from the
	// public API listener on RegistryAPIPort.
	RegistryAPIHealthPort = 8081

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

	// RegistryServerConfigVolumeName is the name of the volume used for registry server config
	RegistryServerConfigVolumeName = "registry-server-config"

	// ServeCommand is the command used to start the registry API server
	ServeCommand = "serve"

	// registryAPIResourceSuffix is the suffix used for registry API resources
	registryAPIResourceSuffix = "-registry-api"

	// DefaultReplicas is the default number of replicas for the registry API deployment
	DefaultReplicas = 1

	// PGPass volume and path constants

	// PGPassSecretVolumeName is the name of the volume for the pgpass secret
	PGPassSecretVolumeName = "pgpass-secret"
	// PGPassVolumeName is the name of the emptyDir volume for the prepared pgpass file
	PGPassVolumeName = "pgpass"
	// PGPassInitContainerName is the name of the init container that sets up the pgpass file
	PGPassInitContainerName = "setup-pgpass"
	// pgpassInitContainerImage is the image used by the init container.
	// Using Chainguard's busybox which runs as nonroot (65532) by default,
	// matching the typical app user so no chown is needed.
	// nolint:gosec // G101: This is a container image reference, not a credential
	pgpassInitContainerImage = "cgr.dev/chainguard/busybox:latest"
	// pgpassSecretMountPath is the path where the secret is mounted in the init container
	// nolint:gosec // G101: This is a file path, not a credential
	pgpassSecretMountPath = "/secret"
	// pgpassEmptyDirMountPath is the path where the emptyDir is mounted
	// nolint:gosec // G101: This is a file path, not a credential
	pgpassEmptyDirMountPath = "/pgpass"
	// PGPassAppUserMountPath is the path where the pgpass file is mounted in the app container
	// nolint:gosec // G101: This is a file path, not a credential
	PGPassAppUserMountPath = "/home/appuser/.pgpass"
	// pgpassFileName is the name of the pgpass file
	pgpassFileName = ".pgpass"
	// pgpassEnvVar is the environment variable name for the pgpass file path
	pgpassEnvVar = "PGPASSFILE"
)

// Error represents a structured error with condition information for operator components
type Error struct {
	Err             error
	Message         string
	ConditionReason string
}

func (e *Error) Error() string {
	return e.Message
}

func (e *Error) Unwrap() error {
	return e.Err
}

//go:generate mockgen -destination=mocks/mock_manager.go -package=mocks -source=types.go Manager

// Manager handles registry API deployment operations
type Manager interface {
	// ReconcileAPIService orchestrates the deployment, service creation, and readiness checking for the registry API
	ReconcileAPIService(ctx context.Context, mcpRegistry *mcpv1beta1.MCPRegistry) *Error

	// CheckAPIReadiness verifies that the deployed registry-API Deployment is ready
	CheckAPIReadiness(ctx context.Context, deployment *appsv1.Deployment) bool

	// IsAPIReady checks if the registry API deployment is ready and serving requests
	IsAPIReady(ctx context.Context, mcpRegistry *mcpv1beta1.MCPRegistry) bool

	// GetReadyReplicas returns the number of ready replicas for the registry API deployment
	GetReadyReplicas(ctx context.Context, mcpRegistry *mcpv1beta1.MCPRegistry) int32

	// GetAPIStatus returns the readiness state and ready replica count from a single Deployment fetch
	GetAPIStatus(ctx context.Context, mcpRegistry *mcpv1beta1.MCPRegistry) (ready bool, readyReplicas int32)
}

// GetServiceAccountName returns the service account name for a given MCPRegistry.
// The name follows the pattern: {registry-name}-registry-api
func GetServiceAccountName(mcpRegistry *mcpv1beta1.MCPRegistry) string {
	return mcpRegistry.Name + registryAPIResourceSuffix
}
