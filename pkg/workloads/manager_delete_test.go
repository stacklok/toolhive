// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package workloads

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	runtimeMocks "github.com/stacklok/toolhive/pkg/container/runtime/mocks"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/process"
	"github.com/stacklok/toolhive/pkg/runner"
	statusMocks "github.com/stacklok/toolhive/pkg/workloads/statuses/mocks"
)

//nolint:paralleltest // saved-workload fixtures use t.Setenv
func TestDefaultManager_deleteContainerWorkload_StopsSupervisorBeforeFreshRemoval(t *testing.T) {
	isolateWorkloadStopState(t)
	removeErr := errors.New("network teardown failed")
	for _, tt := range []struct {
		name        string
		missingErr  error
		removeErr   error
		verifiedErr error
	}{
		{name: "existing primary", verifiedErr: runtime.ErrWorkloadNotFound},
		{name: "primary starts after empty lookup", missingErr: runtime.ErrWorkloadNotFound, verifiedErr: runtime.ErrWorkloadNotFound},
		{name: "primary starts after inspect not found", missingErr: runtime.ErrContainerNotFound, verifiedErr: runtime.ErrContainerNotFound},
		{name: "deletion failure preserves saved config", missingErr: runtime.ErrWorkloadNotFound, removeErr: removeErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			t.Cleanup(cancel)
			const name = "deleting-workload"
			const containerName = "deleting-workload-1730000000"
			rc := &runner.RunConfig{BaseName: name, ContainerName: containerName}
			persistStopRunConfig(t, name, rc)
			saved, err := runner.LoadState(ctx, name)
			require.NoError(t, err)
			proxy := startVerifiedProxyProcess(t, name)
			ctrl := gomock.NewController(t)
			rtMock := runtimeMocks.NewMockRuntime(ctrl)
			sm := statusMocks.NewMockStatusManager(ctrl)
			primaryExists := tt.missingErr == nil
			if tt.missingErr != nil {
				rtMock.EXPECT().GetWorkloadInfo(gomock.Any(), name).
					Return(runtime.ContainerInfo{}, fmt.Errorf("initial inspection: %w", tt.missingErr))
			} else {
				rtMock.EXPECT().GetWorkloadInfo(gomock.Any(), name).Return(runtime.ContainerInfo{
					Name: containerName, Labels: map[string]string{labels.LabelBaseName: name},
				}, nil)
			}
			sm.EXPECT().SetWorkloadStatus(gomock.Any(), name, runtime.WorkloadStatusRemoving, "").Return(nil)
			sm.EXPECT().GetWorkloadPID(gomock.Any(), name).DoAndReturn(func(context.Context, string) (int, error) {
				// Model an in-flight supervisor startup finishing after the initial lookup.
				primaryExists = true
				return proxy.pid, nil
			})
			sm.EXPECT().ResetWorkloadPIDIfMatch(gomock.Any(), name, proxy.pid).Return(nil)
			removalName := name
			if tt.missingErr != nil {
				removalName = containerName
			}
			rtMock.EXPECT().RemoveWorkload(gomock.Any(), removalName).DoAndReturn(func(context.Context, string) error {
				alive, err := process.FindProcess(proxy.pid)
				require.NoError(t, err)
				require.False(t, alive, "runtime deletion began while the supervisor could still recreate the workload")
				// Check liveness before waiting: waiting first could hide the original ordering regression.
				select {
				case <-proxy.exited:
					assert.NoError(t, proxy.waitErr, "supervisor must finish graceful shutdown before runtime deletion")
				case <-time.After(time.Second):
					t.Fatal("supervisor helper was not reaped")
				}
				assert.True(t, primaryExists, "fresh removal must catch the workload created after the initial lookup")
				if tt.removeErr == nil {
					primaryExists = false
				}
				return tt.removeErr
			})
			if tt.removeErr != nil {
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), name, runtime.WorkloadStatusError, tt.removeErr.Error()).Return(nil)
			} else {
				rtMock.EXPECT().GetWorkloadInfo(gomock.Any(), removalName).
					Return(runtime.ContainerInfo{}, fmt.Errorf("removal verification: %w", tt.verifiedErr))
				sm.EXPECT().DeleteWorkloadStatus(gomock.Any(), name).Return(nil)
			}
			manager := &DefaultManager{runtime: rtMock, statuses: sm}
			withPortFreer(func(context.Context, *runner.RunConfig) {
				t.Error("verified supervisor exit must not need port fallback")
			})(manager)

			err = manager.deleteContainerWorkload(ctx, name, rc)
			if tt.removeErr != nil {
				require.ErrorIs(t, err, tt.removeErr)
				after, loadErr := runner.LoadState(ctx, name)
				require.NoError(t, loadErr, "failed deletion must retain recovery state")
				assert.Equal(t, saved, after)
			} else {
				require.NoError(t, err)
				assert.False(t, primaryExists, "delete must not leave a newly created primary behind")
				_, loadErr := runner.LoadState(ctx, name)
				require.Error(t, loadErr, "successful deletion must remove saved config")
			}
		})
	}
}

//nolint:paralleltest // saved-workload fixtures use t.Setenv
func TestDefaultManager_deleteContainerWorkload_AuxiliaryDoesNotStopSupervisorOrDeleteParentConfig(t *testing.T) {
	isolateWorkloadStopState(t)
	ctx := context.Background()
	const parent = "parent-workload"
	const auxiliary = "parent-workload-inspector"
	parentConfig := &runner.RunConfig{BaseName: parent}
	persistStopRunConfig(t, parent, parentConfig)
	saved, err := runner.LoadState(ctx, parent)
	require.NoError(t, err)
	proxy := startVerifiedProxyProcess(t, parent)
	auxLabels := map[string]string{labels.LabelBaseName: auxiliary, labels.LabelAuxiliary: "true"}
	ctrl := gomock.NewController(t)
	rtMock := runtimeMocks.NewMockRuntime(ctrl)
	sm := statusMocks.NewMockStatusManager(ctrl)
	rtMock.EXPECT().GetWorkloadInfo(gomock.Any(), auxiliary).
		Return(runtime.ContainerInfo{Name: auxiliary, Labels: auxLabels}, nil)
	sm.EXPECT().SetWorkloadStatus(gomock.Any(), auxiliary, runtime.WorkloadStatusRemoving, "").Return(nil)
	rtMock.EXPECT().RemoveWorkload(gomock.Any(), auxiliary).Return(nil)
	rtMock.EXPECT().GetWorkloadInfo(gomock.Any(), auxiliary).Return(runtime.ContainerInfo{}, runtime.ErrWorkloadNotFound)
	sm.EXPECT().DeleteWorkloadStatus(gomock.Any(), auxiliary).Return(nil)
	manager := &DefaultManager{runtime: rtMock, statuses: sm}
	withPortFreer(func(context.Context, *runner.RunConfig) { t.Error("auxiliary deletion must not clean up parent proxy") })(manager)

	require.NoError(t, manager.deleteContainerWorkload(ctx, auxiliary, nil))
	alive, err := process.FindProcess(proxy.pid)
	require.NoError(t, err)
	assert.True(t, alive, "auxiliary deletion must not stop the parent supervisor")
	select {
	case <-proxy.exited:
		t.Error("auxiliary deletion stopped the parent supervisor")
	default:
	}
	after, err := runner.LoadState(ctx, parent)
	require.NoError(t, err)
	assert.Equal(t, saved, after)
}
