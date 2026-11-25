// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/x/errorsx"

	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// idJAGDecision is the outcome of deciding a request that mints an ID-JAG from
// an exchange grant, with nothing yet signed.
type idJAGDecision struct {
	mint idJAGMint
	// owner is the user the grant acts for. Its sub_id is logged only on the
	// Debug mint line. Upstream is set only for a client with an actor profile,
	// whose validator receives it.
	owner GrantOwner
	// actor is the client's actor profile, nil for a client without one.
	// actorToken is the checked request value, set only with a profile.
	actor      *ActorProfile
	actorToken string
	// log carries what a refusal may log; nothing above Debug carries sub_id or
	// the upstream identity.
	log *slog.Logger
	// now is the instant that checked the grant. For a client without an actor
	// profile the mint reuses it, so exp and expires_in agree with that check.
	// For a client with a profile the mint is at the later instant at which the
	// validator's answer was checked (see issueIDJAG).
	now time.Time
}

// decideIDJAG decides a request that exchanges an exchange grant's refresh
// token for an ID-JAG. The order is fixed. The request parameters and the
// target come first and read nothing from the store. presentedGrant then
// applies the grant checks in the order that reveals nothing before the client
// check. Then the grant must list the ID-JAG output. Then every subject claim
// the grant recorded is decoded, including ones the target does not require, so
// a stored value that is not JSON is an internal error for every target. Then
// the claims the target requires must have been recorded on the grant. Last,
// for a client with an actor profile, the grant's upstream identity must be
// complete or absent (grantUpstreamIdentity). It writes nothing and never calls
// the actor validator, because it runs in both handler phases.
func (h *RefreshTokenHandler) decideIDJAG(
	ctx context.Context, requester fosite.AccessRequester, policy ConfiguredClientPolicy,
	log *slog.Logger, now time.Time,
) (*idJAGDecision, error) {
	clientID := requester.GetClient().GetID()
	form := requester.GetRequestForm()
	shape, err := checkIDJAGRequest(form, policy)
	if err != nil {
		return nil, refuse(ctx, log, refuseRequestParam, err)
	}
	log = log.With("target", shape.Target.Issuer, "resource", shape.Resource)

	grant, log, err := h.presentedGrant(ctx, clientID, form.Get("subject_token"), now, log)
	if err != nil {
		return nil, err
	}
	hasParent := grant.ParentSignature != ""
	log = log.With("has_parent", hasParent)
	if !slices.Contains(grant.IssuableTokenTypes, oauthproto.TokenTypeIDJAG) {
		return nil, refuseWithReason(ctx, log, reasonNotIssuable)
	}

	// The client's current cap bounds grants it already holds, as derive
	// bounds new children, so lowering id_jag_max_lifetime limits old grants.
	// A grant past the cap is refused like an expired one, so a client cannot
	// tell the two apart. Every create path sets CreatedAt, so a zero value is
	// a corrupt record; refusing it keeps the cap from being skipped.
	if grant.CreatedAt.IsZero() {
		return nil, refuse(ctx, log, refuseInternal,
			serverFailure(hintMintFailed, errors.New("exchange grant has no creation time")))
	}
	capped := grant.CreatedAt.Add(policy.ExchangeRefreshTokens[oauthproto.TokenTypeIDJAG])
	if capped.Unix() <= now.Unix() {
		return nil, refuse(ctx, log, refuseUnknownGrant, errorsx.WithStack(errUnknownGrant))
	}
	notAfter := []time.Time{grant.ExpiresAt, capped}

	available, err := grantSubjectClaims(grant)
	if err != nil {
		return nil, refuse(ctx, log, refuseInternal, serverFailure(hintMintFailed, err))
	}
	// Selecting here refuses a missing required claim in the first decision.
	// The mint gets every available claim and selects again, as its contract
	// says; the result is the same.
	if _, reason, err := selectSubjectClaims(shape.Target, available); err != nil {
		return nil, refuse(ctx, log, reason, err)
	}
	owner, err := idJAGGrantOwner(grant, policy.ActorProfile)
	if err != nil {
		return nil, refuse(ctx, log, refuseInternal, serverFailure(hintMintFailed, err))
	}

	return &idJAGDecision{
		mint: idJAGMint{
			Issuer:             h.issuer,
			RequestingClientID: clientID,
			Subject:            idJAGSubject{Subject: grant.Subject, Claims: available},
			Target:             shape.Target,
			Resource:           shape.Resource,
			NotAfter:           notAfter,
		},
		owner:      owner,
		actor:      policy.ActorProfile,
		actorToken: shape.ActorToken,
		log:        log,
		now:        now,
	}, nil
}

