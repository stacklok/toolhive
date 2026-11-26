// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstreamtoken

import (
	"errors"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// Sentinel errors returned by Service.GetValidTokens and
// UserCredentialReader.GetValidTokensForUser.
var (
	// ErrSessionNotFound indicates no upstream tokens exist for the session.
	ErrSessionNotFound = errors.New("upstream tokens not found for session")

	// ErrNoRefreshToken indicates the access token is expired but no refresh
	// token is available to perform a refresh.
	ErrNoRefreshToken = errors.New("no refresh token available")

	// ErrRefreshFailed indicates a refresh failure (e.g., the
	// refresh token was revoked by the upstream IDP). It wraps the underlying
	// cause, so a definitive upstream rejection is also reachable with
	// errors.Is(err, upstream.ErrGrantRejected), and a token endpoint answer
	// keeps its *oauth2.RetrieveError reachable for the oautherr predicates.
	ErrRefreshFailed = errors.New("upstream token refresh failed")

	// ErrInvalidBinding indicates token binding validation failed (e.g.,
	// subject or client ID mismatch between the stored token and the session).
	ErrInvalidBinding = errors.New("upstream token binding validation failed")

	// ErrNeedsAuthorization indicates the user has no usable connection to the
	// provider and must authorize again: there is no row, the row holds no
	// tokens, the access token is expired and cannot be refreshed, or the
	// provider rejected the refresh with invalid_grant. It is returned together
	// with its cause, which stays reachable with errors.Is.
	//
	// When only the ID token needed refreshing and the access token is still
	// live, a failed refresh, including invalid_grant, does not produce it: the
	// stored credential is returned and the failure is logged at WARN. It
	// follows when the access token expires.
	ErrNeedsAuthorization = errors.New("upstream authorization required")

	// ErrNoCanonicalUser is storage.ErrNoCanonicalUser, re-exported so callers
	// outside the authorization server can match it without importing storage.
	ErrNoCanonicalUser = storage.ErrNoCanonicalUser
)
