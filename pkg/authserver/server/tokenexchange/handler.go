// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/x/errorsx"

	"github.com/stacklok/toolhive/pkg/authserver/server/session"
)

// RFC 8693 grant type and token type URIs.
const (
	// GrantTypeTokenExchange is the grant_type value for RFC 8693 token exchange.
	GrantTypeTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange" //nolint:gosec // not a credential

	// TokenTypeAccessToken is the RFC 8693 token type URI for access tokens.
	TokenTypeAccessToken = "urn:ietf:params:oauth:token-type:access_token" //nolint:gosec // not a credential

	// TokenTypeJWT is the RFC 8693 token type URI for JWT tokens.
	TokenTypeJWT = "urn:ietf:params:oauth:token-type:jwt" //nolint:gosec // not a credential

	// TokenTypeIDToken is the RFC 8693 token type URI for ID tokens.
	TokenTypeIDToken = "urn:ietf:params:oauth:token-type:id_token" //nolint:gosec // not a credential
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
	validator          SubjectTokenValidator // for subject tokens (multi-issuer)
	selfValidator      SubjectTokenValidator // for actor tokens (self-issued only)
	delegationLifespan time.Duration
	config             tokenExchangeConfig
}

// formParams holds the validated RFC 8693 form parameters extracted from a
// token exchange request.
type formParams struct {
	subjectToken     string
	subjectTokenType string
	actorToken       string // empty if not provided
	actorTokenType   string // empty if not provided
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
	return requester.GetGrantTypes().ExactOne(GrantTypeTokenExchange)
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

	form := requester.GetRequestForm()

	// Validate required RFC 8693 form parameters.
	params, err := validateFormParams(form)
	if err != nil {
		return err
	}

	// Validate the subject token against the configured token validator.
	validatedClaims, err := h.validator.Validate(ctx, params.subjectToken)
	if err != nil {
		slog.Debug("Subject token validation failed", "error", err)
		return errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"The subject token is invalid or could not be verified.").WithWrap(err))
	}

	// Resolve actor identity: explicit actor_token or authenticated client.
	actorSub, err := h.resolveActorIdentity(ctx, params, client)
	if err != nil {
		return err
	}

	// Validate that each requested scope is allowed for this client.
	for _, scope := range requester.GetRequestedScopes() {
		if !h.config.GetScopeStrategy(ctx)(client.GetScopes(), scope) {
			return errorsx.WithStack(fosite.ErrInvalidScope.WithHintf(
				"The OAuth 2.0 Client is not allowed to request scope '%s'.", scope))
		}
		requester.GrantScope(scope)
	}

	// Validate that the requested audience is allowed for this client.
	if err := h.config.GetAudienceStrategy(ctx)(client.GetAudience(), requester.GetRequestedAudience()); err != nil {
		return errorsx.WithStack(err)
	}
	for _, aud := range requester.GetRequestedAudience() {
		requester.GrantAudience(aud)
	}

	// Build the delegated session with the user's identity and the agent's act claim.
	delegatedSession := session.New(
		validatedClaims.Subject,
		"", // No IDP session link for delegated tokens.
		actorSub,
		session.UserClaims{
			Name:  validatedClaims.Name,
			Email: validatedClaims.Email,
		},
	)

	// Add the RFC 8693 Section 4.1 "act" claim identifying the acting party.
	delegatedSession.JWTClaims.Extra["act"] = map[string]interface{}{
		"sub": actorSub,
	}

	// Compute the delegated token lifetime: the shorter of the subject token's
	// remaining lifetime and the configured delegation lifespan.
	lifetime, err := h.computeLifetime(validatedClaims.Expiry)
	if err != nil {
		return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint(
			"The subject token has expired.").WithWrap(err))
	}
	delegatedSession.SetExpiresAt(fosite.AccessToken, time.Now().UTC().Add(lifetime))

	requester.SetSession(delegatedSession)

	slog.Debug("Token exchange request validated",
		"subject", validatedClaims.Subject,
		"actor", actorSub,
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

	if !requester.GetClient().GetGrantTypes().Has(GrantTypeTokenExchange) {
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
		if remaining > 0 && remaining < atLifespan {
			atLifespan = remaining
		}
	}

	if _, err := h.IssueAccessToken(ctx, atLifespan, requester, responder); err != nil {
		return err
	}

	// Per RFC 8693 Section 2.2.1, the response MUST include issued_token_type.
	responder.SetExtra("issued_token_type", TokenTypeAccessToken)

	return nil
}

// resolveActorIdentity determines the acting party identity. If an explicit
// actor_token is present it is validated against the AS's own JWKS and bound
// to the authenticated client ID. Otherwise the authenticated OAuth client is
// the acting party.
func (h *Handler) resolveActorIdentity(
	ctx context.Context, params *formParams, client fosite.Client,
) (string, error) {
	if params.actorToken != "" {
		// Validate actor_token against the AS's own JWKS (must be self-issued).
		actorClaims, err := h.selfValidator.Validate(ctx, params.actorToken)
		if err != nil {
			slog.Debug("Actor token validation failed", "error", err)
			return "", errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
				"The actor token is invalid or could not be verified.").WithWrap(err))
		}
		// Binding check: actor_token.sub MUST match the authenticated client ID.
		// This prevents replay attacks where a leaked actor token is presented
		// by a different client. The client ID is always verified by fosite's
		// client authentication before reaching here.
		if actorClaims.Subject != client.GetID() {
			return "", errorsx.WithStack(fosite.ErrInvalidGrant.WithHint(
				"The actor token subject does not match the authenticated client identity."))
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

	switch subjectTokenType {
	case TokenTypeAccessToken, TokenTypeJWT, TokenTypeIDToken:
		// Valid subject token types.
	default:
		return nil, errorsx.WithStack(fosite.ErrInvalidRequest.WithHintf(
			"The 'subject_token_type' value %q is not supported. Use %q, %q, or %q.",
			subjectTokenType, TokenTypeAccessToken, TokenTypeJWT, TokenTypeIDToken))
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
		case TokenTypeAccessToken, TokenTypeJWT:
			// Valid actor token types.
		default:
			return nil, errorsx.WithStack(fosite.ErrInvalidRequest.WithHintf(
				"The 'actor_token_type' value %q is not supported. Use %q or %q.",
				actorTokenType, TokenTypeAccessToken, TokenTypeJWT))
		}
	}

	return &formParams{
		subjectToken:     subjectToken,
		subjectTokenType: subjectTokenType,
		actorToken:       actorToken,
		actorTokenType:   actorTokenType,
	}, nil
}

// computeLifetime returns the minimum of the subject token's remaining lifetime
// and the configured delegation lifespan. If the subject token has no expiry,
// the delegation lifespan is used as-is. Returns an error if the subject token
// has already expired.
func (h *Handler) computeLifetime(subjectExpiry time.Time) (time.Duration, error) {
	if subjectExpiry.IsZero() {
		return h.delegationLifespan, nil
	}

	remaining := time.Until(subjectExpiry)
	if remaining <= 0 {
		return 0, fmt.Errorf("subject token expired %v ago", -remaining)
	}

	if remaining < h.delegationLifespan {
		return remaining, nil
	}
	return h.delegationLifespan, nil
}
