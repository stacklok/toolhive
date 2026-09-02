// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/x/errorsx"

	coreaudit "github.com/stacklok/toolhive-core/audit"
	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// Compile-time check that Handler implements fosite.TokenEndpointHandler.
var _ fosite.TokenEndpointHandler = (*Handler)(nil)

// maxDelegationDepth bounds how many nested "act" entries a subject token's
// delegation chain may carry. Without a cap, a client could repeatedly
// re-exchange a delegated token to grow the chain without limit, bloating the
// issued token and the load it places on downstream resource servers that
// parse it.
//
// maxActClaimSize bounds the serialized normalized act data (iss/sub) after
// chainToAct strips non-identity claims, preventing a shallow prior chain from
// inflating every re-issued token.
const (
	maxDelegationDepth = 10
	maxActClaimSize    = 8 * 1024
)

// anyDelegateClient is the TrustedIssuer.AllowedDelegateClients entry that
// explicitly opts an issuer into "any ToolHive confidential client holding
// the token-exchange grant may act as its delegate" — the behavior that used
// to be the implicit default whenever the field was left empty. See
// checkDelegationConsent's doc comment for why that default was removed:
// permissiveness must now be declared with this wildcard, not left to an
// absent field.
const anyDelegateClient = "*"

// Handler implements RFC 8693 token exchange for user-to-agent delegation.
//
// When an authenticated OAuth client (the acting agent) presents a user's JWT
// as subject_token, the handler validates the token and issues a delegated JWT
// with sub=user and an act claim containing the client's identity, per
// RFC 8693 Section 4.1.
//
// Subject tokens are intentionally reusable within their lifetime: per
// RFC 8693's security considerations, a token exchange does not invalidate
// the subject token, so the same subject token may be exchanged more than
// once. Replay is bounded by the delegated token's lifetime cap
// (min(subject-remaining, delegation)), not by single-use tracking; per-jti
// single-use enforcement is deferred to the broader M2M/sender-constrained-
// token effort.
type Handler struct {
	*oauth2.HandleHelper
	validator          SubjectTokenValidator // for subject tokens (multi-issuer)
	selfValidator      SubjectTokenValidator // for actor tokens (self-issued only)
	issuer             string
	delegationLifespan time.Duration
	config             tokenExchangeConfig
	allowedAudiences   []string
	// configuredDelegateClients holds the operator-configured delegate-client
	// IDs (Config.DelegateClients, ID-only).
	configuredDelegateClients []string
}

// formParams holds the validated RFC 8693 form parameters extracted from a
// token exchange request.
type formParams struct {
	subjectToken string
	actorToken   string // empty if not provided
}

// tokenExchangeConfig defines the configuration interface needed by the handler.
type tokenExchangeConfig interface {
	fosite.ScopeStrategyProvider
	fosite.AudienceStrategyProvider
	fosite.AccessTokenLifespanProvider
}

// CanHandleTokenEndpointRequest returns true if the request's grant_type is
// the RFC 8693 token exchange grant type.
func (*Handler) CanHandleTokenEndpointRequest(_ context.Context, requester fosite.AccessRequester) bool {
	return requester.GetGrantTypes().ExactOne(oauthproto.GrantTypeTokenExchange)
}

// CanSkipClientAuth always returns false: RFC 8693 token exchange requires
// client authentication, and other handlers decide their own requirements.
func (*Handler) CanSkipClientAuth(_ context.Context, _ fosite.AccessRequester) bool {
	return false
}

