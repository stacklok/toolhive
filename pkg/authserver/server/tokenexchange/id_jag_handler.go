// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"slices"
	"sync"

	"github.com/ory/fosite"
	"github.com/ory/x/errorsx"

	"github.com/stacklok/toolhive/pkg/authserver/actor"
	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/subject"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	"github.com/stacklok/toolhive/pkg/oauthproto/idjag"
)

// idJAGReplayPurpose namespaces ID-JAG replay keys apart from plain
// JWT-bearer assertions in storage.AssertionJWTConsumer. The two handlers
// split one grant type by assertion typ, so a given assertion can only ever
// be consumed under one purpose; the separate namespace just keeps that true
// by construction rather than by the typ split alone.
const idJAGReplayPurpose = "id-jag"

// IDJAGHandler implements the bound RFC 7523 JWT-bearer grant for Identity
// Assertion Authorization Grant assertions
// (draft-ietf-oauth-identity-assertion-authz-grant, "ID-JAG" — the inbound
// half of Cross App Access; the requesting half is
// pkg/vmcp/auth/strategies/xaa.go). It claims exactly the assertions
// JWTBearerHandler declines: those whose JOSE typ header is
// "oauth-id-jag+jwt" (oauthproto.JWTTypeIDJAG).
//
// "Bound" is the trust-model difference from the plain handler. An ID-JAG
// names the OAuth client that may redeem it — the IdP mints the draft
// §4.4.1 client_id claim from its administrator-configured resource
// connection — so this handler requires the authenticated client to be that
// client. Only a confidential client that authenticated can redeem. Public
// clients, and requests with no client credentials, are refused with
// invalid_client before the assertion is validated: draft §4.4 requires client
// authentication or a sender-constrained ID-JAG, and this server cannot yet
// verify a sender-constrained one (proof of possession, DPoP or mTLS). A
// client_id alone is not authentication, and the assertion's single-use,
// short-lived, audience-pinned form only decides who redeems a leaked copy
// first. The plain handler's synthetic client is not needed — the redeeming
// client is a real registered one.
//
// The redeeming client's registered grant_types metadata is deliberately not
// consulted, except for the operator-configured clients that set grant_types:
// with open dynamic client registration, self-asserted metadata authorizes
// nothing, but a configured client's grants are operator policy. What
// authorizes the grant is the per-issuer policy plus the client_id binding the
// IdP administrator configured. A configured client that omits grant_types is
// stored with token exchange alone but is not gated, for backward
// compatibility; one that sets grant_types must list the jwt-bearer grant.
type IDJAGHandler struct {
	validator     JWTBearerAssertionValidator
	tokenEndpoint string

	// policies holds the issuers whose policy accepts ID-JAG assertions.
	policies map[string]*JWTBearerGrantPolicy

	// actorPolicies is keyed by issuer URL. A namespaced issuer has no entry
	// and its actor is not checked.
	actorPolicies map[string]actor.Policy

	// redeemer supplies the shared consume, lifetime, session and populate
	// steps. It is a named field, not an embedded type, so no
	// fosite.TokenEndpointHandler method is ever promoted from it.
	redeemer *redeemer

	// explicitGrantClients holds the IDs of clients that must list the
	// jwt-bearer grant in their registered grant types.
	explicitGrantClients map[string]struct{}

	// issuer supplies this authorization server's issuer identifier, the same
	// value the server's own token validator uses (fosite.Config
	// AccessTokenIssuer).
	issuer fosite.AccessTokenIssuerProvider

	// audienceStrategy matches a requested resource against a client's
	// registered audiences.
	audienceStrategy fosite.AudienceStrategyProvider

	// issuerIsResource is set once at construction when the issuer identifier
	// is also one of the server's resource audiences (allowed_audiences). The
	// issuer then stays out of the default accepted audiences, because an
	// access token minted for that resource would otherwise be redeemable as
	// an assertion, and audience_not_issuer is not logged, because no IdP
	// could fix it. It is a bool, not a copy of the issuer, so the issuer
	// provider above remains the only source of the issuer value.
	issuerIsResource bool

	// governedResources is the set of resources this server issues tokens for
	// (allowed_audiences). Every value of an ID-JAG resource claim must be in
	// it; an empty set refuses every claim.
	governedResources []string

	// resolver resolves the subject of a resolve issuer's ID-JAG. It decides
	// which upstream_subject issuers and claims resolve. Only
	// IDJAGIssuanceFactory sets it; it is nil for a handler built directly with
	// newIDJAGIssuanceHandler.
	resolver subject.Resolver

	// detailsRegistry holds the authorization_details types this server
	// processes in an ID-JAG's claim. Only IDJAGIssuanceFactory sets it. When
	// nil, an ID-JAG that carries the claim is refused with unknown_type.
	detailsRegistry *authorizationdetails.Registry

	// logger receives the draft-departure lines; nil means slog.Default().
	logger *slog.Logger

	// warned records the (assertion issuer, finding) pairs already logged at
	// Warn. Its size is bounded by the trusted issuers times the findings.
	warned sync.Map
}

