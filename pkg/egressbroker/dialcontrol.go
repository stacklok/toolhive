// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"syscall"
)

// IPAllowlist is the D7 resolved-IP guard: the set of destination IPs (as
// prefixes) the broker may dial. It mirrors the WithDialControl pattern from
// pkg/vmcp/client/client.go — validation happens after DNS resolution and
// before the TCP handshake, so a hostname that passes the name-based D5
// allowlist but resolves to a rebinding-suspect IP is still refused.
//
// The allowlist is the union of the operator-rendered destination CIDRs for
// the policy's allowed hosts (same data the NetworkPolicy ipBlocks carry).
// Matching is exact: every IP outside the prefixes — loopback included — is
// refused; the broker dials upstream APIs and Redis, never pod-local services.
type IPAllowlist struct {
	prefixes []netip.Prefix
}

// ParseIPAllowlist compiles CIDR/IPv4/IPv6 strings into an ipAllowlist.
// Entries without a '/' are treated as single-host prefixes. Fails loudly on
// any malformed entry — a broker with a corrupt dial allowlist must not
// start.
func ParseIPAllowlist(entries []string) (*IPAllowlist, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("egressbroker: dial IP allowlist must not be empty")
	}
	prefixes := make([]netip.Prefix, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return nil, fmt.Errorf("egressbroker: dial IP allowlist contains an empty entry")
		}
		if !strings.Contains(entry, "/") {
			addr, err := netip.ParseAddr(entry)
			if err != nil {
				return nil, fmt.Errorf("egressbroker: invalid IP %q in dial allowlist: %w", entry, err)
			}
			prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
			continue
		}
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			return nil, fmt.Errorf("egressbroker: invalid CIDR %q in dial allowlist: %w", entry, err)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return &IPAllowlist{prefixes: prefixes}, nil
}

// Allows reports whether addr (a host:port or bare IP dial target) resolves
// inside the allowlist. Fails closed: unparsable targets, and IPs outside
// every prefix, are refused.
func (a *IPAllowlist) Allows(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	for _, prefix := range a.prefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// DialControl returns a net.Dialer.Control hook enforcing the allowlist on
// every TCP dial (D7): the resolved peer IP must be in the allowlist or the
// dial is refused. The signature matches net.Dialer.Control exactly.
func (a *IPAllowlist) DialControl(_, address string, _ syscall.RawConn) error {
	if !a.Allows(address) {
		return fmt.Errorf("egressbroker: refused dial to %s: resolved IP outside destination allowlist (D7)", address)
	}
	return nil
}

// DialContext returns a DialContext function enforcing the allowlist on every
// dial (D7), for components that dial by hostname rather than raw IP (the
// go-redis client takes DialContext, not net.Dialer.Control).
func (a *IPAllowlist) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if !a.Allows(address) {
		return nil, fmt.Errorf("egressbroker: refused dial to %s: resolved IP outside destination allowlist (D7)", address)
	}
	d := &net.Dialer{Control: a.DialControl}
	return d.DialContext(ctx, network, address)
}
