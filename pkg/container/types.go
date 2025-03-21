package container

import (
	"context"
	"io"
	"time"
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
	CreateContainer(ctx context.Context, image, name string, command []string, envVars, labels map[string]string, permissionConfig PermissionConfig) (string, error)

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
}

// RuntimeType represents the type of container runtime
type RuntimeType string

const (
	// RuntimeTypePodman represents the Podman runtime
	RuntimeTypePodman RuntimeType = "podman"
	// RuntimeTypeDocker represents the Docker runtime
	RuntimeTypeDocker RuntimeType = "docker"
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

// Mount represents a volume mount
type Mount struct {
	// Source is the source path on the host
	Source string
	// Target is the target path in the container
	Target string
	// ReadOnly indicates if the mount is read-only
	ReadOnly bool
}