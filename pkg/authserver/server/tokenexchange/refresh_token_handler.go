// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/x/errorsx"

	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// Compile-time check that RefreshTokenHandler implements fosite.TokenEndpointHandler.
var _ fosite.TokenEndpointHandler = (*RefreshTokenHandler)(nil)

// tokenTypeNotApplicable is the RFC 6749 token_type for a response whose
// access_token member is not an access token (RFC 8693 section 2.2.1).
const tokenTypeNotApplicable = "N_A"

// Reasons logged with a refusal. They are fixed strings; the
// authorization_details reasons are the ones returned to the client.
const (
	refuseUnsupportedType = "unsupported_token_type"
	refuseSubjectToken    = reasonSubjectInvalid
	refuseRequestParam    = "request_parameter_refused"
	refuseDetailsInvalid  = "authorization_details_invalid"
	refuseReplay          = "token_used"
	refuseBadToken        = "token_invalid"
	refuseUnknownGrant    = "not_found_or_other_client"
	refuseInternal        = "internal_error"
	refuseStore           = "store_failure"
)

// Reasons returned to the client as the whole error_description when a request
// presents a grant of its own client that cannot be used: token_used, revoked,
// parent_revoked and not_issuable. They are stable strings. A grant that is
// unknown, forged, another client's or expired gets the one code
// reasonGrantInvalid (errUnknownGrant).
const (
	reasonTokenUsed     = "token_used"
	reasonRevoked       = "revoked"
	reasonParentRevoked = "parent_revoked"
	reasonNotIssuable   = "not_issuable"
	reasonGrantInvalid  = "grant_invalid"
)

// errUnknownGrant is the one error returned when the presented refresh token
// fails its HMAC check, is not found, belongs to another client, has expired, or
// is not an exchange grant at all (a login refresh token). Its error_description
// is the same code for all of these, so a caller cannot tell the cases apart.
var errUnknownGrant = exactReason(fosite.ErrInvalidRequest.ErrorField, reasonGrantInvalid)

// Reasons for a refused subject token that are also sent to the client, as the
// error_description. reasonSubjectInvalid is the one logged and sent for every
// refusal a fresh login access token fixes; the other two are configuration
// faults that checkExchangeGrantSubject returns.
const (
	reasonSubjectInvalid      = "subject_token_invalid"
	reasonSubjectAudience     = "subject_token_audience"
	reasonSubjectSourceClient = "subject_token_source_client"
)

// isRefreshTokenExchange reports whether a token-exchange request involves the
// refresh-token type, as the requested or the subject token type.
// RefreshTokenHandler claims these requests and Handler claims all others, so
// the two cannot both handle one request. A request that names the refresh
// token as its subject and asks for an ID-JAG belongs here too.
func isRefreshTokenExchange(form url.Values) bool {
	return form.Get("requested_token_type") == oauthproto.TokenTypeRefreshToken ||
		form.Get("subject_token_type") == oauthproto.TokenTypeRefreshToken
}

// RefreshTokenHandler implements RFC 8693 token exchange for refresh tokens: a
// configured client presents a user's login access token and receives an
// opaque, non-rotating refresh token whose allowed use is fixed at issuance
// (see exchangeGrantType). The token is not stored as a fosite refresh token.
// Nothing is written to fosite's store, so grant_type=refresh_token cannot
// redeem it.
//
// Unlike Handler, the subject access token is single-use: the grant records its
// (iss, jti), and a second exchange of the same token is refused with
// token_used. The check and the write are one atomic store call, made in
// PopulateTokenEndpointResponse, so two concurrent exchanges produce one grant.
//
// A refresh-token subject is exchanged for a child grant (decideChild). Two
// derives from one grant give two independent children, since a child has no
// replay marker. If the response of a derive is lost, the child stays until its
// parent ends; this is accepted.
//
// A client without exchange_refresh_tokens is refused with the unsupported
// requested_token_type text (unsupportedTokenType), or with the unsupported
// subject_token_type text that validateFormParams returns when its subject is a
// refresh token. A refresh-token subject asking for any type but a refresh
// token is refused with the same subject text. All are invalid_request.
//
// The exchange grant is the only profile this handler issues today; a later
// profile would be chosen where decide checks the client's exchange_refresh_tokens.
type RefreshTokenHandler struct {
	strategy       oauth2.RefreshTokenStrategy
	store          storage.ExchangeGrantStorage // nil when no client enables the feature
	selfValidator  SubjectTokenValidator        // refresh tokens only come from self-issued login tokens
	registry       *authorizationdetails.Registry
	clientPolicies map[string]ConfiguredClientPolicy
}

