// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/x/errorsx"

	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	jwtBearerReplayPurpose      = "jwt-bearer"
	jwtBearerNoJTIReplayPurpose = "jwt-bearer-no-jti" //nolint:gosec // G101: replay-store purpose label, not a credential
)

// JWTBearerAssertionValidator verifies cryptographic and registered claims of a
// plain RFC 7523 JWT-bearer assertion.
type JWTBearerAssertionValidator interface {
	ValidateJWTBearerAssertion(
		ctx context.Context, rawToken string, defaultAudiences []string,
	) (*ValidatedClaims, error)
}

// JWTBearerHandler implements the unbound RFC 7523 JWT-bearer grant.
type JWTBearerHandler struct {
	validator     JWTBearerAssertionValidator
	tokenEndpoint string
	policies      map[string]*JWTBearerGrantPolicy
	redeemer      *redeemer
}

// newJWTBearerIssuanceHandler constructs the production handler for plain
// jwt-bearer assertions. See issuanceDeps.validate for the dependency
// requirements.
func newJWTBearerIssuanceHandler(deps issuanceDeps) (*JWTBearerHandler, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return &JWTBearerHandler{
		validator:     deps.validator,
		tokenEndpoint: deps.tokenEndpoint,
		// AcceptedAssertionTypes may exclude the plain type. Filtering here,
		// rather than in HandleTokenEndpointRequest, is the entire enforcement
		// mechanism: an issuer left out of the policy map is indistinguishable
		// from an issuer never configured at all, so the existing "issuer not
		// enabled for this grant" rejection already covers issuers that exclude
		// the plain type. An empty result is valid: every assertion of that
		// form is then rejected as not enabled.
		policies: deps.policiesFor(JWTBearerAssertionTypeJWTBearer),
		redeemer: newRedeemer(deps),
	}, nil
}

// CanHandleTokenEndpointRequest only claims plain assertions. A recognized ID-JAG
// assertion belongs to the bound handler (IDJAGHandler); malformed and
// unsupported typ values remain this handler's responsibility to reject.
func (*JWTBearerHandler) CanHandleTokenEndpointRequest(_ context.Context, requester fosite.AccessRequester) bool {
	return requester.GetGrantTypes().ExactOne(oauthproto.GrantTypeJWTBearer) &&
		assertionType(requester.GetRequestForm().Get("assertion")) != oauthproto.JWTTypeIDJAG
}

// CanSkipClientAuth permits a credential-free plain JWT-bearer assertion.
// fosite.NewAccessRequest always calls AuthenticateClient first and only
// discards its error when this returns true (access_request_handler.go),
// and AuthenticateClient falls back to reading HTTP Basic credentials off
// the raw request when no form-based client_assertion is present
// (client_authentication.go). If this only inspected the form, a request
// carrying a valid assertion plus a wrong HTTP Basic password would be
// silently accepted, since the resulting clientErr would be discarded
// without ever being looked at. So the raw *http.Request that fosite stashes
// under RequestContextKey must be checked for Basic credentials too; treat a
// missing/malformed request as "credentials might be present" so this fails
// closed rather than open.
func (*JWTBearerHandler) CanSkipClientAuth(ctx context.Context, requester fosite.AccessRequester) bool {
	form := requester.GetRequestForm()
	return assertionType(form.Get("assertion")) != oauthproto.JWTTypeIDJAG && !mayCarryClientCredentials(ctx, form)
}

// mayCarryClientCredentials reports whether a token request might carry client
// credentials: a client_id, client_secret, client_assertion or
// client_assertion_type form field, or HTTP Basic credentials. A missing or
// malformed raw request counts as "might carry", so callers fail closed.
func mayCarryClientCredentials(ctx context.Context, form url.Values) bool {
	// Every value of a field is checked, not just the first, so the decision
	// matches what a client-authentication strategy that reads any value accepts.
	for _, field := range []string{"client_id", "client_secret", "client_assertion", "client_assertion_type"} {
		if slices.ContainsFunc(form[field], func(v string) bool { return v != "" }) {
			return true
		}
	}
	req, ok := ctx.Value(fosite.RequestContextKey).(*http.Request)
	if !ok || req == nil {
		return true
	}
	_, _, basic := req.BasicAuth()
	return basic
}

