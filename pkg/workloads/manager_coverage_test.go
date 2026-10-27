// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package workloads

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adrg/xdg"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/client"
	clientMocks "github.com/stacklok/toolhive/pkg/client/mocks"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	runtimeMocks "github.com/stacklok/toolhive/pkg/container/runtime/mocks"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/process"
	"github.com/stacklok/toolhive/pkg/runner"
	statusMocks "github.com/stacklok/toolhive/pkg/workloads/statuses/mocks"
)

func TestDetachedReaperFixture_RejectsMismatchedInvocation(t *testing.T) {
	t.Parallel()
	executable, err := os.Executable()
	require.NoError(t, err)
	for _, tt := range []struct {
		name string
		mode string
	}{
		{name: "marked malformed invocation", mode: "clean"},
		{name: "empty marker"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)
			// If TestMain falls through, select no tests so a regression cannot recursively spawn fixtures.
			cmd := exec.CommandContext(ctx, executable, "-test.run=^$")
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, detachedReaperTestEnv+"=") &&
					!strings.HasPrefix(entry, process.ToolHiveDetachedEnv+"=") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, detachedReaperTestEnv+"="+tt.mode,
				process.ToolHiveDetachedEnv+"="+process.ToolHiveDetachedValue)
			output, err := cmd.CombinedOutput()
			require.NoError(t, ctx.Err(), "fixture must reject the invocation before the deadline")
			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr)
			require.Equal(t, 2, exitErr.ExitCode(), string(output))
			require.Contains(t, string(output), "unexpected detached reaper invocation")
		})
	}
}

func TestDefaultManager_prepareWorkloadRestart_ClientCleanup(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name          string
		stable        bool
		cancelCleanup bool
		preCancelled  bool
		factoryError  bool
	}{
		{name: "cleanup error still restarts"},
		{name: "cleanup error still restarts stable workload", stable: true},
		{name: "cancellation during cleanup preserves status", cancelCleanup: true},
		{name: "pre-cancelled restart preserves status", preCancelled: true},
		{name: "factory failure still restarts", factoryError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)
			if tt.preCancelled {
				cancel()
			}
			ctrl := gomock.NewController(t)
			sm := statusMocks.NewMockStatusManager(ctrl)
			cm := clientMocks.NewMockManager(ctrl)
			const name, group = "retry-client-cleanup", "test-group"
			if !tt.factoryError {
				cleanupCall := cm.EXPECT().RemoveServerFromClients(ctx, name, group).DoAndReturn(func(context.Context, string, string) error {
					if !tt.preCancelled {
						require.NoError(t, ctx.Err(), "cancellation must begin inside client cleanup")
					}
					if tt.cancelCleanup {
						cancel()
					}
					return errors.New("client configuration is read-only")
				})
				if tt.preCancelled {
					cleanupCall.AnyTimes()
				}
			}
			if !tt.cancelCleanup && !tt.preCancelled {
				reason := "Container exited, restarting"
				if tt.stable {
					reason = "Container exited after stable run, restarting"
				}
				sm.EXPECT().SetWorkloadStatus(ctx, name, runtime.WorkloadStatusStarting, reason).Return(nil)
			}
			manager := &DefaultManager{statuses: sm}
			withClientManagerFactory(func(context.Context) (client.Manager, error) {
				if tt.factoryError {
					return nil, errors.New("client manager unavailable")
				}
				return cm, nil
			})(manager)

			manager.prepareWorkloadRestart(ctx, &runner.RunConfig{BaseName: name, Group: group}, tt.stable)
			if tt.cancelCleanup || tt.preCancelled {
				require.ErrorIs(t, ctx.Err(), context.Canceled)
			}
		})
	}
}

