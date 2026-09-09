// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
)

// This file holds the optimizer-mode half of #5786 (PR2). PR1 wired the health
// monitor's OnChange into the passthrough tools resync but made the fan-out a
// deliberate no-op in optimizer mode: the advertised set there is just the
// find_tool/call_tool meta-tools, whose NAMES do not change when a backend's
// health flips, so replacing the session's tool store would emit a downstream
// notifications/tools/list_changed that told the client nothing (go-sdk's
// AddTool notifies unconditionally — "Assume there was a change, since add
// replaces existing tools"). What DOES need to change is the meta-tools'
// backing index: find_tool scopes its search to the tool names the optimizer
// instance was built with (toolOptimizer.toolNames, passed to
// ToolStore.Search as an allow-list) and call_tool dispatches through that
// same instance's handler map, so an instance built while a backend was
// unhealthy keeps hiding that backend's tools after it recovers — and keeps
// offering a failed backend's tools until the session reconnects.
//
// Rather than rebuild the session's advertised tool store, PR2 makes the
// per-session optimizer instance replaceable behind a stable handle: the
// meta-tool handlers close over a sessionOptimizer, whose inner instance is
// swapped atomically when the health-filtered core tool set changes. The
// session's tool store is never rewritten, so no spurious client notification
// is emitted, and the next find_tool/call_tool observes the new scope.

// sessionOptimizer is a stable optimizer.Optimizer handle for one session whose
// backing instance can be replaced atomically.
//
// The meta-tool handlers built in optimizerSessionTools close over this handle
// rather than over a concrete optimizer, which is what lets a re-index avoid
// touching the SDK session's tool store (and therefore avoid emitting a
// downstream notifications/tools/list_changed for an advertised set that has
// not changed).
//
// Concurrency: swap publishes a whole new instance; it never mutates one. A
// FindTool or CallTool already in flight keeps using the instance it loaded, so
// it sees a consistent {tools, toolNames, tokenCounts, baselineTokens} snapshot
// — preserving the immutable-after-construction invariant toolOptimizer's own
// field docs rely on. A call that loads the handle after a swap sees the new
// index. There is deliberately no ordering guarantee between an in-flight call
// and a concurrent swap: a health flip is not a barrier, and the caller's next
// find_tool re-reads the current scope.
type sessionOptimizer struct {
	current atomic.Pointer[optimizerInstance]
}

// optimizerInstance boxes the interface value so it can be stored in an
// atomic.Pointer (which needs a concrete pointee).
type optimizerInstance struct {
	opt optimizer.Optimizer
}

// newSessionOptimizer returns a handle serving opt until the first swap.
func newSessionOptimizer(opt optimizer.Optimizer) *sessionOptimizer {
	h := &sessionOptimizer{}
	h.swap(opt)
	return h
}

// swap replaces the instance every subsequent FindTool/CallTool resolves against.
func (h *sessionOptimizer) swap(opt optimizer.Optimizer) {
	h.current.Store(&optimizerInstance{opt: opt})
}

// FindTool delegates to the current instance, scoping the search to the tool set
// that instance was built over.
func (h *sessionOptimizer) FindTool(
	ctx context.Context, input optimizer.FindToolInput,
) (*optimizer.FindToolOutput, error) {
	return h.current.Load().opt.FindTool(ctx, input)
}

// CallTool delegates to the current instance, so a tool dropped by the last
// re-index resolves as "tool not found" rather than dispatching to a backend the
// catalog no longer advertises.
func (h *sessionOptimizer) CallTool(
	ctx context.Context, input optimizer.CallToolInput,
) (*mcp.CallToolResult, error) {
	return h.current.Load().opt.CallTool(ctx, input)
}

var _ optimizer.Optimizer = (*sessionOptimizer)(nil)

// installSessionOptimizer publishes opt as sessionID's current optimizer and
// returns the handle the meta-tool handlers should close over.
//
// When the session already has a handle (a re-derivation of an existing
// session: cross-pod re-injection, or a list_changed resync falling back to the
// rebuild-and-replace path), opt is swapped into that SAME handle, so handlers
// installed earlier keep resolving against the newest index instead of pinning
// the instance they were built with.
//
// With health monitoring disabled the handle is NOT retained: nothing would
// ever trigger a re-index (no OnChange subscriber — see Serve), so keeping
// per-session state would be a leak for no benefit, exactly as with the resync
// worker registration. The returned handle still works; it simply never gets
// swapped, and the backend-notification path falls back to rebuild-and-replace.
func (s *Server) installSessionOptimizer(sessionID string, opt optimizer.Optimizer) optimizer.Optimizer {
	if s.backendHealth() == nil {
		return opt
	}
	return s.healthResync.installOptimizer(sessionID, opt)
}

// reindexSessionOptimizer rebuilds sessionID's optimizer index over the CURRENT
// health-filtered core tool set and swaps it in, leaving the session's
// advertised meta-tools (and so the client's view of tools/list) untouched.
//
// It reports handled=false when the session has no registered handle — health
// monitoring disabled, or a session that registered before this pod saw it —
// so the caller can fall back to the rebuild-and-replace path rather than
// silently skipping the re-derivation.
//
// ctx must already carry the resyncing principal's identity and forwarded
// headers (runListChangedResync builds it), so coreSessionTools enumerates
// backends with the correct credentials and cache key — the same requirement
// the passthrough resync has.
func (s *Server) reindexSessionOptimizer(
	ctx context.Context, sessionID string, identity *auth.Identity,
) (handled bool, err error) {
	holder := s.healthResync.optimizerFor(sessionID)
	if holder == nil {
		return false, nil
	}

	coreTools, err := s.coreSessionTools(ctx, sessionID, identity)
	if err != nil {
		return true, fmt.Errorf("reindex session optimizer: core ListTools for session %s: %w", sessionID, err)
	}

	opt, err := s.optimizerFactory(ctx, coreTools)
	if err != nil {
		return true, fmt.Errorf("reindex session optimizer: build optimizer for session %s: %w", sessionID, err)
	}
	holder.swap(opt)

	slog.Debug("reindexed session optimizer after catalog change",
		"session_id", sessionID, "indexed_tool_count", len(coreTools))
	return true, nil
}
