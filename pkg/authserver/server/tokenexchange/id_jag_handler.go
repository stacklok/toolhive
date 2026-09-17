// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/x/errorsx"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
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
// "oauth-id-jag+jwt".
//
// "Bound" is the trust-model difference from the plain handler. An ID-JAG
// names the OAuth client that may redeem it — the IdP mints the draft
// §4.4.1 client_id claim from its administrator-configured resource
// connection — so this handler never skips client authentication
// (CanSkipClientAuth) and requires the authenticated client to be that
// client. Confidential clients authenticate fully; a public client is
// identified by client_id, which is sufficient binding here because the
// assertion itself is the primary credential: single-use (required jti),
// short-lived (maxAssertionAge), and audience-pinned to this server
// (acceptedAudiences). The plain handler's synthetic client is not needed —
// the redeeming client is a real registered one.
//
// The redeeming client's registered grant_types metadata is deliberately not
// consulted: with open dynamic client registration, self-asserted metadata
// authorizes nothing. What authorizes the grant is the per-issuer policy
// plus the client_id binding the IdP administrator configured.
type IDJAGHandler struct {
	// core supplies the shared assertion/policy/issuance machinery
	// (validator, per-issuer policies, replay consumer, HandleHelper). It is
	// a named field, not an embedded type, so no fosite.TokenEndpointHandler
	// method is ever promoted from it: a promoted method would gate on the
	// core's own CanHandleTokenEndpointRequest (method values in Go bind at
	// the embedded receiver, there is no virtual dispatch) and reject every
	// ID-JAG request as unknown.
	core *JWTBearerHandler
}

// newIDJAGIssuanceHandler constructs the production bound handler over the
// same issuance core as newJWTBearerIssuanceHandler; see that constructor
// for the dependency requirements.
func newIDJAGIssuanceHandler(
	validator JWTBearerAssertionValidator, tokenEndpoint string, consumer storage.AssertionJWTConsumer,
	config *fosite.Config, strategy oauth2.AccessTokenStrategy, tokenStorage oauth2.AccessTokenStorage,
	trustedIssuers []TrustedIssuer,
) (*IDJAGHandler, error) {
	core, err := newJWTBearerIssuanceHandler(
		validator, tokenEndpoint, consumer, config, strategy, tokenStorage, trustedIssuers)
	if err != nil {
		return nil, err
	}
	return &IDJAGHandler{core: core}, nil
}

// CanHandleTokenEndpointRequest claims only recognized ID-JAG assertions —
// the exact complement of JWTBearerHandler's matcher, so for any given
// jwt-bearer request at most one of the two handlers is responsible.
func (*IDJAGHandler) CanHandleTokenEndpointRequest(_ context.Context, requester fosite.AccessRequester) bool {
	return requester.GetGrantTypes().ExactOne(oauthproto.GrantTypeJWTBearer) &&
		assertionType(requester.GetRequestForm().Get("assertion")) == idJAGJWTType
}

// CanSkipClientAuth never permits skipping client authentication: an ID-JAG
// is issued TO a specific client (its client_id claim), so fosite must have
// authenticated — or, for a public client, at least identified via client_id
// — the caller before HandleTokenEndpointRequest can check the binding.
// fosite.NewAccessRequest returns the client-authentication error whenever
// this is false and authentication failed, so the handler below only ever
// runs with a resolved client.
func (*IDJAGHandler) CanSkipClientAuth(context.Context, fosite.AccessRequester) bool {
	return false
}