// HandleTokenEndpointRequest validates the token exchange request parameters,
// verifies the subject token, and constructs a delegated session with the act claim.
//
// The delegated token's lifetime is the minimum of the subject token's remaining
// lifetime and the configured delegation lifespan.
func (h *Handler) HandleTokenEndpointRequest(ctx context.Context, requester fosite.AccessRequester) error {
	if !h.CanHandleTokenEndpointRequest(ctx, requester) {
		return errorsx.WithStack(fosite.ErrUnknownRequest)
	}

	client := requester.GetClient()

	// The client MUST be confidential — only authenticated confidential clients
	// may act on behalf of a user.
	if client.IsPublic() {
		return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint(
			"The OAuth 2.0 Client is marked as public and is thus not allowed to use authorization grant 'token-exchange'."))
	}

	// Reject clients not registered for this grant type up front, before any
	// subject-token validation work. PopulateTokenEndpointResponse repeats
	// this check as defense-in-depth.
	if !client.GetGrantTypes().Has(oauthproto.GrantTypeTokenExchange) {
		return errorsx.WithStack(fosite.ErrUnauthorizedClient.WithHint(
			"The OAuth 2.0 Client is not allowed to use authorization grant 'token-exchange'."))
	}

	// The authenticated client is the acting party ("actor"). The client is
	// already authenticated by fosite's client authentication strategy before
	// this handler runs.
	actorID := client.GetID()
	form := requester.GetRequestForm()

	// Validate required RFC 8693 form parameters.
	params, err := validateFormParams(form)
	if err != nil {
		return err
	}

	// Validate requested_token_type per RFC 8693 Section 2.1: if the client
	// requests a token type the server does not support, the request must fail.
	requestedTokenType := form.Get("requested_token_type")
	if requestedTokenType != "" && requestedTokenType != oauthproto.TokenTypeAccessToken {
		return errorsx.WithStack(fosite.ErrInvalidRequest.WithHintf(
			"The 'requested_token_type' value %q is not supported. This server only issues %q.",
			requestedTokenType, oauthproto.TokenTypeAccessToken))
	}

	// Validate the subject token against the configured token validator.
	validatedClaims, err := h.validator.Validate(ctx, params.subjectToken)
	if err != nil {
		slog.Debug("Subject token validation failed",
			"error", err,
			"actor", actorID,
		)
		// RFC 8693 §2.2.2 mandates invalid_request here: "if either the
		// subject_token or actor_token are invalid for any reason, or are
		// unacceptable based on policy ... The value of the error parameter
		// MUST be the invalid_request error code." checkDelegationConsent
		// below deliberately returns invalid_grant instead for its own
		// failures — a documented deviation: RFC 6749 §5.2's invalid_grant
		// covers "was issued to another client", §2.2.2 permits other error
		// codes for other failures, and both Keycloak and Hydra follow this
		// same split.
		return errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"The subject token is invalid or could not be verified."))
	}

	// Resolve actor identity: explicit actor_token or authenticated client.
	actorSub, err := h.resolveActorIdentity(ctx, params, client)
	if err != nil {
		return err
	}

	configuredDelegate := slices.Contains(h.configuredDelegateClients, client.GetID())
	if err := checkDelegationConsent(validatedClaims, client.GetID(), actorSub, configuredDelegate); err != nil {
		return err
	}

	if err := h.grantScopes(ctx, requester, client, validatedClaims); err != nil {
		return err
	}

	if err := h.grantAndBoundAudiences(ctx, requester, client, validatedClaims); err != nil {
		return err
	}

	// Build the delegated session with the user's identity and the
	// authenticated client's identity. The third argument becomes the
	// issued token's RFC 9068 client_id, so it must identify the client the
	// token was actually issued to (client.GetID()) — not actorSub, which
	// may name a distinct actor asserted via actor_token and belongs only
	// in the act claim below.
	delegatedSession := session.New(
		delegatedSubject(validatedClaims),
		"", // No IDP session link for delegated tokens.
		client.GetID(),
		session.UserClaims{
			Name:  validatedClaims.Name,
			Email: validatedClaims.Email,
		},
	)

	act, externalIssuer, err := buildActClaim(validatedClaims, h.issuer, actorSub)
	if err != nil {
		return err
	}
	delegatedSession.JWTClaims.Extra["act"] = act
	// external_issuer records that this exchange (or, on re-exchange, a
	// prior one in the chain) involved a trusted external issuer,
	// independent of whether a client-namespace actor was also resolved
	// (see buildActClaim's doc comment for why this is not nested inside
	// act, and for how it is carried forward across re-exchanges).
	if externalIssuer != "" {
		delegatedSession.JWTClaims.Extra["external_issuer"] = externalIssuer
	}

	// Compute the delegated token lifetime: the shorter of the subject token's
	// remaining lifetime and the configured delegation lifespan.
	lifetime, err := h.computeLifetime(validatedClaims.Expiry)
	if err != nil {
		return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint(
			"The subject token has expired."))
	}
	delegatedSession.SetExpiresAt(fosite.AccessToken, time.Now().UTC().Add(lifetime))

	requester.SetSession(delegatedSession)

	slog.Debug("Token exchange request validated",
		"subject", validatedClaims.Subject,
		"actor", actorSub,
		"issuer", validatedClaims.Issuer,
		"subject_token_client", validatedClaims.ExternalActor,
		"subject_token_client_id", validatedClaims.ClientID,
		"lifetime", lifetime.String(),
	)

	return nil
}

// PopulateTokenEndpointResponse issues the delegated access token and sets
// the RFC 8693 issued_token_type in the response.
func (h *Handler) PopulateTokenEndpointResponse(
	ctx context.Context, requester fosite.AccessRequester, responder fosite.AccessResponder,
) error {
	if !h.CanHandleTokenEndpointRequest(ctx, requester) {
		return errorsx.WithStack(fosite.ErrUnknownRequest)
	}

	if !requester.GetClient().GetGrantTypes().Has(oauthproto.GrantTypeTokenExchange) {
		return errorsx.WithStack(fosite.ErrUnauthorizedClient.WithHint(
			"The OAuth 2.0 Client is not allowed to use authorization grant 'token-exchange'."))
	}

	// Use the session's ExpiresAt (set during HandleTokenEndpointRequest) as the
	// authoritative lifetime. This respects the min(subject_remaining, delegation)
	// bound computed earlier. Fall back to the configured access token lifespan
	// only if no expiry was set on the session.
	atLifespan := h.config.GetAccessTokenLifespan(ctx)
	if sessionExpiry := requester.GetSession().GetExpiresAt(fosite.AccessToken); !sessionExpiry.IsZero() {
		remaining := time.Until(sessionExpiry)
		if remaining <= 0 {
			// The session's bound has already elapsed since HandleTokenEndpointRequest
			// computed it. Fail closed instead of signing a token whose exp is already
			// in the past while reporting a negative expires_in — fosite's JWT strategy
			// sets exp directly from this session's expiry (GetExpiresAt), independent
			// of atLifespan, so letting this fall through would not simply hand out a
			// longer-lived token, it would issue an already-expired one.
			return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint(
				"The delegated token's bound lifetime has already elapsed."))
		}
		if remaining < atLifespan {
			atLifespan = remaining
		}
	}

	if _, err := h.IssueAccessToken(ctx, atLifespan, requester, responder); err != nil {
		return err
	}

	// Per RFC 8693 Section 2.2.1, the response MUST include issued_token_type.
	responder.SetExtra("issued_token_type", oauthproto.TokenTypeAccessToken)

	return nil
}

