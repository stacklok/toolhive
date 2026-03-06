// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"github.com/stacklok/toolhive/pkg/auth"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// MultiSession is the vMCP domain session interface. It extends the
// transport-layer Session with behaviour: capability access and session-scoped
// backend routing across multiple backend connections.
//
// A MultiSession is a "session of sessions": each backend contributes its own
// persistent connection (see [backend.Session] in pkg/vmcp/session/internal/backend),
// and the MultiSession aggregates them behind a single routing table.
//
// # Distributed deployment note
//
// Because MCP clients cannot be serialised, horizontal scaling requires sticky
// sessions (session affinity at the load balancer). Without sticky sessions, a
// request routed to a different vMCP instance must recreate backend clients
// (one-time cost per re-route). This is an accepted trade-off.
//
// # Storage
//
// A MultiSession uses a two-layer storage model:
//
//   - Runtime layer (in-process only): backend HTTP connections, routing
//     table, and capability lists. These cannot be serialized and are lost
//     when the process exits. Sessions are therefore node-local.
//
//   - Metadata layer (serializable): identity subject and connected backend
//     IDs are written to the embedded transportsession.Session so that
//     pluggable transportsession.Storage backends (e.g. Redis) can persist
//     them. This enables auditing and future session reconstruction, but
//     does not make the session itself portable — the runtime layer must
//     be rebuilt from scratch on a different node.
type MultiSession interface {
	transportsession.Session
	sessiontypes.Caller

	// Tools returns the resolved tools available in this session.
	// The list is built once at session creation and is read-only thereafter.
	Tools() []vmcp.Tool

	// Resources returns the resolved resources available in this session.
	Resources() []vmcp.Resource

	// Prompts returns the resolved prompts available in this session.
	Prompts() []vmcp.Prompt

	// BackendSessions returns a snapshot of the backend-assigned session IDs,
	// keyed by backend workload ID. The backend session ID is assigned by the
	// backend MCP server and is used to correlate vMCP sessions with backend
	// sessions for debugging and auditing.
	BackendSessions() map[string]string
}

const (
	// MetadataKeyTokenHash is the session metadata key that holds the HMAC-SHA256
	// hash of the bearer token used to create the session. For authenticated sessions
	// this is hex(HMAC-SHA256(bearerToken)). For anonymous sessions this is the empty
	// string sentinel. The raw token is never stored — only the hash.
	//
	// Re-exported from types package for convenience.
	MetadataKeyTokenHash = sessiontypes.MetadataKeyTokenHash

	// MetadataKeyTokenSalt is the session metadata key that holds the hex-encoded
	// random salt used for HMAC-SHA256 token hashing. Each session has a unique salt
	// to prevent attacks across multiple sessions.
	//
	// Re-exported from types package for convenience.
	MetadataKeyTokenSalt = sessiontypes.MetadataKeyTokenSalt
)

// ShouldAllowAnonymous determines if a session should allow anonymous access
// based on the creator's identity. This is session business logic that decides
// whether a session is bound to a specific identity or allows anonymous access.
//
// Sessions without an identity (nil) or with an empty token are treated as
// anonymous and will accept requests from any caller. Sessions with a non-empty
// bearer token are bound to that token and will reject requests from different
// callers.
//
// This function is used by both the session factory (to determine how to create
// the session) and the security layer (to validate requests against the session's
// access policy).
func ShouldAllowAnonymous(identity *auth.Identity) bool {
	return identity == nil || identity.Token == ""
}
