// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/x/errorsx"

	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// Compile-time check that RefreshTokenHandler implements fosite.TokenEndpointHandler.
var _ fosite.TokenEndpointHandler = (*RefreshTokenHandler)(nil)

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
// refusal a fresh login access token fixes. The audience and source-client
// reasons are configuration faults that checkExchangeGrantSubject returns.
// reasonSubjectOfflineAccess is not fixed by a fresh access token either. The
// user must sign in again, and the source client must request offline_access or
// offline at sign-in; a refreshed token keeps its original scopes.
const (
	reasonSubjectInvalid       = "subject_token_invalid"
	reasonSubjectAudience      = "subject_token_audience"
	reasonSubjectSourceClient  = "subject_token_source_client"
	reasonSubjectOfflineAccess = "subject_token_offline_access"
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
	refreshScopes  fosite.RefreshTokenScopesProvider
	signer         *server.Signer // will sign ID-JAGs; nothing reads it yet
	issuer         string         // the access-token issuer
}

// RefreshTokenOption configures RefreshTokenFactory. Factory takes FactoryOption
// instead, so an option cannot be passed to the wrong factory.
type RefreshTokenOption func(*refreshTokenOptions)

type refreshTokenOptions struct {
	exchangeGrantStore storage.ExchangeGrantStorage
}

// WithExchangeGrantStorage gives RefreshTokenFactory the store that holds the
// grants behind issued refresh tokens. It may be nil when no client enables
// exchange_refresh_tokens; the handler then refuses every request for a refresh
// token before it needs the store.
func WithExchangeGrantStorage(store storage.ExchangeGrantStorage) RefreshTokenOption {
	return func(o *refreshTokenOptions) { o.exchangeGrantStore = store }
}

