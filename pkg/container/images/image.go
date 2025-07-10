// Package images handles container image management operations.
package images

import (
	"context"
	"github.com/stacklok/toolhive/pkg/container"

	"github.com/stacklok/toolhive/pkg/container/docker/sdk"
	"github.com/stacklok/toolhive/pkg/logger"
)

// ImageManager defines the interface for managing container images.
// It has been extracted from the runtime interface as part of
// ongoing refactoring. It may be merged into a more general container
// management interface in future.
type ImageManager interface {
	// ImageExists checks if an image exists locally
	ImageExists(ctx context.Context, image string) (bool, error)

	// PullImage pulls an image from a registry
	PullImage(ctx context.Context, image string) error

	// BuildImage builds a Docker image from a Dockerfile in the specified context directory
	BuildImage(ctx context.Context, contextDir, imageName string) error
}

// NewImageManager creates an instance of ImageManager appropriate
// for the current environment, or returns an error if it is not supported.
func NewImageManager(ctx context.Context) ImageManager {
	// Check if we are running in a Kubernetes environment
	if container.IsKubernetesRuntime() {
		logger.Debug("running in Kubernetes environment, using no-op image manager")
		return &NoopImageManager{}
	}

	// First try the registry-based implementation (no Docker daemon required)
	registryManager := NewRegistryImageManager()
	if registryManager != nil {
		logger.Debug("using registry-based image manager")
		return registryManager
	}

	// Fall back to Docker client if registry manager is not available
	dockerClient, _, _, err := sdk.NewDockerClient(ctx)
	if err != nil {
		logger.Debug("no docker runtime found, using no-op image manager")
		return &NoopImageManager{}
	}

	logger.Debug("using docker-based image manager")
	return NewDockerImageManager(dockerClient)
}

// NoopImageManager is a no-op implementation of ImageManager.
type NoopImageManager struct{}

// ImageExists always returns false for the no-op implementation.
func (*NoopImageManager) ImageExists(_ context.Context, _ string) (bool, error) {
	return false, nil
}

// PullImage does nothing for the no-op implementation.
func (*NoopImageManager) PullImage(_ context.Context, _ string) error {
	return nil
}

// BuildImage does nothing for the no-op implementation.
func (*NoopImageManager) BuildImage(_ context.Context, _, _ string) error {
	return nil
}
