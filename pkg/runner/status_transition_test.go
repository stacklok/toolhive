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

	// The mock's SetWorkloadStatus returns nil, so the error is always nil here;
	// the expectations below assert what was written.
	markRunningOnStartup := func(r *Runner) {
		_, _ = r.transitionWorkloadStatus(context.Background(), keepStatusOnStartup, rt.WorkloadStatusRunning, "")
	}
	errRead := errors.New("status file locked")

	tests := []struct {
		name        string
		transition  func(r *Runner)
		current     rt.WorkloadStatus
		getErr      error
		wantStatus  rt.WorkloadStatus // empty means no write
		wantContext string
	}{
		{
			name: "unhealthy from running", transition: (*Runner).markRemoteUnhealthy,
			current: rt.WorkloadStatusRunning, wantStatus: rt.WorkloadStatusUnhealthy, wantContext: remoteUnhealthyStatusContext,
		},
		{
			name: "unhealthy from starting", transition: (*Runner).markRemoteUnhealthy,
			current: rt.WorkloadStatusStarting, wantStatus: rt.WorkloadStatusUnhealthy, wantContext: remoteUnhealthyStatusContext,
		},
		{
			name: "unhealthy overrides auth_retrying", transition: (*Runner).markRemoteUnhealthy,
			current: rt.WorkloadStatusAuthRetrying, wantStatus: rt.WorkloadStatusUnhealthy, wantContext: remoteUnhealthyStatusContext,
		},
		{
			name: "unhealthy when status unreadable", transition: (*Runner).markRemoteUnhealthy,
			getErr: errRead, wantStatus: rt.WorkloadStatusUnhealthy, wantContext: remoteUnhealthyStatusContext,
		},
		{name: "unhealthy again does not rewrite", transition: (*Runner).markRemoteUnhealthy, current: rt.WorkloadStatusUnhealthy},
		{name: "unhealthy keeps unauthenticated", transition: (*Runner).markRemoteUnhealthy, current: rt.WorkloadStatusUnauthenticated},
		{
			name: "recovered from unhealthy", transition: (*Runner).markRemoteRecovered,
			current: rt.WorkloadStatusUnhealthy, wantStatus: rt.WorkloadStatusRunning,
		},
		{name: "recovered keeps unauthenticated", transition: (*Runner).markRemoteRecovered, current: rt.WorkloadStatusUnauthenticated},
		{name: "recovered keeps running", transition: (*Runner).markRemoteRecovered, current: rt.WorkloadStatusRunning},
		{name: "recovered skips unreadable status", transition: (*Runner).markRemoteRecovered, getErr: errRead},
		{
			name: "startup marks starting running", transition: markRunningOnStartup,
			current: rt.WorkloadStatusStarting, wantStatus: rt.WorkloadStatusRunning,
		},
		{
			name: "startup marks missing workload running", transition: markRunningOnStartup,
			getErr: rt.ErrWorkloadNotFound, wantStatus: rt.WorkloadStatusRunning,
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
				statusManager.EXPECT().SetWorkloadStatus(gomock.Any(), testServerName, tt.wantStatus, tt.wantContext)
			}

			r := &Runner{Config: &RunConfig{BaseName: testServerName}, statusManager: statusManager}
			tt.transition(r)
		})
	}
}

func TestTransitionWorkloadStatusWithoutPreserve(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	statusManager := statusesmocks.NewMockStatusManager(ctrl)
	// No GetWorkload expectation: without a preserve func the status is not read.
	statusManager.EXPECT().SetWorkloadStatus(gomock.Any(), testServerName, rt.WorkloadStatusUnauthenticated, "token rejected")

	r := &Runner{Config: &RunConfig{BaseName: testServerName}, statusManager: statusManager}
	changed, err := r.transitionWorkloadStatus(context.Background(), nil, rt.WorkloadStatusUnauthenticated, "token rejected")
	require.NoError(t, err)
	require.True(t, changed)
}
