// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"sync"

	"golang.org/x/exp/jsonrpc2"
)

// progressRoute is the per-request delivery route for notifications/progress
// messages that echo back a proxy-minted ptGlobal token (see
// rewriteMetaProgressToken and handleSingleRequestSSE's setupProgressRouting).
type progressRoute struct {
	// deliver is the in-flight POST-SSE handler's per-request delivery
	// channel. It is buffered (see progressDeliverBufferSize) and owned by
	// that handler; routeProgress only ever sends to it, never closes it.
	deliver chan jsonrpc2.Message
	// originalToken is the client's own progressToken value (string or
	// number), restored into the notification before delivery so the client
	// sees the token it originally asked for, never ptGlobal.
	originalToken any
}

// sessionRouter owns three INDEPENDENT registries used to route server->client
// messages that arrive on the shared backend's response channel to the
// correct session(s) without leaking to any other session:
//
//   - progress tokens: request-scoped, correlate a notifications/progress
//     reply to the single in-flight POST-SSE request that asked for it.
//   - subscriptions: session-scoped, correlate a resources/updated
//     notification to the set of sessions currently subscribed to its uri,
//     and ref-count resources/subscribe|unsubscribe so the shared backend only
//     sees one upstream (un)subscribe per uri.
//   - log levels: session-scoped, track each session's requested logging
//     verbosity so the shared backend's single process-wide level can be
//     reconciled to the maximum (most verbose) requested by any session.
//
// Each registry has its own mutex (see the "one synchronization primitive per
// data structure" style rule): the three are never locked together, and no
// method here locks more than one of ptMu/subMu/logMu at a time.
type sessionRouter struct {
	ptMu   sync.Mutex
	ptToks map[string]progressRoute // key: ptGlobal (proxy-minted, unique per request)

	subMu sync.Mutex
	subs  map[string]map[string]struct{} // uri -> set(sessID)

	logMu  sync.Mutex
	logLvl map[string]int // sessID -> requested verbosity rank (higher = more verbose)
}

// newSessionRouter creates an empty sessionRouter.
func newSessionRouter() *sessionRouter {
	return &sessionRouter{
		ptToks: make(map[string]progressRoute),
		subs:   make(map[string]map[string]struct{}),
		logLvl: make(map[string]int),
	}
}

// recordProgressToken registers route under ptGlobal, so a later
// notifications/progress echoing ptGlobal is delivered via route.deliver (see
// lookupProgressToken). The caller (handleSingleRequestSSE) MUST call
// dropProgressToken(ptGlobal) once the request completes -- progress tokens
// are request-scoped, not session-scoped, so they are never reaped by reap.
func (r *sessionRouter) recordProgressToken(ptGlobal string, route progressRoute) {
	r.ptMu.Lock()
	defer r.ptMu.Unlock()
	r.ptToks[ptGlobal] = route
}

// lookupProgressToken returns the route registered for ptGlobal, if any.
func (r *sessionRouter) lookupProgressToken(ptGlobal string) (progressRoute, bool) {
	r.ptMu.Lock()
	defer r.ptMu.Unlock()
	route, ok := r.ptToks[ptGlobal]
	return route, ok
}

// dropProgressToken removes the route registered for ptGlobal, if any. It is
// always safe to call, including for a ptGlobal that was never recorded.
func (r *sessionRouter) dropProgressToken(ptGlobal string) {
	r.ptMu.Lock()
	defer r.ptMu.Unlock()
	delete(r.ptToks, ptGlobal)
}

// addSubscription records sess as a subscriber of uri and reports whether
// sess is the FIRST subscriber currently recorded for uri.
//
// The forward-vs-dedup decision is NOT driven by this return value: to record
// a subscription only after the upstream subscribe succeeds (see
// interceptSubscribe), the caller first peeks subscribersOf(uri) to decide
// whether an upstream call is needed, forwards, and only then calls
// addSubscription on success. Once any session is subscribed to uri the shared
// backend is already sending updates for it, so a later subscribe for the same
// uri is served locally without another upstream call. firstForURI is retained
// for tests/observability.
func (r *sessionRouter) addSubscription(uri, sess string) (firstForURI bool) {
	r.subMu.Lock()
	defer r.subMu.Unlock()

	set, ok := r.subs[uri]
	if !ok {
		set = make(map[string]struct{})
		r.subs[uri] = set
	}
	firstForURI = len(set) == 0
	set[sess] = struct{}{}
	return firstForURI
}

// removeSubscription removes sess as a subscriber of uri and reports whether
// sess was the LAST subscriber recorded for uri. The caller uses
// lastForURI to decide whether to forward resources/unsubscribe upstream: the
// backend should only stop sending updates for uri once no session wants them
// anymore.
func (r *sessionRouter) removeSubscription(uri, sess string) (lastForURI bool) {
	r.subMu.Lock()
	defer r.subMu.Unlock()

	set, ok := r.subs[uri]
	if !ok {
		return false
	}
	delete(set, sess)
	lastForURI = len(set) == 0
	if lastForURI {
		delete(r.subs, uri)
	}
	return lastForURI
}

// subscribersOf returns the session IDs currently subscribed to uri (nil if
// none). The caller (dispatcher.go's routeResourceUpdated) uses this as the
// routeKeys for serverStreamRegistry.deliverToMany, so a resources/updated
// notification reaches only sessions that actually subscribed to that uri.
func (r *sessionRouter) subscribersOf(uri string) []string {
	r.subMu.Lock()
	defer r.subMu.Unlock()

	set, ok := r.subs[uri]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(set))
	for sess := range set {
		out = append(out, sess)
	}
	return out
}

