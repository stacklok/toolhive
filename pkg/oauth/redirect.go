// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauth

import (
	"context"
	"fmt"
	"net/url"

	"github.com/ory/fosite"
)

// MaxRedirectURILength is the maximum allowed length for a single redirect URI.
// This limit provides DoS protection during URI parsing per RFC 3986 practical constraints.
const MaxRedirectURILength = 2048

// RedirectURIPolicy controls which URI schemes are accepted during redirect URI validation.
type RedirectURIPolicy int

const (
	// RedirectURIPolicyStrict allows only https and http-loopback schemes.
	// This follows RFC 8252 Section 8.4 strict security recommendations and
	// is appropriate for dynamically registered clients where scheme hijacking
	// is a concern.
	RedirectURIPolicyStrict RedirectURIPolicy = iota

	// RedirectURIPolicyAllowPrivateSchemes also allows private-use URI schemes
	// (e.g., cursor://, vscode://) per RFC 8252 Section 7.1.
	// This is appropriate for pre-registered/static clients where the administrator
	// explicitly configures trusted redirect URIs for native applications.
	RedirectURIPolicyAllowPrivateSchemes
)

// ValidateRedirectURI validates a redirect URI per RFC 6749 Section 3.1.2 and RFC 8252.
// The policy parameter controls whether private-use URI schemes are accepted.
//
// Validation rules applied:
//   - URI must not exceed MaxRedirectURILength (DoS protection)
//   - URI must be an absolute URI with a scheme (RFC 6749 Section 3.1.2)
//   - URI must not contain a fragment component (RFC 6749 Section 3.1.2)
//   - Scheme security per policy:
//   - Strict: only https or http-loopback (RFC 8252 Section 8.4)
//   - AllowPrivateSchemes: also allows private-use schemes (RFC 8252 Section 7.1)
func ValidateRedirectURI(uri string, policy RedirectURIPolicy) error {
	if len(uri) > MaxRedirectURILength {
		return fmt.Errorf("redirect_uri too long (maximum %d characters)", MaxRedirectURILength)
	}

	parsed, err := url.Parse(uri)
	if err != nil {
		return fmt.Errorf("invalid redirect_uri format: %w", err)
	}

	// RFC 6749 Section 3.1.2: must be absolute URI without fragment
	if !fosite.IsValidRedirectURI(parsed) {
		return fmt.Errorf("redirect_uri must be an absolute URI without a fragment")
	}

	// Apply scheme security policy
	switch policy {
	case RedirectURIPolicyStrict:
		// RFC 8252 Section 8.4: only https or http for loopback
		if !fosite.IsRedirectURISecureStrict(context.Background(), parsed) {
			return fmt.Errorf("redirect_uri must use http (for loopback) or https scheme")
		}
	case RedirectURIPolicyAllowPrivateSchemes:
		// RFC 8252 Section 7.1: also allow private-use URI schemes
		if !fosite.IsRedirectURISecure(context.Background(), parsed) {
			return fmt.Errorf("redirect_uri must use a secure scheme (https, http for loopback, or a private-use scheme)")
		}
	default:
		return fmt.Errorf("unknown redirect URI policy: %d", policy)
	}

	return nil
}