//nolint:paralleltest // saved-workload fixtures use t.Setenv
func TestDefaultManager_StopCleanupErrorsDoNotPreventStopped(t *testing.T) {
	isolateWorkloadStopState(t)
	for _, remote := range []bool{false, true} {
		for _, factoryError := range []bool{false, true} {
			t.Run(fmt.Sprintf("remote=%t/factoryError=%t", remote, factoryError), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				t.Cleanup(cancel)
				const name, group = "stop-client-cleanup", "test-group"
				rc := &runner.RunConfig{BaseName: name, Group: group}
				persistStopRunConfig(t, name, rc)
				ctrl := gomock.NewController(t)
				sm := statusMocks.NewMockStatusManager(ctrl)
				cm := clientMocks.NewMockManager(ctrl)
				rm := runtimeMocks.NewMockRuntime(ctrl)
				manager := &DefaultManager{statuses: sm, runtime: rm}
				withClientManagerFactory(func(context.Context) (client.Manager, error) {
					if factoryError {
						return nil, errors.New("client manager unavailable")
					}
					return cm, nil
				})(manager)
				var cleanupCall *gomock.Call
				if !factoryError {
					cleanupCall = cm.EXPECT().RemoveServerFromClients(gomock.Any(), name, group).
						Return(errors.New("client configuration is read-only"))
				}
				stopped := sm.EXPECT().SetWorkloadStatus(gomock.Any(), name, runtime.WorkloadStatusStopped, "").Return(nil)
				if cleanupCall != nil {
					stopped.After(cleanupCall)
				}
				if remote {
					require.NoError(t, manager.finishRemoteWorkloadStop(ctx, name, rc))
					return
				}
				sm.EXPECT().GetWorkloadPID(gomock.Any(), name).Return(0, nil)
				withPortFreer(func(context.Context, *runner.RunConfig) {})(manager)
				stop := rm.EXPECT().StopWorkload(gomock.Any(), name).Return(nil)
				stopped.After(stop)
				if cleanupCall != nil {
					cleanupCall.After(stop)
				}
				require.NoError(t, manager.stopSingleContainerWorkload(ctx, &runtime.ContainerInfo{
					Name: name, Labels: map[string]string{labels.LabelBaseName: name},
				}, rc))
			})
		}
	}
}

func TestDefaultManager_stopVerifiedProxy_SignalErrorPreservesPID(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	const name = "signal-error-proxy"
	pid := math.MaxInt32
	manager := &DefaultManager{statuses: statusMocks.NewMockStatusManager(gomock.NewController(t))}
	signalled := false
	signal := func(handle int) error {
		signalled = true
		require.Equal(t, pid, handle, "the injected signal must receive the saved PID")
		return errors.New("process signal failed")
	}

	require.False(t, manager.stopVerifiedProxy(ctx, name, pid, signal), "a failed signal must not reset the saved PID")
	require.True(t, signalled)
}

func TestDefaultManager_spawnDetached_ReapsOwnedChildren(t *testing.T) {
	isolateWorkloadStopState(t)
	t.Setenv(process.ToolHiveDetachedEnv, "")
	for _, mode := range []string{"clean", "error"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(detachedReaperTestEnv, mode)
			name := "detached-reaper-79cbf5-" + mode
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			t.Cleanup(cancel)
			sm := statusMocks.NewMockStatusManager(gomock.NewController(t))
			sm.EXPECT().SetWorkloadStatus(ctx, name, runtime.WorkloadStatusStarting, "").Return(nil)
			manager := &DefaultManager{statuses: sm}

			pid, err := manager.spawnDetached(ctx, &runner.RunConfig{BaseName: name})
			require.NoError(t, err)
			require.Positive(t, pid)
			reaped := false
			t.Cleanup(func() {
				if reaped {
					return
				}
				alive, findErr := process.FindProcess(pid)
				if findErr == nil && alive {
					child, findErr := os.FindProcess(pid)
					if findErr == nil {
						_ = child.Kill()
					}
				}
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cleanupCancel()
				require.NoError(t, process.WaitForExit(cleanupCtx, pid), "detached fixture must not outlive the test")
			})
			require.NoError(t, process.WaitForExit(ctx, pid), "spawnDetached must reap even a failed child")
			reaped = true
			alive, err := process.FindProcess(pid)
			require.NoError(t, err)
			require.False(t, alive)
			log, err := os.ReadFile(filepath.Join(xdg.DataHome, "toolhive", "logs", name+".log"))
			require.NoError(t, err)
			exitCode := 0
			if mode == "error" {
				exitCode = 42
			}
			require.Contains(t, string(log), fmt.Sprintf("detached reaper fixture exit %d", exitCode), "only the explicitly gated child fixture may run")
		})
	}
}
