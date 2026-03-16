// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"fmt"
	"net/url"
)

const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"
)

// ValidateRemoteURL validates that rawURL is a well-formed HTTP or HTTPS URL
// with a non-empty host. No network calls are made.
func ValidateRemoteURL(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("remote URL must not be empty")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("remote URL is invalid: %w", err)
	}

	if u.Scheme != schemeHTTP && u.Scheme != schemeHTTPS {
		return fmt.Errorf("remote URL must use http or https scheme, got %q", u.Scheme)
	}

	if u.Host == "" {
		return fmt.Errorf("remote URL must have a valid host")
	}

	return nil
}

// ValidateJWKSURL validates that rawURL, if non-empty, is a well-formed HTTPS
// URL with a non-empty host. JWKS endpoints serve key material and must use
// HTTPS. An empty rawURL is allowed because JWKS discovery can determine the
// endpoint automatically.
func ValidateJWKSURL(rawURL string) error {
	if rawURL == "" {
		return nil
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("JWKS URL is invalid: %w", err)
	}

	if u.Scheme != schemeHTTPS {
		return fmt.Errorf("JWKS URL must use HTTPS scheme, got %q", u.Scheme)
	}

	if u.Host == "" {
		return fmt.Errorf("JWKS URL must have a valid host")
	}

	return nil
}
