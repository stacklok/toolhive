// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// selectiveSessionManager is a per-ID liveness stub for regression test.
type selectiveSessionManager struct {
	mu    sync.RWMutex
	alive map[string]bool
}

func (m *selectiveSessionManager) GetMultiSession(_ context.Context, sessionID string) (vmcpsession.MultiSession, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ok := m.alive[sessionID]
	return nil, ok
}
func (m *selectiveSessionManager) Generate() string { panic("Generate unexpected") }
func (m *selectiveSessionManager) Validate(string) (bool, error) {
	panic("Validate unexpected")
}
func (m *selectiveSessionManager) Terminate(string) (bool, error) { return false, nil }
func (m *selectiveSessionManager) CreateSession(context.Context, string, vmcpsession.ListChangedSink) (vmcpsession.MultiSession, error) {
	panic("CreateSession unexpected")
}
func (m *selectiveSessionManager) DecorateSession(string, func(vmcpsession.MultiSession) vmcpsession.MultiSession) error {
	panic("DecorateSession unexpected")
}
func (m *selectiveSessionManager) NotifyBackendExpired(string, string, map[string]string) {}

// TestHealthResyncRegistry_BoundedAfterSessionExpiry_5860 verifies that
// resyncSessionsOnBackendHealthChange prunes dead sessions synchronously and
// only triggers live sessions. On the leaky base, dead workers are retained
// until the next async liveness guard runs, and all workers are triggered.
func TestHealthResyncRegistry_BoundedAfterSessionExpiry_5860(t *testing.T) {
	t.Parallel()

	// Track which workers were actually triggered.
	var mu sync.Mutex
	triggered := make(map[string]int)
	var totalTriggers atomic.Int32

	makeWorker := func(id string) *listChangedResyncWorker {
		return &listChangedResyncWorker{
			baseCtx: context.Background(),
			run: func(ctx context.Context, purge bool) {
				mu.Lock()
				triggered[id]++
				mu.Unlock()
				totalTriggers.Add(1)
			},
		}
	}

	alive := map[string]bool{
		"live-1": true,
		"live-2": true,
		// dead-1, dead-2 remain false (expired)
	}
	mgr := &selectiveSessionManager{alive: alive}
	srv := &Server{
		core:           &fakeCore{},
		vmcpSessionMgr: mgr,
		resyncBaseCtx:  context.Background(),
	}

	// Add 4 workers: 2 live, 2 dead.
	for _, id := range []string{"live-1", "live-2", "dead-1", "dead-2"} {
		srv.healthResync.add(id, makeWorker(id))
	}
	require.Len(t, srv.healthResync.snapshot(), 4, "precondition: registry holds all sessions")

	// Fan-out: should synchronously prune dead entries and only trigger live.
	srv.resyncSessionsOnBackendHealthChange(1)

	// Give triggered workers a moment to run (they are async via trigger).
	// Use Eventually to wait for expected live triggers.
	require.Eventually(t, func() bool { return totalTriggers.Load() >= 2 },
		2*time.Second, 10*time.Millisecond, "live workers must be triggered")

	// Allow a short window for any (incorrect) dead triggers to surface.
	// On base, dead workers will also have been triggered, so total will be 4.
	time.Sleep(100 * time.Millisecond)
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		// If dead were triggered, triggered map will contain them.
		_, dead1 := triggered["dead-1"]
		_, dead2 := triggered["dead-2"]
		return dead1 || dead2 || len(triggered) == 2
	}, 500*time.Millisecond, 10*time.Millisecond, "wait for trigger set to settle")

	mu.Lock()
	defer mu.Unlock()

	// Invariant: |workers| == |liveSessions| bounded.
	assert.Equal(t, 2, len(srv.healthResync.snapshot()),
		"registry must be bounded to live sessions after fan-out (dead pruned synchronously)")
	assert.Equal(t, 2, len(triggered),
		"only live sessions must be triggered (dead must be filtered at fan-out)")
	assert.Contains(t, triggered, "live-1")
	assert.Contains(t, triggered, "live-2")
	assert.NotContains(t, triggered, "dead-1", "dead session must not be triggered")
	assert.NotContains(t, triggered, "dead-2", "dead session must not be triggered")
}

// TestHealthResyncRegistry_FilterReducesTriggers_5860 is a second guard that
// ensures filtering happens BEFORE trigger, not just via async liveness prune.
func TestHealthResyncRegistry_FilterReducesTriggers_5860(t *testing.T) {
	t.Parallel()

	alive := map[string]bool{"live": true}
	mgr := &selectiveSessionManager{alive: alive}
	srv := &Server{
		core:           &fakeCore{},
		vmcpSessionMgr: mgr,
		resyncBaseCtx:  context.Background(),
	}

	var liveTriggers atomic.Int32
	var deadTriggers atomic.Int32

	liveWorker := &listChangedResyncWorker{
		baseCtx: context.Background(),
		run: func(context.Context, bool) { liveTriggers.Add(1) },
	}
	deadWorker := &listChangedResyncWorker{
		baseCtx: context.Background(),
		run: func(context.Context, bool) { deadTriggers.Add(1) },
	}

	srv.healthResync.add("live", liveWorker)
	srv.healthResync.add("dead", deadWorker)

	srv.resyncSessionsOnBackendHealthChange(42)

	require.Eventually(t, func() bool { return liveTriggers.Load() == 1 },
		2*time.Second, 10*time.Millisecond, "live must be triggered exactly once")
	// Dead must never be triggered; wait a bit to ensure no late trigger.
	// On base, dead will be triggered (incorrect). Use Sleep then assert.
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(0), deadTriggers.Load(), "dead session must not be triggered at fan-out")
	assert.Equal(t, 1, len(srv.healthResync.snapshot()), "registry must contain only live after fan-out")
}