// claimConfirmation is the RFC 7800 confirmation claim.
const claimConfirmation = "cnf"

// hasConfirmationClaim reports whether a validated assertion carries a cnf
// claim. Only presence counts, so "cnf": null and "cnf": {} are both found; the
// value is never read.
func hasConfirmationClaim(claims *ValidatedClaims) bool {
	if claims == nil {
		return false
	}
	// Claims is the full decoded set and Extra a subset of it. Both are checked
	// so hand-built ValidatedClaims and a later change to assignClaim stay safe.
	_, inClaims := claims.Claims[claimConfirmation]
	_, inExtra := claims.Extra[claimConfirmation]
	return inClaims || inExtra
}

// HandleTokenEndpointRequest validates policy and prepares a bounded access-token session.
func (h *JWTBearerHandler) HandleTokenEndpointRequest(ctx context.Context, requester fosite.AccessRequester) error {
	if !h.CanHandleTokenEndpointRequest(ctx, requester) {
		return errorsx.WithStack(fosite.ErrUnknownRequest)
	}
	assertion, err := validateJWTBearerAssertionForm(requester.GetRequestForm())
	if err != nil {
		return err
	}
	segments, err := canonicalAssertionSegments(assertion)
	if err != nil {
		return err
	}
	if err := validateAssertionType(assertion); err != nil {
		return err
	}
	claims, err := h.validateAssertion(ctx, assertion)
	if err != nil {
		return err
	}
	policy, ok := h.policies[claims.Issuer]
	if !ok {
		return invalidAssertion("The JWT bearer assertion issuer is not enabled for this grant.")
	}
	if err := checkAssertionMaxAge(claims, policy); err != nil {
		return err
	}
	resource, err := requestedResource(requester.GetRequestForm())
	if err != nil {
		return err
	}
	if err := checkSubjectBinding(claims, policy, resource); err != nil {
		return plainBindingRefusal(err)
	}
	clientID := jwtBearerClientID(claims.Issuer, claims.Subject)
	purpose := jwtBearerReplayPurpose
	if claims.JWTID == "" {
		purpose = jwtBearerNoJTIReplayPurpose
	}
	return h.redeemer.complete(ctx, requester, redemption{
		claims:   claims,
		resource: resource,
		// Namespaced by issuer, so a resource server sees one consistent
		// subject form for every assertion-derived token, whichever handler
		// minted it.
		subject:           claimnames.QualifySubject(claims.Issuer, claims.Subject),
		replayPurpose:     purpose,
		replayKey:         assertionReplayKey(segments, claims.JWTID),
		syntheticClientID: clientID,
	})
}

// validateAssertion verifies the assertion and refuses a cnf claim. It runs
// before the assertion is consumed.
func (h *JWTBearerHandler) validateAssertion(ctx context.Context, assertion string) (*ValidatedClaims, error) {
	claims, err := h.validator.ValidateJWTBearerAssertion(ctx, assertion, []string{h.tokenEndpoint})
	if err != nil {
		return nil, assertionValidationRefusal(err, "The JWT bearer assertion is invalid or could not be verified.")
	}
	// RFC 7800 cnf means the issuer expects proof of possession of a key.
	// Redeeming the assertion as a bearer token would silently drop that
	// binding, so refuse it until proof of possession (DPoP or mTLS) is
	// verified.
	if hasConfirmationClaim(claims) {
		return nil, invalidAssertion("The JWT bearer assertion must not carry a 'cnf' claim.")
	}
	return claims, nil
}

