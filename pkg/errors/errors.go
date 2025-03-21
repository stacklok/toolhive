package errors

import (
	"fmt"
)

// Error types
const (
	// ErrInvalidArgument is returned when an invalid argument is provided
	ErrInvalidArgument = "invalid_argument"
	
	// ErrContainerRuntime is returned when there is an error with the container runtime
	ErrContainerRuntime = "container_runtime"
	
	// ErrContainerNotFound is returned when a container is not found
	ErrContainerNotFound = "container_not_found"
	
	// ErrContainerAlreadyExists is returned when a container already exists
	ErrContainerAlreadyExists = "container_already_exists"
	
	// ErrContainerNotRunning is returned when a container is not running
	ErrContainerNotRunning = "container_not_running"
	
	// ErrContainerAlreadyRunning is returned when a container is already running
	ErrContainerAlreadyRunning = "container_already_running"
	
	// ErrTransport is returned when there is an error with the transport
	ErrTransport = "transport"
	
	// ErrPermissions is returned when there is an error with permissions
	ErrPermissions = "permissions"
	
	// ErrInternal is returned when there is an internal error
	ErrInternal = "internal"
)

// Error represents an error in the application
type Error struct {
	// Type is the error type
	Type string
	
	// Message is the error message
	Message string
	
	// Cause is the underlying error
	Cause error
}

// Error returns the error message
func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %s", e.Type, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

// Unwrap returns the underlying error
func (e *Error) Unwrap() error {
	return e.Cause
}

// NewError creates a new error
func NewError(errorType, message string, cause error) *Error {
	return &Error{
		Type:    errorType,
		Message: message,
		Cause:   cause,
	}
}

// NewInvalidArgumentError creates a new invalid argument error
func NewInvalidArgumentError(message string, cause error) *Error {
	return NewError(ErrInvalidArgument, message, cause)
}

// NewContainerRuntimeError creates a new container runtime error
func NewContainerRuntimeError(message string, cause error) *Error {
	return NewError(ErrContainerRuntime, message, cause)
}

// NewContainerNotFoundError creates a new container not found error
func NewContainerNotFoundError(message string, cause error) *Error {
	return NewError(ErrContainerNotFound, message, cause)
}

// NewContainerAlreadyExistsError creates a new container already exists error
func NewContainerAlreadyExistsError(message string, cause error) *Error {
	return NewError(ErrContainerAlreadyExists, message, cause)
}

// NewContainerNotRunningError creates a new container not running error
func NewContainerNotRunningError(message string, cause error) *Error {
	return NewError(ErrContainerNotRunning, message, cause)
}

// NewContainerAlreadyRunningError creates a new container already running error
func NewContainerAlreadyRunningError(message string, cause error) *Error {
	return NewError(ErrContainerAlreadyRunning, message, cause)
}

// NewTransportError creates a new transport error
func NewTransportError(message string, cause error) *Error {
	return NewError(ErrTransport, message, cause)
}

// NewPermissionsError creates a new permissions error
func NewPermissionsError(message string, cause error) *Error {
	return NewError(ErrPermissions, message, cause)
}

// NewInternalError creates a new internal error
func NewInternalError(message string, cause error) *Error {
	return NewError(ErrInternal, message, cause)
}

// IsInvalidArgument checks if the error is an invalid argument error
func IsInvalidArgument(err error) bool {
	e, ok := err.(*Error)
	return ok && e.Type == ErrInvalidArgument
}

// IsContainerRuntime checks if the error is a container runtime error
func IsContainerRuntime(err error) bool {
	e, ok := err.(*Error)
	return ok && e.Type == ErrContainerRuntime
}

// IsContainerNotFound checks if the error is a container not found error
func IsContainerNotFound(err error) bool {
	e, ok := err.(*Error)
	return ok && e.Type == ErrContainerNotFound
}

// IsContainerAlreadyExists checks if the error is a container already exists error
func IsContainerAlreadyExists(err error) bool {
	e, ok := err.(*Error)
	return ok && e.Type == ErrContainerAlreadyExists
}

// IsContainerNotRunning checks if the error is a container not running error
func IsContainerNotRunning(err error) bool {
	e, ok := err.(*Error)
	return ok && e.Type == ErrContainerNotRunning
}

// IsContainerAlreadyRunning checks if the error is a container already running error
func IsContainerAlreadyRunning(err error) bool {
	e, ok := err.(*Error)
	return ok && e.Type == ErrContainerAlreadyRunning
}

// IsTransport checks if the error is a transport error
func IsTransport(err error) bool {
	e, ok := err.(*Error)
	return ok && e.Type == ErrTransport
}

// IsPermissions checks if the error is a permissions error
func IsPermissions(err error) bool {
	e, ok := err.(*Error)
	return ok && e.Type == ErrPermissions
}

// IsInternal checks if the error is an internal error
func IsInternal(err error) bool {
	e, ok := err.(*Error)
	return ok && e.Type == ErrInternal
}