// issueIDJAG signs the ID-JAG of a decision made in PopulateTokenEndpointResponse
// and writes the response. It writes nothing to the store: the grant is neither
// consumed nor rotated, so the same grant may mint again with a new jti.
//
// For a client with an actor profile, the actor validator runs here, once per
// request and never in HandleTokenEndpointRequest, as the last check before the
// mint. The ID-JAG then names the agent in act, is minted at the instant the
// validator's answer was checked (iat), and ends no later than the credential
// does. For other clients it is minted at d.now. The grant is not read again
// after the validator: a revoke that commits while the validator runs does not
// stop this ID-JAG, which ends at its own short expiry, but a grant that
// expires meanwhile is refused as invalid_grant by the mint's bounds check.
func (h *RefreshTokenHandler) issueIDJAG(
	ctx context.Context, d *idJAGDecision, responder fosite.AccessResponder,
) error {
	mint, now := d.mint, d.now
	var res ActorResult
	if d.actor != nil {
		if err := h.refuseTerminalActor(ctx, d); err != nil {
			return err
		}
		answer, checkedAt, err := h.validateIDJAGActor(ctx, d)
		if err != nil {
			return err
		}
		res = answer
		mint, now = idJAGMintWithActor(d.mint, d.actor.ActIssuer, res), checkedAt
	}
	minted, reason, err := respondWithIDJAG(ctx, h.signer, allowAllIDJAGPolicy, mint, responder, now)
	if err != nil {
		return refuse(ctx, d.log, reason, err)
	}
	attrs := []any{"jti", minted.JTI}
	if d.owner.SubjectIdentifier != "" {
		attrs = append(attrs, "sub_id", d.owner.SubjectIdentifier)
	}
	if d.actor != nil {
		attrs = append(attrs, "act_sub", res.AgentID)
		if res.CredentialSubject != "" {
			attrs = append(attrs, "actor_credential_subject", res.CredentialSubject)
		}
	}
	d.log.DebugContext(ctx, "ID-JAG minted", attrs...)
	return nil
}

// refuseTerminalActor refuses an actor token that carries the delegated-access
// marker or an authorization_details claim and was signed by this server,
// before the actor validator sees it. The marker is checked first, so a token
// that carries both keeps the marker's reason. A signature from this server's
// keys and a matching issuer are enough to refuse: an expired token, one with
// an audience this server does not accept, or one that is otherwise not a
// valid subject token is still refused, because those checks can only make a
// token less trustworthy to accept, not make a restricted one safe to pass on.
// A token that does not parse, was not signed by this server, or names another
// issuer goes on to the client's validator unchanged, which decides on it: an
// external actor's authorization_details would describe the agent's own
// limits, not the subject's grant.
func (h *RefreshTokenHandler) refuseTerminalActor(ctx context.Context, d *idJAGDecision) error {
	claims, err := h.selfValidator.signedByThisServer(d.actorToken)
	if err != nil {
		d.log.DebugContext(ctx, "actor token is not self-issued; passed to the actor validator",
			"client_id", d.mint.RequestingClientID)
		return nil
	}
	r := restrictionsOf(claims)
	switch {
	case r.DelegatedAccess:
		return refuseWithReason(ctx, d.log, reasonActorDelegatedAccess)
	case r.AuthorizationDetails:
		return refuseWithReason(ctx, d.log, reasonActorAuthorizationDetails)
	}
	return nil
}

// idJAGMintWithActor returns a copy of m that names the agent in act and ends
// no later than the actor credential. act.iss is the issuer registered for the
// client's validator, never m.Issuer. The cap goes on a copy of NotAfter, so
// the helper never writes to the caller's slice.
//
// act.sub is the validator's AgentID, which may differ from the actor
// credential's own sub (ActorResult.CredentialSubject). The individual draft
// draft-mcguinness-oauth-actor-profile-01 section 6.4 requires the credential's
// sub instead. Whether to follow it is deferred while the draft is not adopted.
// The same draft (section 3.4) recommends a sub_profile member in act, such as
// "ai_agent". act carries only iss and sub until the draft is adopted and a
// consumer reads sub_profile; adding it later only adds a member.
func idJAGMintWithActor(m idJAGMint, actIssuer string, res ActorResult) idJAGMint {
	m.Act = &actClaim{Issuer: actIssuer, Subject: res.AgentID}
	m.NotAfter = append(slices.Clone(m.NotAfter), res.ExpiresAt)
	return m
}

