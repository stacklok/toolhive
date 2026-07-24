// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/headerforward"
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

// listChangedResyncWorker coalesces a session's tools/list_changed resyncs.
//
// The sink runs on the backend's receive-loop goroutine and must not block
// (vmcpsession.ListChangedSink contract), so trigger() only flips a dirty flag
// and, at most, starts ONE worker goroutine — it never does the resync inline
// and never spawns a goroutine per notification. At most one resync is ever in
// flight per session; notifications that arrive while one runs collapse into a
// single follow-up run. This bounds a misbehaving backend to O(1) concurrent
// resync work per session regardless of how fast it emits notifications.
type listChangedResyncWorker struct {
	// run performs exactly one resync using the given (server-lifetime) context.
	// It is self-contained: liveness check, context reconstruction, and the
	// re-derive-and-replace of the session's tools.
	run func(ctx context.Context)
	// baseCtx bounds every run to the server lifetime (cancelled on Stop) so a
	// late notification cannot start work that outlives the server.
	baseCtx context.Context

	mu      sync.Mutex
	running bool
	dirty   bool
}

// trigger requests a resync. It is non-blocking and safe to call from the
// backend receive-loop goroutine: if a resync is already running it just marks
// the state dirty (coalescing), otherwise it starts the single worker goroutine.
func (w *listChangedResyncWorker) trigger() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running {
		w.dirty = true
		return
	}
	w.running = true
	go w.loop()
}

// loop runs resyncs until no further notification arrived during the last run.
// It is the single worker goroutine started by trigger; it exits when idle so
// an idle session holds no goroutine.
func (w *listChangedResyncWorker) loop() {
	for {
		w.run(w.baseCtx)

		w.mu.Lock()
		if !w.dirty {
			w.running = false
			w.mu.Unlock()
			return
		}
		w.dirty = false
		w.mu.Unlock()
	}
}

// buildListChangedSink returns the sink passed to
// SessionManager.CreateSession for a newly-registered session. The returned
// sink runs on the backend receive-loop goroutine and only hands work off to a
// per-session coalescing worker (see listChangedResyncWorker) — it never does
// the cache purge or backend re-aggregation inline.
//
// It closes over the SDK ClientSession, the session ID, and the caller's
// identity AND per-request forwarded headers captured AT REGISTRATION TIME
// (issue #5748, decision B1/Option 2). The async resync reconstructs a context
// carrying that identity + those headers before calling into the core, because
// the capability cache key and the outbound backend authentication are derived
// from the CONTEXT (auth.IdentityFromContext / headerforward.ForwardedHeaders-
// FromContext — see pkg/vmcp/aggregator/caching_aggregator.go and
// pkg/vmcp/client), NOT from an explicit identity argument. Without this the
// resync would enumerate backends unauthenticated and could advertise metadata
// the principal's own credentials would not surface (or wrongly drop
// credential-gated tools), while replacing the correctly-scoped registration
// set.
//
// Token-staleness risk: the captured identity is a snapshot. If its upstream
// tokens are refreshed after registration (see #5323), this resync's
// core.ListTools call authorizes/aggregates using the (possibly stale)
// captured tokens, not the live per-request ones. This only affects the
// accuracy of the asynchronous resync's own view — every live call still
// authenticates via the fresh per-request identity through
// enforceSessionBinding, so staleness here cannot grant a call that would
// otherwise be denied.
func (s *Server) buildListChangedSink(
	sessionID string, session server.ClientSession, identity *auth.Identity, forwardedHeaders map[string]string,
) vmcpsession.ListChangedSink {
	worker := &listChangedResyncWorker{
		baseCtx: s.resyncBaseCtx,
		run: func(ctx context.Context) {
			s.runListChangedResync(ctx, sessionID, session, identity, forwardedHeaders)
		},
	}
	return func(_ context.Context, backendWorkloadID string, kind vmcpsession.ChangeKind) {
		if kind != vmcpsession.KindTools {
			// Resources/prompts list_changed: out of scope (see file doc).
			return
		}
		slog.Debug("backend reported tools/list_changed; scheduling session resync",
			"session_id", sessionID, "backend_id", backendWorkloadID)
		worker.trigger()
	}
}

// runListChangedResync performs one coalesced resync for a session. It runs on
// the worker goroutine (off the receive loop). Order matters:
//  1. Liveness guard — skip (and do no work) if the session was terminated, so
//     a storm of notifications for a dead session cannot drive re-aggregation.
//  2. Reconstruct the registration-time request context (identity + forwarded
//     headers) atop the server-lifetime base context, so the cache key and
//     backend auth match a real request from this principal.
//  3. Invalidate the capability cache so the re-derivation below re-sweeps the
//     backend rather than reading the entry it just purged.
//  4. Re-derive and REPLACE the session's advertised tool set.
func (s *Server) runListChangedResync(
	baseCtx context.Context,
	sessionID string,
	session server.ClientSession,
	identity *auth.Identity,
	forwardedHeaders map[string]string,
) {
	// Liveness guard: if the session is gone (terminated/expired) there is
	// nothing to resync, and doing the work would waste a full backend sweep.
	if _, ok := s.vmcpSessionMgr.GetMultiSession(baseCtx, sessionID); !ok {
		slog.Debug("skipping tools/list_changed resync for terminated session", "session_id", sessionID)
		return
	}

	ctx := auth.WithIdentity(baseCtx, identity)
	if len(forwardedHeaders) > 0 {
		ctx = headerforward.WithForwardedHeaders(ctx, forwardedHeaders)
	}

	// Invalidate FIRST so resyncSessionTools' core.ListTools re-sweeps the
	// backend instead of re-reading the stale cached aggregation. The purge is
	// global (all identities) — see InvalidateCapabilityCache — but coalescing
	// bounds it to at most once per resync burst.
	s.core.InvalidateCapabilityCache()

	if err := s.resyncSessionTools(ctx, session, sessionID, identity); err != nil {
		slog.Warn("failed to resync session tools after backend list_changed",
			"session_id", sessionID, "error", err)
	}
}

// resyncSessionTools re-derives sessionID's advertised tool set (via
// serveSessionTools — the core, cache now cold, plus optimizer meta-tools when
// enabled) and REPLACES the SDK session's tool store with it, so a tool the
// backend removed (and the core therefore no longer advertises) disappears
// rather than lingering (unlike setSessionToolsDirect's registration-time
// MERGE, which only adds). The go-sdk server then auto-emits
// notifications/tools/list_changed to the downstream client, since serve.go
// enables WithToolCapabilities(true).
//
// ctx must already carry the resyncing principal's identity and forwarded
// headers (runListChangedResync builds it) so serveSessionTools ->
// core.ListTools enumerates backends with the correct credentials and cache key.
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
