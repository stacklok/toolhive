// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/auth"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

const (
	// metadataKeyTerminated is the session metadata key that marks a placeholder
	// session as explicitly terminated by the client.
	metadataKeyTerminated = "terminated"

	// metadataValTrue is the string value stored under metadataKeyTerminated
	// when a session has been terminated.
	metadataValTrue = "true"
)

// vmcpSessionManager bridges the domain session lifecycle (MultiSession / MultiSessionFactory)
// to the mark3labs SDK's SessionIdManager interface.
//
// It implements a two-phase session-creation pattern:
//
//   - Generate(): called by SDK during initialize without context;
//     stores an empty placeholder via transportsession.Manager.
//   - CreateSession(): called from OnRegisterSession hook once
//     context is available; calls factory.MakeSessionWithID(), then
//     replaces the placeholder with the fully-formed MultiSession.
//
// All session storage is delegated to transportsession.Manager — no separate
// sync.Map or secondary store is maintained. The MultiSession itself implements
// transportsession.Session and is stored directly in the transport Manager.
//
// Storage backend scope: the transportsession.Manager's pluggable backend
// (including Redis if configured) handles session metadata and TTL lifecycle.
// However, MultiSession holds live in-process state (backend HTTP connections,
// routing table) that cannot be serialized or transferred across processes.
// Sessions are therefore node-local: pluggable backends provide metadata
// durability, but a session can only be served by the process that created it.
// Horizontal scaling requires sticky routing (e.g., session-affinity load
// balancing) — cross-node reconstruction is not implemented.
type vmcpSessionManager struct {
	storage         *transportsession.Manager
	factory         vmcpsession.MultiSessionFactory
	backendRegistry vmcp.BackendRegistry
}

// newVMCPSessionManager creates a vmcpSessionManager backed by the given transport
// manager, session factory, and backend registry.
func newVMCPSessionManager(
	storage *transportsession.Manager,
	factory vmcpsession.MultiSessionFactory,
	backendRegistry vmcp.BackendRegistry,
) *vmcpSessionManager {
	return &vmcpSessionManager{
		storage:         storage,
		factory:         factory,
		backendRegistry: backendRegistry,
	}
}

// Generate implements the SDK's SessionIdManager.Generate().
//
// Phase 1 of the two-phase creation pattern: creates a unique session ID,
// stores an empty placeholder via transportsession.Manager.AddWithID(), and
// returns the ID to the SDK. No context is available at this point.
//
// The placeholder is replaced by CreateSession() in Phase 2 once context
// is available via the OnRegisterSession hook.
func (sm *vmcpSessionManager) Generate() string {
	sessionID := uuid.New().String()

	if err := sm.storage.AddWithID(sessionID); err != nil {
		// UUID collision is astronomically unlikely; log and retry once.
		slog.Error("vmcpSessionManager: failed to store placeholder session", "session_id", sessionID, "error", err)
		sessionID = uuid.New().String()
		if err := sm.storage.AddWithID(sessionID); err != nil {
			slog.Error("vmcpSessionManager: failed to store placeholder session on retry", "session_id", sessionID, "error", err)
			return ""
		}
	}

	slog.Debug("vmcpSessionManager: generated placeholder session", "session_id", sessionID)
	return sessionID
}

