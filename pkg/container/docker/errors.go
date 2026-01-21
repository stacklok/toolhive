// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive/pkg/errors"
)

// Error types for container operations
var (
	// ErrContainerNotFound is returned when a container is not found
	ErrContainerNotFound = errors.WithCode(fmt.Errorf("container not found"), http.StatusNotFound)

	// ErrMultipleContainersFound is returned when multiple containers are found
	ErrMultipleContainersFound = errors.WithCode(fmt.Errorf("multiple containers found with same name"), http.StatusBadRequest)

	// ErrContainerNotRunning is returned when a container is not running
	ErrContainerNotRunning = errors.WithCode(fmt.Errorf("container not running"), http.StatusBadRequest)

	// ErrAttachFailed is returned when attaching to a container fails
	ErrAttachFailed = errors.WithCode(fmt.Errorf("failed to attach to container"), http.StatusBadRequest)

	// ErrContainerExited is returned when a container has exited unexpectedly
	ErrContainerExited = errors.WithCode(fmt.Errorf("container exited unexpectedly"), http.StatusBadRequest)

	// ErrContainerRemoved is returned when a container has been removed
	ErrContainerRemoved = errors.WithCode(fmt.Errorf("container removed"), http.StatusBadRequest)
)

// ContainerError represents an error related to container operations
type ContainerError struct {
	// Err is the underlying error
	Err error
	// ContainerID is the ID of the container
	ContainerID string
	// Message is an optional error message
	Message string
}

// Error returns the error message
func (e *ContainerError) Error() string {
	if e.Message != "" {
		if e.ContainerID != "" {
			return fmt.Sprintf("%s: %s (container: %s)", e.Err, e.Message, e.ContainerID)
		}
		return fmt.Sprintf("%s: %s", e.Err, e.Message)
	}

	if e.ContainerID != "" {
		return fmt.Sprintf("%s (container: %s)", e.Err, e.ContainerID)
	}

	return e.Err.Error()
}

// Unwrap returns the underlying error
func (e *ContainerError) Unwrap() error {
	return e.Err
}

// NewContainerError creates a new container error
func NewContainerError(err error, containerID, message string) *ContainerError {
	return &ContainerError{
		Err:         err,
		ContainerID: containerID,
		Message:     message,
	}
}
