// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package upstreamtoken provides a service for managing upstream IDP token
// lifecycle, including transparent refresh of expired access tokens.
package upstreamtoken

import "context"

// TokenSessionIDClaimKey is the JWT claim key for the token session ID.
// This links JWT access tokens to stored upstream IDP tokens.
// We use "tsid" instead of "sid" to avoid confusion with OIDC session management
// which defines "sid" for different purposes (RFC 7519, OIDC Session Management).
const TokenSessionIDClaimKey = "tsid"

// UpstreamCredential is the opaque result of GetValidTokens.
// The caller only needs the access token to inject into upstream requests.
type UpstreamCredential struct {
	AccessToken string
}

// UpstreamTokenReader retrieves upstream provider access tokens for a session.
// This narrow interface decouples the auth middleware from storage internals.
//
// TODO(auth): Consider enriching the return type from map[string]string to
// map[string]UpstreamCredential to carry per-provider freshness/error metadata.
type UpstreamTokenReader interface {
	// GetAllValidTokens returns access tokens for all upstream providers in a session.
	// Expired tokens are refreshed transparently when possible; if refresh fails,
	// the provider is omitted from the result.
	// Returns an empty map (not error) for unknown sessions.
	GetAllValidTokens(ctx context.Context, sessionID string) (map[string]string, error)
}

// Service owns the upstream token lifecycle: read, refresh, error handling.
type Service interface {
	// GetValidTokens returns a valid upstream credential for a session and provider.
	// It transparently refreshes expired access tokens using the refresh token.
	// The providerName identifies which upstream provider's tokens to retrieve.
	//
	// Returns:
	//   - *UpstreamCredential on success
	//   - ErrSessionNotFound if no upstream tokens exist for the session/provider
	//   - ErrNoRefreshToken if the access token is expired and no refresh token is available
	//   - ErrRefreshFailed if the refresh attempt fails (e.g., revoked refresh token)
	GetValidTokens(ctx context.Context, sessionID, providerName string) (*UpstreamCredential, error)
}
