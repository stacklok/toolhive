// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/x/errorsx"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	idJAGJWTType           = "oauth-id-jag+jwt"
	jwtBearerReplayPurpose = "jwt-bearer"
)

// JWTBearerAssertionValidator verifies cryptographic and registered claims of a
// plain RFC 7523 JWT-bearer assertion.
type JWTBearerAssertionValidator interface {
	ValidateJWTBearerAssertion(ctx context.Context, rawToken, tokenEndpoint string) (*ValidatedClaims, error)
}

// JWTBearerHandler implements the unbound RFC 7523 JWT-bearer grant.
type JWTBearerHandler struct {
	*oauth2.HandleHelper
	validator     JWTBearerAssertionValidator
	tokenEndpoint string
	consumer      storage.AssertionJWTConsumer
	config        tokenExchangeConfig
	policies      map[string]*JWTBearerGrantPolicy
}

// newJWTBearerHandler constructs the shared plain-assertion-matching core of
// JWTBearerHandler: CanHandleTokenEndpointRequest, CanSkipClientAuth, and
// request-form/assertion-type validation. It is not, by itself, safe to wire
// into a real token endpoint — it has no policy, replay-tracking, or
// issuance dependencies. Kept unexported because of that: the only
// production path is newJWTBearerIssuanceHandler, which always supplies
// them. In-package tests use it directly only to exercise this seam in
// isolation (see jwt_bearer_handler_test.go); HandleTokenEndpointRequest
// fails closed rather than succeeding when consumer/policies are unset (see
// below), so an accidental use of this constructor alone can never look like
// a working grant.
func newJWTBearerHandler(validator JWTBearerAssertionValidator, tokenEndpoint string) (*JWTBearerHandler, error) {
	if validator == nil {
		return nil, errors.New("JWT-bearer assertion validator must not be nil")
	}
	if tokenEndpoint == "" {
		return nil, errors.New("JWT-bearer token endpoint must not be empty")
	}
	endpoint, err := url.ParseRequestURI(tokenEndpoint)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return nil, fmt.Errorf("JWT-bearer token endpoint is invalid: %q", tokenEndpoint)
	}
	return &JWTBearerHandler{validator: validator, tokenEndpoint: tokenEndpoint}, nil
}

func newJWTBearerIssuanceHandler(
	validator JWTBearerAssertionValidator, tokenEndpoint string, consumer storage.AssertionJWTConsumer,
	config *fosite.Config, strategy oauth2.AccessTokenStrategy, tokenStorage oauth2.AccessTokenStorage,
	trustedIssuers []TrustedIssuer,
) (*JWTBearerHandler, error) {
	handler, err := newJWTBearerHandler(validator, tokenEndpoint)
	if err != nil {
		return nil, err
	}
	if consumer == nil {
		return nil, errors.New("JWT-bearer storage must implement storage.AssertionJWTConsumer")
	}
	if config == nil || strategy == nil || tokenStorage == nil {
		return nil, errors.New("JWT-bearer issuance dependencies must not be nil")
	}
	policies := make(map[string]*JWTBearerGrantPolicy)
	for _, issuer := range trustedIssuers {
		if issuer.JWTBearerGrant != nil {
			policies[issuer.IssuerURL] = issuer.JWTBearerGrant
		}
	}
	if len(policies) == 0 {
		return nil, errors.New("JWT-bearer issuance requires at least one enabled trusted issuer")
	}
	handler.HandleHelper = &oauth2.HandleHelper{
		AccessTokenStrategy: strategy,
		AccessTokenStorage:  tokenStorage,
		Config:              config,
	}
	handler.consumer = consumer
	handler.config = config
	handler.policies = policies
	return handler, nil
}

