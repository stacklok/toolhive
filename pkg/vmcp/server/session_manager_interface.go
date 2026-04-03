// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"

	mcpserver "github.com/mark3labs/mcp-go/server"

	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// SessionManager extends the SDK's SessionIdManager with Phase 2 session creation
// and session-scoped tool retrieval.
//
// This interface abstracts the session manager implementation to enable testing
// and decouples the Server from concrete session management implementation details.
//
// The concrete implementation is provided by the sessionmanager package.
type SessionManager interface {
	mcpserver.SessionIdManager

	// CreateSession completes Phase 2 of the two-phase session creation pattern.
	// Called from OnRegisterSession hook once context is available; creates backend
	// connections and replaces the placeholder with a fully-formed MultiSession.
	CreateSession(ctx context.Context, sessionID string) (vmcpsession.MultiSession, error)

	// GetAdaptedTools returns SDK-format tools for the given session with session-scoped
	// handlers. This enables session-scoped routing: each tool call goes through the
	// session's backend connections rather than the global router.
	GetAdaptedTools(sessionID string) ([]mcpserver.ServerTool, error)

	// GetAdaptedResources returns SDK-format resources for the given session with
	// session-scoped handlers, analogous to GetAdaptedTools for resources.
	GetAdaptedResources(sessionID string) ([]mcpserver.ServerResource, error)

	// GetMultiSession retrieves the fully-formed MultiSession for the given session ID.
	// Returns (nil, false) if the session does not exist or is still a placeholder.
	// Used to access session-scoped backend tool metadata (e.g. for conflict validation).
	GetMultiSession(sessionID string) (vmcpsession.MultiSession, bool)

	// DecorateSession retrieves the MultiSession for sessionID, applies fn to it,
	// and stores the result back. Used to stack session decorators (composite tools,
	// optimizer) after the base session is created.
	DecorateSession(sessionID string, fn func(sessiontypes.MultiSession) sessiontypes.MultiSession) error

	// Terminate terminates the session with the given ID, closing all backend connections.
	Terminate(sessionID string) (bool, error)

	// NotifyBackendExpired updates session metadata in storage to reflect that the
	// backend identified by workloadID is no longer connected. It is a best-effort,
	// metadata-only operation intended to be called by keepalive or health-monitoring
	// components when they detect that a backend session has expired or been lost.
	// Storage errors are logged but not returned.
	NotifyBackendExpired(sessionID, workloadID string)
}
