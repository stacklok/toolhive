// Package container provides utilities for managing containers,
// including creating, starting, stopping, and monitoring containers.
package container

import (
	"context"
	"os"

	"github.com/StacklokLabs/toolhive/pkg/container/docker"
	"github.com/StacklokLabs/toolhive/pkg/container/kubernetes"
	"github.com/StacklokLabs/toolhive/pkg/container/runtime"
)

// Factory creates container runtimes
type Factory struct{}

// NewFactory creates a new container factory
func NewFactory() *Factory {
	return &Factory{}
}

// Create creates a container runtime
func (*Factory) Create(ctx context.Context) (runtime.Runtime, error) {
	if !isKubernetesRuntime() {
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
func NewMonitor(rt runtime.Runtime, containerID, containerName string) runtime.Monitor {
	return docker.NewMonitor(rt, containerID, containerName)
}

// isKubernetesRuntime returns true if the runtime is Kubernetes
// isn't the best way to do this, but for now it's good enough
func isKubernetesRuntime() bool {
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}
