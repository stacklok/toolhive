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
	"regexp"
	"runtime/debug"
	"slices"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// dcrFlight coalesces concurrent resolveDCRCredentials calls that share the
// same DCRKey. Two goroutines hitting the resolver for the same upstream and
// scope set will both miss the cache, so without coalescing they would both
// call RegisterClientDynamically and the loser's registration would become
// orphaned at the upstream IdP — an operator-visible cleanup task and
// possibly a transient startup failure if the upstream rate-limits
// concurrent registrations. Followers wait for the leader's result and
// observe the same DCRResolution.
//
// Package-level rather than per-store because the deduplication concern is
// the resolver's, not the cache's: a future Redis-backed store would still
// want this in-process gate so a single replica does not double-register.
var dcrFlight singleflight.Group

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

	// RedirectURI is the redirect URI presented to the authorization server
	// during registration. When the caller's run-config did not specify one,
	// this holds the defaulted value derived from the issuer + /oauth/callback
	// (via resolveUpstreamRedirectURI). Persisting it on the resolution lets
	// applyResolution write it back onto the run-config COPY so that
	// downstream consumers (buildPureOAuth2Config, upstream.OAuth2Config
	// validation) see a non-empty RedirectURI.
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

// applyResolution copies resolved credentials and endpoints from res into rc
// and consumes rc.DCRConfig (sets it to nil), transitioning the run-config
// copy from "DCR-pending" (ClientID == "" && DCRConfig != nil) to "DCR-
// resolved" (ClientID populated && DCRConfig == nil).
//
// Callers must pass a COPY of the upstream run-config (per the
// copy-before-mutate rule in .claude/rules/go-style.md); applyResolution does
// not clone rc internally.
//
// Why DCRConfig is cleared: OAuth2UpstreamRunConfig.Validate enforces
// ClientID xor DCRConfig — a resolved copy that left DCRConfig set would
// fail the validator that runs downstream in buildPureOAuth2Config. The
// caller's *original* OAuth2Config is unaffected because
// buildUpstreamConfigs deep-copies before resolution; only the post-
// resolution copy is mutated here.
//
// ClientID, the endpoints, and RedirectURI are written only when rc leaves
// them empty — explicit caller configuration always wins. The conditional
// ClientID write is defence-in-depth against future call sites that bypass
// the resolver's validateResolveInputs precondition (which enforces
// ClientID == "" up front); an unconditional overwrite would silently
// clobber a pre-provisioned ClientID with no error.
//
// The defaulted RedirectURI write closes the loop on resolver-side defaulting:
// when the caller's run-config left RedirectURI empty, resolveUpstreamRedirectURI
// derived issuer + /oauth/callback and persisted it on the resolution; copying
// it back here means the downstream upstream.OAuth2Config has a non-empty
// RedirectURI, which authserver.Config validation requires.
//
// Note on ClientSecret: applyResolution does NOT write the resolved secret
// to rc because OAuth2UpstreamRunConfig models secrets as file-or-env
// references only. To propagate the DCR-resolved secret into the final
// upstream.OAuth2Config, callers must pair this call with
// applyResolutionToOAuth2Config once the config has been built. Keeping the
// two helpers side-by-side localises the DCR-specific application logic.
func applyResolution(rc *authserver.OAuth2UpstreamRunConfig, res *DCRResolution) {
	if rc == nil || res == nil {
		return
	}
	if rc.ClientID == "" {
		rc.ClientID = res.ClientID
	}
	rc.DCRConfig = nil
	if rc.AuthorizationEndpoint == "" {
		rc.AuthorizationEndpoint = res.AuthorizationEndpoint
	}
	if rc.TokenEndpoint == "" {
		rc.TokenEndpoint = res.TokenEndpoint
	}
	if rc.RedirectURI == "" {
		rc.RedirectURI = res.RedirectURI
	}
}