// RefreshTokenFactory returns a server.Factory that creates the
// RefreshTokenHandler. It is registered whenever token exchange is enabled, so
// a request for a refresh token always reaches this handler, which refuses it
// for a client that has not enabled the feature. clientPolicies is the same
// read-only map Factory receives. The exchange-grant store is passed with
// WithExchangeGrantStorage; it may be nil only if no client has
// exchange_refresh_tokens.
//
// The authorization_details registry is built here, once, and startup fails if
// that fails. It is private to this handler.
func RefreshTokenFactory(
	clientPolicies map[string]ConfiguredClientPolicy, options ...FactoryOption,
) (server.Factory, error) {
	registry, err := newExchangeGrantRegistry()
	if err != nil {
		return nil, fmt.Errorf("tokenexchange: failed to build the exchange grant registry: %w", err)
	}
	resolved := resolveFactoryOptions(options)
	if resolved.exchangeGrantStore == nil {
		for id, policy := range clientPolicies {
			if len(policy.ExchangeRefreshTokens) > 0 {
				return nil, fmt.Errorf(
					"tokenexchange: client %q enables exchange_refresh_tokens but no exchange grant store was given", id)
			}
		}
	}
	return func(config *server.AuthorizationServerConfig, _ fosite.Storage, strategy any) (any, error) {
		refreshStrategy, ok := strategy.(oauth2.RefreshTokenStrategy)
		if !ok {
			return nil, fmt.Errorf("tokenexchange: strategy does not implement oauth2.RefreshTokenStrategy (got %T)", strategy)
		}
		selfValidator, err := NewSelfIssuedTokenValidator(
			config.PublicJWKS(), config.GetAccessTokenIssuer(), config.AllowedAudiences)
		if err != nil {
			return nil, fmt.Errorf("tokenexchange: failed to create subject token validator: %w", err)
		}
		return &RefreshTokenHandler{
			strategy:       refreshStrategy,
			store:          resolved.exchangeGrantStore,
			selfValidator:  selfValidator,
			registry:       registry,
			clientPolicies: clientPolicies,
		}, nil
	}, nil
}

// CanHandleTokenEndpointRequest claims token-exchange requests that involve the
// refresh-token type; see isRefreshTokenExchange.
func (*RefreshTokenHandler) CanHandleTokenEndpointRequest(_ context.Context, requester fosite.AccessRequester) bool {
	return requester.GetGrantTypes().ExactOne(oauthproto.GrantTypeTokenExchange) &&
		isRefreshTokenExchange(requester.GetRequestForm())
}

// CanSkipClientAuth always returns false: token exchange requires client
// authentication.
func (*RefreshTokenHandler) CanSkipClientAuth(_ context.Context, _ fosite.AccessRequester) bool {
	return false
}

// HandleTokenEndpointRequest decides the request only to fail early. Nothing is
// written and nothing is carried over: PopulateTokenEndpointResponse decides
// again, so the subject token is verified right before it is consumed.
func (h *RefreshTokenHandler) HandleTokenEndpointRequest(ctx context.Context, requester fosite.AccessRequester) error {
	if !h.CanHandleTokenEndpointRequest(ctx, requester) {
		return errorsx.WithStack(fosite.ErrUnknownRequest)
	}
	_, err := h.decide(ctx, requester, time.Now())
	return err
}