// checkAssertionMaxAge rejects assertions whose lifetime exceeds the policy maximum.
func checkAssertionMaxAge(claims *ValidatedClaims, policy *JWTBearerGrantPolicy) error {
	if claims.IssuedAt.IsZero() || claims.Expiry.IsZero() || claims.Expiry.Sub(claims.IssuedAt) > policy.maxAssertionAge {
		return invalidAssertion("The JWT bearer assertion exceeds the configured maximum age.")
	}
	return nil
}

// requestedResource returns the single resource parameter of the request. Each assertion profile
// chooses its resource, so this is the choice for plain RFC 7523 and other profiles may differ.
func requestedResource(form url.Values) (string, error) {
	resources, present := form["resource"]
	if !present || len(resources) != 1 || resources[0] == "" {
		return "", errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"Exactly one resource parameter is required for the JWT-bearer grant."))
	}
	if err := server.ValidateAudienceURI(resources[0]); err != nil {
		return "", errorsx.WithStack(err)
	}
	return resources[0], nil
}

var (
	errSubjectUnbound  = errors.New("assertion subject is not listed in subject_bindings")
	errResourceUnbound = errors.New("resource is not allowed for the assertion subject")
)

// checkSubjectBinding requires the assertion subject to be bound to the given
// resource. It returns errSubjectUnbound or errResourceUnbound; each handler
// maps them to its own response.
func checkSubjectBinding(claims *ValidatedClaims, policy *JWTBearerGrantPolicy, resource string) error {
	for _, binding := range policy.SubjectBindings {
		if binding.Subject == claims.Subject {
			if slices.Contains(binding.AllowedResources, resource) {
				return nil
			}
			return errResourceUnbound
		}
	}
	return errSubjectUnbound
}

// plainBindingRefusal maps a checkSubjectBinding error for the plain RFC 7523
// grant, which keeps invalid_grant for an unlisted subject and a bare
// invalid_target for a resource that is not allowed.
func plainBindingRefusal(err error) error {
	switch {
	case errors.Is(err, errResourceUnbound):
		return errorsx.WithStack(&fosite.RFC6749Error{ErrorField: "invalid_target", CodeField: http.StatusBadRequest})
	case errors.Is(err, errSubjectUnbound):
		return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint(
			"The JWT bearer assertion subject is not configured for this grant."))
	}
	return err
}

// idJAGBindingRefusal maps a checkSubjectBinding error for the ID-JAG path: a
// valid ID-JAG that the allowlist does not permit is access_denied, because a
// re-issued one will not help.
func idJAGBindingRefusal(err error) error {
	switch {
	case errors.Is(err, errResourceUnbound):
		return errorsx.WithStack(redemptionRefusal(fosite.ErrAccessDenied.ErrorField, reasonResourceDenied, ""))
	case errors.Is(err, errSubjectUnbound):
		return errorsx.WithStack(redemptionRefusal(fosite.ErrAccessDenied.ErrorField, reasonSubjectUnknown, ""))
	}
	return err
}

// PopulateTokenEndpointResponse issues only an access token.
func (h *JWTBearerHandler) PopulateTokenEndpointResponse(
	ctx context.Context, requester fosite.AccessRequester, responder fosite.AccessResponder,
) error {
	if !h.CanHandleTokenEndpointRequest(ctx, requester) {
		return errorsx.WithStack(fosite.ErrUnknownRequest)
	}
	return h.redeemer.populate(ctx, requester, responder)
}

func jwtBearerClientID(issuer, subject string) string {
	digest := sha256.Sum256([]byte(issuer + "\x00" + subject))
	return storage.SyntheticClientIDPrefix + "jwt-bearer-" + base64.RawURLEncoding.EncodeToString(digest[:])
}

// canonicalAssertionSegments rejects alternate encodings of a compact JWT before
// it reaches the validator. The decoded protected header and payload are reused
// for replay keying; the signature is checked for canonical form but not keyed.
func canonicalAssertionSegments(assertion string) ([2][]byte, error) {
	var replaySegments [2][]byte
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		return replaySegments, invalidAssertion("The assertion is not a valid signed JWT.")
	}
	for i, part := range parts {
		decoded, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != part {
			return replaySegments, invalidAssertion("The assertion is not a valid signed JWT.")
		}
		if i < len(replaySegments) {
			replaySegments[i] = decoded
		}
	}
	return replaySegments, nil
}

