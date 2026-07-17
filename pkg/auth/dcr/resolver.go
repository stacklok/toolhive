// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package dcr is the shared RFC 7591 Dynamic Client Registration client used
// by every consumer in the codebase that needs to register a downstream
// OAuth 2.x client at runtime. The package owns the stateful concerns of the
// flow — credential cache, in-process singleflight deduplication, scope-set
// canonicalisation, token-endpoint auth-method selection (with the RFC 7636 /
// OAuth 2.1 S256 PKCE gate), RFC 7591 §3.2.1 expiry-driven cache invalidation,
// the bearer-token transport with redirect refusal, and panic recovery around
// the registration body. Stateless RFC 7591 wire-shape primitives live in
// pkg/oauthproto.
//
// # API surface
//
// ResolveCredentials takes a profile-neutral Request and a CredentialStore
// and returns a Resolution. The Request carries only the fields the resolver
// actually reads (issuer, redirect URI, scopes, discovery URL or registration
// endpoint, optional explicit endpoint overrides, optional initial access
// token, optional registration metadata); each consumer translates its
// domain types into a Request at the call site so the resolver does not
// import any consumer's shapes.
//
// Two consumers exist today:
//
//   - pkg/authserver/runner constructs a Request from
//     *authserver.OAuth2UpstreamRunConfig and uses its own adapter helpers
//     (needsDCR / consumeResolution / applyResolutionToOAuth2Config in
//     runner/dcr_adapter.go) to fold the resolution back into its
//     run-config and built upstream.OAuth2Config.
//   - pkg/auth/discovery constructs a Request from *OAuthFlowConfig for the
//     CLI OAuth flow and copies the returned Resolution onto OAuthFlowResult.
//
// # Concurrency
//
// The package maintains a process-global singleflight keyed on the tuple
// (issuer, upstreamID, redirectURI, scopesHash) so concurrent
// ResolveCredentials calls across all consumers in a single process
// coalesce when their cache keys match. Consumers that share all four
// values will share a flight — the deduplication is a feature for the
// embedded authserver but means callers cannot assume per-call-site flight
// isolation. See the dcrFlight doc comment below for the rationale.
//
// See issue #5145 for the design discussion that motivated lifting this out
// of pkg/authserver/runner.
package dcr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// dcrFlight coalesces concurrent ResolveCredentials calls that share the
// same Key. Two goroutines hitting the resolver for the same upstream and
// scope set will both miss the cache, so without coalescing they would both
// call RegisterClientDynamically and the loser's registration would become
// orphaned at the upstream IdP — an operator-visible cleanup task and
// possibly a transient startup failure if the upstream rate-limits
// concurrent registrations. Followers wait for the leader's result and
// observe the same Resolution.
//
// Lifetime: process-wide. This intentionally contrasts with the
// CredentialStore the embedded authserver constructs and injects into
// ResolveCredentials, which is per-instance for the memory backend and
// shared across replicas for Redis. The asymmetry is load-bearing: the
// singleflight only deduplicates the in-flight network call, while the
// cache deduplicates the resolution itself across calls. Process-wide
// flight means concurrent EmbeddedAuthServer instances in the same
// process targeting the same upstream still get deduplicated; the
// injected cache decides whether the resolution is fresh enough to
// reuse. A Redis-backed store still wants this in-process gate so a
// single replica does not double-register against itself.
//
// Cross-consumer caveat: because dcrFlight is package-global, two
// consumers that happen to construct identical Keys (same issuer, same
// upstream ID, same redirect URI, same scopes hash) will share a single
// in-flight registration even if they semantically want different client
// profiles.
// The two current call sites do not collide by construction — the embedded
// authserver's redirect URI lives on the AS origin, and the CLI flow's
// redirect URI lives on a loopback (http://localhost:{port}/callback per
// RFC 8252 §7.3), so the disjoint RedirectURI address spaces separate
// the public-client and confidential-client profiles at both the
// singleflight and the persistent-cache layers. A future consumer that
// defaults its redirect URI into either of those spaces would silently
// coalesce; if a third profile is added the flight key (and persisted
// DCRKey) MUST gain a consumer-identifier component so a collision is
// impossible rather than improbable.
var dcrFlight singleflight.Group

// flightKeyOf canonicalises a Key into the singleflight string used by
// dcrFlight. Each field is length-prefixed (the same scheme redisDCRKey
// uses in pkg/authserver/storage) so the key is unambiguous regardless of
// field contents; ScopesHash is a SHA-256 hex digest with no colons, so it
// is appended without a prefix, exactly as redisDCRKey does. Exposed as a
// function so tests and future inspection helpers can compute the exact key
// the resolver would route through dcrFlight without re-implementing the
// encoding.
//
// Length-prefixing rather than a "separator byte that never appears":
// UpstreamID is NOT guaranteed to be a clean URL when this key is built. On
// the CLI/remote path it is req.RegistrationEndpoint copied verbatim from
// the upstream's discovery-document JSON, and validateUpstreamEndpointURL
// does not run until later, inside the singleflight body
// (resolveDCREndpoints) — after this key already exists. A newline (or any
// byte) in a discovery-supplied registration_endpoint would therefore reach
// a naive "\n"-joined key, where a crafted value could byte-collide with a
// concurrently-registering upstream and, as the singleflight follower,
// observe that upstream's Resolution. Length-prefixing removes the
// assumption entirely and aligns the flight key with the persisted DCRKey.
//
// UpstreamID keeps two upstreams that share Issuer, RedirectURI, and
// scopes from coalescing into a single flight (issue #5823) — the same
// distinction the persisted DCRKey draws, so the singleflight and cache
// layers stay aligned.
//
// PublicClient is intentionally NOT part of the flight key — the
// dcrFlight doc above explains why: today's two consumers register on
// disjoint RedirectURI address spaces (AS-origin vs RFC 8252 loopback),
// so the public-client and confidential-client profiles cannot share a
// flight key by construction. The same property protects the persisted
// DCRKey from cross-profile coalescence; the two layers are aligned
// rather than asymmetric.
func flightKeyOf(key Key) string {
	return fmt.Sprintf("%d:%s:%d:%s:%d:%s:%s",
		len(key.Issuer), key.Issuer,
		len(key.UpstreamID), key.UpstreamID,
		len(key.RedirectURI), key.RedirectURI,
		key.ScopesHash)
}

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

