// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"

	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
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
// (one-time cost per re-route). This is a known trade-off documented in
// RFC THV-0038: https://github.com/stacklok/toolhive-rfcs/blob/main/rfcs/THV-0038-session-scoped-client-lifecycle.md
//
// # Dual-layer storage model
//
// A MultiSession separates two layers with different lifecycles:
//
//   - Metadata layer (serialisable): session ID, timestamps, identity reference,
//     backend ID list. Stored via the transportsession.Storage interface and
//     can persist across restarts.
//
//   - Runtime layer (non-serialisable): MCP client objects, routing table,
//     capabilities, backend session ID map, closed flag. Lives only in-process.
//
// All session metadata goes through the same Storage interface — no parallel
// storage path is introduced.
type MultiSession interface {
	transportsession.Session

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

	// CallTool invokes toolName on the appropriate backend for this session.
	// The routing table is consulted to identify the backend; the
	// session-scoped client for that backend is then used, avoiding
	// per-request connection overhead.
	//
	// arguments contains the tool input parameters.
	// meta contains protocol-level metadata (_meta) forwarded from the client.
	CallTool(
		ctx context.Context,
		toolName string,
		arguments map[string]any,
		meta map[string]any,
	) (*vmcp.ToolCallResult, error)

	// ReadResource retrieves the resource identified by uri from the
	// appropriate backend for this session.
	ReadResource(ctx context.Context, uri string) (*vmcp.ResourceReadResult, error)

	// GetPrompt retrieves the named prompt from the appropriate backend for
	// this session.
	//
	// arguments contains the prompt input parameters.
	GetPrompt(
		ctx context.Context,
		name string,
		arguments map[string]any,
	) (*vmcp.PromptGetResult, error)

	// Close releases all resources held by this session, including all
	// backend client connections. It waits for any in-flight operations to
	// complete before tearing down clients.
	//
	// Close is idempotent: calling it multiple times returns nil.
	Close() error
}
