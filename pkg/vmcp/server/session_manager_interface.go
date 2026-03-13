// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"

	mcpserver "github.com/mark3labs/mcp-go/server"

	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
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
}
