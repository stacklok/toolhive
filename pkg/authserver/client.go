// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package authserver

import (
	"net"
	"net/url"
	"strings"

	"github.com/ory/fosite"
)

// LoopbackClient is a fosite.Client implementation that supports RFC 8252 Section 7.3
// compliant loopback redirect URI matching for native OAuth clients.
//
// RFC 8252 Section 7.3 specifies that:
//   - Loopback redirect URIs use "http" (not "https")
//   - The host must be "127.0.0.1", "[::1]", or "localhost"
//   - The authorization server MUST allow any port
//   - The path and query components must match exactly
//
// This client extends fosite's built-in loopback support to also handle "localhost"
// as a loopback address, which fosite's default implementation does not support
// for dynamic port matching.
type LoopbackClient struct {
	*fosite.DefaultClient
}

// NewLoopbackClient creates a new LoopbackClient wrapping the provided DefaultClient.
func NewLoopbackClient(client *fosite.DefaultClient) *LoopbackClient {
	return &LoopbackClient{DefaultClient: client}
}

// MatchRedirectURI checks if the given redirect URI matches one of the client's
// registered redirect URIs, with RFC 8252 Section 7.3 loopback support.
//
// For loopback URIs (127.0.0.1, [::1], or localhost), the port is allowed to
// vary while the scheme, host, path, and query must match exactly.
func (c *LoopbackClient) MatchRedirectURI(requestedURI string) bool {
	for _, registeredURI := range c.GetRedirectURIs() {
		if matchesRedirectURI(requestedURI, registeredURI) {
			return true
		}
	}
	return false
}

// GetMatchingRedirectURI returns the matching redirect URI if found, or an empty string.
// For loopback URIs, returns the requested URI (with its port) if it matches a registered
// loopback pattern.
func (c *LoopbackClient) GetMatchingRedirectURI(requestedURI string) string {
	for _, registeredURI := range c.GetRedirectURIs() {
		if matchesRedirectURI(requestedURI, registeredURI) {
			// For loopback matches, return the requested URI to preserve the dynamic port
			if isLoopbackURI(requestedURI) {
				return requestedURI
			}
			return registeredURI
		}
	}
	return ""
}

// matchesRedirectURI checks if a requested URI matches a registered URI.
// Implements RFC 8252 Section 7.3 loopback matching.
func matchesRedirectURI(requestedURI, registeredURI string) bool {
	// Exact match always works
	if requestedURI == registeredURI {
		return true
	}

	// Try loopback matching
	return matchesAsLoopback(requestedURI, registeredURI)
}

// matchesAsLoopback checks if the requested URI matches the registered URI
// using RFC 8252 Section 7.3 loopback rules.
//
// Per RFC 8252 Section 7.3:
//   - Loopback redirect URIs use the "http" scheme
//   - The host must be 127.0.0.1, [::1], or localhost
//   - The authorization server MUST allow any port
//   - The path and query components must match exactly
func matchesAsLoopback(requestedURI, registeredURI string) bool {
	requested, err := url.Parse(requestedURI)
	if err != nil {
		return false
	}

	registered, err := url.Parse(registeredURI)
	if err != nil {
		return false
	}

	// Must use http scheme (not https) for loopback
	if requested.Scheme != schemeHTTP || registered.Scheme != schemeHTTP {
		return false
	}

	// Both must be loopback addresses
	if !IsLoopbackHost(requested.Hostname()) || !IsLoopbackHost(registered.Hostname()) {
		return false
	}

	// Hostnames must match (e.g., both 127.0.0.1 or both localhost)
	if !hostnamesMatch(requested.Hostname(), registered.Hostname()) {
		return false
	}

	// Path must match exactly
	if requested.Path != registered.Path {
		return false
	}

	// Query must match exactly
	if requested.RawQuery != registered.RawQuery {
		return false
	}

	// Port can be any value (this is the key RFC 8252 requirement)
	return true
}

// isLoopbackURI checks if the URI uses a loopback address.
func isLoopbackURI(uri string) bool {
	parsed, err := url.Parse(uri)
	if err != nil {
		return false
	}
	return IsLoopbackHost(parsed.Hostname())
}

// IsLoopbackHost checks if the hostname is a loopback address per RFC 8252 Section 7.3.
// Valid loopback hosts are:
//   - "127.0.0.1" (IPv4 loopback)
//   - "::1" (IPv6 loopback, typically written as "[::1]" in URLs)
//   - "localhost"
//
// This function is exported for reuse by Dynamic Client Registration (DCR) validation.
func IsLoopbackHost(hostname string) bool {
	// Check for localhost (case-insensitive per RFC)
	if strings.EqualFold(hostname, "localhost") {
		return true
	}

	// Check for IP loopback addresses (127.0.0.1 or ::1)
	ip := net.ParseIP(hostname)
	if ip != nil && ip.IsLoopback() {
		return true
	}

	return false
}

// hostnamesMatch checks if two hostnames should be considered equivalent for
// loopback matching purposes.
//
// Per RFC 8252, the hostname must match exactly. We normalize localhost to
// be case-insensitive, but 127.0.0.1 and localhost are treated as different
// hostnames (a client registered with 127.0.0.1 will not match localhost requests).
func hostnamesMatch(requested, registered string) bool {
	// Case-insensitive comparison for localhost
	if strings.EqualFold(requested, "localhost") && strings.EqualFold(registered, "localhost") {
		return true
	}

	// Exact match for IP addresses
	return requested == registered
}

// Compile-time interface compliance check
var _ fosite.Client = (*LoopbackClient)(nil)
