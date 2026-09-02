// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	celgo "cel.dev/cel-go/cel"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/jwx/v3/jwk"

	"github.com/stacklok/toolhive-core/cel"
	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	// httpTimeout is the timeout for HTTP requests to external OIDC endpoints.
	httpTimeout = 10 * time.Second

	// maxResponseBodySize is the maximum size of HTTP response bodies read
	// from external OIDC discovery documents AND JWKS fetches (1 MiB). This
	// prevents resource exhaustion from unexpectedly large responses. The
	// discovery read enforces it directly via io.LimitReader; the JWKS read
	// goes through jwx instead, so it is enforced by wrapping every issuer's
	// *http.Client transport with limitedBodyTransport — see where that
	// client is built in NewMultiIssuerTokenValidator.
	maxResponseBodySize = 1 << 20

	// maxJWKSKeys caps the number of keys accepted from an external JWKS to
	// prevent CPU amplification from a hostile endpoint serving many keys.
	maxJWKSKeys = 100

	// minKidRefreshInterval bounds how often refreshOnUnknownKid forces an
	// issuer's jwk.Cache to fetch its JWKS ahead of jwx's own background
	// refresh schedule. verifySignature's kidMatched check runs before the
	// subject token's signature is trusted, so without this floor a client
	// presenting a syntactically valid JWT that merely names a made-up kid
	// could force a fresh fetch to the external IdP on every attempt.
	minKidRefreshInterval = 30 * time.Second

	// jwksFetchFailureBackoff bounds how often ensureRegistered retries a
	// JWKS fetch for an issuer that has never yet succeeded. Key resolution
	// runs before the subject token's signature is checked, so without this
	// an authenticated client holding the token-exchange grant could force a
	// real outbound fetch to a broken external IdP on every single request.
	jwksFetchFailureBackoff = 30 * time.Second

	// jwksRefreshInterval is the fixed interval at which each issuer's
	// jwk.Cache re-fetches its JWKS in the background. It is passed to
	// Register via jwk.WithConstantInterval, which makes the resource
	// ignore the response's Cache-Control/Expires headers entirely rather
	// than merely bounding them with WithMaxInterval — deliberately:
	// absent this override, httprc derives the interval from those
	// headers, clamped to [DefaultMinInterval, DefaultMaxInterval] =
	// [15m, 30 days], so a hostile or misconfigured issuer could otherwise
	// extend our own key-retention window up to a month simply by setting
	// a long max-age.
	jwksRefreshInterval = 5 * time.Minute

	// defaultActorClaim is the claim read to identify the client that
	// requested an external subject token, when a TrustedIssuer does not
	// configure ActorClaim. This matches Microsoft Entra v2 and many other
	// OIDC providers' convention for the authorized-party claim.
	//
	// "client_id" is the normative claim: RFC 8693 §4.3 names it as the
	// party a token was issued to, and RFC 9068 §2.2 makes it a REQUIRED
	// access-token claim. "azp" is the default here only because Entra,
	// Okta, and other major providers omit "client_id" from their access
	// tokens in practice — "azp" itself appears nowhere in RFC 9068; it is
	// an OIDC Core claim defined for ID tokens, not access tokens.
	defaultActorClaim = "azp"

	// externalClockSkewLeeway widens "nbf"/"iat"/"exp" acceptance for external
	// subject tokens to tolerate clock skew between the external IdP and
	// ToolHive. validateExternalToken independently rejects an expired
	// subject token by comparing its exp against time.Now() with zero
	// tolerance, so this leeway only protects against spurious
	// not-yet-valid/issued-in-the-future rejections — it never causes an
	// expired token to be accepted.
	externalClockSkewLeeway = 60 * time.Second
)

// actorClaimsNotInExtra are claims assignClaim drops or reroutes, so they
// never reach ValidatedClaims.Extra. Only "client_id" has an explicit
// fallback in resolveAllowedActor; configuring ActorClaim to any of these
// would make every external token look like it's missing the claim.
var actorClaimsNotInExtra = []string{
	"sub", "iss", "aud", "exp", "iat", "nbf", "jti", "name", "email", "scope", "scp", "may_act",
}

// Compile-time check that MultiIssuerTokenValidator implements SubjectTokenValidator.
var _ SubjectTokenValidator = (*MultiIssuerTokenValidator)(nil)

// Compile-time check that MultiIssuerTokenValidator implements JWTBearerAssertionValidator.
var _ JWTBearerAssertionValidator = (*MultiIssuerTokenValidator)(nil)

// JWTBearerSubjectBinding restricts a JWT-bearer assertion subject to the
// resources for which it may obtain an access token.
type JWTBearerSubjectBinding struct {
	Subject          string   `json:"subject" yaml:"subject"`
	AllowedResources []string `json:"allowed_resources" yaml:"allowed_resources"`
}

// JWTBearerGrantPolicy enables RFC 7523 JWT-bearer assertions for an issuer.
// MaxAssertionAge uses Go's duration syntax in serialized RunConfig.
type JWTBearerGrantPolicy struct {
	MaxAssertionAge string                    `json:"max_assertion_age" yaml:"max_assertion_age"`
	SubjectBindings []JWTBearerSubjectBinding `json:"subject_bindings" yaml:"subject_bindings"`
	// AcceptedAudiences is the set of "this AS" identity strings an
	// assertion's "aud" claim must intersect — e.g. to support migrating
	// this server's issuer/token-endpoint URL, or exposing it under more
	// than one valid name. Each value uniquely identifies this
	// authorization server for this grant; it is NOT a resource/API
	// identifier — a bare resource audience is deliberately not accepted
	// here, that would let any RFC 8707 resource-scoped token satisfy the
	// grant instead of only tokens minted for this AS. Defaults to
	// [tokenEndpoint] when empty, preserving prior exact-match behavior.
	AcceptedAudiences []string `json:"accepted_audiences,omitempty" yaml:"accepted_audiences,omitempty"`

	maxAssertionAge time.Duration
}

