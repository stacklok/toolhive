// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package upstreamtoken provides a service for managing upstream IDP token
// lifecycle, including transparent refresh of expired access tokens.
package upstreamtoken

//go:generate go run go.uber.org/mock/mockgen -destination=mocks/mock_token_reader.go -package=mocks github.com/stacklok/toolhive/pkg/auth/upstreamtoken TokenReader
//go:generate go run go.uber.org/mock/mockgen -destination=mocks/mock_user_token_reader.go -package=mocks github.com/stacklok/toolhive/pkg/auth/upstreamtoken UserTokenReader

import "context"

// TokenSessionIDClaimKey is the JWT claim key for the token session ID.
// This links JWT access tokens to stored upstream IDP tokens.
// We use "tsid" instead of "sid" to avoid confusion with OIDC session management
// which defines "sid" for different purposes (RFC 7519, OIDC Session Management).
const TokenSessionIDClaimKey = "tsid"

// UpstreamCredential bundles the access token and the ID token for a single
// upstream provider. Access tokens are refreshed when expired.
//
// The IDToken is the rotated ID token when a refresh produced one (OIDC Core
// 1.0 §12.2 permits but does not require a new id_token on refresh), otherwise
// the original JWT captured at the initial OIDC login (OIDC Core 1.0 §3.1.3.7).
// It is not independently validated for freshness.
//
// Callers that PRESENT it as a credential — e.g. as the subject_token of an
// RFC 8693 token exchange — are subject to expiry and must either check its `exp`
// claim first or handle the resulting rejection. Today's such consumer
// (pkg/vmcp/auth/strategies) deliberately takes the second option: it lets the IdP
// reject the exchange and surfaces the `invalid_grant`. Note also that the ID
// token's `aud` is this auth server's client registration with the issuing
// upstream, not the token-exchange endpoint — whether a target authorization
// server accepts it as a subject token is governed by that server's policy and
// is typically limited to the same issuer/audience.
//
// Callers that only READ ITS CLAIMS are a different case and deliberately do not
// check `exp`: the token is then evidence of what the upstream asserted at login,
// and expiry says nothing about whether that assertion happened. See
// Identity.UpstreamIDTokens for the two consumers and why they differ.
//
// IDToken may be empty when the upstream login did not return an id_token
// (e.g. the provider was not asked for the openid scope). An empty IDToken
// is a legitimate state, not an error. Note that Identity.UpstreamTokens and
// Identity.UpstreamIDTokens are projected independently and are NOT guaranteed
// to share the same key set: a provider with an access token but no ID token
// appears only in UpstreamTokens.
type UpstreamCredential struct {
	AccessToken string
	IDToken     string
}

// TokenReader retrieves upstream provider credentials for a session.
// This narrow interface decouples the auth middleware from storage internals.
type TokenReader interface {
	// GetAllUpstreamCredentials returns the access tokens (refreshing them if
	// expired) and ID tokens for every upstream provider associated with the
	// given session ID. Returns an empty map if the session has no stored
	// upstream tokens.
	//
	// The second return value contains the names of providers whose access tokens
	// were expired and could not be refreshed (e.g. the refresh token is missing
	// or the upstream IDP rejected the refresh). Those providers are omitted from
	// the creds map. Callers should treat a non-empty failed list as a signal to
	// return HTTP 401 + WWW-Authenticate so the client can re-authenticate.
	//
	// Each returned IDToken is the rotated ID token when a refresh produced
	// one (OIDC Core 1.0 §12.2), otherwise the original JWT captured at OIDC
	// login (OIDC Core 1.0 §3.1.3.7), and it may be expired. Callers that PRESENT
	// it as a credential (e.g. RFC 8693 subject-token exchange) must check `exp`
	// first or handle the resulting rejection; callers that only READ ITS CLAIMS
	// are unaffected by expiry. See UpstreamCredential for both consumers.
	//
	// Returns an empty map and nil failed slice (not error) for unknown sessions.
	GetAllUpstreamCredentials(ctx context.Context, sessionID string) (
		creds map[string]UpstreamCredential, failed []string, err error)
}

// UserTokenReader retrieves upstream provider credentials for a durable
// platform-user identity. It is a preparatory seam for the platform-user
// credential model: it is deliberately separate from TokenReader, which
// resolves credentials per token session (tsid). A real implementation
// arrives with platform-user storage; nothing in this repository may wrap
// or reuse the session TokenReader as a UserTokenReader.
type UserTokenReader interface {
	// GetUserUpstreamCredential returns the stored upstream credential for
	// the given durable platform-user identity and upstream provider name.
	// Returns an error if the user has no stored credential for the provider.
	GetUserUpstreamCredential(ctx context.Context, userID, providerName string) (*UpstreamCredential, error)
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
	//
	// The returned UpstreamCredential.IDToken may be empty or expired. Callers that
	// PRESENT it as a credential (e.g. as the subject_token of an RFC 8693 token
	// exchange) must check its `exp` claim first or handle the resulting rejection;
	// callers that only READ ITS CLAIMS are unaffected by expiry. See
	// UpstreamCredential for details.
	GetValidTokens(ctx context.Context, sessionID, providerName string) (*UpstreamCredential, error)
}