// resolveActorIdentity determines the acting party identity: either the
// authenticated OAuth client ID, or — when actor_token is present — the
// distinct actor identity it asserts.
//
// actor_token lets the authenticated client name a more specific actor than
// its own client_id (e.g. a particular agent instance or delegate persona)
// the same way a normal issued token records its client identity: via a
// "client_id" claim. The actor_token's own "client_id" claim MUST match the
// authenticated client ID — this is the client-ID binding check, proving the
// token was minted for this client — while its "sub" claim is the actor
// identity that is returned here and flows into the delegated token's act.sub
// the same place a normal client's identity would go. "sub" is deliberately
// not compared to client.GetID(): requiring equality there would make
// actor_token unable to ever assert an identity different from
// the OAuth client, collapsing it to a no-op self-check. Note that this does
// not by itself grant the asserted actor any extra privilege: the resulting
// actor identity still has to satisfy checkDelegationConsent (may_act,
// ExternalActor, client_id binding, or the configured-delegate exemption)
// like any other actor identity would.
func (h *Handler) resolveActorIdentity(
	ctx context.Context, params *formParams, client fosite.Client,
) (string, error) {
	if params.actorToken != "" {
		// Validate actor_token against the AS's own JWKS (must be self-issued).
		actorClaims, err := h.selfValidator.Validate(ctx, params.actorToken)
		if err != nil {
			slog.Debug("Actor token validation failed", "error", fmt.Errorf("actor token: %w", err))
			return "", errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
				"The actor token is invalid or could not be verified."))
		}
		// Client-ID binding: actor_token's client_id claim MUST match the
		// authenticated client ID. This prevents replay attacks where a leaked
		// actor token is presented by a different client. The client ID is
		// always verified by fosite's client authentication before reaching here.
		// RFC 8693 §2.2.2 requires invalid_request when an actor token is
		// unacceptable based on policy.
		if actorClaims.ClientID != client.GetID() {
			return "", errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
				"The actor token's client_id claim does not match the authenticated client identity."))
		}
		return actorClaims.Subject, nil
	}

	// No actor_token: the authenticated client is the acting party.
	return client.GetID(), nil
}

// validateFormParams validates the required RFC 8693 form parameters and returns
// the parsed parameters on success.
func validateFormParams(form url.Values) (*formParams, error) {
	subjectToken := form.Get("subject_token")
	if subjectToken == "" {
		return nil, errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"The 'subject_token' parameter is required for token exchange."))
	}

	subjectTokenType := form.Get("subject_token_type")
	if subjectTokenType == "" {
		return nil, errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"The 'subject_token_type' parameter is required for token exchange."))
	}

	// id_token is intentionally excluded here, the same as for actor_token
	// tokens below: an ID token's claim conventions differ from an access
	// token's (e.g. "aud" names the relying-party client, not a resource) and
	// this validator applies neither a distinct validation profile nor an
	// id_token-specific claim mapping — accepting the type would let a client
	// declare "id_token" while the token is validated exactly like an access
	// token, silently ignoring the declared type's semantics.
	switch subjectTokenType {
	case oauthproto.TokenTypeAccessToken, oauthproto.TokenTypeJWT:
		// Valid subject token types.
	default:
		return nil, errorsx.WithStack(fosite.ErrInvalidRequest.WithHintf(
			"The 'subject_token_type' value %q is not supported. Use %q or %q.",
			subjectTokenType, oauthproto.TokenTypeAccessToken, oauthproto.TokenTypeJWT))
	}

	actorToken := form.Get("actor_token")
	actorTokenType := form.Get("actor_token_type")

	// actor_token_type without actor_token is invalid.
	if actorTokenType != "" && actorToken == "" {
		return nil, errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"The 'actor_token_type' parameter requires 'actor_token' to be present."))
	}

	// actor_token requires actor_token_type.
	if actorToken != "" && actorTokenType == "" {
		return nil, errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"The 'actor_token_type' parameter is required when 'actor_token' is present."))
	}

	// Validate actor_token_type if present.
	// Note: id_token is intentionally excluded for actor tokens. An actor presents
	// a bearer credential (access_token/jwt), not an identity assertion (id_token).
	if actorTokenType != "" {
		switch actorTokenType {
		case oauthproto.TokenTypeAccessToken, oauthproto.TokenTypeJWT:
			// Valid actor token types.
		default:
			return nil, errorsx.WithStack(fosite.ErrInvalidRequest.WithHintf(
				"The 'actor_token_type' value %q is not supported. Use %q or %q.",
				actorTokenType, oauthproto.TokenTypeAccessToken, oauthproto.TokenTypeJWT))
		}
	}

	return &formParams{
		subjectToken: subjectToken,
		actorToken:   actorToken,
	}, nil
}

