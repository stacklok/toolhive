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

// alwaysBlockedCIDRs are IP ranges that must never appear in RemoteURL fields,
// regardless of ValidateRemoteURLOptions. These cover loopback, link-local
// (including cloud metadata), and the unspecified address.
var alwaysBlockedCIDRs = mustParseCIDRs([]string{
	"0.0.0.0/8",      // RFC 1122 "this network" (often resolves to localhost)
	"127.0.0.0/8",    // IPv4 loopback
	"169.254.0.0/16", // IPv4 link-local (cloud metadata lives here)
	"::/128",         // IPv6 unspecified
	"::1/128",        // IPv6 loopback
	"fe80::/10",      // IPv6 link-local
})

// privateCIDRs are private-network ranges that are rejected by default but
// allowed when ValidateRemoteURLOptions.AllowPrivateEndpoint is true.
var privateCIDRs = mustParseCIDRs([]string{
	"10.0.0.0/8",     // RFC 1918 class A
	"172.16.0.0/12",  // RFC 1918 class B
	"192.168.0.0/16", // RFC 1918 class C
	"fc00::/7",       // IPv6 unique-local (ULA)
})

// alwaysBlockedHostnames are well-known internal hostnames that must be
// rejected regardless of ValidateRemoteURLOptions. Subdomain matching (via
// HasSuffix) ensures that e.g. "api.kubernetes.default.svc" is also blocked.
// These are checked before privateHostnames so that
// "kubernetes.default.svc.cluster.local" is rejected before the "cluster.local"
// relaxation can apply.
var alwaysBlockedHostnames = []string{
	"localhost",
	"kubernetes.default.svc.cluster.local",
	"kubernetes.default.svc",
	"kubernetes.default",
	"metadata.google.internal",
}

// privateHostnames are cluster-internal hostname suffixes that are rejected by
// default but allowed when ValidateRemoteURLOptions.AllowPrivateEndpoint is
// true. Note that ".svc"-suffixed hostnames without the cluster domain are not
// on any blocklist (no DNS resolution is performed), so they are allowed
// regardless of the option.
var privateHostnames = []string{
	"cluster.local",
}

// ValidateRemoteURLOptions controls optional relaxations applied by
// ValidateRemoteURL.
type ValidateRemoteURLOptions struct {
	// AllowPrivateEndpoint permits URLs pointing at private or in-cluster
	// endpoints: RFC 1918 and IPv6 unique-local (ULA) addresses, and hostnames
	// ending in "cluster.local". This supports reaching a co-located in-cluster
	// backend in-mesh so the backend's workload-identity authorization policy
	// still applies. Loopback, link-local, cloud-metadata, and
	// kubernetes.default endpoints remain blocked regardless.
	AllowPrivateEndpoint bool
}

// ValidateRemoteURL validates that rawURL is a well-formed HTTP or HTTPS URL
// with a non-empty host. It also rejects URLs targeting internal/metadata
// endpoints to prevent SSRF; opts.AllowPrivateEndpoint relaxes the checks for
// private-network and cluster-internal endpoints only. No network calls or DNS
// resolution is performed.
func ValidateRemoteURL(rawURL string, opts ValidateRemoteURLOptions) error {
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

	if err := validateHostNotInternal(u.Hostname(), opts); err != nil {
		return fmt.Errorf("remote URL host is not allowed: %w", err)
	}

	return nil
}

// validateHostNotInternal checks that the host is not a known internal address.
// It rejects literal IPs in private/loopback/link-local ranges and well-known
// internal hostnames; opts.AllowPrivateEndpoint skips the private-network and
// cluster-internal checks only. Hostnames that are not on the blocklist are
// allowed because we do not perform DNS resolution.
func validateHostNotInternal(host string, opts ValidateRemoteURLOptions) error {
	ip := net.ParseIP(host)
	if ip != nil {
		// Normalize IPv4-mapped IPv6 addresses (e.g. ::ffff:127.0.0.1) to their
		// 4-byte IPv4 form so that IPv4 CIDRs match correctly.
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		if err := checkIPNotBlocked(host, ip, alwaysBlockedCIDRs); err != nil {
			return err
		}
		if !opts.AllowPrivateEndpoint {
			return checkIPNotBlocked(host, ip, privateCIDRs)
		}
		return nil
	}

	// Host is a hostname -- check against blocked names. Always-blocked names
	// go first so that e.g. "kubernetes.default.svc.cluster.local" is rejected
	// before the "cluster.local" relaxation can apply.
	if err := checkHostnameNotBlocked(host, alwaysBlockedHostnames); err != nil {
		return err
	}
	if !opts.AllowPrivateEndpoint {
		return checkHostnameNotBlocked(host, privateHostnames)
	}

	return nil
}

// checkIPNotBlocked returns an error if ip falls within any of the given CIDRs.
func checkIPNotBlocked(host string, ip net.IP, cidrs []*net.IPNet) error {
	for _, cidr := range cidrs {
		if cidr.Contains(ip) {
			return fmt.Errorf("IP address %s falls within blocked range %s", host, cidr)
		}
	}
	return nil
}

// checkHostnameNotBlocked returns an error if host matches any of the given
// blocked names, either exactly or as a subdomain.
func checkHostnameNotBlocked(host string, blockedNames []string) error {
	lower := strings.ToLower(host)
	for _, blocked := range blockedNames {
		if lower == blocked || strings.HasSuffix(lower, "."+blocked) {
			return fmt.Errorf("hostname %q matches blocked internal hostname %q", host, blocked)
		}
	}
	return nil
}

// mustParseCIDRs parses the given CIDR strings, panicking on any malformed
// entry. It is intended for package-level blocklist initialization only.
func mustParseCIDRs(cidrs []string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Sprintf("bad CIDR in blocklist: %s", cidr))
		}
		nets = append(nets, ipNet)
	}
	return nets
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
