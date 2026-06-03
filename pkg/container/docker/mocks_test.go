// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"fmt"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	mobyclient "github.com/moby/moby/client"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// fakeDockerAPI provides a minimal test double for dockerAPI used by Client.
// Centralized here for reuse across tests. The hook signatures mirror the
// redesigned moby/moby client API that Client.api targets.
type fakeDockerAPI struct {
	listFunc    func(ctx context.Context, options mobyclient.ContainerListOptions) ([]container.Summary, error)
	inspectFunc func(ctx context.Context, id string) (container.InspectResponse, error)
	stopFunc    func(ctx context.Context, containerID string, options mobyclient.ContainerStopOptions) error

	// additional hooks to satisfy extended dockerAPI for create/start/remove
	createFunc func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *v1.Platform, containerName string) (container.CreateResponse, error)
	startFunc  func(ctx context.Context, containerID string, options mobyclient.ContainerStartOptions) error
	removeFunc func(ctx context.Context, containerID string, options mobyclient.ContainerRemoveOptions) error
}

func (f *fakeDockerAPI) ContainerList(
	ctx context.Context,
	options mobyclient.ContainerListOptions,
) (mobyclient.ContainerListResult, error) {
	if f.listFunc != nil {
		items, err := f.listFunc(ctx, options)
		return mobyclient.ContainerListResult{Items: items}, err
	}
	return mobyclient.ContainerListResult{}, nil
}

func (f *fakeDockerAPI) ContainerInspect(
	ctx context.Context,
	id string,
	_ mobyclient.ContainerInspectOptions,
) (mobyclient.ContainerInspectResult, error) {
	if f.inspectFunc != nil {
		resp, err := f.inspectFunc(ctx, id)
		return mobyclient.ContainerInspectResult{Container: resp}, err
	}
	return mobyclient.ContainerInspectResult{}, nil
}

func (f *fakeDockerAPI) ContainerStop(
	ctx context.Context,
	containerID string,
	options mobyclient.ContainerStopOptions,
) (mobyclient.ContainerStopResult, error) {
	if f.stopFunc != nil {
		return mobyclient.ContainerStopResult{}, f.stopFunc(ctx, containerID, options)
	}
	return mobyclient.ContainerStopResult{}, nil
}

func (f *fakeDockerAPI) ContainerCreate(
	ctx context.Context,
	options mobyclient.ContainerCreateOptions,
) (mobyclient.ContainerCreateResult, error) {
	if f.createFunc != nil {
		resp, err := f.createFunc(ctx, options.Config, options.HostConfig, options.NetworkingConfig, options.Platform, options.Name)
		return mobyclient.ContainerCreateResult{ID: resp.ID, Warnings: resp.Warnings}, err
	}
	return mobyclient.ContainerCreateResult{}, nil
}

func (f *fakeDockerAPI) ContainerStart(
	ctx context.Context,
	containerID string,
	options mobyclient.ContainerStartOptions,
) (mobyclient.ContainerStartResult, error) {
	if f.startFunc != nil {
		return mobyclient.ContainerStartResult{}, f.startFunc(ctx, containerID, options)
	}
	return mobyclient.ContainerStartResult{}, nil
}

func (f *fakeDockerAPI) ContainerRemove(
	ctx context.Context,
	containerID string,
	options mobyclient.ContainerRemoveOptions,
) (mobyclient.ContainerRemoveResult, error) {
	if f.removeFunc != nil {
		return mobyclient.ContainerRemoveResult{}, f.removeFunc(ctx, containerID, options)
	}
	return mobyclient.ContainerRemoveResult{}, nil
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