// RefreshTokenFactory returns a server.Factory that creates the
// RefreshTokenHandler. It is registered whenever token exchange is enabled, so
// a request for a refresh token always reaches this handler, which refuses it
// for a client that has not enabled the feature. clientPolicies is the same
// read-only map Factory receives. The exchange-grant store is passed with
// WithExchangeGrantStorage; it may be nil only if no client has
// exchange_refresh_tokens.
//
// signer is required and must come from server.NewSigner. Construction also
// fails for any IDJAGTarget in clientPolicies that is not structurally usable
// (empty issuer, a map key that is not the target's issuer, empty client_id, no
// or an empty resource, a lifetime that is not positive).
//
// The authorization_details registry is built here, once, and startup fails if
// that fails. It is private to this handler.
func RefreshTokenFactory(
	clientPolicies map[string]ConfiguredClientPolicy, signer *server.Signer, options ...RefreshTokenOption,
) (server.Factory, error) {
	if signer == nil {
		return nil, fmt.Errorf("tokenexchange: RefreshTokenFactory requires a signer")
	}
	clientIDs := slices.Sorted(maps.Keys(clientPolicies))
	for _, id := range clientIDs {
		targets := clientPolicies[id].IDJAGTargets
		for _, key := range slices.Sorted(maps.Keys(targets)) {
			if err := checkIDJAGTargetUsable(id, key, targets[key]); err != nil {
				return nil, err
			}
		}
	}
	registry, err := newExchangeGrantRegistry()
	if err != nil {
		return nil, fmt.Errorf("tokenexchange: failed to build the exchange grant registry: %w", err)
	}
	resolved := resolveRefreshTokenOptions(options)
	if resolved.exchangeGrantStore == nil {
		for _, id := range clientIDs {
			if len(clientPolicies[id].ExchangeRefreshTokens) > 0 {
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
			refreshScopes:  config,
			signer:         signer,
			issuer:         config.GetAccessTokenIssuer(),
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
	// the unsupported-type text. A refresh-token subject is refused too when it
	// does not ask for a refresh token, or when the client's policy no longer
	// allows refresh tokens, so removing the cap stops derives from source grants the
	// client already holds. The text is the one validateFormParams returns for an
	// unsupported subject type.
	policy := h.clientPolicies[client.GetID()]
	subjectType := form.Get("subject_token_type")
	derive := subjectType == oauthproto.TokenTypeRefreshToken
	_, refreshAllowed := policy.ExchangeRefreshTokens[oauthproto.TokenTypeRefreshToken]
	if derive && (!refreshAllowed ||
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
	// Configuration faults are reported first; a missing offline_access is fixed
	// only by a new sign-in that grants it.
	if refusal == "" {
		refusal = checkOfflineAccess(claims, h.refreshScopes.GetRefreshTokenScopes(ctx))
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
	if grant.SubjectIdentifier, jsonErr = claimJSON(claims.Extra, claimnames.SubjectIdentifierKey); jsonErr != nil {
		return nil, refuse(ctx, log, refuseInternal, issueFailed())
	}
	if grant.UpstreamSubject, jsonErr = claimJSON(claims.Extra, claimnames.UpstreamSubjectKey); jsonErr != nil {
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
// presented token. presentedGrant then runs its checks in the order its own
// comment describes, so nothing is revealed before the client check. Only then
// must the presented grant be top-level and list refresh_token, so a child
// grant is refused here: depth is 1. The presented grant is the source: it
// becomes the parent of the new child.
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
	source, log, err := h.presentedGrant(ctx, clientID, form.Get("subject_token"), now, log)
	if err != nil {
		return nil, err
	}
	if source.ParentSignature != "" || !slices.Contains(source.IssuableTokenTypes, oauthproto.TokenTypeRefreshToken) {
		return nil, refuseWithReason(ctx, log, reasonNotIssuable)
	}

	echo, tokenType, expiresAt, err := decideChildEntry(ctx, pending, clientID, source, policy, now)
	if err != nil {
		reason, clientErr := exchangeGrantDetailsError(err)
		return nil, refuse(ctx, log, reason, clientErr)
	}
	grant := storage.ExchangeGrant{
		ClientID:           source.ClientID,
		Subject:            source.Subject,
		SubjectIdentifier:  source.SubjectIdentifier,
		UpstreamSubject:    source.UpstreamSubject,
		ParentSignature:    source.Signature,
		IssuableTokenTypes: []string{tokenType},
		ExpiresAt:          expiresAt,
		CreatedAt:          now,
	}
	log = log.With("issuable_token_type", tokenType, "expires_at", expiresAt.Unix())
	return &exchangeGrantDecision{grant: grant, echo: echo, now: now, log: log}, nil
}

// presentedGrant returns the live grant behind a presented refresh token (the
// source grant), with the logger extended by what the grant tells. The order is
// fixed: the HMAC check, which needs no store
// (ValidateRefreshToken is called with a session that has no refresh expiry, so
// it checks only the HMAC; RefreshTokenSignature does not verify it); the
// lookup, which is storage.LiveExchangeGrant: the grant's own liveness and its
// parent's; the client check; the whole-second check; then checkPresentedGrant,
// which turns the lookup's result into a refusal.
// Nothing about the presented token is revealed before the client check. The
// whole-second check comes right after it: expires_at is whole seconds, so a
// grant with no whole second left is refused like an expired one
// (reasonGrantInvalid), whatever else is wrong with it. Which output the grant
// may produce is not checked here; the caller (decideChild) checks that after
// this returns.
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
	grant, err := storage.LiveExchangeGrant(ctx, h.store, h.strategy.RefreshTokenSignature(ctx, token), now)
	if errors.Is(err, storage.ErrNotFound) || grant != nil && grant.ClientID != clientID {
		return nil, log, refuse(ctx, log, refuseUnknownGrant, errorsx.WithStack(errUnknownGrant))
	}
	if grant != nil {
		log = log.With("sub", grant.Subject)
		if grant.SubjectIdentifier != "" {
			log = log.With("sub_id", grant.SubjectIdentifier)
		}
	}
	if grant != nil && grant.ExpiresAt.Unix() <= now.Unix() {
		return nil, log, refuse(ctx, log, refuseUnknownGrant, errorsx.WithStack(errUnknownGrant))
	}
	if err := checkPresentedGrant(ctx, err, log); err != nil {
		return nil, log, err
	}
	return grant, log, nil
}

// checkPresentedGrant maps the result of storage.LiveExchangeGrant for a grant
// of the presenting client to a refusal. liveErr is the error LiveExchangeGrant
// returned. ErrParentNotLive means the presented grant's own parent is not
// live. It logs the refusal it returns.
func checkPresentedGrant(ctx context.Context, liveErr error, log *slog.Logger) error {
	switch {
	case liveErr == nil:
	case errors.Is(liveErr, storage.ErrExpired):
		// presentedGrant's whole-second check already refuses every expired grant,
		// so this branch is a guard in case that order changes.
		return refuse(ctx, log, refuseUnknownGrant, errorsx.WithStack(errUnknownGrant))
	case errors.Is(liveErr, storage.ErrRevoked):
		return refuseWithReason(ctx, log, reasonRevoked)
	case errors.Is(liveErr, storage.ErrParentNotLive):
		return refuseWithReason(ctx, log, reasonParentRevoked)
	default:
		log.WarnContext(ctx, "Exchange grant refused", "reason", refuseStore, "error", liveErr)
		return issueFailed()
	}
	return nil
}

// refuseWithReason logs the refusal and returns invalid_request whose whole
// error_description is reason.
func refuseWithReason(ctx context.Context, log *slog.Logger, reason string) error {
	return refuse(ctx, log, reason, errorsx.WithStack(exactReason(fosite.ErrInvalidRequest.ErrorField, reason)))
}

// decideChildEntry is decideEntry for a child: the shape hook returns the one
// entry and validateChildEntry applies the client's policy and the source's
// lifetime to it. It also returns the token type the child allows.
func decideChildEntry(
	ctx context.Context, pending *authorizationdetails.Pending, clientID string,
	source *storage.ExchangeGrant, policy ConfiguredClientPolicy, now time.Time,
) (json.RawMessage, string, time.Time, error) {
	granted, err := pending.Grant(ctx, authorizationdetails.Request{
		ClientID:           clientID,
		Actor:              clientID,
		RequestedTokenType: oauthproto.TokenTypeRefreshToken,
		Subject:            authorizationdetails.Subject{Subject: source.Subject, ClientID: source.ClientID},
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
	echo, expiresAt, err := validateChildDecoded(entry, source, policy, now)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	return echo, entry.IssuableTokenTypes[0], expiresAt, nil
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
	if err != nil {
		return nil, "", time.Time{}, err
	}
	return echo, entry.IssuableTokenTypes[0], expiresAt, nil
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
	responder.SetTokenType(oauthproto.TokenTypeNotApplicable)
	responder.SetExpiresIn(exchangeGrantExpiresIn(grant.ExpiresAt, d.now))
	responder.SetExtra(oauthproto.ParamIssuedTokenType, oauthproto.TokenTypeRefreshToken)
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

// loginSubjectError is the error for a reason from checkLoginToken,
// checkExchangeGrantSubject or checkOfflineAccess. An expired, invalid or
// wrong-kind subject token is fixed by a fresh access token, so the client gets
// reasonSubjectInvalid and the precise reason stays in the log. A token without
// one of the client's subject_token_audiences or from a client that is not an
// allowed source is a configuration fault that a new access token cannot fix,
// so those carry their own reason code. So does a token without
// offline_access. The user must sign in again, and the source client must
// request offline_access or offline at sign-in; a refreshed token keeps its
// original scopes.
//
// This path returns invalid_request for an expired subject token, for a wrong
// audience and for one from a client that is not an allowed source, as RFC 8693
// section 2.2.2 requires. Handler returns invalid_grant for the same cases (see
// checkSourcePolicy, checkSubjectTokenAudience and computeLifetime in
// handler.go). The difference is deliberate.
func loginSubjectError(reason string) error {
	switch reason {
	case reasonSubjectAudience, reasonSubjectSourceClient, reasonSubjectOfflineAccess:
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
	_, hasExternalIssuer := claims.Extra[claimnames.ExternalIssuerKey]
	_, noUpstream := claims.Extra[claimnames.NoUpstreamSessionKey]
	switch {
	case hasAct:
		return "subject_token_delegated"
	// claims.ExternalIssuer is always empty for the self-issued validator; it is
	// checked as defense in depth. Native login subjects never contain "#"; see
	// claimnames.QualifySubject for the qualified form.
	case hasExternalIssuer || claims.ExternalIssuer != "" || claimnames.IsQualifiedSubject(claims.Subject):
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
// are the profile's own policy; the jti is checked in checkLoginToken. The token
// must carry one of the policy's SubjectTokenAudiences (an empty list refuses).
// Unlike checkSourcePolicy, the token must also have been issued to one of the
// policy's allowed source clients; the exchanging client's own tokens are not
// accepted. The audience is checked first, so a token that fails both reports
// the audience.
func checkExchangeGrantSubject(claims *ValidatedClaims, policy ConfiguredClientPolicy) string {
	switch {
	case !carriesAnyAudience(claims, policy.SubjectTokenAudiences):
		return reasonSubjectAudience
	case !slices.Contains(policy.AllowedSourceClients, claims.ClientID):
		return reasonSubjectSourceClient
	}
	return ""
}

// checkOfflineAccess reports reasonSubjectOfflineAccess when the subject
// token's scope contains none of refreshScopes, or "" if it does. An exchange
// grant outlives the sign-in session, so opening one needs offline_access
// granted at sign-in. This is ToolHive policy modeled on OIDC Core section 11,
// which also allows refresh tokens in other contexts; the spec does not
// require it.
//
// The scope set and the empty-list semantics (an empty refreshScopes means no
// gate) are those of fosite's canIssueRefreshToken. Matching differs: it is
// case-sensitive, as RFC 6749 section 3.3 scopes are, while fosite's HasOneOf
// ignores case. fosite also silently omits the refresh token where this path
// refuses the request, because the refresh token is this path's only output.
//
// Only opening a grant is checked here. A child grant derived from a refresh
// token must not be checked again: the scope was checked when the parent grant
// opened, and a refresh-token subject carries no scope.
func checkOfflineAccess(claims *ValidatedClaims, refreshScopes []string) string {
	if len(refreshScopes) == 0 {
		return ""
	}
	granted := strings.Fields(claims.Scopes)
	for _, scope := range refreshScopes {
		if slices.Contains(granted, scope) {
			return ""
		}
	}
	return reasonSubjectOfflineAccess
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

func resolveRefreshTokenOptions(options []RefreshTokenOption) refreshTokenOptions {
	var resolved refreshTokenOptions
	for _, option := range options {
		option(&resolved)
	}
	return resolved
}

// checkIDJAGTargetUsable checks that a target is safe to use as configured: the
// zero value of any field would make the handler sign an unusable ID-JAG. It
// does not check limits that config validation owns.
func checkIDJAGTargetUsable(clientID, key string, target IDJAGTarget) error {
	switch {
	case target.Issuer == "":
		return fmt.Errorf("tokenexchange: client %q id_jag target %q has an empty issuer", clientID, key)
	case target.Issuer != key:
		return fmt.Errorf("tokenexchange: client %q id_jag target is stored under %q but its issuer is %q",
			clientID, key, target.Issuer)
	case target.ClientID == "":
		return fmt.Errorf("tokenexchange: client %q id_jag target %q has an empty client_id", clientID, key)
	case len(target.Resources) == 0:
		return fmt.Errorf("tokenexchange: client %q id_jag target %q has no resources", clientID, key)
	case slices.Contains(target.Resources, ""):
		return fmt.Errorf("tokenexchange: client %q id_jag target %q has an empty resource", clientID, key)
	case target.Lifetime <= 0:
		return fmt.Errorf("tokenexchange: client %q id_jag target %q lifetime must be positive, got %v",
			clientID, key, target.Lifetime)
	}
	return nil
}