// applyResolutionToOAuth2Config overlays the DCR-resolved ClientSecret onto
// a built *upstream.OAuth2Config. This is the companion to applyResolution:
// where that function writes fields representable in the file-or-env run-
// config model, this one writes the inline-only ClientSecret directly on
// the runtime config.
//
// The split exists because buildPureOAuth2Config intentionally retains a
// narrow file-or-env contract (no DCR awareness) and because OAuth2's
// ClientSecret on the run-config is modelled as a reference rather than an
// inline string. Any future output path from OAuth2UpstreamRunConfig to
// upstream.OAuth2Config must call both helpers to get a fully-resolved
// DCR client — exercised by TestBuildUpstreamConfigs_DCR.
func applyResolutionToOAuth2Config(cfg *upstream.OAuth2Config, res *DCRResolution) {
	if cfg == nil || res == nil {
		return
	}
	cfg.ClientSecret = res.ClientSecret
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

// Step identifiers for structured error logs emitted by the caller of
// resolveDCRCredentials. These values flow through the "step" attribute so
// operators can narrow failures to a specific phase without parsing error
// messages. They are reported only at the boundary log — see
// DCRStepError — so a single failure produces a single slog.Error record.
const (
	dcrStepValidate         = "validate"
	dcrStepResolveRedirect  = "resolve_redirect_uri"
	dcrStepCacheRead        = "cache_read"
	dcrStepMetadata         = "metadata_discovery"
	dcrStepSelectAuthMethod = "select_auth_method"
	dcrStepRegister         = "dcr_call"
	dcrStepCacheWrite       = "cache_write"
)

// DCRStepError annotates a resolver error with the phase it was produced
// in. The boundary caller (buildUpstreamConfigs) emits the single
// slog.Error record for the failure; individual error branches inside
// resolveDCRCredentials do not log so that each failure surfaces exactly
// once in the combined log stream.
//
// RedirectURI is included when known so that operators can correlate the
// failure with a specific upstream registration without parsing the
// wrapped error string. A zero-value DCRStepError is invalid; construct
// via newDCRStepError or the resolver's internal helpers.
type DCRStepError struct {
	Step        string
	Issuer      string
	RedirectURI string
	Err         error
}

// Error implements error. The "step" tag mirrors the structured-log
// attribute so command-line log scrapers see the same phase identifier.
func (e *DCRStepError) Error() string {
	return fmt.Sprintf("dcr: %s: %s", e.Step, e.Err.Error())
}

// Unwrap lets errors.Is / errors.As reach the wrapped cause.
func (e *DCRStepError) Unwrap() error { return e.Err }

// newDCRStepError builds a DCRStepError. It never returns nil for a
// non-nil cause.
func newDCRStepError(step, issuer, redirectURI string, err error) *DCRStepError {
	return &DCRStepError{
		Step:        step,
		Issuer:      issuer,
		RedirectURI: redirectURI,
		Err:         err,
	}
}

// resolveDCRCredentials performs Dynamic Client Registration for rc against
// the upstream authorization server identified by rc.DCRConfig, caching the
// resulting credentials in cache. On cache hit the resolver returns
// immediately without any network I/O.
//
// rc must have ClientID == "" and DCRConfig != nil — the caller is expected
// to have validated this via OAuth2UpstreamRunConfig.Validate.
//
// localIssuer is *this* auth server's issuer identifier, NOT the upstream's.
// It is used to key the cache and to default the redirect URI to
// {localIssuer}/oauth/callback when rc.RedirectURI is empty. The upstream's
// issuer is recovered separately from rc.DCRConfig.DiscoveryURL inside the
// resolver and is used solely for RFC 8414 §3.3 metadata verification.
// Passing the upstream's issuer here would produce a wrong-origin default
// redirect and a cache key that does not identify the auth-server context.
//
// The caller is responsible for applying the returned resolution onto a COPY
// of rc via applyResolution (per the copy-before-mutate rule). This function
// neither mutates rc nor the cache on failure.
//
// Observability: this function never calls slog.Error directly — all
// failures are annotated with a *DCRStepError and returned to the caller,
// which is expected to emit the boundary Error record. This avoids the
// double-logging pattern where both the resolver and the outer frame
// report the same failure. Cache-hit Debug / stale-Warn logs and the
// successful-registration Debug log are emitted here because they have no
// outer-frame equivalent. No secret values (client_secret,
// registration_access_token, initial_access_token) are ever logged — only
// public metadata such as client_id and redirect_uri.
func resolveDCRCredentials(
	ctx context.Context,
	rc *authserver.OAuth2UpstreamRunConfig,
	localIssuer string,
	cache DCRCredentialStore,
) (*DCRResolution, error) {
	if err := validateResolveInputs(rc, localIssuer, cache); err != nil {
		return nil, newDCRStepError(dcrStepValidate, localIssuer, "", err)
	}

	redirectURI, err := resolveUpstreamRedirectURI(rc.RedirectURI, localIssuer)
	if err != nil {
		return nil, newDCRStepError(dcrStepResolveRedirect, localIssuer, "",
			fmt.Errorf("resolve redirect uri: %w", err))
	}

	scopes := slices.Clone(rc.Scopes)
	key := DCRKey{
		Issuer:      localIssuer,
		RedirectURI: redirectURI,
		ScopesHash:  scopesHash(scopes),
	}

	// Cache lookup short-circuits before any network I/O.
	if cached, hit, err := lookupCachedResolution(ctx, cache, key, localIssuer, redirectURI); err != nil {
		return nil, newDCRStepError(dcrStepCacheRead, localIssuer, redirectURI, err)
	} else if hit {
		return cached, nil
	}

	// Coalesce concurrent registrations for the same DCRKey — see dcrFlight
	// doc comment. The leader runs the registerOnce closure; followers
	// receive the leader's *DCRResolution result. The flight key embeds the
	// DCRKey fields with a separator that cannot appear in any of them
	// (newline is not valid in OAuth scope tokens, URLs, or hex digests).
	//
	// A defer/recover inside the closure converts a panic in registerAndCache
	// (or anything it calls) into a normal error. Without this, singleflight
	// re-panics the leader's panic in every follower — N concurrent callers
	// for the same DCRKey would all crash with the same value. The panic is
	// still surfaced: it is logged at Error with a stack trace, and the
	// returned error wraps the recovered value so callers can react to it as
	// a normal failure. The wrapped error is annotated with the dcrStepRegister
	// step so LogDCRStepError surfaces it in the same step taxonomy as the
	// other registration failures.
	flightKey := key.Issuer + "\n" + key.RedirectURI + "\n" + key.ScopesHash
	resolutionAny, err, _ := dcrFlight.Do(flightKey, func() (res any, err error) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("dcr: registration panicked",
					"panic", fmt.Sprintf("%v", r),
					"stack", string(debug.Stack()),
				)
				err = newDCRStepError(dcrStepRegister, localIssuer, redirectURI,
					fmt.Errorf("registration panicked: %v", r))
				res = nil
			}
		}()
		return registerAndCache(ctx, rc, localIssuer, redirectURI, scopes, key, cache)
	})
	if err != nil {
		return nil, err
	}
	return resolutionAny.(*DCRResolution), nil
}

