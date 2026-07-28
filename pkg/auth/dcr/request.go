// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package dcr

// Request is the profile-neutral input to ResolveCredentials. Each consumer
// (embedded authserver, CLI OAuth flow) translates its domain types into a
// Request at the call site so the resolver does not import any consumer's
// shapes.
//
// The struct collects exactly the fields the resolver reads:
//
//   - Identity: Issuer (the caller's logical scope — see the Issuer field
//     doc for what each consumer puts there), Scopes, and RedirectURI.
//   - Endpoint discovery: DiscoveryURL or RegistrationEndpoint, with optional
//     explicit AuthorizationEndpoint / TokenEndpoint overrides.
//   - Registration metadata: InitialAccessToken, ClientName. The caller is
//     responsible for resolving any file-or-env reference (e.g. the embedded
//     authserver's InitialAccessTokenFile / InitialAccessTokenEnvVar) into
//     InitialAccessToken before constructing a Request.
//
// Mutually exclusive constraints are enforced at validation time:
//
//   - Exactly one of DiscoveryURL / RegistrationEndpoint must be non-empty.
//   - Issuer must be non-empty.
//
// Constructing a Request is the caller's responsibility; the resolver does
// not clone or mutate it.
type Request struct {
	// Issuer is the caller's logical scope for cache keying and (when
	// RedirectURI is empty) the basis for the default redirect URI
	// ({Issuer}/oauth/callback). Required.
	//
	// What each consumer puts here:
	//
	//   - Embedded authserver: its own issuer identifier. The authserver
	//     owns a distinct logical identity from the upstream IdP, and
	//     defaulting RedirectURI to {Issuer}/oauth/callback lands the
	//     callback on the authserver's origin (correct).
	//   - CLI OAuth flow: the upstream OAuth provider's issuer URL,
	//     because the CLI has no separate logical issuer of its own. The
	//     CLI always supplies an explicit loopback RedirectURI per
	//     RFC 8252 §7.3, so the redirect-URI defaulting is never
	//     exercised on this path.
	//
	// The resolver's cache key is (Issuer, UpstreamID, RedirectURI,
	// ScopesHash), where UpstreamID is derived by the resolver from
	// DiscoveryURL / RegistrationEndpoint (issue #5823). The two consumers
	// do not collide on the key because the embedded authserver's
	// RedirectURI lives on the authserver's origin and the CLI's lives on a
	// loopback (RFC 8252 §7.3) — even when Issuer happens to match between
	// the two profiles, the RedirectURI component keeps the cache key
	// distinct; UpstreamID additionally keeps two upstreams within a single
	// consumer apart. See the cross-consumer caveat on dcrFlight in
	// resolver.go for the wider invariant.
	//
	// Issuer is *not* used for RFC 8414 §3.3 metadata verification —
	// that uses an issuer recovered from DiscoveryURL inside the
	// resolver.
	Issuer string

	// RedirectURI is the redirect URI to register with the upstream. When
	// empty, the resolver derives {Issuer}/oauth/callback. HTTPS is required
	// except for loopback hosts (RFC 8252 §7.3 — the CLI flow uses
	// http://localhost:{port}/callback).
	RedirectURI string

	// Scopes are the OAuth scopes to request in the registration body.
	// May be empty; the resolver will fall back to discovered scopes if the
	// upstream advertises any.
	Scopes []string

	// DiscoveryURL points at the upstream's RFC 8414 / OIDC Discovery
	// document. The resolver fetches this URL exactly once and reads
	// registration_endpoint, authorization_endpoint, token_endpoint,
	// token_endpoint_auth_methods_supported, scopes_supported, and
	// code_challenge_methods_supported from it. On this branch the resolver
	// also derives the cache key's UpstreamID from the upstream issuer
	// recovered from this URL.
	//
	// Mutually exclusive with RegistrationEndpoint.
	DiscoveryURL string

	// RegistrationEndpoint is used directly when set, bypassing discovery.
	// On this branch the caller is expected to also supply AuthorizationEndpoint
	// and TokenEndpoint explicitly; the resolver's auth-method selection
	// defaults to client_secret_basic because no server-capability fields
	// are available unless CodeChallengeMethodsSupported is also set. This
	// URL is also the cache key's UpstreamID on this branch.
	//
	// Mutually exclusive with DiscoveryURL.
	RegistrationEndpoint string

	// CodeChallengeMethodsSupported lists the PKCE challenge methods the
	// upstream advertises. Only consulted when RegistrationEndpoint is set;
	// on the DiscoveryURL branch the resolver reads this value from the
	// fetched metadata document. Callers that have already performed
	// multi-URL AS metadata discovery (e.g. via
	// oauthproto.FetchAuthorizationServerMetadata) should populate this
	// field so the S256 PKCE gate fires correctly without a redundant
	// round-trip.
	CodeChallengeMethodsSupported []string

	// AuthorizationEndpoint, when non-empty, overrides any value discovered
	// via DiscoveryURL. Explicit caller configuration always wins.
	AuthorizationEndpoint string

	// TokenEndpoint, when non-empty, overrides any value discovered via
	// DiscoveryURL. Explicit caller configuration always wins.
	TokenEndpoint string

	// InitialAccessToken is the RFC 7591 initial access token presented to
	// the registration endpoint as a Bearer header. The caller resolves any
	// file-or-env reference before populating this field.
	InitialAccessToken string

	// ClientName is sent as the RFC 7591 "client_name" registration
	// metadata. When empty, the resolver uses
	// oauthproto.ToolHiveMCPClientName.
	ClientName string

	// PublicClient, when true, instructs the resolver to register as a
	// public PKCE client (token_endpoint_auth_method=none) regardless of
	// what other methods the upstream advertises. This matches the CLI
	// flow's intent (RFC 8252 §8.4 native public clients) and the
	// resolver still enforces the RFC 7636 / OAuth 2.1 S256 PKCE gate:
	// when the upstream's discovery metadata does not advertise S256 in
	// code_challenge_methods_supported, the registration is refused with
	// a clear error rather than silently downgrading.
	//
	// When false (the embedded-authserver default), the resolver picks
	// the strongest auth method the upstream advertises (private_key_jwt
	// > client_secret_basic > client_secret_post > none, with the same
	// S256 gate on "none").
	//
	// Has no effect on the RegistrationEndpoint-direct branch when neither
	// DiscoveryURL nor CodeChallengeMethodsSupported is set: without
	// code_challenge_methods_supported the S256 gate cannot be evaluated,
	// so the resolver refuses to register as a public client.
	PublicClient bool

	// AllowPrivateIPs permits both of the resolver's outbound calls — the
	// discovery fetch to DiscoveryURL and the registration POST to the
	// resolved registration endpoint — to connect to private IP ranges.
	// See networking.NewHostScopedClientBuilder for the CWE-918 guard policy
	// this widens (loopback exemption, dial-time re-check, etc.). Set this to
	// true only when the upstream authorization server is reachable solely
	// over a private address (e.g. an in-cluster IdP with no public
	// endpoint); defaults to false. Mirrors the AllowPrivateIPs posture
	// already carried by the OAuth2 and OIDC upstream configs so a DCR
	// upstream is not the one outbound-facing path without an SSRF guard.
	AllowPrivateIPs bool
}
