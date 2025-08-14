// Package runtime provides interfaces and types for container runtimes,
// including creating, starting, stopping, and monitoring containers.
package runtime

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/viper"
	"github.com/stacklok/toolhive/pkg/ignore"
	"github.com/stacklok/toolhive/pkg/permissions"
)

// WorkloadStatus is an enum representing the possible statuses of a workload.
type WorkloadStatus string

const (
	// WorkloadStatusRunning indicates that the workload is currently running.
	WorkloadStatusRunning WorkloadStatus = "running"
	// WorkloadStatusStopped indicates that the workload is stopped.
	WorkloadStatusStopped WorkloadStatus = "stopped"
	// WorkloadStatusError indicates that the workload has encountered an error
	// during creation/stop/restart/delete.
	WorkloadStatusError WorkloadStatus = "error"
	// WorkloadStatusStarting indicates that the workload is being started.
	WorkloadStatusStarting WorkloadStatus = "starting"
	// WorkloadStatusStopping indicates that the workload is being stopped.
	WorkloadStatusStopping WorkloadStatus = "stopping"
	// WorkloadStatusUnhealthy indicates that the workload is running, but is
	// in an inconsistent state which prevents normal operation.
	WorkloadStatusUnhealthy WorkloadStatus = "unhealthy"
	// WorkloadStatusRemoving indicates that the workload is being removed.
	WorkloadStatusRemoving WorkloadStatus = "removing"
	// WorkloadStatusUnknown indicates that the workload status is unknown.
	WorkloadStatusUnknown WorkloadStatus = "unknown"
)

// ContainerInfo represents information about a container
// TODO: Consider merging this with workloads.Workload
type ContainerInfo struct {
	// Name is the container name
	Name string
	// Image is the container image
	Image string
	// Status is the container status
	// This is usually some human-readable context.
	Status string
	// State is the container state
	State WorkloadStatus
	// Created is the container creation timestamp
	Created time.Time
	// Labels is the container labels
	Labels map[string]string
	// Ports is the container port mappings
	Ports []PortMapping
}

// IsRunning returns true if the container is currently running.
func (c *ContainerInfo) IsRunning() bool {
	return c.State == WorkloadStatusRunning
}

// PortMapping represents a port mapping for a container
type PortMapping struct {
	// ContainerPort is the port inside the container
	ContainerPort int
	// HostPort is the port on the host
	HostPort int
	// Protocol is the protocol (tcp, udp)
	Protocol string
}

// Deployer contains the methods to start and stop a workload.
// This is intended as a subset of the Runtime interface for
// the runner code.
type Deployer interface {
	// DeployWorkload creates and starts a complete workload deployment.
	// This includes the primary container, any required sidecars, networking setup,
	// volume mounts, and service configuration. The workload is started as part
	// of this operation, making it immediately available for use.
	//
	// Parameters:
	// - image: The primary container image to deploy
	// - name: The workload name (used for identification and networking)
	// - command: Command to run in the primary container
	// - envVars: Environment variables for the primary container
	// - labels: Labels to apply to all workload components
	// - permissionProfile: Security and permission configuration
	// - transportType: Communication transport (sse, stdio, etc.)
	// - options: Additional deployment options (ports, sidecars, etc.)
	//
	// Returns the workload ID for subsequent operations.
	// If options is nil, default options will be used.
	//todo: make args a struct to reduce number of args (linter going crazy)
	DeployWorkload(
		ctx context.Context,
		image, name string,
		command []string,
		envVars, labels map[string]string,
		permissionProfile *permissions.Profile,
		transportType string,
		options *DeployWorkloadOptions,
		isolateNetwork bool,
	) (int, error)

	// StopWorkload gracefully stops a running workload and all its components.
	// This includes stopping the primary container, sidecars, and cleaning up
	// any associated network resources. The workload remains available for restart.
	StopWorkload(ctx context.Context, workloadName string) error

	// AttachToWorkload establishes a direct connection to the primary container
	// of the workload for interactive communication. This is typically used
	// for stdio transport where direct input/output streaming is required.
	AttachToWorkload(ctx context.Context, workloadName string) (io.WriteCloser, io.ReadCloser, error)

	// IsWorkloadRunning checks if a workload is currently running and healthy.
	// This verifies that the primary container is running and that any
	// required sidecars are also operational.
	IsWorkloadRunning(ctx context.Context, workloadName string) (bool, error)
}

