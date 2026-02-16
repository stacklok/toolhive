// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"

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
}
