// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/headerforward"
)

// listChangedDebounce coalesces a burst of NotifyBackendListChanged calls
// (multiple sessions' listeners plus the mid-call path firing for the same
// underlying backend mutation) into a single invalidate+re-sweep cycle. It is
// the default for the per-coordinator debounce; tests shorten it via the
// coordinator's atomic debounce field to avoid real quarter-second sleeps.
const listChangedDebounce = 250 * time.Millisecond

// listChangedSweepTimeout bounds the re-derivation of a single session's
// capability set (core re-list + optimizer rebuild) during a re-sweep. It is
// generous relative to the per-request deadlines elsewhere in this package
// because a re-sweep runs off the request path, in the background, on the
// coordinator's own goroutine.
const listChangedSweepTimeout = 30 * time.Second

// kindSet is a bitmask over vmcp.ListChangedKind, letting the coordinator
// accumulate "which capability lists changed" for a backend (or the union
// across backends affecting one session) without repeated slice scans.
type kindSet uint8

const (
	kindTools kindSet = 1 << iota
	kindResources
	kindPrompts
)

// kindBit maps a vmcp.ListChangedKind to its bit. An unrecognized kind (should
// be unreachable — vmcp.ListChangedKindForMethod is the only producer) maps to
// 0, which every kindSet.has check reports as absent.
func kindBit(k vmcp.ListChangedKind) kindSet {
	switch k {
	case vmcp.ListChangedTools:
		return kindTools
	case vmcp.ListChangedResources:
		return kindResources
	case vmcp.ListChangedPrompts:
		return kindPrompts
	default:
		return 0
	}
}

func (s kindSet) has(bit kindSet) bool { return s&bit != 0 }

// trackedSession is the coordinator's per-session record, captured once at
// session registration (handleSessionRegistrationImpl) and consulted on every
// re-sweep.
type trackedSession struct {
	// sess is the live SDK session. SetSessionTools/SetSessionResources/
	// SetSessionPrompts on it reconcile the per-session overlay onto the live
	// go-sdk server and (for tools) auto-notify the downstream client.
	sess server.ClientSession

	// identity is the caller's identity at registration time. It is immutable
	// (see pkg/auth/identity.go) so retaining it is safe, but it can go stale
	// relative to a refreshed upstream token (#5323) — the re-swept core calls
	// below are therefore best-effort with respect to identity freshness, same
	// as any other background (non-request-path) use of a captured identity.
	identity *auth.Identity

	// fwdHeaders is the headerforward snapshot captured at registration
	// (headerforward.ForwardedHeadersFromContext on the registration request).
	// It must be replayed on the re-sweep context so the cache key the
	// re-derivation computes (aggregator.cacheKey) matches the one the original
	// registration populated — otherwise the re-sweep would miss the just-
	// invalidated cache entry and silently serve a second, freshly-swept but
	// differently-keyed entry.
	fwdHeaders map[string]string

	// backendIDs is this session's connected backend set (from the session's
	// MultiSession metadata at registration). A backend's list_changed only
	// affects sessions whose backendIDs contains that backend.
	backendIDs map[string]struct{}
}

