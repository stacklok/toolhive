package vmcp

import "errors"

// Common domain errors used across vmcp subpackages.
// Following DDD principles, domain errors are defined at the package root.
// These errors should be checked using errors.Is().

var (
	// ErrNotFound indicates a requested resource (tool, resource, prompt, workflow) was not found.
	// Wrapping errors should provide specific details about what was not found.
	ErrNotFound = errors.New("not found")

	// ErrInvalidConfig indicates invalid configuration was provided.
	// Wrapping errors should provide specific details about what is invalid.
	ErrInvalidConfig = errors.New("invalid configuration")

	// ErrAuthenticationFailed indicates authentication failure.
	// Wrapping errors should include the underlying authentication error.
	ErrAuthenticationFailed = errors.New("authentication failed")

	// ErrAuthorizationFailed indicates authorization failure.
	// Wrapping errors should include the policy or permission that was denied.
	ErrAuthorizationFailed = errors.New("authorization failed")

	// ErrWorkflowFailed indicates workflow execution failed.
	// Wrapping errors should include the step ID and failure reason.
	ErrWorkflowFailed = errors.New("workflow execution failed")

	// ErrTimeout indicates an operation timed out.
	// Wrapping errors should include the operation type and timeout duration.
	ErrTimeout = errors.New("operation timed out")

	// ErrCancelled indicates an operation was cancelled.
	// Context cancellation should wrap this error with context.Cause().
	ErrCancelled = errors.New("operation cancelled")

	// ErrInvalidInput indicates invalid input parameters.
	// Wrapping errors should specify which parameter is invalid and why.
	ErrInvalidInput = errors.New("invalid input")
)
