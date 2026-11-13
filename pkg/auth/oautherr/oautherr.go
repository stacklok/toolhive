// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package oautherr provides compatibility forwarding for OAuth error classification.
//
// Deprecated: Use github.com/stacklok/toolhive/pkg/oauthproto/oautherr instead.
package oautherr

import (
	"golang.org/x/oauth2"

	protocolerr "github.com/stacklok/toolhive/pkg/oauthproto/oautherr"
)

// IsTransientRetrieveError reports whether a token-endpoint failure is transient.
//
// Deprecated: Use oauthproto/oautherr.IsTransientRetrieveError instead.
func IsTransientRetrieveError(err *oauth2.RetrieveError) bool {
	return protocolerr.IsTransientRetrieveError(err)
}

// IsPermanentCredentialError reports whether a token-endpoint failure is permanent.
//
// Deprecated: Use oauthproto/oautherr.IsPermanentCredentialError instead.
func IsPermanentCredentialError(err error) bool {
	return protocolerr.IsPermanentCredentialError(err)
}

// IsRejectedRefreshGrant reports whether the OAuth server rejected the refresh grant.
//
// Deprecated: Use oauthproto/oautherr.IsRejectedRefreshGrant instead.
func IsRejectedRefreshGrant(err error) bool {
	return protocolerr.IsRejectedRefreshGrant(err)
}

// IsParseError reports whether the OAuth library could not parse the token response.
//
// Deprecated: Use oauthproto/oautherr.IsParseError instead.
func IsParseError(err error) bool {
	return protocolerr.IsParseError(err)
}
