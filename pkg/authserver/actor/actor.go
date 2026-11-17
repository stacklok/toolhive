// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package actor decides whether the agent acting on a user's behalf, named by
// a delegated token, is allowed.
//
// It authorizes the actor a token already names, when the token is presented:
// the outermost act claim, or the client under SourceClient. Checking an
// actor's credential before ToolHive issues a token, and so deciding what goes
// into act, is tokenexchange.ActorTokenValidator.
package actor

import (
	"context"
	"errors"
	"fmt"
)

// Where an identity provider names the actor.
const (
	// SourceAct takes the actor from the token's outermost act claim. It is
	// the default.
	SourceAct = "act"
	// SourceClient takes the actor from the client the token was issued to,
	// for providers that never emit act. There is no fallback between sources.
	SourceClient = "client"
)

var (
	// ErrNoActor means the source is "act" and the token has no act claim.
	ErrNoActor = errors.New("token has no actor")
	// ErrMalformedActor means the act claim, or the input that stands in for
	// it, cannot name an actor.
	ErrMalformedActor = errors.New("malformed actor")
)

// Actor is the agent a token names as acting on the user's behalf.
type Actor struct {
	// Source is where the identity provider names the actor.
	Source string
	// Issuer is act.iss, or the token's issuer when act has none.
	Issuer string
	// Subject is act.sub, or the client ID when Source is SourceClient.
	Subject string
	// Claims is a copy of the outermost act object without its nested act
	// member, so a prior actor cannot drive a decision. It is nil when Source
	// is SourceClient. DefaultPolicy does not read it; it is there for Policy
	// implementations that do.
	Claims map[string]any
}

// Request is what a Policy decides on.
type Request struct {
	TokenIssuer string
	// ClientID is a separate input. It is never the actor, except when the
	// issuer's source is SourceClient.
	ClientID string
	// Actor is nil when the token has no actor.
	Actor    *Actor
	User     string
	Resource string
}

// Decision is a Policy's answer.
type Decision struct {
	Allow bool
	// Reason is a stable string callers map to a reason code.
	Reason string
	// Principal is the actor identity for session binding and audit.
	Principal string
	// Source is where the actor came from, SourceAct or SourceClient, copied
	// from the Actor for audit. It is empty when there was no actor.
	Source string
}

// Policy decides whether an actor may act for a user. A Decision that does not
// allow is a denial. A non-nil error means the policy could not decide (for
// example a directory-backed policy that is unavailable, or a request for
// another token issuer): the Decision does not allow, and callers answer with a
// server error rather than a denial, like subject.ErrUnavailable.
type Policy interface {
	Evaluate(ctx context.Context, req Request) (Decision, error)
}

// Effective returns the actor a token names. Only the outermost act counts
// (RFC 8693 section 4.1): Claims is a deep copy of it with the nested act
// removed, and does not alias the caller's map or any map[string]any or []any
// inside it (the shapes JSON decoding produces). An act without iss takes
// tokenIssuer, so providers that omit it (Okta) work.
//
// For SourceClient the actor is the client, with tokenIssuer as its issuer.
// An empty source means SourceAct.
//
// Effective is the strict parser for authorization decisions: it fails closed,
// unlike the lenient audit parser audit.ParseDelegationChain (toolhive-core),
// which never fails.
func Effective(source string, claims map[string]any, tokenIssuer, clientID string) (*Actor, error) {
	switch source {
	case SourceClient:
		if tokenIssuer == "" || clientID == "" {
			return nil, fmt.Errorf("%w: client source needs an issuer and a client ID", ErrMalformedActor)
		}
		return &Actor{Source: SourceClient, Issuer: tokenIssuer, Subject: clientID}, nil
	case SourceAct, "":
	default:
		return nil, fmt.Errorf("unknown actor source %q", source)
	}

	raw, ok := claims["act"]
	if !ok || raw == nil {
		return nil, ErrNoActor
	}
	act, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: act is not an object", ErrMalformedActor)
	}
	sub, _ := act["sub"].(string)
	if sub == "" {
		return nil, fmt.Errorf("%w: act has no sub", ErrMalformedActor)
	}
	iss := tokenIssuer
	if v, present := act["iss"]; present {
		s, isString := v.(string)
		if !isString || s == "" {
			return nil, fmt.Errorf("%w: act.iss is not a non-empty string", ErrMalformedActor)
		}
		iss = s
	}
	if iss == "" {
		return nil, fmt.Errorf("%w: act has no iss and the token has none", ErrMalformedActor)
	}
	actClaims := make(map[string]any, len(act))
	for k, v := range act {
		if k != "act" {
			actClaims[k] = deepCopy(v)
		}
	}
	return &Actor{Source: SourceAct, Issuer: iss, Subject: sub, Claims: actClaims}, nil
}

// deepCopy copies the JSON-shaped containers (map[string]any and []any)
// recursively and returns every other value as is.
func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = deepCopy(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = deepCopy(e)
		}
		return out
	default:
		return v
	}
}
