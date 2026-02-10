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

// Package registration provides OAuth client types and utilities, including
// RFC 8252 compliant loopback redirect URI support for native OAuth clients.
package registration

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/ory/fosite"
	"golang.org/x/crypto/bcrypt"

	"github.com/stacklok/toolhive/pkg/networking"
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
// as a loopback address. Fosite's isMatchingAsLoopback uses isLoopbackAddress()
// which only supports IP addresses (net.ParseIP().IsLoopback()), not the "localhost"
// hostname. This is needed for DCR with clients like VS Code, Claude Code, and other
// native apps that register redirect URIs like "http://localhost/callback" and then
// request authorization with dynamic ports like "http://localhost:57403/callback".
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

// DefaultScopes are the default OAuth 2.0 scopes for registered clients.
// Includes offline_access to enable refresh token issuance.
var DefaultScopes = []string{"openid", "profile", "email", "offline_access"}

// Config holds configuration for creating a new OAuth client.
type Config struct {
	// ID is the unique client identifier.
	ID string

	// Secret is the client secret for confidential clients.
	// Empty for public clients.
	Secret string

	// RedirectURIs is the list of allowed redirect URIs.
	RedirectURIs []string

	// Public indicates whether this is a public client (no secret).
	Public bool

	// GrantTypes overrides the default grant types.
	// If nil or empty, defaultGrantTypes is used.
	GrantTypes []string

	// ResponseTypes overrides the default response types.
	// If nil or empty, defaultResponseTypes is used.
	ResponseTypes []string

	// Scopes overrides the default scopes.
	// If nil or empty, DefaultScopes is used.
	Scopes []string
}

// New creates a fosite.Client from the given configuration.
// Public clients are wrapped in LoopbackClient to support RFC 8252 Section 7.3
// compliant loopback redirect URI matching for native OAuth clients.
// Confidential clients with secrets have their Secret field bcrypt-hashed
// as required by fosite for credential validation.
func New(cfg Config) (fosite.Client, error) {
	// Apply defaults for empty slices
	grantTypes := cfg.GrantTypes
	if len(grantTypes) == 0 {
		grantTypes = defaultGrantTypes
	}

	responseTypes := cfg.ResponseTypes
	if len(responseTypes) == 0 {
		responseTypes = defaultResponseTypes
	}

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = DefaultScopes
	}

	// Create the DefaultClient
	defaultClient := &fosite.DefaultClient{
		ID:            cfg.ID,
		RedirectURIs:  cfg.RedirectURIs,
		ResponseTypes: responseTypes,
		GrantTypes:    grantTypes,
		Scopes:        scopes,
		Public:        cfg.Public,
	}

	// Set bcrypt-hashed secret for confidential clients.
	// Fosite expects the Secret field to contain a bcrypt hash
	// for proper credential validation.
	if !cfg.Public {
		if cfg.Secret == "" {
			return nil, fmt.Errorf("confidential client requires a secret")
		}
		hashedSecret, err := bcrypt.GenerateFromPassword([]byte(cfg.Secret), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("failed to hash client secret: %w", err)
		}
		defaultClient.Secret = hashedSecret
	}

	// Wrap public clients in LoopbackClient for RFC 8252 Section 7.3
	// dynamic port matching for native app loopback redirect URIs.
	if cfg.Public {
		return NewLoopbackClient(defaultClient), nil
	}

	return defaultClient, nil
}

// Compile-time interface compliance check
var _ fosite.Client = (*LoopbackClient)(nil)

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

	// RFC 8252 Section 7.3: Loopback redirect URIs use the "http" scheme.
	// Dynamic port matching only applies to http loopback URIs, not https.
	if requested.Scheme != "http" || registered.Scheme != "http" {
		return false
	}

	// Both must be loopback addresses
	if !networking.IsLocalhost(requested.Hostname()) || !networking.IsLocalhost(registered.Hostname()) {
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
	return networking.IsLocalhost(parsed.Hostname())
}

// hostnamesMatch checks if two hostnames (as returned by url.Hostname()) should
// be considered equivalent for loopback matching purposes.
//
// The parameters are expected to be pre-parsed hostname strings from url.Hostname(),
// not raw URIs. This function is called from matchesAsLoopback which handles URL parsing.
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