// computeLifetime returns the minimum of the subject token's remaining lifetime
// and the configured delegation lifespan. Returns an error if the subject token
// has already expired.
func (h *Handler) computeLifetime(subjectExpiry time.Time) (time.Duration, error) {
	remaining := time.Until(subjectExpiry)
	if remaining <= 0 {
		return 0, fmt.Errorf("subject token expired %v ago", -remaining)
	}

	if remaining < h.delegationLifespan {
		return remaining, nil
	}
	return h.delegationLifespan, nil
}

// delegatedSubject returns the "sub" to embed in the delegated token.
//
// Self-issued tokens pass through unchanged: their subject is already a
// UUID from UserResolver.ResolveUser's (providerID, providerSubject)
// mapping (pkg/authserver/server/handlers/user.go), ToolHive's own native
// subject namespace.
//
// A subject token from a trusted external issuer is qualified as
// "<issuerURL>#<sub>" instead, because the issued token's "sub" is the only
// signal Cedar's extractClientIDFromClaims (pkg/authz/authorizers/cedar/core.go)
// keys on — it reads "sub" alone, not "iss" — and the external issuer
// chooses that value, not ToolHive. Without qualification, an external
// issuer could pick a "sub" equal to a native user's UUID and mint a
// delegated token indistinguishable from that user's, entirely without
// malicious intent on either issuer's part: two trusted issuers merely
// choosing overlapping subject values collide with no attacker involved.
//
// "#" is an unambiguous separator: an issuer URL cannot itself contain a
// fragment (the OIDC/OAuth "iss" value is a URL without a fragment
// component, and this validator's own issuer comparison is an exact string
// match against that value), so strings.Cut(sub, "#") on the qualified
// subject always recovers (issuerURL, externalSub) uniquely. A native UUID
// can never collide with a qualified value, since a UUID contains no "#".
//
// Re-exchanging a previously-issued delegated token does not double-qualify:
// such a token's "iss" is always this server's own issuer (fosite stamps
// "iss" from the server's config at issuance — see session.New's doc
// comment), so Validate routes it to the self-issued path, which never
// calls this branch.
func delegatedSubject(validatedClaims *ValidatedClaims) string {
	if validatedClaims.ExternalIssuer == "" {
		return validatedClaims.Subject
	}
	return validatedClaims.ExternalIssuer + "#" + validatedClaims.Subject
}

