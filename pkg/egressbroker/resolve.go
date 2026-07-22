// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
)

// ErrDNSResolution marks DNS lookup failures during dial-allowlist resolution.
// Callers (the operator reconciler) treat it as transient — retry with
// backoff — unlike policy-shape errors, which are terminal spec violations.
var ErrDNSResolution = errors.New("egressbroker: DNS resolution failed")

// ResolveDialAllowlist resolves the policy's allowed hosts to the concrete
// IP/CIDR set used for both the operator-rendered NetworkPolicy ipBlocks and
// the sidecar's D7 per-dial allowlist. allowedHosts are hostnames by CRD
// grammar, so every entry goes through lookupHost (DNS at operator reconcile
// time; v1: best-effort defense-in-depth — the load-bearing control is D5 +
// D7 in the sidecar).
//
// A lookup error wraps ErrDNSResolution (transient: the caller retries with
// backoff). A host that resolves to nothing (or only to non-public rebinding
// suspects) is a plain error — the policy destinations are wrong, so the
// caller surfaces it as a terminal spec violation rather than rendering an
// empty-allowlist sidecar that silently refuses all egress.
//
// The result is sorted: DNS answer order is unstable, and the allowlist is
// rendered into NetworkPolicy ipBlocks and the policy ConfigMap — an
// order-only difference would churn both every reconcile.
func ResolveDialAllowlist(policy *EgressPolicy, lookupHost func(string) ([]net.IP, error)) ([]string, error) {
	if policy == nil {
		return nil, fmt.Errorf("egressbroker: policy must not be nil")
	}
	if lookupHost == nil {
		return nil, fmt.Errorf("egressbroker: DNS lookup must not be nil")
	}
	seen := map[string]struct{}{}
	var out []string
	for _, host := range policy.HostAllowlist() {
		// Wildcard patterns cannot resolve; strip the marker and resolve the
		// base domain (best-effort — CDN fronted APIs may move; D7 in the
		// sidecar re-validates at dial time).
		name := strings.TrimPrefix(host, "*.")
		ips, err := lookupHost(name)
		if err != nil {
			return nil, fmt.Errorf("%w: policy host %q: %w", ErrDNSResolution, name, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("egressbroker: policy host %q resolved to no addresses", name)
		}
		for _, ip := range ips {
			addr, ok := netip.AddrFromSlice(ip)
			if !ok {
				continue
			}
			addr = addr.Unmap()
			if isNonPublic(addr) {
				continue
			}
			prefix := netip.PrefixFrom(addr, addr.BitLen()).String()
			if _, dup := seen[prefix]; !dup {
				seen[prefix] = struct{}{}
				out = append(out, prefix)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("egressbroker: policy resolved to an empty dial allowlist")
	}
	slices.Sort(out)
	return out, nil
}

// nonPublicPrefixes are never allowlisted from DNS answers (OWASP SSRF set:
// loopback, RFC 1918, link-local, CGNAT, IPv6 ULA/link-local). A name that
// resolves into these is a rebinding suspect and must not widen the dial
// allowlist.
var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func isNonPublic(addr netip.Addr) bool {
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