// draftDeparture names a way an accepted ID-JAG departs from draft-ietf-oauth-
// identity-assertion-authz-grant-06 Section 4.4.1.
type draftDeparture string

const (
	departureAudienceNotIssuer draftDeparture = "audience_not_issuer"
)

// IssuersAcceptingAssertionType returns the subset of trustedIssuers whose
// JWTBearerGrant policy accepts assertionType. An issuer with no
// AcceptedAssertionTypes configured (nil/empty) accepts both types.
//
// This is the single source of truth for "which issuers accept this
// assertion type": it builds each handler's policy map.
func IssuersAcceptingAssertionType(trustedIssuers []TrustedIssuer, assertionType JWTBearerAssertionType) []TrustedIssuer {
	accepted := make([]TrustedIssuer, 0, len(trustedIssuers))
	for _, issuer := range trustedIssuers {
		if issuer.JWTBearerGrant == nil {
			continue
		}
		if slices.Contains(effectiveAssertionTypes(issuer.JWTBearerGrant), assertionType) {
			accepted = append(accepted, issuer)
		}
	}
	return accepted
}

// newIDJAGIssuanceHandler constructs the production bound handler. See
// issuanceDeps.validate for the dependency requirements.
func newIDJAGIssuanceHandler(
	deps issuanceDeps, explicitGrantClientIDs []string, logger *slog.Logger,
) (*IDJAGHandler, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	explicitGrantClients := make(map[string]struct{}, len(explicitGrantClientIDs))
	for _, id := range explicitGrantClientIDs {
		explicitGrantClients[id] = struct{}{}
	}
	h := &IDJAGHandler{
		validator:     deps.validator,
		tokenEndpoint: deps.tokenEndpoint,
		// AcceptedAssertionTypes may exclude the ID-JAG type. Filtering here,
		// rather than in HandleTokenEndpointRequest, is the entire enforcement
		// mechanism: an issuer left out of the policy map is indistinguishable
		// from an issuer never configured at all, so the existing "issuer not
		// enabled for this grant" rejection already covers issuers that exclude
		// the ID-JAG type. An empty result is valid: every assertion of that
		// form is then rejected as not enabled.
		policies:             deps.policiesFor(JWTBearerAssertionTypeIDJAG),
		redeemer:             newRedeemer(deps),
		explicitGrantClients: explicitGrantClients,
		issuer:               deps.config,
		audienceStrategy:     deps.config,
		governedResources:    slices.Clone(deps.allowedAudiences),
		logger:               logger,
	}
	actorPolicies, err := newActorPolicies(h.policies)
	if err != nil {
		return nil, err
	}
	h.actorPolicies = actorPolicies
	asIssuer := deps.config.GetAccessTokenIssuer(context.Background())
	h.issuerIsResource = asIssuer != "" && slices.Contains(deps.allowedAudiences, asIssuer)
	// Reads the unfiltered issuer list on purpose: the warning covers every
	// trusted issuer, not only those that accept id_jag.
	if h.issuerIsResource && slices.ContainsFunc(deps.trustedIssuers, func(ti TrustedIssuer) bool {
		return ti.JWTBearerGrant != nil && len(ti.JWTBearerGrant.AcceptedAudiences) == 0
	}) {
		if logger == nil {
			logger = slog.Default()
		}
		logger.Warn("The issuer identifier is also a resource audience (allowed_audiences), "+
			"so ID-JAGs with aud set to the issuer are not accepted by default; "+
			"have the IdP use the token endpoint as aud, or stop listing the issuer in allowed_audiences",
			"issuer", asIssuer)
	}
	warnSubjectResolutionDisabled(logger, h.policies)
	return h, nil
}

