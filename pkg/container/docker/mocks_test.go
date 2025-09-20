package docker

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// fakeDockerAPI provides a minimal test double for dockerAPI used by Client.
// Centralized here for reuse across tests.
type fakeDockerAPI struct {
	listFunc    func(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	inspectFunc func(ctx context.Context, id string) (container.InspectResponse, error)
	stopFunc    func(ctx context.Context, containerID string, options container.StopOptions) error

	// additional hooks to satisfy extended dockerAPI for create/start/remove
	createFunc func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *v1.Platform, containerName string) (container.CreateResponse, error)
	startFunc  func(ctx context.Context, containerID string, options container.StartOptions) error
	removeFunc func(ctx context.Context, containerID string, options container.RemoveOptions) error
}

func (f *fakeDockerAPI) ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
	if f.listFunc != nil {
		return f.listFunc(ctx, options)
	}
	return nil, nil
}

func (f *fakeDockerAPI) ContainerInspect(ctx context.Context, id string) (container.InspectResponse, error) {
	if f.inspectFunc != nil {
		return f.inspectFunc(ctx, id)
	}
	return container.InspectResponse{}, nil
}

func (f *fakeDockerAPI) ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error {
	if f.stopFunc != nil {
		return f.stopFunc(ctx, containerID, options)
	}
	return nil
}

func (f *fakeDockerAPI) ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *v1.Platform, containerName string) (container.CreateResponse, error) {
	if f.createFunc != nil {
		return f.createFunc(ctx, config, hostConfig, networkingConfig, platform, containerName)
	}
	return container.CreateResponse{}, nil
}

func (f *fakeDockerAPI) ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error {
	if f.startFunc != nil {
		return f.startFunc(ctx, containerID, options)
	}
	return nil
}

func (f *fakeDockerAPI) ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error {
	if f.removeFunc != nil {
		return f.removeFunc(ctx, containerID, options)
	}
	return nil
}

// fakeImageManager provides a minimal test double for ImageManager
type fakeImageManager struct {
	pulledImages    map[string]struct{}
	availableImages map[string]struct{}
}

func (f *fakeImageManager) BuildImage(_ context.Context, _, image string) error {
	f.makeImagePulled(image)
	return nil
}

func (f *fakeImageManager) ImageExists(_ context.Context, image string) (bool, error) {
	return f.hasImagePulled(image), nil
}

func (f *fakeImageManager) PullImage(_ context.Context, image string) error {
	if f.hasImagePulled(image) {
		return nil
	}
	if !f.hasImageAvailable(image) {
		return fmt.Errorf("failed to pull image %q", image)
	}
	f.makeImagePulled(image)

	return nil
}

func (f *fakeImageManager) hasImageAvailable(image string) bool {
	if f.availableImages == nil {
		return false
	}

	_, available := f.availableImages[image]
	return available
}

func (f *fakeImageManager) hasImagePulled(image string) bool {
	if f.pulledImages == nil {
		return false
	}
	_, exists := f.pulledImages[image]
	return exists
}

func (f *fakeImageManager) makeImagePulled(imageName string) {
	if f.pulledImages == nil {
		f.pulledImages = make(map[string]struct{})
	}

	f.pulledImages[imageName] = struct{}{}
}
