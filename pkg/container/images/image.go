// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package images handles container image management operations.
package images

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/stacklok/toolhive/pkg/container/docker/sdk"
	"github.com/stacklok/toolhive/pkg/container/runtime"
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
// for the current environment, aligned with TOOLHIVE_RUNTIME and
// priority-ordered runtime auto-detection.
func NewImageManager(ctx context.Context) ImageManager {
	runtimeName := strings.TrimSpace(os.Getenv("TOOLHIVE_RUNTIME"))
	return NewImageManagerFromRegistry(ctx, DefaultManagerRegistry, runtimeName)
}

// NewImageManagerFromRegistry resolves an ImageManager from the supplied registry.
// When runtimeName is non-empty, only that runtime is considered (preventing
// split-brain between runtimes when multiple are installed). When the registry
// has no entries (for example in isolated tests that do not import pkg/container),
// it falls back to direct Kubernetes / Docker detection.
func NewImageManagerFromRegistry(ctx context.Context, reg *ManagerRegistry, runtimeName string) ImageManager {
	if reg != nil {
		registered := reg.ByPriority()
		if len(registered) > 0 {
			return resolveFromRegistry(ctx, reg, registered, runtimeName)
		}
	}

	// Fallback when no runtime packages have registered (e.g. isolated package tests).
	if runtime.IsKubernetesRuntime() {
		slog.Debug("running in Kubernetes environment, using no-op image manager")
		return &NoopImageManager{}
	}

	dockerClient, _, _, err := sdk.NewDockerClient(ctx)
	if err != nil {
		slog.Debug("no docker runtime found, using no-op image manager")
		return &NoopImageManager{}
	}

	return NewRegistryImageManager(dockerClient)
}

func resolveFromRegistry(
	ctx context.Context,
	reg *ManagerRegistry,
	ordered []*ManagerInfo,
	runtimeName string,
) ImageManager {
	if runtimeName != "" {
		info := reg.Get(runtimeName)
		if info == nil {
			//nolint:gosec // G706: runtime name from TOOLHIVE_RUNTIME env var
			slog.Debug("requested image manager runtime not registered, using no-op image manager", "runtime", runtimeName)
			return &NoopImageManager{}
		}
		if info.AutoDetector != nil && !info.AutoDetector() {
			//nolint:gosec // G706: runtime name from TOOLHIVE_RUNTIME env var
			slog.Debug("requested image manager runtime not available, using no-op image manager", "runtime", runtimeName)
			return &NoopImageManager{}
		}
		mgr, err := info.Initializer(ctx)
		if err != nil {
			//nolint:gosec // G706: runtime name from TOOLHIVE_RUNTIME env var
			slog.Debug("failed to initialize requested image manager, using no-op image manager",
				"runtime", runtimeName, "error", err)
			return &NoopImageManager{}
		}
		return mgr
	}

	if runtime.IsKubernetesRuntime() {
		if info := reg.Get(string(runtime.TypeKubernetes)); info != nil {
			if mgr, err := info.Initializer(ctx); err == nil {
				return mgr
			}
		}
		slog.Debug("running in Kubernetes environment, using no-op image manager")
		return &NoopImageManager{}
	}

	for _, info := range ordered {
		if info.AutoDetector != nil && !info.AutoDetector() {
			continue
		}
		mgr, err := info.Initializer(ctx)
		if err != nil {
			slog.Debug("failed to initialize image manager candidate", "runtime", info.Name, "error", err)
			continue
		}
		return mgr
	}

	slog.Debug("no available image manager found, using no-op image manager")
	return &NoopImageManager{}
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