// CanHandleTokenEndpointRequest only claims plain assertions. A recognized ID-JAG
// assertion is intentionally left for a future bound handler; malformed and
// unsupported typ values remain this handler's responsibility to reject.
func (*JWTBearerHandler) CanHandleTokenEndpointRequest(_ context.Context, requester fosite.AccessRequester) bool {
	return requester.GetGrantTypes().ExactOne(oauthproto.GrantTypeJWTBearer) &&
		assertionType(requester.GetRequestForm().Get("assertion")) != idJAGJWTType
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
	if assertionType(form.Get("assertion")) == idJAGJWTType ||
		form.Get("client_id") != "" ||
		form.Get("client_secret") != "" ||
		form.Get("client_assertion") != "" ||
		form.Get("client_assertion_type") != "" {
		return false
	}
	req, ok := ctx.Value(fosite.RequestContextKey).(*http.Request)
	if !ok || req == nil {
		return false
	}
	if _, _, ok := req.BasicAuth(); ok {
		return false
	}
	return true
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
	if err := validateAssertionType(assertion); err != nil {
		return err
	}
	claims, err := h.validator.ValidateJWTBearerAssertion(ctx, assertion, h.tokenEndpoint)
	if err != nil {
		return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint("The JWT bearer assertion is invalid or could not be verified."))
	}
	// h.policies is nil when this handler was built by newJWTBearerHandler
	// directly rather than newJWTBearerIssuanceHandler (the seam tests use it
	// this way) — a nil map lookup below always misses, so this request is
	// rejected rather than treated as authorized. Only newJWTBearerIssuanceHandler
	// is wired into a real token endpoint; see its doc comment.
	policy, ok := h.policies[claims.Issuer]
	if !ok {
		return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint("The JWT bearer assertion issuer is not enabled for this grant."))
	}
	if h.consumer == nil {
		return errorsx.WithStack(fosite.ErrServerError.WithHint("The JWT-bearer grant is not fully configured."))
	}
	resource, err := validateJWTBearerPolicy(requester.GetRequestForm(), claims, policy)
	if err != nil {
		return err
	}
	// Consume before issuing. This intentionally fails closed: if issuance fails
	// after this point, the assertion remains consumed rather than becoming
	// replayable.
	replayKey := assertionReplayKey(assertion, claims.JWTID)
	if err := h.consumer.ConsumeAssertionJWT(ctx, jwtBearerReplayPurpose, claims.Issuer, replayKey, claims.Expiry); err != nil {
		if errors.Is(err, fosite.ErrJTIKnown) {
			return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint("The JWT bearer assertion has already been used."))
		}
		// A non-ErrJTIKnown failure here is a storage/operational problem (e.g. a
		// replay-store outage), not evidence the caller sent a bad assertion —
		// it must not be indistinguishable from a genuine invalid_grant.
		return errorsx.WithStack(fosite.ErrServerError.WithHint("The JWT bearer assertion could not be consumed.").
			WithWrap(err).WithDebug(err.Error()))
	}
	remaining := time.Until(claims.Expiry)
	if remaining <= 0 {
		return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint("The JWT bearer assertion has expired."))
	}
	lifetime := h.config.GetAccessTokenLifespan(ctx)
	if remaining < lifetime {
		lifetime = remaining
	}
	clientID := jwtBearerClientID(claims.Issuer, claims.Subject)
	issuedSession := session.New(claims.Issuer+"#"+claims.Subject, "", clientID, session.UserClaims{})
	issuedSession.SetExpiresAt(fosite.AccessToken, time.Now().UTC().Add(lifetime))
	// This grant skips client authentication (CanSkipClientAuth), so fosite
	// never populates requester's client — attach a synthetic one so every
	// storage backend's marshal path (which calls GetClient unconditionally)
	// has a real, non-nil client to serialize.
	accessRequest, ok := requester.(*fosite.AccessRequest)
	if !ok {
		return errorsx.WithStack(fosite.ErrServerError.WithHint("The JWT-bearer grant requires a *fosite.AccessRequest."))
	}
	// Always replace any resolved caller client. Fosite accepts a supplied
	// public client_id without a secret; retaining it could renew a DCR client
	// registration TTL after this clientless grant succeeds.
	accessRequest.Client = storage.NewSyntheticClient(clientID)
	requester.GrantAudience(resource)
	requester.SetSession(issuedSession)
	return nil
}

func validateJWTBearerPolicy(form url.Values, claims *ValidatedClaims, policy *JWTBearerGrantPolicy) (string, error) {
	if claims.IssuedAt.IsZero() || claims.Expiry.IsZero() || claims.Expiry.Sub(claims.IssuedAt) > policy.maxAssertionAge {
		return "", errorsx.WithStack(fosite.ErrInvalidGrant.WithHint("The JWT bearer assertion exceeds the configured maximum age."))
	}
	resources, present := form["resource"]
	if !present || len(resources) != 1 || resources[0] == "" {
		return "", errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"Exactly one resource parameter is required for the JWT-bearer grant."))
	}
	if err := server.ValidateAudienceURI(resources[0]); err != nil {
		return "", errorsx.WithStack(err)
	}
	for _, binding := range policy.SubjectBindings {
		if binding.Subject == claims.Subject {
			for _, resource := range binding.AllowedResources {
				if resource == resources[0] {
					return resource, nil
				}
			}
			return "", errorsx.WithStack(&fosite.RFC6749Error{
				ErrorField: "invalid_target",
				CodeField:  http.StatusBadRequest,
			})
		}
	}
	return "", errorsx.WithStack(fosite.ErrInvalidGrant.WithHint(
		"The JWT bearer assertion subject is not configured for this grant."))
}

// PopulateTokenEndpointResponse issues only an access token.
func (h *JWTBearerHandler) PopulateTokenEndpointResponse(
	ctx context.Context, requester fosite.AccessRequester, responder fosite.AccessResponder,
) error {
	if !h.CanHandleTokenEndpointRequest(ctx, requester) {
		return errorsx.WithStack(fosite.ErrUnknownRequest)
	}
	if h.HandleHelper == nil {
		return errorsx.WithStack(fosite.ErrInvalidRequest.WithHint("JWT-bearer token issuance is not configured."))
	}
	lifetime := h.config.GetAccessTokenLifespan(ctx)
	if expiry := requester.GetSession().GetExpiresAt(fosite.AccessToken); !expiry.IsZero() {
		remaining := time.Until(expiry)
		if remaining <= 0 {
			return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint("The JWT bearer assertion has expired."))
		}
		if remaining < lifetime {
			lifetime = remaining
		}
	}
	_, err := h.IssueAccessToken(ctx, lifetime, requester, responder)
	return err
}

