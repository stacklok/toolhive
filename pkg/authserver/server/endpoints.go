// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"net"
	"net/url"
	"strings"
)

// TokenEndpointPath is the path of the token endpoint, relative to the issuer.
// Discovery advertises the issuer joined with it, the router serves it, and
// client assertions may name that URL as their audience.
const TokenEndpointPath = "/oauth/token" //nolint:gosec // G101: URL path, not a credential

// NormalizeIssuer returns the form of an issuer URL used to compare issuers:
// lower-case scheme and host, no default port for the scheme (443 for https,
// 80 for http), and one trailing slash dropped. It is the key of
// tokenexchange.ConfiguredClientPolicy.IDJAGTargets. The configured spelling,
// not this form, is what an ID-JAG carries as aud. A value that does not
// parse is returned unchanged.
func NormalizeIssuer(issuer string) string {
	parsed, err := url.Parse(issuer)
	if err != nil {
		return issuer
	}
	normalized := *parsed
	normalized.Scheme = strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (normalized.Scheme == "https" && port == "443") || (normalized.Scheme == "http" && port == "80") {
		port = ""
	}
	switch {
	case port != "":
		normalized.Host = net.JoinHostPort(host, port)
	case strings.Contains(host, ":"):
		normalized.Host = "[" + host + "]"
	default:
		normalized.Host = host
	}
	normalized.Path = strings.TrimSuffix(parsed.Path, "/")
	normalized.RawPath = strings.TrimSuffix(parsed.RawPath, "/")
	return normalized.String()
}
