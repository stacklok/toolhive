// Package container provides utilities for managing containers,
// including creating, starting, stopping, and monitoring containers.
package container

import (
	"fmt"
)

// Error types for container operations
var (
	// ErrContainerNotFound is returned when a container is not found
	ErrContainerNotFound = fmt.Errorf("container not found")

	// ErrContainerAlreadyExists is returned when a container already exists
	ErrContainerAlreadyExists = fmt.Errorf("container already exists")

	// ErrContainerNotRunning is returned when a container is not running
	ErrContainerNotRunning = fmt.Errorf("container not running")

	// ErrContainerAlreadyRunning is returned when a container is already running
	ErrContainerAlreadyRunning = fmt.Errorf("container already running")

	// ErrRuntimeNotFound is returned when a container runtime is not found
	ErrRuntimeNotFound = fmt.Errorf("container runtime not found")

	// ErrInvalidRuntimeType is returned when an invalid runtime type is specified
	ErrInvalidRuntimeType = fmt.Errorf("invalid runtime type")

	// ErrAttachFailed is returned when attaching to a container fails
	ErrAttachFailed = fmt.Errorf("failed to attach to container")

	// ErrContainerExited is returned when a container has exited unexpectedly
	ErrContainerExited = fmt.Errorf("container exited unexpectedly")
)

// ContainerError represents an error related to container operations
//
//nolint:revive // Intentionally named ContainerError despite package name
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
	return err == ErrContainerNotFound || (err != nil && err.Error() == ErrContainerNotFound.Error())
}

// IsContainerAlreadyExists checks if the error is a container already exists error
func IsContainerAlreadyExists(err error) bool {
	return err == ErrContainerAlreadyExists || (err != nil && err.Error() == ErrContainerAlreadyExists.Error())
}

// IsContainerNotRunning checks if the error is a container not running error
func IsContainerNotRunning(err error) bool {
	return err == ErrContainerNotRunning || (err != nil && err.Error() == ErrContainerNotRunning.Error())
}

// IsContainerAlreadyRunning checks if the error is a container already running error
func IsContainerAlreadyRunning(err error) bool {
	return err == ErrContainerAlreadyRunning || (err != nil && err.Error() == ErrContainerAlreadyRunning.Error())
}

// IsRuntimeNotFound checks if the error is a runtime not found error
func IsRuntimeNotFound(err error) bool {
	return err == ErrRuntimeNotFound || (err != nil && err.Error() == ErrRuntimeNotFound.Error())
}

// IsInvalidRuntimeType checks if the error is an invalid runtime type error
func IsInvalidRuntimeType(err error) bool {
	return err == ErrInvalidRuntimeType || (err != nil && err.Error() == ErrInvalidRuntimeType.Error())
}

// IsAttachFailed checks if the error is an attach failed error
func IsAttachFailed(err error) bool {
	return err == ErrAttachFailed || (err != nil && err.Error() == ErrAttachFailed.Error())
}

// IsContainerExited checks if the error is a container exited error
func IsContainerExited(err error) bool {
	return err == ErrContainerExited || (err != nil && err.Error() == ErrContainerExited.Error())
}
