// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"

	"github.com/stacklok/toolhive/pkg/auth"
)

// withExplicitIdentity returns ctx carrying identity as the caller identity
// for the components below the core that still read it from ctx
// (auth.IdentityFromContext): the outgoing auth strategies, backend telemetry,
// the backend client's fallback identity, and the capability cache key. Every
// core method that takes an identity derives its context this way at entry, so
// a caller that passes the identity only as a parameter — or passes a ctx
// carrying a different identity — still has its backend calls authenticated,
// recorded, and cache-keyed as the parameter identity.
//
// A nil identity stays anonymous: a typed-nil *auth.Identity stored under the
// key reads back as absent from auth.IdentityFromContext, so downstream readers
// see no caller rather than the ctx identity the parameter replaced.
//
// context.WithValue is used rather than auth.WithIdentity, which would also
// write any auth.IdentityHolder in ctx and skips a nil identity. The holder is
// the audit middleware's view of the request principal and must not be
// rewritten by the core (see the composite-workflow audit tests).
//
// Trade-off: this expands reliance on identity-in-ctx, which vmcp
// anti-pattern #1 warns against. It is the small fix for #7143: the components
// below the core take no identity parameter today, and threading one through
// them (the issue's option 2) would touch many packages, decorators, mocks,
// and tests. The core itself still never READS identity from ctx for its own
// decisions — admission takes the explicit parameter — it only hands that
// parameter to downstream readers that do.
func withExplicitIdentity(ctx context.Context, identity *auth.Identity) context.Context {
	return context.WithValue(ctx, auth.IdentityContextKey{}, identity)
}
