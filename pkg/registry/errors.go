// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"errors"
	"fmt"

	"github.com/stacklok/toolhive/pkg/registry/legacyhint"
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

// LegacyFormatError indicates the registry source contains data in the legacy
// ToolHive registry format instead of the upstream MCP registry format.
// API handlers translate this into a structured HTTP 503 with a
// "registry_legacy_format" code so desktop clients can surface a targeted
// recovery flow (instead of a generic error screen).
//
// URL is optional and identifies the offending source (remote URL or local
// file path) when known. The Error() message embeds legacyhint.MigrationMessage
// so CLI consumers continue to see the same actionable hint.
type LegacyFormatError struct {
	URL string
}

func (e *LegacyFormatError) Error() string {
	if e.URL != "" {
		return fmt.Sprintf("registry at %s: %s", e.URL, legacyhint.MigrationMessage)
	}
	return legacyhint.MigrationMessage
}

// Is enables errors.Is matching against any *LegacyFormatError sentinel,
// regardless of the URL field. Existing callers using a zero-value sentinel
// (e.g. errLegacyFormat) keep matching when the returned error carries a URL.
func (*LegacyFormatError) Is(target error) bool {
	_, ok := target.(*LegacyFormatError)
	return ok
}