// buildActClaim assembles the RFC 8693 Section 4.1 "act" claim for the
// delegated token, and separately reports the external issuer of the
// original external hop (if any) for the caller to record as its own
// top-level claim. This is either the external issuer this exchange itself
// resolved, or — when this exchange has none of its own (a self-issued
// subject token being re-exchanged) — the value already carried in the
// subject token's own "external_issuer" claim, so the fact that an external
// issuer was ever involved survives across re-exchanges (up to whatever
// depth maxDelegationDepth still permits) instead of disappearing after one
// hop.
//
// externalIssuer is deliberately NOT nested inside act: RFC 8693 §4.1
// defines act as identifying a party that acted, and an issuer alone names
// no party — "the combination of the two claims iss and sub might be
// necessary to uniquely identify an actor" (§4.1) treats iss only as a
// qualifier for an already-identified sub, never as a standalone actor.
// Nesting a bare {"iss": ...} entry under act.act would present a
// non-actor as a prior actor to any RFC-8693-aware consumer that walks the
// chain -- including this codebase's own pkg/audit, whose
// extractDelegationChainFromIdentity is documented as extracting "the full
// chain of acting parties." A may_act-bearing or ActorMatcher-only external
// token has no client-namespace actor to report (see ExternalActor's doc
// comment), so for those the act claim stops at the outer, genuinely-acting
// hop, and the external issuer is reported only via the returned string.
//
// The outermost act.sub is always actorID (the ToolHive client) — every
// downstream consumer reads that as "who is acting", and it must not change
// regardless of how the subject token was obtained.
//
// Extracted from HandleTokenEndpointRequest rather than inlined: the external
// provenance nesting, the prior-chain depth gate, and the encoded-size gate
// below add several branches that have nothing to do with the surrounding
// request plumbing, and multiple of them reject the request outright.
func buildActClaim(validatedClaims *ValidatedClaims, issuer, actorID string) (map[string]any, string, error) {
	act := map[string]any{"iss": issuer, "sub": actorID}
	nestUnder := act
	newLevels := 1

	// When the subject token came from a trusted external issuer
	// (ValidatedClaims.ExternalIssuer is set only there — see
	// multi_issuer_validator.go), nest that issuer's allowlisted actor one
	// level in, when one was resolved: RFC 8693 §4.1 anticipates exactly
	// this, "the combination of the two claims 'iss' and 'sub' might be
	// necessary to uniquely identify an actor." A may_act-bearing or
	// ActorMatcher-only external token has no such actor to nest (see
	// ExternalActor's doc comment) — its act claim stays a single hop, and
	// the caller-returned externalIssuer below is this exchange's only
	// record that an external issuer was involved.
	if validatedClaims.ExternalActor != "" {
		external := map[string]any{"iss": validatedClaims.ExternalIssuer, "sub": validatedClaims.ExternalActor}
		act["act"] = external
		nestUnder = external
		newLevels = 2
	}

	// If the subject token itself carries an "act" claim (i.e. it is a
	// previously-delegated token being re-exchanged), nest it so the full
	// delegation chain remains auditable rather than being discarded.
	// newLevels accounts for the external wrapper above, if any, so the
	// resulting chain never exceeds maxDelegationDepth regardless of how
	// many levels this exchange itself adds.
	if priorAct, ok := validatedClaims.Extra["act"]; ok && priorAct != nil {
		// Parse with the shared audit-side parser rather than a bespoke walker: it
		// reports both the chain depth and any RFC 8693 Section 4.1 conformance
		// violation, so a non-object act cannot slip past the depth gate and be
		// re-minted. A JSON-null act is filtered by the guard above: it asserts no
		// delegation and must not read as malformed.
		chain := coreaudit.ParseDelegationChain(priorAct, maxDelegationDepth)
		if chain.Malformed {
			// MalformedReason is a closed, value-free enum; it is surfaced to the
			// client and MUST stay that way — never interpolate claim contents here.
			return nil, "", errorsx.WithStack(fosite.ErrInvalidRequest.WithHintf(
				"The subject token's delegation chain is malformed (%s).", chain.MalformedReason))
		}
		// len(Chain) is the prior chain's depth: the parser appends exactly one
		// hop per level and stops at maxDelegationDepth, so it is
		// min(depth, maxDelegationDepth). newLevels is what this exchange adds
		// on top — one for the acting client, two when an external issuer is
		// also nested — so the resulting chain never exceeds the cap.
		if len(chain.Chain)+newLevels > maxDelegationDepth {
			return nil, "", errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
				"The subject token's delegation chain is too deep."))
		}
		// Rebuild the nested act from the parsed chain rather than nesting
		// priorAct verbatim: the subject token's prior act claim may itself
		// have originated from an untrusted external issuer (e.g. a
		// re-exchange of a token whose own nested act content we never
		// validated), and nesting it verbatim would re-sign that content
		// under ToolHive's own key, presenting it as vouched-for. Rebuilding
		// from chain.Chain keeps only iss/sub per hop — extraClaims in
		// coreaudit are dropped by the parser into an in-memory-only map and
		// never resurface here — so unknown or untrusted per-hop claims are
		// normalized away while the audit trail of who-acted-for-whom
		// (Section 4.1's stated purpose for act) is preserved.
		//
		// nestUnder is the innermost entry this exchange added: the acting
		// client normally, or the external-issuer entry when one was nested
		// above, so the prior chain hangs below the provenance rather than
		// displacing it.
		normalizedAct, err := chainToAct(chain.Chain)
		if err != nil {
			return nil, "", errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
				"The subject token's delegation chain contains an empty actor."))
		}
		nestUnder["act"] = normalizedAct
	}
	encodedAct, err := json.Marshal(act)
	if err != nil {
		return nil, "", errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"The subject token's delegation chain cannot be serialized."))
	}
	if len(encodedAct) > maxActClaimSize {
		return nil, "", errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"The subject token's delegation chain is too large."))
	}

	// Under the current handler structure there is only ever one external
	// hop, so "original" and "most recent" coincide today; if a design ever
	// introduces multiple external-issuer hops, this will need to change to
	// keep tracking the original rather than the latest.
	externalIssuer := validatedClaims.ExternalIssuer
	if externalIssuer == "" {
		if prior, ok := validatedClaims.Extra["external_issuer"].(string); ok {
			externalIssuer = prior
		}
	}
	return act, externalIssuer, nil
}