// Resolution captures the full RFC 7591 + RFC 7592 response for a
// successful Dynamic Client Registration, together with the endpoints the
// upstream advertises so the caller need not re-discover them.
//
// The struct is the unit of storage in CredentialStore and the unit of
// application that consumers project back into their own domain types
// (e.g. consumeResolution / applyResolutionToOAuth2Config in
// pkg/authserver/runner/dcr_adapter.go, or direct OAuthFlowResult field
// writes in pkg/auth/discovery).
//
// MUST update both converters (resolutionToCredentials and
// credentialsToResolution in store.go) when adding, renaming, or
// removing a field here. The two converters are the seam between this
// dcr-package type and the persisted *storage.DCRCredentials shape; a
// field added here without a paired converter update will silently fail
// to round-trip across an authserver restart, the exact "parallel types
// drift" failure mode .claude/rules/go-style.md warns about. The
// round-trip behaviour is pinned by TestResolutionCredentialsRoundTrip
// in store_test.go.
type Resolution struct {
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

	// RedirectURI is the redirect URI presented to the authorization server
	// during registration. When the caller's Request did not specify one,
	// this holds the defaulted value derived from the issuer + /oauth/callback
	// (via resolveUpstreamRedirectURI). Persisting it on the resolution lets
	// consumers (pkg/authserver/runner.consumeResolution) write it back onto
	// the run-config COPY so that downstream code (buildPureOAuth2Config,
	// upstream.OAuth2Config validation) sees a non-empty RedirectURI.
	RedirectURI string

	// ClientIDIssuedAt is the RFC 7591 §3.2.1 "client_id_issued_at" value
	// converted to a Go time.Time. Zero when the upstream omitted the field
	// (the field is OPTIONAL per RFC 7591). Informational; not used to
	// invalidate the cache.
	ClientIDIssuedAt time.Time

	// ClientSecretExpiresAt is the RFC 7591 §3.2.1 "client_secret_expires_at"
	// value converted to a Go time.Time. The wire convention is that 0 means
	// "the secret does not expire"; in this struct that is represented by
	// the zero time.Time so callers can use IsZero() rather than special-
	// casing 0.
	//
	// When non-zero, this field is the authoritative signal that
	// lookupCachedResolution uses to refetch credentials before the upstream
	// rejects them at the token endpoint. The 90-day dcrStaleAgeThreshold
	// is a heuristic for "consider rotating"; this is a hard expiry asserted
	// by the upstream itself.
	ClientSecretExpiresAt time.Time

	// CreatedAt is the wall-clock time at which the resolution was completed.
	// Used by the staleness observability in lookupCachedResolution to
	// compute age against dcrStaleAgeThreshold and emit a warn log when a
	// cached registration exceeds the threshold.
	CreatedAt time.Time
}

// Step identifiers for structured error logs emitted by the caller of
// ResolveCredentials. These values flow through the "step" attribute so
// operators can narrow failures to a specific phase without parsing error
// messages. They are reported only at the boundary log — see
// dcrStepError — so a single failure produces a single slog.Error record.
const (
	dcrStepValidate         = "validate"
	dcrStepResolveRedirect  = "resolve_redirect_uri"
	dcrStepResolveUpstream  = "resolve_upstream_id"
	dcrStepCacheRead        = "cache_read"
	dcrStepMetadata         = "metadata_discovery"
	dcrStepSelectAuthMethod = "select_auth_method"
	dcrStepRegister         = "dcr_call"
	dcrStepCacheWrite       = "cache_write"
)

// dcrStepError annotates a resolver error with the phase it was produced
// in. The boundary caller (buildUpstreamConfigs) emits the single
// slog.Error record for the failure; individual error branches inside
// ResolveCredentials do not log so that each failure surfaces exactly
// once in the combined log stream.
//
// RedirectURI is included when known so that operators can correlate the
// failure with a specific upstream registration without parsing the
// wrapped error string. Stack carries a captured stack trace for the
// dcrStepRegister panic-recovery branch so LogStepError can include
// it in the single boundary record without the in-defer site emitting
// its own duplicate slog.Error. A zero-value dcrStepError is invalid;
// construct via newDCRStepError or the resolver's internal helpers.
type dcrStepError struct {
	Step        string
	Issuer      string
	RedirectURI string
	Stack       string
	Err         error
}

// Error implements error. The "step" tag mirrors the structured-log
// attribute so command-line log scrapers see the same phase identifier.
func (e *dcrStepError) Error() string {
	return fmt.Sprintf("dcr: %s: %s", e.Step, e.Err.Error())
}

// Unwrap lets errors.Is / errors.As reach the wrapped cause.
func (e *dcrStepError) Unwrap() error { return e.Err }

// newDCRStepError builds a dcrStepError. It never returns nil for a
// non-nil cause.
func newDCRStepError(step, issuer, redirectURI string, err error) *dcrStepError {
	return &dcrStepError{
		Step:        step,
		Issuer:      issuer,
		RedirectURI: redirectURI,
		Err:         err,
	}
}

// ResolveCredentials performs Dynamic Client Registration for req against
// the upstream authorization server identified by req.DiscoveryURL or
// req.RegistrationEndpoint, caching the resulting credentials in cache.
// On cache hit the resolver returns immediately without any network I/O.
//
// req.Issuer is *this caller's* logical issuer identifier, NOT the upstream's.
// It is used to key the cache and to default the redirect URI to
// {req.Issuer}/oauth/callback when req.RedirectURI is empty. The upstream's
// issuer is recovered separately from req.DiscoveryURL inside the resolver
// and is used solely for RFC 8414 §3.3 metadata verification. Passing the
// upstream's issuer in req.Issuer would produce a wrong-origin default
// redirect and a cache key that does not identify the caller context.
//
// The resolver does not mutate req or the cache on failure.
//
// Observability: this function never calls slog.Error directly — all
// failures are annotated with a *dcrStepError and returned to the caller,
// which is expected to emit the boundary Error record. This avoids the
// double-logging pattern where both the resolver and the outer frame
// report the same failure. Cache-hit Debug / stale-Warn logs and the
// successful-registration Debug log are emitted here because they have no
// outer-frame equivalent. No secret values (client_secret,
// registration_access_token, initial_access_token) are ever logged — only
// public metadata such as client_id and redirect_uri.
func ResolveCredentials(
	ctx context.Context,
	req *Request,
	cache CredentialStore,
) (*Resolution, error) {
	if err := validateResolveInputs(req, cache); err != nil {
		issuer := ""
		if req != nil {
			issuer = req.Issuer
		}
		return nil, newDCRStepError(dcrStepValidate, issuer, "", err)
	}

	redirectURI, err := resolveUpstreamRedirectURI(req.RedirectURI, req.Issuer)
	if err != nil {
		return nil, newDCRStepError(dcrStepResolveRedirect, req.Issuer, "",
			fmt.Errorf("resolve redirect uri: %w", err))
	}

	// Identify the upstream authorization server so two upstreams that share
	// req.Issuer, redirectURI, and scopes — the common case within a single
	// embedded authserver — do not collide on one cache entry (issue #5823).
	upstreamID, err := resolveUpstreamKeyIdentity(req)
	if err != nil {
		return nil, newDCRStepError(dcrStepResolveUpstream, req.Issuer, redirectURI,
			fmt.Errorf("resolve upstream identity: %w", err))
	}

	scopes := slices.Clone(req.Scopes)
	key := Key{
		Issuer:      req.Issuer,
		UpstreamID:  upstreamID,
		RedirectURI: redirectURI,
		ScopesHash:  storage.ScopesHash(scopes),
	}

	// Cache lookup short-circuits before any network I/O.
	if cached, hit, err := lookupCachedResolution(ctx, cache, key, req.Issuer, redirectURI); err != nil {
		return nil, newDCRStepError(dcrStepCacheRead, req.Issuer, redirectURI, err)
	} else if hit {
		return cached, nil
	}

	// Coalesce concurrent registrations for the same Key — see dcrFlight
	// doc comment. The leader runs the registerOnce closure; followers
	// receive the leader's *Resolution result. The flight key embeds the
	// Key fields with a separator that cannot appear in any of them
	// (newline is not valid in OAuth scope tokens, URLs, or hex digests).
	//
	// A defer/recover inside the closure converts a panic in registerAndCache
	// (or anything it calls) into a normal error. Without this, singleflight
	// re-panics the leader's panic in every follower — N concurrent callers
	// for the same Key would all crash with the same value. The panic is
	// still surfaced: the captured stack trace is attached to the wrapped
	// dcrStepError and surfaces in the single boundary log emitted by
	// LogStepError, so the failure produces exactly one Error record (no
	// in-defer log here) and callers can react to it as a normal failure.
	flightKey := flightKeyOf(key)
	resolutionAny, err, _ := dcrFlight.Do(flightKey, func() (res any, err error) {
		defer func() {
			if r := recover(); r != nil {
				stepErr := newDCRStepError(dcrStepRegister, req.Issuer, redirectURI,
					fmt.Errorf("registration panicked: %v", r))
				stepErr.Stack = string(debug.Stack())
				err = stepErr
				res = nil
			}
		}()
		return registerAndCache(ctx, req, redirectURI, scopes, key, cache)
	})
	if err != nil {
		return nil, err
	}
	return resolutionAny.(*Resolution), nil
}

