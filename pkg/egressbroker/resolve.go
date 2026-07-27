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

	"github.com/stacklok/toolhive/pkg/networking"
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
			if networking.IsPrivateIP(addr.AsSlice()) {
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

// The non-public address classifier is networking.IsPrivateIP (single shared
// implementation — do not reintroduce a local table): it rejects the OWASP
// SSRF set (loopback, RFC 1918, link-local, CGNAT, IPv6 ULA/link-local,
// multicast/reserved, documentation ranges, 192.0.0.0/24, 198.18.0.0/15) AND
// decodes NAT64 64:ff9b::/96 + 64:ff9b:1::/48 embeddings, so a rebinding
// suspect hidden behind a NAT64 address never widens the dial allowlist.
