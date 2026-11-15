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
// an exchange grant, with nothing yet signed. now is the instant that checked
// the grant; the mint reuses it, so exp and expires_in agree with that check.
type idJAGDecision struct {
	mint idJAGMint
	// subID is the grant's recorded sub_id JSON; logged only on the Debug mint line.
	subID string
	// log carries what a refusal may log; nothing above Debug carries sub_id.
	log *slog.Logger
	now time.Time
}

// decideIDJAG decides a request that exchanges an exchange grant's refresh
// token for an ID-JAG. The order is fixed. The request parameters and the
// target come first and read nothing from the store. presentedGrant then
// applies the grant checks in the order that reveals nothing before the client
// check. Then the grant must list the ID-JAG output. Then every subject claim
// the grant recorded is decoded, including ones the target does not require, so
// a stored value that is not JSON is an internal error for every target. Last,
// the claims the target requires must have been recorded on the grant. It
// writes nothing.
func (h *RefreshTokenHandler) decideIDJAG(
	ctx context.Context, requester fosite.AccessRequester, policy ConfiguredClientPolicy,
	log *slog.Logger, now time.Time,
) (*idJAGDecision, error) {
	clientID := requester.GetClient().GetID()
	form := requester.GetRequestForm()
	shape, err := checkIDJAGRequest(form, policy)
	if err != nil {
		reason := refuseRequestParam
		if policy.ActorProfile != nil {
			reason = refuseActorUnavailable
		}
		return nil, refuse(ctx, log, reason, err)
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

	return &idJAGDecision{
		mint: idJAGMint{
			Issuer:             h.issuer,
			RequestingClientID: clientID,
			Subject:            idJAGSubject{Subject: grant.Subject, Claims: available},
			Target:             shape.Target,
			Resource:           shape.Resource,
			NotAfter:           notAfter,
		},
		subID: grant.SubjectIdentifier,
		log:   log,
		now:   now,
	}, nil
}

// issueIDJAG signs the ID-JAG of a decision made in PopulateTokenEndpointResponse
// and writes the response. It writes nothing to the store: the grant is neither
// consumed nor rotated, so the same grant may mint again with a new jti.
func (h *RefreshTokenHandler) issueIDJAG(
	ctx context.Context, d *idJAGDecision, responder fosite.AccessResponder,
) error {
	minted, reason, err := respondWithIDJAG(ctx, h.signer, allowAllIDJAGPolicy, d.mint, responder, d.now)
	if err != nil {
		return refuse(ctx, d.log, reason, err)
	}
	attrs := []any{"jti", minted.JTI}
	if d.subID != "" {
		attrs = append(attrs, "sub_id", d.subID)
	}
	d.log.DebugContext(ctx, "ID-JAG minted", attrs...)
	return nil
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
