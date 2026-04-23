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
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
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

	// RedirectURI is the redirect URI presented to the authorization server
	// during registration. When the caller's run-config did not specify one,
	// this holds the defaulted value derived from the issuer + /oauth/callback
	// (via resolveUpstreamRedirectURI). Persisting it on the resolution lets
	// applyResolution write it back onto the run-config COPY so that
	// downstream consumers (buildPureOAuth2Config, upstream.OAuth2Config
	// validation) see a non-empty RedirectURI.
	RedirectURI string

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
	rc.ClientID = res.ClientID
	if rc.AuthorizationEndpoint == "" {
		rc.AuthorizationEndpoint = res.AuthorizationEndpoint
	}
	if rc.TokenEndpoint == "" {
		rc.TokenEndpoint = res.TokenEndpoint
	}
	// When the caller's run-config left RedirectURI empty, the resolver
	// defaulted it (issuer + /oauth/callback). Write the defaulted value
	// back so the downstream upstream.OAuth2Config has a non-empty
	// RedirectURI — otherwise authserver.Config validation rejects the
	// OAuth2 upstream with "redirect_uri is required".
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
	issuer string,
	cache DCRCredentialStore,
) (*DCRResolution, error) {
	if err := validateResolveInputs(rc, issuer, cache); err != nil {
		return nil, newDCRStepError(dcrStepValidate, issuer, "", err)
	}

	redirectURI, err := resolveUpstreamRedirectURI(rc.RedirectURI, issuer)
	if err != nil {
		return nil, newDCRStepError(dcrStepResolveRedirect, issuer, "",
			fmt.Errorf("resolve redirect uri: %w", err))
	}

	scopes := slices.Clone(rc.Scopes)
	key := DCRKey{
		Issuer:      issuer,
		RedirectURI: redirectURI,
		ScopesHash:  scopesHash(scopes),
	}

	// Cache lookup short-circuits before any network I/O.
	if cached, hit, err := lookupCachedResolution(ctx, cache, key, issuer, redirectURI); err != nil {
		return nil, newDCRStepError(dcrStepCacheRead, issuer, redirectURI, err)
	} else if hit {
		return cached, nil
	}

	// Endpoint resolution: discover metadata when configured, otherwise use
	// the caller-supplied RegistrationEndpoint directly.
	endpoints, err := resolveDCREndpoints(ctx, rc.DCRConfig, issuer)
	if err != nil {
		return nil, newDCRStepError(dcrStepMetadata, issuer, redirectURI, err)
	}
	applyExplicitEndpointOverrides(endpoints, rc)

	// Token-endpoint auth method: intersect server support with our
	// preference order; default to client_secret_basic if the server does
	// not advertise the field at all.
	authMethod, err := selectTokenEndpointAuthMethod(endpoints.tokenEndpointAuthMethodsSupported)
	if err != nil {
		return nil, newDCRStepError(dcrStepSelectAuthMethod, issuer, redirectURI, err)
	}

	registrationScopes := chooseRegistrationScopes(scopes, endpoints.scopesSupported, issuer)

	response, err := performRegistration(ctx, rc.DCRConfig, endpoints.registrationEndpoint,
		redirectURI, authMethod, registrationScopes)
	if err != nil {
		return nil, newDCRStepError(dcrStepRegister, issuer, redirectURI, err)
	}

	resolution := buildResolution(response, endpoints, authMethod, redirectURI)

	// Write to durable storage before updating caller state (per
	// .claude/rules/go-style.md "write to durable storage before in-memory").
	if err := cache.Put(ctx, key, resolution); err != nil {
		return nil, newDCRStepError(dcrStepCacheWrite, issuer, redirectURI,
			fmt.Errorf("cache put: %w", err))
	}

	//nolint:gosec // G706: client_id is public metadata per RFC 7591.
	slog.Debug("dcr: registered new client",
		"issuer", issuer,
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
//
// On a hit the resolution's age (now - CreatedAt) is computed and logged as
// dcr_age_days; if the age exceeds dcrStaleAgeThreshold, an additional
// slog.Warn is emitted with a remediation hint so operators can act on
// long-lived registrations that may need rotation or re-registration.
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

	age := time.Since(cached.CreatedAt)
	ageDays := int(age / (24 * time.Hour))

	//nolint:gosec // G706: client_id is public metadata per RFC 7591.
	slog.Debug("dcr: cache hit",
		"issuer", issuer,
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
			"issuer", issuer,
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
// recorded so downstream consumers see a definite value. redirectURI is the
// value passed to the registration endpoint (caller-supplied or defaulted
// via resolveUpstreamRedirectURI); it is persisted on the resolution so
// applyResolution can propagate a defaulted value back to the run-config.
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