// chainToAct rebuilds a nested RFC 8693 act structure from a parsed
// delegation chain, keeping only each hop's iss/sub — never the hop's extra
// claims, which coreaudit.ParseDelegationChain deliberately keeps out of the
// returned struct's serializable fields. This is what strips arbitrary
// content an untrusted prior issuer may have placed in a nested act before
// it gets re-signed under ToolHive's own key. hops is outermost-first (see
// coreaudit.DelegationChain's doc comment); the result nests them in that
// same order. It rejects any hop without an issuer or subject. Returns nil for
// an empty chain.
func chainToAct(hops []coreaudit.DelegatedActor) (map[string]any, error) {
	var nested map[string]any
	for i := len(hops) - 1; i >= 0; i-- {
		if hops[i].Issuer == "" && hops[i].Subject == "" {
			return nil, fmt.Errorf("delegation hop %d has no issuer or subject", i)
		}

		hop := make(map[string]any, 3)
		if hops[i].Issuer != "" {
			hop["iss"] = hops[i].Issuer
		}
		if hops[i].Subject != "" {
			hop["sub"] = hops[i].Subject
		}
		if nested != nil {
			hop["act"] = nested
		}
		nested = hop
	}
	return nested, nil
}

// delegateClientAllowed reports whether actorID may use an issuer's
// AllowedDelegateClients-bound consent grant (a may_act.sub match or external
// actor authorization): either the list contains the wildcard
// anyDelegateClient, or it contains actorID directly. An empty list denies
// every client — validateTrustedIssuer rejects that configuration outright
// for any TrustedIssuer, so the empty case here is an extra fail-closed
// backstop, not the path an operator is expected to reach. Callers must only
// invoke this for a claim that actually went through that per-issuer
// validation (ExternalIssuer != ""); a self-issued token's
// AllowedDelegateClients is always nil, since that field has no equivalent
// concept on the self-issued path.
func delegateClientAllowed(allowedDelegateClients []string, actorID string) bool {
	return slices.Contains(allowedDelegateClients, anyDelegateClient) ||
		slices.Contains(allowedDelegateClients, actorID)
}

// checkDelegationConsent enforces the RFC 8693 §4.4 delegation consent check.
//
// There are three consent sources, checked in order:
//
//  1. may_act: if present, it is the authoritative consent signal — only the
//     party named in may_act.sub may delegate. The client_id binding is
//     skipped in this case because may_act enables cross-client delegation
//     (the token was issued to client A but authorizes client B to act).
//     ValidatedClaims.AllowedDelegateClients is additionally enforced here,
//     but ONLY when ExternalIssuer is set: that field is a TrustedIssuer
//     concept with no self-issued equivalent, so a self-issued may_act (a
//     ToolHive user token delegating to a ToolHive client directly) is
//     bound by may_act.sub alone, the same as before this check existed.
//     On the external path, may_act bypasses AllowedActors and ActorMatcher,
//     not per-client containment — delegateClientAllowed still applies there.
//
//  2. External actor authorization: if may_act is absent but the multi-issuer
//     validator has already established consent for this token
//     (multi_issuer_validator.go), it did so by matching the subject token's
//     actor claim against that issuer's operator-configured AllowedActors or
//     ActorMatcher. An allowlisted actor claim — even when configured as
//     "client_id" — names a client in the EXTERNAL issuer's namespace, not ToolHive's, so it must never be compared
//     against actorID the way case 3 below compares ValidatedClaims.ClientID.
//     This case MUST be checked before case 3: it is not a fallback for an
//     empty ClientID, it is a distinct, already-verified consent signal that
//     takes priority whenever it is set, even if ClientID also happens to be
//     populated.
//
//     ValidatedClaims.AllowedDelegateClients binds this to specific ToolHive
//     clients: actorID must appear in it (or it must contain the wildcard
//     anyDelegateClient), checked below by delegateClientAllowed. Without
//     this, the allowlist would authorize "this external client's tokens may
//     be exchanged", not "...by this particular ToolHive client" — every
//     ToolHive confidential client holding the token-exchange grant would be
//     delegation-equivalent with respect to an allowlisted external actor, so
//     compromise of the weakest such client would be as good as compromise
//     of all of them (see #5989). validateTrustedIssuer rejects an empty
//     AllowedDelegateClients at construction for exactly this reason: an
//     operator must declare permissiveness with the wildcard, not get it by
//     omission. Either way this remains bounded by: the calling client must
//     already possess a valid subject token (it cannot forge one), and
//     grantScopes/grantAndBoundAudiences still narrow the result to what
//     both the client and the subject token are authorized for.
//
//  3. client_id: if neither of the above applies, fall back to client_id
//     binding — the subject token's client_id must match the authenticated
//     client. This prevents a stolen subject token from being exchanged by a
//     different client.
//
//     Exception: a configured delegate client (Handler.configuredDelegateClients,
//     Config.DelegateClients ID-only) is exempt from this binding when the
//     subject token is self-issued (ExternalIssuer == ""). Such a client may
//     present ANY self-issued subject token regardless of which client it was
//     originally issued to. This is deliberately scoped to the self-issued
//     path only — see selfIssuedDelegate below — because delegate clients are
//     operator-declared at server startup, not obtained through self-service
//     registration (DCR), matching the blanket-trust model already accepted
//     for the AllowedDelegateClients wildcard (anyDelegateClient) above.
//     The remaining bounds still apply: grantScopes/grantAndBoundAudiences
//     narrow the result to what both the client and the subject token are
//     authorized for, and the delegated token's lifetime is capped as usual.
//
// If none of the three consent sources apply, the subject token carries no
// verifiable binding to any client at all — this fails closed (CWE-863)
// rather than allowing an unbound token through.
func checkDelegationConsent(validatedClaims *ValidatedClaims, clientID, actorSub string, configuredDelegate bool) error {
	// selfIssuedDelegate is true only when a configured delegate client is
	// presenting a self-issued token (ExternalIssuer == "" rules out the
	// external-issuer path explicitly, rather than relying on ExternalActor
	// always being set there — see multi_issuer_validator.go). Without this,
	// a future change to that file's actor-resolution logic could let a
	// delegate client bypass AllowedDelegateClients (the #5989 protection)
	// on the external path.
	selfIssuedDelegate := configuredDelegate && validatedClaims.ExternalIssuer == ""

	switch {
	case validatedClaims.MayAct != nil:
		// may_act.sub is compared against actorSub, not clientID: this is
		// the one binding that is meant to key off the asserted actor —
		// may_act's whole purpose is authorizing a specific actor, which
		// actor_token lets the authenticated client name distinctly from
		// itself.
		if validatedClaims.MayAct.Sub != actorSub {
			return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint(
				"The subject token does not authorize this client to act on behalf of the subject."))
		}
		// AllowedDelegateClients binds to the authenticated ToolHive client
		// (clientID), not the asserted actor — see the doc comment above.
		if validatedClaims.ExternalIssuer != "" && !delegateClientAllowed(validatedClaims.AllowedDelegateClients, clientID) {
			return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint(
				"This client is not authorized to exchange subject tokens from the external actor's issuer."))
		}
	case validatedClaims.ExternalActorAuthorized:
		// Consent was already established by the external-issuer validator:
		// either the subject token's actor claim matched this issuer's
		// operator-configured AllowedActors or its complete verified claims
		// matched ActorMatcher. An allowlisted actor claim lives in the external
		// issuer's client namespace, not ToolHive's, so — even when ClientID is
		// also populated (ActorClaim: "client_id") — it must never be compared
		// against clientID or actorSub. This case must be checked before the
		// client_id cases below, not merged with them.
		//
		// AllowedDelegateClients binds this authorization to a specific set of
		// ToolHive clients — see delegateClientAllowed. This must be the
		// authenticated client (clientID), not the asserted actor (actorSub):
		// the allowlist is "this external actor's tokens may be exchanged by
		// this ToolHive client", independent of whatever actor identity that
		// client asserts via actor_token.
		if !delegateClientAllowed(validatedClaims.AllowedDelegateClients, clientID) {
			return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint(
				"This client is not authorized to exchange subject tokens from the external actor's issuer."))
		}
	case validatedClaims.ClientID != "" && validatedClaims.ClientID != clientID && !selfIssuedDelegate:
		return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint(
			"The subject token was issued to a different client."))
	case validatedClaims.ClientID == "":
		return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint(
			"The subject token carries no verifiable client binding: it has neither a 'may_act' claim nor a 'client_id' claim."))
	}
	return nil
}