// validateIDJAGActor calls the client's actor validator once for the decision
// and returns its accepted answer with the instant it was checked. A failure is
// logged once at Warn with its reason and the cause that validateActor has
// already redacted, and the client error is returned as validateActor built it.
func (h *RefreshTokenHandler) validateIDJAGActor(
	ctx context.Context, d *idJAGDecision,
) (ActorResult, time.Time, error) {
	req := ActorRequest{
		ActorToken:     d.actorToken,
		ActorTokenType: d.actor.ActorTokenType,
		ClientID:       d.mint.RequestingClientID,
		Grant:          d.owner,
		Target:         ActorTarget{Issuer: d.mint.Target.Issuer, Resource: d.mint.Resource},
	}
	res, checkedAt, reason, err := validateActor(ctx, d.actor.Validator, h.actorLimiter, req, h.clock, h.actorTimeout)
	if err != nil {
		attrs := []any{"reason", reason}
		// Every error validateActor returns is an *fosite.RFC6749Error; its
		// cause is absent only for an expired result.
		var oauthErr *fosite.RFC6749Error
		if errors.As(err, &oauthErr) && oauthErr.Unwrap() != nil {
			attrs = append(attrs, "error", oauthErr.Unwrap())
		}
		d.log.WarnContext(ctx, "Exchange grant refused", attrs...)
		return ActorResult{}, time.Time{}, err
	}
	return res, checkedAt, nil
}

// idJAGGrantOwner returns the user a grant acts for. Upstream is filled only for
// a client with an actor profile, whose validator receives it; for a client
// without one the grant's recorded claims are not checked further here.
func idJAGGrantOwner(grant *storage.ExchangeGrant, profile *ActorProfile) (GrantOwner, error) {
	owner := GrantOwner{Subject: grant.Subject, SubjectIdentifier: grant.SubjectIdentifier}
	if profile == nil {
		return owner, nil
	}
	upstream, err := grantUpstreamIdentity(grant)
	if err != nil {
		return GrantOwner{}, err
	}
	owner.Upstream = upstream
	return owner, nil
}

// grantUpstreamIdentity builds the user's upstream identity from the sub_id and
// upstream_subject a grant recorded at login. session.SetSubjectIdentifier
// writes both together, so neither recorded gives the zero value and only one
// is a fault. When both are recorded, sub_id must be an iss_sub object and every
// member of both must be a non-empty string, with equal iss values. An error
// names the claim and the problem, never a value or the decoder's text.
func grantUpstreamIdentity(grant *storage.ExchangeGrant) (UpstreamIdentity, error) {
	if grant.SubjectIdentifier == "" && grant.UpstreamSubject == "" {
		return UpstreamIdentity{}, nil
	}
	if grant.SubjectIdentifier == "" || grant.UpstreamSubject == "" {
		return UpstreamIdentity{}, errors.New("the grant recorded only one of sub_id and upstream_subject")
	}
	var subID struct{ Format, Iss, Sub *string }
	if err := json.Unmarshal([]byte(grant.SubjectIdentifier), &subID); err != nil {
		return UpstreamIdentity{}, errors.New("the grant's stored sub_id is not a JSON object of strings")
	}
	var upstream struct{ Iss, Claim, Value *string }
	if err := json.Unmarshal([]byte(grant.UpstreamSubject), &upstream); err != nil {
		return UpstreamIdentity{}, errors.New("the grant's stored upstream_subject is not a JSON object of strings")
	}
	// A pointer, so a later null member resets an earlier duplicate to nil.
	val := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	id := UpstreamIdentity{
		Issuer: val(subID.Iss), Subject: val(subID.Sub), StableClaim: val(upstream.Claim), StableValue: val(upstream.Value),
	}
	if val(subID.Format) != "iss_sub" {
		return UpstreamIdentity{}, errors.New("the grant's stored sub_id has a format other than iss_sub")
	}
	if id.Issuer == "" || id.Subject == "" || val(upstream.Iss) == "" || id.StableClaim == "" || id.StableValue == "" {
		return UpstreamIdentity{}, errors.New("the grant's stored sub_id or upstream_subject has a missing or empty member")
	}
	if id.Issuer != val(upstream.Iss) {
		return UpstreamIdentity{}, errors.New("the grant's stored sub_id and upstream_subject name different issuers")
	}
	return id, nil
}

// grantSubjectClaims returns the subject claims a grant recorded at login,
// keyed by wire key. A claim the grant did not record is absent. A recorded
// value that is not JSON is an error; the message names the claim, not the
// value.
func grantSubjectClaims(grant *storage.ExchangeGrant) (map[string]json.RawMessage, error) {
	available := make(map[string]json.RawMessage, 2)
	for _, recorded := range []struct{ key, value string }{
		{claimnames.UpstreamSubjectKey, grant.UpstreamSubject},
		{claimnames.SubjectIdentifierKey, grant.SubjectIdentifier},
	} {
		if recorded.value == "" {
			continue
		}
		if !json.Valid([]byte(recorded.value)) {
			return nil, fmt.Errorf("the grant's stored %s is not valid JSON", recorded.key)
		}
		available[recorded.key] = json.RawMessage(recorded.value)
	}
	return available, nil
}
