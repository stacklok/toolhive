// Package container provides utilities for managing containers,
// including creating, starting, stopping, and monitoring containers.
package container

import (
	"context"

	"github.com/stacklok/vibetool/pkg/container/docker"
	"github.com/stacklok/vibetool/pkg/container/runtime"
)

// Factory creates container runtimes
type Factory struct{}

// NewFactory creates a new container factory
func NewFactory() *Factory {
	return &Factory{}
}

// Create creates a container runtime
func (*Factory) Create(ctx context.Context) (runtime.Runtime, error) {
	// Try to create a container client
	client, err := docker.NewClient(ctx)
	if err != nil {
		return nil, err
	}

	return client, nil
}

// NewMonitor creates a new container monitor
func NewMonitor(rt runtime.Runtime, containerID, containerName string) runtime.Monitor {
	return docker.NewMonitor(rt, containerID, containerName)
}
