// Package errors provides error types and constants for the transport package.
package errors

import (
	"errors"
)

// Common transport errors
var (
	ErrUnsupportedTransport = errors.New("unsupported transport type")
	ErrContainerNameNotSet  = errors.New("container name not set")
)
