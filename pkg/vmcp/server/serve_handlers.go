// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// This file holds the Serve-path (s.core != nil) capability wiring. On the Serve
// path the core VMCP is the single authoritative aggregation: session
// registration sources the advertised tool/resource set from core.ListTools /
// core.ListResources (admission-filtered, composites included) and installs SDK
// handlers that route invocations through core.CallTool / core.ReadResource with
// an explicit *auth.Identity. The session factory does NOT aggregate on this path,
// so there is no second aggregation and no drift between the advertised set and
// the call path. Identity binding is still enforced by the session layer via
// enforceSessionBinding before each call reaches the core.
//
// The legacy server.New path (s.core == nil) is untouched: it sources capabilities
// from the session factory's aggregation via Manager.GetAdaptedTools/Resources and
// routes through the session's own MultiSession.

// injectCoreSessionCapabilities sources the session's advertised capability set
// from the core and installs it on the SDK session. It is invoked from
// handleSessionRegistrationImpl on the Serve path, after the bound session record
// has been created. The core.ListTools / core.ListResources calls here are the
// single CORE aggregation per session.
//
// "Single core aggregation" — not "the only backend work per session" — because the
// preceding CreateSession opens the session's backend connections via the factory.
// To honor AC2 (no double-aggregation, no drift), the composition root MUST configure
// the Serve-path session factory WITHOUT its own aggregator (see the contract on
// ServerConfig.SessionManagerConfig); otherwise the factory would aggregate a second,
// divergent set whose routing table this path discards.
//
// Prompts are intentionally not injected: per-session prompt injection is not yet
// supported by the SDK (parity with the legacy path, which also omits them).
func (s *Server) injectCoreSessionCapabilities(ctx context.Context, session server.ClientSession) error {
	sessionID := session.SessionID()

	// Identity is read from the SDK hook context here, at the transport boundary,
	// and passed explicitly to the core — the core never reads it from context.
	identity, _ := auth.IdentityFromContext(ctx)

	tools, err := s.coreSessionTools(ctx, sessionID, identity)
	if err != nil {
		slog.Error("failed to list core tools for session", "session_id", sessionID, "error", err)
		return err
	}
	resources, err := s.coreSessionResources(ctx, sessionID, identity)
	if err != nil {
		slog.Error("failed to list core resources for session", "session_id", sessionID, "error", err)
		return err
	}

	if len(resources) > 0 {
		if err := setSessionResourcesDirect(session, resources); err != nil {
			slog.Error("failed to add session resources", "session_id", sessionID, "error", err)
			return err
		}
	}
	if len(tools) > 0 {
		if err := setSessionToolsDirect(session, tools); err != nil {
			slog.Error("failed to add session tools", "session_id", sessionID, "error", err)
			return err
		}
	}

	slog.Info("session capabilities injected from core",
		"session_id", sessionID,
		"tool_count", len(tools),
		"resource_count", len(resources))
	return nil
}

// coreSessionTools queries the core for the tools advertised to identity and
// adapts them to SDK ServerTools whose handlers route through core.CallTool. The
// core owns conflict resolution and backend-name translation, so the SDK tool name
// is forwarded as-is to core.CallTool.
func (s *Server) coreSessionTools(
	ctx context.Context, sessionID string, identity *auth.Identity,
) ([]server.ServerTool, error) {
	domainTools, err := s.core.ListTools(ctx, identity)
	if err != nil {
		return nil, fmt.Errorf("core ListTools: %w", err)
	}

	sdkTools := make([]server.ServerTool, 0, len(domainTools))
	for _, domainTool := range domainTools {
		schemaJSON, err := json.Marshal(domainTool.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("marshal schema for tool %s: %w", domainTool.Name, err)
		}

		tool := mcp.Tool{
			Name:           domainTool.Name,
			Description:    domainTool.Description,
			RawInputSchema: schemaJSON,
			Annotations:    conversion.ToMCPToolAnnotations(domainTool.Annotations),
		}
		// Unlike the required InputSchema (a marshal failure aborts registration above),
		// the optional OutputSchema is best-effort: on failure the tool is still advertised
		// without it. Mirrors the legacy GetAdaptedTools adapter.
		if domainTool.OutputSchema != nil {
			if outputSchemaJSON, marshalErr := json.Marshal(domainTool.OutputSchema); marshalErr != nil {
				slog.Warn("failed to marshal tool output schema", "tool", domainTool.Name, "error", marshalErr)
			} else {
				tool.RawOutputSchema = outputSchemaJSON
			}
		}

		sdkTools = append(sdkTools, server.ServerTool{
			Tool:    tool,
			Handler: s.coreToolHandler(sessionID, domainTool.Name),
		})
	}
	return sdkTools, nil
}