// assertionReplayKey uses jti when present; otherwise it hashes the decoded
// protected header and payload with a length-delimited header. Excluding the
// signature makes re-signings of the same assertion share a replay slot. The
// no-jti purpose keeps these keys separate from issuer-supplied jti values.
func assertionReplayKey(segments [2][]byte, jti string) string {
	if jti != "" {
		return jti
	}
	framed := binary.BigEndian.AppendUint64(nil, uint64(len(segments[0])))
	framed = append(framed, segments[0]...)
	framed = append(framed, segments[1]...)
	digest := sha256.Sum256(framed)
	return "jwt-bearer-noJTI-" + base64.RawURLEncoding.EncodeToString(digest[:])
}

func validateJWTBearerAssertionForm(form url.Values) (string, error) {
	assertions, ok := form["assertion"]
	if !ok || len(assertions) != 1 || assertions[0] == "" {
		return "", errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"The 'assertion' parameter is required exactly once for the JWT-bearer grant."))
	}
	return assertions[0], nil
}

func validateAssertionType(assertion string) error {
	parsed, err := jwt.ParseSigned(assertion, allowedSignatureAlgorithms)
	if err != nil {
		return invalidAssertion("The assertion is not a valid signed JWT.")
	}
	if len(parsed.Headers) != 1 {
		return invalidAssertion("The assertion must contain exactly one JOSE signature.")
	}
	typ, ok := parsed.Headers[0].ExtraHeaders[jose.HeaderType]
	if !ok {
		return nil
	}
	value, ok := typ.(string)
	if !ok {
		return invalidAssertion("The assertion JOSE typ header must be a string.")
	}
	// An ID-JAG is not accepted here. Routing sends every assertion whose typ
	// is exactly oauthproto.JWTTypeIDJAG to IDJAGHandler, so this handler never
	// sees one, and any other spelling of that type must stay refused: it has
	// not been through the client authentication IDJAGHandler requires.
	if value == "" || value == "JWT" {
		return nil
	}
	return invalidAssertion("The assertion JOSE typ header is not supported.")
}

// assertionType returns the raw JOSE typ header of the assertion, with no
// normalization. JWT-bearer and ID-JAG routing (CanHandleTokenEndpointRequest
// of both handlers, and CanSkipClientAuth) compare this value exactly to
// "oauth-id-jag+jwt", the spelling the ID-JAG draft requires of an issuer.
// RFC 7515 Section 4.1.9 would also treat a different case, or the
// "application/" prefix, as the same type, but redemption does not: such an
// assertion is not routed to IDJAGHandler, and validateAssertionType refuses it
// with invalid_grant. That narrowness is intended and fails closed. The deny
// side is the opposite: rejectIDJAGType and pkg/auth call idjag.IsIDJAG, which
// compares with oauthproto.IsJOSEType, so no spelling of the ID-JAG type passes
// as an access token. Revisit this if an issuer is found to emit a variant spelling.
func assertionType(assertion string) string {
	parsed, err := jwt.ParseSigned(assertion, allowedSignatureAlgorithms)
	if err != nil || len(parsed.Headers) != 1 {
		return ""
	}
	typ, _ := parsed.Headers[0].ExtraHeaders[jose.HeaderType].(string)
	return typ
}