// TrustedIssuer configures an external OIDC issuer whose tokens are accepted
// as RFC 8693 subject tokens or RFC 7523 JWT-bearer assertions.
//
// This type is reused verbatim as the wire schema for
// authserver.RunConfig.TrustedIssuers (deliberately, to avoid a parallel
// type that drifts — see the go-style rule against that). Its JSON/YAML
// tags are therefore part of the serialized RunConfig, which is reflected
// into docs/server/swagger.*; adding, renaming, or retagging a field here is
// a schema change, not a purely internal one.
type TrustedIssuer struct {
	// IssuerURL is the expected "iss" claim value (exact match).
	IssuerURL string `json:"issuer_url" yaml:"issuer_url"`
	// ExpectedAudience is the expected "aud" claim value that must appear
	// in an RFC 8693 subject token's audience list (a resource/API identifier,
	// not a client ID — required for delegation unless JWTBearerGrant is
	// configured; see looksLikeResourceIdentifier). RFC 7523 assertions use
	// the token endpoint as their audience instead.
	// See docs/arch/17-token-exchange-delegation.md ("ID/access-token
	// discrimination") for why and its limits.
	ExpectedAudience string `json:"expected_audience" yaml:"expected_audience"`
	// JWKSURL is the URL to fetch the issuer's JSON Web Key Set from.
	// If empty, it is resolved via OIDC discovery at {IssuerURL}/.well-known/openid-configuration.
	JWKSURL string `json:"jwks_url,omitempty" yaml:"jwks_url,omitempty"`
	// InsecureAllowHTTP permits plain-HTTP OIDC discovery and JWKS fetches
	// for THIS issuer only. Development and testing only — never set in
	// production. Does not relax the private-IP guard; see AllowPrivateIPs.
	// Deliberately per-issuer: this server's own InsecureAllowHTTP must not
	// silently permit plaintext discovery for every trusted external issuer
	// too — a network attacker who can intercept that traffic could
	// substitute a JWKS and forge subject tokens for that issuer's
	// namespace.
	InsecureAllowHTTP bool `json:"insecure_allow_http,omitempty" yaml:"insecure_allow_http,omitempty"`
	// AllowPrivateIPs permits OIDC discovery and JWKS fetches for THIS
	// issuer to resolve to a private or loopback address. Use only when the
	// issuer is hosted inside the same cluster and has no public endpoint.
	AllowPrivateIPs bool `json:"allow_private_ips,omitempty" yaml:"allow_private_ips,omitempty"`
	// CAFilePath is the path to a PEM CA bundle added to the system roots when
	// fetching this issuer's OIDC discovery document and JWKS. Trust is additive
	// and scoped to this issuer: the public roots still apply, and no other
	// issuer's client is affected.
	CAFilePath string `json:"ca_file_path,omitempty" yaml:"ca_file_path,omitempty"`
	// ActorClaim names the claim identifying the client that requested the
	// subject token from THIS EXTERNAL ISSUER (used by AllowedActors below).
	// Values are in the external issuer's namespace, NOT ToolHive client
	// IDs. Defaults to "azp"; use "appid" for Microsoft Entra v1, "cid" for
	// Okta. The special value "client_id" reads ValidatedClaims.ClientID
	// instead of Extra (assignClaim routes it to that field) — it is still
	// the external token's client_id claim, not a ToolHive one.
	ActorClaim string `json:"actor_claim,omitempty" yaml:"actor_claim,omitempty"`
	// AllowedActors is the allowlist of ActorClaim values authorized to
	// exchange a subject token from this issuer when it carries no
	// "may_act" claim. ActorMatcher can additionally authorize a token by
	// matching its complete verified claims map; either signal is sufficient.
	// When both are empty, only may_act-bearing tokens are accepted, and only
	// if AllowMayAct is also true for this issuer. By itself names no
	// ToolHive client — see AllowedDelegateClients and
	// docs/arch/17-token-exchange-delegation.md ("Accepted limitations" #1).
	AllowedActors []string `json:"allowed_actors,omitempty" yaml:"allowed_actors,omitempty"`
	// ActorMatcher is an admin-authored CEL expression evaluated against the
	// complete signature-verified JWT claims map as "claims". A true result
	// authorizes delegation alongside AllowedActors; a syntax or type error
	// fails configuration validation. An expression that compiles but does
	// not return bool is NOT caught at that point, though — it compiles
	// successfully and is only rejected the first time it is evaluated
	// against a real token, denying that token (and every one after it, since
	// the expression will never return bool). Any other runtime evaluation
	// error denies the token the same way.
	ActorMatcher string `json:"actor_matcher,omitempty" yaml:"actor_matcher,omitempty"`
	// AllowedDelegateClients restricts which ToolHive client IDs may
	// exchange a subject token from this issuer, for BOTH consent paths.
	// Required (validateTrustedIssuer rejects empty/absent); "*" permits
	// any confidential client holding the grant. See
	// docs/arch/17-token-exchange-delegation.md ("Accepted limitations" #1).
	//nolint:lll // field tags require full JSON+YAML names
	AllowedDelegateClients []string `json:"allowed_delegate_clients,omitempty" yaml:"allowed_delegate_clients,omitempty"`
	// AllowMayAct permits this external issuer's may_act claim to authorize
	// delegation. It defaults to false; external issuers must be opted in
	// explicitly because may_act bypasses AllowedActors and ActorMatcher. It
	// does not affect self-issued subject tokens. When enabled,
	// AllowedDelegateClients must name specific ToolHive clients rather than
	// use the wildcard.
	AllowMayAct bool `json:"allow_may_act,omitempty" yaml:"allow_may_act,omitempty"`
	// JWTBearerGrant optionally enables the plain RFC 7523 JWT-bearer grant.
	// It accepts assertions from this issuer without client authentication and
	// limits their maximum age, subjects, and RFC 8707 resources. It is
	// independent from RFC 8693 delegation policy.
	JWTBearerGrant *JWTBearerGrantPolicy `json:"jwt_bearer_grant,omitempty" yaml:"jwt_bearer_grant,omitempty"`
}

// MultiIssuerTokenValidator validates subject tokens from the authorization
// server itself or from configured external OIDC issuers, delegating
// self-issued tokens to SelfIssuedTokenValidator and resolving external
// issuers' JWKS (via OIDC discovery if needed) to verify signature and
// claims.
//
// A valid signature and audience alone would authorize ToolHive as a
// resource, not any particular client, as a delegate — a confused-deputy
// risk (CWE-863). validateExternalToken therefore requires a "may_act"
// claim or authorization by AllowedActors or ActorMatcher before returning
// successfully. See docs/arch/17-token-exchange-delegation.md for the full
// consent-signal precedence and trust model.
type MultiIssuerTokenValidator struct {
	selfIssuer    string
	selfValidator *SelfIssuedTokenValidator
	issuers       map[string]*externalIssuerConfig

	// cancel tears down the validator-scoped context that every per-issuer
	// jwksCache's background worker pool is rooted in. Close calls it after
	// shutting each cache down individually; see Close. It is always set for a
	// validator returned by NewMultiIssuerTokenValidator, even one with no
	// external issuers.
	cancel context.CancelFunc
}

// externalIssuerConfig holds the configuration and cached state for an external
// OIDC issuer. The embedded TrustedIssuer is treated as immutable after
// construction: resolveActorAuthorization reads TrustedIssuer.AllowedActors
// and actorMatcher (and discoverJWKSURL/fetchJWKS read httpClient) on every
// validation without holding mu, since mu protects JWKS state only. Mutating
// TrustedIssuer fields in place after NewMultiIssuerTokenValidator returns is
// a data race.
type externalIssuerConfig struct {
	TrustedIssuer

	// actorMatcher is compiled once from TrustedIssuer.ActorMatcher during
	// construction and is immutable thereafter.
	actorMatcher *cel.CompiledExpression

	// httpClient is dedicated to this issuer, built once at construction
	// time from its own InsecureAllowHTTP/AllowPrivateIPs. A single
	// validator-wide client couldn't enforce per-issuer SSRF/transport
	// policy: http.Client.CheckRedirect and Transport.DialContext have no
	// way to know which issuer's fetch they are guarding.
	httpClient *http.Client

	// jwksCache is this issuer's own jwk.Cache, registered with httpClient
	// above via jwk.WithHTTPClient (see registerOrRefresh). A cache per
	// issuer, rather than one shared across every configured issuer, is what
	// makes two issuers resolving to the same jwks_url (e.g. two Microsoft
	// Entra v1 tenants, which share one tenant-independent JWKS endpoint) a
	// non-event: httprc keys a cached resource by URL alone and only honors
	// jwk.WithHTTPClient on a URL's first Register call, so a shared cache
	// would have the second such issuer silently inherit the first one's
	// *http.Client — defeating InsecureAllowHTTP/AllowPrivateIPs's per-issuer
	// guarantee for it. Splitting the cache per issuer makes that collision
	// unrepresentable instead of guarding against it.
	jwksCache *jwk.Cache

	mu sync.Mutex
	// jwksURL is resolved from OIDC discovery, or copied from
	// TrustedIssuer.JWKSURL when hand-configured. Once set, it is never
	// cleared: unlike the JWKS document itself (which jwksCache keeps fresh
	// on its own schedule — see ensureRegistered), a change to
	// the *endpoint URL* an issuer serves its keys from requires a process
	// restart to pick up. Neither Microsoft Entra nor Okta documents rotating
	// that URL, only the keys served at it, and every other TrustedIssuer
	// field already requires a restart to take effect, so this is
	// consistent with the rest of this config's lifecycle.
	jwksURL string
	// fetched is true once at least one JWKS fetch has succeeded for this
	// issuer since process start. Until then, ensureRegistered forces a
	// synchronous fetch on every call, since there is no cached value yet to
	// fall back on and jwx's own background schedule offers no way to wait
	// for its result.
	fetched bool
	// lastKidRefresh is the last time refreshOnUnknownKid forced a fetch;
	// see minKidRefreshInterval.
	lastKidRefresh time.Time
	// fetchFailedAt and fetchErr gate retries once registered but never fetched:
	// key resolution runs before signature verification, so without this an
	// authenticated client can otherwise drive one real outbound fetch per
	// token-exchange request just by naming an issuer whose endpoint is
	// down. fetchErr is served directly while the gate is closed, so the
	// caller still gets a specific error instead of a generic "try later".
	// Not consulted once fetched is true: a healthy issuer, or one serving
	// stale-but-valid keys through a later background-refresh failure, must
	// never be gated.
	fetchFailedAt time.Time
	fetchErr      error
}

// limitedBodyTransport wraps an http.RoundTripper to cap every response body
// at max bytes, via http.MaxBytesReader rather than io.LimitReader: the
// latter truncates silently, which would let a caller parse a cut-off JWKS
// document as if it were complete, where the former surfaces a
// *http.MaxBytesError instead. Its error text ("http: request body too
// large") is written for the request-body case MaxBytesReader was designed
// for, so it reads oddly for a capped response — an accepted rough edge
// rather than justifying a custom ReadCloser.
//
// The nil first argument (http.ResponseWriter) is safe: MaxBytesReader only
// reaches it through a type assertion (`l.w.(requestTooLarger)`) used to tell
// a real server connection to close early, which — on a nil interface value
// — safely evaluates to false rather than panicking (net/http/request.go).
type limitedBodyTransport struct {
	base http.RoundTripper
	max  int64
}

