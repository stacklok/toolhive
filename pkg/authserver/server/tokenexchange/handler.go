// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
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
const maxDelegationDepth = 10

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
	validator          SubjectTokenValidator
	delegationLifespan time.Duration
	config             tokenExchangeConfig
	allowedAudiences   []string
	// configuredDelegateClients holds the operator-configured delegate-client
	// IDs (Config.DelegateClients, ID-only).
	configuredDelegateClients []string
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

// CanSkipClientAuth returns false because client authentication is required
// for all token exchange requests.
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

	subjectToken, err := validateExchangeParams(requester.GetRequestForm())
	if err != nil {
		return err
	}

	// Validate the subject token against the server's own JWKS.
	validatedClaims, err := h.validator.Validate(ctx, subjectToken)
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

	configuredDelegate := slices.Contains(h.configuredDelegateClients, actorID)
	if err := checkDelegationConsent(validatedClaims, actorID, configuredDelegate); err != nil {
		return err
	}

	if err := h.grantScopes(ctx, requester, client, validatedClaims); err != nil {
		return err
	}

	if err := h.grantAndBoundAudiences(ctx, requester, client, validatedClaims); err != nil {
		return err
	}

	// Build the delegated session with the user's identity and the agent's act claim.
	delegatedSession := session.New(
		delegatedSubject(validatedClaims),
		"", // No IDP session link for delegated tokens.
		actorID,
		session.UserClaims{
			Name:  validatedClaims.Name,
			Email: validatedClaims.Email,
		},
	)

	act, err := buildActClaim(validatedClaims, actorID)
	if err != nil {
		return err
	}
	delegatedSession.JWTClaims.Extra["act"] = act

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
		"actor", actorID,
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

// validateExchangeParams validates the required RFC 8693 form parameters and
// returns the subject token on success.
func validateExchangeParams(form url.Values) (string, error) {
	subjectToken := form.Get("subject_token")
	if subjectToken == "" {
		return "", errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"The 'subject_token' parameter is required for token exchange."))
	}

	subjectTokenType := form.Get("subject_token_type")
	if subjectTokenType == "" {
		return "", errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"The 'subject_token_type' parameter is required for token exchange."))
	}

	if subjectTokenType != oauthproto.TokenTypeAccessToken && subjectTokenType != oauthproto.TokenTypeJWT {
		return "", errorsx.WithStack(fosite.ErrInvalidRequest.WithHintf(
			"The 'subject_token_type' value %q is not supported. Use %q or %q.",
			subjectTokenType, oauthproto.TokenTypeAccessToken, oauthproto.TokenTypeJWT))
	}

	// Reject actor_token parameters for now — the acting party identity is
	// derived from the authenticated OAuth client. A later commit adds
	// actor_token support for asserting a distinct actor.
	if form.Get("actor_token") != "" || form.Get("actor_token_type") != "" {
		return "", errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"The 'actor_token' and 'actor_token_type' parameters are not yet supported."))
	}

	// Validate requested_token_type per RFC 8693 Section 2.1: if the client
	// requests a token type the server does not support, the request must fail.
	requestedTokenType := form.Get("requested_token_type")
	if requestedTokenType != "" && requestedTokenType != oauthproto.TokenTypeAccessToken {
		return "", errorsx.WithStack(fosite.ErrInvalidRequest.WithHintf(
			"The 'requested_token_type' value %q is not supported. This server only issues %q.",
			requestedTokenType, oauthproto.TokenTypeAccessToken))
	}

	return subjectToken, nil
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
// delegated token. The outermost act.sub is always actorID (the ToolHive
// client) — every downstream consumer reads that as "who is acting", and it
// must not change regardless of how the subject token was obtained.
//
// Extracted from HandleTokenEndpointRequest rather than inlined: the external
// provenance nesting and the prior-chain depth gate below add five branches
// that have nothing to do with the surrounding request plumbing, and two of
// them reject the request outright.
func buildActClaim(validatedClaims *ValidatedClaims, actorID string) (map[string]any, error) {
	act := map[string]any{"sub": actorID}
	nestUnder := act
	newLevels := 1

	// When the subject token came from a trusted external issuer
	// (ValidatedClaims.ExternalIssuer is set only there — see
	// multi_issuer_validator.go), nest that issuer one level in, together
	// with the allowlisted actor when one was resolved. RFC 8693 §4.1
	// anticipates exactly this: "the combination of the two claims 'iss' and
	// 'sub' might be necessary to uniquely identify an actor." Without this,
	// the issued token would carry no record that the delegation originated
	// externally at all — including for a may_act-bearing external token,
	// which leaves ExternalActor unset because may_act.sub already names the
	// delegate directly via the actorID binding above. That token still has
	// an external issuer worth recording, so nesting here is keyed on
	// ExternalIssuer, not ExternalActor: the allowlist path's accepted
	// any-ToolHive-client scope limitation (see checkDelegationConsent)
	// depends on this provenance being auditable after the fact, and a
	// may_act-bearing external token — the path that bypasses the allowlist
	// entirely — needs it at least as much.
	if validatedClaims.ExternalIssuer != "" {
		external := map[string]any{"iss": validatedClaims.ExternalIssuer}
		// ExternalActor is only present on the allowlist path (see its doc
		// comment) — a may_act-bearing external token has no client-namespace
		// actor claim to report, so the nested entry there carries "iss" only.
		if validatedClaims.ExternalActor != "" {
			external["sub"] = validatedClaims.ExternalActor
		}
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
			// RFC 8693 Section 2.2.2 nominally calls for invalid_request on a bad
			// subject_token, but also allows that "other error codes may also be
			// used, as appropriate": invalid_grant keeps this consistent with the
			// depth, consent, and expiry gates in this same handler, all of which
			// reject a structurally unacceptable subject token that way.
			//
			// MalformedReason is a closed, value-free enum; it is surfaced to the
			// client and MUST stay that way — never interpolate claim contents here.
			return nil, errorsx.WithStack(fosite.ErrInvalidGrant.WithHintf(
				"The subject token's delegation chain is malformed (%s).", chain.MalformedReason))
		}
		// len(Chain) is the prior chain's depth: the parser appends exactly one
		// hop per level and stops at maxDelegationDepth, so it is
		// min(depth, maxDelegationDepth). newLevels is what this exchange adds
		// on top — one for the acting client, two when an external issuer is
		// also nested — so the resulting chain never exceeds the cap.
		if len(chain.Chain)+newLevels > maxDelegationDepth {
			return nil, errorsx.WithStack(fosite.ErrInvalidGrant.WithHint(
				"The subject token's delegation chain is too deep."))
		}
		// Nest priorAct verbatim rather than re-serializing from chain: core keeps
		// each hop's extra claims in an unexported map, so rebuilding would silently
		// drop the history trail that Section 4.1 asks us to preserve. The cost is
		// that unknown hop claims — and the non-identity claims Section 4.1 calls
		// "not meaningful" inside act — pass through re-signed, which sits in
		// tension with Section 6 data minimization. Accepted here; bounding the
		// subtree is tracked separately.
		//
		// nestUnder is the innermost entry this exchange added: the acting
		// client normally, or the external-issuer entry when one was nested
		// above, so the prior chain hangs below the provenance rather than
		// displacing it.
		nestUnder["act"] = priorAct
	}
	return act, nil
}