// CreateSession is Phase 2 of the two-phase creation pattern.
//
// It is called from the OnRegisterSession hook once the request context is
// available. It:
//  1. Resolves the caller identity from the context.
//  2. Lists available backends from the registry.
//  3. Calls MultiSessionFactory.MakeSessionWithID() to build a fully-formed
//     MultiSession (which opens real HTTP connections to each backend).
//  4. Replaces the placeholder stored by Generate() with the new MultiSession.
//
// The returned MultiSession can be retrieved later via GetMultiSession().
func (sm *vmcpSessionManager) CreateSession(
	ctx context.Context,
	sessionID string,
) (vmcpsession.MultiSession, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("vmcpSessionManager.CreateSession: session ID must not be empty")
	}

	// Fast-fail before opening any backend connections: verify the phase-1
	// placeholder still exists and has not been marked terminated. A client
	// DELETE between Generate() and this hook sets terminated=true on the
	// placeholder (or removes it entirely). Opening backend connections first
	// and checking afterwards would waste those resources and could silently
	// resurrect a session the client intentionally ended.
	placeholder, exists := sm.storage.Get(sessionID)
	if !exists {
		return nil, fmt.Errorf(
			"vmcpSessionManager.CreateSession: placeholder for session %q not found (terminated concurrently?)",
			sessionID,
		)
	}
	if placeholder.GetMetadata()[metadataKeyTerminated] == metadataValTrue {
		return nil, fmt.Errorf(
			"vmcpSessionManager.CreateSession: session %q was terminated before backend connections could be opened",
			sessionID,
		)
	}

	// Resolve the caller identity (may be nil for anonymous access).
	identity, _ := auth.IdentityFromContext(ctx)

	// List all available backends from the registry.
	rawBackends := sm.backendRegistry.List(ctx)
	backends := make([]*vmcp.Backend, len(rawBackends))
	for i := range rawBackends {
		b := rawBackends[i] // avoid loop-variable capture
		backends[i] = &b
	}

	// Build the fully-formed MultiSession using the SDK-assigned session ID.
	sess, err := sm.factory.MakeSessionWithID(ctx, sessionID, identity, backends)
	if err != nil {
		return nil, fmt.Errorf("vmcpSessionManager.CreateSession: failed to create multi-session: %w", err)
	}

	// Re-check that the placeholder is still present after the (potentially
	// slow) MakeSessionWithID call. A second DELETE arriving during backend
	// initialisation would remove the placeholder; upserting over an absent
	// entry would silently resurrect a terminated session.
	if _, exists := sm.storage.Get(sessionID); !exists {
		_ = sess.Close()
		return nil, fmt.Errorf(
			"vmcpSessionManager.CreateSession: placeholder for session %q disappeared during backend init (terminated concurrently)",
			sessionID,
		)
	}

	// Replace the placeholder in the transport manager.
	if err := sm.storage.ReplaceSession(sess); err != nil {
		// Best-effort close of the newly created session to release backend connections.
		_ = sess.Close()
		return nil, fmt.Errorf("vmcpSessionManager.CreateSession: failed to replace placeholder: %w", err)
	}

	slog.Debug("vmcpSessionManager: created multi-session",
		"session_id", sessionID,
		"backend_count", len(backends))
	return sess, nil
}

// Validate implements the SDK's SessionIdManager.Validate().
//
// Returns (isTerminated=true, nil) for explicitly terminated sessions.
// Returns (false, error) for unknown sessions — per the SDK interface contract,
// a lookup failure is signalled via err, not via isTerminated.
// Returns (false, nil) for valid, active sessions.
func (sm *vmcpSessionManager) Validate(sessionID string) (isTerminated bool, err error) {
	if sessionID == "" {
		return false, fmt.Errorf("vmcpSessionManager.Validate: empty session ID")
	}

	sess, exists := sm.storage.Get(sessionID)
	if !exists {
		slog.Debug("vmcpSessionManager.Validate: session not found", "session_id", sessionID)
		return false, fmt.Errorf("session not found")
	}

	if sess.GetMetadata()[metadataKeyTerminated] == metadataValTrue {
		slog.Debug("vmcpSessionManager.Validate: session is terminated", "session_id", sessionID)
		return true, nil
	}

	return false, nil
}

// Terminate implements the SDK's SessionIdManager.Terminate().
//
// The two session types are handled asymmetrically:
//
//   - MultiSession (Phase 2): Close() releases backend connections, then the
//     session is deleted from storage immediately. After deletion Validate()
//     returns (false, error) — the same response as "never existed". This is
//     intentional: a terminated MultiSession has no resources to preserve, so
//     immediate removal is cleaner than marking and waiting for TTL.
//
//   - Placeholder (Phase 1): the session is marked terminated=true and left
//     for TTL cleanup. This lets Validate() return (isTerminated=true, nil)
//     during the window between client termination and TTL expiry, which
//     allows the SDK to distinguish "actively terminated" from "never existed".
//
// Returns (isNotAllowed=false, nil) on success; client termination is always permitted.
func (sm *vmcpSessionManager) Terminate(sessionID string) (isNotAllowed bool, err error) {
	if sessionID == "" {
		return false, fmt.Errorf("vmcpSessionManager.Terminate: empty session ID")
	}

	sess, exists := sm.storage.Get(sessionID)
	if !exists {
		slog.Debug("vmcpSessionManager.Terminate: session not found (already expired?)", "session_id", sessionID)
		return false, nil
	}

	// If the session is a fully-formed MultiSession, close its backend connections.
	if multiSess, ok := sess.(vmcpsession.MultiSession); ok {
		if closeErr := multiSess.Close(); closeErr != nil {
			slog.Warn("vmcpSessionManager.Terminate: error closing multi-session backend connections",
				"session_id", sessionID, "error", closeErr)
			// Continue with removal even if Close() fails.
		}
		if deleteErr := sm.storage.Delete(sessionID); deleteErr != nil {
			return false, fmt.Errorf("vmcpSessionManager.Terminate: failed to delete session from storage: %w", deleteErr)
		}
	} else {
		// Placeholder session — mark as terminated and write back to storage so
		// the flag is visible to any storage backend (including distributed ones).
		// TTL cleanup will remove the record later; the terminated flag lets
		// Validate() return isTerminated=true during the intervening window.
		sess.SetMetadata(metadataKeyTerminated, metadataValTrue)
		if replaceErr := sm.storage.ReplaceSession(sess); replaceErr != nil {
			slog.Warn("vmcpSessionManager.Terminate: failed to persist terminated flag for placeholder",
				"session_id", sessionID, "error", replaceErr)
			// Non-fatal: the in-memory flag is set; TTL will clean up anyway.
		}
	}

	slog.Info("vmcpSessionManager.Terminate: session terminated", "session_id", sessionID)
	return false, nil
}