// RoundTrip delegates to base and then caps the returned body. The cap
// applies to every response, including a non-2xx one whose body the caller
// doesn't intend to parse — draining it is only ever io.Discard-ed, not
// unbounded, so this errs on the safe side rather than special-casing status
// codes.
func (t *limitedBodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = http.MaxBytesReader(nil, resp.Body, t.max)
	return resp, nil
}

// CloseIdleConnections forwards to the wrapped transport, as every wrapping
// RoundTripper must — see networking.IdleConnectionCloser. There is no pool to
// drain today (this client is built WithDisableKeepAlives(true)); the forwarder
// keeps that an optimization decision rather than a correctness dependency.
func (t *limitedBodyTransport) CloseIdleConnections() {
	if closer, ok := t.base.(networking.IdleConnectionCloser); ok {
		closer.CloseIdleConnections()
	}
}

// newActorMatcherEngine creates a CEL engine for admin-authored actor matcher
// expressions. The only available variable is "claims", a map[string]any.
func newActorMatcherEngine() *cel.Engine {
	return cel.NewEngine(
		celgo.Variable("claims", celgo.MapType(celgo.StringType, celgo.DynType)),
	)
}

// compileActorMatcher compiles an optional admin-authored actor matcher.
//
// It does not check the expression's output type: toolhive-core's
// cel.Engine has no accessor for the checked AST's static type, only the
// compiled *cel.CompiledExpression. An expression that type-checks but
// does not return bool (e.g. "claims.foo" where foo is a string) therefore
// compiles successfully here — it is only rejected later, at evaluation
// time, by EvaluateBool's runtime type assertion (see
// resolveActorAuthorization), which denies the token on any evaluation
// error including a wrong result type.
func compileActorMatcher(actorMatcher string) (*cel.CompiledExpression, error) {
	if actorMatcher == "" {
		return nil, nil
	}

	expr, err := newActorMatcherEngine().Compile(actorMatcher)
	if err != nil {
		return nil, fmt.Errorf("invalid actor_matcher: %w", err)
	}

	return expr, nil
}

// ResolveJWTBearerGrantPolicies clones grant policies and parses their duration
// fields once at the RunConfig-to-runtime boundary.
func ResolveJWTBearerGrantPolicies(issuers []TrustedIssuer) ([]TrustedIssuer, error) {
	resolved := slices.Clone(issuers)
	for i := range resolved {
		if resolved[i].JWTBearerGrant == nil {
			continue
		}
		policy := cloneJWTBearerGrantPolicy(resolved[i].JWTBearerGrant)
		age, err := time.ParseDuration(policy.MaxAssertionAge)
		if err != nil || age <= 0 {
			return nil, fmt.Errorf("issuer_url %q: jwt_bearer_grant.max_assertion_age must be a positive duration", resolved[i].IssuerURL)
		}
		policy.maxAssertionAge = age
		resolved[i].JWTBearerGrant = policy
	}
	return resolved, nil
}

// NewMultiIssuerTokenValidator creates a validator that accepts tokens from
// the authorization server itself and from the provided trusted external
// issuers. Returns an error if selfValidator is nil, selfIssuer is empty,
// any TrustedIssuer is invalid (see validateTrustedIssuer), or an issuer's
// dedicated HTTP client cannot be built (see newExternalIssuerConfig for
// why each issuer gets its own).
func NewMultiIssuerTokenValidator(
	selfValidator *SelfIssuedTokenValidator,
	selfIssuer string,
	trustedIssuers []TrustedIssuer,
	allowedAudiences []string,
) (_ *MultiIssuerTokenValidator, retErr error) {
	if selfValidator == nil {
		return nil, errors.New("selfValidator must not be nil")
	}
	if selfIssuer == "" {
		return nil, errors.New("selfIssuer must not be empty")
	}

	// Root every per-issuer JWKS worker pool in one validator-scoped context so
	// Close can tear them all down; see (*MultiIssuerTokenValidator).Close and
	// newExternalIssuerConfig.
	ctx, cancel := context.WithCancel(context.Background())
	issuers := make(map[string]*externalIssuerConfig, len(trustedIssuers))
	// If construction fails after some issuers' caches have already started,
	// release them rather than leaking their workers for the life of the
	// process (buildProvider runs after buildUpstreams, so a later failure that
	// abandons a half-built validator is reachable). Same cancel-first, single
	// shared-deadline shape as Close.
	defer func() {
		if retErr != nil {
			cancel()
			shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), httpTimeout)
			defer cancelShutdown()
			for issuerURL, issuerConfig := range issuers {
				// Log rather than drop: retErr is the dominant signal, but a
				// pool that fails to drain during this cleanup would otherwise
				// leave no diagnostic.
				if err := issuerConfig.shutdownJWKSCache(shutdownCtx); err != nil {
					slog.Warn("failed to shut down JWKS cache during validator construction cleanup",
						"issuer", issuerURL, "error", err)
				}
			}
		}
	}()

	for _, ti := range trustedIssuers {
		if err := validateTrustedIssuer(ti, selfIssuer, issuers, allowedAudiences); err != nil {
			return nil, err
		}
		if len(ti.AllowedActors) == 0 && ti.ActorMatcher == "" {
			message := func() string {
				if ti.AllowMayAct {
					return "Trusted issuer has no allowed actors or actor matcher configured; " +
						"only may_act-bearing subject tokens from it will be accepted"
				}
				return "Trusted issuer has no allowed actors or actor matcher configured; " +
					"no subject tokens from it will be accepted"
			}()
			slog.Warn(message, "issuer", ti.IssuerURL)
		}
		if !looksLikeResourceIdentifier(ti.ExpectedAudience) {
			slog.Warn("Trusted issuer's expected_audience does not look like a resource identifier "+
				"(no URI scheme, e.g. \"https://\" or \"api://\"); it may actually be a client ID. "+
				"rejectIDTokenClaims only catches an ID token that carries at_hash/c_hash, which OIDC "+
				"does not require, so this issuer's audience check may not distinguish an ID token "+
				"from an access token",
				"issuer", ti.IssuerURL, "expected_audience", ti.ExpectedAudience,
			)
		}

		issuerConfig, err := newExternalIssuerConfig(ctx, ti)
		if err != nil {
			return nil, err
		}
		issuers[ti.IssuerURL] = issuerConfig
	}

	return &MultiIssuerTokenValidator{
		selfIssuer:    selfIssuer,
		selfValidator: selfValidator,
		issuers:       issuers,
		cancel:        cancel,
	}, nil
}