// validateJWTBearerAssertionClaims validates the assertion's registered
// claims. acceptedAudiences is the set of "this AS" identity strings the
// assertion's "aud" must intersect — see JWTBearerGrantPolicy.AcceptedAudiences.
// "jti" is not required: many real-world IdPs (e.g. Microsoft Entra ID
// client_credentials tokens) never emit one. When absent, assertionReplayKey
// hashes the decoded protected header and payload for the replay check.
func validateJWTBearerAssertionClaims(claims jwt.Claims, issuer string, acceptedAudiences []string) error {
	if claims.Expiry == nil {
		return errors.New("assertion is missing required 'exp' claim")
	}
	if claims.IssuedAt == nil {
		return errors.New("assertion is missing required 'iat' claim")
	}
	if claims.Subject == "" {
		return errors.New("assertion is missing required 'sub' claim")
	}
	if !audienceIntersects(claims.Audience, acceptedAudiences) {
		return fmt.Errorf("assertion audience must include one of the accepted authorization server identities %q", acceptedAudiences)
	}
	if err := claims.ValidateWithLeeway(jwt.Expected{Issuer: issuer}, externalClockSkewLeeway); err != nil {
		return fmt.Errorf("assertion claims validation failed: %w", err)
	}
	if claims.Expiry.Time().Before(time.Now()) {
		return errors.New("assertion has expired")
	}
	return nil
}

// audienceIntersects reports whether any value in audience is a member of
// accepted. RFC 7523 §3 permits a multi-valued assertion "aud"; membership
// (not equality) is deliberate so the assertion can additionally name other
// audiences (e.g. an IdP-specific application ID) without losing its match
// against one of this AS's own accepted identities.
func audienceIntersects(audience jwt.Audience, accepted []string) bool {
	for _, value := range audience {
		if slices.Contains(accepted, value) {
			return true
		}
	}
	return false
}

// JWTBearerIssuanceFactory builds the production RFC 7523 handler. Like the
// ID-JAG handler it is registered whenever the grant is enabled, even when no
// trusted issuer accepts the jwt_bearer assertion type (every issuer does
// unless acceptedAssertionTypes excludes it): a plain assertion from an
// excluded issuer then gets the precise "issuer is not enabled for this grant"
// rejection instead of fosite's generic invalid_request.
//
// shared is used as the JWTBearerAssertionValidator and is REQUIRED whenever
// trustedIssuers is non-empty (an error is returned otherwise). The RFC 8693
// token-exchange Factory and this one are usually enabled for the same trusted
// issuers, and each MultiIssuerTokenValidator registers its own per-issuer
// jwkfetch.Cache and background refresh goroutines; sharing one instance avoids
// doubling that cost, and — since the validator's JWKS workers are released
// only by its Close — keeps them owned by the caller rather than built and
// abandoned inside this compose-time closure. Build it once with
// NewSharedTrustedIssuerValidator and hold it for Close.
func JWTBearerIssuanceFactory(trustedIssuers []TrustedIssuer, shared *MultiIssuerTokenValidator) (server.Factory, error) {
	return jwtBearerGrantFactory(trustedIssuers, shared,
		func(deps issuanceDeps) (any, error) { return newJWTBearerIssuanceHandler(deps) })
}