// registerAndCache is the leader-only body of ResolveCredentials wrapped
// by the singleflight. It rechecks the cache before any network I/O so
// followers that arrive after the leader's Put returns immediately see the
// fresh entry on a subsequent call. Endpoint resolution, registration, and
// the durable Put live here.
func registerAndCache(
	ctx context.Context,
	req *Request,
	redirectURI string,
	scopes []string,
	key Key,
	cache CredentialStore,
) (*Resolution, error) {
	// Recheck cache: another flight that just finished may have populated
	// it between our initial lookup and our singleflight entry.
	if cached, hit, err := lookupCachedResolution(ctx, cache, key, req.Issuer, redirectURI); err != nil {
		return nil, newDCRStepError(dcrStepCacheRead, req.Issuer, redirectURI, err)
	} else if hit {
		return cached, nil
	}

	// Endpoint resolution: discover metadata when configured, otherwise use
	// the caller-supplied RegistrationEndpoint directly. The upstream's
	// expected issuer is recovered from req.DiscoveryURL inside the helper.
	// req.Issuer here is *this caller's* issuer — correct for cache keying
	// and redirect URI defaulting, but it must not be used for RFC 8414
	// §3.3 metadata verification (which is the upstream's concern).
	endpoints, err := resolveDCREndpoints(ctx, req)
	if err != nil {
		return nil, newDCRStepError(dcrStepMetadata, req.Issuer, redirectURI, err)
	}
	if err := applyExplicitEndpointOverrides(endpoints, req); err != nil {
		return nil, newDCRStepError(dcrStepMetadata, req.Issuer, redirectURI, err)
	}

	// Token-endpoint auth method: intersect server support with our
	// preference order; default to client_secret_basic if the server does
	// not advertise the field at all. When the caller declared the
	// registration is for a public PKCE client (CLI flow), force
	// token_endpoint_auth_method=none while still enforcing the S256
	// PKCE gate.
	authMethod, err := selectTokenEndpointAuthMethod(
		endpoints.tokenEndpointAuthMethodsSupported,
		endpoints.codeChallengeMethodsSupported,
		req.PublicClient,
	)
	if err != nil {
		return nil, newDCRStepError(dcrStepSelectAuthMethod, req.Issuer, redirectURI, err)
	}

	registrationScopes := chooseRegistrationScopes(scopes, endpoints.scopesSupported, req.Issuer)

	response, err := performRegistration(ctx, req, endpoints.registrationEndpoint,
		redirectURI, authMethod, registrationScopes)
	if err != nil {
		return nil, newDCRStepError(dcrStepRegister, req.Issuer, redirectURI, err)
	}

	resolution := buildResolution(response, endpoints, authMethod, redirectURI)

	// Write to durable storage before returning the resolution so a Put
	// failure leaves no in-memory state diverging from the cache: the
	// next call simply re-resolves rather than reading a value the cache
	// never saw.
	if err := cache.Put(ctx, key, resolution); err != nil {
		return nil, newDCRStepError(dcrStepCacheWrite, req.Issuer, redirectURI,
			fmt.Errorf("cache put: %w", err))
	}

	//nolint:gosec // G706: client_id is public metadata per RFC 7591.
	slog.Debug("dcr: registered new client",
		"local_issuer", req.Issuer,
		"upstream_id", key.UpstreamID,
		"redirect_uri", redirectURI,
		"client_id", resolution.ClientID,
	)
	return resolution, nil
}

// LogStepError emits the single boundary slog.Error record for a DCR
// resolver failure, carrying the step / issuer / redirect_uri attributes
// extracted from err. If err is not a *dcrStepError, it is logged with a
// generic "unknown" step — ResolveCredentials always wraps its errors,
// so this branch indicates a programming error in a future caller rather
// than a runtime condition. err == nil is a no-op so this function is
// safe to call without an outer guard.
//
// Every wrapped error is passed through SanitizeErrorForLog to strip URL
// query parameters that could plausibly contain sensitive tokens (defense
// in depth — the current DCR flow sends the initial access token as an
// Authorization header, not a query parameter, but nothing in the type
// system prevents a future refactor from doing otherwise).
func LogStepError(upstreamName string, err error) {
	if err == nil {
		return
	}
	var stepErr *dcrStepError
	if !errors.As(err, &stepErr) {
		slog.Error("dcr: resolve failed",
			"upstream", upstreamName,
			"step", "unknown",
			"error", SanitizeErrorForLog(err),
		)
		return
	}

	attrs := []any{
		"upstream", upstreamName,
		"step", stepErr.Step,
		"issuer", stepErr.Issuer,
		"error", SanitizeErrorForLog(stepErr.Err),
	}
	if stepErr.RedirectURI != "" {
		attrs = append(attrs, "redirect_uri", stepErr.RedirectURI)
	}
	if stepErr.Stack != "" {
		attrs = append(attrs, "stack", stepErr.Stack)
	}
	slog.Error("dcr: resolve failed", attrs...)
}

// SanitizeErrorForLog strips secret-bearing components from any URLs
// embedded in err's message. The Go HTTP client, url.Error, and other
// net/* wrappers embed the full request URL — including userinfo,
// query, and fragment — in their error strings. Any of those can carry
// credentials or tokens (e.g. https://user:pass@host, ?token=…,
// implicit-flow callbacks #access_token=…); the current DCR flow does
// not embed any of them today, but stripping them here is defense in
// depth that protects the log regardless of future changes.
//
// Scheme, host, and path are preserved so operators retain enough
// context to correlate with upstream server logs. Trailing sentence
// punctuation adjacent to the URL (e.g. the comma in "reaching
// URL?q=1, retrying") is preserved — see trimURLTrailingPunctuation
// for the list of characters considered terminators.
//
// Scope: the regex matches http://, https://, redis://, and rediss://
// schemes (case-insensitively per RFC 3986 §3.1). The redis schemes are
// included because the embedded-authserver DCR path persists through
// pkg/authserver/storage/redis.go and a redis-go error chain on the
// Get/Put critical path can embed a sentinel/cluster URL with
// credentials. Other schemes (file://, postgres://, smtp://, raw
// host:port) are NOT sanitised; if a future call site flows error
// strings from one of those into LogStepError, extend the regex here
// rather than re-implementing the sanitiser at the call site.
//
// IMPORTANT — caller responsibility: this function strips credentials
// only from the schemes listed above. Callers that may receive errors
// containing other credential-bearing URL schemes MUST verify those
// URLs are not credential-bearing before logging, or sanitise them
// separately.
func SanitizeErrorForLog(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	return queryStrippingPattern.ReplaceAllStringFunc(msg, func(match string) string {
		// Split any trailing sentence punctuation off the match before
		// handing it to url.Parse. Without this, a period / comma /
		// closing bracket at the end of the sentence is absorbed into
		// the URL's raw query and dropped along with the rest of the
		// query component, mangling the error text. The trimmed
		// punctuation is re-appended to the replacement so the
		// surrounding prose is preserved verbatim.
		core, tail := trimURLTrailingPunctuation(match)
		u, parseErr := url.Parse(core)
		if parseErr != nil {
			return match
		}
		if u.User == nil && u.RawQuery == "" && u.Fragment == "" {
			return match
		}
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		return u.String() + tail
	})
}