// Close shuts down every per-issuer jwk.Cache, stopping the background JWKS
// refresh worker pool (and its ~3 goroutines) each one runs. It cancels the
// validator-scoped context those pools share — signalling them all to stop at
// once — then waits, under a single shared httpTimeout budget, for each cache
// to drain. Cancelling up front (rather than after the loop) means the pools
// unwind in parallel, and the one shared deadline bounds the total wait by
// httpTimeout rather than httpTimeout×N even if a pool ignores cancellation.
// This is the same order the construction-failure path uses. It is safe to call
// more than once and on a validator with no external issuers; the validator
// must not be used after Close.
//
// A server holds its MultiIssuerTokenValidator and calls this from Close and
// its construction error path (see pkg/authserver), so neither a normal
// shutdown nor a failed reconstruction leaks these workers.
// authserver.CloseIdleConnections deliberately does NOT reach it: that path
// must stay safe to call on a still-serving server, which needs the workers to
// keep refreshing external issuers' keys.
func (v *MultiIssuerTokenValidator) Close() error {
	if v == nil {
		return nil
	}
	if v.cancel != nil {
		v.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()
	var errs []error
	for issuerURL, issuerConfig := range v.issuers {
		if err := issuerConfig.shutdownJWKSCache(ctx); err != nil {
			errs = append(errs, fmt.Errorf("issuer %s: %w", issuerURL, err))
		}
	}
	return errors.Join(errs...)
}

func cloneJWTBearerGrantPolicy(policy *JWTBearerGrantPolicy) *JWTBearerGrantPolicy {
	if policy == nil {
		return nil
	}
	clone := *policy
	clone.AcceptedAudiences = slices.Clone(policy.AcceptedAudiences)
	clone.SubjectBindings = make([]JWTBearerSubjectBinding, len(policy.SubjectBindings))
	for i, binding := range policy.SubjectBindings {
		clone.SubjectBindings[i] = JWTBearerSubjectBinding{
			Subject:          binding.Subject,
			AllowedResources: slices.Clone(binding.AllowedResources),
		}
	}
	if clone.maxAssertionAge == 0 {
		clone.maxAssertionAge, _ = time.ParseDuration(clone.MaxAssertionAge)
	}
	return &clone
}

// newExternalIssuerConfig builds the *externalIssuerConfig for a single
// already-validated TrustedIssuer: a dedicated HTTP client (scoped to that
// issuer's own InsecureAllowHTTP/AllowPrivateIPs), its body-size-capped
// transport, and its own jwk.Cache rooted in ctx. Called once per issuer from
// NewMultiIssuerTokenValidator's constructor loop, after validateTrustedIssuer
// and the startup warnings have already run for ti. ctx is the validator-scoped
// context (see NewMultiIssuerTokenValidator); cancelling it, or the validator's
// Close, tears down the cache's worker pool.
func newExternalIssuerConfig(ctx context.Context, ti TrustedIssuer) (*externalIssuerConfig, error) {
	// Clone AllowedActors and AllowedDelegateClients so a caller mutating
	// their original slices in place (e.g. a future config reload) cannot
	// race with the unsynchronized reads in resolveActorAuthorization and
	// validateExternalToken, which run on every validation without
	// holding externalIssuerConfig.mu (that mutex guards JWKS state only).
	ti.AllowedActors = slices.Clone(ti.AllowedActors)
	ti.AllowedDelegateClients = slices.Clone(ti.AllowedDelegateClients)
	ti.JWTBearerGrant = cloneJWTBearerGrantPolicy(ti.JWTBearerGrant)

	actorMatcher, err := compileActorMatcher(ti.ActorMatcher)
	if err != nil {
		return nil, fmt.Errorf("issuer_url %q: %w", ti.IssuerURL, err)
	}

	// Deliberately networking.NewHttpClientBuilder(), not
	// NewHostScopedClientBuilder: that helper ORs
	// INSECURE_DISABLE_URL_VALIDATION and an auto-localhost exemption into
	// BOTH the HTTP-scheme and private-IP gates, so an unrelated env var —
	// or a trusted issuer that merely happens to be on localhost — would
	// silently widen AllowPrivateIPs regardless of what the operator set,
	// defeating the point of splitting the two flags per issuer.
	//
	// Keep-alive connections are disabled: this client dials jwks_uri, a host taken
	// from an untrusted discovery document, only on the fixed
	// jwksRefreshInterval schedule plus occasional on-demand refreshes —
	// no hot path here to trade the per-dial SSRF check away for.
	builder := networking.NewHttpClientBuilder().
		WithInsecureAllowHTTP(ti.InsecureAllowHTTP).
		WithPrivateIPs(ti.AllowPrivateIPs).
		WithTimeout(httpTimeout).
		WithDisableKeepAlives(true)
	if ti.CAFilePath != "" {
		builder = builder.WithSystemRootsPlusCABundle(ti.CAFilePath)
	}
	httpClient, err := builder.Build()
	if err != nil {
		return nil, fmt.Errorf("issuer_url %q: failed to build HTTP client: %w", ti.IssuerURL, err)
	}
	// Guard against a discovery/JWKS redirect hop landing on a
	// different, unvetted host — the same policy the transparent proxy
	// data path applies to a response derived from an untrusted remote
	// server (see SameHostRedirectPolicy's doc comment).
	httpClient.CheckRedirect = networking.SameHostRedirectPolicy()

	// Cap every response body this client reads at maxResponseBodySize —
	// discovery already enforces this itself via io.LimitReader
	// (discoverJWKSURL), but the JWKS fetch is handed to jwx's jwk.Cache
	// below, which has no equivalent cap of its own (httprc.MaxBufferSize
	// is ~1000 MiB, and its transformer does an unbounded io.ReadAll under
	// that ceiling before parsing). Wrapped OUTSIDE httpClient.Transport
	// (which Build() always sets — see networking's builder) so the
	// private-IP dial guard and ValidatingTransport's scheme check still
	// run first, on the inner, unwrapped transport.
	httpClient.Transport = &limitedBodyTransport{
		base: httpClient.Transport,
		max:  maxResponseBodySize,
	}

	// One jwk.Cache per issuer (see externalIssuerConfig.jwksCache's doc
	// comment for why), each running its own background worker pool
	// (jwk.NewCache -> httprc.Client.Start). WithWorkers(1) caps that pool to
	// one worker per issuer instead of httprc's default five — budget roughly
	// three goroutines per issuer including its controller loop and wait-group
	// waiter.
	//
	// The pool is rooted in ctx, the validator-scoped context, so it outlives
	// any single call but is torn down when the validator's Close cancels ctx
	// and shuts each cache down (see shutdownJWKSCache). Before this wiring the
	// pools ran for the life of the process, one leaked set per issuer on every
	// server reconstruction; pkg/auth/token.go's TokenValidator still has that
	// shape but roots its cache in a caller-supplied context.
	jwksCache, err := jwk.NewCache(ctx, httprc.NewClient(httprc.WithWorkers(1)))
	if err != nil {
		return nil, fmt.Errorf("issuer_url %q: failed to create JWKS cache: %w", ti.IssuerURL, err)
	}

	return &externalIssuerConfig{
		TrustedIssuer: ti,
		actorMatcher:  actorMatcher,
		jwksURL:       ti.JWKSURL,
		httpClient:    httpClient,
		jwksCache:     jwksCache,
	}, nil
}

// shutdownJWKSCache stops this issuer's JWKS refresh worker pool, waiting for
// its goroutines to drain until ctx expires. ctx must be fresh — not the
// validator-scoped one the cache was started with, which Close cancels — or
// Shutdown returns immediately without waiting; see httprc's
// Controller.ShutdownContext. Callers pass one shared deadline context so a set
// of N issuers drains within a single timeout budget, not N of them. A nil
// cache (an externalIssuerConfig built by ValidateTrustedIssuers, which never
// starts one) is a no-op, as is a double shutdown.
func (c *externalIssuerConfig) shutdownJWKSCache(ctx context.Context) error {
	if c.jwksCache == nil {
		return nil
	}
	return c.jwksCache.Shutdown(ctx)
}

// ValidateTrustedIssuers runs every structural check
// NewMultiIssuerTokenValidator performs on trustedIssuers — required fields,
// self-issuer collision, duplicate issuers, ActorClaim reachability, and
// ActorMatcher compilation — without constructing a validator or any
// per-issuer HTTP client. Config validation calls this to fail before the live upstream DCR registration
// and storage creation that run between RunConfig.Validate and server
// construction; NewMultiIssuerTokenValidator repeats the same checks at
// server startup as defence in depth. Both route through validateTrustedIssuer,
// so the two can't drift out of sync.
func ValidateTrustedIssuers(trustedIssuers []TrustedIssuer, selfIssuer string, allowedAudiences []string) error {
	issuers := make(map[string]*externalIssuerConfig, len(trustedIssuers))
	for _, ti := range trustedIssuers {
		if err := validateTrustedIssuer(ti, selfIssuer, issuers, allowedAudiences); err != nil {
			return err
		}
		issuers[ti.IssuerURL] = &externalIssuerConfig{TrustedIssuer: ti}
	}
	return nil
}

// Validate parses the raw JWT to extract the issuer claim, then routes validation
// to either the self-issued validator or the appropriate external issuer validator.
// Returns an error if the issuer is not trusted.
func (v *MultiIssuerTokenValidator) Validate(ctx context.Context, rawToken string) (*ValidatedClaims, error) {
	// Parse the JWT without verification to peek at the issuer claim.
	// This is safe because we verify the signature in a subsequent step.
	issuer, err := peekIssuer(rawToken)
	if err != nil {
		return nil, fmt.Errorf("failed to determine token issuer: %w", err)
	}

	if issuer == v.selfIssuer {
		return v.selfValidator.Validate(ctx, rawToken)
	}

	issuerConfig, ok := v.issuers[issuer]
	if !ok {
		return nil, fmt.Errorf("untrusted issuer: %q", issuer)
	}

	return v.validateExternalToken(ctx, rawToken, issuerConfig)
}

// ValidateJWTBearerAssertion verifies a plain RFC 7523 assertion issued by an
// external issuer explicitly enabled for the JWT-bearer grant. It deliberately does not apply RFC 8693
// delegation consent (AllowedActors, ActorMatcher, or may_act): a JWT-bearer
// assertion's authorization policy is separate from delegation policy.
//
// The assertion audience must intersect the issuer's configured
// JWTBearerGrant.AcceptedAudiences, or just tokenEndpoint when that is unset.
// The caller is responsible for the phase-specific subject binding, maximum
// age, replay, and issuance policy after this cryptographic verification
// succeeds.
func (v *MultiIssuerTokenValidator) ValidateJWTBearerAssertion(
	ctx context.Context, rawToken, tokenEndpoint string,
) (*ValidatedClaims, error) {
	if tokenEndpoint == "" {
		return nil, errors.New("token endpoint must not be empty")
	}

	issuer, err := peekIssuer(rawToken)
	if err != nil {
		return nil, fmt.Errorf("failed to determine assertion issuer: %w", err)
	}
	issuerConfig, ok := v.issuers[issuer]
	if !ok {
		return nil, fmt.Errorf("untrusted assertion issuer: %q", issuer)
	}
	if issuerConfig.JWTBearerGrant == nil {
		return nil, fmt.Errorf("assertion issuer is not enabled for the JWT-bearer grant: %q", issuer)
	}

	standardClaims, extraClaims, err := v.verifyExternalSignature(ctx, rawToken, issuerConfig)
	if err != nil {
		return nil, err
	}

	if err := rejectIDTokenClaims(extraClaims); err != nil {
		return nil, err
	}

	acceptedAudiences := []string{tokenEndpoint}
	if issuerConfig.JWTBearerGrant != nil && len(issuerConfig.JWTBearerGrant.AcceptedAudiences) > 0 {
		acceptedAudiences = issuerConfig.JWTBearerGrant.AcceptedAudiences
	}
	if err := validateJWTBearerAssertionClaims(standardClaims, issuerConfig.IssuerURL, acceptedAudiences); err != nil {
		return nil, err
	}
	return buildValidatedClaims(standardClaims, extraClaims), nil
}

// verifyExternalSignature parses and verifies a JWT using the configured
// issuer's cached JWKS. Both RFC 8693 subject tokens and RFC 7523 assertions
// use this path so unknown-kid refreshes and key-use/algorithm restrictions
// cannot diverge between grant types.
func (v *MultiIssuerTokenValidator) verifyExternalSignature(
	ctx context.Context, rawToken string, issuerConfig *externalIssuerConfig,
) (jwt.Claims, map[string]any, error) {
	parsedToken, err := jwt.ParseSigned(rawToken, allowedSignatureAlgorithms)
	if err != nil {
		return jwt.Claims{}, nil, fmt.Errorf("subject token is not a valid JWT: %w", err)
	}

	jwks, err := v.lookupJWKS(ctx, issuerConfig)
	if err != nil {
		return jwt.Claims{}, nil, fmt.Errorf("failed to fetch JWKS for issuer %s: %w", issuerConfig.IssuerURL, err)
	}

	standardClaims, extraClaims, kidMatched, err := verifySignature(parsedToken, jwks)
	if err != nil && !kidMatched {
		// The token's kid isn't among the keys we have cached — possibly a
		// legitimate rotation the issuer's cache hasn't caught up with yet
		// (jwx's own background refresh floor is 15 minutes by default).
		// Force an immediate re-fetch and retry once before giving up; a
		// spoofed-kid attempt (kidMatched true) never reaches this branch,
		// since a refresh can't change what signature the token was made
		// with.
		v.refreshOnUnknownKid(ctx, issuerConfig)
		if refreshedJWKS, lookupErr := v.lookupJWKS(ctx, issuerConfig); lookupErr == nil {
			standardClaims, extraClaims, _, err = verifySignature(parsedToken, refreshedJWKS)
		}
	}
	if err != nil {
		return jwt.Claims{}, nil, err
	}
	return standardClaims, extraClaims, nil
}

// validateExternalToken verifies a JWT from a trusted external issuer by
// fetching the issuer's JWKS (with caching) and validating the signature and claims.
func (v *MultiIssuerTokenValidator) validateExternalToken(
	ctx context.Context,
	rawToken string,
	issuerConfig *externalIssuerConfig,
) (*ValidatedClaims, error) {
	standardClaims, extraClaims, err := v.verifyExternalSignature(ctx, rawToken, issuerConfig)
	if err != nil {
		return nil, err
	}

	// Validate issuer and audience, tolerating externalClockSkewLeeway on
	// nbf/iat/exp. Expiry is enforced strictly (no leeway) below.
	expected := jwt.Expected{
		Issuer:      issuerConfig.IssuerURL,
		AnyAudience: jwt.Audience{issuerConfig.ExpectedAudience},
	}
	if err := standardClaims.ValidateWithLeeway(expected, externalClockSkewLeeway); err != nil {
		return nil, fmt.Errorf("subject token claims validation failed: %w", err)
	}

	// Subject is required for delegation.
	if standardClaims.Subject == "" {
		return nil, fmt.Errorf("subject token is missing required 'sub' claim")
	}

	// Expiry is required so the delegated token can be bounded by the subject
	// token's remaining lifetime.
	if standardClaims.Expiry == nil {
		return nil, errors.New("subject token is missing required 'exp' claim")
	}

	// An expired subject token is never acceptable, despite the leeway
	// above: it cannot bound the delegated token's lifetime.
	if standardClaims.Expiry.Time().Before(time.Now()) {
		return nil, errors.New("subject token has expired")
	}

	// Reject a subject token that is actually an ID token — see
	// rejectIDTokenClaims's doc comment.
	if err := rejectIDTokenClaims(extraClaims); err != nil {
		return nil, err
	}

	if err := checkMayActAllowed(extraClaims, v.selfIssuer, issuerConfig); err != nil {
		return nil, err
	}

	claims := buildValidatedClaims(standardClaims, extraClaims)

	// Recorded for every external token, even a may_act-bearing one that
	// leaves ExternalActor unset — without this, a may_act-authorized
	// external token would be indistinguishable from a self-issued exchange
	// downstream, which is backwards: this is the path that most needs an
	// audit trail. Set from issuerConfig, already matched against the
	// validated "iss" claim above — never from token content.
	claims.ExternalIssuer = issuerConfig.IssuerURL

	// Set unconditionally, including for a may_act-bearing token: an issuer
	// that can emit may_act but has no way to populate AllowedActors (e.g.
	// Entra, Okta) would otherwise have no per-client containment on that
	// path (see #5989). checkDelegationConsent enforces it against the
	// authenticated client; this validator never sees that client ID.
	claims.AllowedDelegateClients = issuerConfig.AllowedDelegateClients

	// A may_act claim is authoritative and enforced by checkDelegationConsent
	// against the authenticated client — validateMayActShape above has
	// already confirmed its own "iss" (if any) names this server, so
	// may_act.sub is in ToolHive's own client namespace. AllowedActors and
	// ActorMatcher below are skipped entirely when MayAct is set. Otherwise,
	// either the resolved external actor claim or the matcher evaluated against
	// the complete signature-verified JWT claims authorizes delegation. Neither
	// is ever compared against the authenticated ToolHive client directly.
	if claims.MayAct == nil {
		actor, authorized, err := resolveActorAuthorization(issuerConfig, claims, extraClaims)
		if err != nil {
			return nil, err
		}
		claims.ExternalActor = actor
		claims.ExternalActorAuthorized = authorized
	}

	return claims, nil
}

// checkMayActAllowed rejects a may_act claim from an issuer that hasn't
// opted in (issuerConfig.AllowMayAct false) before validating the claim's
// shape: a malformed may_act on a disabled-by-default issuer should surface
// this clearer, more-actionable message rather than "malformed 'may_act'
// claim" — both paths reject the token either way, but this one names the
// actual reason for the common case.
//
// selfIssuer (not issuerConfig.IssuerURL) is the correct comparison for
// may_act's optional "iss" once shape validation runs: that member
// identifies the namespace of may_act.sub, always compared against a
// ToolHive client ID regardless of which issuer validated the surrounding
// token. requireIss is true here — unlike the self-issued path, the external
// path cannot leave "iss" optional; see validateMayActShape's doc comment.
func checkMayActAllowed(extraClaims map[string]any, selfIssuer string, issuerConfig *externalIssuerConfig) error {
	rawMayAct, ok := extraClaims["may_act"]
	if ok && rawMayAct != nil && !issuerConfig.AllowMayAct {
		return fmt.Errorf(
			"subject token from issuer %q carries a may_act claim, but allow_may_act is disabled",
			issuerConfig.IssuerURL)
	}
	return validateMayActShape(extraClaims, selfIssuer, true)
}

// ensureRegistered resolves issuerConfig.jwksURL — via OIDC discovery on
// first use — and registers it with issuerConfig's own jwk.Cache (see
// registerOrRefresh for the Register-vs-Refresh decision). Discovery
// happens at most once per issuer for the life of the process; see
// externalIssuerConfig's jwksURL doc comment.
//
// The JWKS fetch is retried until one succeeds (fetched stays false
// otherwise), gated by jwksFetchFailureBackoff: key resolution runs before
// the subject token's signature is checked, so without this gate an
// authenticated client could force a real outbound attempt on every
// request to an issuer whose endpoint is down. The gate only applies
// before the first successful fetch — once fetched is true, a healthy
// issuer or one serving stale-but-valid keys through a later refresh
// failure is unaffected. While closed, the last error is replayed
// directly rather than retried.
//
// Every background refresh, not just the first request-triggered one,
// goes through the maxResponseBodySize-capped transport (see
// limitedBodyTransport) despite having no request or client authentication
// of its own — without that cap a compromised issuer could force an
// oversized allocation on every autonomous refresh, indefinitely.
func (v *MultiIssuerTokenValidator) ensureRegistered(ctx context.Context, issuerConfig *externalIssuerConfig) error {
	issuerConfig.mu.Lock()
	defer issuerConfig.mu.Unlock()

	if issuerConfig.fetched {
		return nil
	}
	if time.Since(issuerConfig.fetchFailedAt) < jwksFetchFailureBackoff {
		return issuerConfig.fetchErr
	}

	if err := v.registerOrRefresh(ctx, issuerConfig); err != nil {
		issuerConfig.fetchErr = err
		issuerConfig.fetchFailedAt = time.Now()
		return err
	}
	issuerConfig.fetched = true
	issuerConfig.fetchErr = nil
	return nil
}

// registerOrRefresh performs the actual discovery/registration/fetch
// attempt for issuerConfig; ensureRegistered holds issuerConfig.mu across
// this call, single-flighting it per issuer so concurrent validations
// don't pile up N redundant fetches.
//
// Whether jwksURL is already registered is asked of
// issuerConfig.jwksCache.IsRegistered directly, never remembered in a
// field: Register's own registration step is a channel send to the
// cache's backend goroutine and can fail after enqueue but before receipt
// (context deadline), in which case nothing was actually registered. A
// locally remembered "we called Register" flag can't distinguish that
// from "registered, only the fetch failed", and would wrongly keep
// retrying via Refresh — which errors on a URL the cache never heard of —
// forever after. Asking the cache directly is authoritative either way.
//
// IsRegistered makes no network call but isn't free: it's a round-trip
// over that same channel, so it blocks if the backend is busy and returns
// false (not an error) on context expiry. Its own timeout below matters
// for that reason — a false from an expired context just routes to
// Register, whose "already registered" error is transient and absorbed by
// ensureRegistered's backoff gate.
func (v *MultiIssuerTokenValidator) registerOrRefresh(ctx context.Context, issuerConfig *externalIssuerConfig) error {
	// Detach from the caller's request context throughout this function: net/http
	// cancels ctx when the client disconnects, and this runs before the subject
	// token's signature is even checked, so an aborted connection must not cut off
	// work other in-flight validations of this issuer are waiting on (mu, held by
	// the caller), nor let repeating the abort drive unbounded outbound requests
	// to the external IdP.
	detached := context.WithoutCancel(ctx)

	if issuerConfig.jwksURL == "" {
		discoverCtx, cancel := context.WithTimeout(detached, httpTimeout)
		jwksURL, err := v.discoverJWKSURL(discoverCtx, issuerConfig)
		cancel()
		if err != nil {
			return fmt.Errorf("OIDC discovery failed for %s: %w", issuerConfig.IssuerURL, err)
		}
		issuerConfig.jwksURL = jwksURL
	}

	// Validate the JWKS URL here, in the single choke point every
	// registration passes through — whether it was hand-configured on
	// TrustedIssuer or just discovered above. A configured JWKSURL never
	// reaches discoverJWKSURL, so checking only there would leave
	// hand-configured URLs unvalidated.
	if err := ValidateJWKSURL(issuerConfig.jwksURL, issuerConfig.InsecureAllowHTTP, issuerConfig.AllowPrivateIPs); err != nil {
		return fmt.Errorf("jwks_url for issuer %s is invalid: %w", issuerConfig.IssuerURL, err)
	}

	registeredCtx, cancel := context.WithTimeout(detached, httpTimeout)
	registered := issuerConfig.jwksCache.IsRegistered(registeredCtx, issuerConfig.jwksURL)
	cancel()

	if registered {
		// Already registered with this issuer's own cache, on a prior call
		// whose own fetch never completed successfully — the only way this
		// can be true, now that each issuer has its own cache. Register
		// would error on an already-tracked URL, so retry via Refresh
		// instead.
		fetchCtx, cancel := context.WithTimeout(detached, httpTimeout)
		defer cancel()
		if _, err := issuerConfig.jwksCache.Refresh(fetchCtx, issuerConfig.jwksURL); err != nil {
			return fmt.Errorf("failed to fetch JWKS for issuer %s: %w", issuerConfig.IssuerURL, err)
		}
		return nil
	}

	// A newly created httprc.Resource is always scheduled to fetch
	// immediately, so Register's own default WithWaitReady(true) blocks on
	// that single automatic fetch. An explicit Refresh call right here would
	// race it and issue a genuine second outbound request — that's why the
	// registered branch above, not this one, is where Refresh is used.
	fetchCtx, cancel := context.WithTimeout(detached, httpTimeout)
	defer cancel()
	if err := issuerConfig.jwksCache.Register(fetchCtx, issuerConfig.jwksURL,
		jwk.WithHTTPClient(issuerConfig.httpClient), jwk.WithConstantInterval(jwksRefreshInterval)); err != nil {
		return fmt.Errorf("failed to register JWKS for issuer %s: %w", issuerConfig.IssuerURL, err)
	}
	return nil
}

// lookupJWKS returns issuerConfig's current JWKS, registering and fetching it
// first if this is the first use (see ensureRegistered). jwk.Cache serves the
// last successfully fetched Set even while a later background refresh is
// failing (httprc only stores a value after a successful fetch), so a
// transient outage at an issuer that has already been reached once no longer
// surfaces as a validation failure.
// Each call converts the cached Set into a fresh *jose.JSONWebKeySet — the
// two libraries' key representations aren't shared, so nothing here is
// visible to, or mutable by, any other concurrent caller.
func (v *MultiIssuerTokenValidator) lookupJWKS(
	ctx context.Context,
	issuerConfig *externalIssuerConfig,
) (*jose.JSONWebKeySet, error) {
	if err := v.ensureRegistered(ctx, issuerConfig); err != nil {
		return nil, err
	}

	set, err := issuerConfig.jwksCache.Lookup(ctx, issuerConfig.jwksURL)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup JWKS: %w", err)
	}

	jwks, err := bridgeJWKSet(set)
	if err != nil {
		return nil, err
	}

	if len(jwks.Keys) == 0 {
		return nil, errors.New("JWKS contains no keys")
	}
	if len(jwks.Keys) > maxJWKSKeys {
		return nil, fmt.Errorf("JWKS contains too many keys: %d (max %d)", len(jwks.Keys), maxJWKSKeys)
	}

	return jwks, nil
}

