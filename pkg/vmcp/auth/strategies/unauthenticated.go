// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package strategies

import (
	"context"
	"net/http"

	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// UnauthenticatedStrategy is a no-op authentication strategy that performs no authentication.
// This strategy is used when a backend MCP server requires no authentication.
//
// Unlike passing a nil authenticator (which is now an error), this strategy makes
// the intent explicit: "this backend intentionally has no authentication".
//
// The strategy performs no modifications to requests and validates all metadata.
//
// This is appropriate when:
//   - The backend MCP server is on a trusted network (e.g., localhost)
//   - The backend has no authentication requirements
//   - Authentication is handled by network-level security (e.g., VPC, firewall)
//
// Security Warning: Only use this strategy when you are certain the backend
// requires no authentication. For production deployments, prefer explicit
// authentication strategies (header_injection, token_exchange).
//
// Configuration: No metadata required, but any metadata is accepted and ignored.
//
// Example configuration:
//
//	backends:
//	  local-backend:
//	    strategy: "unauthenticated"
type UnauthenticatedStrategy struct{}

// NewUnauthenticatedStrategy creates a new UnauthenticatedStrategy instance.
func NewUnauthenticatedStrategy() *UnauthenticatedStrategy {
	return &UnauthenticatedStrategy{}
}

// Name returns the strategy identifier.
func (*UnauthenticatedStrategy) Name() string {
	return authtypes.StrategyTypeUnauthenticated
}

// Authenticate performs no authentication and returns immediately.
//
// This method:
//  1. Does not modify the request in any way
//  2. Always returns nil (success)
//
// Parameters:
//   - ctx: Request context (unused)
//   - req: The HTTP request (not modified)
//   - config: Strategy configuration (ignored)
//
// Returns nil (always succeeds).
func (*UnauthenticatedStrategy) Authenticate(_ context.Context, _ *http.Request, _ *authtypes.BackendAuthStrategy) error {
	// No-op: intentionally does nothing
	return nil
}

// Validate checks if the strategy configuration is valid.
//
// UnauthenticatedStrategy accepts any configuration (including nil or empty),
// so this always returns nil.
//
// This permissive validation allows the strategy to be used without
// configuration or with arbitrary configuration that may be present
// for documentation purposes.
func (*UnauthenticatedStrategy) Validate(_ *authtypes.BackendAuthStrategy) error {
	// No-op: accepts any configuration
	return nil
}
