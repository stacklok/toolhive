// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/container/docker/sdk"
	"github.com/stacklok/toolhive/pkg/container/images"
	"github.com/stacklok/toolhive/pkg/container/runtime"
)

func init() {
	runtime.RegisterRuntime(&runtime.Info{
		Name:     RuntimeName,
		Priority: 100,
		Initializer: func(ctx context.Context) (runtime.Runtime, error) {
			return NewClient(ctx)
		},
		AutoDetector: func() bool {
			return IsAvailable()
		},
	})

	images.RegisterManager(&images.ManagerInfo{
		Name:     RuntimeName,
		Priority: 100,
		Initializer: func(ctx context.Context) (images.ImageManager, error) {
			dockerClient, _, _, err := sdk.NewDockerClient(ctx)
			if err != nil {
				return nil, fmt.Errorf("failed to initialize docker image manager: %w", err)
			}
			return images.NewRegistryImageManager(dockerClient), nil
		},
	})
}