// bridgeJWKSet converts a jwx jwk.Set into the go-jose jose.JSONWebKeySet
// shape verifySignature works with, by round-tripping through the standard
// JWKS JSON both types serialize to and from. This is the simplest correct
// conversion between the two libraries' key representations — it doesn't
// require hand-mapping every key type's fields (RSA, EC, OKP, ...) between
// jwk.Key and jose.JSONWebKey.
func bridgeJWKSet(set jwk.Set) (*jose.JSONWebKeySet, error) {
	raw, err := json.Marshal(set)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JWKS: %w", err)
	}
	var jwks jose.JSONWebKeySet
	if err := json.Unmarshal(raw, &jwks); err != nil {
		return nil, fmt.Errorf("failed to parse JWKS: %w", err)
	}
	return &jwks, nil
}

// refreshOnUnknownKid forces issuerConfig's own jwk.Cache to re-fetch its
// JWKS immediately, ahead of jwx's own background refresh schedule, when a
// subject token names a kid the last cached JWKS doesn't have — the
// situation a legitimate key rotation produces. Gated by minKidRefreshInterval
// and single-flighted via issuerConfig.mu, the same mutex ensureRegistered
// uses for its own initial fetch.
//
// Errors are logged, not returned: the caller (validateExternalToken) has
// already failed signature verification once and will simply fail again if
// the refresh didn't produce a usable key, which is the correct outcome for
// a genuinely invalid token.
func (*MultiIssuerTokenValidator) refreshOnUnknownKid(ctx context.Context, issuerConfig *externalIssuerConfig) {
	issuerConfig.mu.Lock()
	defer issuerConfig.mu.Unlock()

	if time.Since(issuerConfig.lastKidRefresh) < minKidRefreshInterval {
		return
	}
	issuerConfig.lastKidRefresh = time.Now()

	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*httpTimeout)
	defer cancel()
	if _, err := issuerConfig.jwksCache.Refresh(fetchCtx, issuerConfig.jwksURL); err != nil {
		slog.Debug("JWKS refresh on unknown kid failed", "issuer", issuerConfig.IssuerURL, "error", err)
	}
}

