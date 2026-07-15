// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/x/errorsx"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// Compile-time check that Handler implements fosite.TokenEndpointHandler.
var _ fosite.TokenEndpointHandler = (*Handler)(nil)

// Handler implements RFC 8693 token exchange for user-to-agent delegation.
//
// When an authenticated OAuth client (the acting agent) presents a user's JWT
// as subject_token, the handler validates the token and issues a delegated JWT
// with sub=user and an act claim containing the client's identity, per
// RFC 8693 Section 4.1.
type Handler struct {
	*oauth2.HandleHelper
	validator          *SubjectTokenValidator
	delegationLifespan time.Duration
	config             tokenExchangeConfig
	allowedAudiences   []string
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
		return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint(
			"The subject token is invalid or could not be verified."))
	}

	if err := checkDelegationConsent(validatedClaims, actorID); err != nil {
		return err
	}

	if err := h.grantScopes(ctx, requester, client, validatedClaims); err != nil {
		return err
	}

	if err := h.grantAudiences(ctx, requester, client); err != nil {
		return err
	}

	if err := grantResourceAudience(requester, h.allowedAudiences); err != nil {
		return err
	}

	if err := h.grantDefaultAudience(requester); err != nil {
		return err
	}

	// Build the delegated session with the user's identity and the agent's act claim.
	delegatedSession := session.New(
		validatedClaims.Subject,
		"", // No IDP session link for delegated tokens.
		actorID,
		session.UserClaims{
			Name:  validatedClaims.Name,
			Email: validatedClaims.Email,
		},
	)

	// Add the RFC 8693 Section 4.1 "act" claim identifying the acting party.
	// If the subject token itself carries an "act" claim (i.e. it is a
	// previously-delegated token being re-exchanged), nest it so the full
	// delegation chain remains auditable rather than being discarded.
	act := map[string]interface{}{"sub": actorID}
	if priorAct, ok := validatedClaims.Extra["act"]; ok && priorAct != nil {
		act["act"] = priorAct
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
// and the configured delegation lifespan. If the subject token has no expiry,
// the delegation lifespan is used as-is. Returns an error if the subject token
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

// checkDelegationConsent enforces the RFC 8693 §4.1 delegation consent check.
//
// If the subject token carries a may_act claim, it is the authoritative
// consent signal: only the party named in may_act.sub may delegate. The
// client_id binding is skipped in this case because may_act enables
// cross-client delegation (the token was issued to client A but authorizes
// client B to act).
//
// If may_act is absent, fall back to client_id binding: the subject token's
// client_id must match the authenticated client. This prevents a stolen
// subject token from being exchanged by a different client.
//
// If neither may_act nor client_id is present, the subject token carries no
// verifiable binding to any client at all — this fails closed (CWE-863)
// rather than allowing an unbound token through.
func checkDelegationConsent(validatedClaims *ValidatedClaims, actorID string) error {
	switch {
	case validatedClaims.MayAct != nil:
		if validatedClaims.MayAct.Sub != actorID {
			return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint(
				"The subject token does not authorize this client to act on behalf of the subject."))
		}
	case validatedClaims.ClientID != "" && validatedClaims.ClientID != actorID:
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
// allowedAudiences and grants it as an additional audience claim, binding
// the issued token to a specific resource server (e.g., an MCP server).
// Multiple resource parameters are rejected for security.
func grantResourceAudience(requester fosite.AccessRequester, allowedAudiences []string) error {
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
		if err := server.ValidateAudienceAllowed(resource, allowedAudiences); err != nil {
			return errorsx.WithStack(err)
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
// default exists rather than silently issuing an audience-less token.
func (h *Handler) grantDefaultAudience(requester fosite.AccessRequester) error {
	if len(requester.GetGrantedAudience()) > 0 {
		return nil
	}
	if len(h.allowedAudiences) == 1 {
		slog.Debug("no resource parameter, defaulting to sole allowed audience",
			"audience", h.allowedAudiences[0],
		)
		requester.GrantAudience(h.allowedAudiences[0])
		return nil
	}
	return errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
		"An explicit 'resource' or 'audience' parameter is required because no unambiguous default audience is configured."))
}