// CanHandleTokenEndpointRequest claims only recognized ID-JAG assertions —
// the exact complement of JWTBearerHandler's matcher, so for any given
// jwt-bearer request at most one of the two handlers is responsible.
func (*IDJAGHandler) CanHandleTokenEndpointRequest(_ context.Context, requester fosite.AccessRequester) bool {
	return requester.GetGrantTypes().ExactOne(oauthproto.GrantTypeJWTBearer) &&
		assertionType(requester.GetRequestForm().Get("assertion")) == oauthproto.JWTTypeIDJAG
}

// CanSkipClientAuth permits skipping client authentication only for a request
// that carries no client credentials at all. fosite then gives the handler its
// placeholder client with an empty ID, and HandleTokenEndpointRequest refuses
// it with invalid_client, instead of fosite's invalid_request. A request that
// carries any credentials, or a client_id alone, is authenticated or
// identified by fosite first, and a failure is returned to the client. Public
// clients are refused by HandleTokenEndpointRequest (draft §4.4), so a
// client_id alone never redeems an ID-JAG.
func (*IDJAGHandler) CanSkipClientAuth(ctx context.Context, requester fosite.AccessRequester) bool {
	return !mayCarryClientCredentials(ctx, requester.GetRequestForm())
}

// HandleTokenEndpointRequest validates the ID-JAG assertion, its client
// binding, and per-issuer policy, then prepares a bounded access-token
// session. After the subject-mode gate, a resolve issuer's subject is resolved
// to a local user.
func (h *IDJAGHandler) HandleTokenEndpointRequest(ctx context.Context, requester fosite.AccessRequester) error {
	if !h.CanHandleTokenEndpointRequest(ctx, requester) {
		return errorsx.WithStack(fosite.ErrUnknownRequest)
	}
	// First, so a caller that did not authenticate learns nothing about the
	// assertion: no validation and no consumption. Only the unverified JOSE
	// typ header was read, for routing.
	if err := h.checkRequestClient(ctx, requester); err != nil {
		return err
	}
	assertion, err := validateJWTBearerAssertionForm(requester.GetRequestForm())
	if err != nil {
		return err
	}
	claims, err := h.validateAssertion(ctx, assertion)
	if err != nil {
		return err
	}
	policy, err := h.enabledPolicy(claims)
	if err != nil {
		return err
	}
	// Unlike a plain RFC 7523 assertion (where many IdPs omit jti and the
	// replay key falls back to an assertion hash), the ID-JAG draft requires
	// jti, and it is what makes the assertion single-use. Absence means the
	// token is not a conforming ID-JAG; reject rather than fall back.
	if claims.JWTID == "" {
		return invalidAssertion("The ID-JAG assertion must carry a 'jti' claim.")
	}
	if err := h.checkClientBinding(requester, claims); err != nil {
		return err
	}
	resource, err := h.checkIDJAGPolicy(requester, claims, policy)
	if err != nil {
		return err
	}
	if err := h.checkClientAudience(ctx, requester, resource); err != nil {
		return err
	}
	if err := h.checkActorIssuer(ctx, claims, policy, requester.GetClient().GetID()); err != nil {
		return err
	}
	// Closed until delegated access is enabled; see delegatedAccessEnabled.
	// Today resolution and the actor policy, which runs after it, are tested
	// through idJAGRedemption and redeemer.complete; full-handler tests with
	// the gate open belong with the change that opens it.
	if err := checkSubjectModeGate(policy); err != nil {
		return err
	}
	if err := h.completeRedemption(ctx, requester, claims, policy, resource); err != nil {
		return err
	}
	h.logDraftDepartures(ctx, claims, policy, requester.GetClient().GetID())
	return nil
}