// peekIssuer parses a JWT without signature verification to extract the "iss" claim.
func peekIssuer(rawToken string) (string, error) {
	token, err := jwt.ParseSigned(rawToken, allowedSignatureAlgorithms)
	if err != nil {
		return "", fmt.Errorf("subject token is not a valid JWT: %w", err)
	}

	var claims jwt.Claims
	if err := token.UnsafeClaimsWithoutVerification(&claims); err != nil {
		return "", fmt.Errorf("failed to extract claims from subject token: %w", err)
	}

	if claims.Issuer == "" {
		return "", fmt.Errorf("subject token is missing 'iss' claim")
	}

	return claims.Issuer, nil
}

// discoverJWKSURL performs OIDC discovery to resolve the JWKS URL for an issuer.
// It fetches the OpenID Connect discovery document at {issuerURL}/.well-known/openid-configuration
// and extracts the jwks_uri field, using issuerConfig's own dedicated HTTP client.
//
// IssuerURL itself may legitimately carry a trailing slash (see
// validateTrustedIssuerURL in pkg/authserver/config.go — Microsoft Entra ID
// v1 is one real-world example), so it is trimmed before the well-known path
// is appended here, per OIDC Discovery §4.1. The comparison against the
// discovery document's own "issuer" below intentionally uses the untrimmed
// IssuerURL: per §4.3 that comparison must be exact, since it stands in for
// the eventual match against the token's "iss".
func (*MultiIssuerTokenValidator) discoverJWKSURL(ctx context.Context, issuerConfig *externalIssuerConfig) (string, error) {
	discoveryURL := strings.TrimSuffix(issuerConfig.IssuerURL, "/") + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create discovery request: %w", err)
	}

	resp, err := issuerConfig.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("discovery request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", fmt.Errorf("discovery endpoint returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		return "", fmt.Errorf("failed to read discovery response: %w", err)
	}

	var doc oauthproto.OIDCDiscoveryDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("failed to parse discovery document: %w", err)
	}

	if doc.Issuer != issuerConfig.IssuerURL {
		return "", fmt.Errorf("discovery document issuer %q does not match expected issuer %q", doc.Issuer, issuerConfig.IssuerURL)
	}

	if doc.JWKSURI == "" {
		return "", fmt.Errorf("discovery document missing 'jwks_uri'")
	}

	// The returned URL is validated by the caller (ensureRegistered), which is
	// the single choke point covering both discovered and pre-configured
	// JWKS URLs.
	return doc.JWKSURI, nil
}

