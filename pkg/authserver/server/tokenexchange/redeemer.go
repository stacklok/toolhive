// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"errors"
	"maps"
	"sync"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/x/errorsx"

	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// redeemer is the shared tail of JWT-bearer and ID-JAG redemption. It is not a
// fosite handler. It deliberately does not embed issuanceDeps: embedding would
// put the unfiltered trusted issuer list and allowed audiences one field away
// from both handlers at request time.
type redeemer struct {
	consumer storage.AssertionJWTConsumer
	config   *fosite.Config
	helper   *oauth2.HandleHelper

	// detailsWarned records the assertion issuers whose authorization_details
	// refusal was already logged at Warn. Only validated assertions reach it,
	// so its size is bounded by the trusted issuers.
	detailsWarned sync.Map
}

// redemption is what a handler's front half decided about a validated
// assertion.
type redemption struct {
	claims   *ValidatedClaims
	resource string
	// subject is the subject of the issued token: <iss>#<sub> for a namespaced
	// redemption, the local user ID for a resolved one.
	subject       string
	replayPurpose string
	replayKey     string
	// carryAct copies the assertion's act claim into the issued token.
	carryAct bool
	// delegatedAccess is set only for a resolved redemption from a resolve
	// issuer. The session builder stamps the delegated-access marker and the
	// lifetime step caps the lifetime.
	delegatedAccess bool
	// syntheticClientID, when set, replaces the request's client with a
	// synthetic one of that ID, and is the client_id the issued token carries.
	// When empty the request's own client is kept and its ID is the token's
	// client_id.
	syntheticClientID string
}

// delegatedAccessMarkerCap is the longest lifetime of a token that carries the
// delegated-access marker.
const delegatedAccessMarkerCap = 5 * time.Minute

// newRedeemer builds the redeemer from validated dependencies. Callers run
// deps.validate first, so it cannot fail.
func newRedeemer(deps issuanceDeps) *redeemer {
	return &redeemer{
		consumer: deps.consumer,
		config:   deps.config,
		helper: &oauth2.HandleHelper{
			AccessTokenStrategy: deps.strategy,
			AccessTokenStorage:  deps.tokenStorage,
			Config:              deps.config,
		},
	}
}

// complete finishes a redemption whose front half accepted the assertion: it
// consumes the assertion, bounds the token lifetime, and installs the session
// and granted audience on req.
func (r *redeemer) complete(ctx context.Context, req fosite.AccessRequester, d redemption) error {
	// Consume before issuing. This intentionally fails closed: if issuance fails
	// after this point, the assertion remains consumed rather than becoming
	// replayable.
	if err := r.consumeAssertion(ctx, d.replayPurpose, d.claims.Issuer, d.replayKey, d.claims.Expiry); err != nil {
		return err
	}
	lifetime, err := r.assertionBoundedLifetime(ctx, d)
	if err != nil {
		return err
	}
	clientID := d.syntheticClientID
	if clientID != "" {
		// Callers set syntheticClientID for a grant that skips client
		// authentication (CanSkipClientAuth), where fosite never populates the
		// request's client. Attach a synthetic one so every storage backend's
		// marshal path (which calls GetClient unconditionally) has a real,
		// non-nil client to serialize.
		accessRequest, ok := req.(*fosite.AccessRequest)
		if !ok {
			return errorsx.WithStack(fosite.ErrServerError.WithHint("The JWT-bearer grant requires a *fosite.AccessRequest."))
		}
		// Always replace any resolved caller client. Fosite accepts a supplied
		// public client_id without a secret; retaining it could renew a DCR client
		// registration TTL after a clientless grant succeeds.
		accessRequest.Client = storage.NewSyntheticClient(clientID)
	} else {
		clientID = req.GetClient().GetID()
	}
	req.GrantAudience(d.resource)
	req.SetSession(newRedemptionSession(d, clientID, lifetime))
	return nil
}

// populate issues an access token bounded by the session expiry complete set.
// Each handler gates on its own CanHandleTokenEndpointRequest before calling
// it.
func (r *redeemer) populate(ctx context.Context, req fosite.AccessRequester, resp fosite.AccessResponder) error {
	return issueBoundedAccessToken(ctx, r.helper, r.config.GetAccessTokenLifespan(ctx), req, resp,
		func() error { return invalidAssertion("The JWT bearer assertion has expired.") })
}

