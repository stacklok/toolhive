// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
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