func jwtBearerClientID(issuer, subject string) string {
	digest := sha256.Sum256([]byte(issuer + "\x00" + subject))
	return storage.SyntheticClientIDPrefix + "jwt-bearer-" + base64.RawURLEncoding.EncodeToString(digest[:])
}

// assertionReplayKey returns the value used to key the single-use replay
// check for a JWT-bearer assertion. jti is used when the issuer supplied
// one; otherwise a hash of the raw assertion JWT stands in for it — the
// raw assertion (including its signature bytes) is itself guaranteed
// unique per assertion, so it is an equally valid single-use key. The
// "noJTI" prefix keeps these synthetic keys visually distinct from real
// jti values and guarantees they can never collide with one.
func assertionReplayKey(rawAssertion, jti string) string {
	if jti != "" {
		return jti
	}
	digest := sha256.Sum256([]byte(rawAssertion))
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
		return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint("The assertion is not a valid signed JWT."))
	}
	if len(parsed.Headers) != 1 {
		return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint("The assertion must contain exactly one JOSE signature."))
	}
	typ, ok := parsed.Headers[0].ExtraHeaders[jose.HeaderType]
	if !ok {
		return nil
	}
	value, ok := typ.(string)
	if !ok {
		return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint("The assertion JOSE typ header must be a string."))
	}
	if value == "" || value == "JWT" || value == idJAGJWTType {
		return nil
	}
	return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint("The assertion JOSE typ header is not supported."))
}

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
// falls back to hashing the raw assertion for the replay check instead.
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

// JWTBearerIssuanceFactory builds the production RFC 7523 handler. It is only
// registered by composition when a trusted issuer opts into the grant.
//
// shared, when non-nil, is used as the JWTBearerAssertionValidator instead of
// building a second MultiIssuerTokenValidator: the RFC 8693 token-exchange
// Factory and this one are usually enabled for the same trusted issuers, and
// each MultiIssuerTokenValidator registers its own per-issuer jwk.Cache and
// background refresh goroutines, so building one from each factory would
// double that cost for no benefit. Pass nil to build one locally (e.g. when
// only the JWT-bearer grant is enabled).
func JWTBearerIssuanceFactory(trustedIssuers []TrustedIssuer, shared *MultiIssuerTokenValidator) (server.Factory, error) {
	resolvedIssuers, err := ResolveJWTBearerGrantPolicies(trustedIssuers)
	if err != nil {
		return nil, fmt.Errorf("JWT-bearer trusted issuers: %w", err)
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
			// resource; checked here too (not only inside
			// NewMultiIssuerTokenValidator below) because that constructor is
			// skipped entirely when shared is non-nil — this is the runtime
			// choke point every JWTBearerIssuanceFactory call goes through.
			for _, audience := range issuer.JWTBearerGrant.AcceptedAudiences {
				if slices.Contains(config.AllowedAudiences, audience) {
					return nil, fmt.Errorf(
						"JWT-bearer accepted_audiences value %q must not also be a configured "+
							"resource audience (allowed_audiences)", audience)
				}
			}
		}
		var validator JWTBearerAssertionValidator = shared
		if shared == nil {
			selfValidator, err := NewSelfIssuedTokenValidator(config.PublicJWKS(), config.GetAccessTokenIssuer(), config.AllowedAudiences)
			if err != nil {
				return nil, fmt.Errorf("JWT-bearer: failed to create self validator: %w", err)
			}
			validator, err = NewMultiIssuerTokenValidator(
				selfValidator, config.GetAccessTokenIssuer(), resolvedIssuers, config.AllowedAudiences)
			if err != nil {
				return nil, fmt.Errorf("JWT-bearer: trusted_issuers: %w", err)
			}
		}
		return newJWTBearerIssuanceHandler(validator, config.TokenURL, consumer, config.Config, atStrategy, atStorage, resolvedIssuers)
	}, nil
}

// assertionJWTConsumer resolves rawStorage's AssertionJWTConsumer capability.
// Every decorator in the actual composition chain must itself implement (and
// forward, one level down) AssertionJWTConsumer for this to succeed -- there
// is no automatic bypass via storage.Unwrap. That is deliberate: a decorator
// that sits between rawStorage and the innermost backend gets an explicit,
// visible opportunity to intercept or audit assertion-JWT consumption, rather
// than being silently skipped past. See CIMDStorageDecorator.ConsumeAssertionJWT
// and SPIFFEStorageDecorator.ConsumeAssertionJWT for the two production
// decorators that forward it today. A future decorator that omits this method
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
