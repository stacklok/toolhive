// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package apple

import (
	"bytes"
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/container/images"
)

// ImageManager implements images.ImageManager using Apple's `container` CLI.
type ImageManager struct {
	runner CommandRunner
}

// Compile-time assertion that ImageManager implements images.ImageManager.
var _ images.ImageManager = (*ImageManager)(nil)

// NewImageManager creates a new Apple container ImageManager.
func NewImageManager() *ImageManager {
	return &ImageManager{runner: execCommandRunner{}}
}

func newImageManagerWithRunner(runner CommandRunner) *ImageManager {
	return &ImageManager{runner: runner}
}

// ImageExists checks whether imageName is present in Apple container's local image store.
func (mgr *ImageManager) ImageExists(ctx context.Context, imageName string) (bool, error) {
	out, err := mgr.runner.Run(ctx, containerBinary, "image", "inspect", "--", imageName)
	if err == nil {
		trimmed := bytes.TrimSpace(out)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[]")) {
			return false, nil
		}
		return true, nil
	}
	if isNotFoundError(err) {
		return false, nil
	}
	return false, fmt.Errorf("failed to inspect image %q: %w", imageName, err)
}

// PullImage pulls imageName into Apple container's local image store.
func (mgr *ImageManager) PullImage(ctx context.Context, imageName string) error {
	if _, err := mgr.runner.Run(ctx, containerBinary, "image", "pull", "--", imageName); err != nil {
		return fmt.Errorf("failed to pull image %q: %w", imageName, err)
	}
	return nil
}

// BuildImage builds a container image from contextDir and tags it as imageName.
func (mgr *ImageManager) BuildImage(ctx context.Context, contextDir, imageName string) error {
	if _, err := mgr.runner.Run(ctx, containerBinary, "build", "-t", imageName, "--", contextDir); err != nil {
		return fmt.Errorf("failed to build image %q from %q: %w", imageName, contextDir, err)
	}
	return nil
}