// consumeAssertion marks an assertion's replay key used, mapping consumer
// errors onto the grant's wire errors. Shared by both JWT-bearer handlers;
// purpose keeps their replay namespaces separate.
func (r *redeemer) consumeAssertion(
	ctx context.Context, purpose, issuer, replayKey string, expiry time.Time,
) error {
	if err := r.consumer.ConsumeAssertionJWT(ctx, purpose, issuer, replayKey, expiry); err != nil {
		if errors.Is(err, fosite.ErrJTIKnown) {
			return invalidAssertion("The JWT bearer assertion has already been used.")
		}
		// A non-ErrJTIKnown failure here is a storage/operational problem (e.g. a
		// replay-store outage), not evidence the caller sent a bad assertion —
		// it must not be indistinguishable from a genuine invalid_grant.
		return errorsx.WithStack(fosite.ErrServerError.WithHint("The JWT bearer assertion could not be consumed.").
			WithWrap(err).WithDebug(err.Error()))
	}
	return nil
}

// assertionBoundedLifetime returns the issued token's lifetime: the
// configured access-token lifespan, capped by the assertion's remaining
// validity so a token derived from an assertion can never outlive it, and, for
// a token that carries the delegated-access marker, by
// delegatedAccessMarkerCap.
func (r *redeemer) assertionBoundedLifetime(ctx context.Context, d redemption) (time.Duration, error) {
	remaining := time.Until(d.claims.Expiry)
	if remaining <= 0 {
		return 0, invalidAssertion("The JWT bearer assertion has expired.")
	}
	lifetime := min(r.config.GetAccessTokenLifespan(ctx), remaining)
	// Last, so the cap wins over every earlier bound.
	if d.delegatedAccess {
		lifetime = min(lifetime, delegatedAccessMarkerCap)
	}
	return lifetime, nil
}

// issuedAct returns the act claim for the issued token. For a marked token the
// outer iss is this server, so an act without an iss would be read as acted by
// this server's issuer; actor.Effective reads it as the assertion's issuer, so
// that issuer is written into a copy. The assertion's claims are not changed.
// An explicit iss, nested actors and other redemptions are left as they are.
func issuedAct(act any, d redemption) any {
	m, ok := act.(map[string]any)
	if !ok || !d.delegatedAccess {
		return act
	}
	if _, present := m["iss"]; present {
		return act
	}
	filled := maps.Clone(m)
	filled["iss"] = d.claims.Issuer
	return filled
}

// newRedemptionSession builds the bounded access-token session for a
// redeemed assertion. The caller supplies the subject in d and the resolved
// client ID.
func newRedemptionSession(d redemption, clientID string, lifetime time.Duration) *session.Session {
	issuedSession := session.New(d.subject, "", clientID, session.UserClaims{})
	// This grant links to no upstream IdP login, so the issued token can never
	// carry an upstream credential. Say so in the token rather than leaving a
	// resource server to infer it from the absence of a tsid claim, which is
	// also how an unenriched or foreign-issued identity looks. See
	// claimnames.NoUpstreamSessionKey.
	issuedSession.JWTClaims.Extra[claimnames.NoUpstreamSessionKey] = true
	// Only when carryAct is set, carry the assertion's actor chain (RFC 8693
	// §4.1 "act", the requesting agent's identity) into the issued token, so a
	// resource server or auditor can still tell WHICH agent acted for the
	// subject after redemption. Copied as is, except for the iss fill in
	// issuedAct: act is defined as a nested-JSON claim and
	// buildValidatedClaims left it untouched in Extra.
	if d.carryAct {
		if act, ok := d.claims.Extra["act"]; ok {
			issuedSession.JWTClaims.Extra["act"] = issuedAct(act, d)
		}
	}
	// Mark the token as issued under delegated access.
	if d.delegatedAccess {
		issuedSession.JWTClaims.Extra[claimnames.DelegatedAccessKey] = true
	}
	issuedSession.SetExpiresAt(fosite.AccessToken, time.Now().UTC().Add(lifetime))
	return issuedSession
}
