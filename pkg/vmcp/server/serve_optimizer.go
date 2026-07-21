// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	"github.com/stacklok/toolhive/pkg/vmcp/schema"
	"github.com/stacklok/toolhive/pkg/vmcp/session/optimizerdec"
)

// This file holds the Serve-path optimizer wiring. Unlike the legacy server.New
// path — where the optimizer is a session-factory decorator that indexes the
// factory's aggregated tools and replaces MultiSession.Tools() with
// find_tool/call_tool — the Serve path keeps the optimizer a Serve-layer,
// session-scoped index but sources its tool set from the core:
//
//   - The advertised set is built from core.ListTools (admission-filtered,
//     aggregated, composites included) via coreSessionTools, whose handlers route
//     through core.CallTool.
//   - call_tool's inner invocation dispatches to that core handler, so the core
//     admission seam authorizes the inner target by its real name (closing the
//     deferred optimizer-admission gap documented in core/admission.go).
//
// The optimizer is NOT placed in the stateless core: it upserts a session's tools
// into a shared FTS5 store, which is transport/session state. To avoid indexing a
// second, divergent set, the session factory's optimizer decorator is skipped on
// this path (FactoryConfig.AdvertiseFromCore); the resolved factory is consumed
// directly here via s.optimizerFactory.

// serveSessionTools returns the SDK tools to advertise for a Serve-path session:
// the core's advertised set, or — when the optimizer is enabled — the find_tool /
// call_tool meta-tools built over that set. Both session registration
// (injectCoreSessionCapabilities) and cross-pod re-injection (lazyInjectSessionTools)
// call it, so the two paths advertise an identical set for the same identity.
func (s *Server) serveSessionTools(
	ctx context.Context, sessionID string, identity *auth.Identity,
) ([]server.ServerTool, error) {
	coreTools, err := s.coreSessionTools(ctx, sessionID, identity)
	if err != nil {
		return nil, err
	}
	if s.optimizerFactory == nil {
		return coreTools, nil
	}
	return s.optimizerSessionTools(ctx, sessionID, coreTools)
}

// optimizerSessionTools builds a per-session optimizer over coreTools (the core's
// advertised set, whose handlers route through core.CallTool) and returns exactly
// the find_tool and call_tool meta-tools. find_tool searches this session's core
// tools; call_tool dispatches the named inner tool through its core handler so the
// inner target is admission-checked by the core. Building the optimizer upserts
// coreTools into the shared store; the returned optimizer is telemetry-wrapped, so
// find_tool/call_tool metrics and traces fire on this path as on the legacy one.
func (s *Server) optimizerSessionTools(
	ctx context.Context, sessionID string, coreTools []server.ServerTool,
) ([]server.ServerTool, error) {
	// This runs once per registration AND once per cross-pod lazyInjectSessionTools
	// rehydration; each build re-upserts coreTools into the shared store (and, when
	// embeddings are configured, an embedding round-trip per tool). Rows do not
	// accumulate — the store upserts by tool-name PK (INSERT OR REPLACE) — so this is
	// repeated work, not a leak. Acceptable while the Serve path is test-only;
	// skipping the re-upsert on rehydration is a deferred optimization (tracked for
	// #5445), not done now to avoid premature optimization without measured evidence.
	opt, err := s.optimizerFactory(ctx, coreTools)
	if err != nil {
		return nil, fmt.Errorf("build session optimizer: %w", err)
	}

	defs := optimizerdec.OptimizerTools()
	sdkTools := make([]server.ServerTool, 0, len(defs))
	for _, def := range defs {
		schemaJSON, marshalErr := json.Marshal(def.InputSchema)
		if marshalErr != nil {
			return nil, fmt.Errorf("marshal schema for %s: %w", def.Name, marshalErr)
		}
		handler, handlerErr := s.optimizerToolHandler(sessionID, def.Name, opt)
		if handlerErr != nil {
			return nil, handlerErr
		}
		sdkTools = append(sdkTools, server.ServerTool{
			Tool: mcp.Tool{
				Name:           def.Name,
				Description:    def.Description,
				RawInputSchema: schemaJSON,
			},
			Handler: handler,
		})
	}

	slog.Debug("session optimizer built over core tools",
		"session_id", sessionID, "indexed_tool_count", len(coreTools))
	return sdkTools, nil
}

