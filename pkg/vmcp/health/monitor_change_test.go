// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
)

// TestStatusTracker_AdvertisabilityTransitions verifies the changed-signal
// contract of RecordSuccess/RecordFailure (#5786): they report true exactly
// when the backend's advertisability genuinely flips, and false for every
// state update that does not change whether the backend participates in
// capability aggregation — including a previously-untracked backend's first
// result (the registry fallback the aggregation had been serving is
// advertisable in practice, so a first success changes nothing).
func TestStatusTracker_AdvertisabilityTransitions(t *testing.T) {
	t.Parallel()

	const (
		id   = "backend-1"
		name = "Backend 1"
	)
	checkErr := errors.New("check failed")

	// Each step drives one Record* call against a tracker with threshold 2 and
	// asserts the returned changed signal.
	type step struct {
		name        string
		record      func(t *statusTracker) bool
		wantChanged bool
	}
	steps := []step{
		{
			name: "first success is quiet (fallback already advertisable)",
			record: func(tr *statusTracker) bool {
				return tr.RecordSuccess(id, name, vmcp.BackendHealthy)
			},
			wantChanged: false,
		},
		{
			name: "repeat success is steady state",
			record: func(tr *statusTracker) bool {
				return tr.RecordSuccess(id, name, vmcp.BackendHealthy)
			},
			wantChanged: false,
		},
		{
			name: "healthy to degraded stays advertisable",
			record: func(tr *statusTracker) bool {
				return tr.RecordSuccess(id, name, vmcp.BackendDegraded)
			},
			wantChanged: false,
		},
		{
			name: "failure below threshold keeps advertisability",
			record: func(tr *statusTracker) bool {
				return tr.RecordFailure(id, name, vmcp.BackendUnhealthy, checkErr)
			},
			wantChanged: false,
		},
		{
			name: "failure crossing threshold drops the backend",
			record: func(tr *statusTracker) bool {
				return tr.RecordFailure(id, name, vmcp.BackendUnhealthy, checkErr)
			},
			wantChanged: true,
		},
		{
			name: "failure while already unhealthy is steady state",
			record: func(tr *statusTracker) bool {
				return tr.RecordFailure(id, name, vmcp.BackendUnhealthy, checkErr)
			},
			wantChanged: false,
		},
		{
			name: "success after unhealthy recovers the backend",
			record: func(tr *statusTracker) bool {
				return tr.RecordSuccess(id, name, vmcp.BackendHealthy)
			},
			wantChanged: true,
		},
	}

	tracker := newStatusTracker(2, nil)
	for _, s := range steps {
		got := s.record(tracker)
		assert.Equal(t, s.wantChanged, got, "step %q", s.name)
	}
}

// TestStatusTracker_NewBackend_QuietUntilThresholdCross verifies the
// first-result semantics for a never-successful backend: below-threshold
// failures are quiet (a brand-new backend whose workload is still starting
// must not flap connected sessions — the aggregation had been serving the
// advertisable registry fallback, and UnhealthyThreshold promises tolerance),
// the genuine threshold crossing reports the withdrawal, and a subsequent
// recovery reports the restore.
func TestStatusTracker_NewBackend_QuietUntilThresholdCross(t *testing.T) {
	t.Parallel()

	const (
		id   = "backend-1"
		name = "Backend 1"
	)
	boom := errors.New("boom")

	tracker := newStatusTracker(2, nil)

	changed := tracker.RecordFailure(id, name, vmcp.BackendUnhealthy, boom)
	assert.False(t, changed, "first failure below threshold must be quiet")

	changed = tracker.RecordFailure(id, name, vmcp.BackendUnhealthy, boom)
	assert.True(t, changed, "the genuine threshold crossing is the backend's first real withdrawal")

	changed = tracker.RecordFailure(id, name, vmcp.BackendUnhealthy, boom)
	assert.False(t, changed, "failures past the threshold are steady state")

	changed = tracker.RecordSuccess(id, name, vmcp.BackendHealthy)
	assert.True(t, changed, "recovery after the withdrawal must be reported")
}

// TestStatusTracker_NewBackend_RecoveryAfterQuietFailure verifies the
// reviewer-flagged startup path (#5786 PR1 review): a new backend's failed
// first probe is quiet, and the success that follows once the workload is up
// reports a change — the quiet failure left the backend tracked as
// BackendUnknown, which the catalog excludes, so the recovery must restore it
// for any session that re-derived in the interim.
func TestStatusTracker_NewBackend_RecoveryAfterQuietFailure(t *testing.T) {
	t.Parallel()

	const (
		id   = "backend-1"
		name = "Backend 1"
	)

	tracker := newStatusTracker(3, nil)

	assert.False(t, tracker.RecordFailure(id, name, vmcp.BackendUnhealthy, errors.New("starting")),
		"first failed probe of a starting workload must be quiet")
	assert.False(t, tracker.RecordFailure(id, name, vmcp.BackendUnhealthy, errors.New("starting")),
		"second below-threshold failure must be quiet")
	assert.True(t, tracker.RecordSuccess(id, name, vmcp.BackendHealthy),
		"success after quiet failures must report (tracked BackendUnknown excluded the backend from the catalog)")
}

