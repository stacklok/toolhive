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
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// LoopbackClient wraps a fosite.DefaultOpenIDConnectClient with RFC 8252
// Section 7.3 loopback redirect URI matching helpers (MatchRedirectURI,
// GetMatchingRedirectURI, defined below).
//
// RFC 8252 Section 7.3 specifies that:
//   - Loopback redirect URIs use "http" (not "https")
//   - The host must be "127.0.0.1", "[::1]", or "localhost"
//   - The authorization server MUST allow any port
//   - The path and query components must match exactly
//
// What this type does NOT do: fosite's own authorize-path redirect matching
// (MatchRedirectURIWithClientRedirectURIs → isMatchingAsLoopback) reads only
// GetRedirectURIs() and never calls this type's methods, so they take no
// effect on that path. Fosite's own loopback matching (isLoopbackAddress,
// net.ParseIP().IsLoopback()) covers IP literals (127.0.0.1, [::1]) but not
// the "localhost" hostname — net.ParseIP("localhost") returns nil — so a
// client registered with "http://localhost/callback" gets exact-match only
// against fosite's matcher; a dynamic-port authorize request like
// "http://localhost:57403/callback" (the pattern VS Code, Claude Code, and
// other native apps use) fails today. MatchRedirectURI/GetMatchingRedirectURI
// exist for callers that do their own matching outside fosite's authorize
// path; they are not a fosite hook. This type's live value in the codebase is
// carrying the OIDC client shape (so GetTokenEndpointAuthMethod survives)
// through storage's DCR round-trip.
type LoopbackClient struct {
	*fosite.DefaultOpenIDConnectClient
}