// PopulateTokenEndpointResponse decides the request again and issues the refresh
// token. The second decision verifies the subject token right before it is
// consumed. The grant carries the subject token's (iss, jti), so a reused
// subject token fails in issue with token_used. A child grant has no (iss, jti)
// marker, so token_used applies only to top-level grants. The response is the RFC 8693
// shape with token_type N_A and no refresh_token member; fosite writes
// Cache-Control: no-store and Pragma: no-cache.
func (h *RefreshTokenHandler) PopulateTokenEndpointResponse(
	ctx context.Context, requester fosite.AccessRequester, responder fosite.AccessResponder,
) error {
	if !h.CanHandleTokenEndpointRequest(ctx, requester) {
		return errorsx.WithStack(fosite.ErrUnknownRequest)
	}
	decision, err := h.decide(ctx, requester, time.Now())
	if err != nil {
		return err
	}
	return h.issue(ctx, requester, decision, responder)
}

// refuse logs a refusal at warn level with a stable reason and returns the error
// to send to the client, so a refusal is one return statement. The logger carries
// what was known when the request was refused; it never holds the refresh token,
// its signature or the subject token.
func refuse(ctx context.Context, log *slog.Logger, reason string, clientErr error) error {
	log.WarnContext(ctx, "Exchange grant refused", "reason", reason)
	return clientErr
}

// exchangeGrantDecision is the outcome of deciding a request: everything needed
// to write the grant and the response, with nothing yet written. grant has no
// Signature; issue fills it in from the token it generates. now is the instant
// that validated the entry, and the response reuses it so expires_in agrees with
// the echoed expires_at. A child grant sets grant.ParentSignature; a top-level
// grant leaves it empty.
type exchangeGrantDecision struct {
	grant storage.ExchangeGrant
	echo  json.RawMessage
	now   time.Time
	log   *slog.Logger
}

// decide validates the request, the subject token and the authorization_details
// entry, and returns what to issue. It writes nothing to the store and consumes
// nothing; its only effect is the refusal log line. It runs twice per request
// (in HandleTokenEndpointRequest and again in PopulateTokenEndpointResponse), so
// the authorization_details shape hook (pending.Grant) must be idempotent and
// free of side effects. A request logs at most one refusal, because a refusal in
// HandleTokenEndpointRequest ends the request. Each kind of subject has its own
// branch (decideTopLevel for a login access token, decideChild for a refresh
// token) that builds the same exchangeGrantDecision, which issue then writes.
// That is a different axis from a new profile, which changes what the grant
// allows, not what it is derived from.
func (h *RefreshTokenHandler) decide(
	ctx context.Context, requester fosite.AccessRequester, now time.Time,
) (*exchangeGrantDecision, error) {
	client := requester.GetClient()
	form := requester.GetRequestForm()
	log := slog.With("client_id", client.GetID())

	if client.IsPublic() {
		return nil, errorsx.WithStack(fosite.ErrInvalidGrant.WithHint(
			"The OAuth 2.0 Client is marked as public and is thus not allowed to use authorization grant 'token-exchange'."))
	}
	if !client.GetGrantTypes().Has(oauthproto.GrantTypeTokenExchange) {
		return nil, errorsx.WithStack(fosite.ErrUnauthorizedClient.WithHint(
			"The OAuth 2.0 Client is not allowed to use authorization grant 'token-exchange'."))
	}

	// The policy gate comes first: a client that has not enabled the feature gets
	// the unsupported-type text. A refresh-token subject that does not ask for a
	// refresh token is refused too; the text is the one validateFormParams returns
	// for an unsupported subject type.
	policy := h.clientPolicies[client.GetID()]
	subjectType := form.Get("subject_token_type")
	derive := subjectType == oauthproto.TokenTypeRefreshToken
	if derive && (len(policy.ExchangeRefreshTokens) == 0 ||
		form.Get("requested_token_type") != oauthproto.TokenTypeRefreshToken) {
		return nil, refuse(ctx, log, refuseUnsupportedType, errorsx.WithStack(fosite.ErrInvalidRequest.WithHintf(
			"The 'subject_token_type' value %q is not supported. Use %q or %q.",
			subjectType, oauthproto.TokenTypeAccessToken, oauthproto.TokenTypeJWT)))
	}
	// The client's policy chooses the profile; today the exchange grant is the
	// only one. A second profile (for example ordinary redeemable refresh tokens)
	// would be selected here by the client's configuration, one profile per client
	// and checked at startup, not by the shape of the request. A client with no
	// policy must keep meaning "nothing allowed".
	if len(policy.ExchangeRefreshTokens) == 0 {
		return nil, refuse(ctx, log, refuseUnsupportedType,
			errorsx.WithStack(unsupportedTokenType("requested_token_type", form.Get("requested_token_type"))))
	}
	if derive {
		return h.decideChild(ctx, requester, policy, log.With("has_parent", true), now)
	}
	return h.decideTopLevel(ctx, requester, policy, log, now)
}

