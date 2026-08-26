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
	"log/slog"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/go-jose/go-jose/v3"
	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/oauthproto"
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

	// JSONWebKeys contains the validated inline public keys for private_key_jwt.
	JSONWebKeys *jose.JSONWebKeySet

	// TokenEndpointAuthSigningAlgorithm is the validated signing algorithm for
	// private_key_jwt client authentication.
	TokenEndpointAuthSigningAlgorithm string
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
// validation).
//
// Any other concrete type is a caller bug, but this sits on the GetClient
// path reachable from the unauthenticated /oauth/authorize endpoint, so it
// must not crash the process. Falling back to the client unwrapped, with an
// error log naming the type, is a bounded degradation: the row loses its
// DCRIssued marker and so stops renewing its TTL, which is recoverable —
// unlike a panic on an unauthenticated request path.
func MarkDCRIssued(client fosite.Client) fosite.Client {
	switch c := client.(type) {
	case *fosite.DefaultOpenIDConnectClient:
		return &markedDCRIssuedOIDC{DefaultOpenIDConnectClient: c}
	case *fosite.DefaultClient:
		return &markedDCRIssuedDefault{DefaultClient: c}
	default:
		slog.Error("registration: MarkDCRIssued: unsupported concrete client type, returning unmarked",
			"type", fmt.Sprintf("%T", client))
		return client
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
// "none" method is recorded and enforced). RFC 8252 Section 7.3 loopback
// dynamic-port matching for native apps is provided separately by
// RegisteredLoopbackRedirectURI, not by this type.
type publicClient struct {
	dcrIssuedMarker
	*fosite.DefaultOpenIDConnectClient
}

// confidentialClient is the DCR-issued confidential client shape: an OIDC
// client so fosite pins and enforces the registered auth method at the token
// endpoint. RegisteredLoopbackRedirectURI refuses dynamic-port matching for
// any non-public client, so a secret-holding client never gets it.
type confidentialClient struct {
	dcrIssuedMarker
	*fosite.DefaultOpenIDConnectClient
}

// privateKeyJWTClient is a DCR-issued client authenticated with an inline
// public key rather than a client secret. It is non-public because it must
// authenticate at the token endpoint, while retaining the DCR marker.
type privateKeyJWTClient struct {
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
// Public clients get TokenEndpointAuthMethod "none" via DefaultOpenIDConnectClient;
// RFC 8252 Section 7.3 loopback redirect URI matching for native OAuth clients is
// provided separately by RegisteredLoopbackRedirectURI, not by the client type itself.
// Confidential clients with secrets have their Secret field SHA-256 hashed
// (see SHA256Hasher) as required by fosite for credential validation.
func New(cfg Config) (fosite.Client, error) {
	// Validate the auth method explicitly: silently defaulting an empty or
	// unknown value would reclassify the client one layer up, the same
	// public-ification bug the storage read path fails closed against.
	switch cfg.TokenEndpointAuthMethod {
	case oauthproto.TokenEndpointAuthMethodNone,
		oauthproto.TokenEndpointAuthMethodClientSecretBasic,
		oauthproto.TokenEndpointAuthMethodClientSecretPost,
		oauthproto.TokenEndpointAuthMethodPrivateKeyJWT:
	default:
		return nil, fmt.Errorf("unsupported token_endpoint_auth_method: %q", cfg.TokenEndpointAuthMethod)
	}
	public := cfg.TokenEndpointAuthMethod == oauthproto.TokenEndpointAuthMethodNone
	privateKeyJWT := cfg.TokenEndpointAuthMethod == oauthproto.TokenEndpointAuthMethodPrivateKeyJWT

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
	if !public && !privateKeyJWT {
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
		DefaultClient:                     defaultClient,
		TokenEndpointAuthMethod:           cfg.TokenEndpointAuthMethod,
		JSONWebKeys:                       cloneJSONWebKeySet(cfg.JSONWebKeys),
		TokenEndpointAuthSigningAlgorithm: cfg.TokenEndpointAuthSigningAlgorithm,
	}

	if public {
		return &publicClient{DefaultOpenIDConnectClient: oidcClient}, nil
	}
	if privateKeyJWT {
		return &privateKeyJWTClient{DefaultOpenIDConnectClient: oidcClient}, nil
	}
	return &confidentialClient{DefaultOpenIDConnectClient: oidcClient}, nil
}

func cloneJSONWebKeySet(jwks *jose.JSONWebKeySet) *jose.JSONWebKeySet {
	if jwks == nil {
		return nil
	}
	return &jose.JSONWebKeySet{Keys: append([]jose.JSONWebKey(nil), jwks.Keys...)}
}

// NewConfidentialPlain creates a DCR-issued confidential client as a plain
// *fosite.DefaultClient (Public: false, hashed secret), NOT the
// *fosite.DefaultOpenIDConnectClient shape New produces for an ordinary
// confidential registration.
//
// The difference matters: fosite only enforces token_endpoint_auth_method for
// clients implementing fosite.OpenIDConnectClient (see
// client_authentication.go in fosite v0.49.0 — AuthenticateClient type-
// switches on that interface before checking the method at all). A plain
// *fosite.DefaultClient with Public=false accepts credentials via either HTTP
// Basic or the form body and verifies whichever is presented. Use this
// constructor when the caller does not know which presentation the client
// will use — pinning the wrong one yields an invalid_client the operator
// cannot debug remotely.
//
// This is the shape ValidateDCRRequest's override path uses when a request's
// redirect_uris matches an operator-configured
// Config.ForceConfidentialRedirectURIs entry: such a client declared itself
// public but requires a secret, and the operator cannot know in advance
// whether its OAuth library presents credentials via Basic or form body.
//
// TokenEndpointAuthMethod on cfg is ignored; the returned client has no
// pinned method by construction. Ignores cfg.GrantTypes/ResponseTypes
// defaulting the same way New does. The returned client always carries the
// DCRIssued marker (see MarkDCRIssued) so storage retention applies.
func NewConfidentialPlain(cfg Config) (fosite.Client, error) {
	if cfg.Secret == "" {
		return nil, fmt.Errorf("confidential client requires a secret")
	}

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

	hashedSecret, err := SHA256Hasher.Hash(context.Background(), []byte(cfg.Secret))
	if err != nil {
		return nil, fmt.Errorf("failed to hash client secret: %w", err)
	}

	defaultClient := &fosite.DefaultClient{
		ID:            cfg.ID,
		Secret:        hashedSecret,
		RedirectURIs:  cfg.RedirectURIs,
		ResponseTypes: responseTypes,
		GrantTypes:    grantTypes,
		Scopes:        scopes,
		Audience:      cfg.Audience,
		Public:        false,
	}

	return MarkDCRIssued(defaultClient), nil
}

// NewStaticDelegateClient creates an unmarked, pre-provisioned confidential
// client for token exchange. A bare DefaultClient intentionally leaves the
// token endpoint authentication method unpinned, allowing both HTTP Basic and
// form-body client-secret authentication.
func NewStaticDelegateClient(cfg Config) (*fosite.DefaultClient, error) {
	if cfg.ID == "" {
		return nil, fmt.Errorf("delegate client requires an ID")
	}
	if cfg.Secret == "" {
		return nil, fmt.Errorf("confidential client requires a secret")
	}
	if len(cfg.GrantTypes) != 1 || cfg.GrantTypes[0] != oauthproto.GrantTypeTokenExchange {
		return nil, fmt.Errorf("delegate client grant types must be exactly [%q]", oauthproto.GrantTypeTokenExchange)
	}
	if len(cfg.Scopes) == 0 {
		return nil, fmt.Errorf("delegate client requires at least one scope")
	}
	if len(cfg.Audience) == 0 {
		return nil, fmt.Errorf("delegate client requires at least one audience")
	}

	hashedSecret, err := SHA256Hasher.Hash(context.Background(), []byte(cfg.Secret))
	if err != nil {
		return nil, fmt.Errorf("failed to hash client secret: %w", err)
	}

	return &fosite.DefaultClient{
		ID:         cfg.ID,
		Secret:     hashedSecret,
		GrantTypes: slices.Clone(cfg.GrantTypes),
		Scopes:     slices.Clone(cfg.Scopes),
		Audience:   slices.Clone(cfg.Audience),
		Public:     false,
	}, nil
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
	//
	// The registered side gets the same userinfo check: registration
	// validation (oauthproto.ValidateRedirectURI, backed by fosite's
	// IsValidRedirectURI) only rejects a missing fragment, not userinfo, so a
	// registered "http://user:pass@localhost/callback" would otherwise match
	// a dynamic-port request for "http://localhost:54321/callback" -- the
	// hostname/path/query comparisons below never look at User, so dropping
	// this check would let a registered entry's userinfo silently vanish from
	// what "matching" means.
	if requested.Fragment != "" || requested.User != nil || registered.User != nil {
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

// isLoopbackURI reports whether uri's host is a loopback address (an IP
// loopback literal or "localhost", case-insensitively) — a hostname-only
// check, with no DNS resolution. Used by ValidateConfidentialRedirectURIs in
// dcr.go to reject a confidential registration's redirect_uri that targets
// loopback, which RFC 8252 §7.3 reserves for native/public clients.
func isLoopbackURI(uri string) bool {
	parsed, err := url.Parse(uri)
	if err != nil {
		return false
	}
	return isLoopbackHostname(parsed.Hostname())
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
