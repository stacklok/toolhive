// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"errors"
	"fmt"
)

// ErrServerNotFound indicates a server was not found in the registry.
var ErrServerNotFound = errors.New("server not found")

// UnavailableError indicates the upstream registry is unreachable
// or returned an unexpected (non-auth) error such as 404, timeout, or
// connection refused. API handlers translate this into HTTP 503.
type UnavailableError struct {
	URL string
	Err error
}

func (e *UnavailableError) Error() string {
	if e.URL != "" {
		return fmt.Sprintf("upstream registry at %s is unavailable: %s", e.URL, e.Err)
	}
	return fmt.Sprintf("upstream registry is unavailable: %s", e.Err)
}

func (e *UnavailableError) Unwrap() error {
	return e.Err
}
