// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthproto

import (
	"net"
	"strings"
)

// IsLoopbackHost reports whether host is a loopback hostname or IP address.
// pkg/networking wraps this function in its own IsLocalhost to avoid a
// reverse import dependency from this leaf package into networking.
//
// Recognised forms: "localhost" (case-insensitive) and any loopback IP
// (127.0.0.0/8, ::1), each with or without a port, and IPv6 literals with or
// without brackets. url.Hostname() strips brackets, so callers passing a
// parsed hostname hand us the bare "::1" form.
func IsLoopbackHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		// No port (or a bare IPv6 literal, which SplitHostPort rejects for
		// missing brackets) — treat the whole value as the host.
		h = host
	}
	h = strings.TrimPrefix(strings.TrimSuffix(h, "]"), "[")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
