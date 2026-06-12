// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package composer provides composite tool workflow execution for Virtual MCP Server.
package composer

import (
	"errors"
	"fmt"
)

// Common workflow execution errors.
var (
	// ErrWorkflowNotFound indicates the workflow doesn't exist.
	ErrWorkflowNotFound = errors.New("workflow not found")

	// ErrWorkflowTimeout indicates the workflow exceeded its timeout.
	ErrWorkflowTimeout = errors.New("workflow timed out")

	// ErrWorkflowCancelled indicates the workflow was cancelled.
	ErrWorkflowCancelled = errors.New("workflow cancelled")

	// ErrInvalidWorkflowDefinition indicates the workflow definition is invalid.
	ErrInvalidWorkflowDefinition = errors.New("invalid workflow definition")

	// ErrStepFailed indicates a workflow step failed.
	ErrStepFailed = errors.New("step failed")

	// ErrTemplateExpansion indicates template expansion failed.
	ErrTemplateExpansion = errors.New("template expansion failed")

	// ErrCircularDependency indicates a circular dependency in step dependencies.
	ErrCircularDependency = errors.New("circular dependency detected")

	// ErrDependencyNotMet indicates a step dependency hasn't completed.
	ErrDependencyNotMet = errors.New("dependency not met")

	// ErrToolCallFailed indicates a tool call failed.
	ErrToolCallFailed = errors.New("tool call failed")
)

// ValidationError wraps workflow validation errors.
type ValidationError struct {
	// Field is the field that failed validation.
	Field string

	// Message is the error message.
	Message string

	// Cause is the underlying error.
	Cause error
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("validation error for %s: %s: %v", e.Field, e.Message, e.Cause)
	}
	return fmt.Sprintf("validation error for %s: %s", e.Field, e.Message)
}

// Unwrap returns the underlying error.
func (e *ValidationError) Unwrap() error {
	return e.Cause
}

// NewValidationError creates a new validation error.
func NewValidationError(field, message string, cause error) *ValidationError {
	return &ValidationError{
		Field:   field,
		Message: message,
		Cause:   cause,
	}
}
