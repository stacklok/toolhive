package docker

import "fmt"

// Error types for container operations
var (
	// ErrContainerNotFound is returned when a container is not found
	ErrContainerNotFound = fmt.Errorf("container not found")

	// ErrContainerNotRunning is returned when a container is not running
	ErrContainerNotRunning = fmt.Errorf("container not running")

	// ErrAttachFailed is returned when attaching to a container fails
	ErrAttachFailed = fmt.Errorf("failed to attach to container")

	// ErrContainerExited is returned when a container has exited unexpectedly
	ErrContainerExited = fmt.Errorf("container exited unexpectedly")
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