// trimURLTrailingPunctuation returns (core, tail) where tail is the run of
// trailing ASCII punctuation that commonly terminates a URL inside prose
// but is never a meaningful part of the URL itself. The characters chosen
// here mirror those used by general-purpose URL extractors (e.g.,
// Chromium's autolinker): sentence-ending punctuation, closing brackets,
// and a few separators that appear between URLs in freeform text.
//
// Note that ')' and ']' are stripped unconditionally — a URL legitimately
// containing a percent-encoded closing bracket will have it as "%29" or
// "%5D", not as a literal, so this cannot truncate a real URL path or
// query. The reverse case (an unescaped ')' inside a path) is
// non-conforming per RFC 3986 and out of scope for a log sanitiser.
func trimURLTrailingPunctuation(s string) (core, tail string) {
	// terminators is intentionally ASCII-only; Unicode terminators (e.g.
	// '」') are out of scope for this log sanitiser, so byte indexing is
	// safe and avoids the rune-decoding overhead of strings.ContainsRune.
	const terminators = ".,;:!?)]}>"
	i := len(s)
	for i > 0 && strings.IndexByte(terminators, s[i-1]) >= 0 {
		i--
	}
	return s[:i], s[i:]
}

// queryStrippingPattern matches URL-shaped substrings inside an error
// message — sufficient to reach the url.Parse path in SanitizeErrorForLog
// and let it decide whether a secret-bearing component exists to strip.
// The regexp covers the schemes that actually flow through the DCR
// resolver's error paths today (http/https from upstream calls, redis/
// rediss from the persistent CredentialStore on the embedded-authserver
// path). It matches schemes case-insensitively per RFC 3986 §3.1 since
// upstream metadata or user input can carry mixed-case schemes.
// Trailing sentence punctuation that the character class happens to
// include (e.g. '.', ',', ')') is stripped by
// trimURLTrailingPunctuation before the match is parsed.
var queryStrippingPattern = regexp.MustCompile(`(?i)(?:https?|rediss?)://[^\s"']+`)

// -----------------------------------------------------------------------------
// Private helpers
// -----------------------------------------------------------------------------

// validateResolveInputs performs the defensive re-check of resolver
// preconditions. Each consumer is expected to validate its own domain
// types upstream of the resolver; this is the final-line check for an
// entry point that programmatic callers can reach with partially-
// constructed Requests.
func validateResolveInputs(
	req *Request,
	cache CredentialStore,
) error {
	if req == nil {
		return fmt.Errorf("dcr: request is required")
	}
	if req.Issuer == "" {
		return fmt.Errorf("dcr: issuer is required")
	}
	if req.DiscoveryURL == "" && req.RegistrationEndpoint == "" {
		return fmt.Errorf("dcr: request must set either discovery_url or registration_endpoint")
	}
	if req.DiscoveryURL != "" && req.RegistrationEndpoint != "" {
		return fmt.Errorf("dcr: discovery_url and registration_endpoint are mutually exclusive")
	}
	if cache == nil {
		return fmt.Errorf("dcr: credential store is required")
	}
	return nil
}

// lookupCachedResolution checks the cache and logs the hit. On hit it
// returns (resolution, true, nil). On miss it returns (nil, false, nil). An
// error is returned only on backend failure.
//
// Two distinct staleness signals shape the hit/miss decision and the log:
//
//   - Hard expiry (RFC 7591 §3.2.1 client_secret_expires_at): when the
//     cached resolution's ClientSecretExpiresAt is non-zero and in the
//     past, the entry is treated as a miss so the singleflight body
//     (registerAndCache) re-runs the registration and overwrites the stale
//     entry via cache.Put. Without this check the cache would serve an
//     expired secret indefinitely; the upstream's token endpoint would 401
//     on every use and the resolver would have no signal to refetch. The
//     check is skipped when the field is zero, per the RFC 7591 convention
//     "0 means the secret does not expire". This is the authoritative
//     signal — the upstream said when its credential expires.
//   - Soft staleness (now - CreatedAt vs dcrStaleAgeThreshold): the age in
//     days is logged on every hit, and if it exceeds the threshold an
//     additional slog.Warn is emitted with a remediation hint so operators
//     can act on long-lived registrations that may need rotation or
//     re-registration. This is observability only, not a cache-invalidation
//     trigger.
func lookupCachedResolution(
	ctx context.Context,
	cache CredentialStore,
	key Key,
	localIssuer, redirectURI string,
) (*Resolution, bool, error) {
	cached, ok, err := cache.Get(ctx, key)
	if err != nil {
		return nil, false, fmt.Errorf("dcr: cache lookup: %w", err)
	}
	if !ok {
		return nil, false, nil
	}
	if !cached.ClientSecretExpiresAt.IsZero() && time.Now().After(cached.ClientSecretExpiresAt) {
		//nolint:gosec // G706: client_id is public metadata per RFC 7591.
		slog.Debug("dcr: cache hit ignored; cached secret expired per upstream client_secret_expires_at",
			"local_issuer", localIssuer,
			"upstream_id", key.UpstreamID,
			"redirect_uri", redirectURI,
			"client_id", cached.ClientID,
			"client_secret_expires_at", cached.ClientSecretExpiresAt.UTC().Format(time.RFC3339),
		)
		return nil, false, nil
	}

	age := time.Since(cached.CreatedAt)
	ageDays := int(age / (24 * time.Hour))

	//nolint:gosec // G706: client_id is public metadata per RFC 7591.
	slog.Debug("dcr: cache hit",
		"local_issuer", localIssuer,
		"upstream_id", key.UpstreamID,
		"redirect_uri", redirectURI,
		"client_id", cached.ClientID,
		"dcr_age_days", ageDays,
	)

	if age > dcrStaleAgeThreshold {
		//nolint:gosec // G706: client_id is public metadata per RFC 7591.
		slog.Warn(
			"dcr: cached registration exceeds staleness threshold; "+
				"consider rotating the registration via RFC 7592 deregistration "+
				"and re-registering at next startup",
			"local_issuer", localIssuer,
			"upstream_id", key.UpstreamID,
			"redirect_uri", redirectURI,
			"client_id", cached.ClientID,
			"dcr_age_days", ageDays,
			"stale_threshold_days", int(dcrStaleAgeThreshold/(24*time.Hour)),
		)
	}

	return cached, true, nil
}

// applyExplicitEndpointOverrides overwrites the discovered
// authorizationEndpoint / tokenEndpoint in endpoints with explicit values
// from req when req specifies them. Explicit caller configuration always
// wins over discovery.
//
// Both overrides run through validateUpstreamEndpointURL even though the
// known consumer call sites only ever populate these fields from
// already-validated sources. validateUpstreamEndpointURL is the documented
// gatekeeper "every point where an endpoint URL enters the resolver from
// outside" — applying it here closes the override path's gap rather than
// relying on the caller-side validation to never drift.
func applyExplicitEndpointOverrides(endpoints *dcrEndpoints, req *Request) error {
	if req.AuthorizationEndpoint != "" {
		if err := validateUpstreamEndpointURL(req.AuthorizationEndpoint, "authorization_endpoint"); err != nil {
			return fmt.Errorf("explicit %w", err)
		}
		endpoints.authorizationEndpoint = req.AuthorizationEndpoint
	}
	if req.TokenEndpoint != "" {
		if err := validateUpstreamEndpointURL(req.TokenEndpoint, "token_endpoint"); err != nil {
			return fmt.Errorf("explicit %w", err)
		}
		endpoints.tokenEndpoint = req.TokenEndpoint
	}
	return nil
}

