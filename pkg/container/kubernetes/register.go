// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"context"

	"github.com/stacklok/toolhive/pkg/container/images"
	"github.com/stacklok/toolhive/pkg/container/runtime"
)

func init() {
	runtime.RegisterRuntime(&runtime.Info{
		Name:     RuntimeName,
		Priority: 200,
		Initializer: func(ctx context.Context) (runtime.Runtime, error) {
			return NewClient(ctx)
		},
		AutoDetector: func() bool {
			return runtime.IsKubernetesRuntime()
		},
	})

	images.RegisterManager(&images.ManagerInfo{
		Name:     RuntimeName,
		Priority: 200,
		Initializer: func(_ context.Context) (images.ImageManager, error) {
			return &images.NoopImageManager{}, nil
		},
		AutoDetector: func() bool {
			return runtime.IsKubernetesRuntime()
		},
	})
}
