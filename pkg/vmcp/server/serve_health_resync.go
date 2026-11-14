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
// (handleSessionRegistrationImpl; passthrough mode with health monitoring
// enabled only — optimizer-mode sessions are never registered because the
// fan-out is a no-op for them in PR1, and with health monitoring disabled
// there is no OnChange subscriber, so nothing would ever trigger the fan-out
// or run the lazy prune below) and removed eagerly on every termination
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
// registers: it purges the shared capability cache once, then triggers a tools
// resync for every registered session. The monitor already debounces delivery
// and each per-session worker coalesces concurrent triggers, so a burst of
// health transitions costs each session at most one in-flight re-derivation
// (plus one queued follow-up). That is a PER-SESSION bound, not a bound on
// total work: one delivery still fans out to up to len(workers) concurrent
// re-derivations, each a full backend sweep on a cache miss. Sessions sharing
// an identity and forwarded-header set can hit the entry an earlier sibling's
// sweep populated (the per-run purge is skipped on this path — see
// listChangedResyncWorker.trigger), but there is no singleflight, so
// same-key sweeps that start together may still each hit the backends.
//
// generation is the monitor's change counter; it is logged for correlation
// only — the resync always re-derives from the current health view, so a
// later generation subsumes an earlier one.
func (s *Server) resyncSessionsOnBackendHealthChange(generation uint64) {
	// #5786 PR1 is passthrough-only: in optimizer mode the advertised
	// meta-tools are unchanged by a health flip and rebuilding the per-session
	// optimizer index is deferred to the optimizer-mode follow-up. Optimizer-
	// mode sessions are never registered (handleSessionRegistrationImpl skips
	// the add), so this gate is defense in depth keeping the no-op explicit.
	if s.optimizerFactory != nil {
		slog.Debug("skipping session resync on backend health change (optimizer mode)",
			"generation", generation)
		return
	}

	// Purge the shared capability cache ONCE per delivery, before the fan-out,
	// instead of once per session run. For the plain health flip this is
	// belt-and-braces — the cache key hashes the health-filtered backend-ID
	// set, so the flip changes the key by itself — but it also evicts entries
	// cached under a key that is about to be current again: a backend that
	// flipped down and back up within one debounce window (or was removed and
	// re-added) yields a delivery whose filtered set equals a previously
	// cached one, and the backend may have restarted with different tools in
	// between.
	s.core.InvalidateCapabilityCache()

	workers := s.healthResync.snapshot()
	slog.Debug("backend health change: triggering tools resync for live sessions",
		"generation", generation, "sessions", len(workers))
	for _, w := range workers {
		w.trigger(false)
	}
}