// chooseRegistrationScopes selects the scopes to send in the registration
// request: explicit caller scopes > discovered scopes_supported > empty.
// Logs a warning when neither source produces any scopes.
func chooseRegistrationScopes(explicit, discovered []string, localIssuer string) []string {
	if len(explicit) > 0 {
		return explicit
	}
	if len(discovered) > 0 {
		return discovered
	}
	slog.Warn("dcr: no scopes configured or discovered; registering with empty scope",
		"local_issuer", localIssuer,
	)
	return nil
}

// performRegistration executes the HTTP registration request exactly once.
// The initial access token (if configured) is injected as a
// bearer-token Authorization header via a wrapping http.Client.
//
// The caller is responsible for resolving any file-or-env initial access
// token reference into req.InitialAccessToken before calling
// ResolveCredentials; the resolver does not touch the filesystem or
// environment.
func performRegistration(
	ctx context.Context,
	req *Request,
	registrationEndpoint, redirectURI, authMethod string,
	scopes []string,
) (*oauthproto.DynamicClientRegistrationResponse, error) {
	httpClient, err := newDCRHTTPClient(req.InitialAccessToken, registrationEndpoint, req.AllowPrivateIPs)
	if err != nil {
		return nil, fmt.Errorf("dcr: build registration http client: %w", err)
	}

	clientName := req.ClientName
	if clientName == "" {
		clientName = oauthproto.ToolHiveMCPClientName
	}

	registrationRequest := &oauthproto.DynamicClientRegistrationRequest{
		RedirectURIs:            []string{redirectURI},
		ClientName:              clientName,
		TokenEndpointAuthMethod: authMethod,
		GrantTypes:              []string{oauthproto.GrantTypeAuthorizationCode, oauthproto.GrantTypeRefreshToken},
		ResponseTypes:           []string{oauthproto.ResponseTypeCode},
		Scopes:                  scopes,
	}

	// Call exactly once — no retry loop. If retry/backoff against
	// transient upstream failures becomes useful it belongs at a higher
	// layer (above ResolveCredentials), so the singleflight and cache
	// semantics here stay simple: one attempt per resolution per process.
	response, err := oauthproto.RegisterClientDynamically(ctx, registrationEndpoint, registrationRequest, httpClient)
	if err != nil {
		return nil, fmt.Errorf("dcr: register client: %w", err)
	}
	return response, nil
}

// buildResolution assembles the Resolution from the RFC 7591 response and
// the resolved endpoints. If the server did not echo a
// token_endpoint_auth_method in the response, the method actually sent is
// recorded so downstream consumers see a definite value. redirectURI is the
// value passed to the registration endpoint (caller-supplied or defaulted
// via resolveUpstreamRedirectURI); it is persisted on the resolution so
// consumers (pkg/authserver/runner.consumeResolution) can propagate a
// defaulted value back to the run-config.
//
// RFC 7591 §3.2.1 client_id_issued_at and client_secret_expires_at are
// converted from int64 epoch seconds to time.Time. The wire value 0 means
// "field absent" or "secret does not expire"; both map to the zero time.Time
// so callers can use IsZero() uniformly.
func buildResolution(
	response *oauthproto.DynamicClientRegistrationResponse,
	endpoints *dcrEndpoints,
	sentAuthMethod string,
	redirectURI string,
) *Resolution {
	authMethod := response.TokenEndpointAuthMethod
	if authMethod == "" {
		authMethod = sentAuthMethod
	}
	return &Resolution{
		ClientID:                response.ClientID,
		ClientSecret:            response.ClientSecret,
		AuthorizationEndpoint:   endpoints.authorizationEndpoint,
		TokenEndpoint:           endpoints.tokenEndpoint,
		RegistrationAccessToken: response.RegistrationAccessToken,
		RegistrationClientURI:   response.RegistrationClientURI,
		TokenEndpointAuthMethod: authMethod,
		RedirectURI:             redirectURI,
		ClientIDIssuedAt:        epochSecondsToTime(response.ClientIDIssuedAt),
		ClientSecretExpiresAt:   epochSecondsToTime(response.ClientSecretExpiresAt),
		CreatedAt:               time.Now(),
	}
}

// epochSecondsToTime converts the int64 epoch-seconds form used by RFC 7591
// into a time.Time. Zero passes through to the zero time.Time so callers can
// rely on IsZero() to mean "field absent" / "does not expire".
func epochSecondsToTime(epoch int64) time.Time {
	if epoch == 0 {
		return time.Time{}
	}
	return time.Unix(epoch, 0).UTC()
}

// dcrEndpoints is the internal bundle of endpoints produced by endpoint
// resolution. The separation from Resolution lets the resolver reason
// about discovered vs. overridden values before committing to a resolution.
type dcrEndpoints struct {
	authorizationEndpoint             string
	tokenEndpoint                     string
	registrationEndpoint              string
	tokenEndpointAuthMethodsSupported []string
	scopesSupported                   []string
	// codeChallengeMethodsSupported is consumed by
	// selectTokenEndpointAuthMethod to gate the public-client (none) auth
	// method on S256 PKCE being advertised. RFC 7636 / OAuth 2.1 require
	// PKCE-with-S256 for public clients; registering as none against an
	// upstream that advertises only plain (or omits the field) would be a
	// compliance gap.
	codeChallengeMethodsSupported []string
}