// setLogLevel records sess's requested verbosity rank and reports whether the
// MAXIMUM rank across all currently-tracked sessions changed as a result,
// along with the new maximum. The caller (streamable_proxy.go's
// logging/setLevel handling) uses maxChanged to decide whether to reconcile
// the shared backend's single process-wide log level upstream: forwarding
// each session's own request directly would let the last writer silently
// under-serve an earlier, more-verbose requester, since every session shares
// one backend log level.
func (r *sessionRouter) setLogLevel(sess string, rank int) (maxChanged bool, newMax int) {
	r.logMu.Lock()
	defer r.logMu.Unlock()

	before := r.maxLogLevelLocked()
	r.logLvl[sess] = rank
	after := r.maxLogLevelLocked()
	return after != before, after
}

// dropLogLevel removes sess's tracked verbosity rank, if any. It is always
// safe to call, including for a sess that was never recorded.
func (r *sessionRouter) dropLogLevel(sess string) {
	r.logMu.Lock()
	defer r.logMu.Unlock()
	delete(r.logLvl, sess)
}

// maxLogLevelLocked returns the maximum rank across all tracked sessions, or
// -1 if none are tracked. Callers MUST hold logMu.
func (r *sessionRouter) maxLogLevelLocked() int {
	maxRank := -1
	for _, rank := range r.logLvl {
		if rank > maxRank {
			maxRank = rank
		}
	}
	return maxRank
}

// purgeSession removes sess from every subscription set and from the
// log-level table. Progress tokens are request-scoped (see
// recordProgressToken's doc comment), not session-scoped, so they are
// untouched here -- an in-flight request's progress token outlives this call
// by design, and is dropped by its own handler's deferred dropProgressToken.
//
// It reports whether removing sess changed the maximum log-level rank, so the
// caller (handleDelete) can decide whether the shared backend's level should
// be reconciled down now that sess is gone.
func (r *sessionRouter) purgeSession(sess string) (maxChanged bool, newMax int) {
	r.subMu.Lock()
	for uri, set := range r.subs {
		if _, ok := set[sess]; ok {
			delete(set, sess)
			if len(set) == 0 {
				delete(r.subs, uri)
			}
		}
	}
	r.subMu.Unlock()

	r.logMu.Lock()
	defer r.logMu.Unlock()
	if _, ok := r.logLvl[sess]; !ok {
		return false, r.maxLogLevelLocked()
	}
	before := r.maxLogLevelLocked()
	delete(r.logLvl, sess)
	after := r.maxLogLevelLocked()
	return after != before, after
}

// reap removes subs/logLvl entries for every session isActive reports as no
// longer active, bounding the router's memory growth for sessions whose owning
// client disappeared without an explicit DELETE (see purgeSession for that
// path). Progress tokens are request-scoped and are never touched by reap
// (see recordProgressToken's doc comment).
//
// isActive is called OUTSIDE of ptMu/subMu/logMu: it may perform I/O (e.g. a
// Redis-backed session.Manager), so resolving liveness for every candidate
// session happens in a first pass with no lock held, and stale entries are
// then removed in a second pass that only ever does in-memory map work while
// holding a lock. This keeps reap from blocking concurrent
// addSubscription/removeSubscription/setLogLevel calls for the (possibly
// slow) duration of every liveness check.
//
// TOCTOU, the other direction: because isActive is snapshotted in the first
// pass and never re-checked once the second pass's locks are held, a session
// that (a) expires, (b) is re-created with the SAME session ID, and (c)
// re-subscribes or re-sets a log level -- all within the window between this
// tick's first and second pass -- could have its brand-new entry wrongly
// swept away by this same tick, since isActive's stale "false" result for the
// old incarnation is applied to the new one's state too. This is practically
// unreachable: session IDs are server-minted UUIDs (see resolveSessionForRequest),
// never reused by any real client in practice, so the "same ID reappears
// mid-tick" precondition does not occur outside a contrived test. Re-checking
// liveness a second time inside the phase-3 lock would close this gap, but is
// deliberately avoided: isActive's underlying session-store read (e.g. Redis
// GETEX) is I/O, and doing it while holding subMu/logMu would reintroduce the
// exact blocking-under-lock problem the two-pass design exists to avoid.
func (r *sessionRouter) reap(isActive func(sess string) bool) {
	seen := make(map[string]struct{})

	r.subMu.Lock()
	for _, set := range r.subs {
		for sess := range set {
			seen[sess] = struct{}{}
		}
	}
	r.subMu.Unlock()

	r.logMu.Lock()
	for sess := range r.logLvl {
		seen[sess] = struct{}{}
	}
	r.logMu.Unlock()

	stale := make(map[string]struct{}, len(seen))
	for sess := range seen {
		if !isActive(sess) {
			stale[sess] = struct{}{}
		}
	}
	if len(stale) == 0 {
		return
	}

	r.subMu.Lock()
	for uri, set := range r.subs {
		for sess := range stale {
			delete(set, sess)
		}
		if len(set) == 0 {
			delete(r.subs, uri)
		}
	}
	r.subMu.Unlock()

	r.logMu.Lock()
	for sess := range stale {
		delete(r.logLvl, sess)
	}
	r.logMu.Unlock()
}
