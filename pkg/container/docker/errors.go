// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/container/runtime"
)

// Docker-specific error types
var (
	// ErrMultipleContainersFound is returned when multiple containers are found
	ErrMultipleContainersFound = httperr.WithCode(fmt.Errorf("multiple containers found with same name"), http.StatusBadRequest)

	// ErrAttachFailed is returned when attaching to a container fails
	ErrAttachFailed = httperr.WithCode(fmt.Errorf("failed to attach to container"), http.StatusBadRequest)
)

// Deprecated aliases — kept so that docker/client.go compiles without changes.
// New code should use the runtime package directly.
var (
	// Deprecated: Use runtime.ErrContainerNotFound.
	ErrContainerNotFound = runtime.ErrContainerNotFound

	// Deprecated: Use runtime.ErrContainerNotRunning.
	ErrContainerNotRunning = runtime.ErrContainerNotRunning

	// Deprecated: Use runtime.ErrContainerExited.
	ErrContainerExited = runtime.ErrContainerExited

	// Deprecated: Use runtime.ErrContainerRestarted.
	ErrContainerRestarted = runtime.ErrContainerRestarted

	// Deprecated: Use runtime.ErrContainerRemoved.
	ErrContainerRemoved = runtime.ErrContainerRemoved

	// Deprecated: Use runtime.NewContainerError.
	NewContainerError = runtime.NewContainerError

	// Deprecated: Use runtime.IsContainerNotFound.
	IsContainerNotFound = runtime.IsContainerNotFound
)

// ContainerError is a deprecated alias for runtime.ContainerError.
//
// Deprecated: Use runtime.ContainerError.
type ContainerError = runtime.ContainerError
