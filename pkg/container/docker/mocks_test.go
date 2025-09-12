package docker

import (
	"context"

	"github.com/docker/docker/api/types/container"
)

// fakeDockerAPI provides a minimal test double for dockerAPI used by Client.
// Centralized here for reuse across tests.
type fakeDockerAPI struct {
	listFunc    func(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	inspectFunc func(ctx context.Context, id string) (container.InspectResponse, error)
	stopFunc    func(ctx context.Context, containerID string, options container.StopOptions) error
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