// optimizerToolHandler returns the SDK handler for a Serve-path optimizer meta-tool.
// It is total over the two names OptimizerTools advertises; any other name is a
// programming error (a definition without a wired handler) and fails registration.
func (s *Server) optimizerToolHandler(
	sessionID, toolName string, opt optimizer.Optimizer,
) (server.ToolHandlerFunc, error) {
	switch toolName {
	case optimizerdec.FindToolName:
		return s.optimizerFindToolHandler(sessionID, opt), nil
	case optimizerdec.CallToolName:
		return s.optimizerCallToolHandler(sessionID, opt), nil
	default:
		return nil, fmt.Errorf("unknown optimizer meta-tool %q", toolName)
	}
}

// optimizerFindToolHandler builds the find_tool SDK handler. It enforces the
// session's identity binding (anti-hijack) before searching, then returns the
// optimizer's FindToolOutput marshalled as both text and structured content,
// mirroring the legacy optimizerdec handler.
func (s *Server) optimizerFindToolHandler(sessionID string, opt optimizer.Optimizer) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		caller, _ := auth.IdentityFromContext(ctx)
		if err := s.enforceSessionBinding(ctx, sessionID, caller); err != nil {
			s.terminateOnBindingFailure(sessionID, optimizerdec.FindToolName, err)
			return mcp.NewToolResultError(fmt.Sprintf("Unauthorized: %v", err)), nil
		}

		args, ok := req.Params.Arguments.(map[string]any)
		if !ok {
			return mcp.NewToolResultError(
				fmt.Sprintf("%v: arguments must be object, got %T", vmcp.ErrInvalidInput, req.Params.Arguments)), nil
		}

		input, err := schema.Translate[optimizer.FindToolInput](args)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
		}

		output, err := opt.FindTool(ctx, input)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("find_tool failed: %v", err)), nil
		}
		// Defensive parity with the legacy optimizerdec handler (and the sibling
		// call_tool handler): the production optimizer never returns (nil, nil), but
		// guard so a nil output cannot marshal to "null" and surface as a success.
		if output == nil {
			return mcp.NewToolResultError("find_tool: optimizer returned nil result"), nil
		}

		jsonBytes, err := json.Marshal(output)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to marshal find_tool output: %v", err)), nil
		}

		// Unmarshal cannot fail: jsonBytes was just produced by json.Marshal above.
		var structured map[string]any
		_ = json.Unmarshal(jsonBytes, &structured)

		result := mcp.NewToolResultText(string(jsonBytes))
		result.StructuredContent = structured
		return result, nil
	}
}

// optimizerCallToolHandler builds the call_tool SDK handler. It enforces the
// session's identity binding, then delegates to opt.CallTool, which dispatches to
// the inner tool's coreToolHandler — routing through core.CallTool with the real
// inner tool name. The core admission seam authorizes the inner target there; a
// denial surfaces as coreToolHandler's generic "call denied by authorization
// policy" result (the optimizer returns it verbatim), so no authorizer detail
// leaks. The MCP result from the optimizer is returned as-is.
func (s *Server) optimizerCallToolHandler(sessionID string, opt optimizer.Optimizer) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		caller, _ := auth.IdentityFromContext(ctx)
		if err := s.enforceSessionBinding(ctx, sessionID, caller); err != nil {
			s.terminateOnBindingFailure(sessionID, optimizerdec.CallToolName, err)
			return mcp.NewToolResultError(fmt.Sprintf("Unauthorized: %v", err)), nil
		}

		args, ok := req.Params.Arguments.(map[string]any)
		if !ok {
			return mcp.NewToolResultError(
				fmt.Sprintf("%v: arguments must be object, got %T", vmcp.ErrInvalidInput, req.Params.Arguments)), nil
		}

		input, err := schema.Translate[optimizer.CallToolInput](args)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
		}

		result, err := opt.CallTool(ctx, input)
		if err != nil {
			return conversion.ErrorToToolResult(fmt.Errorf("call_tool failed: %w", err)), nil
		}
		// Defensive parity with the legacy optimizerdec handler: the production
		// optimizer never returns (nil, nil), but guard so a future implementation
		// cannot hand a bare nil result to the SDK.
		if result == nil {
			return mcp.NewToolResultError("call_tool: optimizer returned nil result"), nil
		}
		return result, nil
	}
}