// ValidateJWKSURL checks that jwksURL parses, has a host, uses HTTPS unless
// insecureAllowHTTP permits plain HTTP — and only exactly the "http" scheme,
// not any other non-https scheme such as "file" or "ftp" — and, when the
// host is an IP literal, is not a private or loopback address unless
// allowPrivateIPs permits that. Both flags come from the specific
// TrustedIssuer being fetched (see ensureRegistered), never from a validator-wide
// or self-issuer setting. This prevents SSRF attacks where a compromised
// discovery document — or a hand-configured jwks_url — points to internal
// services.
//
// This is the single implementation shared by the runtime choke point above
// (ensureRegistered, on every fetch) and pkg/authserver/config.go's config-time
// check (validateJWKSEndpointURL): the two must not drift out of sync, or a
// laxer runtime check would silently defeat the config-time guard.
func ValidateJWKSURL(jwksURL string, insecureAllowHTTP, allowPrivateIPs bool) error {
	u, err := url.Parse(jwksURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if u.Host == "" {
		return errors.New("host is required")
	}

	// Unlike issuer_url, a jwks_url carrying userinfo would actually work —
	// net/http turns it into a Basic auth header on every JWKS fetch — which
	// is precisely why it is rejected rather than tolerated: it would put a
	// live credential in the RunConfig, in this function's error strings, and
	// in any log that quotes the URL. A JWKS endpoint is public by
	// definition (it serves verification keys), so there is no legitimate
	// reason to authenticate to one.
	if u.User != nil {
		return errors.New("must not contain userinfo (credentials in the URL)")
	}

	if u.Scheme != "https" && (u.Scheme != "http" || !insecureAllowHTTP) {
		return fmt.Errorf("must use HTTPS, got %q", u.Scheme)
	}

	host := u.Hostname()
	ip := net.ParseIP(host)
	if ip != nil && !allowPrivateIPs && networking.IsPrivateIP(ip) {
		return errors.New("must not point to a private or loopback address")
	}

	return nil
}

// validateTrustedIssuer checks a single TrustedIssuer for structural validity
// before it is admitted into issuers: required fields, no collision with
// selfIssuer or an already-registered issuer, an ActorClaim that
// resolveActorAuthorization can actually read from Extra (or ClientID), a
// compilable ActorMatcher, and a well-formed AllowedDelegateClients (non-empty,
// no empty entries, and the wildcard anyDelegateClient never combined with
// specific client IDs or AllowMayAct).
//
// Error messages name TrustedIssuer's wire keys (issuer_url,
// expected_audience, actor_claim), not its Go field names: TrustedIssuer is
// the serialized RunConfig.TrustedIssuers schema (see its doc comment), so an
// operator's YAML uses these keys and should see them echoed back, not Go
// identifiers they never wrote.
func validateTrustedIssuer(
	ti TrustedIssuer, selfIssuer string, issuers map[string]*externalIssuerConfig, allowedAudiences []string,
) error {
	if ti.IssuerURL == "" {
		return errors.New("issuer_url is required")
	}
	if ti.ExpectedAudience == "" && ti.JWTBearerGrant == nil {
		return fmt.Errorf("issuer_url %q: expected_audience is required when JWT-bearer grant is disabled", ti.IssuerURL)
	}
	if ti.IssuerURL == selfIssuer {
		return fmt.Errorf("issuer_url %q: must not equal the authorization server's own issuer; "+
			"self-issued tokens are already handled separately", ti.IssuerURL)
	}
	if _, dup := issuers[ti.IssuerURL]; dup {
		return fmt.Errorf("issuer_url %q: configured more than once", ti.IssuerURL)
	}
	if ti.ActorClaim != "" && slices.Contains(actorClaimsNotInExtra, ti.ActorClaim) {
		return fmt.Errorf(
			"issuer_url %q: actor_claim %q is not supported "+
				`(use "client_id" or a non-registered claim such as "azp", "appid", "cid")`,
			ti.IssuerURL, ti.ActorClaim)
	}
	if _, err := compileActorMatcher(ti.ActorMatcher); err != nil {
		return fmt.Errorf("issuer_url %q: %w", ti.IssuerURL, err)
	}
	if err := validateDelegationPolicy(ti); err != nil {
		return err
	}
	if err := validateJWTBearerGrantPolicy(ti, allowedAudiences); err != nil {
		return err
	}
	if ti.AllowMayAct && slices.Contains(ti.AllowedDelegateClients, anyDelegateClient) {
		return fmt.Errorf(
			"issuer_url %q: allow_may_act must not be enabled when allowed_delegate_clients contains the wildcard %q",
			ti.IssuerURL, anyDelegateClient)
	}
	// AllowPrivateIPs without a hand-configured jwks_url would let OIDC
	// discovery — a document fetched from, and thus influenceable by, the
	// external issuer itself — choose the private target the dial is
	// allowed to reach. Requiring jwks_url pins that target to
	// operator-supplied config. Mirrors the config-time check in
	// pkg/authserver/config.go's validateTrustedIssuers; duplicated here so
	// a caller that builds the validator directly (factory, tests) without
	// running Config.Validate cannot bypass it.
	if ti.AllowPrivateIPs && ti.JWKSURL == "" {
		return fmt.Errorf(
			"issuer_url %q: allow_private_ips requires jwks_url to be set explicitly; "+
				"otherwise OIDC discovery — fetched from the external issuer — would choose the private target",
			ti.IssuerURL)
	}
	return nil
}

// validateDelegationPolicy validates fields used exclusively by RFC 8693.
func validateDelegationPolicy(ti TrustedIssuer) error {
	hasDelegationSignal := ti.ActorClaim != "" || ti.ActorMatcher != "" || ti.AllowMayAct || len(ti.AllowedActors) > 0
	if ti.ExpectedAudience == "" && !hasDelegationSignal {
		return nil
	}
	if ti.ExpectedAudience == "" && hasDelegationSignal {
		return fmt.Errorf(
			"issuer_url %q: expected_audience is required when actor_claim, actor_matcher, allow_may_act, or allowed_actors is set",
			ti.IssuerURL)
	}
	return validateAllowedDelegateClients(ti)
}

func validateJWTBearerGrantPolicy(ti TrustedIssuer, allowedAudiences []string) error {
	if ti.JWTBearerGrant == nil {
		return nil
	}
	policy := ti.JWTBearerGrant
	age, err := time.ParseDuration(policy.MaxAssertionAge)
	if err != nil || age <= 0 {
		return fmt.Errorf("issuer_url %q: jwt_bearer_grant.max_assertion_age must be a positive duration", ti.IssuerURL)
	}
	if err := validateJWTBearerSubjectBindings(ti, policy.SubjectBindings); err != nil {
		return err
	}
	return validateJWTBearerAcceptedAudiences(ti, policy.AcceptedAudiences, allowedAudiences)
}

func validateJWTBearerSubjectBindings(ti TrustedIssuer, bindings []JWTBearerSubjectBinding) error {
	if len(bindings) == 0 {
		return fmt.Errorf("issuer_url %q: jwt_bearer_grant.subject_bindings is required", ti.IssuerURL)
	}
	seenSubjects := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		if binding.Subject == "" || binding.Subject == anyDelegateClient {
			return fmt.Errorf("issuer_url %q: jwt_bearer_grant.subject_bindings contains an empty or wildcard subject", ti.IssuerURL)
		}
		if _, duplicate := seenSubjects[binding.Subject]; duplicate {
			return fmt.Errorf(
				"issuer_url %q: jwt_bearer_grant.subject_bindings contains duplicate subject %q",
				ti.IssuerURL, binding.Subject)
		}
		seenSubjects[binding.Subject] = struct{}{}
		if len(binding.AllowedResources) == 0 {
			return fmt.Errorf("issuer_url %q: jwt_bearer_grant subject %q has no allowed_resources", ti.IssuerURL, binding.Subject)
		}
		for _, resource := range binding.AllowedResources {
			if resource == "" || resource == anyDelegateClient {
				return fmt.Errorf(
					"issuer_url %q: jwt_bearer_grant subject %q has an empty or wildcard resource",
					ti.IssuerURL, binding.Subject)
			}
			if err := server.ValidateAudienceURI(resource); err != nil {
				return fmt.Errorf(
					"issuer_url %q: jwt_bearer_grant subject %q resource %q is invalid: %w",
					ti.IssuerURL, binding.Subject, resource, err)
			}
		}
	}
	return nil
}