// coreSessionResources queries the core for the resources advertised to identity
// and adapts them to SDK ServerResources whose handlers route through
// core.ReadResource.
func (s *Server) coreSessionResources(
	ctx context.Context, sessionID string, identity *auth.Identity,
) ([]server.ServerResource, error) {
	domainResources, err := s.core.ListResources(ctx, identity)
	if err != nil {
		return nil, fmt.Errorf("core ListResources: %w", err)
	}

	sdkResources := make([]server.ServerResource, 0, len(domainResources))
	for _, domainResource := range domainResources {
		sdkResources = append(sdkResources, server.ServerResource{
			Resource: mcp.Resource{
				Name:        domainResource.Name,
				URI:         domainResource.URI,
				Description: domainResource.Description,
				MIMEType:    domainResource.MimeType,
			},
			Handler: s.coreResourceHandler(sessionID, domainResource.URI),
		})
	}
	return sdkResources, nil
}

// coreToolHandler builds the SDK handler for a Serve-path tool. It enforces the
// session's identity binding, then delegates to core.CallTool with the caller's
// explicit identity. Admission (authorization) is the core's responsibility.
func (s *Server) coreToolHandler(sessionID, toolName string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, ok := req.Params.Arguments.(map[string]any)
		if !ok {
			wrappedErr := fmt.Errorf("%w: arguments must be object, got %T", vmcp.ErrInvalidInput, req.Params.Arguments)
			slog.Warn("invalid arguments for tool", "tool", toolName, "error", wrappedErr)
			return mcp.NewToolResultError(wrappedErr.Error()), nil
		}

		caller, _ := auth.IdentityFromContext(ctx)
		if err := s.enforceSessionBinding(sessionID, caller); err != nil {
			s.terminateOnBindingFailure(sessionID, toolName, err)
			return mcp.NewToolResultError(fmt.Sprintf("Unauthorized: %v", err)), nil
		}

		result, err := s.core.CallTool(ctx, caller, toolName, args, conversion.FromMCPMeta(req.Params.Meta))
		if err != nil {
			// Admission denial returns a generic message so the underlying authorizer
			// error is never forwarded to the client.
			if errors.Is(err, vmcp.ErrAuthorizationFailed) {
				return mcp.NewToolResultError("call denied by authorization policy"), nil
			}
			return mcp.NewToolResultError(err.Error()), nil
		}

		return &mcp.CallToolResult{
			Result:            mcp.Result{Meta: conversion.ToMCPMeta(result.Meta)},
			Content:           conversion.ToMCPContents(result.Content),
			StructuredContent: result.StructuredContent,
			IsError:           result.IsError,
		}, nil
	}
}

// coreResourceHandler builds the SDK handler for a Serve-path resource. It mirrors
// coreToolHandler: binding check, then core.ReadResource with explicit identity.
func (s *Server) coreResourceHandler(
	sessionID, uri string,
) func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	return func(ctx context.Context, _ mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		caller, _ := auth.IdentityFromContext(ctx)
		if err := s.enforceSessionBinding(sessionID, caller); err != nil {
			s.terminateOnBindingFailure(sessionID, uri, err)
			return nil, fmt.Errorf("unauthorized: %w", err)
		}

		result, err := s.core.ReadResource(ctx, caller, uri)
		if err != nil {
			if errors.Is(err, vmcp.ErrAuthorizationFailed) {
				return nil, errors.New("read denied by authorization policy")
			}
			return nil, err
		}
		return conversion.ToMCPResourceContents(result.Contents), nil
	}
}

// enforceSessionBinding validates caller against the session's stored identity
// binding. It is the SOLE identity-binding enforcement point on the Serve call
// path: requests reach the core directly, bypassing the BindSession decorator that
// performs this check on MultiSession.CallTool on the legacy path. The SDK's
// SessionIdManager.Validate only gates session existence/termination, not caller
// identity, so without this check a different principal could reuse the session ID.
//
// It fails closed in two ways: a missing session record (already terminated/expired)
// rejects the caller, and ValidateCaller rejects an empty/unparsable binding with
// ErrSessionOwnerUnknown. Unlike the legacy GetAdaptedTools handler, which terminates
// only on ErrUnauthorizedCaller/ErrNilCaller, terminateOnBindingFailure terminates on
// any rejection here — intentional fail-closed behavior, not a bug.
func (s *Server) enforceSessionBinding(sessionID string, caller *auth.Identity) error {
	sess, ok := s.vmcpSessionMgr.GetMultiSession(sessionID)
	if !ok {
		return sessiontypes.ErrUnauthorizedCaller
	}
	// Single-key read: avoid GetMetadata()'s per-call full-map copy on this hot path.
	storedBinding, _ := sess.GetMetadataValue(vmcpsession.MetadataKeyIdentityBinding)
	return vmcpsession.ValidateCaller(storedBinding, caller)
}

// terminateOnBindingFailure logs the hijack-prevention event and terminates the
// session, mirroring GetAdaptedTools' handling of ErrUnauthorizedCaller/ErrNilCaller.
func (s *Server) terminateOnBindingFailure(sessionID, capability string, err error) {
	slog.Warn("caller authorization failed, terminating session",
		"session_id", sessionID, "capability", capability, "error", err)
	if _, termErr := s.vmcpSessionMgr.Terminate(sessionID); termErr != nil {
		slog.Error("failed to terminate session after auth failure",
			"session_id", sessionID, "error", termErr)
	}
}
