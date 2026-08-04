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
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/ory/fosite"
	"golang.org/x/crypto/bcrypt"
)

// RegisteredLoopbackRedirectURI returns the registered redirect URI of c that
// requestedURI matches -- exactly, or under RFC 8252 Section 7.3 loopback
// dynamic-port rules -- or ("", false) if none matches.
//
// Loopback dynamic-port matching is restricted to public clients: RFC 8252
// loopback redirects are a native-app pattern and a confidential client must
// never get dynamic-port flexibility on its registered redirect_uri. Keeping
// that guard here rather than in a per-client method means no storage backend
// can reconstruct a client that silently skips it.
func RegisteredLoopbackRedirectURI(c fosite.Client, requestedURI string) (string, bool) {
	if !c.IsPublic() {
		return "", false
	}
	return matchLoopbackRedirectURI(c.GetRedirectURIs(), requestedURI)
}

// IsLocalhostHostname reports whether host (as returned by url.Hostname()) is
// the string "localhost", matched case-insensitively -- the same definition
// hostnamesMatch and isLoopbackHostname use. Exported so callers outside this
// package that need to know "is this specifically localhost, not an IP
// loopback literal" (e.g. deciding whether fosite's own IP-literal-only
// loopback matching already handles a redirect_uri, or whether it needs this
// package's help) share this package's one definition instead of
// re-implementing it and risking drift.
func IsLocalhostHostname(host string) bool {
	return strings.EqualFold(host, "localhost")
}

// matchLoopbackRedirectURI returns the registered URI (from registeredURIs)
// that requestedURI matches, either exactly or under RFC 8252 Section 7.3
// loopback dynamic-port rules, or ("", false) if none matches.
//
// Exact matches take precedence over loopback matches: a full pass over
// registeredURIs checks for an exact match first, and only then falls back to
// loopback matching. Without this, a client registered with both
// "http://localhost/callback" and "http://localhost:54321/callback" would
// have a request for the second rewritten to the first, because loopback
// matching against the first entry succeeds before the loop ever reaches the
// second entry's exact match. A client that registered a specific port must
// not have its request silently redirected to a different registered entry.
func matchLoopbackRedirectURI(registeredURIs []string, requestedURI string) (string, bool) {
	if slices.Contains(registeredURIs, requestedURI) {
		return requestedURI, true
	}
	for _, registeredURI := range registeredURIs {
		if matchesAsLoopback(requestedURI, registeredURI) {
			return registeredURI, true
		}
	}
	return "", false
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
	Secret string //nolint:gosec // G117: field legitimately holds sensitive data

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

	// Audience is the list of allowed audience values for this client.
	// Per RFC 8707, the "resource" parameter in token requests is validated
	// against this list. If nil, audience validation will reject all values.
	Audience []string
}

// New creates a fosite.Client from the given configuration.
// Public clients get TokenEndpointAuthMethod "none" via DefaultOpenIDConnectClient;
// RFC 8252 Section 7.3 loopback redirect URI matching for native OAuth clients is
// provided separately by RegisteredLoopbackRedirectURI, not by the client type itself.
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
		Audience:      cfg.Audience,
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

	// Use DefaultOpenIDConnectClient for public clients so TokenEndpointAuthMethod
	// ("none") is set; RegisteredLoopbackRedirectURI provides RFC 8252 §7.3
	// dynamic port matching for these clients' loopback redirect URIs.
	if cfg.Public {
		return &fosite.DefaultOpenIDConnectClient{
			DefaultClient:           defaultClient,
			TokenEndpointAuthMethod: "none",
		}, nil
	}

	return defaultClient, nil
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

	// RFC 6749 Section 3.1.2: the redirection endpoint URI MUST NOT include a
	// fragment component, and must not carry userinfo. Fosite's own
	// IsValidRedirectURI enforces this on whatever redirect_uri it actually
	// validates -- but a loopback match here is what causes the ORIGINAL
	// requested URI (not fosite's validated one) to become the effective
	// redirect target, so the same check must run here too, or an invalid
	// requested URI could reach storage/token-issuance unvalidated.
	if requested.Fragment != "" || requested.User != nil {
		return false
	}

	// RFC 8252 Section 7.3: Loopback redirect URIs use the "http" scheme.
	// Dynamic port matching only applies to http loopback URIs, not https.
	if requested.Scheme != "http" || registered.Scheme != "http" {
		return false
	}

	// Both must be loopback addresses
	if !isLoopbackHostname(requested.Hostname()) || !isLoopbackHostname(registered.Hostname()) {
		return false
	}

	// Hostnames must match (e.g., both 127.0.0.1 or both localhost)
	if !hostnamesMatch(requested.Hostname(), registered.Hostname()) {
		return false
	}

	// Path must match exactly. EscapedPath() (not Path) is compared: Path is
	// percent-decoded, so an encoded separator (e.g. registered
	// "/callback%2Fchild") would otherwise compare equal to a literal,
	// unencoded path ("/callback/child") that was never actually registered.
	if requested.EscapedPath() != registered.EscapedPath() {
		return false
	}

	// Query must match exactly, including whether a bare "?" was present at
	// all (ForceQuery): RawQuery alone can't distinguish "/callback" from
	// "/callback?", since both parse to an empty RawQuery.
	if requested.RawQuery != registered.RawQuery || requested.ForceQuery != registered.ForceQuery {
		return false
	}

	// Port can be any value (this is the key RFC 8252 requirement)
	return true
}

// isLoopbackHostname reports whether host (as returned by url.Hostname(), so
// already stripped of brackets and port) is one of the RFC 8252 §7.3 loopback
// forms: "localhost" (case-insensitive, matching hostnamesMatch below) or an
// IP loopback literal (127.0.0.1, ::1).
//
// This is deliberately self-contained rather than delegating to
// networking.IsLocalhost: that helper (via oauthproto.IsLoopbackHost) is a
// case-SENSITIVE prefix check requiring the bracketed "[::1]" form, which
// url.Hostname() never produces -- using it here would silently make
// "LOCALHOST" and "::1" both unmatchable despite hostnamesMatch's own
// case-insensitive "localhost" contract.
func isLoopbackHostname(host string) bool {
	if IsLocalhostHostname(host) {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