// registerAndCache is the leader-only body of resolveDCRCredentials wrapped
// by the singleflight. It rechecks the cache before any network I/O so
// followers that arrive after the leader's Put returns immediately see the
// fresh entry on a subsequent call. Endpoint resolution, registration, and
// the durable Put live here.
func registerAndCache(
	ctx context.Context,
	rc *authserver.OAuth2UpstreamRunConfig,
	localIssuer, redirectURI string,
	scopes []string,
	key DCRKey,
	cache DCRCredentialStore,
) (*DCRResolution, error) {
	// Recheck cache: another flight that just finished may have populated
	// it between our initial lookup and our singleflight entry.
	if cached, hit, err := lookupCachedResolution(ctx, cache, key, localIssuer, redirectURI); err != nil {
		return nil, newDCRStepError(dcrStepCacheRead, localIssuer, redirectURI, err)
	} else if hit {
		return cached, nil
	}

	// Endpoint resolution: discover metadata when configured, otherwise use
	// the caller-supplied RegistrationEndpoint directly. The upstream's
	// expected issuer is recovered from cfg.DiscoveryURL inside the helper.
	// localIssuer here is *this* auth server's issuer — correct for cache
	// keying and redirect URI defaulting, but it must not be used for
	// RFC 8414 §3.3 metadata verification (which is the upstream's concern).
	endpoints, err := resolveDCREndpoints(ctx, rc.DCRConfig)
	if err != nil {
		return nil, newDCRStepError(dcrStepMetadata, localIssuer, redirectURI, err)
	}
	applyExplicitEndpointOverrides(endpoints, rc)

	// Token-endpoint auth method: intersect server support with our
	// preference order; default to client_secret_basic if the server does
	// not advertise the field at all.
	authMethod, err := selectTokenEndpointAuthMethod(
		endpoints.tokenEndpointAuthMethodsSupported,
		endpoints.codeChallengeMethodsSupported,
	)
	if err != nil {
		return nil, newDCRStepError(dcrStepSelectAuthMethod, localIssuer, redirectURI, err)
	}

	registrationScopes := chooseRegistrationScopes(scopes, endpoints.scopesSupported, localIssuer)

	response, err := performRegistration(ctx, rc.DCRConfig, endpoints.registrationEndpoint,
		redirectURI, authMethod, registrationScopes)
	if err != nil {
		return nil, newDCRStepError(dcrStepRegister, localIssuer, redirectURI, err)
	}

	resolution := buildResolution(response, endpoints, authMethod, redirectURI)

	// Write to durable storage before updating caller state (per
	// .claude/rules/go-style.md "write to durable storage before in-memory").
	if err := cache.Put(ctx, key, resolution); err != nil {
		return nil, newDCRStepError(dcrStepCacheWrite, localIssuer, redirectURI,
			fmt.Errorf("cache put: %w", err))
	}

	//nolint:gosec // G706: client_id is public metadata per RFC 7591.
	slog.Debug("dcr: registered new client",
		"local_issuer", localIssuer,
		"redirect_uri", redirectURI,
		"client_id", resolution.ClientID,
	)
	return resolution, nil
}

