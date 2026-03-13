// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstreamtoken

import "errors"

// Sentinel errors returned by Service.GetValidTokens.
var (
	// ErrSessionNotFound indicates no upstream tokens exist for the session.
	ErrSessionNotFound = errors.New("upstream tokens not found for session")

	// ErrNoRefreshToken indicates the access token is expired but no refresh
	// token is available to perform a refresh.
	ErrNoRefreshToken = errors.New("no refresh token available")

	// ErrRefreshFailed indicates a refresh failure (e.g., the
	// refresh token was revoked by the upstream IDP).
	ErrRefreshFailed = errors.New("upstream token refresh failed")

	// ErrInvalidBinding indicates token binding validation failed (e.g.,
	// subject or client ID mismatch between the stored token and the session).
	ErrInvalidBinding = errors.New("upstream token binding validation failed")
)
