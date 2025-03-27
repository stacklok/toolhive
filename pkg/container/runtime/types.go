// Package runtime provides interfaces and types for container runtimes,
// including creating, starting, stopping, and monitoring containers.
package runtime

import (
	"context"
	"io"
	"time"

	"github.com/stacklok/vibetool/pkg/permissions"
)

// ContainerInfo represents information about a container
type ContainerInfo struct {
	// ID is the container ID
	ID string
	// Name is the container name
	Name string
	// Image is the container image
	Image string
	// Status is the container status
	Status string
	// State is the container state
	State string
	// Created is the container creation timestamp
	Created time.Time
	// Labels is the container labels
	Labels map[string]string
	// Ports is the container port mappings
	Ports []PortMapping
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

// Runtime defines the interface for container runtimes
type Runtime interface {
	// CreateContainer creates a container without starting it
	// If options is nil, default options will be used
	//todo: make args a struct to reduce number of args (linter going crazy)
	CreateContainer(
		ctx context.Context,
		image, name string,
		command []string,
		envVars, labels map[string]string,
		permissionProfile *permissions.Profile,
		transportType string,
		options *CreateContainerOptions,
	) (string, error)

	// StartContainer starts a container
	StartContainer(ctx context.Context, containerID string) error

	// ListContainers lists containers
	ListContainers(ctx context.Context) ([]ContainerInfo, error)

	// StopContainer stops a container
	StopContainer(ctx context.Context, containerID string) error

	// RemoveContainer removes a container
	RemoveContainer(ctx context.Context, containerID string) error

	// ContainerLogs gets container logs
	ContainerLogs(ctx context.Context, containerID string) (string, error)

	// IsContainerRunning checks if a container is running
	IsContainerRunning(ctx context.Context, containerID string) (bool, error)

	// GetContainerInfo gets container information
	GetContainerInfo(ctx context.Context, containerID string) (ContainerInfo, error)

	// GetContainerIP gets container IP address
	GetContainerIP(ctx context.Context, containerID string) (string, error)

	// AttachContainer attaches to a container
	AttachContainer(ctx context.Context, containerID string) (io.WriteCloser, io.ReadCloser, error)

	// ImageExists checks if an image exists locally
	ImageExists(ctx context.Context, image string) (bool, error)

	// PullImage pulls an image from a registry
	PullImage(ctx context.Context, image string) error
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
}

// CreateContainerOptions represents options for creating a container
type CreateContainerOptions struct {
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
}

// PortBinding represents a host port binding
type PortBinding struct {
	// HostIP is the host IP to bind to (empty for all interfaces)
	HostIP string
	// HostPort is the host port to bind to (empty for random port)
	HostPort string
}

// NewCreateContainerOptions creates a new CreateContainerOptions with default values
func NewCreateContainerOptions() *CreateContainerOptions {
	return &CreateContainerOptions{
		ExposedPorts: make(map[string]struct{}),
		PortBindings: make(map[string][]PortBinding),
		AttachStdio:  false,
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
}