// resolveDCREndpoints produces the endpoint bundle from req.
//
// Two branches, in priority order:
//
//  1. req.RegistrationEndpoint set — use it directly and skip discovery
//     entirely. Server-capability fields (token_endpoint_auth_methods_supported,
//     scopes_supported) are unavailable on this branch; the caller is
//     expected to also supply AuthorizationEndpoint, TokenEndpoint, and an
//     explicit Scopes list. Auth method falls back to the
//     selectTokenEndpointAuthMethod default.
//  2. req.DiscoveryURL set — fetch the exact document the operator
//     configured (bypassing the well-known path fallback). RFC 8414 §3.3
//     requires the metadata's "issuer" field to match the authorization
//     server's issuer identifier; that identifier is the upstream's, not
//     this caller's, so it is recovered from the discovery URL via
//     deriveExpectedIssuerFromDiscoveryURL rather than reusing
//     req.Issuer (which names this caller and is used elsewhere in
//     ResolveCredentials for redirect URI defaulting and cache keying).
//
// validateResolveInputs enforces that exactly one of the two is set, so
// this function does not re-check the both-empty case.
//
// When metadata is returned but omits registration_endpoint, the resolver
// synthesises {origin}/register — a convention used by nanobot and Hydra
// for providers that ship DCR without advertising it in discovery. Origin
// is taken from the upstream issuer, not this caller's issuer, so the
// synthesised endpoint lands at the upstream.
func resolveDCREndpoints(
	ctx context.Context,
	req *Request,
) (*dcrEndpoints, error) {
	if req.RegistrationEndpoint != "" {
		// Validate locally so a non-HTTPS or malformed URL fails before
		// performRegistration constructs a bearer-token transport for it.
		if err := validateUpstreamEndpointURL(req.RegistrationEndpoint, "registration_endpoint"); err != nil {
			return nil, fmt.Errorf("dcr: %w", err)
		}
		return &dcrEndpoints{
			registrationEndpoint:          req.RegistrationEndpoint,
			codeChallengeMethodsSupported: req.CodeChallengeMethodsSupported,
		}, nil
	}

	upstreamIssuer, err := deriveExpectedIssuerFromDiscoveryURL(req.DiscoveryURL)
	if err != nil {
		return nil, err
	}

	// Dial the discovery fetch through the private-IP-guarded client so a
	// DiscoveryURL that resolves to a private/loopback/link-local address —
	// or rebinds to one after the caller validated it — is refused at connect
	// time (CWE-918). Guarded by default; req.AllowPrivateIPs opts in to
	// private ranges for an in-cluster upstream.
	discoveryHost, err := hostFromURL(req.DiscoveryURL)
	if err != nil {
		return nil, err
	}
	discoveryClient, err := newGuardedDCRClient(discoveryHost, req.AllowPrivateIPs)
	if err != nil {
		return nil, fmt.Errorf("dcr: build discovery http client: %w", err)
	}
	// The discovery URL is operator-configured, but the document it returns —
	// and any 30x the upstream serves in response — is upstream-controlled.
	// Restrict the fetch to same-host redirects so a malicious redirect cannot
	// walk the request onto another host (CWE-918); this matters even when the
	// dial guard is relaxed for a loopback discovery host. The registration
	// client refuses redirects outright because it carries the bearer token;
	// the discovery GET carries no secret, so same-host redirects are allowed —
	// matching the CLI discovery clients in pkg/auth/discovery.
	discoveryClient.CheckRedirect = networking.SameHostRedirectPolicy()

	metadata, err := oauthproto.FetchAuthorizationServerMetadataFromURL(ctx, req.DiscoveryURL, upstreamIssuer, discoveryClient)
	return endpointsFromMetadata(metadata, err, upstreamIssuer)
}

// resolveUpstreamKeyIdentity returns the stable identifier for the upstream
// authorization server a registration is bound to, used as the
// Key.UpstreamID cache component. It disambiguates upstreams that share the
// caller's Issuer, RedirectURI, and scope set — the common case inside a
// single embedded authserver — so they receive distinct dynamically-
// registered clients instead of colliding on one cache entry (issue #5823).
//
// The identity source mirrors resolveDCREndpoints' own branch order so the
// key names the exact server the client registers against:
//
//   - RegistrationEndpoint set: the registration endpoint URL itself.
//   - DiscoveryURL set: the upstream issuer recovered from the discovery URL
//     via deriveExpectedIssuerFromDiscoveryURL — the same value used for
//     RFC 8414 §3.3 metadata verification.
//
// validateResolveInputs guarantees exactly one of the two is set, so the
// final return is only reached on the DiscoveryURL branch. The derivation is
// pure URL parsing (no network I/O), so computing it before the cache lookup
// keeps the cache-hit path free of I/O; a malformed DiscoveryURL that fails
// here could never have produced a stored entry to hit anyway, because a
// successful registration requires the same derivation to succeed first.
//
// Residual limitation: on the DiscoveryURL branch the identity is the derived
// upstream *issuer*, not the discovery URL. For a custom (non-well-known)
// discovery URL, deriveExpectedIssuerFromDiscoveryURL falls back to the bare
// origin and discards the path, so two upstreams on the same host with
// distinct custom metadata paths, the same scopes, and both declaring that
// origin as their metadata "issuer" derive the same UpstreamID and still
// collide. This is intentional: UpstreamID names the authorization server
// (aligned with the RFC 8414 §3.3 issuer used for metadata verification), so
// two configs that resolve to the same issuer are the same AS and correctly
// share one registration. The collision is therefore confined to the
// nonstandard case where a single issuer serves distinct registration
// endpoints under custom (non-well-known) discovery paths: both upstreams
// resolve to that one issuer, so their UpstreamIDs match. Upstreams that
// resolve to different issuers derive different UpstreamIDs and never collide
// — the key itself keeps them apart, independent of §3.3 verification.
func resolveUpstreamKeyIdentity(req *Request) (string, error) {
	if req.RegistrationEndpoint != "" {
		return req.RegistrationEndpoint, nil
	}
	return deriveExpectedIssuerFromDiscoveryURL(req.DiscoveryURL)
}

