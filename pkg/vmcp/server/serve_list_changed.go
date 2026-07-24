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

// This file holds the tools/resources/prompts list_changed propagation added
// by #5748 (tools) and extended by #5969 (resources/resource templates and
// prompts): a persistent backend connection observing
// notifications/tools/list_changed, notifications/resources/list_changed, or
// notifications/prompts/list_changed invalidates the shared capability cache
// and resyncs the affected session's advertised set for that capability kind,
// so a client that already initialized eventually sees the backend's change
// reflected in tools/list, resources/list, resources/templates/list, or
// prompts/list without reconnecting.
//
// Scope: additions AND removals propagate for tools; for resources/resource
// templates/prompts, only ADDITIONS propagate today — see the add-only caveat
// on resyncSessionResources/resyncSessionPrompts and
// docs/arch/10-virtual-mcp-architecture.md. Cross-pod session restore
// (RestoreSession) does not thread a sink at all (no live ClientSession to
// resync there).

// listChangedResyncWorker coalesces a session's list_changed resyncs for ONE
// capability kind (tools, resources, or prompts). buildListChangedSink builds
// one worker per (session, kind): a tools notification resyncing tools alone,
// without touching resources/prompts, matters because each capability's
// resync REPLACES that capability's session store, and re-deriving one kind
// must not force a redundant re-apply (and possible spurious downstream
// list_changed) of the others.
//
// The sink runs on the backend's receive-loop goroutine and must not block
// (vmcpsession.ListChangedSink contract), so trigger() only flips a dirty flag
// and, at most, starts ONE worker goroutine — it never does the resync inline
// and never spawns a goroutine per notification. At most one resync is ever in
// flight per (session, kind); notifications that arrive while one runs
// collapse into a single follow-up run. This bounds a misbehaving backend to
// O(1) concurrent resync work per (session, kind) regardless of how fast it
// emits notifications.
type listChangedResyncWorker struct {
	// run performs exactly one resync using the given (server-lifetime) context.
	// It is self-contained: liveness check, context reconstruction, and the
	// re-derive-and-replace of the session's advertised set for this worker's
	// capability kind (tools, resources, or prompts).
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
// per-(session, kind) coalescing worker (see listChangedResyncWorker) — it
// never does the cache purge or backend re-aggregation inline.
//
// It builds one listChangedResyncWorker per ChangeKind (tools, resources,
// prompts) rather than a single shared worker: each worker's run closure
// resyncs only its own capability kind, so a tools notification cannot force a
// spurious re-apply (and possible downstream list_changed) of the session's
// resources/prompts overlay, and vice versa.
//
// Each worker closes over the SDK ClientSession, the session ID, and the
// caller's identity AND per-request forwarded headers captured AT
// REGISTRATION TIME (issue #5748, decision B1/Option 2). The async resync
// reconstructs a context carrying that identity + those headers before calling
// into the core, because the capability cache key and the outbound backend
// authentication are derived from the CONTEXT (auth.IdentityFromContext /
// headerforward.ForwardedHeadersFromContext — see
// pkg/vmcp/aggregator/caching_aggregator.go and pkg/vmcp/client), NOT from an
// explicit identity argument. Without this the resync would enumerate
// backends unauthenticated and could advertise metadata the principal's own
// credentials would not surface (or wrongly drop credential-gated
// tools/resources/prompts), while replacing the correctly-scoped registration
// set.
//
// Token-staleness risk: the captured identity is a snapshot. If its upstream
// tokens are refreshed after registration (see #5323), a resync's
// core.ListTools/ListResources/ListPrompts call authorizes/aggregates using
// the (possibly stale) captured tokens, not the live per-request ones. This
// only affects the accuracy of the asynchronous resync's own view — every
// live call still authenticates via the fresh per-request identity through
// enforceSessionBinding, so staleness here cannot grant a call that would
// otherwise be denied.
func (s *Server) buildListChangedSink(
	sessionID string, session server.ClientSession, identity *auth.Identity, forwardedHeaders map[string]string,
) vmcpsession.ListChangedSink {
	newWorker := func(kind vmcpsession.ChangeKind) *listChangedResyncWorker {
		return &listChangedResyncWorker{
			baseCtx: s.resyncBaseCtx,
			run: func(ctx context.Context) {
				s.runListChangedResync(ctx, sessionID, session, identity, forwardedHeaders, kind)
			},
		}
	}
	workers := map[vmcpsession.ChangeKind]*listChangedResyncWorker{
		vmcpsession.KindTools:     newWorker(vmcpsession.KindTools),
		vmcpsession.KindResources: newWorker(vmcpsession.KindResources),
		vmcpsession.KindPrompts:   newWorker(vmcpsession.KindPrompts),
	}
	return func(_ context.Context, backendWorkloadID string, kind vmcpsession.ChangeKind) {
		worker, ok := workers[kind]
		if !ok {
			slog.Debug("ignoring list_changed notification of unknown kind",
				"session_id", sessionID, "backend_id", backendWorkloadID, "kind", kind)
			return
		}
		slog.Debug("backend reported list_changed; scheduling session resync",
			"session_id", sessionID, "backend_id", backendWorkloadID, "kind", kind)
		worker.trigger()
	}
}

// runListChangedResync performs one coalesced resync for a session, for the
// given capability kind. It runs on the worker goroutine (off the receive
// loop). Order matters:
//  1. Liveness guard — skip (and do no work) if the session was terminated, so
//     a storm of notifications for a dead session cannot drive re-aggregation.
//  2. Reconstruct the registration-time request context (identity + forwarded
//     headers) atop the server-lifetime base context, so the cache key and
//     backend auth match a real request from this principal.
//  3. Invalidate the capability cache so the re-derivation below re-sweeps the
//     backend rather than reading the entry it just purged.
//  4. Re-derive and REPLACE the session's advertised set for kind: KindTools
//     resyncs tools, KindResources resyncs both resources and resource
//     templates (MCP 2025-11-25 has no separate wire method for template
//     changes), and KindPrompts resyncs prompts.
func (s *Server) runListChangedResync(
	baseCtx context.Context,
	sessionID string,
	session server.ClientSession,
	identity *auth.Identity,
	forwardedHeaders map[string]string,
	kind vmcpsession.ChangeKind,
) {
	// Liveness guard: if the session is gone (terminated/expired) there is
	// nothing to resync, and doing the work would waste a full backend sweep.
	if _, ok := s.vmcpSessionMgr.GetMultiSession(baseCtx, sessionID); !ok {
		slog.Debug("skipping list_changed resync for terminated session", "session_id", sessionID, "kind", kind)
		return
	}

	ctx := auth.WithIdentity(baseCtx, identity)
	if len(forwardedHeaders) > 0 {
		ctx = headerforward.WithForwardedHeaders(ctx, forwardedHeaders)
	}

	// Invalidate FIRST so the re-derivation below re-sweeps the backend instead
	// of re-reading the stale cached aggregation. The purge is global (all
	// identities, all kinds) — see InvalidateCapabilityCache — but coalescing
	// bounds it to at most once per resync burst per kind.
	s.core.InvalidateCapabilityCache()

	var err error
	switch kind {
	case vmcpsession.KindTools:
		err = s.resyncSessionTools(ctx, session, sessionID, identity)
	case vmcpsession.KindResources:
		err = s.resyncSessionResources(ctx, session, sessionID, identity)
	case vmcpsession.KindPrompts:
		err = s.resyncSessionPrompts(ctx, session, sessionID, identity)
	default:
		slog.Debug("skipping resync for unknown list_changed kind", "session_id", sessionID, "kind", kind)
		return
	}
	if err != nil {
		slog.Warn("failed to resync session after backend list_changed",
			"session_id", sessionID, "kind", kind, "error", err)
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

// resyncSessionResources re-derives sessionID's advertised resources AND
// resource templates (via coreSessionResources/coreSessionResourceTemplates —
// the core, cache now cold) and REPLACES the SDK session's resource and
// resource-template stores with them, mirroring resyncSessionTools. Both are
// re-derived together because MCP 2025-11-25 has no separate wire method for
// resource-template changes: notifications/resources/list_changed covers both.
//
// ctx must already carry the resyncing principal's identity and forwarded
// headers (runListChangedResync builds it) so the coreSession* helpers ->
// core.ListResources/ListResourceTemplates enumerate backends with the
// correct credentials and cache key.
//
// Add-only removal limitation: unlike resyncSessionTools, replacing the
// overlay here propagates ADDITIONS but not REMOVALS. toolhive-core's
// mcpcompat per-session sync for resources/resource templates
// (syncSessionResources/syncSessionResourceTemplates) is add-only — unlike
// syncSessionTools, which also calls RemoveTools — so a backend-removed
// resource/template stays registered on the session's go-sdk server (listed
// until re-initialize) even though this overlay no longer contains it. The
// overlay itself IS replaced (not merged) here, so when toolhive-core gains
// removal reconciliation (tracked follow-up: stacklok/toolhive-core#184),
// removals start propagating with no change needed in this function.
// Advertising listChanged:true stays honest — notifications ARE emitted on
// change (except that a pure removal which EMPTIES the overlay issues no Add*
// and therefore triggers no downstream list_changed, since go-sdk notifies
// only via Add*/RemoveTools); listChanged promises notification, not list
// minimization — but until that follow-up lands, a refetch after a pure
// removal returns a stale superset.
func (s *Server) resyncSessionResources(
	ctx context.Context, session server.ClientSession, sessionID string, identity *auth.Identity,
) error {
	resources, err := s.coreSessionResources(ctx, sessionID, identity)
	if err != nil {
		return fmt.Errorf("resync session resources: core ListResources for session %s: %w", sessionID, err)
	}
	sessionWithResources, ok := session.(server.SessionWithResources)
	if !ok {
		return fmt.Errorf("resync session resources: session %s does not support per-session resources", sessionID)
	}
	resourceMap := make(map[string]server.ServerResource, len(resources))
	for _, res := range resources {
		resourceMap[res.Resource.URI] = res
	}
	sessionWithResources.SetSessionResources(resourceMap)

	templates, err := s.coreSessionResourceTemplates(ctx, sessionID, identity)
	if err != nil {
		return fmt.Errorf("resync session resource templates: core ListResourceTemplates for session %s: %w", sessionID, err)
	}
	sessionWithTemplates, ok := session.(server.SessionWithResourceTemplates)
	if !ok {
		return fmt.Errorf(
			"resync session resource templates: session %s does not support per-session resource templates", sessionID)
	}
	templateMap := make(map[string]server.ServerResourceTemplate, len(templates))
	for _, tmpl := range templates {
		templateMap[tmpl.Template.URITemplate] = tmpl
	}
	sessionWithTemplates.SetSessionResourceTemplates(templateMap)

	slog.Debug("resynced session resources after backend list_changed",
		"session_id", sessionID, "resource_count", len(resources), "resource_template_count", len(templates))
	return nil
}

// resyncSessionPrompts re-derives sessionID's advertised prompt set (via
// coreSessionPrompts — the core, cache now cold) and REPLACES the SDK
// session's prompt store with it, mirroring resyncSessionTools.
//
// ctx must already carry the resyncing principal's identity and forwarded
// headers (runListChangedResync builds it) so coreSessionPrompts ->
// core.ListPrompts enumerates backends with the correct credentials and cache
// key.
//
// Add-only removal limitation: as with resyncSessionResources, replacing the
// overlay here propagates ADDITIONS but not REMOVALS. toolhive-core's
// mcpcompat per-session sync for prompts (syncSessionPrompts) is add-only, so
// a backend-removed prompt stays registered on the session's go-sdk server
// (listed until re-initialize) even though this overlay no longer contains
// it. The overlay itself IS replaced (not merged) here, so when toolhive-core
// gains removal reconciliation (tracked follow-up: stacklok/toolhive-core#184),
// removals start propagating with no change needed in this function.
// Advertising listChanged:true stays honest — notifications ARE emitted on
// change (except that a pure removal which EMPTIES the overlay issues no Add*
// and therefore triggers no downstream list_changed, since go-sdk notifies
// only via Add*/RemoveTools); listChanged promises notification, not list
// minimization — but until that follow-up lands, a refetch after a pure
// removal returns a stale superset.
func (s *Server) resyncSessionPrompts(
	ctx context.Context, session server.ClientSession, sessionID string, identity *auth.Identity,
) error {
	prompts, err := s.coreSessionPrompts(ctx, sessionID, identity)
	if err != nil {
		return fmt.Errorf("resync session prompts: core ListPrompts for session %s: %w", sessionID, err)
	}

	sessionWithPrompts, ok := session.(server.SessionWithPrompts)
	if !ok {
		return fmt.Errorf("resync session prompts: session %s does not support per-session prompts", sessionID)
	}

	promptMap := make(map[string]server.ServerPrompt, len(prompts))
	for _, p := range prompts {
		promptMap[p.Prompt.Name] = p
	}
	sessionWithPrompts.SetSessionPrompts(promptMap)

	slog.Debug("resynced session prompts after backend list_changed",
		"session_id", sessionID, "prompt_count", len(prompts))
	return nil
}
