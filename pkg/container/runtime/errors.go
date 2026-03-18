// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive-core/httperr"
)

// Error types for container operations
var (
	// ErrContainerNotFound is returned when a container is not found
	ErrContainerNotFound = httperr.WithCode(fmt.Errorf("container not found"), http.StatusNotFound)

	// ErrContainerNotRunning is returned when a container is not running
	ErrContainerNotRunning = httperr.WithCode(fmt.Errorf("container not running"), http.StatusBadRequest)

	// ErrContainerExited is returned when a container has exited unexpectedly
	ErrContainerExited = httperr.WithCode(fmt.Errorf("container exited unexpectedly"), http.StatusBadRequest)

	// ErrContainerRestarted is returned when a container has been restarted
	// (e.g., by Docker restart policy). The container is still running.
	// This is distinct from ErrContainerExited.
	ErrContainerRestarted = httperr.WithCode(fmt.Errorf("container restarted"), http.StatusBadRequest)

	// ErrContainerRemoved is returned when a container has been removed
	ErrContainerRemoved = httperr.WithCode(fmt.Errorf("container removed"), http.StatusBadRequest)
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

// IsContainerNotFound checks if the error is a container not found error
func IsContainerNotFound(err error) bool {
	return errors.Is(err, ErrContainerNotFound)
}
