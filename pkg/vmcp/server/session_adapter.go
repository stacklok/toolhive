// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/stacklok/toolhive/pkg/transport/session"
)

// sessionIDAdapter adapts ToolHive's session.Manager to implement
// the mark3labs SDK's SessionIdManager interface.
//
// Session Management Ownership:
//   - ALL session storage, TTL, and cleanup is handled by ToolHive's session.Manager
//   - The mark3labs SDK does NOT manage sessions - it only calls our interface
//   - This is the SAME session.Manager used by pkg/transport/proxy/streamable for MCP sessions
//
// The SDK calls our adapter methods during MCP protocol flows:
//  1. Generate() - When client sends MCP initialize without Mcp-Session-Id header
//  2. Validate() - On every request to check if session exists and is active
//  3. Terminate() - When client sends HTTP DELETE to end session
//
// All three methods delegate to ToolHive's session.Manager. The adapter is purely
// infrastructure glue code (not domain logic) and correctly lives within the
// server bounded context per DDD principles.
type sessionIDAdapter struct {
	manager *session.Manager
}

// newSessionIDAdapter creates an adapter that bridges session.Manager
// to the mark3labs SDK's SessionIdManager interface.
func newSessionIDAdapter(manager *session.Manager) *sessionIDAdapter {
	return &sessionIDAdapter{
		manager: manager,
	}
}

// Generate creates a new session ID and registers it with the session manager.
// Called by the SDK when handling MCP initialize requests without a session ID.
//
// Per MCP spec (https://modelcontextprotocol.io/specification/2025-06-18/basic/transports):
//   - Session ID SHOULD be globally unique and cryptographically secure
//   - Session ID MUST only contain visible ASCII characters (0x21 to 0x7E)
//   - Server returns session ID in Mcp-Session-Id header on InitializeResult
func (a *sessionIDAdapter) Generate() string {
	// Generate a cryptographically secure UUID as per MCP spec requirements
	sessionID := uuid.New().String()

	// Create and register the session with the manager
	if err := a.manager.AddWithID(sessionID); err != nil {
		// This shouldn't happen unless we have a UUID collision (extremely unlikely)
		slog.Error("Failed to create session", "session", sessionID, "error", err)
		// Generate another ID and try once more
		sessionID = uuid.New().String()
		if err := a.manager.AddWithID(sessionID); err != nil {
			// Session storage is broken - return empty string as sentinel value.
			// The SDK will check for empty string and not send Mcp-Session-Id header,
			// causing subsequent client requests to fail validation gracefully.
			slog.Error("Failed to create session on retry - returning empty session ID", "session", sessionID, "error", err)
			return ""
		}
	}

	slog.Debug("Generated new MCP session", "session", sessionID)
	return sessionID
}

// Validate checks if a session ID is valid and not terminated.
// Called by the SDK on every request to verify the session exists.
//
// Per MCP spec:
//   - Clients MUST include Mcp-Session-Id header on all requests after initialization
//   - Server MUST respond with 404 Not Found if session is terminated or unknown
//   - Session IDs are terminated via HTTP DELETE or server-side timeout
//
// Returns:
//   - isTerminated=true: Session ID was valid but has been explicitly terminated
//   - isTerminated=false, err=nil: Session is active and valid
//   - err!=nil: Session ID is invalid or not found
func (a *sessionIDAdapter) Validate(sessionID string) (isTerminated bool, err error) {
	if sessionID == "" {
		return false, fmt.Errorf("empty session ID")
	}

	// Check if session exists and update its activity timestamp
	// The Get() call touches the session, extending its TTL
	sess, exists := a.manager.Get(sessionID)
	if !exists {
		// Session not found - could be:
		//   1. Never existed (invalid ID)
		//   2. TTL expired and was cleaned up
		//   3. Explicitly terminated and cleaned up
		// Per MCP spec, we should return 404 for all these cases
		slog.Debug("Session validation failed: not found", "session", sessionID)
		return false, fmt.Errorf("session not found")
	}

	// Check if session is marked as terminated via metadata
	// Terminated sessions are kept briefly to distinguish "terminated" from "never existed"
	if sess.GetMetadata()["terminated"] == "true" {
		slog.Debug("Session is terminated", "session", sessionID)
		return true, nil
	}

	// Session is valid and active
	slog.Debug("Session validated successfully", "session", sessionID)
	return false, nil
}

// Terminate marks a session as terminated.
// Called by the SDK when handling HTTP DELETE requests to the MCP endpoint.
//
// Per MCP spec:
//   - Clients SHOULD send HTTP DELETE with Mcp-Session-Id to end sessions explicitly
//   - Server MAY respond with 405 Method Not Allowed to prevent client termination
//   - After termination, subsequent requests with that session ID MUST return 404
//
// Returns:
//   - isNotAllowed=true: Server policy prevents client-initiated termination
//   - isNotAllowed=false: Termination succeeded
//   - err!=nil: Termination failed
func (a *sessionIDAdapter) Terminate(sessionID string) (isNotAllowed bool, err error) {
	if sessionID == "" {
		return false, fmt.Errorf("empty session ID")
	}

	// Get the session to mark it as terminated
	sess, exists := a.manager.Get(sessionID)
	if !exists {
		// Session doesn't exist - this is okay, return success
		// Client may be deleting an already-expired session
		slog.Debug("Terminate called on non-existent session", "session", sessionID)
		return false, nil
	}

	// Mark session as terminated via metadata
	// Don't delete immediately - keep it for a short time to return proper
	// isTerminated=true on Validate() calls for subsequent requests
	sess.SetMetadata("terminated", "true")
	slog.Info("Session terminated", "session", sessionID)

	// Note: The session.Manager's TTL cleanup will eventually delete this session.
	// This is correct behavior - we keep terminated sessions briefly so Validate()
	// can distinguish "terminated" (404) from "never existed" (404) with proper semantics.

	// Client-initiated termination is allowed (return isNotAllowed=false)
	return false, nil
}
