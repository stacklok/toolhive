// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package errors provides error types with HTTP status codes for API error handling.
package errors

import (
	"errors"
	"net/http"
)

// CodedError wraps an error with an HTTP status code.
// This allows errors to carry their intended HTTP response code through the call stack,
// enabling centralized error handling in API handlers.
type CodedError struct {
	err  error
	code int
}

// Error implements the error interface.
func (e *CodedError) Error() string {
	return e.err.Error()
}

// Unwrap returns the underlying error for errors.Is() and errors.As() compatibility.
func (e *CodedError) Unwrap() error {
	return e.err
}

// HTTPCode returns the HTTP status code associated with this error.
func (e *CodedError) HTTPCode() int {
	return e.code
}

// WithCode wraps an error with an HTTP status code.
// The returned error implements Unwrap() for use with errors.Is() and errors.As().
// If err is nil, WithCode returns nil.
func WithCode(err error, code int) error {
	if err == nil {
		return nil
	}
	return &CodedError{err: err, code: code}
}

// Code extracts the HTTP status code from an error.
// It unwraps the error chain looking for a CodedError.
// If no CodedError is found, it returns http.StatusInternalServerError (500).
func Code(err error) int {
	if err == nil {
		return http.StatusOK
	}

	var coded *CodedError
	if errors.As(err, &coded) {
		return coded.code
	}

	return http.StatusInternalServerError
}

// New creates a new error with the given message and HTTP status code.
// This is a convenience function equivalent to WithCode(errors.New(message), code).
func New(message string, code int) error {
	return &CodedError{err: errors.New(message), code: code}
}
