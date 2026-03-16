// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gomicrovm

import (
	"context"

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

// RuntimeName is the name identifier for the go-microvm microVM runtime.
const RuntimeName = "go-microvm"

func init() {
	runtime.RegisterRuntime(&runtime.Info{
		Name:     RuntimeName,
		Priority: 300, // Never auto-selected over Docker (100) or Kubernetes (200)
		Initializer: func(ctx context.Context) (runtime.Runtime, error) {
			return NewClient(ctx)
		},
		AutoDetector: func() bool {
			return IsAvailable()
		},
	})
}