// listChangedCoordinator consumes backend list_changed notifications (from both
// the per-call and persistent-session paths, see vmcp.BackendListChangedNotifier)
// and propagates them to every affected, still-live session by invalidating the
// per-identity capability cache and re-deriving + re-applying that session's
// capability set.
//
// Concurrency model: dirty and tracked are the coordinator's only mutable state
// besides invalidator, all guarded by mu. NotifyBackendListChanged (called
// directly from a backend client's receive loop) only ever does a map write
// under mu plus a non-blocking channel send — it never blocks on I/O or takes
// any lock this type does not already own, so it cannot stall the receive loop
// it runs on or the drain-ping barrier (pkg/vmcp/client's
// drainServerToClientNotifications). All the heavy lifting — cache invalidation,
// snapshotting tracked, and the per-session core re-derivation — happens on the
// single worker goroutine started by Serve, well clear of any request path.
type listChangedCoordinator struct {
	srv *Server

	mu sync.Mutex
	// invalidator evicts per-identity capability cache entries scoped to one
	// backend. Nil when response caching is disabled (aggregator.
	// NewCachingAggregator returns the aggregator unwrapped for ttl<=0), in
	// which case invalidation is skipped — there is no cache to go stale.
	invalidator aggregator.CacheInvalidator
	// dirty accumulates backendID -> which capability kinds changed, since the
	// last sweep drained it.
	dirty map[string]kindSet
	// tracked holds the coordinator's own sessionID -> trackedSession registry.
	// This is deliberately NOT layered onto the session Manager or the
	// validating cache (pkg/cache/validating_cache.go) — neither is designed to
	// be enumerated (Range is intentionally absent, see their docs), and adding
	// that capability purely to support this coordinator would widen a stable
	// interface for one caller.
	//
	// Entries are removed on two paths:
	//   1. Graceful termination (client DELETE, or any internal Terminate): the
	//      Manager's SetOnTerminate hook calls untrack synchronously — see
	//      Serve's wiring and terminateOnBindingFailure.
	//   2. Sweep-time discovery: a sweep that finds Manager.Validate reports a
	//      tracked session terminated prunes it then (covers, e.g., a session
	//      that expired without a DELETE but is later revisited by a sweep).
	//
	// UNBOUNDED-GROWTH GAP: a session whose client vanishes WITHOUT a DELETE
	// (ungraceful transport drop) fires neither path unless a later sweep happens
	// to revisit it — and a sweep only revisits sessions whose backend set
	// intersects a dirty backend, so an idle session on an otherwise-quiet
	// backend is never revisited. There is no unregister hook to catch this:
	// mcpcompat's server.Hooks exposes only OnRegisterSession, and go-sdk's
	// session-disconnect path is internal (no consumer-settable close callback,
	// verified against toolhive-core@v0.0.32 / go-sdk@v1.6.1). So such an entry
	// (a live server.ClientSession, identity, and two small maps) leaks until the
	// process exits — the SAME class of leak mcpcompat itself documents for its
	// own session maps (issue #156 finding 5). Bounding this needs an upstream
	// close hook; tracked as a follow-up, not added to toolhive-core in this PR.
	tracked map[string]*trackedSession

	// wake is a buffered(1) signal from NotifyBackendListChanged to the worker.
	// Buffering at 1 means a storm of notifications collapses to a single
	// pending wake-up rather than queuing one per notification.
	wake chan struct{}
	// done is closed by stop to terminate the worker goroutine. Its close is
	// guarded by stopOnce so a second Server.Stop cannot panic on close-of-closed.
	done     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	// baseCtx is the root context for every background re-sweep; cancel cancels it
	// inside stop() so an in-flight sweep's core calls abort promptly instead of
	// blocking shutdown for up to the sweep timeout per affected session.
	baseCtx context.Context
	cancel  context.CancelFunc

	// debounceNanos is the coalescing window in nanoseconds, read atomically by
	// the worker on each wake. It defaults to listChangedDebounce; tests store a
	// far smaller value to avoid real quarter-second sleeps under -race.
	debounceNanos atomic.Int64
}

var _ vmcp.BackendListChangedNotifier = (*listChangedCoordinator)(nil)

// newListChangedCoordinator constructs a coordinator bound to srv. The caller
// (Serve) must call start() once before any session can register, and must
// arrange for stop to run during shutdown.
func newListChangedCoordinator(srv *Server) *listChangedCoordinator {
	baseCtx, cancel := context.WithCancel(context.Background())
	c := &listChangedCoordinator{
		srv:     srv,
		dirty:   make(map[string]kindSet),
		tracked: make(map[string]*trackedSession),
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
		baseCtx: baseCtx,
		cancel:  cancel,
	}
	c.debounceNanos.Store(int64(listChangedDebounce))
	return c
}

