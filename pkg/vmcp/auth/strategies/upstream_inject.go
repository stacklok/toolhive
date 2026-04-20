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

// UpstreamInjectStrategy injects an upstream IDP token into backend request headers.
// The token is obtained by the embedded authorization server during the OAuth flow
// and stored in identity.UpstreamTokens, keyed by provider name.
//
// This strategy looks up the provider-specific token from the authenticated identity
// and sets it as a Bearer token in the Authorization header of the backend request.
//
// Required configuration fields (in BackendAuthStrategy.UpstreamInject):
//   - ProviderName: The upstream provider name matching an entry in AuthServer.Upstreams.
//     The token for this provider must be present in the identity's UpstreamTokens map.
//
// This strategy is appropriate when:
//   - The backend requires a user-specific upstream IDP token for authentication
//   - The embedded authorization server has been configured to obtain tokens from
//     the upstream provider during the OAuth flow
//   - The upstream token should be passed through to the backend as-is (no exchange)
type UpstreamInjectStrategy struct{}

// NewUpstreamInjectStrategy creates a new UpstreamInjectStrategy instance.
func NewUpstreamInjectStrategy() *UpstreamInjectStrategy {
	return &UpstreamInjectStrategy{}
}

// Name returns the strategy identifier.
func (*UpstreamInjectStrategy) Name() string {
	return authtypes.StrategyTypeUpstreamInject
}

// Authenticate injects the upstream IDP token from the identity into the request header.
//
// This method:
//  1. Skips authentication for health check requests (no user identity to inject)
//  2. Retrieves the authenticated identity from the request context
//  3. Looks up the upstream token for the configured provider name
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
//   - The upstream token for the configured provider is not found in the identity
func (*UpstreamInjectStrategy) Authenticate(
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

	providerName := strategy.UpstreamInject.ProviderName

	token := identity.UpstreamTokens[providerName]
	if token == "" {
		return fmt.Errorf("provider %q: %w", providerName, authtypes.ErrUpstreamTokenNotFound)
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