// HandleTokenEndpointRequest validates the ID-JAG assertion, its client
// binding, and per-issuer policy, then prepares a bounded access-token
// session.
func (h *IDJAGHandler) HandleTokenEndpointRequest(ctx context.Context, requester fosite.AccessRequester) error {
	if !h.CanHandleTokenEndpointRequest(ctx, requester) {
		return errorsx.WithStack(fosite.ErrUnknownRequest)
	}
	assertion, err := validateJWTBearerAssertionForm(requester.GetRequestForm())
	if err != nil {
		return err
	}
	claims, err := h.core.validator.ValidateJWTBearerAssertion(ctx, assertion, h.core.tokenEndpoint)
	if err != nil {
		return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint("The ID-JAG assertion is invalid or could not be verified."))
	}
	// Same fail-closed nil-map/nil-consumer posture as the plain handler;
	// see the comments in JWTBearerHandler.HandleTokenEndpointRequest.
	policy, ok := h.core.policies[claims.Issuer]
	if !ok {
		return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint("The ID-JAG assertion issuer is not enabled for this grant."))
	}
	if h.core.consumer == nil {
		return errorsx.WithStack(fosite.ErrServerError.WithHint("The JWT-bearer grant is not fully configured."))
	}
	// Unlike a plain RFC 7523 assertion (where many IdPs omit jti and the
	// replay key falls back to an assertion hash), the ID-JAG draft requires
	// jti, and it is what makes the assertion single-use. Absence means the
	// token is not a conforming ID-JAG; reject rather than fall back.
	if claims.JWTID == "" {
		return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint("The ID-JAG assertion must carry a 'jti' claim."))
	}
	if err := h.checkClientBinding(requester, claims); err != nil {
		return err
	}
	resource, err := validateJWTBearerPolicy(requester.GetRequestForm(), claims, policy)
	if err != nil {
		return err
	}
	// Consume before issuing — fails closed like the plain handler: if
	// issuance fails past this point the assertion stays consumed rather
	// than becoming replayable.
	if err := h.core.consumeAssertion(ctx, idJAGReplayPurpose, claims.Issuer, claims.JWTID, claims.Expiry); err != nil {
		return err
	}
	lifetime, err := h.core.assertionBoundedLifetime(ctx, claims.Expiry)
	if err != nil {
		return err
	}
	// Subject is namespaced by issuer exactly like the plain handler's, so a
	// resource server sees one consistent subject form for every
	// assertion-derived token regardless of which handler minted it. The
	// client, unlike there, is the real authenticated one — fosite resolved
	// it (CanSkipClientAuth is always false), so requester already carries
	// it and no synthetic replacement happens.
	issuedSession := session.New(claims.Issuer+"#"+claims.Subject, "", requester.GetClient().GetID(), session.UserClaims{})
	// An ID-JAG links to no upstream IdP login at THIS server, same as a
	// plain assertion; see session.NoUpstreamSessionClaimKey.
	issuedSession.JWTClaims.Extra[session.NoUpstreamSessionClaimKey] = true
	// Carry the assertion's actor chain (RFC 8693 §4.1 "act" — for an
	// Okta-minted ID-JAG, the requesting agent's identity) into the issued
	// token, so a resource server or auditor can still tell WHICH agent
	// acted for the subject after redemption. Copied verbatim: act is
	// defined as a nested-JSON claim and buildValidatedClaims left it
	// untouched in Extra.
	if act, ok := claims.Extra["act"]; ok {
		issuedSession.JWTClaims.Extra["act"] = act
	}
	issuedSession.SetExpiresAt(fosite.AccessToken, time.Now().UTC().Add(lifetime))
	requester.GrantAudience(resource)
	requester.SetSession(issuedSession)
	return nil
}

// PopulateTokenEndpointResponse issues only an access token, sharing the
// plain handler's session-bounded issuance.
func (h *IDJAGHandler) PopulateTokenEndpointResponse(
	ctx context.Context, requester fosite.AccessRequester, responder fosite.AccessResponder,
) error {
	if !h.CanHandleTokenEndpointRequest(ctx, requester) {
		return errorsx.WithStack(fosite.ErrUnknownRequest)
	}
	return h.core.populateAccessTokenResponse(ctx, requester, responder)
}

// checkClientBinding enforces the draft's §4.4.1 client_id continuity: the
// assertion's client_id claim (minted by the IdP from its resource
// connection) must name the client fosite resolved for this request. Both
// sides are required to be non-empty first so the check can never
// degenerate into an empty-equals-empty pass — fosite initializes an
// unauthenticated request's client to an ID-less placeholder, and this
// handler must not depend on that staying unreachable.
func (*IDJAGHandler) checkClientBinding(requester fosite.AccessRequester, claims *ValidatedClaims) error {
	if claims.ClientID == "" {
		return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint("The ID-JAG assertion must carry a 'client_id' claim."))
	}
	client := requester.GetClient()
	if client == nil || client.GetID() == "" {
		return errorsx.WithStack(fosite.ErrInvalidClient.WithHint("The ID-JAG grant requires an authenticated or identified client."))
	}
	if claims.ClientID != client.GetID() {
		return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint("The ID-JAG assertion was not issued to the requesting client."))
	}
	return nil
}

// IDJAGIssuanceFactory builds the production bound ID-JAG handler. It is
// registered alongside JWTBearerIssuanceFactory whenever any trusted issuer
// enables the JWT-bearer grant: the two handlers share the per-issuer policy
// and split one grant type by assertion typ, so enabling the grant enables
// both assertion forms. See JWTBearerIssuanceFactory for the shared
// validator's ownership contract.
func IDJAGIssuanceFactory(trustedIssuers []TrustedIssuer, shared *MultiIssuerTokenValidator) (server.Factory, error) {
	return jwtBearerGrantFactory(trustedIssuers, shared,
		func(
			validator JWTBearerAssertionValidator, tokenEndpoint string, consumer storage.AssertionJWTConsumer,
			config *fosite.Config, strategy oauth2.AccessTokenStrategy, tokenStorage oauth2.AccessTokenStorage,
			resolvedIssuers []TrustedIssuer,
		) (any, error) {
			return newIDJAGIssuanceHandler(
				validator, tokenEndpoint, consumer, config, strategy, tokenStorage, resolvedIssuers)
		})
}

var _ fosite.TokenEndpointHandler = (*IDJAGHandler)(nil)
