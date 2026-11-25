// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/ory/fosite"
	"github.com/ory/x/errorsx"

	"github.com/stacklok/toolhive/pkg/authserver/actor"
)

const (
	hintActorCheckFailed = "The actor could not be checked."
	reasonActorMalformed = "actor_malformed"
	reasonActorPolicyErr = "actor_policy_error"
)

// newActorPolicies builds the actor policy of each resolve issuer: it accepts
// the ID-JAG's own issuer and actor_issuers. Namespaced issuers get none.
func newActorPolicies(policies map[string]*JWTBearerGrantPolicy) (map[string]actor.Policy, error) {
	actorPolicies := make(map[string]actor.Policy)
	for _, issuerURL := range slices.Sorted(maps.Keys(policies)) {
		policy := policies[issuerURL]
		if policy.SubjectMode != SubjectModeResolve {
			continue
		}
		p, err := actor.NewDefaultPolicy(issuerURL, policy.ActorIssuers)
		if err != nil {
			return nil, fmt.Errorf("issuer_url %q: actor policy: %w", issuerURL, err)
		}
		actorPolicies[issuerURL] = p
	}
	return actorPolicies, nil
}

// checkActorIssuer refuses an ID-JAG from a resolve issuer whose effective
// actor is missing or malformed, or comes from an issuer other than the
// ID-JAG's own or one in actor_issuers. It needs no storage, so it runs before
// the gate and before resolution; checkActorPolicy runs after resolution.
// Namespaced issuers are not checked.
func (h *IDJAGHandler) checkActorIssuer(
	ctx context.Context, claims *ValidatedClaims, policy *JWTBearerGrantPolicy, clientID string,
) error {
	if _, ok := h.actorPolicies[claims.Issuer]; !ok {
		return nil
	}
	a, err := actor.Effective(actor.SourceAct, claims.Extra, claims.Issuer, clientID)
	if err != nil {
		reason := reasonActorMalformed
		if errors.Is(err, actor.ErrNoActor) {
			reason = actor.ReasonNoActor
		}
		return h.refuseActor(ctx, claims.Issuer, clientID, reason)
	}
	// DefaultPolicy repeats this check on purpose. actorPolicies holds the
	// Policy interface, so a future implementation might not check the issuer,
	// and actor_issuers must still bind whatever policy is in place.
	if a.Issuer != claims.Issuer && !slices.Contains(policy.ActorIssuers, a.Issuer) {
		return h.refuseActor(ctx, claims.Issuer, clientID, actor.ReasonIssuerNotAllowed)
	}
	return nil
}

// checkActorPolicy asks the issuer's actor policy whether the actor may act
// for the resolved user, who is a local user ID. A denial is refused as
// actor_denied; a policy that cannot decide is a server error, not a denial.
// It runs after resolution and before the ID-JAG is consumed. Namespaced
// issuers are not checked.
func (h *IDJAGHandler) checkActorPolicy(
	ctx context.Context, claims *ValidatedClaims, clientID, resource, user string,
) error {
	actorPolicy, ok := h.actorPolicies[claims.Issuer]
	if !ok {
		return nil
	}
	// checkActorIssuer accepted this actor, so Effective cannot fail here.
	a, err := actor.Effective(actor.SourceAct, claims.Extra, claims.Issuer, clientID)
	if err != nil {
		return h.refuseActor(ctx, claims.Issuer, clientID, reasonActorMalformed)
	}
	decision, err := actorPolicy.Evaluate(ctx, actor.Request{
		TokenIssuer: claims.Issuer, ClientID: clientID, Actor: a, User: user, Resource: resource,
	})
	if err != nil {
		// The cause travels in the returned error, which is logged once.
		h.logActorRefusal(ctx, slog.LevelWarn, claims.Issuer, clientID, reasonActorPolicyErr)
		return serverFailure(hintActorCheckFailed, err)
	}
	if !decision.Allow {
		return h.refuseActor(ctx, claims.Issuer, clientID, decision.Reason)
	}
	return nil
}

func (h *IDJAGHandler) refuseActor(ctx context.Context, issuer, clientID, reason string) error {
	h.logActorRefusal(ctx, slog.LevelDebug, issuer, clientID, reason)
	return errorsx.WithStack(redemptionRefusal(fosite.ErrAccessDenied.ErrorField, reasonActorDenied, ""))
}

// logActorRefusal logs one line for a refused actor check. A denial is logged
// at Debug: a caller can trigger it at request rate and already gets the
// refusal, so the line is only for diagnosis. A policy that cannot decide is
// logged at Warn: it is a server fault the caller did not cause, and operators
// need to see it. The line carries no subject, jti or act value.
func (h *IDJAGHandler) logActorRefusal(
	ctx context.Context, level slog.Level, issuer, clientID, reason string,
) {
	logger := h.logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Log(ctx, level, "ID-JAG redemption refused: actor check failed",
		"issuer", issuer, "client_id", clientID, "reason", reason)
}
