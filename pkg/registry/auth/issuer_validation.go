// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"fmt"
	"net/url"

	"github.com/stacklok/toolhive/pkg/networking"
)

// validateIssuerURL validates that the issuer is a well-formed URL using HTTPS.
// HTTP is permitted only for localhost (development). Per OIDC Core Section 3.1.2.1
// and RFC 8414 Section 2, the issuer MUST use the "https" scheme.
func validateIssuerURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid issuer URL %q: %w", rawURL, err)
	}
	if u.Host == "" {
		return fmt.Errorf("issuer URL must include a host: %s", rawURL)
	}
	if u.Scheme != "https" && !networking.IsLocalhost(u.Host) {
		return fmt.Errorf("issuer URL must use HTTPS (except localhost for development): %s", rawURL)
	}
	return nil
}
