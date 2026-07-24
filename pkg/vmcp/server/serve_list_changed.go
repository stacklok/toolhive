// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/auth"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// This file holds the tools list_changed propagation added by #5748: a
// persistent backend connection observing notifications/tools/list_changed
// invalidates the shared capability cache and resyncs the affected session's
// advertised tool set, so a client that already initialized eventually sees
// the backend's change reflected in tools/list without reconnecting.
//
// Scope: TOOLS only. Resources/prompts list_changed propagation is a tracked
// follow-up (see docs/arch/10-virtual-mcp-architecture.md) — the backend
// connector (pkg/vmcp/session/internal/backend) does not even dispatch those
// notification methods to this sink yet.

// buildListChangedSink returns the sink passed to
// SessionManager.CreateSession for a newly-registered session. It closes over
// the SDK ClientSession and the caller's identity captured AT REGISTRATION
// TIME (issue #5748, decision B1/Option 2): a later asynchronous firing reuses
// this snapshot identity to re-derive the advertised tool set, rather than
// resolving a fresh one.
//
// Token-staleness risk: if the identity's upstream tokens are refreshed after
// registration (see #5323), this resync's core.ListTools call authorizes and
// aggregates using the (potentially stale) captured tokens, not the live
// per-request ones. This only affects the ACCURACY of this asynchronous
// resync's own admission view — every live call still authenticates via the
// fresh per-request identity through enforceSessionBinding, so a stale token
// here cannot grant a call that would otherwise be denied.
func (s *Server) buildListChangedSink(
	sessionID string, session server.ClientSession, identity *auth.Identity,
) vmcpsession.ListChangedSink {
	return func(ctx context.Context, backendWorkloadID, kind string) {
		if kind != "tools" {
			// Resources/prompts list_changed: out of scope (see file doc).
			return
		}
		// Invalidate the cache FIRST so resyncSessionTools' core.ListTools call
		// below re-sweeps the backend instead of immediately re-reading the
		// stale cached aggregation InvalidateCapabilityCache just purged.
		s.core.InvalidateCapabilityCache()
		if err := s.resyncSessionTools(ctx, session, sessionID, identity); err != nil {
			slog.Warn("failed to resync session tools after backend list_changed",
				"session_id", sessionID, "backend_id", backendWorkloadID, "error", err)
		}
	}
}

// resyncSessionTools re-derives sessionID's advertised tool set (via
// serveSessionTools — the core, cache now cold, plus optimizer meta-tools when
// enabled) and REPLACES the SDK session's tool store with it, so a tool the
// backend removed disappears from the advertised set rather than lingering
// (unlike setSessionToolsDirect's registration-time MERGE, which only adds).
// The go-sdk server auto-emits notifications/tools/list_changed to the
// downstream client after SetSessionTools, since serve.go enables
// WithToolCapabilities(true).
func (s *Server) resyncSessionTools(
	ctx context.Context, session server.ClientSession, sessionID string, identity *auth.Identity,
) error {
	tools, err := s.serveSessionTools(ctx, sessionID, identity)
	if err != nil {
		return fmt.Errorf("resync session tools: core ListTools for session %s: %w", sessionID, err)
	}

	sessionWithTools, ok := session.(server.SessionWithTools)
	if !ok {
		return fmt.Errorf("resync session tools: session %s does not support per-session tools", sessionID)
	}

	toolMap := make(map[string]server.ServerTool, len(tools))
	for _, tool := range tools {
		toolMap[tool.Tool.Name] = tool
	}
	sessionWithTools.SetSessionTools(toolMap)

	slog.Debug("resynced session tools after backend list_changed",
		"session_id", sessionID, "tool_count", len(tools))
	return nil
}