// Runtime defines the interface for container runtimes that manage workloads.
//
// A workload in ToolHive represents a complete deployment unit that may consist of:
// - Primary MCP server container
// - Sidecar containers (for logging, monitoring, proxying, etc.)
// - Network configurations and port mappings
// - Volume mounts and storage
// - Service discovery and load balancing components
// - Security policies and permission profiles
//
// This is a departure from simple container management, as modern deployments
// often require orchestrating multiple interconnected components that work
// together to provide a complete service.
//
//go:generate mockgen -destination=mocks/mock_runtime.go -package=mocks -source=types.go Runtime
type Runtime interface {
	Deployer

	// ListWorkloads lists all deployed workloads managed by this runtime.
	// Returns information about each workload including its components,
	// status, and resource usage.
	ListWorkloads(ctx context.Context) ([]ContainerInfo, error)

	// RemoveWorkload completely removes a workload and all its components.
	// This includes removing containers, cleaning up networks, volumes,
	// and any other resources associated with the workload. This operation
	// is irreversible.
	RemoveWorkload(ctx context.Context, workloadName string) error

	// GetWorkloadLogs retrieves logs from the primary container of the workload.
	// If follow is true, the logs will be streamed continuously.
	// For workloads with multiple containers, this returns logs from the
	// main MCP server container.
	GetWorkloadLogs(ctx context.Context, workloadName string, follow bool) (string, error)

	// GetWorkloadInfo retrieves detailed information about a workload.
	// This includes status, resource usage, network configuration,
	// and metadata about all components in the workload.
	GetWorkloadInfo(ctx context.Context, workloadName string) (ContainerInfo, error)

	// IsRunning checks the health of the container runtime.
	// This is used to verify that the runtime is operational and can manage workloads.
	IsRunning(ctx context.Context) error
}

// Monitor defines the interface for container monitoring
type Monitor interface {
	// StartMonitoring starts monitoring the container
	// Returns a channel that will receive an error if the container exits unexpectedly
	StartMonitoring(ctx context.Context) (<-chan error, error)

	// StopMonitoring stops monitoring the container
	StopMonitoring()
}

// Type represents the type of container runtime
type Type string

const (
	// TypePodman represents the Podman runtime
	TypePodman Type = "podman"
	// TypeDocker represents the Docker runtime
	TypeDocker Type = "docker"
	// TypeKubernetes represents the Kubernetes runtime
	TypeKubernetes Type = "kubernetes"
)

// MountType represents the type of mount
type MountType string

const (
	// MountTypeBind represents a bind mount
	MountTypeBind MountType = "bind"
	// MountTypeTmpfs represents a tmpfs mount
	MountTypeTmpfs MountType = "tmpfs"
)

// String returns the string representation of the mount type
func (mt MountType) String() string {
	return string(mt)
}

// PermissionConfig represents container permission configuration
type PermissionConfig struct {
	// Mounts is the list of volume mounts
	Mounts []Mount
	// NetworkMode is the network mode
	NetworkMode string
	// CapDrop is the list of capabilities to drop
	CapDrop []string
	// CapAdd is the list of capabilities to add
	CapAdd []string
	// SecurityOpt is the list of security options
	SecurityOpt []string
	// Privileged indicates whether the container should run in privileged mode
	Privileged bool
}

// DeployWorkloadOptions represents configuration options for deploying a workload.
// These options control how the workload is deployed, including networking,
// platform-specific configurations, and communication settings.
type DeployWorkloadOptions struct {
	// ExposedPorts is a map of container ports to expose
	// The key is in the format "port/protocol" (e.g., "8080/tcp")
	// The value is an empty struct (not used)
	ExposedPorts map[string]struct{}

	// PortBindings is a map of container ports to host ports
	// The key is in the format "port/protocol" (e.g., "8080/tcp")
	// The value is a slice of host port bindings
	PortBindings map[string][]PortBinding

	// AttachStdio indicates whether to attach stdin/stdout/stderr
	// This is typically set to true for stdio transport
	AttachStdio bool

	// K8sPodTemplatePatch is a JSON string to patch the Kubernetes pod template
	// Only applicable when using Kubernetes runtime
	K8sPodTemplatePatch string

	// SSEHeadlessServiceName is the name of the Kubernetes service to use for the workload
	// Only applicable when using Kubernetes runtime and SSE transport
	SSEHeadlessServiceName string

	// IgnoreConfig contains configuration for ignore patterns and tmpfs overlays
	// Used to filter bind mount contents by hiding sensitive files
	IgnoreConfig *ignore.Config
}

// PortBinding represents a host port binding
type PortBinding struct {
	// HostIP is the host IP to bind to (empty for all interfaces)
	HostIP string
	// HostPort is the host port to bind to (empty for random port)
	HostPort string
}

// NewDeployWorkloadOptions creates a new DeployWorkloadOptions with default values.
// This provides a baseline configuration suitable for most workload deployments,
// with empty port mappings and standard communication settings.
func NewDeployWorkloadOptions() *DeployWorkloadOptions {
	return &DeployWorkloadOptions{
		ExposedPorts:        make(map[string]struct{}),
		PortBindings:        make(map[string][]PortBinding),
		AttachStdio:         false,
		K8sPodTemplatePatch: "",
	}
}

// Mount represents a volume mount
type Mount struct {
	// Source is the source path on the host
	Source string
	// Target is the target path in the container
	Target string
	// ReadOnly indicates if the mount is read-only
	ReadOnly bool
	// Type is the mount type (bind or tmpfs)
	Type MountType
}

// IsKubernetesRuntime returns true if the runtime is Kubernetes
// isn't the best way to do this, but for now it's good enough
func IsKubernetesRuntime() bool {
	// Check explicit flag first
	if runtimeFlag := viper.GetString("runtime"); runtimeFlag != "" {
		return runtimeFlag == "kubernetes"
	}
	// Fall back to environment detection (original logic)
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}

// Common errors
var (
	// ErrWorkloadNotFound indicates that the specified workload was not found.
	ErrWorkloadNotFound = fmt.Errorf("workload not found")
)
