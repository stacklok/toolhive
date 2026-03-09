// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"github.com/stacklok/toolhive/pkg/auth"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// MultiSession is an alias for sessiontypes.MultiSession, re-exported here for
// backward compatibility and convenience.
type MultiSession = sessiontypes.MultiSession

const (
	// MetadataKeyTokenHash is the session metadata key that holds the HMAC-SHA256
	// hash of the bearer token used to create the session. For authenticated sessions
	// this is hex(HMAC-SHA256(bearerToken)). For anonymous sessions this is the empty
	// string sentinel. The raw token is never stored — only the hash.
	//
	// Re-exported from types package for convenience.
	MetadataKeyTokenHash = sessiontypes.MetadataKeyTokenHash
)

// ShouldAllowAnonymous determines if a session should allow anonymous access
// based on the creator's identity. This is session business logic that decides
// whether a session is bound to a specific identity or allows anonymous access.
//
// Sessions without an identity (nil) or with an empty token are treated as
// anonymous and will only accept nil callers or callers with an empty token;
// callers presenting a non-empty token are rejected to prevent session-upgrade
// attacks. Sessions with a non-empty bearer token are bound to that token and
// will reject requests from callers with a different token.
//
// This function is used by both the session factory (to determine how to create
// the session) and the security layer (to validate requests against the session's
// access policy).
func ShouldAllowAnonymous(identity *auth.Identity) bool {
	return identity == nil || identity.Token == ""
}
