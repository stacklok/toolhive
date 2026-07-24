// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp

import "sync"

// LateBoundListChangedNotifier is a BackendListChangedNotifier whose backing
// target is set once, after the real coordinator exists.
//
// The composition root (pkg/vmcp/cli) builds the session factory — which needs a
// non-nil BackendListChangedNotifier to hand to the backend connector — before
// server.Serve constructs the coordinator that will actually receive the
// notifications. Bind resolves that construction-order inversion: the factory is
// given this holder up front, and the composition root calls Bind once the
// server's coordinator exists, before any session starts sending real traffic.
// This mirrors lateBoundElicitationRequester in pkg/vmcp/server/elicitation_latebound.go.
//
// While unbound, NotifyBackendListChanged is a silent no-op: a notification that
// arrives before Bind (impossible in practice, since no backend connection is
// opened before the composition root finishes wiring) is simply dropped rather
// than panicking or blocking.
//
// Safe for concurrent use: Bind happens once during construction (before serving
// begins), NotifyBackendListChanged reads under the same lock.
type LateBoundListChangedNotifier struct {
	mu     sync.RWMutex
	target BackendListChangedNotifier
}

var _ BackendListChangedNotifier = (*LateBoundListChangedNotifier)(nil)

// NewLateBoundListChangedNotifier returns an unbound notifier. Bind must be
// called before any backend connection that carries a reference to it starts
// receiving list_changed notifications, or they are silently dropped.
func NewLateBoundListChangedNotifier() *LateBoundListChangedNotifier {
	return &LateBoundListChangedNotifier{}
}

// Bind sets the backing notifier. The composition root calls it exactly once,
// after the server's list_changed coordinator is constructed and before serving
// begins.
func (l *LateBoundListChangedNotifier) Bind(target BackendListChangedNotifier) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.target = target
}

// NotifyBackendListChanged forwards to the bound target, or no-ops when called
// before Bind.
func (l *LateBoundListChangedNotifier) NotifyBackendListChanged(backendID string, kind ListChangedKind) {
	l.mu.RLock()
	target := l.target
	l.mu.RUnlock()
	if target == nil {
		return
	}
	target.NotifyBackendListChanged(backendID, kind)
}
