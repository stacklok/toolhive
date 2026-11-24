// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package apple

import (
	"context"

	"github.com/stacklok/toolhive/pkg/container/images"
	"github.com/stacklok/toolhive/pkg/container/runtime"
)

const runtimePriority = 150

func init() {
	runtime.RegisterRuntime(&runtime.Info{
		Name:     RuntimeName,
		Priority: runtimePriority,
		Initializer: func(ctx context.Context) (runtime.Runtime, error) {
			return NewClient(ctx)
		},
		AutoDetector: func() bool {
			return IsAvailable()
		},
	})

	images.RegisterManager(&images.ManagerInfo{
		Name:     RuntimeName,
		Priority: runtimePriority,
		Initializer: func(_ context.Context) (images.ImageManager, error) {
			return NewImageManager(), nil
		},
		AutoDetector: func() bool {
			return IsAvailable()
		},
	})
}