// checkRequestClient refuses, in order, a client that did not authenticate,
// an explicit grant client not registered for the grant, and a request that
// sends the authorization_details parameter. The parameter refusal's WARN line
// names the authenticated client.
func (h *IDJAGHandler) checkRequestClient(ctx context.Context, requester fosite.AccessRequester) error {
	if err := checkClientAuthenticated(requester.GetClient()); err != nil {
		return err
	}
	if err := h.checkClientGrant(requester); err != nil {
		return err
	}
	// The client authenticated, so every refusal is logged at Warn.
	return refuseDetailsParameter(ctx, requester.GetRequestForm(), requester.GetClient().GetID(),
		func() slog.Level { return slog.LevelWarn })
}

// completeRedemption decides the subject, then processes the ID-JAG's
// authorization_details claim, then consumes the assertion and installs the
// session. The claim is processed after subject resolution, so its hooks see
// the resolved user, and before consumption, so a refusal leaves the ID-JAG
// unconsumed.
func (h *IDJAGHandler) completeRedemption(
	ctx context.Context, requester fosite.AccessRequester, claims *ValidatedClaims,
	policy *JWTBearerGrantPolicy, resource string,
) error {
	clientID := requester.GetClient().GetID()
	d, err := h.idJAGRedemption(ctx, claims, policy, requester.GetClient(), resource)
	if err != nil {
		return err
	}
	// Only a resolved redemption sets delegatedAccess, and its subject is then
	// the resolved local user.
	resolvedUser := ""
	if d.delegatedAccess {
		resolvedUser = d.subject
	}
	if d.authorizationDetails, err = h.grantSignedDetails(ctx, claims, clientID, resource, resolvedUser); err != nil {
		return err
	}
	return h.redeemer.complete(ctx, requester, d)
}

// PopulateTokenEndpointResponse issues only an access token, through the
// redeemer it shares with the plain handler.
func (h *IDJAGHandler) PopulateTokenEndpointResponse(
	ctx context.Context, requester fosite.AccessRequester, responder fosite.AccessResponder,
) error {
	if !h.CanHandleTokenEndpointRequest(ctx, requester) {
		return errorsx.WithStack(fosite.ErrUnknownRequest)
	}
	return h.redeemer.populate(ctx, requester, responder)
}

// checkIDJAGPolicy runs the per-issuer policy checks in the same order as
// the plain handler (assertion age, then resource, then subject binding) and
// returns the resource the token is for. The subject binding check is for
// namespaced issuers; a resolve issuer has no bindings.
func (h *IDJAGHandler) checkIDJAGPolicy(
	requester fosite.AccessRequester, claims *ValidatedClaims, policy *JWTBearerGrantPolicy,
) (string, error) {
	if err := checkAssertionMaxAge(claims, policy); err != nil {
		return "", err
	}
	resource, err := h.idJAGResource(requester.GetRequestForm(), claims)
	if err != nil {
		return "", err
	}
	// A resolve issuer has no subject bindings, so nothing here narrows the
	// resource per subject. The resource is bounded by the resource claim
	// (governed resources) and by checkClientAudience. checkClientAudience does
	// not narrow a DCR or CIMD client that is not resource-scoped, so such a
	// client is refused when the subject is resolved, after the gate, in
	// idJAGRedemption.
	if policy.SubjectMode == SubjectModeResolve {
		return resource, nil
	}
	if err := checkSubjectBinding(claims, policy, resource); err != nil {
		return "", idJAGBindingRefusal(err)
	}
	return resource, nil
}

