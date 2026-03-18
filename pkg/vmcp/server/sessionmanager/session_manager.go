// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package sessionmanager provides session lifecycle management.
//
// This package implements the two-phase session creation pattern that bridges
// the MCP SDK's session management with the vMCP server's backend lifecycle:
//   - Phase 1 (Generate): Creates a placeholder session with no context
//   - Phase 2 (CreateSession): Replaces placeholder with fully-initialized MultiSession
//
// The Manager type implements the server.SessionManager interface and is used by
// the server package.
package sessionmanager

import (
	"context"
	"encoding/json"
	"errors"
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
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

const (
	// MetadataKeyTerminated is the session metadata key that marks a placeholder
	// session as explicitly terminated by the client.
	MetadataKeyTerminated = "terminated"

	// MetadataValTrue is the string value stored under MetadataKeyTerminated
	// when a session has been terminated.
	MetadataValTrue = "true"
)

// Manager bridges the domain session lifecycle (MultiSession / MultiSessionFactory)
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
type Manager struct {
	storage         *transportsession.Manager
	factory         vmcpsession.MultiSessionFactory
	backendRegistry vmcp.BackendRegistry
}

// New creates a Manager backed by the given transport manager, session factory,
// and backend registry.
func New(
	storage *transportsession.Manager,
	factory vmcpsession.MultiSessionFactory,
	backendRegistry vmcp.BackendRegistry,
) *Manager {
	return &Manager{
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
func (sm *Manager) Generate() string {
	sessionID := uuid.New().String()

	if err := sm.storage.AddWithID(sessionID); err != nil {
		// UUID collision is astronomically unlikely; log and retry once.
		slog.Error("Manager: failed to store placeholder session", "session_id", sessionID, "error", err)
		sessionID = uuid.New().String()
		if err := sm.storage.AddWithID(sessionID); err != nil {
			slog.Error("Manager: failed to store placeholder session on retry", "session_id", sessionID, "error", err)
			return ""
		}
	}

	slog.Debug("Manager: generated placeholder session", "session_id", sessionID)
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
func (sm *Manager) CreateSession(
	ctx context.Context,
	sessionID string,
) (vmcpsession.MultiSession, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("Manager.CreateSession: session ID must not be empty")
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
			"Manager.CreateSession: placeholder for session %q not found (terminated concurrently?)",
			sessionID,
		)
	}
	if placeholder.GetMetadata()[MetadataKeyTerminated] == MetadataValTrue {
		return nil, fmt.Errorf(
			"Manager.CreateSession: session %q was terminated before backend connections could be opened",
			sessionID,
		)
	}

	// Resolve the caller identity (may be nil for anonymous access).
	identity, _ := auth.IdentityFromContext(ctx)

	// Note: Token hash and salt are computed and stored by the session factory
	// (MakeSessionWithID below). Token binding enforcement happens at the session
	// level via validateCaller(), which uses HMAC-SHA256 with a per-session salt.

	// List all available backends from the registry.
	rawBackends := sm.backendRegistry.List(ctx)
	backends := make([]*vmcp.Backend, len(rawBackends))
	for i := range rawBackends {
		backends[i] = &rawBackends[i]
	}

	// Build the fully-formed MultiSession using the SDK-assigned session ID.
	// Sessions created with an identity are bound to that identity (allowAnonymous=false).
	// Sessions created without an identity allow anonymous access (allowAnonymous=true).
	allowAnonymous := sessiontypes.ShouldAllowAnonymous(identity)
	sess, err := sm.factory.MakeSessionWithID(ctx, sessionID, identity, allowAnonymous, backends)
	if err != nil {
		return nil, fmt.Errorf("Manager.CreateSession: failed to create multi-session: %w", err)
	}

	// Re-check that the placeholder is still present AND not terminated after
	// the (potentially slow) MakeSessionWithID call. A concurrent DELETE could:
	//   1. Delete the placeholder entirely (caught by !exists check), OR
	//   2. Mark it terminated=true (caught by terminated flag check)
	// Without this second check, UpsertSession would silently resurrect a
	// session the client already terminated, wasting backend connections.
	placeholder2, exists := sm.storage.Get(sessionID)
	if !exists {
		_ = sess.Close()
		return nil, fmt.Errorf(
			"Manager.CreateSession: placeholder for session %q disappeared during backend init (terminated concurrently)",
			sessionID,
		)
	}
	if placeholder2.GetMetadata()[MetadataKeyTerminated] == MetadataValTrue {
		_ = sess.Close()
		return nil, fmt.Errorf(
			"Manager.CreateSession: session %q was terminated during backend init (marked after first check)",
			sessionID,
		)
	}

	// The token hash and salt are already stored in the session metadata by the
	// factory (MakeSessionWithID). No need to transfer from placeholder.

	// Replace the placeholder in the transport manager.
	if err := sm.storage.UpsertSession(sess); err != nil {
		// Best-effort close of the newly created session to release backend connections.
		_ = sess.Close()
		return nil, fmt.Errorf("Manager.CreateSession: failed to replace placeholder: %w", err)
	}

	slog.Debug("Manager: created multi-session",
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
func (sm *Manager) Validate(sessionID string) (isTerminated bool, err error) {
	if sessionID == "" {
		return false, fmt.Errorf("Manager.Validate: empty session ID")
	}

	sess, exists := sm.storage.Get(sessionID)
	if !exists {
		slog.Debug("Manager.Validate: session not found", "session_id", sessionID)
		return false, fmt.Errorf("session not found")
	}

	if sess.GetMetadata()[MetadataKeyTerminated] == MetadataValTrue {
		slog.Debug("Manager.Validate: session is terminated", "session_id", sessionID)
		return true, nil
	}

	return false, nil
}

// Terminate implements the SDK's SessionIdManager.Terminate().
//
// The two session types are handled asymmetrically to prevent a race condition
// where client termination during the Phase 1→Phase 2 window could resurrect
// sessions with open backend connections:
//
//   - MultiSession (Phase 2): Close() releases backend connections, then the
//     session is deleted from storage immediately. After deletion Validate()
//     returns (false, error) — the same response as "never existed". This is
//     intentional: a terminated MultiSession has no resources to preserve, so
//     immediate removal is cleaner than marking and waiting for TTL.
//
//   - Placeholder (Phase 1): the session is marked terminated=true and left
//     for TTL cleanup. This prevents CreateSession() from opening backend
//     connections for an already-terminated session (see fast-fail check in
//     CreateSession at lines 142-147). The terminated flag also lets Validate()
//     return (isTerminated=true, nil) during the window between termination
//     and TTL expiry, allowing the SDK to distinguish "actively terminated"
//     from "never existed".
//
// Returns (isNotAllowed=false, nil) on success; client termination is always permitted.
func (sm *Manager) Terminate(sessionID string) (isNotAllowed bool, err error) {
	if sessionID == "" {
		return false, fmt.Errorf("Manager.Terminate: empty session ID")
	}

	sess, exists := sm.storage.Get(sessionID)
	if !exists {
		slog.Debug("Manager.Terminate: session not found (already expired?)", "session_id", sessionID)
		return false, nil
	}

	// If the session is a fully-formed MultiSession, close its backend connections.
	if multiSess, ok := sess.(vmcpsession.MultiSession); ok {
		if closeErr := multiSess.Close(); closeErr != nil {
			slog.Warn("Manager.Terminate: error closing multi-session backend connections",
				"session_id", sessionID, "error", closeErr)
			// Continue with removal even if Close() fails.
		}
		if deleteErr := sm.storage.Delete(sessionID); deleteErr != nil {
			return false, fmt.Errorf("Manager.Terminate: failed to delete session from storage: %w", deleteErr)
		}
	} else {
		// Placeholder session (not yet upgraded to MultiSession).
		//
		// This handles the race condition where a client sends DELETE between
		// Generate() (Phase 1) and CreateSession() (Phase 2). The two-phase
		// pattern creates a window where the session exists as a placeholder:
		//
		//   1. Client sends initialize → Generate() creates placeholder
		//   2. Client sends DELETE before OnRegisterSession hook fires
		//   3. We mark the placeholder as terminated (don't delete it)
		//   4. CreateSession() hook fires → sees terminated flag → fails fast
		//
		// Without this branch, CreateSession() would open backend HTTP connections
		// for a session the client already terminated, silently resurrecting it.
		//
		// We mark (not delete) so Validate() can return isTerminated=true, which
		// lets the SDK distinguish "actively terminated" from "never existed".
		// TTL cleanup will remove the placeholder later.
		sess.SetMetadata(MetadataKeyTerminated, MetadataValTrue)
		if replaceErr := sm.storage.UpsertSession(sess); replaceErr != nil {
			slog.Warn("Manager.Terminate: failed to persist terminated flag for placeholder; attempting delete fallback",
				"session_id", sessionID, "error", replaceErr)
			if deleteErr := sm.storage.Delete(sessionID); deleteErr != nil {
				return false, fmt.Errorf(
					"Manager.Terminate: failed to persist terminated flag and delete placeholder: upsertErr=%v, deleteErr=%w",
					replaceErr, deleteErr)
			}
		}
	}

	slog.Info("Manager.Terminate: session terminated", "session_id", sessionID)
	return false, nil
}

// GetMultiSession retrieves the fully-formed MultiSession for a given SDK session ID.
// Returns (nil, false) if the session does not exist or has not yet been
// upgraded from placeholder to MultiSession.
func (sm *Manager) GetMultiSession(sessionID string) (vmcpsession.MultiSession, bool) {
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
// When the session factory is configured with an aggregator (WithAggregator),
// tools are in their final resolved form — overrides and conflict resolution
// applied via ProcessPreQueriedCapabilities. Each handler passes the resolved
// tool name to CallTool, which translates it back to the original backend name
// via GetBackendCapabilityName.
//
// Without an aggregator, raw backend tool names are used as-is (no overrides
// or conflict resolution applied).
func (sm *Manager) GetAdaptedTools(sessionID string) ([]mcpserver.ServerTool, error) {
	multiSess, ok := sm.GetMultiSession(sessionID)
	if !ok {
		return nil, fmt.Errorf("Manager.GetAdaptedTools: session %q not found or not a multi-session", sessionID)
	}

	domainTools := multiSess.Tools()
	sdkTools := make([]mcpserver.ServerTool, 0, len(domainTools))

	for _, domainTool := range domainTools {
		schemaJSON, err := json.Marshal(domainTool.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("Manager.GetAdaptedTools: failed to marshal schema for tool %s: %w", domainTool.Name, err)
		}

		tool := mcp.Tool{
			Name:           domainTool.Name,
			Description:    domainTool.Description,
			RawInputSchema: schemaJSON,
		}

		capturedSess := multiSess
		capturedSessionID := sessionID
		capturedToolName := domainTool.Name
		handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args, ok := req.Params.Arguments.(map[string]any)
			if !ok {
				wrappedErr := fmt.Errorf("%w: arguments must be object, got %T", vmcp.ErrInvalidInput, req.Params.Arguments)
				slog.Warn("invalid arguments for tool", "tool", capturedToolName, "error", wrappedErr)
				return mcp.NewToolResultError(wrappedErr.Error()), nil
			}

			meta := conversion.FromMCPMeta(req.Params.Meta)
			caller, _ := auth.IdentityFromContext(ctx)

			result, callErr := capturedSess.CallTool(ctx, caller, capturedToolName, args, meta)
			if callErr != nil {
				if errors.Is(callErr, sessiontypes.ErrUnauthorizedCaller) || errors.Is(callErr, sessiontypes.ErrNilCaller) {
					slog.Warn("caller authorization failed, terminating session",
						"session_id", capturedSessionID, "tool", capturedToolName, "error", callErr)
					if _, termErr := sm.Terminate(capturedSessionID); termErr != nil {
						slog.Error("failed to terminate session after auth failure",
							"session_id", capturedSessionID, "error", termErr)
					}
					return mcp.NewToolResultError(fmt.Sprintf("Unauthorized: %v", callErr)), nil
				}
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
		slog.Debug("Manager.GetAdaptedTools: adapted tool", "session_id", sessionID, "tool", domainTool.Name)
	}

	return sdkTools, nil
}

// GetAdaptedResources returns SDK-format resources for the given session, with handlers
// that delegate read requests directly to the session's ReadResource() method.
func (sm *Manager) GetAdaptedResources(sessionID string) ([]mcpserver.ServerResource, error) {
	multiSess, ok := sm.GetMultiSession(sessionID)
	if !ok {
		return nil, fmt.Errorf("Manager.GetAdaptedResources: session %q not found or not a multi-session", sessionID)
	}

	domainResources := multiSess.Resources()
	sdkResources := make([]mcpserver.ServerResource, 0, len(domainResources))

	for _, domainResource := range domainResources {
		resource := mcp.Resource{
			Name:        domainResource.Name,
			URI:         domainResource.URI,
			Description: domainResource.Description,
			MIMEType:    domainResource.MimeType,
		}

		capturedSess := multiSess
		capturedSessionID := sessionID
		capturedResourceURI := domainResource.URI
		handler := func(ctx context.Context, _ mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			caller, _ := auth.IdentityFromContext(ctx)

			result, readErr := capturedSess.ReadResource(ctx, caller, capturedResourceURI)
			if readErr != nil {
				if errors.Is(readErr, sessiontypes.ErrUnauthorizedCaller) || errors.Is(readErr, sessiontypes.ErrNilCaller) {
					slog.Warn("caller authorization failed, terminating session",
						"session_id", capturedSessionID, "resource", capturedResourceURI, "error", readErr)
					if _, termErr := sm.Terminate(capturedSessionID); termErr != nil {
						slog.Error("failed to terminate session after auth failure",
							"session_id", capturedSessionID, "error", termErr)
					}
					return nil, fmt.Errorf("unauthorized: %w", readErr)
				}
				return nil, readErr
			}

			mimeType := result.MimeType
			if mimeType == "" {
				mimeType = "application/octet-stream"
			}
			return []mcp.ResourceContents{
				mcp.TextResourceContents{
					URI:      capturedResourceURI,
					MIMEType: mimeType,
					Text:     string(result.Contents),
				},
			}, nil
		}

		sdkResources = append(sdkResources, mcpserver.ServerResource{
			Resource: resource,
			Handler:  handler,
		})
		slog.Debug("Manager.GetAdaptedResources: adapted resource", "session_id", sessionID, "uri", domainResource.URI)
	}

	return sdkResources, nil
}
