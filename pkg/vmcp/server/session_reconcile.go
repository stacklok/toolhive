// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"time"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// reconcileSessionsOnRegistryChange watches a DynamicRegistry for membership
// changes and evicts live sessions that still hold a connection to a backend the
// registry no longer lists, so a dropped backend's lingering per-session
// connection (e.g. an SSE server-push stream) is reclaimed promptly rather than
// persisting until the owning client session ends (#6546).
//
// It follows the same version-counter poll pattern as periodicStatusReporting,
// but with its own independent version tracker so a change can never be absorbed
// by another observer, and it runs regardless of whether status reporting is
// enabled. Only a DynamicRegistry can drop a backend; for a static registry
// membership never changes, so this returns immediately.
//
// The loop runs until ctx is cancelled (on server Stop). pollInterval is passed
// in (rather than read from a package-level default) so tests can drive it
// without mutating shared state that a parallel test also touches.
func (s *Server) reconcileSessionsOnRegistryChange(ctx context.Context, pollInterval time.Duration) {
	dynamicReg, isDynamic := s.backendRegistry.(vmcp.DynamicRegistry)
	if !isDynamic || s.vmcpSessionMgr == nil {
		return
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	lastVersion := dynamicReg.Version()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			v := dynamicReg.Version()
			if v == lastVersion {
				continue
			}
			// A single new version may fold together several Upserts and Removes.
			// EvictStaleSessions reconciles against current membership, so it
			// handles every drop the new version carries in one pass and is a
			// no-op when the change added backends without removing any.
			lastVersion = v
			s.vmcpSessionMgr.EvictStaleSessions(ctx)
		}
	}
}
