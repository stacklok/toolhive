// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import "fmt"

// RegistryUnavailableError indicates the upstream registry is unreachable
// or returned an unexpected (non-auth) error such as 404, timeout, or
// connection refused. API handlers translate this into HTTP 503.
type RegistryUnavailableError struct {
	URL string
	Err error
}

func (e *RegistryUnavailableError) Error() string {
	if e.URL != "" {
		return fmt.Sprintf("upstream registry at %s is unavailable: %s", e.URL, e.Err)
	}
	return fmt.Sprintf("upstream registry is unavailable: %s", e.Err)
}

func (e *RegistryUnavailableError) Unwrap() error {
	return e.Err
}