// NewLoopbackClient creates a new LoopbackClient wrapping the provided client.
// The wrapper preserves all OIDC fields (including TokenEndpointAuthMethod).
//
// Note: fosite's redirect-matching path does not call MatchRedirectURI —
// MatchRedirectURIWithClientRedirectURIs reads only GetRedirectURIs() and
// applies fosite's own loopback handling (isMatchingAsLoopback), which covers
// loopback IP literals but not the "localhost" hostname. This wrapper's value
// is carrying the OIDC client shape (so GetTokenEndpointAuthMethod survives)
// for callers that do their own matching via MatchRedirectURI/
// GetMatchingRedirectURI; it is not a fosite hook.
func NewLoopbackClient(client *fosite.DefaultOpenIDConnectClient) *LoopbackClient {
	return &LoopbackClient{DefaultOpenIDConnectClient: client}
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
	// Required for client_secret_basic / client_secret_post; ignored for "none".
	Secret string //nolint:gosec // G117: field legitimately holds sensitive data

	// RedirectURIs is the list of allowed redirect URIs.
	RedirectURIs []string

	// TokenEndpointAuthMethod is the client's registered auth method:
	// "none" (public client) or one of the client_secret_* methods
	// (confidential client). Required — there is no default, so callers must
	// choose explicitly rather than drifting into one.
	TokenEndpointAuthMethod string

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

// dcrIssued is the marker identifying clients built by this package (i.e.
// issued via dynamic client registration or an equivalent explicit
// registration.New call). Storage backends use it to tell DCR-issued
// registrations — which carry an anti-bloat TTL — from pre-provisioned
// clients, which must never acquire one. The method is unexported so the
// marker cannot be implemented outside this package.
type dcrIssued interface {
	dcrIssued()
}

// DCRIssued reports whether client was issued by this package.
func DCRIssued(client fosite.Client) bool {
	_, ok := client.(dcrIssued)
	return ok
}

// MarkDCRIssued wraps a client rebuilt from persisted DCR-issued form so the
// DCRIssued marker — and with it the anti-bloat TTL behaviour in storage —
// survives the storage round-trip. Callers must only mark clients they know
// were DCR-issued; pre-provisioned clients must never carry the marker.
//
// client must be one of the two concrete shapes clientFromStored produces:
// *fosite.DefaultOpenIDConnectClient (a row with a recorded
// token_endpoint_auth_method) or *fosite.DefaultClient (a row with none). The
// type switch below embeds whichever concrete type it was given rather than
// the fosite.Client interface, so every method the concrete type implements —
// now and in the future, including optional fosite interfaces like
// ClientWithSecretRotation — promotes automatically instead of being silently
// dropped, which is exactly what embedding the interface would do (fosite
// type-asserts for ClientWithSecretRotation.GetRotatedHashes during secret
// validation). Any other concrete type is a caller bug and panics rather than
// risk that silent drop.
func MarkDCRIssued(client fosite.Client) fosite.Client {
	switch c := client.(type) {
	case *fosite.DefaultOpenIDConnectClient:
		return &markedDCRIssuedOIDC{DefaultOpenIDConnectClient: c}
	case *fosite.DefaultClient:
		return &markedDCRIssuedDefault{DefaultClient: c}
	default:
		panic(fmt.Sprintf("registration: MarkDCRIssued: unsupported concrete client type %T", client))
	}
}

type markedDCRIssuedOIDC struct {
	*fosite.DefaultOpenIDConnectClient
}

func (markedDCRIssuedOIDC) dcrIssued() {}

type markedDCRIssuedDefault struct {
	*fosite.DefaultClient
}

func (markedDCRIssuedDefault) dcrIssued() {}

type dcrIssuedMarker struct{}

func (dcrIssuedMarker) dcrIssued() {}

// publicClient is the DCR-issued public client shape: an OIDC client (so the
// "none" method is recorded and enforced) with loopback redirect matching for
// native apps.
type publicClient struct {
	dcrIssuedMarker
	*LoopbackClient
}

// confidentialClient is the DCR-issued confidential client shape: an OIDC
// client so fosite pins and enforces the registered auth method at the token
// endpoint. It is deliberately NOT a LoopbackClient — a secret-holding client
// gets no dynamic-port matching.
type confidentialClient struct {
	dcrIssuedMarker
	*fosite.DefaultOpenIDConnectClient
}

// GenerateClientSecret mints a new client secret: 32 bytes of crypto/rand
// output, base64url-encoded (43 characters, no padding). RawURLEncoding is
// load-bearing, not cosmetic: fosite url.QueryUnescape's both Basic-auth
// components, so a secret containing '+', '/', or '%' would be corrupted or
// rejected at the token endpoint.
func GenerateClientSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate client secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// New creates a fosite.Client from the given configuration.
// Public clients ("none") are wrapped in LoopbackClient to support RFC 8252
// Section 7.3 compliant loopback redirect URI matching for native OAuth
// clients. Confidential clients (client_secret_basic / client_secret_post)
// require a Secret, have it SHA-256 hashed (see SHA256Hasher), and are not
// loopback-wrapped.
func New(cfg Config) (fosite.Client, error) {
	// Validate the auth method explicitly: silently defaulting an empty or
	// unknown value would reclassify the client one layer up, the same
	// public-ification bug the storage read path fails closed against.
	switch cfg.TokenEndpointAuthMethod {
	case oauthproto.TokenEndpointAuthMethodNone,
		oauthproto.TokenEndpointAuthMethodClientSecretBasic,
		oauthproto.TokenEndpointAuthMethodClientSecretPost:
	default:
		return nil, fmt.Errorf("unsupported token_endpoint_auth_method: %q", cfg.TokenEndpointAuthMethod)
	}
	public := cfg.TokenEndpointAuthMethod == oauthproto.TokenEndpointAuthMethodNone

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
		Public:        public,
	}

	// Hash the secret for confidential clients. fosite compares the stored
	// hash with the presented secret using the hasher configured on
	// fosite.Config.ClientSecretsHasher, so this must use the same SHA-256
	// hasher — see SHA256Hasher for why no KDF is used.
	if !public {
		if cfg.Secret == "" {
			return nil, fmt.Errorf("confidential client requires a secret")
		}
		hashedSecret, err := SHA256Hasher.Hash(context.Background(), []byte(cfg.Secret))
		if err != nil {
			return nil, fmt.Errorf("failed to hash client secret: %w", err)
		}
		defaultClient.Secret = hashedSecret
	}

	oidcClient := &fosite.DefaultOpenIDConnectClient{
		DefaultClient:           defaultClient,
		TokenEndpointAuthMethod: cfg.TokenEndpointAuthMethod,
	}

	// Public clients get the LoopbackClient wrapper for RFC 8252 Section 7.3
	// dynamic port matching on native-app loopback redirect URIs; confidential
	// clients do not (no dynamic-port matching for a secret holder).
	if public {
		return &publicClient{LoopbackClient: NewLoopbackClient(oidcClient)}, nil
	}
	return &confidentialClient{DefaultOpenIDConnectClient: oidcClient}, nil
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
