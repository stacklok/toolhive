// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package workloads

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	runtimeMocks "github.com/stacklok/toolhive/pkg/container/runtime/mocks"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/runner"
	statusMocks "github.com/stacklok/toolhive/pkg/workloads/statuses/mocks"
)

// unverifiableSupervisorPID returns a live PID whose identity this test
// process cannot inspect. PID 1 belongs to another user unless tests run as
// root, in which case the scenario cannot be reproduced.
func unverifiableSupervisorPID(t *testing.T, name string) int {
	t.Helper()
	if identifySupervisor(1, name) != supervisorUnverifiable {
		t.Skip("PID 1 can be inspected by this user")
	}
	return 1
}

func TestDefaultManager_UnverifiableSupervisorFailsStopDeleteAndStart(t *testing.T) {
	t.Parallel()
	const name = "unverifiable-supervisor"
	pid := unverifiableSupervisorPID(t, name)
	exited := runtime.ContainerInfo{Name: name, State: "exited", Labels: map[string]string{labels.LabelBaseName: name}}

	t.Run("stop", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		rtMock := runtimeMocks.NewMockRuntime(ctrl)
		sm := statusMocks.NewMockStatusManager(ctrl)
		rtMock.EXPECT().GetWorkloadInfo(gomock.Any(), name).Return(exited, nil)
		sm.EXPECT().GetWorkloadPID(gomock.Any(), name).Return(pid, nil)
		// No status writes or runtime stop: stop must not report a supervisor
		// it could neither signal nor rule out as stopped.
		manager := &DefaultManager{runtime: rtMock, statuses: sm}

		err := manager.stopContainerWorkload(context.Background(), name, &runner.RunConfig{BaseName: name})
		require.ErrorIs(t, err, errSupervisorUnverifiable)
	})

	t.Run("delete", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		rtMock := runtimeMocks.NewMockRuntime(ctrl)
		sm := statusMocks.NewMockStatusManager(ctrl)
		rtMock.EXPECT().GetWorkloadInfo(gomock.Any(), name).Return(exited, nil)
		sm.EXPECT().SetWorkloadStatus(gomock.Any(), name, runtime.WorkloadStatusRemoving, "").Return(nil)
		sm.EXPECT().GetWorkloadPID(gomock.Any(), name).Return(pid, nil)
		sm.EXPECT().SetWorkloadStatus(gomock.Any(), name, runtime.WorkloadStatusError, gomock.Any()).Return(nil)
		// No container removal or status deletion while the supervisor may live.
		manager := &DefaultManager{runtime: rtMock, statuses: sm}

		err := manager.deleteContainerWorkload(context.Background(), name, nil)
		require.ErrorIs(t, err, errSupervisorUnverifiable)
	})

	t.Run("start", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		sm := statusMocks.NewMockStatusManager(ctrl)
		sm.EXPECT().GetWorkloadPID(gomock.Any(), name).Return(pid, nil).Times(2)
		manager := &DefaultManager{statuses: sm}

		foregroundPID, err := manager.stopOtherSupervisor(context.Background(), name)
		require.ErrorIs(t, err, errSupervisorUnverifiable)
		assert.Zero(t, foregroundPID)
	})
}

