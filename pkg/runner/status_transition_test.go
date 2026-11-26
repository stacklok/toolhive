// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	statusesmocks "github.com/stacklok/toolhive/pkg/workloads/statuses/mocks"
)

func TestRunnerStatusTransitions(t *testing.T) {
	t.Parallel()

	markUnhealthy := func(r *Runner) error {
		r.markRemoteUnhealthy()
		return nil
	}
	markRunningOnStartup := func(r *Runner) error {
		_, err := r.transitionWorkloadStatus(context.Background(), keepStatusOnStartup, rt.WorkloadStatusRunning, "")
		return err
	}
	errRead := errors.New("status file locked")
	errWrite := errors.New("disk full")

	tests := []struct {
		name        string
		transition  func(r *Runner) error
		current     rt.WorkloadStatus
		getErr      error
		setErr      error
		wantStatus  rt.WorkloadStatus // empty means no write
		wantContext string
		wantErr     error
	}{
		{
			name: "unhealthy from running", transition: markUnhealthy,
			current: rt.WorkloadStatusRunning, wantStatus: rt.WorkloadStatusUnhealthy, wantContext: remoteUnhealthyStatusContext,
		},
		{
			name: "unhealthy from starting", transition: markUnhealthy,
			current: rt.WorkloadStatusStarting, wantStatus: rt.WorkloadStatusUnhealthy, wantContext: remoteUnhealthyStatusContext,
		},
		{
			name: "unhealthy overrides auth_retrying", transition: markUnhealthy,
			current: rt.WorkloadStatusAuthRetrying, wantStatus: rt.WorkloadStatusUnhealthy, wantContext: remoteUnhealthyStatusContext,
		},
		{name: "unhealthy again does not rewrite", transition: markUnhealthy, current: rt.WorkloadStatusUnhealthy},
		{name: "unhealthy keeps unauthenticated", transition: markUnhealthy, current: rt.WorkloadStatusUnauthenticated},
		{name: "unhealthy keeps stopping", transition: markUnhealthy, current: rt.WorkloadStatusStopping},
		{name: "unhealthy keeps stopped", transition: markUnhealthy, current: rt.WorkloadStatusStopped},
		{name: "unhealthy keeps removing", transition: markUnhealthy, current: rt.WorkloadStatusRemoving},
		{name: "unhealthy skips missing workload", transition: markUnhealthy, getErr: rt.ErrWorkloadNotFound},
		{name: "unhealthy skips unreadable status", transition: markUnhealthy, getErr: errRead},
		{
			name: "recovered from unhealthy", transition: (*Runner).markRemoteRecovered,
			current: rt.WorkloadStatusUnhealthy, wantStatus: rt.WorkloadStatusRunning,
		},
		{name: "recovered keeps unauthenticated", transition: (*Runner).markRemoteRecovered, current: rt.WorkloadStatusUnauthenticated},
		{name: "recovered keeps running", transition: (*Runner).markRemoteRecovered, current: rt.WorkloadStatusRunning},
		{name: "recovered keeps stopped", transition: (*Runner).markRemoteRecovered, current: rt.WorkloadStatusStopped},
		{name: "recovered skips missing workload", transition: (*Runner).markRemoteRecovered, getErr: rt.ErrWorkloadNotFound},
		{
			name: "recovered reports unreadable status", transition: (*Runner).markRemoteRecovered,
			getErr: errRead, wantErr: errRead,
		},
		{
			name: "recovered reports failed write", transition: (*Runner).markRemoteRecovered,
			current: rt.WorkloadStatusUnhealthy, setErr: errWrite, wantStatus: rt.WorkloadStatusRunning, wantErr: errWrite,
		},
		{
			name: "startup marks starting running", transition: markRunningOnStartup,
			current: rt.WorkloadStatusStarting, wantStatus: rt.WorkloadStatusRunning,
		},
		{
			name: "startup marks missing workload running", transition: markRunningOnStartup,
			getErr: rt.ErrWorkloadNotFound, wantStatus: rt.WorkloadStatusRunning,
		},
		{
			name: "startup marks unreadable status running", transition: markRunningOnStartup,
			getErr: errRead, wantStatus: rt.WorkloadStatusRunning,
		},
		{
			name: "startup reports failed write", transition: markRunningOnStartup,
			current: rt.WorkloadStatusStarting, setErr: errWrite, wantStatus: rt.WorkloadStatusRunning, wantErr: errWrite,
		},
		{name: "startup keeps unhealthy", transition: markRunningOnStartup, current: rt.WorkloadStatusUnhealthy},
		{name: "startup keeps unauthenticated", transition: markRunningOnStartup, current: rt.WorkloadStatusUnauthenticated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			statusManager := statusesmocks.NewMockStatusManager(ctrl)
			statusManager.EXPECT().GetWorkload(gomock.Any(), testServerName).
				Return(core.Workload{Name: testServerName, Status: tt.current}, tt.getErr)
			if tt.wantStatus != "" {
				statusManager.EXPECT().SetWorkloadStatus(gomock.Any(), testServerName, tt.wantStatus, tt.wantContext).
					Return(tt.setErr)
			}

			r := &Runner{Config: &RunConfig{BaseName: testServerName}, statusManager: statusManager}
			err := tt.transition(r)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestTransitionWorkloadStatusResult(t *testing.T) {
	t.Parallel()

	errWrite := errors.New("disk full")

	tests := []struct {
		name        string
		preserve    func(current rt.WorkloadStatus, readErr error) bool
		setErr      error
		wantChanged bool
		wantErr     error
	}{
		// Without a preserve func the status is not read, so there is no
		// GetWorkload expectation.
		{name: "without preserve writes unread", wantChanged: true},
		{name: "failed write reports not changed", preserve: keepStatusOnStartup, setErr: errWrite, wantErr: errWrite},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			statusManager := statusesmocks.NewMockStatusManager(ctrl)
			if tt.preserve != nil {
				statusManager.EXPECT().GetWorkload(gomock.Any(), testServerName).
					Return(core.Workload{Name: testServerName, Status: rt.WorkloadStatusStarting}, nil)
			}
			statusManager.EXPECT().SetWorkloadStatus(gomock.Any(), testServerName, rt.WorkloadStatusRunning, "").
				Return(tt.setErr)

			r := &Runner{Config: &RunConfig{BaseName: testServerName}, statusManager: statusManager}
			changed, err := r.transitionWorkloadStatus(context.Background(), tt.preserve, rt.WorkloadStatusRunning, "")
			require.Equal(t, tt.wantChanged, changed)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}
