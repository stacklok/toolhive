// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"time"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// defaultUpstreamRedirectPath is the redirect path derived from the issuer
// origin when the caller's run-config does not supply an explicit RedirectURI.
// Matches the authserver's public callback route.
const defaultUpstreamRedirectPath = "/oauth/callback"

// authMethodPreference is the preferred order of token_endpoint_auth_methods,
// most preferred first. The resolver intersects this list with the server's
// advertised methods and picks the first match.
//
// Rationale: private_key_jwt is cryptographically strongest (asymmetric, no
// shared secret on the wire). client_secret_basic and client_secret_post are
// equally secure in transit but basic is marginally preferred because the
// credentials do not appear in request-body logs. "none" is the fallback for
// public PKCE clients.
var authMethodPreference = []string{
	"private_key_jwt",
	"client_secret_basic",
	"client_secret_post",
	"none",
}

// DCRResolution captures the full RFC 7591 + RFC 7592 response for a
// successful Dynamic Client Registration, together with the endpoints the
// upstream advertises so the caller need not re-discover them.
//
// The struct is the unit of storage in DCRCredentialStore and the unit of
// application via applyResolution.
type DCRResolution struct {
	// ClientID is the RFC 7591 "client_id" returned by the authorization
	// server.
	ClientID string

	// ClientSecret is the RFC 7591 "client_secret" returned by the
	// authorization server. Empty for public PKCE clients.
	ClientSecret string

	// AuthorizationEndpoint is the discovered (or configured) authorization
	// endpoint for this upstream.
	AuthorizationEndpoint string

	// TokenEndpoint is the discovered (or configured) token endpoint for this
	// upstream.
	TokenEndpoint string

	// RegistrationAccessToken is the RFC 7592 "registration_access_token"
	// required for subsequent registration management operations (update,
	// read, delete).
	RegistrationAccessToken string

	// RegistrationClientURI is the RFC 7592 "registration_client_uri" for
	// registration management operations.
	RegistrationClientURI string

	// TokenEndpointAuthMethod is the authentication method negotiated at the
	// token endpoint for this client.
	TokenEndpointAuthMethod string

	// CreatedAt is the wall-clock time at which the resolution was completed.
	// Used by Step 2g observability to compute staleness against
	// dcrStaleAgeThreshold.
	CreatedAt time.Time
}

// needsDCR reports whether rc requires runtime Dynamic Client Registration.
// A run-config needs DCR exactly when ClientID is empty and DCRConfig is
// non-nil (the mutually-exclusive constraint is enforced by
// OAuth2UpstreamRunConfig.Validate; this helper is a convenience check).
func needsDCR(rc *authserver.OAuth2UpstreamRunConfig) bool {
	if rc == nil {
		return false
	}
	return rc.ClientID == "" && rc.DCRConfig != nil
}

// applyResolution copies resolved credentials and endpoints from res into rc.
// Callers must pass a COPY of the upstream run-config (per the
// copy-before-mutate rule in .claude/rules/go-style.md); applyResolution does
// not clone rc internally.
//
// ClientID is always overwritten by the resolved value; callers must pass a
// copy whose ClientID is empty. resolveDCRCredentials enforces this via its
// precondition check in validateResolveInputs — a stray pre-seeded ClientID
// would be silently clobbered by this function otherwise.
//
// The authorization and token endpoints are written only when rc does not
// already specify them, so explicit caller configuration always wins.
func applyResolution(rc *authserver.OAuth2UpstreamRunConfig, res *DCRResolution) {
	if rc == nil || res == nil {
		return
	}
	rc.ClientID = res.ClientID
	if rc.AuthorizationEndpoint == "" {
		rc.AuthorizationEndpoint = res.AuthorizationEndpoint
	}
	if rc.TokenEndpoint == "" {
		rc.TokenEndpoint = res.TokenEndpoint
	}
	// Note: the resolved ClientSecret is NOT copied onto rc because
	// OAuth2UpstreamRunConfig models secrets as file-or-env references, not
	// inline values. Callers that need the resolved secret must read it from
	// the DCRResolution directly.
}

