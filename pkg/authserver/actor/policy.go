// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package actor

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Stable reasons in Decision.Reason.
const (
	ReasonAllowed          = "allowed"
	ReasonNoActor          = "no_actor"
	ReasonIssuerNotAllowed = "actor_issuer_not_allowed"
)

// DefaultPolicy allows an actor whose issuer is the token issuer or one of the
// issuers configured to act through it. There is one policy per token issuer.
type DefaultPolicy struct {
	tokenIssuer string
	issuers     map[string]struct{}
}

var _ Policy = (*DefaultPolicy)(nil)

// NewDefaultPolicy returns a DefaultPolicy for tokens issued by tokenIssuer.
// The actor's issuer must be tokenIssuer or one of actorIssuers, which may be
// empty.
//
// act.iss is not required to equal the token issuer: a ToolHive authorization
// server names its agents under a different issuer (for example a SPIFFE trust
// domain), and the server refuses an actor issuer equal to its own issuer.
//
// tokenIssuer must be non-empty. Empty issuers are refused, and so is any
// issuer containing #, because Principal is issuer#subject and splitting on the
// first # must recover both parts.
func NewDefaultPolicy(tokenIssuer string, actorIssuers []string) (*DefaultPolicy, error) {
	if tokenIssuer == "" {
		return nil, errors.New("actor policy: token issuer is empty")
	}
	p := &DefaultPolicy{
		tokenIssuer: tokenIssuer,
		issuers:     make(map[string]struct{}, len(actorIssuers)+1),
	}
	if strings.Contains(tokenIssuer, "#") {
		return nil, fmt.Errorf("actor policy: token issuer %q contains #", tokenIssuer)
	}
	p.issuers[tokenIssuer] = struct{}{}
	for _, iss := range actorIssuers {
		if iss == "" {
			return nil, errors.New("actor policy: allowed actor issuer is empty")
		}
		if strings.Contains(iss, "#") {
			return nil, fmt.Errorf("actor policy: allowed actor issuer %q contains #", iss)
		}
		p.issuers[iss] = struct{}{}
	}
	return p, nil
}

// Evaluate implements Policy. An Actor without an issuer or a subject counts as
// no actor. A request whose TokenIssuer is not the policy's returns an error,
// because the policy cannot decide for another issuer.
//
// Evaluate trusts Request.Actor to come from Effective with the same
// TokenIssuer and ClientID. It does not re-validate a hand-built Actor against
// the token's claims; it only treats an empty issuer or subject as no actor.
// It checks only the actor's issuer, so it does not restrict actors per subject
// or per client: any actor subject at an allowed issuer passes.
func (p *DefaultPolicy) Evaluate(_ context.Context, req Request) (Decision, error) {
	if req.TokenIssuer != p.tokenIssuer {
		return Decision{}, fmt.Errorf("actor policy: request is for another token issuer; this policy is for %q",
			p.tokenIssuer)
	}
	a := req.Actor
	if a == nil || a.Issuer == "" || a.Subject == "" {
		return Decision{Reason: ReasonNoActor}, nil
	}
	if _, ok := p.issuers[a.Issuer]; !ok {
		return Decision{Reason: ReasonIssuerNotAllowed, Source: a.Source}, nil
	}
	return Decision{
		Allow:     true,
		Reason:    ReasonAllowed,
		Principal: a.Issuer + "#" + a.Subject,
		Source:    a.Source,
	}, nil
}