// checkClientAudience refuses a valid ID-JAG whose resource the client may
// not target. The resource may come from the request or from the assertion's
// resource claim. Draft -06 section 4.4.1 requires access_denied here, and the
// description does not echo the resource.
func (h *IDJAGHandler) checkClientAudience(
	ctx context.Context, requester fosite.AccessRequester, resource string,
) error {
	err := server.ValidateIDJAGClientAudience(resource, requester.GetClient(), h.audienceStrategy.GetAudienceStrategy(ctx))
	if errors.Is(err, server.ErrInvalidTarget) {
		return errorsx.WithStack(redemptionRefusal(fosite.ErrAccessDenied.ErrorField, reasonResourceDenied, ""))
	}
	return err
}

// idJAGResource picks the resource an ID-JAG redemption is for. Draft section
// 4.4.1 requires every resource claim value to be a resource this server
// governs, compared as exact strings (section 4.3), and restricts the access
// token to the resources in the claim. So a malformed claim, or one naming a
// resource outside allowed_audiences, is refused as an invalid assertion, and
// a requested resource the claim does not list is refused as invalid_target.
// Draft section 4.4 defines no resource parameter for redemption, so a client
// may leave it out: a claim with a single value names the target without
// ambiguity and is used as the default. Every other case keeps the rule of
// requestedResource: if the parameter is present in any form (empty or
// repeated too) it decides, and without a usable single-value claim the
// request is refused exactly as before.
func (h *IDJAGHandler) idJAGResource(form url.Values, claims *ValidatedClaims) (string, error) {
	claim, claimPresent := claims.Extra["resource"]
	var values []string
	if claimPresent {
		var wellFormed bool
		if values, wellFormed = idjag.ResourceClaim(claim); !wellFormed {
			return "", invalidAssertion("The ID-JAG assertion resource claim is malformed.")
		}
		// Every governed resource passed ValidateAudienceURI at startup, so
		// membership alone also refuses a value that is not a valid URI. The
		// description never quotes a claim value.
		for _, v := range values {
			if !slices.Contains(h.governedResources, v) {
				return "", invalidAssertion(
					"The ID-JAG assertion resource claim names a resource this server does not govern.")
			}
		}
	}
	if _, present := form["resource"]; present {
		resource, err := requestedResource(form)
		if err != nil {
			return "", err
		}
		if claimPresent && !slices.Contains(values, resource) {
			// Bare invalid_target with no reason code, on purpose: the draft
			// defines no resource parameter at redemption, so RFC 8707's
			// invalid_target applies, and the description must not name the
			// resource.
			return "", errorsx.WithStack(server.ErrInvalidTarget)
		}
		return resource, nil
	}
	if !claimPresent || len(values) != 1 {
		return requestedResource(form)
	}
	return values[0], nil
}

// logDraftDepartures reports an ID-JAG that departs from the draft. It runs
// once the request has passed validation (issuance can still fail later in
// Populate) and changes nothing about what is accepted. The first occurrence
// of each (assertion issuer, finding) pair is logged at Warn and later ones
// at Debug. The lines carry no subject, jti or claim values.
func (h *IDJAGHandler) logDraftDepartures(
	ctx context.Context, claims *ValidatedClaims, policy *JWTBearerGrantPolicy, clientID string,
) {
	var found []draftDeparture
	// An issuer that is also a resource audience can never be an accepted aud:
	// it is left out of the defaults, and accepted_audiences must not overlap
	// allowed_audiences. No IdP could fix the finding, and the startup warning
	// in newIDJAGIssuanceHandler already reports the configuration.
	asIssuer := h.issuer.GetAccessTokenIssuer(ctx)
	if asIssuer != "" && !h.issuerIsResource && !slices.Contains(claims.Audience, asIssuer) {
		found = append(found, departureAudienceNotIssuer)
	}
	if len(found) == 0 {
		return
	}
	logger := h.logger
	if logger == nil {
		logger = slog.Default()
	}
	for _, finding := range found {
		level := slog.LevelDebug
		if _, seen := h.warned.LoadOrStore([2]string{claims.Issuer, string(finding)}, struct{}{}); !seen {
			level = slog.LevelWarn
		}
		attrs := []any{"issuer", claims.Issuer, "client_id", clientID, "finding", string(finding)}
		if finding == departureAudienceNotIssuer {
			attrs = append(attrs, "hint", audienceHint(policy, asIssuer))
		}
		logger.Log(ctx, level, "ID-JAG departs from the draft; accepted for compatibility", attrs...)
	}
}