// validateJWTBearerAcceptedAudiences also rejects any accepted_audiences
// entry that is a configured resource audience (allowedAudiences).
// AcceptedAudiences identifies this authorization server itself (see its
// doc comment); an entry shared with a resource audience would let an
// upstream access token minted for that resource — not for this AS — pass
// ValidateJWTBearerAssertion's audience check and mint a ToolHive token for
// whatever resource the subject binding allows, bypassing the
// client-authenticated RFC 8693 delegation route entirely.
func validateJWTBearerAcceptedAudiences(ti TrustedIssuer, acceptedAudiences, allowedAudiences []string) error {
	for _, audience := range acceptedAudiences {
		if audience == "" {
			return fmt.Errorf("issuer_url %q: jwt_bearer_grant.accepted_audiences must not contain an empty value", ti.IssuerURL)
		}
		if err := server.ValidateAudienceURI(audience); err != nil {
			return fmt.Errorf(
				"issuer_url %q: jwt_bearer_grant.accepted_audiences value %q is invalid: %w",
				ti.IssuerURL, audience, err)
		}
		if slices.Contains(allowedAudiences, audience) {
			return fmt.Errorf(
				"issuer_url %q: jwt_bearer_grant.accepted_audiences value %q must not also be a "+
					"configured resource audience (allowed_audiences); accepted_audiences identifies "+
					"this authorization server, not a resource, or a resource-scoped access token "+
					"could be replayed as a JWT-bearer assertion", ti.IssuerURL, audience)
		}
	}
	return nil
}

// validateAllowedDelegateClients checks the structural and narrowing
// invariants for TrustedIssuer.AllowedDelegateClients, split out of
// validateTrustedIssuer to keep that function's cyclomatic complexity down.
func validateAllowedDelegateClients(ti TrustedIssuer) error {
	if len(ti.AllowedDelegateClients) == 0 {
		return fmt.Errorf(
			"issuer_url %q: allowed_delegate_clients is required; set it to [%q] to permit any ToolHive "+
				"confidential client holding the token-exchange grant, or list specific client IDs to bind "+
				"delegation to them — an issuer with no entries can never authorize a delegated exchange",
			ti.IssuerURL, anyDelegateClient)
	}
	if slices.Contains(ti.AllowedDelegateClients, "") {
		return fmt.Errorf(
			"issuer_url %q: allowed_delegate_clients must not contain an empty client ID", ti.IssuerURL)
	}
	if slices.Contains(ti.AllowedDelegateClients, anyDelegateClient) && len(ti.AllowedDelegateClients) > 1 {
		return fmt.Errorf(
			"issuer_url %q: allowed_delegate_clients must not combine the wildcard %q with specific client IDs",
			ti.IssuerURL, anyDelegateClient)
	}
	if ti.AllowMayAct && slices.Contains(ti.AllowedDelegateClients, anyDelegateClient) {
		return fmt.Errorf(
			"issuer_url %q: allow_may_act must not be enabled when allowed_delegate_clients contains the wildcard %q",
			ti.IssuerURL, anyDelegateClient)
	}
	return nil
}

// looksLikeResourceIdentifier reports whether aud is shaped like a resource/API
// identifier (an absolute URI, e.g. "https://api.example.com" or
// "api://<app-id>") rather than a bare opaque string or GUID, which is more
// likely a client ID. This is a heuristic warning signal only — Entra v1
// legitimately uses a bare app-ID GUID as an access token's "aud" (see
// TrustedIssuer.ExpectedAudience's doc comment), so a bare value is not
// rejected, only flagged for the operator to double-check.
func looksLikeResourceIdentifier(aud string) bool {
	return strings.Contains(aud, "://")
}

// resolveActorAuthorization authorizes an external token when either its
// configured actor claim matches AllowedActors or ActorMatcher evaluates true
// against the complete signature-verified JWT claims map. It returns the
// resolved actor, when one exists, separately from authorization because a
// matcher need not depend on an actor claim. Called only when the subject token
// carries no may_act claim — see validateExternalToken.
func resolveActorAuthorization(
	issuerConfig *externalIssuerConfig,
	claims *ValidatedClaims,
	verifiedClaims map[string]any,
) (string, bool, error) {
	actor, actorErr := resolveAllowedActor(issuerConfig, claims)
	if actorErr == nil {
		return actor, true, nil
	}

	if issuerConfig.actorMatcher != nil {
		matched, err := issuerConfig.actorMatcher.EvaluateBool(map[string]any{"claims": verifiedClaims})
		if err != nil {
			// The raw CEL error is never logged or returned: it can quote
			// runtime values from the claims map (e.g. timestamp(claims.foo)
			// on a malformed string embeds that string in the error),
			// which would otherwise leak signed claim data into logs and,
			// via HandleTokenEndpointRequest's own error logging, further
			// downstream.
			slog.Debug("Trusted issuer actor matcher evaluation failed; denying subject token",
				"issuer", issuerConfig.IssuerURL)
			return "", false, fmt.Errorf("actor matcher evaluation failed for issuer %q", issuerConfig.IssuerURL)
		} else if matched {
			return actor, true, nil
		}
	}

	if len(issuerConfig.AllowedActors) == 0 && issuerConfig.actorMatcher != nil {
		return "", false, fmt.Errorf("subject token from issuer %q was not authorized by actor matcher", issuerConfig.IssuerURL)
	}

	return "", false, actorErr
}

// resolveAllowedActor resolves the issuer's configured actor claim from
// claims and checks it against issuerConfig.AllowedActors, returning the
// matched value on success.
func resolveAllowedActor(issuerConfig *externalIssuerConfig, claims *ValidatedClaims) (string, error) {
	claimName := issuerConfig.ActorClaim
	if claimName == "" {
		claimName = defaultActorClaim
	}

	// "client_id" is routed to ValidatedClaims.ClientID by assignClaim, so it
	// never appears in Extra; without this fallback a plausible operator
	// config (ActorClaim: "client_id") would silently reject all traffic.
	var raw any
	if claimName == "client_id" {
		raw = claims.ClientID
	} else {
		raw = claims.Extra[claimName]
	}

	actor, ok := raw.(string)
	if !ok || actor == "" {
		return "", fmt.Errorf(
			"subject token from issuer %q is missing or has an invalid %q claim required for delegation consent",
			issuerConfig.IssuerURL, claimName)
	}

	if !slices.Contains(issuerConfig.AllowedActors, actor) {
		return "", fmt.Errorf(
			"subject token from issuer %q names actor %q in claim %q, which is not in the allowed actors list",
			issuerConfig.IssuerURL, actor, claimName)
	}

	return actor, nil
}