// A supervisor's shutdown stops its containers one by one with the runtime's
// stop timeout, so it can outlast stop's wait. Delete and start must not
// overlap with it, but `thv stop` followed by `thv start` or `thv rm` must not
// fail either, so they kill a supervisor that is still exiting.
func TestDefaultManager_ExitingSupervisorIsKilledBeforeDeleteAndStart(t *testing.T) {
	t.Parallel()

	t.Run("stop leaves it exiting", func(t *testing.T) {
		t.Parallel()
		const name = "exiting-supervisor-stop"
		proxy := startProxyProcess(t, name, true)
		ctrl := gomock.NewController(t)
		sm := statusMocks.NewMockStatusManager(ctrl)
		sm.EXPECT().GetWorkloadPID(gomock.Any(), name).Return(proxy.pid, nil)
		manager := &DefaultManager{statuses: sm}
		withProxyExitTimeout(50 * time.Millisecond)(manager)

		require.Equal(t, proxyExiting, manager.stopProcess(context.Background(), name, false))
		select {
		case <-proxy.exited:
			t.Error("stop must not kill a supervisor that is shutting down")
		default:
		}
	})

	t.Run("start", func(t *testing.T) {
		t.Parallel()
		const name = "exiting-supervisor-start"
		proxy := startProxyProcess(t, name, true)
		ctrl := gomock.NewController(t)
		sm := statusMocks.NewMockStatusManager(ctrl)
		sm.EXPECT().GetWorkloadPID(gomock.Any(), name).Return(proxy.pid, nil).Times(2)
		sm.EXPECT().ResetWorkloadPIDIfMatch(gomock.Any(), name, proxy.pid).Return(nil)
		manager := &DefaultManager{statuses: sm}
		withProxyExitTimeout(50 * time.Millisecond)(manager)

		foregroundPID, err := manager.stopOtherSupervisor(context.Background(), name)
		require.NoError(t, err)
		assert.Zero(t, foregroundPID)
		requireExited(t, proxy)
	})

	t.Run("delete", func(t *testing.T) {
		t.Parallel()
		const name = "exiting-supervisor-delete"
		proxy := startProxyProcess(t, name, true)
		ctrl := gomock.NewController(t)
		rtMock := runtimeMocks.NewMockRuntime(ctrl)
		sm := statusMocks.NewMockStatusManager(ctrl)
		rtMock.EXPECT().GetWorkloadInfo(gomock.Any(), name).Return(runtime.ContainerInfo{
			Name: name, State: "running", Labels: map[string]string{labels.LabelBaseName: name},
		}, nil)
		sm.EXPECT().SetWorkloadStatus(gomock.Any(), name, runtime.WorkloadStatusRemoving, "").Return(nil)
		sm.EXPECT().GetWorkloadPID(gomock.Any(), name).Return(proxy.pid, nil)
		sm.EXPECT().ResetWorkloadPIDIfMatch(gomock.Any(), name, proxy.pid).Return(nil)
		rtMock.EXPECT().RemoveWorkload(gomock.Any(), name).DoAndReturn(func(context.Context, string) error {
			requireExited(t, proxy)
			return nil
		})
		rtMock.EXPECT().GetWorkloadInfo(gomock.Any(), name).Return(runtime.ContainerInfo{}, runtime.ErrWorkloadNotFound)
		sm.EXPECT().DeleteWorkloadStatus(gomock.Any(), name).Return(nil)
		manager := &DefaultManager{
			runtime: rtMock, statuses: sm,
			clientManagers: testClientManagerProvider(unavailableClientManager),
		}
		withProxyExitTimeout(50 * time.Millisecond)(manager)
		withPortFreer(func(context.Context, *runner.RunConfig) {
			t.Error("a signalled supervisor must not be signalled again through port cleanup")
		})(manager)

		require.NoError(t, manager.deleteContainerWorkload(context.Background(), name, nil))
	})
}

func requireExited(t *testing.T, proxy *workloadProxyProcess) {
	t.Helper()
	// The helper's Wait goroutine closes exited just after reaping the process.
	select {
	case <-proxy.exited:
	case <-time.After(time.Second):
		t.Fatal("the old supervisor must be gone before the caller proceeds")
	}
}

func TestDefaultManager_maybeSetupRemoteWorkload_ForegroundRunKeepsOwnership(t *testing.T) {
	t.Parallel()
	const name = "foreground-remote"
	proxy := startVerifiedProxyProcess(t, "", "run", "--name", name, "https://example.com/mcp")
	ctrl := gomock.NewController(t)
	sm := statusMocks.NewMockStatusManager(ctrl)
	sm.EXPECT().GetWorkload(gomock.Any(), name).Return(core.Workload{Name: name, Status: runtime.WorkloadStatusUnhealthy}, nil)
	sm.EXPECT().GetWorkloadPID(gomock.Any(), name).Return(proxy.pid, nil)
	// No status writes: the foreground run would delete the workload if it
	// were signalled during restart.
	manager := &DefaultManager{statuses: sm}

	gotRunner, err := manager.maybeSetupRemoteWorkload(context.Background(), name,
		&runner.RunConfig{BaseName: name, RemoteURL: "https://example.com/mcp"})
	require.NoError(t, err)
	assert.Nil(t, gotRunner)
	select {
	case <-proxy.exited:
		t.Error("foreground run must keep ownership of its remote workload")
	case <-time.After(100 * time.Millisecond):
	}
}