// audienceHint tells the operator how to stop audience_not_issuer.
func audienceHint(policy *JWTBearerGrantPolicy, asIssuer string) string {
	switch {
	case len(policy.AcceptedAudiences) == 0:
		return "this server's issuer identifier is accepted by default; have the IdP use it as aud"
	case !slices.Contains(policy.AcceptedAudiences, asIssuer):
		return "add this server's issuer identifier to jwt_bearer_grant.accepted_audiences"
	default:
		return "have the IdP use this server's issuer identifier as aud"
	}
}

// defaultAudiences is what an ID-JAG's aud may name when the issuer's policy
// configures no accepted_audiences. Draft section 4.4.1 makes the resource
// AS's issuer identifier the aud, so it is accepted along with the token
// endpoint. This is safe because an ID-JAG is already refused as an RFC 8693
// subject token and as an access token (typ checks), a plain JWT with aud =
// issuer goes to the plain handler, which still accepts only the token
// endpoint, and the issuer and token endpoint both name this server. The
// issuer is left out when it is also a resource audience (issuerIsResource).
func (h *IDJAGHandler) defaultAudiences(ctx context.Context) []string {
	audiences := []string{h.tokenEndpoint}
	if issuer := h.issuer.GetAccessTokenIssuer(ctx); issuer != "" && !h.issuerIsResource {
		audiences = append(audiences, issuer)
	}
	return audiences
}

// validateAssertion verifies the assertion and refuses an aud array with more
// than one element and a cnf claim, for every issuer. It runs before the
// assertion is consumed.
func (h *IDJAGHandler) validateAssertion(ctx context.Context, assertion string) (*ValidatedClaims, error) {
	claims, err := h.validator.ValidateJWTBearerAssertion(ctx, assertion, h.defaultAudiences(ctx))
	if err != nil {
		return nil, assertionValidationRefusal(err, "The ID-JAG assertion is invalid or could not be verified.")
	}
	if len(claims.Audience) > 1 {
		return nil, invalidAssertion("The ID-JAG assertion aud must name exactly one audience.")
	}
	// Draft section 9.11 says a server that cannot verify the confirmation
	// method of an ID-JAG with a cnf claim MUST refuse it. This server cannot
	// verify proof of possession yet, so accepting the ID-JAG would silently
	// drop the key binding the IdP asked for. The refusal stays until proof of
	// possession (DPoP or mTLS) is verified.
	if restrictionsOf(claims).Confirmation {
		return nil, invalidAssertion("The ID-JAG assertion must not carry a 'cnf' claim.")
	}
	return claims, nil
}

// checkClientGrant refuses an explicit-grant client whose registered grant types
// do not include jwt-bearer.
func (h *IDJAGHandler) checkClientGrant(requester fosite.AccessRequester) error {
	client := requester.GetClient()
	if _, explicit := h.explicitGrantClients[client.GetID()]; !explicit {
		return nil
	}
	if !slices.Contains(client.GetGrantTypes(), oauthproto.GrantTypeJWTBearer) {
		return errorsx.WithStack(fosite.ErrUnauthorizedClient.WithHint("The client is not registered for the jwt-bearer grant."))
	}
	return nil
}

