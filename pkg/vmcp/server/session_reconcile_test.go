// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// evictCountingSessionManager records how many times EvictStaleSessions is
// called. It reuses stubSessionManager for every other method.
type evictCountingSessionManager struct {
	stubSessionManager
	evictCalls atomic.Int64
}

func (m *evictCountingSessionManager) EvictStaleSessions(context.Context) int {
	m.evictCalls.Add(1)
	return 0
}

// TestReconcileSessionsOnRegistryChange_EvictsOnVersionChange verifies that a
// registry membership change (a backend dropped, bumping the version) triggers
// EvictStaleSessions without waiting for any longer interval (#6546).
func TestReconcileSessionsOnRegistryChange_EvictsOnVersionChange(t *testing.T) {
	t.Parallel()

	orig := versionPollInterval
	versionPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { versionPollInterval = orig })

	reg := &testDynamicRegistry{}
	mgr := &evictCountingSessionManager{}
	srv := &Server{backendRegistry: reg, vmcpSessionMgr: mgr}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.reconcileSessionsOnRegistryChange(ctx)
	}()

	// No change yet -> no eviction.
	time.Sleep(50 * time.Millisecond)
	require.Zero(t, mgr.evictCalls.Load(), "no eviction should occur before any registry change")

	// Drop a backend: the version bumps and the next poll must reconcile.
	require.NoError(t, reg.Remove("some-backend"))
	require.Eventually(t, func() bool {
		return mgr.evictCalls.Load() >= 1
	}, time.Second, 5*time.Millisecond, "a registry change should trigger EvictStaleSessions")

	cancel()
	<-done
}

// TestReconcileSessionsOnRegistryChange_StaticRegistryReturns verifies that the
// loop exits immediately for a static (non-dynamic) registry, whose membership
// never changes, rather than polling for the server's lifetime.
func TestReconcileSessionsOnRegistryChange_StaticRegistryReturns(t *testing.T) {
	t.Parallel()

	srv := &Server{
		backendRegistry: vmcp.NewImmutableRegistry(nil),
		vmcpSessionMgr:  &evictCountingSessionManager{},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// A never-cancelled context would hang here if the loop kept running.
		srv.reconcileSessionsOnRegistryChange(context.Background())
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reconcile loop should return immediately for a static registry")
	}
}