// scopesHash returns the SHA-256 hex digest of the canonical scope set.
//
// Canonicalisation:
//  1. Sort ascending so the digest is order-insensitive — e.g.
//     []string{"openid", "profile"} and []string{"profile", "openid"} hash to
//     the same value.
//  2. Deduplicate so that []string{"openid"} and []string{"openid", "openid"}
//     hash to the same value. An OAuth scope set is a set, not a multiset
//     (RFC 6749 §3.3), and without deduplication a caller that accidentally
//     duplicated a scope would miss cache entries and trigger redundant
//     RFC 7591 registrations.
//  3. Join with newlines (a character not valid in OAuth scope tokens per
//     RFC 6749 §3.3) to avoid collision between e.g. ["ab", "c"] and
//     ["a", "bc"].
func scopesHash(scopes []string) string {
	sorted := slices.Clone(scopes)
	sort.Strings(sorted)
	sorted = slices.Compact(sorted)

	h := sha256.New()
	for i, s := range sorted {
		if i > 0 {
			_, _ = h.Write([]byte("\n"))
		}
		_, _ = h.Write([]byte(s))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// resolveDCRCredentials performs Dynamic Client Registration for rc against
// the given issuer, caching the resulting credentials in cache. On cache hit
// the resolver returns immediately without any network I/O.
//
// rc must have ClientID == "" and DCRConfig != nil — the caller is expected
// to have validated this via OAuth2UpstreamRunConfig.Validate. issuer is the
// upstream authorization server's issuer identifier used both for discovery
// and for keying the cache.
//
// The caller is responsible for applying the returned resolution onto a COPY
// of rc via applyResolution (per the copy-before-mutate rule). This function
// neither mutates rc nor the cache on failure.
func resolveDCRCredentials(
	ctx context.Context,
	rc *authserver.OAuth2UpstreamRunConfig,
	issuer string,
	cache DCRCredentialStore,
) (*DCRResolution, error) {
	if err := validateResolveInputs(rc, issuer, cache); err != nil {
		return nil, err
	}

	redirectURI, err := resolveUpstreamRedirectURI(rc.RedirectURI, issuer)
	if err != nil {
		return nil, fmt.Errorf("dcr: resolve redirect uri: %w", err)
	}

	scopes := slices.Clone(rc.Scopes)
	key := DCRKey{
		Issuer:      issuer,
		RedirectURI: redirectURI,
		ScopesHash:  scopesHash(scopes),
	}

	// Cache lookup short-circuits before any network I/O.
	if cached, hit, err := lookupCachedResolution(ctx, cache, key, issuer, redirectURI); err != nil {
		return nil, err
	} else if hit {
		return cached, nil
	}

	// Endpoint resolution: discover metadata when configured, otherwise use
	// the caller-supplied RegistrationEndpoint directly.
	endpoints, err := resolveDCREndpoints(ctx, rc.DCRConfig, issuer)
	if err != nil {
		return nil, err
	}
	applyExplicitEndpointOverrides(endpoints, rc)

	// Token-endpoint auth method: intersect server support with our
	// preference order; default to client_secret_basic if the server does
	// not advertise the field at all.
	authMethod, err := selectTokenEndpointAuthMethod(endpoints.tokenEndpointAuthMethodsSupported)
	if err != nil {
		return nil, fmt.Errorf("dcr: %w", err)
	}

	registrationScopes := chooseRegistrationScopes(scopes, endpoints.scopesSupported, issuer)

	response, err := performRegistration(ctx, rc.DCRConfig, endpoints.registrationEndpoint,
		redirectURI, authMethod, registrationScopes)
	if err != nil {
		return nil, err
	}

	resolution := buildResolution(response, endpoints, authMethod)

	// Write to durable storage before updating caller state (per
	// .claude/rules/go-style.md "write to durable storage before in-memory").
	if err := cache.Put(ctx, key, resolution); err != nil {
		return nil, fmt.Errorf("dcr: cache put: %w", err)
	}

	//nolint:gosec // G706: client_id is public metadata per RFC 7591.
	slog.Debug("dcr: registered new client",
		"issuer", issuer,
		"redirect_uri", redirectURI,
		"client_id", resolution.ClientID,
	)
	return resolution, nil
}

// -----------------------------------------------------------------------------
// Private helpers
// -----------------------------------------------------------------------------

// validateResolveInputs performs the defensive re-check of resolver
// preconditions. Validate() enforces most of these at config-load time, but
// resolveDCRCredentials is an entry point that programmatic callers can
// reach with partially-constructed run-configs.
func validateResolveInputs(
	rc *authserver.OAuth2UpstreamRunConfig,
	issuer string,
	cache DCRCredentialStore,
) error {
	if rc == nil {
		return fmt.Errorf("oauth2 upstream run-config is required")
	}
	if rc.ClientID != "" {
		return fmt.Errorf("dcr: oauth2 upstream has a pre-provisioned client_id")
	}
	if rc.DCRConfig == nil {
		return fmt.Errorf("dcr: oauth2 upstream has no dcr_config")
	}
	if issuer == "" {
		return fmt.Errorf("dcr: issuer is required")
	}
	if cache == nil {
		return fmt.Errorf("dcr: credential store is required")
	}
	return nil
}

// lookupCachedResolution checks the cache and logs the hit. On hit it
// returns (resolution, true, nil). On miss it returns (nil, false, nil). An
// error is returned only on backend failure.
func lookupCachedResolution(
	ctx context.Context,
	cache DCRCredentialStore,
	key DCRKey,
	issuer, redirectURI string,
) (*DCRResolution, bool, error) {
	cached, ok, err := cache.Get(ctx, key)
	if err != nil {
		return nil, false, fmt.Errorf("dcr: cache lookup: %w", err)
	}
	if !ok {
		return nil, false, nil
	}
	slog.Debug("dcr: cache hit",
		"issuer", issuer,
		"redirect_uri", redirectURI,
		"client_id", cached.ClientID,
	)
	return cached, true, nil
}

// applyExplicitEndpointOverrides overwrites the discovered
// authorizationEndpoint / tokenEndpoint in endpoints with explicit values
// from rc when rc specifies them. Explicit caller configuration always wins
// over discovery.
func applyExplicitEndpointOverrides(endpoints *dcrEndpoints, rc *authserver.OAuth2UpstreamRunConfig) {
	if rc.AuthorizationEndpoint != "" {
		endpoints.authorizationEndpoint = rc.AuthorizationEndpoint
	}
	if rc.TokenEndpoint != "" {
		endpoints.tokenEndpoint = rc.TokenEndpoint
	}
}

// chooseRegistrationScopes selects the scopes to send in the registration
// request: explicit caller scopes > discovered scopes_supported > empty.
// Logs a warning when neither source produces any scopes.
func chooseRegistrationScopes(explicit, discovered []string, issuer string) []string {
	if len(explicit) > 0 {
		return explicit
	}
	if len(discovered) > 0 {
		return discovered
	}
	slog.Warn("dcr: no scopes configured or discovered; registering with empty scope",
		"issuer", issuer,
	)
	return nil
}

// performRegistration executes the HTTP registration request exactly once.
// The initial access token (if configured) is injected as a
// bearer-token Authorization header via a wrapping http.Client.
func performRegistration(
	ctx context.Context,
	dcrCfg *authserver.DCRUpstreamConfig,
	registrationEndpoint, redirectURI, authMethod string,
	scopes []string,
) (*oauthproto.DynamicClientRegistrationResponse, error) {
	// Initial access token is optional; resolveSecret returns ("", nil)
	// when neither file nor env var is configured.
	initialAccessToken, err := resolveSecret(dcrCfg.InitialAccessTokenFile, dcrCfg.InitialAccessTokenEnvVar)
	if err != nil {
		return nil, fmt.Errorf("dcr: resolve initial access token: %w", err)
	}

	httpClient := newDCRHTTPClient(initialAccessToken)

	request := &oauthproto.DynamicClientRegistrationRequest{
		RedirectURIs:            []string{redirectURI},
		ClientName:              oauthproto.ToolHiveMCPClientName,
		TokenEndpointAuthMethod: authMethod,
		GrantTypes:              []string{oauthproto.GrantTypeAuthorizationCode, oauthproto.GrantTypeRefreshToken},
		ResponseTypes:           []string{oauthproto.ResponseTypeCode},
		Scopes:                  scopes,
	}

	// Call exactly once — no retry loop. Step 2g will add retry/backoff at a
	// higher layer if needed.
	response, err := oauthproto.RegisterClientDynamically(ctx, registrationEndpoint, request, httpClient)
	if err != nil {
		return nil, fmt.Errorf("dcr: register client: %w", err)
	}
	return response, nil
}

// buildResolution assembles the DCRResolution from the RFC 7591 response and
// the resolved endpoints. If the server did not echo a
// token_endpoint_auth_method in the response, the method actually sent is
// recorded so downstream consumers see a definite value.
func buildResolution(
	response *oauthproto.DynamicClientRegistrationResponse,
	endpoints *dcrEndpoints,
	sentAuthMethod string,
) *DCRResolution {
	authMethod := response.TokenEndpointAuthMethod
	if authMethod == "" {
		authMethod = sentAuthMethod
	}
	return &DCRResolution{
		ClientID:                response.ClientID,
		ClientSecret:            response.ClientSecret,
		AuthorizationEndpoint:   endpoints.authorizationEndpoint,
		TokenEndpoint:           endpoints.tokenEndpoint,
		RegistrationAccessToken: response.RegistrationAccessToken,
		RegistrationClientURI:   response.RegistrationClientURI,
		TokenEndpointAuthMethod: authMethod,
		CreatedAt:               time.Now(),
	}
}

// dcrEndpoints is the internal bundle of endpoints produced by endpoint
// resolution. The separation from DCRResolution lets the resolver reason
// about discovered vs. overridden values before committing to a resolution.
type dcrEndpoints struct {
	authorizationEndpoint             string
	tokenEndpoint                     string
	registrationEndpoint              string
	tokenEndpointAuthMethodsSupported []string
	scopesSupported                   []string
}

// resolveDCREndpoints produces the endpoint bundle from the DCRUpstreamConfig.
//
// Three branches, in priority order:
//
//  1. cfg.RegistrationEndpoint set — use it directly and skip discovery
//     entirely. Server-capability fields (token_endpoint_auth_methods_supported,
//     scopes_supported) are unavailable on this branch; the caller is
//     expected to also supply AuthorizationEndpoint, TokenEndpoint, and an
//     explicit Scopes list. Auth method falls back to the
//     selectTokenEndpointAuthMethod default.
//  2. cfg.DiscoveryURL set — fetch the exact document the operator
//     configured (bypassing the well-known path fallback). The returned
//     document's issuer must match the caller's issuer per RFC 8414 §3.3.
//  3. Neither set — defensive; Validate() rejects this configuration, but
//     as a programmatic entry point the resolver returns an error rather
//     than falling back to an unexpected strategy.
//
// When metadata is returned but omits registration_endpoint, the resolver
// synthesises {origin}/register — a convention used by nanobot and Hydra
// for providers that ship DCR without advertising it in discovery.
func resolveDCREndpoints(
	ctx context.Context,
	cfg *authserver.DCRUpstreamConfig,
	issuer string,
) (*dcrEndpoints, error) {
	if cfg.RegistrationEndpoint != "" {
		return &dcrEndpoints{
			registrationEndpoint: cfg.RegistrationEndpoint,
		}, nil
	}

	if cfg.DiscoveryURL == "" {
		return nil, fmt.Errorf(
			"dcr: dcr_config must set either discovery_url or registration_endpoint")
	}

	metadata, err := oauthproto.FetchAuthorizationServerMetadataFromURL(ctx, cfg.DiscoveryURL, issuer, nil)
	return endpointsFromMetadata(metadata, err, issuer)
}

// endpointsFromMetadata converts a FetchAuthorizationServerMetadata* result
// into a dcrEndpoints bundle. Handles the ErrRegistrationEndpointMissing
// sentinel by synthesising {origin}/register.
func endpointsFromMetadata(
	metadata *oauthproto.AuthorizationServerMetadata,
	fetchErr error,
	issuer string,
) (*dcrEndpoints, error) {
	switch {
	case errors.Is(fetchErr, oauthproto.ErrRegistrationEndpointMissing):
		// Metadata is otherwise valid — synthesise the registration
		// endpoint from the issuer origin. FetchAuthorizationServerMetadata*
		// deliberately returns ErrRegistrationEndpointMissing alongside a
		// non-nil metadata document, so use the returned endpoints/scopes.
		synth, err := synthesiseRegistrationEndpoint(issuer)
		if err != nil {
			return nil, fmt.Errorf("synthesise registration endpoint: %w", err)
		}
		return &dcrEndpoints{
			authorizationEndpoint:             metadata.AuthorizationEndpoint,
			tokenEndpoint:                     metadata.TokenEndpoint,
			registrationEndpoint:              synth,
			tokenEndpointAuthMethodsSupported: metadata.TokenEndpointAuthMethodsSupported,
			scopesSupported:                   metadata.ScopesSupported,
		}, nil
	case fetchErr != nil:
		return nil, fmt.Errorf("discover authorization server metadata: %w", fetchErr)
	}

	return &dcrEndpoints{
		authorizationEndpoint:             metadata.AuthorizationEndpoint,
		tokenEndpoint:                     metadata.TokenEndpoint,
		registrationEndpoint:              metadata.RegistrationEndpoint,
		tokenEndpointAuthMethodsSupported: metadata.TokenEndpointAuthMethodsSupported,
		scopesSupported:                   metadata.ScopesSupported,
	}, nil
}

// synthesiseRegistrationEndpoint builds {origin}/register from the issuer,
// used when discovery succeeds but omits registration_endpoint.
func synthesiseRegistrationEndpoint(issuer string) (string, error) {
	u, err := url.Parse(issuer)
	if err != nil {
		return "", fmt.Errorf("parse issuer: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("issuer missing scheme or host: %q", issuer)
	}
	origin := &url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/register"}
	return origin.String(), nil
}

// resolveUpstreamRedirectURI returns the redirect URI to present to the
// upstream. The caller-supplied value wins; otherwise a default is derived
// from the issuer + /oauth/callback. HTTPS is required except for loopback
// hosts (development).
func resolveUpstreamRedirectURI(configured, issuer string) (string, error) {
	if configured != "" {
		u, err := url.Parse(configured)
		if err != nil {
			return "", fmt.Errorf("invalid redirect uri %q: %w", configured, err)
		}
		if err := validateRedirectURL(u); err != nil {
			return "", err
		}
		return configured, nil
	}

	issuerURL, err := url.Parse(issuer)
	if err != nil {
		return "", fmt.Errorf("invalid issuer %q: %w", issuer, err)
	}
	ref, err := url.Parse(defaultUpstreamRedirectPath)
	if err != nil {
		return "", fmt.Errorf("parse default redirect path: %w", err)
	}
	resolved := issuerURL.ResolveReference(ref)
	if err := validateRedirectURL(resolved); err != nil {
		return "", err
	}
	return resolved.String(), nil
}

// validateRedirectURL enforces the HTTPS-except-loopback rule shared across
// OAuth URLs.
func validateRedirectURL(u *url.URL) error {
	if u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("redirect uri missing scheme or host: %q", u.String())
	}
	if u.Scheme != "https" && !networking.IsLocalhost(u.Host) {
		return fmt.Errorf("redirect uri must use https (got %q) unless host is loopback", u.Scheme)
	}
	return nil
}

// selectTokenEndpointAuthMethod returns the preferred token endpoint auth
// method given the server's advertised set. When the server does not
// advertise any methods the caller's default of client_secret_basic is used
// (RFC 6749 §2.3.1 baseline).
func selectTokenEndpointAuthMethod(serverSupported []string) (string, error) {
	if len(serverSupported) == 0 {
		return "client_secret_basic", nil
	}

	supported := make(map[string]struct{}, len(serverSupported))
	for _, m := range serverSupported {
		supported[m] = struct{}{}
	}

	for _, m := range authMethodPreference {
		if _, ok := supported[m]; ok {
			return m, nil
		}
	}
	return "", fmt.Errorf(
		"no supported token_endpoint_auth_method in server advertisement %v; "+
			"client supports %v", serverSupported, authMethodPreference)
}

// bearerTokenTransport is an http.RoundTripper that adds
// Authorization: Bearer {token} to each outgoing request. Used to supply the
// RFC 7591 initial access token to oauthproto.RegisterClientDynamically
// without leaking the abstraction up into that package.
type bearerTokenTransport struct {
	token string
	next  http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t *bearerTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone per http.RoundTripper contract: implementations must not modify
	// the input request's headers.
	cp := req.Clone(req.Context())
	cp.Header.Set("Authorization", "Bearer "+t.token)
	return t.next.RoundTrip(cp)
}

// newDCRHTTPClient returns the http.Client to pass to
// oauthproto.RegisterClientDynamically. When initialAccessToken is non-empty
// the client wraps the canonical DCR client's transport with a
// bearerTokenTransport; otherwise it returns nil so oauthproto builds its
// own default client.
//
// The timeout policy is sourced from oauthproto.NewDefaultDCRClient so
// future tightening of those bounds propagates automatically into the
// initial-access-token code path.
func newDCRHTTPClient(initialAccessToken string) *http.Client {
	if initialAccessToken == "" {
		// Letting oauthproto supply its default client keeps timeout /
		// transport tuning in one place.
		return nil
	}

	client := oauthproto.NewDefaultDCRClient()
	next := client.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	client.Transport = &bearerTokenTransport{
		token: initialAccessToken,
		next:  next,
	}
	return client
}