// setInvalidator installs the cache invalidator once the caching aggregator
// (built in server.New) is known. Called before the server begins accepting
// connections, so there is no concurrent sweep in progress the first time it
// runs; later reads of invalidator by the worker take the same mu.
func (c *listChangedCoordinator) setInvalidator(inv aggregator.CacheInvalidator) {
	c.mu.Lock()
	c.invalidator = inv
	c.mu.Unlock()
}

// NotifyBackendListChanged implements vmcp.BackendListChangedNotifier. It is
// invoked directly from a backend client's OnNotification handler (both the
// per-call and persistent-session paths), so it MUST NOT block: it does a
// single guarded map write plus a non-blocking wake signal, nothing else.
func (c *listChangedCoordinator) NotifyBackendListChanged(backendID string, kind vmcp.ListChangedKind) {
	bit := kindBit(kind)
	if bit == 0 {
		return
	}
	c.mu.Lock()
	c.dirty[backendID] |= bit
	c.mu.Unlock()

	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// track registers sessionID's coordinator entry. Called once, after a session's
// capabilities have been successfully injected at registration.
func (c *listChangedCoordinator) track(sessionID string, entry *trackedSession) {
	c.mu.Lock()
	c.tracked[sessionID] = entry
	c.mu.Unlock()
}

// untrack removes sessionID's coordinator entry. Called on session termination
// (including binding-failure termination) and when a sweep discovers the
// session manager reports it terminated.
func (c *listChangedCoordinator) untrack(sessionID string) {
	c.mu.Lock()
	delete(c.tracked, sessionID)
	c.mu.Unlock()
}

// start launches the single coordinator worker goroutine. Serve calls it
// exactly once, before the server begins accepting connections.
func (c *listChangedCoordinator) start() {
	c.wg.Add(1)
	go c.run()
}

// stop signals the worker to exit and waits for it, bounded by ctx. It is
// registered on Server.shutdownFuncs. It is idempotent (stopOnce guards the
// channel close and context cancel) so a second Server.Stop cannot panic.
// Cancelling baseCtx first aborts any in-flight re-sweep's core calls promptly,
// so shutdown does not block for up to the sweep timeout per affected session.
//
// It stops ONLY the coordinator goroutine and clears the tracked registry's
// worker; it does NOT close the per-session persistent backend connections /
// standalone list_changed streams — those MultiSessions are owned by the session
// Manager's cache and are released on eviction (see the lifecycle note in
// backend.NewHTTPConnector), not here. Eager close-on-Stop is a documented
// follow-up.
func (c *listChangedCoordinator) stop(ctx context.Context) error {
	c.stopOnce.Do(func() {
		c.cancel()
		close(c.done)
	})
	waited := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(waited)
	}()
	select {
	case <-waited:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// run is the coordinator's sole worker goroutine: wait for a wake-up (or
// shutdown), debounce briefly to coalesce a notification storm, then drain and
// process dirty backends until none remain (re-checking after each sweep, since
// a notification may have arrived mid-sweep). It aborts promptly on shutdown at
// every blocking or loop boundary.
func (c *listChangedCoordinator) run() {
	defer c.wg.Done()
	for {
		select {
		case <-c.wake:
		case <-c.done:
			return
		}

		timer := time.NewTimer(time.Duration(c.debounceNanos.Load()))
		select {
		case <-timer.C:
		case <-c.done:
			timer.Stop()
			return
		}

		for {
			// Abort promptly on shutdown rather than draining the whole dirty set.
			select {
			case <-c.done:
				return
			default:
			}
			dirty := c.drainDirty()
			if len(dirty) == 0 {
				break
			}
			c.sweep(dirty)
		}
	}
}

// drainDirty atomically swaps out the dirty map, returning the previous
// contents (or nil if empty) and leaving an empty map in place.
func (c *listChangedCoordinator) drainDirty() map[string]kindSet {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.dirty) == 0 {
		return nil
	}
	d := c.dirty
	c.dirty = make(map[string]kindSet)
	return d
}

// sweep invalidates the capability cache for every dirty backend, then
// re-derives and re-applies the capability set for every tracked session that
// backend set affects.
func (c *listChangedCoordinator) sweep(dirty map[string]kindSet) {
	c.mu.Lock()
	inv := c.invalidator
	sessions := make(map[string]*trackedSession, len(c.tracked))
	for id, e := range c.tracked {
		sessions[id] = e
	}
	c.mu.Unlock()

	if inv != nil {
		for backendID := range dirty {
			inv.InvalidateBackend(backendID)
		}
	}

	// Sequential, not parallel: the first affected session's re-sweep for a
	// given (identity, forwarded-headers, backend-set) cache key repopulates
	// the just-invalidated aggregator cache entry; any other tracked session
	// sharing that exact key then hits the warm cache instead of re-sweeping
	// the backends itself.
	for sessionID, entry := range sessions {
		// Bail between sessions on shutdown so stop() is not held up by the
		// remaining sessions' re-sweeps (baseCtx is already cancelled too).
		select {
		case <-c.done:
			return
		default:
		}
		kinds := affectedKinds(entry.backendIDs, dirty)
		if kinds == 0 {
			continue
		}
		c.resweepSession(sessionID, entry, kinds)
	}
}

// affectedKinds unions the dirty kindSets of every backend in backendIDs,
// returning 0 when none of dirty's backends belong to this session.
func affectedKinds(backendIDs map[string]struct{}, dirty map[string]kindSet) kindSet {
	var union kindSet
	for id := range backendIDs {
		if k, ok := dirty[id]; ok {
			union |= k
		}
	}
	return union
}

// resweepSession re-derives and re-applies sessionID's capability set for the
// dirty kinds, after confirming the session is still live.
func (c *listChangedCoordinator) resweepSession(sessionID string, entry *trackedSession, kinds kindSet) {
	// Manager.Validate is storage-only (no restore/reconnect side effects),
	// unlike GetMultiSession which can trigger a full backend reconnect on a
	// cache miss — exactly the kind of expensive, request-shaped work this
	// background sweep must not trigger incidentally. Validate is sufficient
	// here: we only need to know whether the session still exists.
	terminated, err := c.srv.vmcpSessionMgr.Validate(sessionID)
	if err != nil {
		slog.Debug("list_changed: skipping session re-sweep; validate failed",
			"session_id", sessionID, "error", err)
		return
	}
	if terminated {
		c.untrack(sessionID)
		return
	}

	// c.baseCtx, not context.Background(): this runs on the coordinator's own
	// goroutine, well after the (long-gone) registration request completed, but
	// it must still abort promptly when stop() cancels baseCtx (otherwise
	// shutdown blocks up to the sweep timeout per affected session). Carrying
	// identity + forwarded headers reproduces the exact cache key
	// (aggregator.cacheKey) the original registration populated, so the
	// invalidated entry is genuinely refreshed rather than missed.
	ctx := auth.WithIdentity(c.baseCtx, entry.identity)
	ctx = headerforward.WithForwardedHeaders(ctx, entry.fwdHeaders)
	ctx, cancel := context.WithTimeout(ctx, listChangedSweepTimeout)
	defer cancel()

	if kinds.has(kindTools) {
		c.resweepTools(ctx, sessionID, entry)
	}
	if kinds.has(kindResources) {
		c.resweepResources(ctx, sessionID, entry)
	}
	if kinds.has(kindPrompts) {
		c.resweepPrompts(ctx, sessionID, entry)
	}
}

// resweepTools re-derives sessionID's advertised tool set and REPLACES the
// session's tool overlay with it (setSessionToolsReplace, not the
// registration-time merge helper), so a tool the backend removed actually
// disappears — the merge helper can only ever add. On a re-derivation error
// the previous tool set is left untouched (never shrunk to empty on a
// transient failure); the next backend notification retries.
func (c *listChangedCoordinator) resweepTools(ctx context.Context, sessionID string, entry *trackedSession) {
	tools, err := c.srv.serveSessionTools(ctx, sessionID, entry.identity)
	if err != nil {
		slog.Warn("list_changed: failed to re-list tools for session; retaining previous set",
			"session_id", sessionID, "error", err)
		return
	}
	if err := setSessionToolsReplace(entry.sess, tools); err != nil {
		slog.Warn("list_changed: failed to apply re-swept tools",
			"session_id", sessionID, "error", err)
	}
}

// resweepResources re-derives sessionID's advertised resources and resource
// templates and applies them via the ADD-ONLY setSessionResourcesDirect /
// setSessionResourceTemplatesDirect helpers.
//
// Removal gap (documented, not fixed here): unlike tools, the underlying
// go-sdk-facing sync (mcpcompat's syncSessionResources / syncSessionResourceTemplates)
// only ever ADDS — it has no RemoveResources/RemoveResourceTemplates
// reconciliation the way syncSessionTools has RemoveTools. So a resource or
// resource template a backend REMOVES stays advertised on already-registered
// sessions until the session ends; only additions propagate. Closing this gap
// needs a toolhive-core change (making those two sync functions reconciling,
// like syncSessionTools) plus a version bump, which is out of scope for this
// PR — tracked as a toolhive-core follow-up (see the PR description).
func (c *listChangedCoordinator) resweepResources(ctx context.Context, sessionID string, entry *trackedSession) {
	resources, err := c.srv.coreSessionResources(ctx, sessionID, entry.identity)
	if err != nil {
		slog.Warn("list_changed: failed to re-list resources for session",
			"session_id", sessionID, "error", err)
	} else if len(resources) > 0 {
		if err := setSessionResourcesDirect(entry.sess, resources); err != nil {
			slog.Warn("list_changed: failed to apply re-swept resources",
				"session_id", sessionID, "error", err)
		}
	}

	templates, err := c.srv.coreSessionResourceTemplates(ctx, sessionID, entry.identity)
	if err != nil {
		slog.Warn("list_changed: failed to re-list resource templates for session",
			"session_id", sessionID, "error", err)
	} else if len(templates) > 0 {
		if err := setSessionResourceTemplatesDirect(entry.sess, templates); err != nil {
			slog.Warn("list_changed: failed to apply re-swept resource templates",
				"session_id", sessionID, "error", err)
		}
	}
}

// resweepPrompts re-derives sessionID's advertised prompts and applies them via
// the ADD-ONLY setSessionPromptsDirect helper. See resweepResources for the
// same add-only / toolhive-core-follow-up caveat, which applies identically to
// prompts (mcpcompat's syncSessionPrompts is also add-only).
func (c *listChangedCoordinator) resweepPrompts(ctx context.Context, sessionID string, entry *trackedSession) {
	prompts, err := c.srv.coreSessionPrompts(ctx, sessionID, entry.identity)
	if err != nil {
		slog.Warn("list_changed: failed to re-list prompts for session",
			"session_id", sessionID, "error", err)
		return
	}
	if len(prompts) > 0 {
		if err := setSessionPromptsDirect(entry.sess, prompts); err != nil {
			slog.Warn("list_changed: failed to apply re-swept prompts",
				"session_id", sessionID, "error", err)
		}
	}
}

// BackendListChangedNotifier returns the server's list_changed coordinator as
// the narrow interface backend clients/connectors depend on. It returns a true
// nil interface value (not a non-nil interface wrapping a nil *listChangedCoordinator)
// when the server has no coordinator, so callers' nil checks behave correctly.
func (s *Server) BackendListChangedNotifier() vmcp.BackendListChangedNotifier {
	if s.listChanged == nil {
		return nil
	}
	return s.listChanged
}
