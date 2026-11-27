// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package strategies

import (
	"context"
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive/pkg/auth"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	healthcontext "github.com/stacklok/toolhive/pkg/vmcp/health/context"
)

// UpstreamInjectStrategy passes an upstream provider's access token through to
// the backend as a Bearer token. Non-delegated routes use Identity.UpstreamTokens.
// Delegated admission uses a UserCredentialReader keyed by platform user ID and
// provider, without falling back to the identity's token map.
//
// UpstreamInject.ProviderName must match an entry in AuthServer.Upstreams.
type UpstreamInjectStrategy struct {
	creds upstreamCredentials
}

// NewUpstreamInjectStrategy creates an UpstreamInjectStrategy. Delegated
// authentication fails closed unless a reader is configured.
func NewUpstreamInjectStrategy(opts ...Option) *UpstreamInjectStrategy {
	s := &UpstreamInjectStrategy{}
	s.creds.apply(opts...)
	return s
}

// Name returns the strategy identifier.
func (*UpstreamInjectStrategy) Name() string {
	return authtypes.StrategyTypeUpstreamInject
}

// Authenticate injects the configured provider's upstream access token into the request header.
//
// This method:
//  1. Skips authentication for health check requests (no user identity to inject)
//  2. Retrieves the authenticated identity from the request context
//  3. Resolves the upstream token for the configured provider name. For a delegated
//     identity (AdmissionRoute delegated) it reads the credential through the configured
//     UserCredentialReader by platform user ID and provider, with no fallback; otherwise
//     it reads Identity.UpstreamTokens
//  4. Sets the Authorization header with the upstream token as a Bearer token
//
// Parameters:
//   - ctx: Request context containing the authenticated identity (or health check marker)
//   - req: The HTTP request to authenticate
//   - strategy: Backend auth strategy containing upstream inject configuration
//
// Returns an error if:
//   - No identity is found in the context
//   - Strategy configuration is nil or missing UpstreamInject
//   - The upstream token cannot be resolved for the configured provider
//   - A delegated read fails: ErrDelegatedCredentialUnavailable, or a
//     DelegatedAuthRequiredError (package authtypes)
func (s *UpstreamInjectStrategy) Authenticate(
	ctx context.Context, req *http.Request, strategy *authtypes.BackendAuthStrategy,
) error {
	// Health checks have no user identity — skip authentication.
	if healthcontext.IsHealthCheck(ctx) {
		return nil
	}

	identity, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return fmt.Errorf("no identity found in context")
	}

	if strategy == nil || strategy.UpstreamInject == nil {
		return fmt.Errorf("upstream_inject configuration required")
	}

	token, err := s.creds.accessToken(ctx, identity, strategy.UpstreamInject.ProviderName)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	return nil
}

// Validate checks if the required strategy configuration fields are present and valid.
//
// This method verifies that:
//   - The UpstreamInject configuration block is present
//   - ProviderName is present and non-empty
//
// This validation is typically called during configuration parsing to fail fast
// if the strategy is misconfigured.
func (*UpstreamInjectStrategy) Validate(strategy *authtypes.BackendAuthStrategy) error {
	if strategy == nil || strategy.UpstreamInject == nil {
		return fmt.Errorf("upstream_inject configuration required")
	}

	if strategy.UpstreamInject.ProviderName == "" {
		return fmt.Errorf("provider_name required in configuration")
	}

	return nil
}
