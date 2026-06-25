// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package strategies

import (
	"context"
	"net/http"
	"slices"

	"github.com/stacklok/toolhive/pkg/auth"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	healthcontext "github.com/stacklok/toolhive/pkg/vmcp/health/context"
)

// ClaimInjectionStrategy injects authenticated user identity claims as HTTP headers
// into outgoing backend requests.
//
// This enables backend MCP servers to identify the caller (e.g. read X-User-Sub)
// without needing their own OAuth token validation or /introspect calls. The gateway
// is the sole trust boundary; backends trust the headers because they are unreachable
// by anything other than the gateway (enforced via Cloud Run IAM or equivalent).
//
// Which claims to forward is opt-in via ClaimInjectionConfig.Claims to minimise PII
// exposure. The default (empty Claims list) injects only X-User-Sub.
type ClaimInjectionStrategy struct{}

// NewClaimInjectionStrategy creates a new ClaimInjectionStrategy instance.
func NewClaimInjectionStrategy() *ClaimInjectionStrategy {
	return &ClaimInjectionStrategy{}
}

// Name returns the strategy identifier.
func (*ClaimInjectionStrategy) Name() string {
	return authtypes.StrategyTypeClaimInjection
}

// Authenticate reads the per-request identity from the context and injects the
// configured claims as X-User-* headers on the outgoing request.
//
// The method is a no-op (returns nil without modifying the request) when:
//   - the request is a health check (no real user identity available)
//   - no identity is present in the request context
//   - the identity subject is empty or "anonymous" (unauthenticated mode)
func (*ClaimInjectionStrategy) Authenticate(
	ctx context.Context, req *http.Request, strategy *authtypes.BackendAuthStrategy,
) error {
	// Health-check probes carry no user identity; skip silently.
	if healthcontext.IsHealthCheck(ctx) {
		return nil
	}

	identity, ok := auth.IdentityFromContext(ctx)
	if !ok || identity == nil {
		return nil
	}

	// Skip anonymous sessions (vmcp unauthenticated mode).
	// In anonymous mode the subject is "anonymous" and email is "anonymous@localhost".
	if identity.Subject == "" || identity.Subject == "anonymous" {
		return nil
	}

	// Determine which claims to inject.  Default to ["sub"] when not configured.
	claims := []string{"sub"}
	if strategy != nil && strategy.ClaimInjection != nil && len(strategy.ClaimInjection.Claims) > 0 {
		claims = strategy.ClaimInjection.Claims
	}

	if slices.Contains(claims, "sub") && identity.Subject != "" {
		req.Header.Set("X-User-Sub", identity.Subject)
	}
	if slices.Contains(claims, "email") && identity.Email != "" {
		req.Header.Set("X-User-Email", identity.Email)
	}
	if slices.Contains(claims, "name") && identity.Name != "" {
		req.Header.Set("X-User-Name", identity.Name)
	}

	return nil
}

// Validate checks strategy configuration. ClaimInjectionConfig is optional
// (defaults to sub-only injection), so an absent config is valid.
func (*ClaimInjectionStrategy) Validate(strategy *authtypes.BackendAuthStrategy) error {
	return nil
}
