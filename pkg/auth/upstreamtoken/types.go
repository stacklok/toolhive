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

// Service owns the upstream token lifecycle: read, refresh, error handling.
type Service interface {
	// GetValidTokens returns a valid upstream credential for a session.
	// It transparently refreshes expired access tokens using the refresh token.
	//
	// Returns:
	//   - *UpstreamCredential on success
	//   - ErrSessionNotFound if no upstream tokens exist for the session
	//   - ErrNoRefreshToken if the access token is expired and no refresh token is available
	//   - ErrRefreshFailed if the refresh attempt fails (e.g., revoked refresh token)
	GetValidTokens(ctx context.Context, sessionID string) (*UpstreamCredential, error)
}
