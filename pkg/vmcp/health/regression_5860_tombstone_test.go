// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
)

// TestRemovedBackends_Bounded_5860 verifies that removedBackends tombstones
// are bounded and expire after TTL. On the leaky base, removedBackends grows
// without bound as distinct backend IDs churn.
func TestRemovedBackends_Bounded_5860(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockBackendClient(ctrl)
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(&vmcp.CapabilityList{}, nil).
		AnyTimes()

	// Use short check interval so TTL (2*interval) is short for test.
	config := MonitorConfig{
		CheckInterval:      20 * time.Millisecond,
		UnhealthyThreshold: 1,
		Timeout:            10 * time.Millisecond,
	}
	monitor, err := NewMonitor(mockClient, nil, config)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, monitor.Start(ctx))
	defer func() { _ = monitor.Stop() }()

	// Churn 50 distinct backends: each added then removed.
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("backend-%d", i)
		b := vmcp.Backend{ID: id, Name: id, BaseURL: "http://example.com"}
		monitor.UpdateBackends([]vmcp.Backend{b})
		monitor.UpdateBackends([]vmcp.Backend{})
	}

	monitor.statusTracker.mu.RLock()
	initial := len(monitor.statusTracker.removedBackends)
	monitor.statusTracker.mu.RUnlock()
	require.Equal(t, 50, initial, "precondition: churn should create 50 tombstones")

	// Wait for TTL (2*CheckInterval = 40ms) plus margin, so tombstones should expire.
	time.Sleep(60 * time.Millisecond)

	// Trigger pruning via UpdateBackends (after fix, this prunes expired entries).
	monitor.UpdateBackends([]vmcp.Backend{})

	monitor.statusTracker.mu.RLock()
	after := len(monitor.statusTracker.removedBackends)
	monitor.statusTracker.mu.RUnlock()

	assert.Less(t, after, 10,
		"removedBackends must be bounded after TTL expiry (leak if still 50); after=%d", after)
}

// TestRemovedBackends_IsRemoved_Expiry_5860 verifies that isRemoved
// tombstone expires after TTL and allows re-recording health checks.
// On base, isRemoved never expires, so a removed backend stays ignored forever.
func TestRemovedBackends_IsRemoved_Expiry_5860(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(3, nil)
	// Use short interval so TTL = 2*interval = 40ms is fast for test.
	tracker.checkInterval = 20 * time.Millisecond
	backendID := "backend-expire-test"

	// Simulate removal.
	tracker.RemoveBackend(backendID)

	// Immediately, isRemoved should be true (race protection).
	tracker.mu.RLock()
	isRemovedNow := tracker.isRemoved(backendID)
	tracker.mu.RUnlock()
	require.True(t, isRemovedNow, "immediately after RemoveBackend, isRemoved must be true")

	// Simulate expiry by moving the stored timestamp to the past beyond TTL.
	// On fixed version (map[string]time.Time) this makes isRemoved prune and return false.
	// On base (map[string]bool) there is no timestamp — entry stays true forever,
	// so this test FAILs on base, proving the leak.
	tracker.mu.Lock()
	tracker.removedBackends[backendID] = time.Now().Add(-100 * time.Millisecond)
	tracker.mu.Unlock()

	tracker.mu.Lock()
	isRemovedAfter := tracker.isRemoved(backendID)
	// After expiry, entry must be pruned, so map should not contain the ID.
	_, stillExists := tracker.removedBackends[backendID]
	tracker.mu.Unlock()

	assert.False(t, isRemovedAfter, "isRemoved must be false after TTL expiry (tombstone should expire)")
	assert.False(t, stillExists, "expired tombstone must be pruned from map")

	// Verify that after expiry, health recording is no longer suppressed:
	// RecordSuccess should create a new state instead of being ignored.
	changed := tracker.RecordSuccess(backendID, "backend-expire-test", "healthy")
	// On fixed version, RecordSuccess will not be ignored and will create state.
	// We verify state exists.
	tracker.mu.RLock()
	_, exists := tracker.states[backendID]
	tracker.mu.RUnlock()
	assert.True(t, exists, "after tombstone expiry, RecordSuccess must create state (not be ignored)")
	_ = changed // suppress unused warning; advertisability flip is not asserted here
}

// TestRemovedBackends_PruneExpired_5860 verifies that pruneExpired removes old tombstones.
func TestRemovedBackends_PruneExpired_5860(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(3, nil)
	tracker.checkInterval = 20 * time.Millisecond
	// Insert 3 tombstones with old timestamps.
	past := time.Now().Add(-100 * time.Millisecond)
	tracker.mu.Lock()
	tracker.removedBackends["old-1"] = past
	tracker.removedBackends["old-2"] = past
	tracker.removedBackends["recent"] = time.Now()
	tracker.mu.Unlock()

	tracker.pruneExpiredRemovedBackends()

	tracker.mu.RLock()
	_, old1Exists := tracker.removedBackends["old-1"]
	_, old2Exists := tracker.removedBackends["old-2"]
	_, recentExists := tracker.removedBackends["recent"]
	count := len(tracker.removedBackends)
	tracker.mu.RUnlock()

	assert.False(t, old1Exists, "expired old-1 must be pruned")
	assert.False(t, old2Exists, "expired old-2 must be pruned")
	assert.True(t, recentExists, "recent must not be pruned")
	assert.Equal(t, 1, count, "only recent should remain")
}
