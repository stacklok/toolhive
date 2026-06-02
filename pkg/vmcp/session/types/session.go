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
	"log/slog"

	"github.com/stacklok/toolhive/pkg/auth"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/session/binding"
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

	// Tools returns the advertised tools available in this session (shown to MCP clients).
	// The list is built once at session creation and is read-only thereafter.
	Tools() []vmcp.Tool

	// AllTools returns all resolved tools in this session, including tools that are
	// excluded from advertising to MCP clients via excludeAll or filter configuration.
	// Used by the workflow engine for argument type coercion via InputSchema lookup.
	AllTools() []vmcp.Tool

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
	// MetadataKeyTokenHash held hex(HMAC-SHA256(bearerToken)) for authenticated
	// sessions and "" for anonymous sessions.
	//
	// Legacy: superseded by MetadataKeyIdentityBinding (#5306); sessions written
	// under this key are invalidated on read. Constant removed in the follow-up PR.
	MetadataKeyTokenHash = "vmcp.token.hash" //nolint:gosec // This is a metadata key name, not a credential.

	// MetadataKeyTokenSalt held the hex-encoded per-session salt used for
	// HMAC-SHA256 token hashing.
	//
	// Legacy: superseded by MetadataKeyIdentityBinding (#5306); removed in the follow-up PR.
	MetadataKeyTokenSalt = "vmcp.token.salt" //nolint:gosec // This is a metadata key name, not a credential.

	// MetadataKeyIdentityBinding is the session metadata key that holds the
	// identity-binding string in the format defined by
	// [pkg/vmcp/session/binding]. Bound sessions store binding.Format(iss, sub);
	// unauthenticated sessions store binding.UnauthenticatedSentinel.
	//
	// Storage is plaintext PII — the Redis/Valkey instance must be access-controlled.
	MetadataKeyIdentityBinding = "vmcp.identity.binding"
)

// ShouldAllowAnonymous reports whether a session has no per-user binding and
// should be treated as anonymous. The identity is bound when Token is set, or
// when Claims yields a (iss, sub) pair accepted by binding.Format.
//
// Fail-closed: a Claims["iss"] or Claims["sub"] that is present but not a
// string (a misbehaving validator) is treated as bound, with a WARN logged.
//
// Contract note: this function answers "should this session be CREATED as
// anonymous?" using Token presence as a fast path. It does NOT guarantee that
// a non-anonymous identity will produce a successful binding: an identity with
// Token != "" but missing iss/sub claims passes this check as bound, then
// fails in BindSession with an extraction error. In practice this is
// pathological — all shipping middlewares (JWT validator, LocalUserMiddleware,
// AnonymousMiddleware) populate Claims. Callers who need "will this identity
// actually produce a binding?" must use extractBindingID directly, which is
// deliberately kept internal to the security decorator.
func ShouldAllowAnonymous(identity *auth.Identity) bool {
	if identity == nil {
		return true
	}
	if identity.Token != "" {
		return false
	}
	iss, issOK := claimString(identity.Claims, "iss")
	sub, subOK := claimString(identity.Claims, "sub")
	if !issOK || !subOK {
		slog.Warn("auth identity has present-but-non-string iss/sub claim; treating as bound")
		return false
	}
	if _, err := binding.Format(iss, sub); err == nil {
		return false
	}
	return true
}

// claimString returns (value, ok) for a string claim. ok is true when the
// claim is missing entirely (absence is benign — the caller proceeds to
// binding.Format which rejects empty halves) or when the claim is present
// and a string (including the empty string). ok is false only when the
// claim is present but not a string — the caller must treat that as a
// misconfigured validator and fail closed.
func claimString(claims map[string]any, key string) (string, bool) {
	v, present := claims[key]
	if !present {
		return "", true
	}
	s, isString := v.(string)
	if !isString {
		return "", false
	}
	return s, true
}

// Identity binding errors returned by Caller methods when caller identity
// validation fails.
var (
	// ErrUnauthorizedCaller is returned when the caller identity does not
	// match the session owner's identity (identity binding mismatch).
	ErrUnauthorizedCaller = errors.New("caller identity does not match session owner")

	// ErrNilCaller is returned when a bound session receives a nil caller.
	// Bound sessions require explicit caller identity on every method call.
	ErrNilCaller = errors.New("caller identity is required for bound sessions")

	// ErrSessionOwnerUnknown is returned when the session has no bound identity
	// but is configured to require one. This indicates a configuration error.
	ErrSessionOwnerUnknown = errors.New("session has no bound identity")
)