// TestStatusTracker_ThresholdOne_FirstFailureReportsChange verifies that with
// UnhealthyThreshold of 1 the very first failed probe classifies the backend
// and reports the withdrawal: there is no below-threshold window to be quiet
// in.
func TestStatusTracker_ThresholdOne_FirstFailureReportsChange(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(1, nil)
	changed := tracker.RecordFailure("backend-1", "Backend 1", vmcp.BackendUnhealthy, errors.New("boom"))
	assert.True(t, changed)
}

// TestMonitor_OnChange_FiresOnRecoveryTransition drives a backend through
// fail -> recover via the monitor's own health-check loop and asserts OnChange
// listeners observe both the drop-out and the recovery, with strictly
// increasing generations, and observe nothing more in steady state.
func TestMonitor_OnChange_FiresOnRecoveryTransition(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	// healthy flag controls whether checks succeed; starts failing.
	var healthy atomic.Bool
	mockClient := mocks.NewMockBackendClient(ctrl)
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
			if healthy.Load() {
				return &vmcp.CapabilityList{}, nil
			}
			return nil, errors.New("backend unavailable")
		}).
		AnyTimes()

	monitor, err := NewMonitor(mockClient,
		[]vmcp.Backend{{ID: "backend-1", Name: "Backend 1", BaseURL: "http://localhost:8080", TransportType: "sse"}},
		MonitorConfig{
			CheckInterval:      25 * time.Millisecond,
			UnhealthyThreshold: 1,
			Timeout:            10 * time.Millisecond,
		})
	require.NoError(t, err)

	fires := make(chan uint64, 64)
	monitor.OnChange(func(gen uint64) { fires <- gen })

	require.NoError(t, monitor.Start(context.Background()))
	t.Cleanup(func() { _ = monitor.Stop() })

	// With UnhealthyThreshold 1 the initial failing check classifies the
	// backend as unhealthy immediately: one delivery.
	firstGen := waitForFire(t, fires)

	// Steady failing state: no further deliveries.
	assertNoFire(t, fires)

	// Flip to healthy: the recovery transition must be delivered with a
	// strictly greater generation.
	healthy.Store(true)
	recoveryGen := waitForFire(t, fires)
	assert.Greater(t, recoveryGen, firstGen)

	require.Eventually(t, func() bool {
		status, err := monitor.GetBackendStatus("backend-1")
		return err == nil && ShouldAdvertise(status)
	}, 2*time.Second, 10*time.Millisecond, "backend must become advertisable after recovery")

	// Steady healthy state: no further deliveries.
	assertNoFire(t, fires)
}

// TestMonitor_OnChange_FiresOnBackendSetChange verifies UpdateBackends
// notifies listeners when the monitored set gains or loses a backend, and
// stays quiet when the set is unchanged.
func TestMonitor_OnChange_FiresOnBackendSetChange(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockClient := mocks.NewMockBackendClient(ctrl)
	mockClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(&vmcp.CapabilityList{}, nil).
		AnyTimes()

	b1 := vmcp.Backend{ID: "backend-1", Name: "Backend 1", BaseURL: "http://localhost:8080", TransportType: "sse"}
	b2 := vmcp.Backend{ID: "backend-2", Name: "Backend 2", BaseURL: "http://localhost:8081", TransportType: "sse"}

	// A long check interval keeps the periodic loop quiet after the initial
	// check, so the assertions below observe only UpdateBackends-driven
	// deliveries. It is also the debounce window, hence the drain step.
	monitor, err := NewMonitor(mockClient, []vmcp.Backend{b1}, MonitorConfig{
		CheckInterval:      50 * time.Millisecond,
		UnhealthyThreshold: 1,
		Timeout:            10 * time.Millisecond,
	})
	require.NoError(t, err)

	fires := make(chan uint64, 64)
	monitor.OnChange(func(gen uint64) { fires <- gen })

	require.NoError(t, monitor.Start(context.Background()))
	t.Cleanup(func() { _ = monitor.Stop() })

	// The initial successful check is quiet — the registry fallback the
	// aggregation had been serving is already advertisable, so a first
	// success flips nothing — leaving the debounce leading edge free for the
	// UpdateBackends deliveries below.
	monitor.WaitForInitialHealthChecks()
	assertNoFire(t, fires)

	// Unchanged set: no delivery.
	monitor.UpdateBackends([]vmcp.Backend{b1})
	assertNoFire(t, fires)

	// Adding a backend delivers (the membership change itself; the new
	// backend's successful initial check is quiet).
	monitor.UpdateBackends([]vmcp.Backend{b1, b2})
	waitForFire(t, fires)
	drainFires(fires)
	time.Sleep(60 * time.Millisecond)
	drainFires(fires)

	// Removing a backend delivers.
	monitor.UpdateBackends([]vmcp.Backend{b1})
	waitForFire(t, fires)
}

func waitForFire(t *testing.T, fires <-chan uint64) uint64 {
	t.Helper()
	select {
	case gen := <-fires:
		return gen
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for OnChange notification")
		return 0
	}
}

func assertNoFire(t *testing.T, fires <-chan uint64) {
	t.Helper()
	select {
	case gen := <-fires:
		t.Fatalf("unexpected OnChange notification with generation %d", gen)
	case <-time.After(100 * time.Millisecond):
	}
}

func drainFires(fires <-chan uint64) {
	for {
		select {
		case <-fires:
		default:
			return
		}
	}
}
