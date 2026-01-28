// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"fmt"
)

var (
	// ErrRegistryTimeout is returned when a registry validation times out
	ErrRegistryTimeout = errors.New("registry validation timed out")

	// ErrRegistryUnreachable is returned when a registry cannot be reached
	ErrRegistryUnreachable = errors.New("registry is unreachable")

	// ErrRegistryValidationFailed is returned when registry validation fails
	ErrRegistryValidationFailed = errors.New("registry validation failed")
)

// RegistryError wraps registry-related errors with additional context
type RegistryError struct {
	// Type is the type of registry (url, api, file)
	Type string
	// URL is the registry URL or path
	URL string
	// Err is the underlying error
	Err error
}

func (e *RegistryError) Error() string {
	return fmt.Sprintf("registry error for %s (%s): %v", e.Type, e.URL, e.Err)
}

func (e *RegistryError) Unwrap() error {
	return e.Err
}

// IsTimeout checks if the error is a timeout error
func (e *RegistryError) IsTimeout() bool {
	return errors.Is(e.Err, ErrRegistryTimeout)
}

// IsUnreachable checks if the error is an unreachable error
func (e *RegistryError) IsUnreachable() bool {
	return errors.Is(e.Err, ErrRegistryUnreachable)
}

// IsValidationFailed checks if the error is a validation error
func (e *RegistryError) IsValidationFailed() bool {
	return errors.Is(e.Err, ErrRegistryValidationFailed)
}