// grantScopes validates that each requested scope is allowed for both the
// client and the subject token, granting the intersection. The delegated
// token's scope set is the intersection of the client's registered scopes
// and the subject token's granted scopes, preventing a client from
// escalating privileges beyond what the user authorized. A subject token
// without a scope claim grants no scopes.
func (h *Handler) grantScopes(
	ctx context.Context, requester fosite.AccessRequester, client fosite.Client, validatedClaims *ValidatedClaims,
) error {
	subjectScopes := strings.Fields(validatedClaims.Scopes)
	subjectScopeSet := make(map[string]bool, len(subjectScopes))
	for _, s := range subjectScopes {
		subjectScopeSet[s] = true
	}

	for _, scope := range requester.GetRequestedScopes() {
		if !h.config.GetScopeStrategy(ctx)(client.GetScopes(), scope) {
			return errorsx.WithStack(fosite.ErrInvalidScope.WithHintf(
				"The OAuth 2.0 Client is not allowed to request scope '%s'.", scope))
		}
		if !subjectScopeSet[scope] {
			return errorsx.WithStack(fosite.ErrInvalidScope.WithHintf(
				"The scope '%s' was not granted by the subject token.", scope))
		}
		requester.GrantScope(scope)
	}
	return nil
}

// grantAndBoundAudiences resolves the delegated token's audience from the
// request (explicit "audience", RFC 8707 "resource", or the configured
// default) and then bounds the result by the subject token.
//
// The delegated token must not target a resource the subject token was not
// itself valid for: every granted audience must be covered by the subject
// token's audience — delegation narrows, never broadens, the resource
// boundary. This mirrors grantScopes, which bounds scopes by the subject
// token; without it a client registered for audiences A and B could exchange a
// user token minted for A into a token for B, an escalation the user never
// consented to.
func (h *Handler) grantAndBoundAudiences(
	ctx context.Context, requester fosite.AccessRequester, client fosite.Client, validatedClaims *ValidatedClaims,
) error {
	if err := h.grantAudiences(ctx, requester, client); err != nil {
		return err
	}
	if err := h.grantResourceAudience(ctx, requester, client); err != nil {
		return err
	}
	if err := h.grantDefaultAudience(ctx, requester, client); err != nil {
		return err
	}
	return ensureAudienceSubsetOfSubject(requester.GetGrantedAudience(), validatedClaims.Audience)
}