// decideTopLevel decides a request that exchanges a login access token for a
// grant with no parent.
func (h *RefreshTokenHandler) decideTopLevel(
	ctx context.Context, requester fosite.AccessRequester, policy ConfiguredClientPolicy,
	log *slog.Logger, now time.Time,
) (*exchangeGrantDecision, error) {
	clientID := requester.GetClient().GetID()
	form := requester.GetRequestForm()
	if reason, err := checkRefreshTokenRequest(requester, form); err != nil {
		return nil, refuse(ctx, log, reason, errorsx.WithStack(err))
	}
	pending, err := parseExchangeGrantDetails(h.registry, form)
	if err != nil {
		reason, clientErr := exchangeGrantDetailsError(err)
		return nil, refuse(ctx, log, reason, clientErr)
	}

	claims, err := h.selfValidator.Validate(ctx, form.Get("subject_token"))
	if err != nil {
		slog.Debug("Subject token validation failed", "error", err, "actor", clientID)
		return nil, refuse(ctx, log, refuseSubjectToken,
			errorsx.WithStack(exactReason(fosite.ErrInvalidRequest.ErrorField, reasonSubjectInvalid)))
	}
	log = log.With("sub", claims.Subject, "source_jti", claims.JWTID)
	refusal := checkLoginToken(claims)
	if refusal == "" {
		refusal = checkExchangeGrantSubject(claims, policy)
	}
	if refusal != "" {
		return nil, refuse(ctx, log, refusal, errorsx.WithStack(loginSubjectError(refusal)))
	}

	echo, tokenType, expiresAt, err := decideEntry(ctx, pending, clientID, claims, policy, now)
	if err != nil {
		reason, clientErr := exchangeGrantDetailsError(err)
		return nil, refuse(ctx, log, reason, clientErr)
	}
	grant := storage.ExchangeGrant{
		ClientID:           clientID,
		Subject:            claims.Subject,
		SourceIssuer:       claims.Issuer,
		SourceJTI:          claims.JWTID,
		SourceExpiresAt:    claims.Expiry,
		IssuableTokenTypes: []string{tokenType},
		ExpiresAt:          expiresAt,
		CreatedAt:          now,
	}
	var jsonErr error
	if grant.SubjectIdentifier, jsonErr = claimJSON(claims.Extra, "sub_id"); jsonErr != nil {
		return nil, refuse(ctx, log, refuseInternal, issueFailed())
	}
	if grant.UpstreamSubject, jsonErr = claimJSON(claims.Extra, session.UpstreamSubjectClaimKey); jsonErr != nil {
		return nil, refuse(ctx, log, refuseInternal, issueFailed())
	}
	if grant.SubjectIdentifier != "" {
		log = log.With("sub_id", grant.SubjectIdentifier)
	}
	log = log.With("issuable_token_type", tokenType, "expires_at", expiresAt.Unix())
	return &exchangeGrantDecision{grant: grant, echo: echo, now: now, log: log}, nil
}

