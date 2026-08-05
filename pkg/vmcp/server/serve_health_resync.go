// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"log/slog"
	"sync"

	"github.com/stacklok/toolhive-core/mcpcompat/server"
)

// This file holds the backend-health-driven tools resync added by #5786 (PR1,
// passthrough mode). The backend-notification path (#5748, serve_list_changed.go)
// only reacts when a connected backend itself emits notifications/tools/
// list_changed; a backend that flips unhealthy⇄healthy, or is added to /
// removed from the group, emits nothing — so already-connected sessions kept
// serving the capability set snapshotted at registration until they
// reconnected. Serve subscribes to the core-owned health monitor's OnChange
// callback (debounced in the monitor) and fans the change out to every live
// session's tools resync worker — the same per-session coalescing worker the
// backend-notification path uses, so identity/header capture, the liveness
// guard, cache invalidation, replace semantics, and the SDK's automatic
// notifications/tools/list_changed emission are all shared.
//
// Scope (#5786 PR1): passthrough mode only. When the optimizer is enabled the
// advertised set is the find_tool/call_tool meta-tools, which do not change on
// a health flip; rebuilding the optimizer's backing index for live sessions is
// deferred to the optimizer-mode follow-up (PR2), so the fan-out is a no-op.
// Tools only: resources/resource-templates/prompts re-derivation on health
// change is likewise out of scope here.

// healthResyncRegistry tracks the per-session tools resync workers eligible
// for backend-health-driven fan-out. The zero value is usable.
//
// Lifecycle: a session is added after registration succeeds
// (handleSessionRegistrationImpl) and removed eagerly on every termination
// path the server observes — registration failure, binding-failure
// termination, and SDK-initiated termination (HTTP DELETE), the last via
// pruneOnTerminateSessionIDManager. Sessions that end without any Terminate
// call (TTL expiry) are pruned lazily by runListChangedResync when a
// triggered resync finds them gone. Between fan-out events the registry can
// therefore still hold entries for expired sessions; each such entry retains
// the worker closure (the SDK ClientSession and the registration-time
// identity + forwarded headers), is skipped harmlessly by the worker's
// liveness guard, and is pruned on the next trigger.
type healthResyncRegistry struct {
	mu      sync.Mutex
	workers map[string]*listChangedResyncWorker
}

// pruneOnTerminateSessionIDManager wraps the vMCP session manager in its role
// as the SDK's SessionIdManager so that an SDK-initiated termination (the
// client's HTTP DELETE, which reaches Terminate without passing through any
// other server code) eagerly deregisters the session's health-resync worker.
// Deregistration happens only when the underlying Terminate actually
// terminated the session; a disallowed or failed termination leaves the
// (still live) session registered.
type pruneOnTerminateSessionIDManager struct {
	server.SessionIdManager
	registry *healthResyncRegistry
}

func (m *pruneOnTerminateSessionIDManager) Terminate(sessionID string) (bool, error) {
	isNotAllowed, err := m.SessionIdManager.Terminate(sessionID)
	if !isNotAllowed && err == nil {
		m.registry.remove(sessionID)
	}
	return isNotAllowed, err
}

// add registers sessionID's tools resync worker for health-driven fan-out,
// replacing any previous registration for the same ID.
func (r *healthResyncRegistry) add(sessionID string, w *listChangedResyncWorker) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.workers == nil {
		r.workers = make(map[string]*listChangedResyncWorker)
	}
	r.workers[sessionID] = w
}

// remove deregisters sessionID. A no-op for unknown IDs.
func (r *healthResyncRegistry) remove(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.workers, sessionID)
}

// snapshot returns the currently registered workers. The copy lets callers
// trigger workers without holding the registry lock (trigger may start a
// goroutine that re-enters remove via the liveness prune).
func (r *healthResyncRegistry) snapshot() []*listChangedResyncWorker {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*listChangedResyncWorker, 0, len(r.workers))
	for _, w := range r.workers {
		out = append(out, w)
	}
	return out
}

// resyncSessionsOnBackendHealthChange is the Monitor.OnChange listener Serve
// registers: it triggers a tools resync for every registered session. The
// monitor already debounces delivery and each per-session worker coalesces
// concurrent triggers, so a burst of health transitions costs each session at
// most one in-flight re-derivation (plus one queued follow-up).
//
// generation is the monitor's change counter; it is logged for correlation
// only — the resync always re-derives from the current health view, so a
// later generation subsumes an earlier one.
func (s *Server) resyncSessionsOnBackendHealthChange(generation uint64) {
	// #5786 PR1 is passthrough-only: in optimizer mode the advertised
	// meta-tools are unchanged by a health flip and rebuilding the per-session
	// optimizer index is deferred to the optimizer-mode follow-up.
	if s.optimizerFactory != nil {
		slog.Debug("skipping session resync on backend health change (optimizer mode)",
			"generation", generation)
		return
	}

	workers := s.healthResync.snapshot()
	slog.Debug("backend health change: triggering tools resync for live sessions",
		"generation", generation, "sessions", len(workers))
	for _, w := range workers {
		w.trigger()
	}
}
