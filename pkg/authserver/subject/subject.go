// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package subject maps a user's identity at an issuer, one claim and its
// value, to the local user it belongs to. Callers build the key from the token
// they hold: the upstream_subject claim of an ID-JAG from a ToolHive
// authorization server, or the configured binding claim of a token that an
// identity provider issued directly.
package subject

import (
	"context"
	"errors"
)

// Ref is a lookup key for a subject: one claim, with its value, from one issuer.
//
// It has the shape of the https://toolhive.dev/upstream_subject claim
// ({iss, claim, value}). From ToolHive's own record of an upstream login
// (session.UpstreamSubject, tokenexchange.UpstreamIdentity) it is
// {Issuer, StableClaim, StableValue}; the raw sub is not part of the key.
type Ref struct {
	// Issuer is the issuer the claim belongs to. For an upstream_subject claim
	// that is the identity provider, not the server that signed the token.
	Issuer string
	// Claim is the name of the claim, such as "sub".
	Claim string
	// Value is the claim's value.
	Value string
}

// Resolver maps a Ref to the canonical local user ID.
//
// A nil error always comes with a non-empty user ID. Resolvers never create
// users; a Ref with no local user returns ErrUnknown. Callers log the errors
// an implementation returns, so they must not include the Ref's values.
type Resolver interface {
	Resolve(ctx context.Context, ref Ref) (string, error)
}

var (
	// ErrUnknown means no local user matches the Ref. Callers refuse the token.
	ErrUnknown = errors.New("unknown subject")
	// ErrAmbiguous means more than one local user matches the Ref. Callers
	// refuse the token. Directory-backed resolvers return it; the standalone
	// resolver never does.
	ErrAmbiguous = errors.New("ambiguous subject")
	// ErrUnavailable means the resolver could not answer. Callers report a
	// server error, not invalid_grant, so the client may retry.
	ErrUnavailable = errors.New("subject resolver unavailable")
)
