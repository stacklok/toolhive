// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"
)

// internalCIDRs are IP ranges that should never appear in RemoteURL fields.
// These cover loopback, link-local (including cloud metadata), RFC 1918
// private ranges, and the unspecified address.
var internalCIDRs = func() []*net.IPNet {
	cidrs := []string{
		"0.0.0.0/8",      // RFC 1122 "this network" (often resolves to localhost)
		"127.0.0.0/8",    // IPv4 loopback
		"169.254.0.0/16", // IPv4 link-local (cloud metadata lives here)
		"10.0.0.0/8",     // RFC 1918 class A
		"172.16.0.0/12",  // RFC 1918 class B
		"192.168.0.0/16", // RFC 1918 class C
		"::/128",         // IPv6 unspecified
		"::1/128",        // IPv6 loopback
		"fe80::/10",      // IPv6 link-local
		"fc00::/7",       // IPv6 unique-local (ULA)
	}

	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Sprintf("bad CIDR in internalCIDRs: %s", cidr))
		}
		nets = append(nets, ipNet)
	}
	return nets
}()

// blockedHostnames are well-known internal hostnames that must be rejected.
// Subdomain matching (via HasSuffix) ensures that e.g. "api.kubernetes.default.svc"
// is also blocked.
var blockedHostnames = []string{
	"localhost",
	"kubernetes.default.svc.cluster.local",
	"kubernetes.default.svc",
	"kubernetes.default",
	"cluster.local",
	"metadata.google.internal",
}

// ValidateRemoteURL validates that rawURL is a well-formed HTTP or HTTPS URL
// with a non-empty host. It also rejects URLs targeting internal/metadata
// endpoints to prevent SSRF. No network calls or DNS resolution is performed.
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

	if err := validateHostNotInternal(u.Hostname()); err != nil {
		return fmt.Errorf("remote URL host is not allowed: %w", err)
	}

	return nil
}

// validateHostNotInternal checks that the host is not a known internal address.
// It rejects literal IPs in private/loopback/link-local ranges and well-known
// internal hostnames. Hostnames that are not on the blocklist are allowed
// because we do not perform DNS resolution.
func validateHostNotInternal(host string) error {
	ip := net.ParseIP(host)
	if ip != nil {
		// Normalize IPv4-mapped IPv6 addresses (e.g. ::ffff:127.0.0.1) to their
		// 4-byte IPv4 form so that IPv4 CIDRs match correctly.
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		for _, cidr := range internalCIDRs {
			if cidr.Contains(ip) {
				return fmt.Errorf("IP address %s falls within blocked range %s", host, cidr)
			}
		}
		return nil
	}

	// Host is a hostname -- check against blocked names.
	lower := strings.ToLower(host)
	for _, blocked := range blockedHostnames {
		if lower == blocked || strings.HasSuffix(lower, "."+blocked) {
			return fmt.Errorf("hostname %q matches blocked internal hostname %q", host, blocked)
		}
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
