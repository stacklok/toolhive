// Package docker provides Docker-specific implementation of container runtime.
package docker

import "fmt"

// Error types for container operations
var (
	// ErrContainerNotFound is returned when attempting to operate on a container
	// that does not exist in the runtime.
	ErrContainerNotFound = fmt.Errorf("container not found")

	// ErrContainerAlreadyExists is returned when attempting to create a container
	// with a name that is already in use, but with different configuration.
	ErrContainerAlreadyExists = fmt.Errorf("container already exists")

	// ErrContainerNotRunning is returned when attempting to perform an operation
	// that requires a running container (e.g., attaching to stdin/stdout).
	ErrContainerNotRunning = fmt.Errorf("container not running")

	// ErrContainerAlreadyRunning is returned when attempting to start a container
	// that is already in a running state.
	ErrContainerAlreadyRunning = fmt.Errorf("container already running")

	// ErrRuntimeNotFound is returned when the container runtime (Docker/Podman)
	// is not available on the system or cannot be accessed.
	ErrRuntimeNotFound = fmt.Errorf("container runtime not found")

	// ErrInvalidRuntimeType is returned when an unsupported or invalid runtime
	// type is specified in the configuration.
	ErrInvalidRuntimeType = fmt.Errorf("invalid runtime type")

	// ErrAttachFailed is returned when the attempt to attach to a container's
	// stdin/stdout/stderr streams fails.
	ErrAttachFailed = fmt.Errorf("failed to attach to container")

	// ErrContainerExited is returned when a container unexpectedly exits or
	// stops while an operation is being performed.
	ErrContainerExited = fmt.Errorf("container exited unexpectedly")
)

// ContainerError represents a detailed error related to container operations.
// It provides context about the specific container and operation that failed.
type ContainerError struct {
	// Err is the underlying error that occurred
	Err error

	// ContainerID is the ID of the container involved in the error.
	// This may be empty if the error occurred before a container was created.
	ContainerID string

	// Message is a human-readable description of what went wrong
	Message string
}

// Error returns a formatted error message that includes the container ID
// (if available) and additional context about what went wrong.
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

// Unwrap returns the underlying error, allowing ContainerError to work with
// the standard errors.Is and errors.As functions.
func (e *ContainerError) Unwrap() error {
	return e.Err
}

// NewContainerError creates a new ContainerError with the specified details.
// This is a helper function to ensure consistent error creation throughout the package.
//
// Parameters:
//   - err: The underlying error that occurred
//   - containerID: The ID of the container involved (may be empty)
//   - message: A human-readable description of what went wrong
func NewContainerError(err error, containerID, message string) *ContainerError {
	return &ContainerError{
		Err:         err,
		ContainerID: containerID,
		Message:     message,
	}
}