// jwtBearerGrantFactory is the shared core of JWTBearerIssuanceFactory and
// IDJAGIssuanceFactory: policy resolution, the shared-validator ownership
// requirement, and the compose-time dependency and audience checks are
// identical for both handlers — only the handler constructed at the end
// differs, supplied as build. Both factories are registered from the same
// issuer policies, so every check here runs once per factory; that is
// deliberate redundancy, not waste — each factory must stay independently
// safe to register alone.
func jwtBearerGrantFactory(
	trustedIssuers []TrustedIssuer,
	shared *MultiIssuerTokenValidator,
	build func(deps issuanceDeps) (any, error),
) (server.Factory, error) {
	resolvedIssuers, err := ResolveJWTBearerGrantPolicies(trustedIssuers)
	if err != nil {
		return nil, fmt.Errorf("JWT-bearer trusted issuers: %w", err)
	}
	// Unconditionally required (unlike the token-exchange Factory, which validly
	// supports self-issued-only): JWT-bearer issuance is meaningful only against
	// trusted issuers, so there is no no-issuer case that would legitimately
	// leave shared nil. Requiring it also keeps the validator's JWKS workers
	// owned by the caller for Close rather than built and abandoned here.
	if shared == nil {
		return nil, fmt.Errorf("JWT-bearer: a shared validator built via NewSharedTrustedIssuerValidator " +
			"is required so its JWKS refresh workers can be released on shutdown")
	}
	return func(config *server.AuthorizationServerConfig, rawStorage fosite.Storage, strategy any) (any, error) {
		consumer, err := assertionJWTConsumer(rawStorage)
		if err != nil {
			return nil, err
		}
		atStrategy, ok := strategy.(oauth2.AccessTokenStrategy)
		if !ok {
			return nil, fmt.Errorf("JWT-bearer strategy does not implement oauth2.AccessTokenStrategy (got %T)", strategy)
		}
		atStorage, ok := rawStorage.(oauth2.AccessTokenStorage)
		if !ok {
			return nil, fmt.Errorf("JWT-bearer storage does not implement oauth2.AccessTokenStorage (got %T)", rawStorage)
		}
		for _, issuer := range resolvedIssuers {
			if issuer.JWTBearerGrant == nil {
				continue
			}
			for _, binding := range issuer.JWTBearerGrant.SubjectBindings {
				for _, resource := range binding.AllowedResources {
					if !slices.Contains(config.AllowedAudiences, resource) {
						return nil, fmt.Errorf("JWT-bearer resource %q is not an allowed audience", resource)
					}
				}
			}
			// accepted_audiences identifies this authorization server, not a
			// resource; checked here (this is the runtime choke point every
			// JWTBearerIssuanceFactory call goes through) as well as inside
			// NewSharedTrustedIssuerValidator, so a caller that builds shared
			// separately is still covered.
			for _, audience := range issuer.JWTBearerGrant.AcceptedAudiences {
				if slices.Contains(config.AllowedAudiences, audience) {
					return nil, fmt.Errorf(
						"JWT-bearer accepted_audiences value %q must not also be a configured "+
							"resource audience (allowed_audiences)", audience)
				}
			}
		}
		// shared is guaranteed non-nil (checked when this factory was built), so
		// the validator is always the caller-owned, closeable one — this closure
		// never builds a MultiIssuerTokenValidator whose JWKS workers leak.
		var validator JWTBearerAssertionValidator = shared
		return build(issuanceDeps{
			validator:        validator,
			tokenEndpoint:    config.TokenURL,
			consumer:         consumer,
			config:           config.Config,
			strategy:         atStrategy,
			tokenStorage:     atStorage,
			trustedIssuers:   resolvedIssuers,
			allowedAudiences: config.AllowedAudiences,
		})
	}, nil
}

// assertionJWTConsumer resolves rawStorage's AssertionJWTConsumer capability.
// Every decorator in the actual composition chain must itself implement (and
// forward, one level down) AssertionJWTConsumer for this to succeed -- there
// is no automatic bypass via storage.Unwrap. That is deliberate: a decorator
// that sits between rawStorage and the innermost backend gets an explicit,
// visible opportunity to intercept or audit assertion-JWT consumption, rather
// than being silently skipped past. See CIMDStorageDecorator.ConsumeAssertionJWT
// (and the shape guard that NewCIMDShapeGuardStorage returns when CIMD is
// disabled), SPIFFEStorageDecorator.ConsumeAssertionJWT and
// ClientAdmissionStorage.ConsumeAssertionJWT for the production decorators
// that forward it today. A future decorator that omits this method
// breaks visibly, right here, with an error naming the offending type -- not
// silently, by having its ConsumeAssertionJWT logic (if any) never run.
func assertionJWTConsumer(rawStorage fosite.Storage) (storage.AssertionJWTConsumer, error) {
	consumer, ok := rawStorage.(storage.AssertionJWTConsumer)
	if !ok {
		return nil, fmt.Errorf("JWT-bearer storage %T does not implement storage.AssertionJWTConsumer", rawStorage)
	}
	return consumer, nil
}

var _ fosite.TokenEndpointHandler = (*JWTBearerHandler)(nil)
