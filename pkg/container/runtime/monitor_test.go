// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runtime_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	rtmocks "github.com/stacklok/toolhive/pkg/container/runtime/mocks"
)

func TestNewMonitor_Constructs(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRT := rtmocks.NewMockRuntime(ctrl)

	m := runtime.NewMonitor(mockRT, "workload-1")
	require.NotNil(t, m)
}

func TestWorkloadMonitor_StartMonitoring_WhenRunningStarts(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRT := rtmocks.NewMockRuntime(ctrl)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// StartMonitoring should verify running exactly once on first call.
	mockRT.EXPECT().IsWorkloadRunning(ctx, "workload-1").Return(true, nil).Times(1)
	// StartMonitoring now gets the container start time
	mockRT.EXPECT().GetWorkloadInfo(ctx, "workload-1").Return(runtime.ContainerInfo{
		StartedAt: time.Now(),
	}, nil).Times(1)

	m := runtime.NewMonitor(mockRT, "workload-1")
	ch, err := m.StartMonitoring(ctx)
	require.NoError(t, err)
	require.NotNil(t, ch)

	// Idempotent: subsequent call returns same channel and does not call runtime again.
	ch2, err := m.StartMonitoring(ctx)
	require.NoError(t, err)
	assert.Equal(t, ch, ch2)

	// Ensure StopMonitoring is safe and unblocks the background goroutine quickly
	// without needing to wait for the 5s ticker.
	m.StopMonitoring()
}

func TestWorkloadMonitor_StartMonitoring_WhenNotRunningErrors(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRT := rtmocks.NewMockRuntime(ctrl)

	ctx := t.Context()

	mockRT.EXPECT().IsWorkloadRunning(ctx, "workload-2").Return(false, nil)

	m := runtime.NewMonitor(mockRT, "workload-2")
	ch, err := m.StartMonitoring(ctx)
	require.Error(t, err)
	assert.Nil(t, ch)
	assert.ErrorIs(t, err, runtime.ErrContainerNotRunning)
}

func TestWorkloadMonitor_StartMonitoring_RuntimeErrorBubblesUp(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRT := rtmocks.NewMockRuntime(ctrl)

	ctx := t.Context()

	mockRT.EXPECT().IsWorkloadRunning(ctx, "workload-3").Return(false, errors.New("boom"))

	m := runtime.NewMonitor(mockRT, "workload-3")
	ch, err := m.StartMonitoring(ctx)
	require.Error(t, err)
	assert.Nil(t, ch)
	assert.EqualError(t, err, "boom")
}

func TestWorkloadMonitor_StopMonitoring_NotRunningIsNoop(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRT := rtmocks.NewMockRuntime(ctrl)

	// Construct monitor but do not start
	m := runtime.NewMonitor(mockRT, "workload-4")
	// Should not panic or deadlock
	m.StopMonitoring()
}

func TestWorkloadMonitor_StartStop_TerminatesQuickly(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRT := rtmocks.NewMockRuntime(ctrl)

	// Start path: initially running
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	mockRT.EXPECT().IsWorkloadRunning(ctx, "workload-5").Return(true, nil).Times(1)
	// StartMonitoring now gets the container start time
	mockRT.EXPECT().GetWorkloadInfo(ctx, "workload-5").Return(runtime.ContainerInfo{
		StartedAt: time.Now(),
	}, nil).Times(1)

	m := runtime.NewMonitor(mockRT, "workload-5")
	ch, err := m.StartMonitoring(ctx)
	require.NoError(t, err)
	require.NotNil(t, ch)

	// Stop should complete promptly (no waits on the 5s ticker).
	done := make(chan struct{})
	go func() {
		m.StopMonitoring()
		close(done)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatal("StopMonitoring did not return promptly")
	}
}

// --- Polling loop tests (previously untested) ---

func TestWorkloadMonitor_ContainerExitsUnexpectedly(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRT := rtmocks.NewMockRuntime(ctrl)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	startTime := time.Now()

	// Initial start checks
	mockRT.EXPECT().IsWorkloadRunning(gomock.Any(), "exit-test").Return(true, nil).Times(1)
	mockRT.EXPECT().GetWorkloadInfo(gomock.Any(), "exit-test").Return(runtime.ContainerInfo{
		StartedAt: startTime,
	}, nil).Times(1)

	// First poll: container is no longer running
	mockRT.EXPECT().IsWorkloadRunning(gomock.Any(), "exit-test").Return(false, nil).Times(1)
	// The monitor fetches logs and info for the error message
	mockRT.EXPECT().GetWorkloadLogs(gomock.Any(), "exit-test", false, 50).Return("some logs", nil).Times(1)
	mockRT.EXPECT().GetWorkloadInfo(gomock.Any(), "exit-test").Return(runtime.ContainerInfo{
		Status: "exited",
	}, nil).Times(1)

	m := runtime.NewMonitor(mockRT, "exit-test")
	ch, err := m.StartMonitoring(ctx)
	require.NoError(t, err)

	select {
	case exitErr := <-ch:
		require.Error(t, exitErr)
		assert.ErrorIs(t, exitErr, runtime.ErrContainerExited)
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for container exit error")
	}
}