// LogDCRStepError emits the single boundary slog.Error record for a DCR
// resolver failure, carrying the step / issuer / redirect_uri attributes
// extracted from err. If err is not a *DCRStepError, it is logged with a
// generic "unknown" step — resolveDCRCredentials always wraps its errors,
// so this branch indicates a programming error in a future caller rather
// than a runtime condition.
//
// Every wrapped error is passed through sanitizeErrorForLog to strip URL
// query parameters that could plausibly contain sensitive tokens (defense
// in depth — the current DCR flow sends the initial access token as an
// Authorization header, not a query parameter, but nothing in the type
// system prevents a future refactor from doing otherwise).
func LogDCRStepError(upstreamName string, err error) {
	var stepErr *DCRStepError
	if !errors.As(err, &stepErr) {
		slog.Error("dcr: resolve failed",
			"upstream", upstreamName,
			"step", "unknown",
			"error", sanitizeErrorForLog(err),
		)
		return
	}

	attrs := []any{
		"upstream", upstreamName,
		"step", stepErr.Step,
		"issuer", stepErr.Issuer,
		"error", sanitizeErrorForLog(stepErr.Err),
	}
	if stepErr.RedirectURI != "" {
		attrs = append(attrs, "redirect_uri", stepErr.RedirectURI)
	}
	slog.Error("dcr: resolve failed", attrs...)
}

