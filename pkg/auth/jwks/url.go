// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package jwks

import (
	"errors"
	"fmt"
	"net"
	"net/url"

	"github.com/stacklok/toolhive/pkg/networking"
)

// ValidateJWKSURL checks that jwksURL parses, has a host, uses HTTPS unless
// insecureAllowHTTP permits plain HTTP — and only exactly the "http" scheme,
// not any other non-https scheme such as "file" or "ftp" — and, when the
// host is an IP literal, is not a private or loopback address unless
// allowPrivateIPs permits that. Both flags come from the specific Fetcher
// (or issuer) being fetched, never from a validator-wide or self-issuer
// setting. This prevents SSRF attacks where a compromised discovery document —
// or a hand-configured jwks_url — points to internal services.
//
// This is the single implementation shared by the runtime choke point in
// Fetcher.registerOrRefresh (on every fetch) and pkg/authserver/config.go's
// config-time check (validateJWKSEndpointURL): the two must not drift out of
// sync, or a laxer runtime check would silently defeat the config-time guard.
//
// Deliberately env-immune: neither flag is widened by
// INSECURE_DISABLE_URL_VALIDATION or any other environment variable, so an
// unrelated env var can never silently disable this SSRF-relevant check.
func ValidateJWKSURL(jwksURL string, insecureAllowHTTP, allowPrivateIPs bool) error {
	u, err := url.Parse(jwksURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if u.Host == "" {
		return errors.New("host is required")
	}

	// Unlike issuer_url, a jwks_url carrying userinfo would actually work —
	// net/http turns it into a Basic auth header on every JWKS fetch — which
	// is precisely why it is rejected rather than tolerated: it would put a
	// live credential in the RunConfig, in this function's error strings, and
	// in any log that quotes the URL. A JWKS endpoint is public by
	// definition (it serves verification keys), so there is no legitimate
	// reason to authenticate to one.
	if u.User != nil {
		return errors.New("must not contain userinfo (credentials in the URL)")
	}

	if u.Scheme != "https" && (u.Scheme != "http" || !insecureAllowHTTP) {
		return fmt.Errorf("must use HTTPS, got %q", u.Scheme)
	}

	host := u.Hostname()
	ip := net.ParseIP(host)
	if ip != nil && !allowPrivateIPs && networking.IsPrivateIP(ip) {
		return errors.New("must not point to a private or loopback address")
	}

	return nil
}