// grantAudiences validates that the requested audience is allowed for this
// client and grants it.
func (h *Handler) grantAudiences(ctx context.Context, requester fosite.AccessRequester, client fosite.Client) error {
	if err := h.config.GetAudienceStrategy(ctx)(client.GetAudience(), requester.GetRequestedAudience()); err != nil {
		return errorsx.WithStack(err)
	}
	for _, aud := range requester.GetRequestedAudience() {
		requester.GrantAudience(aud)
	}
	return nil
}

// grantResourceAudience validates the RFC 8707 resource parameter against
// both the server's allowedAudiences and the client's own registered
// audiences, and grants it as an additional audience claim, binding the
// issued token to a specific resource server (e.g., an MCP server).
//
// Per RFC 8707 §2, a request MAY carry multiple resource parameters; this
// handler deliberately narrows that to at most one, since a delegated MCP
// token is expected to target a single resource.
func (h *Handler) grantResourceAudience(ctx context.Context, requester fosite.AccessRequester, client fosite.Client) error {
	resources := requester.GetRequestForm()["resource"]
	if len(resources) > 1 {
		return errorsx.WithStack(server.ErrInvalidTarget.WithHint(
			"Multiple resource parameters are not supported."))
	}
	if len(resources) == 1 && resources[0] != "" {
		resource := resources[0]
		if err := server.ValidateAudienceURI(resource); err != nil {
			return errorsx.WithStack(err)
		}
		if err := server.ValidateAudienceAllowed(resource, h.allowedAudiences); err != nil {
			return errorsx.WithStack(err)
		}
		// The resource parameter is RFC 8707's mechanism for requesting an
		// audience, so it must be subject to the same per-client audience
		// registration as the "audience" parameter (grantAudiences) — otherwise
		// a client could bypass its registered audiences simply by using
		// "resource" instead of "audience".
		if err := h.config.GetAudienceStrategy(ctx)(client.GetAudience(), []string{resource}); err != nil {
			return errorsx.WithStack(server.ErrInvalidTarget.WithHintf(
				"The client is not permitted to request a token for resource %q.", resource))
		}
		requester.GrantAudience(resource)
	}
	return nil
}

// grantDefaultAudience ensures the delegated token carries at least one
// audience, since RFC 9068 §4 makes "aud" a required claim. grantAudiences and
// grantResourceAudience only grant an audience when the client explicitly
// requests one; if neither was requested, this defaults to the server's sole
// configured audience when unambiguous (mirroring handlers/token.go's
// authorization_code fallback), or rejects the request when no unambiguous
// default exists rather than silently issuing an audience-less token. The
// default audience is checked against the client's registered audiences
// like any explicitly requested audience, so a client is never granted an
// audience it wasn't registered for.
func (h *Handler) grantDefaultAudience(ctx context.Context, requester fosite.AccessRequester, client fosite.Client) error {
	if len(requester.GetGrantedAudience()) > 0 {
		return nil
	}
	if len(h.allowedAudiences) != 1 {
		return errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"An explicit 'resource' or 'audience' parameter is required because no unambiguous default audience is configured."))
	}
	defaultAudience := h.allowedAudiences[0]
	if err := h.config.GetAudienceStrategy(ctx)(client.GetAudience(), []string{defaultAudience}); err != nil {
		return errorsx.WithStack(fosite.ErrInvalidRequest.WithHintf(
			"The client is not permitted to request a token for the default audience %q; "+
				"an explicit 'resource' or 'audience' parameter is required.", defaultAudience))
	}
	slog.Debug("no resource parameter, defaulting to sole allowed audience",
		"audience", defaultAudience,
	)
	requester.GrantAudience(defaultAudience)
	return nil
}

// ensureAudienceSubsetOfSubject verifies that every audience granted to the
// delegated token is covered by the subject token's own audience. A subject
// token always carries at least one audience, so an empty subject audience
// here can only reject — but what guarantees that non-empty audience differs
// by path. On the self-issued path, SelfIssuedTokenValidator rejects tokens
// whose aud does not intersect this server's AllowedAudiences. On the
// external path, validateExternalToken instead requires aud to contain the
// issuer's own configured ExpectedAudience, which has no required
// relationship to AllowedAudiences — see the TrustedIssuers field doc
// comment in pkg/authserver/config.go for the operator-facing consequence.
func ensureAudienceSubsetOfSubject(granted, subjectAud []string) error {
	subj := make(map[string]bool, len(subjectAud))
	for _, a := range subjectAud {
		subj[a] = true
	}
	for _, a := range granted {
		if !subj[a] {
			return errorsx.WithStack(server.ErrInvalidTarget.WithHintf(
				"The delegated token audience %q is not covered by the subject token's audience.", a))
		}
	}
	return nil
}