// claimJSON returns the JSON of the named claim, or "" when it is absent.
func claimJSON(extra map[string]any, key string) (string, error) {
	value, ok := extra[key]
	if !ok {
		return "", nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// decideChild decides a request that exchanges a refresh token for a child
// grant. The request parameters are checked first; they say nothing about the
// presented token. presentedGrant then applies the checks in the order that
// reveals nothing before the client check.
func (h *RefreshTokenHandler) decideChild(
	ctx context.Context, requester fosite.AccessRequester, policy ConfiguredClientPolicy,
	log *slog.Logger, now time.Time,
) (*exchangeGrantDecision, error) {
	clientID := requester.GetClient().GetID()
	form := requester.GetRequestForm()
	if reason, err := checkDeriveRequest(requester, form); err != nil {
		return nil, refuse(ctx, log, reason, errorsx.WithStack(err))
	}
	pending, err := parseExchangeGrantDetails(h.registry, form)
	if err != nil {
		reason, clientErr := exchangeGrantDetailsError(err)
		return nil, refuse(ctx, log, reason, clientErr)
	}
	parent, log, err := h.presentedGrant(ctx, clientID, form.Get("subject_token"), now, log)
	if err != nil {
		return nil, err
	}

	echo, tokenType, expiresAt, err := decideChildEntry(ctx, pending, clientID, parent, policy, now)
	if err != nil {
		reason, clientErr := exchangeGrantDetailsError(err)
		return nil, refuse(ctx, log, reason, clientErr)
	}
	grant := storage.ExchangeGrant{
		ClientID:           parent.ClientID,
		Subject:            parent.Subject,
		SubjectIdentifier:  parent.SubjectIdentifier,
		ParentSignature:    parent.Signature,
		IssuableTokenTypes: []string{tokenType},
		ExpiresAt:          expiresAt,
		CreatedAt:          now,
	}
	log = log.With("issuable_token_type", tokenType, "expires_at", expiresAt.Unix())
	return &exchangeGrantDecision{grant: grant, echo: echo, now: now, log: log}, nil
}

// presentedGrant returns the live grant behind a presented refresh token that
// may be derived from, with the logger extended by what the grant tells. The
// order is fixed: the HMAC check, which needs no store
// (ValidateRefreshToken is called with a session that has no refresh expiry, so
// it checks only the HMAC; RefreshTokenSignature does not verify it); the
// lookup; the client check; the grant's own liveness; its parent's liveness;
// then the refresh_token type.
// Nothing about the presented token is revealed before the client check. A
// child grant never lists refresh_token, so it fails the last check: depth is 1.
func (h *RefreshTokenHandler) presentedGrant(
	ctx context.Context, clientID, token string, now time.Time, log *slog.Logger,
) (*storage.ExchangeGrant, *slog.Logger, error) {
	if h.store == nil {
		return nil, log, refuse(ctx, log, refuseInternal, issueFailed())
	}
	hmacOnly := &fosite.Request{Session: &fosite.DefaultSession{}}
	if err := h.strategy.ValidateRefreshToken(ctx, hmacOnly, token); err != nil {
		return nil, log, refuse(ctx, log, refuseBadToken, errorsx.WithStack(errUnknownGrant))
	}
	grant, err := h.store.GetExchangeGrant(ctx, h.strategy.RefreshTokenSignature(ctx, token))
	switch {
	case errors.Is(err, storage.ErrNotFound), err == nil && grant.ClientID != clientID:
		return nil, log, refuse(ctx, log, refuseUnknownGrant, errorsx.WithStack(errUnknownGrant))
	case err != nil:
		log.WarnContext(ctx, "Exchange grant refused", "reason", refuseStore, "error", err)
		return nil, log, issueFailed()
	}
	log = log.With("sub", grant.Subject)
	if grant.SubjectIdentifier != "" {
		log = log.With("sub_id", grant.SubjectIdentifier)
	}

	// Expiry is checked before Live, which reports a revoked grant first. An
	// expired grant is answered like an unknown one whatever its revocation.
	if now.After(grant.ExpiresAt) {
		return nil, log, refuse(ctx, log, refuseUnknownGrant, errorsx.WithStack(errUnknownGrant))
	}
	if err := grant.Live(now); err != nil {
		if errors.Is(err, storage.ErrRevoked) {
			return nil, log, refuseWithReason(ctx, log, reasonRevoked)
		}
		return nil, log, refuse(ctx, log, refuseUnknownGrant, errorsx.WithStack(errUnknownGrant))
	}
	if grant.ParentSignature != "" {
		parent, err := h.store.GetExchangeGrant(ctx, grant.ParentSignature)
		switch {
		case err != nil && !errors.Is(err, storage.ErrNotFound):
			log.WarnContext(ctx, "Exchange grant refused", "reason", refuseStore, "error", err)
			return nil, log, issueFailed()
		case err != nil || parent.Live(now) != nil:
			return nil, log, refuseWithReason(ctx, log, reasonParentRevoked)
		}
	}
	if grant.ParentSignature != "" || !slices.Contains(grant.IssuableTokenTypes, oauthproto.TokenTypeRefreshToken) {
		return nil, log, refuseWithReason(ctx, log, reasonNotIssuable)
	}
	return grant, log, nil
}

// refuseWithReason logs the refusal and returns invalid_request whose whole
// error_description is reason.
func refuseWithReason(ctx context.Context, log *slog.Logger, reason string) error {
	return refuse(ctx, log, reason, errorsx.WithStack(exactReason(fosite.ErrInvalidRequest.ErrorField, reason)))
}

// decideChildEntry is decideEntry for a child: the shape hook returns the one
// entry and validateChildEntry applies the client's policy and the parent's
// lifetime to it. It also returns the token type the child allows.
func decideChildEntry(
	ctx context.Context, pending *authorizationdetails.Pending, clientID string,
	parent *storage.ExchangeGrant, policy ConfiguredClientPolicy, now time.Time,
) (json.RawMessage, string, time.Time, error) {
	granted, err := pending.Grant(ctx, authorizationdetails.Request{
		ClientID:           clientID,
		Actor:              clientID,
		RequestedTokenType: oauthproto.TokenTypeRefreshToken,
		Subject:            authorizationdetails.Subject{Subject: parent.Subject, ClientID: parent.ClientID},
	})
	if err != nil {
		return nil, "", time.Time{}, err
	}
	rawEntry, err := singleGrantedEntry(granted)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	entry, err := decodeExchangeGrantEntry(rawEntry)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	echo, expiresAt, err := validateChildDecoded(entry, parent, policy, now)
	return echo, entry.IssuableTokenTypes[0], expiresAt, err
}

// decideEntry asks the shape hook for the one entry and applies the client's
// policy to it. The returned echo comes from validateTopLevelDecoded, not from the
// hook, which returns the request unchanged. It also returns the token type the
// grant allows.
func decideEntry(
	ctx context.Context, pending *authorizationdetails.Pending, clientID string,
	claims *ValidatedClaims, policy ConfiguredClientPolicy, now time.Time,
) (json.RawMessage, string, time.Time, error) {
	granted, err := pending.Grant(ctx, authorizationdetails.Request{
		ClientID:           clientID,
		Actor:              clientID,
		RequestedTokenType: oauthproto.TokenTypeRefreshToken,
		Subject: authorizationdetails.Subject{
			Issuer: claims.Issuer, Subject: claims.Subject, ClientID: claims.ClientID, JWTID: claims.JWTID,
			Audience: slices.Clone(claims.Audience), IssuedAt: claims.IssuedAt, Expiry: claims.Expiry,
			Claims: deepCopyClaims(claims.Claims),
		},
	})
	if err != nil {
		return nil, "", time.Time{}, err
	}
	rawEntry, err := singleGrantedEntry(granted)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	entry, err := decodeExchangeGrantEntry(rawEntry)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	echo, expiresAt, err := validateTopLevelDecoded(entry, policy, now)
	return echo, entry.IssuableTokenTypes[0], expiresAt, err
}

// issue generates the refresh token, writes the grant and sets the response. It
// serves every kind of decision. The token comes from the refresh-token
// strategy, which needs no store.
func (h *RefreshTokenHandler) issue(
	ctx context.Context, requester fosite.AccessRequester, d *exchangeGrantDecision, responder fosite.AccessResponder,
) error {
	log := d.log
	if h.store == nil {
		return refuse(ctx, log, refuseInternal, issueFailed())
	}
	// The token uses fosite's refresh-token format and key, so it looks like an
	// ordinary refresh token. The format prefix is not covered by the signature
	// and is not a boundary. A distinct prefix is a follow-up to decide before
	// release. The server has no revocation or introspection endpoint today; one
	// added later must look these tokens up in ExchangeGrantStorage, because
	// fosite's store does not hold them.
	token, signature, err := h.strategy.GenerateRefreshToken(ctx, requester)
	if err != nil {
		log.WarnContext(ctx, "Exchange grant refused", "reason", refuseInternal, "error", err)
		return issueFailed()
	}
	grant := d.grant
	grant.Signature = signature
	// The grant goes only to ExchangeGrantStorage, never to fosite's store: which
	// store holds a token is what makes it non-redeemable at
	// grant_type=refresh_token. A profile field on the record is not the way to
	// tell kinds apart. A redeemable profile would live in fosite's store and
	// needs the single-use marker as its own store operation, written before the
	// session.
	err = h.store.CreateExchangeGrant(ctx, &grant)
	switch {
	case errors.Is(err, fosite.ErrJTIKnown):
		return refuse(ctx, log, refuseReplay, errorsx.WithStack(exactReason(
			fosite.ErrInvalidRequest.ErrorField, reasonTokenUsed)))
	case err != nil:
		log.WarnContext(ctx, "Exchange grant refused", "reason", refuseStore, "error", err)
		return issueFailed()
	}

	responder.SetAccessToken(token)
	responder.SetTokenType(tokenTypeNotApplicable)
	responder.SetExpiresIn(exchangeGrantExpiresIn(grant.ExpiresAt, d.now))
	responder.SetExtra("issued_token_type", oauthproto.TokenTypeRefreshToken)
	// RFC 9396 section 7.1: the response member is an array of the granted entries.
	responder.SetExtra(authorizationDetailsParam, []json.RawMessage{d.echo})
	if grant.ParentSignature != "" {
		log.DebugContext(ctx, "Exchange grant derived")
	} else {
		log.DebugContext(ctx, "Exchange grant created")
	}
	return nil
}

// exactReason builds an OAuth error whose error_description is exactly reason.
// It is a literal with no hint on purpose: fosite.ErrInvalidRequest carries a
// default HintField, so ErrInvalidRequest.WithDescription(reason) would append
// that hint to the reason. Use it for every stable client-visible reason.
func exactReason(code, reason string) *fosite.RFC6749Error {
	return &fosite.RFC6749Error{ErrorField: code, DescriptionField: reason, CodeField: http.StatusBadRequest}
}

// issueFailed is the generic server_error for a failure the client cannot act
// on; the cause is logged, not returned.
func issueFailed() error {
	return errorsx.WithStack(fosite.ErrServerError.WithHint("The refresh token could not be issued."))
}

// checkRefreshTokenRequest checks the request parameters of an access-token
// subject exchanged for a refresh token: the subject token is present and is an
// access token, then the shared checks of checkRefreshTokenParams. It returns the
// refusal reason to log with the error for the client.
func checkRefreshTokenRequest(requester fosite.AccessRequester, form url.Values) (string, error) {
	if reason, err := checkSubjectTokenPresent(form); err != nil {
		return reason, err
	}
	if got := form.Get("subject_token_type"); got != oauthproto.TokenTypeAccessToken {
		return refuseRequestParam, fosite.ErrInvalidRequest.WithHintf(
			"The 'subject_token_type' value %q is not supported when requesting a refresh token. Use %q.",
			got, oauthproto.TokenTypeAccessToken)
	}
	return checkRefreshTokenParams(requester, form)
}

// checkDeriveRequest checks the request parameters of a refresh-token subject
// exchanged for a child. decide has already checked the subject and requested
// token types.
func checkDeriveRequest(requester fosite.AccessRequester, form url.Values) (string, error) {
	if reason, err := checkSubjectTokenPresent(form); err != nil {
		return reason, err
	}
	return checkRefreshTokenParams(requester, form)
}

func checkSubjectTokenPresent(form url.Values) (string, error) {
	if form.Get("subject_token") == "" {
		return refuseRequestParam, fosite.ErrInvalidRequest.WithHint(
			"The 'subject_token' parameter is required for token exchange.")
	}
	return "", nil
}

// checkRefreshTokenParams checks the parameters that no request for a refresh
// token may carry: an actor token, scope, resource or audience.
func checkRefreshTokenParams(requester fosite.AccessRequester, form url.Values) (string, error) {
	if form.Has("actor_token") || form.Has("actor_token_type") {
		return refuseRequestParam, fosite.ErrInvalidRequest.WithHint(
			"The 'actor_token' parameter is not supported when requesting a refresh token.")
	}
	if len(requester.GetRequestedScopes()) > 0 {
		return refuseRequestParam, fosite.ErrInvalidScope.WithHint(
			"The 'scope' parameter is not supported when requesting a refresh token.")
	}
	if form.Has("resource") || form.Has("audience") {
		return refuseRequestParam, server.ErrInvalidTarget.WithHint(
			"The 'resource' and 'audience' parameters are not supported when requesting a refresh token.")
	}
	return "", nil
}

// loginSubjectError is the error for a reason from checkLoginToken or
// checkExchangeGrantSubject. An expired, invalid or wrong-kind subject token is
// fixed by a fresh access token, so the client gets reasonSubjectInvalid and the
// precise reason stays in the log. A token without the client's API audience or
// from a client that is not an allowed source is a configuration fault that a
// new access token cannot fix, so those carry their own reason code.
//
// This path returns invalid_request for an expired subject token and for one
// from a client that is not an allowed source, as RFC 8693 section 2.2.2
// requires. Handler returns invalid_grant for the same cases (see checkSourcePolicy
// and computeLifetime in handler.go). The difference is deliberate.
func loginSubjectError(reason string) error {
	switch reason {
	case reasonSubjectAudience, reasonSubjectSourceClient:
		return exactReason(fosite.ErrInvalidRequest.ErrorField, reason)
	}
	return exactReason(fosite.ErrInvalidRequest.ErrorField, reasonSubjectInvalid)
}

// checkLoginToken reports why a validated subject token is not a user login
// access token, or "" if it is. A refresh token of any kind needs this, because
// the token is tied to a real login. A token minted by token exchange (act), by
// the JWT-bearer or ID-JAG grant (external subject or no upstream session) is
// refused, since its subject did not log in here.
//
// The jti is checked here because a later profile must share the single-use
// (iss, jti) marker, so one login access token cannot open one grant of each
// kind. A second profile would call checkSourcePolicy instead of
// checkExchangeGrantSubject.
func checkLoginToken(claims *ValidatedClaims) string {
	_, hasAct := claims.Extra["act"]
	_, hasExternalIssuer := claims.Extra["external_issuer"]
	_, noUpstream := claims.Extra[session.NoUpstreamSessionClaimKey]
	switch {
	case hasAct:
		return "subject_token_delegated"
	// claims.ExternalIssuer is always empty for the self-issued validator; it is
	// checked as defense in depth.
	case hasExternalIssuer || claims.ExternalIssuer != "" || strings.Contains(claims.Subject, "#"):
		return "subject_token_external"
	case noUpstream:
		return "subject_token_no_upstream_session"
	case claims.ClientID == "":
		return "subject_token_missing_client_id"
	case claims.JWTID == "":
		return "subject_token_missing_jti"
	}
	return ""
}

// checkExchangeGrantSubject reports why a login token is not acceptable to the
// exchange-grant profile, or "" if it is. The audience and source-client rules
// are the profile's own policy; the jti is checked in checkLoginToken. Unlike
// checkSourcePolicy, the token must have been issued to one of
// the policy's allowed source clients; the exchanging client's own tokens are
// not accepted.
func checkExchangeGrantSubject(claims *ValidatedClaims, policy ConfiguredClientPolicy) string {
	switch {
	case policy.APIAudience == "" || !slices.Contains(claims.Audience, policy.APIAudience):
		return reasonSubjectAudience
	case !slices.Contains(policy.AllowedSourceClients, claims.ClientID):
		return reasonSubjectSourceClient
	}
	return ""
}

// exchangeGrantDetailsError maps an error from the exchange-grant rules to the
// error for the client and the reason to log. The exchange-grant validators return plain
// authorizationdetails.Error values; their Reason is the error_description.
// Anything else the package or the hook produced is internal and its text is
// never sent to the client.
func exchangeGrantDetailsError(err error) (string, error) {
	var typed authorizationdetails.Error
	var oauthErr *fosite.RFC6749Error
	switch {
	case errors.As(err, &typed):
		return typed.Reason, errorsx.WithStack(exactReason(string(typed.Code), typed.Reason))
	case errors.As(err, &oauthErr):
		return refuseDetailsInvalid, errorsx.WithStack(err)
	default:
		return refuseInternal, errorsx.WithStack(fosite.ErrServerError.WithDescription(
			"The authorization_details could not be processed."))
	}
}