// sanitizeErrorForLog strips query strings from any URLs embedded in err's
// message. The Go HTTP client, url.Error, and other net/* wrappers embed
// the full request URL — including query parameters — in their error
// strings. Query parameters rarely carry secrets in our DCR flow (the
// initial access token is sent as an Authorization header), but a future
// change could silently introduce a token in a query parameter; stripping
// query strings here is defense in depth that protects the log regardless.
//
// Only the query component of URL-shaped substrings is replaced; scheme,
// host, and path are preserved so operators retain enough context to
// correlate with upstream server logs. Trailing sentence punctuation
// adjacent to the URL (e.g. the comma in "reaching URL?q=1, retrying")
// is preserved — see trimURLTrailingPunctuation for the list of
// characters considered terminators.
//
// Scope: the regex only matches http:// and https:// schemes. Other
// schemes (file://, raw host:port) are not sanitised; the current DCR
// flow never embeds those in errors, and broadening the match risks
// false positives on unrelated text.
func sanitizeErrorForLog(err error) string {
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
		if parseErr != nil || u.RawQuery == "" {
			return match
		}
		u.RawQuery = ""
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
	const terminators = ".,;:!?)]}>"
	i := len(s)
	for i > 0 && strings.ContainsRune(terminators, rune(s[i-1])) {
		i--
	}
	return s[:i], s[i:]
}

// queryStrippingPattern matches URL-shaped substrings inside an error
// message — sufficient to reach the url.Parse path in sanitizeErrorForLog
// and let it decide whether a query component exists to strip. The regexp
// is intentionally narrow (http/https schemes only) to avoid false
// positives. Trailing sentence punctuation that the character class
// happens to include (e.g. '.', ',', ')') is stripped by
// trimURLTrailingPunctuation before the match is parsed.
var queryStrippingPattern = regexp.MustCompile(`https?://[^\s"']+`)

// -----------------------------------------------------------------------------
// Private helpers
// -----------------------------------------------------------------------------

