// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package networking

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

const (
	// ErrPrivateIpAddress is the error returned when the provided URL redirects to a private IP address
	ErrPrivateIpAddress = "the provided URL redirects to a private IP address, which is not allowed"
)

func init() {
	for _, cidr := range []string{
		"127.0.0.0/8",    // IPv4 loopback
		"10.0.0.0/8",     // RFC1918
		"172.16.0.0/12",  // RFC1918
		"192.168.0.0/16", // RFC1918
		"169.254.0.0/16", // RFC3927 link-local
		"::1/128",        // IPv6 loopback
		"fe80::/10",      // IPv6 link-local
		"fc00::/7",       // IPv6 unique local addr
	} {
		_, block, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Errorf("parse error on %q: %w", cidr, err))
		}
		privateIPBlocks = append(privateIPBlocks, block)
	}
}

// IsPrivateIP reports whether ip is a private, loopback, or link-local address.
func IsPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	for _, block := range privateIPBlocks {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

// AddressReferencesPrivateIp returns an error if the address references a private IP address
func AddressReferencesPrivateIp(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	// Check for a private IP address or loopback
	ip := net.ParseIP(host)
	if IsPrivateIP(ip) {
		return errors.New(ErrPrivateIpAddress)
	}

	return nil
}

// ValidateEndpointURL validates that an endpoint URL is secure
func ValidateEndpointURL(endpoint string) error {
	skipValidation := strings.EqualFold(os.Getenv("INSECURE_DISABLE_URL_VALIDATION"), "true")
	return validateEndpointURLWithSkip(endpoint, skipValidation)
}

// ValidateEndpointURLWithInsecure validates that an endpoint URL is secure, allowing HTTP if insecureAllowHTTP is true
// WARNING: This is insecure and should NEVER be used in production
func ValidateEndpointURLWithInsecure(endpoint string, insecureAllowHTTP bool) error {
	skipValidation := strings.EqualFold(os.Getenv("INSECURE_DISABLE_URL_VALIDATION"), "true")
	return validateEndpointURLWithSkip(endpoint, skipValidation || insecureAllowHTTP)
}

// validateEndpointURLWithSkip validates that an endpoint URL is secure, with an option to skip validation
func validateEndpointURLWithSkip(endpoint string, skipValidation bool) error {
	if skipValidation {
		return nil // Skip validation
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	// Ensure HTTPS for security (except localhost for development)
	if u.Scheme != HttpsScheme && !IsLocalhost(u.Host) {
		return fmt.Errorf("endpoint must use HTTPS: %s", endpoint)
	}

	return nil
}

// ValidateHTTPSURL checks that rawURL is a valid URL using the https scheme.
// Unlike ValidateEndpointURL, no localhost exception is made — HTTPS is always
// required (suitable for gateway URLs and other production endpoints).
func ValidateHTTPSURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Host == "" {
		return fmt.Errorf("URL must include a host: %s", rawURL)
	}
	if parsed.Scheme != HttpsScheme {
		return fmt.Errorf("must use HTTPS, got scheme %q", parsed.Scheme)
	}
	return nil
}

// ValidateIssuerURL validates that an OIDC issuer URL is well-formed and uses
// HTTPS. HTTP is permitted only for localhost (development). Per OIDC Core
// Section 3.1.2.1 and RFC 8414 Section 2, the issuer MUST use the "https"
// scheme.
func ValidateIssuerURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid issuer URL %q: %w", rawURL, err)
	}
	if u.Host == "" {
		return fmt.Errorf("issuer URL must include a host: %s", rawURL)
	}
	if u.Scheme != HttpsScheme && !IsLocalhost(u.Host) {
		return fmt.Errorf("issuer URL must use HTTPS (except localhost for development): %s", rawURL)
	}
	return nil
}

// ValidateLoopbackAddress returns an error if addr (a host:port string) does
// not contain a literal loopback IP address. Both IPv4 (127.x.x.x) and IPv6
// (::1) loopback addresses are accepted. Hostnames (including "localhost") are
// not resolved and will be rejected.
func ValidateLoopbackAddress(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen address %q must be a loopback interface (127.x.x.x or ::1)", addr)
	}
	return nil
}

// IsLocalhost checks if a host is localhost (for development)
func IsLocalhost(host string) bool {
	return strings.HasPrefix(host, "localhost:") ||
		strings.HasPrefix(host, "127.0.0.1:") ||
		strings.HasPrefix(host, "[::1]:") ||
		host == "localhost" ||
		host == "127.0.0.1" ||
		host == "[::1]"
}

// IsLoopbackHost reports whether the Host header value refers to a loopback
// address. It is intended for DNS-rebinding guards on loopback-only listeners.
// It accepts the hostname "localhost" (case-insensitive), any 127.x.x.x
// address, and the IPv6 loopback ::1. Both plain-host and host:port forms are
// accepted. Hostnames other than "localhost" are NOT resolved.
func IsLoopbackHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		// No port present — treat the whole value as the host.
		h = host
		// Strip brackets from bare IPv6 literals like "[::1]".
		if len(h) > 2 && h[0] == '[' && h[len(h)-1] == ']' {
			h = h[1 : len(h)-1]
		}
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// IsURL checks if the input is a valid HTTP or HTTPS URL
func IsURL(input string) bool {
	parsedURL, err := url.Parse(input)
	if err != nil {
		return false
	}
	// Must have HTTP or HTTPS scheme and a valid host
	return (parsedURL.Scheme == HttpScheme || parsedURL.Scheme == HttpsScheme) &&
		parsedURL.Host != "" &&
		parsedURL.Host != "//"
}