// delegateClientAllowed reports whether actorID may use an issuer's
// AllowedDelegateClients-bound consent grant (a may_act.sub match or an
// AllowedActors match): either the list contains the wildcard
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
//     On the external path, may_act bypasses AllowedActors, not per-client
//     containment — delegateClientAllowed still applies there.
//
//  2. ExternalActor: if may_act is absent but the multi-issuer validator has
//     already established consent for this token (multi_issuer_validator.go),
//     it did so by matching the subject token's actor claim against that
//     issuer's operator-configured AllowedActors. That actor claim — even
//     when configured as "client_id" — names a client in the EXTERNAL
//     issuer's namespace, not ToolHive's, so it must never be compared
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
func checkDelegationConsent(validatedClaims *ValidatedClaims, actorID string, configuredDelegate bool) error {
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
		if validatedClaims.MayAct.Sub != actorID {
			return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint(
				"The subject token does not authorize this client to act on behalf of the subject."))
		}
		if validatedClaims.ExternalIssuer != "" && !delegateClientAllowed(validatedClaims.AllowedDelegateClients, actorID) {
			return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint(
				"This client is not authorized to exchange subject tokens from the external actor's issuer."))
		}
	case validatedClaims.ExternalActor != "":
		// Consent was already established by the external-issuer validator: the
		// subject token's actor claim matched this issuer's operator-configured
		// AllowedActors. That claim lives in the external issuer's client
		// namespace, not ToolHive's, so — even when ClientID is also populated
		// (ActorClaim: "client_id") — it must never be compared against
		// actorID. This case must be checked before the client_id cases below,
		// not merged with them.
		//
		// AllowedDelegateClients binds this allowlisted actor to a specific set
		// of ToolHive clients — see delegateClientAllowed.
		if !delegateClientAllowed(validatedClaims.AllowedDelegateClients, actorID) {
			return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint(
				"This client is not authorized to exchange subject tokens from the external actor's issuer."))
		}
	case validatedClaims.ClientID != "" && validatedClaims.ClientID != actorID && !selfIssuedDelegate:
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