// validateResolveInputs performs the defensive re-check of resolver
// preconditions. Validate() enforces most of these at config-load time, but
// resolveDCRCredentials is an entry point that programmatic callers can
// reach with partially-constructed run-configs.
func validateResolveInputs(
	rc *authserver.OAuth2UpstreamRunConfig,
	localIssuer string,
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
	if localIssuer == "" {
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
	cache DCRCredentialStore,
	key DCRKey,
	localIssuer, redirectURI string,
) (*DCRResolution, bool, error) {
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
// recorded so downstream consumers see a definite value. redirectURI is the
// value passed to the registration endpoint (caller-supplied or defaulted
// via resolveUpstreamRedirectURI); it is persisted on the resolution so
// applyResolution can propagate a defaulted value back to the run-config.
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
// resolution. The separation from DCRResolution lets the resolver reason
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
//     configured (bypassing the well-known path fallback). RFC 8414 §3.3
//     requires the metadata's "issuer" field to match the authorization
//     server's issuer identifier; that identifier is the upstream's, not
//     this auth server's, so it is recovered from the discovery URL via
//     deriveExpectedIssuerFromDiscoveryURL rather than reusing the
//     caller-supplied issuer (which names this auth server and is used
//     elsewhere in resolveDCRCredentials for redirect URI defaulting and
//     cache keying).
//  3. Neither set — defensive; Validate() rejects this configuration, but
//     as a programmatic entry point the resolver returns an error rather
//     than falling back to an unexpected strategy.
//
// When metadata is returned but omits registration_endpoint, the resolver
// synthesises {origin}/register — a convention used by nanobot and Hydra
// for providers that ship DCR without advertising it in discovery. Origin
// is taken from the upstream issuer, not this auth server's issuer, so the
// synthesised endpoint lands at the upstream.
func resolveDCREndpoints(
	ctx context.Context,
	cfg *authserver.DCRUpstreamConfig,
) (*dcrEndpoints, error) {
	if cfg.RegistrationEndpoint != "" {
		// Validate locally so a non-HTTPS or malformed URL fails before
		// performRegistration constructs a bearer-token transport for it.
		if err := validateUpstreamEndpointURL(cfg.RegistrationEndpoint, "registration_endpoint"); err != nil {
			return nil, fmt.Errorf("dcr: %w", err)
		}
		return &dcrEndpoints{
			registrationEndpoint: cfg.RegistrationEndpoint,
		}, nil
	}

	if cfg.DiscoveryURL == "" {
		return nil, fmt.Errorf(
			"dcr: dcr_config must set either discovery_url or registration_endpoint")
	}

	upstreamIssuer, err := deriveExpectedIssuerFromDiscoveryURL(cfg.DiscoveryURL)
	if err != nil {
		return nil, err
	}

	metadata, err := oauthproto.FetchAuthorizationServerMetadataFromURL(ctx, cfg.DiscoveryURL, upstreamIssuer, nil)
	return endpointsFromMetadata(metadata, err, upstreamIssuer)
}

// deriveExpectedIssuerFromDiscoveryURL recovers the issuer identifier the
// upstream is expected to claim in its RFC 8414 / OIDC Discovery document,
// given an operator-configured DiscoveryURL.
//
// Two recognised conventions:
//
//  1. Well-known suffix: the URL ends with /.well-known/oauth-authorization-server
//     or /.well-known/openid-configuration. The suffix is stripped to recover
//     the issuer; this covers single-tenant providers (e.g.
//     https://mcp.atlassian.com/.well-known/oauth-authorization-server →
//     https://mcp.atlassian.com) and the issuer-suffix multi-tenant style
//     (e.g. https://idp.example.com/tenants/acme/.well-known/openid-configuration
//     → https://idp.example.com/tenants/acme).
//  2. Non-well-known path: the URL points at a custom metadata endpoint that
//     does not end in either suffix. Origin (scheme://host) is used as a
//     best-effort fallback; this matches the common shape where the upstream
//     issuer is the host root.
//
// RFC 8414 §3.1's path-aware form (well-known path inserted between host and
// tenant path, e.g. https://example.com/.well-known/oauth-authorization-server/tenant)
// is not auto-detected here — operators on that pattern can switch to
// dcr_config.registration_endpoint to bypass discovery.
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
	case strings.HasSuffix(u.Path, oauthSuffix):
		u.Path = strings.TrimSuffix(u.Path, oauthSuffix)
	case strings.HasSuffix(u.Path, oidcSuffix):
		u.Path = strings.TrimSuffix(u.Path, oidcSuffix)
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
func endpointsFromMetadata(
	metadata *oauthproto.AuthorizationServerMetadata,
	fetchErr error,
	upstreamIssuer string,
) (*dcrEndpoints, error) {
	if fetchErr != nil && !errors.Is(fetchErr, oauthproto.ErrRegistrationEndpointMissing) {
		return nil, fmt.Errorf("discover authorization server metadata: %w", fetchErr)
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
// of client_secret_basic is used (RFC 6749 §2.3.1 baseline).
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
func selectTokenEndpointAuthMethod(serverSupported, codeChallengeMethodsSupported []string) (string, error) {
	if len(serverSupported) == 0 {
		return "client_secret_basic", nil
	}

	supported := make(map[string]struct{}, len(serverSupported))
	for _, m := range serverSupported {
		supported[m] = struct{}{}
	}

	pkceS256Advertised := slices.Contains(codeChallengeMethodsSupported, oauthproto.PKCEMethodS256)

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
// oauthproto.RegisterClientDynamically. The client always blocks HTTP
// redirects so that an upstream cannot use a 30x to coerce us into
// re-issuing the registration request (and any attached
// Authorization: Bearer header) against a different origin. RFC 7591 §3
// does not require redirect support, so refusing them is safe.
//
// When initialAccessToken is non-empty the client also wraps the canonical
// DCR client's transport with a bearerTokenTransport that injects the
// Authorization header. The combination of the bearer transport plus the
// redirect block is what prevents the token-leak class of bug.
//
// The timeout policy is sourced from oauthproto.NewDefaultDCRClient so
// future tightening of those bounds propagates automatically.
func newDCRHTTPClient(initialAccessToken string) *http.Client {
	client := oauthproto.NewDefaultDCRClient()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return errDCRRedirectRefused
	}

	if initialAccessToken == "" {
		return client
	}

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
