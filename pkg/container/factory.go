// Package container provides utilities for managing containers,
// including creating, starting, stopping, and monitoring containers.
package container

import (
	"context"

	"github.com/stacklok/toolhive/pkg/container/docker"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
	"github.com/stacklok/toolhive/pkg/container/runtime"
)

// Factory creates container runtimes
type Factory struct{}

// NewFactory creates a new container factory
func NewFactory() *Factory {
	return &Factory{}
}

// Create creates a container runtime
func (*Factory) Create(ctx context.Context) (runtime.Runtime, error) {
	if !runtime.IsKubernetesRuntime() {
		client, err := docker.NewClient(ctx)
		if err != nil {
			return nil, err
		}
		return client, nil
	}

	client, err := kubernetes.NewClient(ctx)
	if err != nil {
		return nil, err
	}

	return client, nil
}

// NewMonitor creates a new container monitor
func NewMonitor(rt runtime.Runtime, containerName string) runtime.Monitor {
	return docker.NewMonitor(rt, containerName)
}