// checkClientAuthenticated refuses a nil client, the ID-less placeholder fosite
// uses when a request carried no credentials, and a public client, which fosite
// only identifies by client_id. Draft §4.4 requires client authentication or a
// sender-constrained ID-JAG, and sender-constrained ID-JAGs are refused until
// proof of possession (DPoP or mTLS) can be verified.
func checkClientAuthenticated(client fosite.Client) error {
	if client == nil || client.GetID() == "" || client.IsPublic() {
		return clientAuthRequired()
	}
	return nil
}

// checkClientBinding enforces the draft's §4.4.1 client_id continuity: the
// assertion's client_id claim (minted by the IdP from its resource
// connection) must name the client fosite resolved for this request. Both
// sides are required to be non-empty first so the check can never
// degenerate into an empty-equals-empty pass. HandleTokenEndpointRequest
// already refuses an unauthenticated client; repeating checkClientAuthenticated
// here keeps the binding safe on its own and returns the same refusal.
func (*IDJAGHandler) checkClientBinding(requester fosite.AccessRequester, claims *ValidatedClaims) error {
	if claims.ClientID == "" {
		return invalidAssertion("The ID-JAG assertion must carry a 'client_id' claim.")
	}
	client := requester.GetClient()
	if err := checkClientAuthenticated(client); err != nil {
		return err
	}
	if claims.ClientID != client.GetID() {
		return invalidAssertion("The ID-JAG assertion was not issued to the requesting client.")
	}
	return nil
}

// IDJAGIssuanceFactory builds the production bound ID-JAG handler. Like the
// plain handler, it is registered whenever the jwt-bearer grant type is
// enabled at all, even if no trusted issuer currently accepts ID-JAG
// assertions, so a recognized ID-JAG
// gets IDJAGHandler's precise "issuer is not enabled for this grant" rejection
// instead of falling through to fosite's generic invalid_request. The two
// handlers share the per-issuer policy and split one grant type by assertion
// typ. explicitGrantClientIDs names the clients that must list the jwt-bearer
// grant in their registered grant types. subjects is required when a trusted
// issuer has subject_mode resolve: the factory then returns an error if it is
// nil. The resolver alone decides which upstream_subject issuers and claims
// resolve, reporting the others with subject.ErrNotConfigured. It applies
// WithAuthorizationDetailsRegistry: the registry's types registered for the
// jwt-bearer grant are the ones an ID-JAG's authorization_details claim may
// carry. See JWTBearerIssuanceFactory for the shared validator's ownership
// contract.
func IDJAGIssuanceFactory(
	trustedIssuers []TrustedIssuer, shared *MultiIssuerTokenValidator, explicitGrantClientIDs []string,
	resolver subject.Resolver, options ...FactoryOption,
) (server.Factory, error) {
	if resolver == nil && HasResolveIssuer(trustedIssuers) {
		return nil, errors.New("a subject resolver is required when a trusted issuer has subject_mode resolve")
	}
	resolved := resolveFactoryOptions(options)
	return jwtBearerGrantFactory(trustedIssuers, shared,
		func(deps issuanceDeps) (any, error) {
			h, err := newIDJAGIssuanceHandler(deps, explicitGrantClientIDs, nil)
			if err != nil {
				return nil, err
			}
			h.resolver = resolver
			h.detailsRegistry = resolved.authorizationDetailsRegistry
			return h, nil
		})
}

var _ fosite.TokenEndpointHandler = (*IDJAGHandler)(nil)

// enabledPolicy returns the issuer's policy for this grant, refusing an issuer
// that is not enabled for it. Unlike the plain grant, it does not refuse an
// authorization_details claim: completeRedemption processes it.
func (h *IDJAGHandler) enabledPolicy(claims *ValidatedClaims) (*JWTBearerGrantPolicy, error) {
	policy, ok := h.policies[claims.Issuer]
	if !ok {
		return nil, invalidAssertion("The ID-JAG assertion issuer is not enabled for this grant.")
	}
	return policy, nil
}