// deriveExpectedIssuerFromDiscoveryURL recovers the issuer identifier the
// upstream is expected to claim in its RFC 8414 / OIDC Discovery document,
// given an operator-configured DiscoveryURL.
//
// Three recognised conventions:
//
//  1. Well-known suffix: the URL ends with /.well-known/oauth-authorization-server
//     or /.well-known/openid-configuration. The suffix is stripped to recover
//     the issuer; this covers single-tenant providers (e.g.
//     https://mcp.atlassian.com/.well-known/oauth-authorization-server →
//     https://mcp.atlassian.com) and the OIDC-style suffix-append shape used
//     by some multi-tenant providers (e.g.
//     https://idp.example.com/tenants/acme/.well-known/openid-configuration
//     → https://idp.example.com/tenants/acme).
//  2. RFC 8414 §3.1 path-insertion: the well-known path is inserted BETWEEN
//     the host and the issuer's tenant path. Per RFC 8414 §3 / RFC 8615,
//     this is the canonical form for issuers with a path component, e.g.
//     issuer https://example.com/v1/mcp →
//     discovery URL https://example.com/.well-known/oauth-authorization-server/v1/mcp.
//     The tenant suffix that appears AFTER the well-known segment is
//     re-attached to the origin to recover the issuer.
//  3. Non-well-known path: the URL points at a custom metadata endpoint that
//     contains neither suffix in a recognisable position. Origin
//     (scheme://host) is used as a best-effort fallback; this matches the
//     common shape where the upstream issuer is the host root.
//
// Case (1) and case (2) are disambiguated by where the well-known segment
// sits in the path: at the end ⇒ suffix-append, immediately after the host
// with more path following ⇒ path-insertion.
func deriveExpectedIssuerFromDiscoveryURL(discoveryURL string) (string, error) {
	const (
		oauthSuffix = "/.well-known/oauth-authorization-server"
		oidcSuffix  = "/.well-known/openid-configuration"
	)

	u, err := url.Parse(discoveryURL)
	if err != nil {
		return "", fmt.Errorf("parse discovery url %q: %w", discoveryURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("discovery url missing scheme or host: %q", discoveryURL)
	}

	switch {
	// Suffix-append form (case 1): well-known segment at end of path.
	case strings.HasSuffix(u.Path, oauthSuffix):
		u.Path = strings.TrimSuffix(u.Path, oauthSuffix)
	case strings.HasSuffix(u.Path, oidcSuffix):
		u.Path = strings.TrimSuffix(u.Path, oidcSuffix)
	// RFC 8414 §3.1 path-insertion form (case 2): well-known segment at the
	// start of the path with tenant path following. Strip just the well-known
	// segment to recover {origin}{tenant-path}.
	//
	// Two shapes hit this branch:
	//   1. A real tenant suffix follows the well-known segment, e.g.
	//      /.well-known/oauth-authorization-server/v1/mcp →
	//      issuer https://host/v1/mcp.
	//   2. A trailing slash with no tenant, e.g.
	//      /.well-known/oauth-authorization-server/ — TrimPrefix leaves
	//      a stray "/", which would yield a spurious issuer
	//      "https://host/" that fails the §3.3 byte-equality check
	//      against the upstream's declared "https://host". Normalise
	//      that stray "/" back to empty so case (2.2) and the bare
	//      suffix case derive the same origin issuer.
	case strings.HasPrefix(u.Path, oauthSuffix+"/"):
		u.Path = strings.TrimPrefix(u.Path, oauthSuffix)
		if u.Path == "/" {
			u.Path = ""
		}
	case strings.HasPrefix(u.Path, oidcSuffix+"/"):
		u.Path = strings.TrimPrefix(u.Path, oidcSuffix)
		if u.Path == "/" {
			u.Path = ""
		}
	default:
		// Custom (non-well-known) discovery URL — fall back to origin.
		u.Path = ""
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// endpointsFromMetadata converts a FetchAuthorizationServerMetadata* result
// into a dcrEndpoints bundle. Handles the ErrRegistrationEndpointMissing
// sentinel by synthesising {origin}/register.
//
// authorization_endpoint and token_endpoint are validated for HTTPS / well-
// formedness before being copied into the bundle. A self-consistent metadata
// document — possible if TLS to the metadata host is compromised, or if the
// upstream is misconfigured — could otherwise plant http:// URLs that flow
// through to the authorization-code and token-exchange call paths.
//
// Contract with oauthproto: FetchAuthorizationServerMetadata* guarantees a
// non-nil *AuthorizationServerMetadata whenever fetchErr is nil OR fetchErr
// is ErrRegistrationEndpointMissing (in the latter case the metadata is
// otherwise valid; only registration_endpoint is missing). The defensive
// nil guard below catches a future cross-package contract regression — e.g.,
// a new oauthproto sentinel that returns nil metadata alongside a non-fatal
// error — and converts it into a clean validation error rather than a
// nil-pointer dereference at the field accesses.
func endpointsFromMetadata(
	metadata *oauthproto.AuthorizationServerMetadata,
	fetchErr error,
	upstreamIssuer string,
) (*dcrEndpoints, error) {
	if fetchErr != nil && !errors.Is(fetchErr, oauthproto.ErrRegistrationEndpointMissing) {
		return nil, fmt.Errorf("discover authorization server metadata: %w", fetchErr)
	}
	if metadata == nil {
		return nil, fmt.Errorf(
			"dcr: authorization server metadata is nil (oauthproto contract " +
				"violation: nil metadata returned alongside a non-fatal fetch error)")
	}

	if err := validateUpstreamEndpointURL(metadata.AuthorizationEndpoint, "authorization_endpoint"); err != nil {
		return nil, fmt.Errorf("dcr: discovered %w", err)
	}
	if err := validateUpstreamEndpointURL(metadata.TokenEndpoint, "token_endpoint"); err != nil {
		return nil, fmt.Errorf("dcr: discovered %w", err)
	}

	registrationEndpoint := metadata.RegistrationEndpoint
	if errors.Is(fetchErr, oauthproto.ErrRegistrationEndpointMissing) {
		// Metadata is otherwise valid — synthesise the registration
		// endpoint from the upstream issuer's origin.
		// FetchAuthorizationServerMetadata* deliberately returns
		// ErrRegistrationEndpointMissing alongside a non-nil metadata
		// document, so we still use the returned endpoints/scopes.
		synth, err := synthesiseRegistrationEndpoint(upstreamIssuer)
		if err != nil {
			return nil, fmt.Errorf("synthesise registration endpoint: %w", err)
		}
		registrationEndpoint = synth
	} else if err := validateUpstreamEndpointURL(registrationEndpoint, "registration_endpoint"); err != nil {
		// Unlike the synthesised branch above, this value came straight from
		// the discovery document — validate it the same as
		// authorization_endpoint/token_endpoint rather than letting it reach
		// hostFromURL unvalidated.
		return nil, fmt.Errorf("dcr: discovered %w", err)
	}

	return &dcrEndpoints{
		authorizationEndpoint:             metadata.AuthorizationEndpoint,
		tokenEndpoint:                     metadata.TokenEndpoint,
		registrationEndpoint:              registrationEndpoint,
		tokenEndpointAuthMethodsSupported: metadata.TokenEndpointAuthMethodsSupported,
		scopesSupported:                   metadata.ScopesSupported,
		codeChallengeMethodsSupported:     metadata.CodeChallengeMethodsSupported,
	}, nil
}

// synthesiseRegistrationEndpoint builds {upstreamIssuer}/register, used when
// discovery succeeds but omits registration_endpoint. The argument is the
// upstream's issuer (recovered from the discovery URL), not this auth
// server's local issuer.
//
// The issuer's path is preserved so multi-tenant upstreams that ship DCR
// without advertising it (e.g. https://idp.example.com/tenants/acme) keep
// their tenant prefix in the synthesised URL. Stripping the path would land
// the registration request at a global /register that does not match the
// tenant-aware token/authorize URLs already accepted from metadata.
func synthesiseRegistrationEndpoint(upstreamIssuer string) (string, error) {
	u, err := url.Parse(upstreamIssuer)
	if err != nil {
		return "", fmt.Errorf("parse issuer: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("issuer missing scheme or host: %q", upstreamIssuer)
	}
	synth := &url.URL{
		Scheme: u.Scheme,
		Host:   u.Host,
		Path:   strings.TrimRight(u.Path, "/") + "/register",
	}
	return synth.String(), nil
}

// resolveUpstreamRedirectURI returns the redirect URI to present to the
// upstream. The caller-supplied value wins; otherwise a default is derived
// from {localIssuer}/oauth/callback. HTTPS is required except for loopback
// hosts (development).
//
// localIssuer here is *this* auth server's issuer — the redirect URI is
// where the upstream sends the user back to us, so it must live on our
// origin, not the upstream's.
//
// The issuer's path is preserved when defaulting: an issuer with a tenant
// prefix produces a redirect URI under that prefix, not at the host root.
// url.URL.ResolveReference would replace the path entirely because
// defaultUpstreamRedirectPath starts with "/", so we explicitly concatenate
// instead.
func resolveUpstreamRedirectURI(configured, localIssuer string) (string, error) {
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

	issuerURL, err := url.Parse(localIssuer)
	if err != nil {
		return "", fmt.Errorf("invalid issuer %q: %w", localIssuer, err)
	}
	resolved := &url.URL{
		Scheme: issuerURL.Scheme,
		Host:   issuerURL.Host,
		Path:   strings.TrimRight(issuerURL.Path, "/") + defaultUpstreamRedirectPath,
	}
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

// validateUpstreamEndpointURL enforces well-formedness and the
// HTTPS-except-loopback rule for an upstream-supplied OAuth endpoint URL.
//
// Used at every point where an endpoint URL enters the resolver from outside
// — operator-configured RegistrationEndpoint, or authorization_endpoint /
// token_endpoint copied out of an upstream's metadata document. The
// downstream oauthproto.validateRegistrationEndpoint enforces HTTPS for the
// registration URL too, but only after a bearer-token transport has already
// been constructed, so a local fail-fast check keeps the
// "no bearer-token transport for a non-HTTPS endpoint" invariant local.
//
// label is included in the error message ("registration_endpoint",
// "authorization_endpoint", "token_endpoint", …) so failures can be tied
// back to the specific field without an additional wrapper.
func validateUpstreamEndpointURL(rawURL, label string) error {
	if rawURL == "" {
		return fmt.Errorf("%s is required", label)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%s %q is not a valid URL: %w", label, rawURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%s %q missing scheme or host", label, rawURL)
	}
	if u.Scheme != "https" && !networking.IsLocalhost(u.Host) {
		return fmt.Errorf("%s %q must use https unless host is loopback (got scheme %q)",
			label, rawURL, u.Scheme)
	}
	return nil
}

// selectTokenEndpointAuthMethod returns the preferred token endpoint auth
// method given the server's advertised set, intersected with our preference
// order. When the server does not advertise any methods the caller's default
// of client_secret_basic is used (RFC 6749 §2.3.1 baseline) — unless
// publicClient is true, in which case the absence of advertised methods is
// not enough to safely register as a public client and the function returns
// an error (the S256 PKCE gate cannot be evaluated without
// code_challenge_methods_supported).
//
// PKCE coupling for "none": the public-client method "none" is selected only
// when the upstream also advertises S256 in code_challenge_methods_supported.
// RFC 7636 §4.2 / OAuth 2.1 require S256 PKCE for public clients; registering
// as none against an upstream that advertises only "plain" — or omits the
// field entirely — would be a compliance gap. When S256 is missing, "none"
// is skipped (the iteration continues to the next less-preferred method),
// and if no other method is mutually supported the function returns an error
// so the operator sees a clear failure at boot rather than a silent
// downgrade at runtime.
//
// publicClient: when true (CLI flow), the function returns "none" if and
// only if S256 PKCE is advertised. Any other outcome is an error — the
// caller has declared intent and the resolver must not silently switch to
// a confidential-client method.
func selectTokenEndpointAuthMethod(serverSupported, codeChallengeMethodsSupported []string, publicClient bool) (string, error) {
	pkceS256Advertised := slices.Contains(codeChallengeMethodsSupported, oauthproto.PKCEMethodS256)

	if publicClient {
		if !pkceS256Advertised {
			return "", fmt.Errorf(
				"public client requested but upstream does not advertise S256 in "+
					"code_challenge_methods_supported (got %v); refusing to register a "+
					"public client without S256 PKCE per RFC 7636 / OAuth 2.1",
				codeChallengeMethodsSupported)
		}
		// When serverSupported is non-empty, require that it includes "none"
		// so we don't claim a method the AS would reject.
		if len(serverSupported) > 0 {
			if !slices.Contains(serverSupported, "none") {
				return "", fmt.Errorf(
					"public client requested but upstream does not advertise "+
						"token_endpoint_auth_method=none (got %v); the CLI flow is a "+
						"public PKCE client and cannot register with a confidential method",
					serverSupported)
			}
		}
		return "none", nil
	}

	if len(serverSupported) == 0 {
		return "client_secret_basic", nil
	}

	supported := make(map[string]struct{}, len(serverSupported))
	for _, m := range serverSupported {
		supported[m] = struct{}{}
	}

	for _, m := range authMethodPreference {
		if _, ok := supported[m]; !ok {
			continue
		}
		if m == "none" && !pkceS256Advertised {
			// Public-client registration without S256 PKCE is non-compliant
			// per RFC 7636 / OAuth 2.1. Try the next less-preferred method.
			continue
		}
		return m, nil
	}
	if _, noneOnly := supported["none"]; noneOnly && !pkceS256Advertised {
		return "", fmt.Errorf(
			"upstream advertises only token_endpoint_auth_method=none but does not advertise "+
				"S256 in code_challenge_methods_supported (got %v); refusing to register a public "+
				"client without S256 PKCE per RFC 7636 / OAuth 2.1", codeChallengeMethodsSupported)
	}
	return "", fmt.Errorf(
		"no supported token_endpoint_auth_method in server advertisement %v; "+
			"client supports %v", serverSupported, authMethodPreference)
}

// bearerTokenTransport is an http.RoundTripper that adds
// Authorization: Bearer {token} to each outgoing request. Used to supply the
// RFC 7591 initial access token to oauthproto.RegisterClientDynamically
// without leaking the abstraction up into that package.
//
// The wrapping http.Client (see newDCRHTTPClient) refuses to follow HTTP
// redirects via CheckRedirect, so this transport is only ever invoked for
// the original registration request — never for a redirected request whose
// URL the upstream chose. That is what prevents this token from being
// forwarded to a foreign origin.
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

// errDCRRedirectRefused is returned when a DCR registration endpoint
// responds with a 30x. Net/http surfaces it via *url.Error so callers
// observe a clear failure mode instead of a confusing JSON decode error.
var errDCRRedirectRefused = errors.New(
	"dcr: registration endpoint returned a redirect; refusing to follow " +
		"to avoid forwarding the RFC 7591 initial access token to a foreign origin")

// newDCRHTTPClient returns the http.Client to pass to
// oauthproto.RegisterClientDynamically for a registration POST to
// registrationEndpoint. The client dials through the private-IP-guarded
// transport built by newGuardedDCRClient (CWE-918 SSRF protection) and always
// blocks HTTP redirects so that an upstream cannot use a 30x to coerce us into
// re-issuing the registration request (and any attached
// Authorization: Bearer header) against a different origin. RFC 7591 §3
// does not require redirect support, so refusing them is safe.
//
// When initialAccessToken is non-empty the client also wraps the guarded
// transport with a bearerTokenTransport that injects the Authorization header.
// The combination of the bearer transport plus the redirect block is what
// prevents the token-leak class of bug.
func newDCRHTTPClient(initialAccessToken, registrationEndpoint string, allowPrivateIPs bool) (*http.Client, error) {
	host, err := hostFromURL(registrationEndpoint)
	if err != nil {
		return nil, err
	}
	client, err := newGuardedDCRClient(host, allowPrivateIPs)
	if err != nil {
		return nil, err
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return errDCRRedirectRefused
	}

	if initialAccessToken == "" {
		return client, nil
	}

	next := client.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	client.Transport = &bearerTokenTransport{
		token: initialAccessToken,
		next:  next,
	}
	return client, nil
}

// newGuardedDCRClient builds the private-IP-guarded *http.Client used for both
// of the resolver's outbound calls — the discovery fetch and the registration
// POST. See networking.NewHostScopedClientBuilder for the CWE-918 guard policy
// this applies (allowPrivateIPs semantics, loopback exemption, HTTPS
// enforcement). Keep-alive is disabled so the dial-time check re-runs on
// every request rather than being bypassed by a pooled connection.
//
// The returned client has no CheckRedirect policy: each caller layers its own
// (the registration client refuses all redirects to protect the bearer token;
// the discovery client restricts them to the same host). The dial guard alone
// does not stop a redirect to a different public host, so the redirect policy
// is a required complement, not an optional one.
func newGuardedDCRClient(host string, allowPrivateIPs bool) (*http.Client, error) {
	return networking.NewHostScopedClientBuilder(host, allowPrivateIPs, false).
		WithDisableKeepAlives(true).
		Build()
}

// hostFromURL extracts the host[:port] component used to scope the guarded
// HTTP client. Every URL reaching this helper has already passed
// scheme-and-host validation at the resolver's entry points
// (validateUpstreamEndpointURL for the registration endpoint — both the
// caller-supplied case and the metadata-discovered case handled in
// endpointsFromMetadata — and FetchAuthorizationServerMetadataFromURL for the
// discovery URL; the synthesised-endpoint case derives its host from an
// upstream issuer that already passed the RFC 8414 §3.3 issuer-match check),
// so a parse failure or empty host here signals an internal inconsistency
// rather than untrusted input.
func hostFromURL(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("dcr: parse url for http client host: %w", err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("dcr: url missing host: %q", rawURL)
	}
	return u.Host, nil
}
