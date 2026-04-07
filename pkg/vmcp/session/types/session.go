// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package types defines shared session interfaces for the vmcp/session package
// hierarchy. Placing the common types here allows both the internal backend
// package and the top-level session package to share a definition without
// introducing an import cycle.
package types

//go:generate mockgen -destination=mocks/mock_session.go -package=mocks github.com/stacklok/toolhive/pkg/vmcp/session/types MultiSession

import (
	"context"
	"errors"

	"github.com/stacklok/toolhive/pkg/auth"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// Caller represents the ability to invoke MCP protocol operations against a
// backend. It is the common subset shared by both a single-backend
// [backend.Session] and the multi-backend [session.MultiSession].
//
// Implementations must be safe for concurrent use.
type Caller interface {
	// CallTool invokes toolName on the backend.
	//
	// caller identifies the requesting user/service. For bound sessions, caller
	// must be non-nil and its identity must match the session creator. For
	// anonymous sessions, caller may be nil.
	//
	// Returns:
	//   - ErrNilCaller if caller is nil for a bound session
	//   - ErrUnauthorizedCaller if the caller identity does not match the session owner
	//
	// arguments contains the tool input parameters.
	// meta contains protocol-level metadata (_meta) forwarded from the client.
	CallTool(
		ctx context.Context,
		caller *auth.Identity,
		toolName string,
		arguments map[string]any,
		meta map[string]any,
	) (*vmcp.ToolCallResult, error)

	// ReadResource retrieves the resource identified by uri from the backend.
	//
	// caller identifies the requesting user/service. For bound sessions, caller
	// must be non-nil and its identity must match the session creator. For
	// anonymous sessions, caller may be nil.
	//
	// Returns:
	//   - ErrNilCaller if caller is nil for a bound session
	//   - ErrUnauthorizedCaller if the caller identity does not match the session owner
	ReadResource(ctx context.Context, caller *auth.Identity, uri string) (*vmcp.ResourceReadResult, error)

	// GetPrompt retrieves the named prompt from the backend.
	//
	// caller identifies the requesting user/service. For bound sessions, caller
	// must be non-nil and its identity must match the session creator. For
	// anonymous sessions, caller may be nil.
	//
	// Returns:
	//   - ErrNilCaller if caller is nil for a bound session
	//   - ErrUnauthorizedCaller if the caller identity does not match the session owner
	//
	// arguments contains the prompt input parameters.
	GetPrompt(
		ctx context.Context,
		caller *auth.Identity,
		name string,
		arguments map[string]any,
	) (*vmcp.PromptGetResult, error)

	// Close releases all resources held by this caller. Implementations must
	// be idempotent: calling Close multiple times returns nil.
	Close() error
}

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
	Caller

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

	// GetRoutingTable returns the session's routing table.
	// Used by the discovery middleware to inject DiscoveredCapabilities into the
	// request context so composite tool workflow steps can route backend tool calls.
	GetRoutingTable() *vmcp.RoutingTable
}

const (
	// MetadataKeyTokenHash is the session metadata key that holds the HMAC-SHA256
	// hash of the bearer token used to create the session. For authenticated sessions
	// this is hex(HMAC-SHA256(bearerToken)). For anonymous sessions this is the empty
	// string sentinel. The raw token is never stored — only the hash.
	//
	// This constant is the single source of truth used by the session factory and
	// security layer to store and validate token binding metadata.
	MetadataKeyTokenHash = "vmcp.token.hash" //nolint:gosec // This is a metadata key name, not a credential.

	// MetadataKeyTokenSalt is the session metadata key that holds the hex-encoded
	// random salt used for HMAC-SHA256 token hashing. Each authenticated session has a
	// unique salt to prevent attacks across multiple sessions. Anonymous sessions do not
	// generate a salt and this key is omitted from their metadata.
	//
	// This constant is the single source of truth used by the session factory and
	// security layer to store and validate token binding metadata.
	MetadataKeyTokenSalt = "vmcp.token.salt" //nolint:gosec // This is a metadata key name, not a credential.
)

// ShouldAllowAnonymous determines if a session should allow anonymous access
// based on the creator's identity. Sessions without an identity (nil) or with
// an empty token are treated as anonymous.
func ShouldAllowAnonymous(identity *auth.Identity) bool {
	return identity == nil || identity.Token == ""
}

// Token binding errors returned by Caller methods when caller identity
// validation fails.
var (
	// ErrUnauthorizedCaller is returned when the caller identity does not
	// match the session owner's identity (token hash mismatch).
	ErrUnauthorizedCaller = errors.New("caller identity does not match session owner")

	// ErrNilCaller is returned when a bound session receives a nil caller.
	// Bound sessions require explicit caller identity on every method call.
	ErrNilCaller = errors.New("caller identity is required for bound sessions")

	// ErrSessionOwnerUnknown is returned when the session has no bound identity
	// but is configured to require one. This indicates a configuration error.
	ErrSessionOwnerUnknown = errors.New("session has no bound identity")
)