func TestWorkloadMonitor_ContainerRemoved(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRT := rtmocks.NewMockRuntime(ctrl)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	startTime := time.Now()

	// Initial start checks
	mockRT.EXPECT().IsWorkloadRunning(gomock.Any(), "remove-test").Return(true, nil).Times(1)
	mockRT.EXPECT().GetWorkloadInfo(gomock.Any(), "remove-test").Return(runtime.ContainerInfo{
		StartedAt: startTime,
	}, nil).Times(1)

	// First poll: container not found (removed)
	mockRT.EXPECT().IsWorkloadRunning(gomock.Any(), "remove-test").Return(false, runtime.ErrContainerNotFound).Times(1)

	m := runtime.NewMonitor(mockRT, "remove-test")
	ch, err := m.StartMonitoring(ctx)
	require.NoError(t, err)

	select {
	case exitErr := <-ch:
		require.Error(t, exitErr)
		assert.ErrorIs(t, exitErr, runtime.ErrContainerRemoved)
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for container removed error")
	}
}

func TestWorkloadMonitor_ContainerRestarted(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRT := rtmocks.NewMockRuntime(ctrl)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	originalStart := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	newStart := time.Date(2025, 1, 1, 12, 5, 0, 0, time.UTC)

	// Initial start checks
	mockRT.EXPECT().IsWorkloadRunning(gomock.Any(), "restart-test").Return(true, nil).Times(1)
	mockRT.EXPECT().GetWorkloadInfo(gomock.Any(), "restart-test").Return(runtime.ContainerInfo{
		StartedAt: originalStart,
	}, nil).Times(1)

	// First poll: container still running but with different start time
	mockRT.EXPECT().IsWorkloadRunning(gomock.Any(), "restart-test").Return(true, nil).Times(1)
	mockRT.EXPECT().GetWorkloadInfo(gomock.Any(), "restart-test").Return(runtime.ContainerInfo{
		StartedAt: newStart,
	}, nil).Times(1)

	m := runtime.NewMonitor(mockRT, "restart-test")
	ch, err := m.StartMonitoring(ctx)
	require.NoError(t, err)

	select {
	case exitErr := <-ch:
		require.Error(t, exitErr)
		assert.ErrorIs(t, exitErr, runtime.ErrContainerRestarted)
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for container restart error")
	}
}

func TestWorkloadMonitor_ContextCanceled(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRT := rtmocks.NewMockRuntime(ctrl)

	ctx, cancel := context.WithCancel(t.Context())

	startTime := time.Now()

	// Initial start checks
	mockRT.EXPECT().IsWorkloadRunning(gomock.Any(), "cancel-test").Return(true, nil).Times(1)
	mockRT.EXPECT().GetWorkloadInfo(gomock.Any(), "cancel-test").Return(runtime.ContainerInfo{
		StartedAt: startTime,
	}, nil).Times(1)

	// Allow polling calls but don't require them
	mockRT.EXPECT().IsWorkloadRunning(gomock.Any(), "cancel-test").Return(true, nil).AnyTimes()
	mockRT.EXPECT().GetWorkloadInfo(gomock.Any(), "cancel-test").Return(runtime.ContainerInfo{
		StartedAt: startTime,
	}, nil).AnyTimes()

	m := runtime.NewMonitor(mockRT, "cancel-test")
	_, err := m.StartMonitoring(ctx)
	require.NoError(t, err)

	// Cancel the context — the goroutine should exit cleanly
	cancel()

	// Give the goroutine time to exit
	time.Sleep(200 * time.Millisecond)

	// StopMonitoring should still work cleanly after context cancel
	m.StopMonitoring()
}

func TestWorkloadMonitor_RuntimeErrorDuringPollingContinues(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRT := rtmocks.NewMockRuntime(ctrl)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	startTime := time.Now()

	// Initial start checks
	mockRT.EXPECT().IsWorkloadRunning(gomock.Any(), "error-test").Return(true, nil).Times(1)
	mockRT.EXPECT().GetWorkloadInfo(gomock.Any(), "error-test").Return(runtime.ContainerInfo{
		StartedAt: startTime,
	}, nil).Times(1)

	// First poll: transient runtime error (not "not found") — should continue
	mockRT.EXPECT().IsWorkloadRunning(gomock.Any(), "error-test").Return(false, errors.New("network timeout")).Times(1)

	// Second poll: container exits
	mockRT.EXPECT().IsWorkloadRunning(gomock.Any(), "error-test").Return(false, nil).Times(1)
	mockRT.EXPECT().GetWorkloadLogs(gomock.Any(), "error-test", false, 50).Return("", nil).Times(1)
	mockRT.EXPECT().GetWorkloadInfo(gomock.Any(), "error-test").Return(runtime.ContainerInfo{
		Status: "exited",
	}, nil).Times(1)

	m := runtime.NewMonitor(mockRT, "error-test")
	ch, err := m.StartMonitoring(ctx)
	require.NoError(t, err)

	select {
	case exitErr := <-ch:
		require.Error(t, exitErr)
		// Should get the exit error from the second poll, not the transient error
		assert.ErrorIs(t, exitErr, runtime.ErrContainerExited)
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for container exit error")
	}
}

// Compile-time assertion that WorkloadMonitor implements Monitor
var _ runtime.Monitor = (*runtime.WorkloadMonitor)(nil)