// GetMultiSession retrieves the fully-formed MultiSession for a given SDK session ID.
// Returns (nil, false) if the session does not exist or has not yet been
// upgraded from placeholder to MultiSession.
func (sm *vmcpSessionManager) GetMultiSession(sessionID string) (vmcpsession.MultiSession, bool) {
	sess, exists := sm.storage.Get(sessionID)
	if !exists {
		return nil, false
	}
	multiSess, ok := sess.(vmcpsession.MultiSession)
	return multiSess, ok
}

// GetAdaptedTools returns SDK-format tools for the given session, with handlers
// that delegate tool invocations directly to the session's CallTool() method.
//
// This enables session-scoped routing: each tool call goes through the session's
// backend connections rather than the global router.
func (sm *vmcpSessionManager) GetAdaptedTools(sessionID string) ([]mcpserver.ServerTool, error) {
	multiSess, ok := sm.GetMultiSession(sessionID)
	if !ok {
		return nil, fmt.Errorf("vmcpSessionManager.GetAdaptedTools: session %q not found or not a multi-session", sessionID)
	}

	domainTools := multiSess.Tools()
	sdkTools := make([]mcpserver.ServerTool, 0, len(domainTools))

	for _, domainTool := range domainTools {
		t := domainTool // capture loop variable

		// Marshal InputSchema to JSON so the SDK exposes the full parameter
		// schema to clients (matching the behaviour of CapabilityAdapter.ToSDKTools).
		schemaJSON, err := json.Marshal(t.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("vmcpSessionManager.GetAdaptedTools: failed to marshal schema for tool %s: %w", t.Name, err)
		}

		tool := mcp.Tool{
			Name:           t.Name,
			Description:    t.Description,
			RawInputSchema: schemaJSON,
		}

		// Build the session-scoped handler that captures multiSess and toolName
		// by value. Using the captured toolName (not req.Params.Name) ensures
		// routing is driven by the server-registered name, not client-supplied input.
		capturedSess := multiSess
		toolName := t.Name
		handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args, ok := req.Params.Arguments.(map[string]any)
			if !ok {
				wrappedErr := fmt.Errorf("%w: arguments must be object, got %T", vmcp.ErrInvalidInput, req.Params.Arguments)
				slog.Warn("invalid arguments for tool", "tool", toolName, "error", wrappedErr)
				return mcp.NewToolResultError(wrappedErr.Error()), nil
			}

			meta := conversion.FromMCPMeta(req.Params.Meta)
			result, callErr := capturedSess.CallTool(ctx, toolName, args, meta)
			if callErr != nil {
				return mcp.NewToolResultError(callErr.Error()), nil
			}

			return &mcp.CallToolResult{
				Result: mcp.Result{
					Meta: conversion.ToMCPMeta(result.Meta),
				},
				Content:           conversion.ToMCPContents(result.Content),
				StructuredContent: result.StructuredContent,
				IsError:           result.IsError,
			}, nil
		}

		sdkTools = append(sdkTools, mcpserver.ServerTool{
			Tool:    tool,
			Handler: handler,
		})
		slog.Debug("vmcpSessionManager.GetAdaptedTools: adapted tool", "session_id", sessionID, "tool", t.Name)
	}

	return sdkTools, nil
}
