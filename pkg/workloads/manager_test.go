package workloads

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	statusMocks "github.com/stacklok/toolhive/pkg/workloads/statuses/mocks"
)

func TestDefaultManager_ListWorkloadsInGroup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		groupName      string
		mockWorkloads  []core.Workload
		expectedNames  []string
		expectError    bool
		setupStatusMgr func(*statusMocks.MockStatusManager)
	}{
		{
			name:      "non existent group returns empty list",
			groupName: "non-group",
			mockWorkloads: []core.Workload{
				{Name: "workload1", Group: "other-group"},
				{Name: "workload2", Group: "another-group"},
			},
			expectedNames: []string{},
			expectError:   false,
			setupStatusMgr: func(sm *statusMocks.MockStatusManager) {
				sm.EXPECT().ListWorkloads(gomock.Any(), true, gomock.Any()).Return([]core.Workload{
					{Name: "workload1", Group: "other-group"},
					{Name: "workload2", Group: "another-group"},
				}, nil)

				sm.EXPECT().GetWorkload(gomock.Any(), gomock.Any()).Return(core.Workload{
					Name:   "remote-workload",
					Status: runtime.WorkloadStatusRunning,
				}, nil).AnyTimes()
			},
		},
		{
			name:      "multiple workloads in group",
			groupName: "test-group",
			mockWorkloads: []core.Workload{
				{Name: "workload1", Group: "test-group"},
				{Name: "workload2", Group: "other-group"},
				{Name: "workload3", Group: "test-group"},
				{Name: "workload4", Group: "test-group"},
			},
			expectedNames: []string{"workload1", "workload3", "workload4"},
			expectError:   false,
			setupStatusMgr: func(sm *statusMocks.MockStatusManager) {
				sm.EXPECT().ListWorkloads(gomock.Any(), true, gomock.Any()).Return([]core.Workload{
					{Name: "workload1", Group: "test-group"},
					{Name: "workload2", Group: "other-group"},
					{Name: "workload3", Group: "test-group"},
					{Name: "workload4", Group: "test-group"},
				}, nil)

				sm.EXPECT().GetWorkload(gomock.Any(), gomock.Any()).Return(core.Workload{
					Name:   "remote-workload",
					Status: runtime.WorkloadStatusRunning,
				}, nil).AnyTimes()
			},
		},
		{
			name:      "workloads with empty group names",
			groupName: "",
			mockWorkloads: []core.Workload{
				{Name: "workload1", Group: ""},
				{Name: "workload2", Group: "test-group"},
				{Name: "workload3", Group: ""},
			},
			expectedNames: []string{"workload1", "workload3"},
			expectError:   false,
			setupStatusMgr: func(sm *statusMocks.MockStatusManager) {
				sm.EXPECT().ListWorkloads(gomock.Any(), true, gomock.Any()).Return([]core.Workload{
					{Name: "workload1", Group: ""},
					{Name: "workload2", Group: "test-group"},
					{Name: "workload3", Group: ""},
				}, nil)

				sm.EXPECT().GetWorkload(gomock.Any(), gomock.Any()).Return(core.Workload{
					Name:   "remote-workload",
					Status: runtime.WorkloadStatusRunning,
				}, nil).AnyTimes()
			},
		},
		{
			name:      "includes stopped workloads",
			groupName: "test-group",
			mockWorkloads: []core.Workload{
				{Name: "running-workload", Group: "test-group", Status: runtime.WorkloadStatusRunning},
				{Name: "stopped-workload", Group: "test-group", Status: runtime.WorkloadStatusStopped},
				{Name: "other-group-workload", Group: "other-group", Status: runtime.WorkloadStatusRunning},
			},
			expectedNames: []string{"running-workload", "stopped-workload"},
			expectError:   false,
			setupStatusMgr: func(sm *statusMocks.MockStatusManager) {
				sm.EXPECT().ListWorkloads(gomock.Any(), true, gomock.Any()).Return([]core.Workload{
					{Name: "running-workload", Group: "test-group", Status: runtime.WorkloadStatusRunning},
					{Name: "stopped-workload", Group: "test-group", Status: runtime.WorkloadStatusStopped},
					{Name: "other-group-workload", Group: "other-group", Status: runtime.WorkloadStatusRunning},
				}, nil)

				sm.EXPECT().GetWorkload(gomock.Any(), gomock.Any()).Return(core.Workload{
					Name:   "remote-workload",
					Status: runtime.WorkloadStatusRunning,
				}, nil).AnyTimes()
			},
		},
		{
			name:          "error from ListWorkloads propagated",
			groupName:     "test-group",
			expectedNames: nil,
			expectError:   true,
			setupStatusMgr: func(sm *statusMocks.MockStatusManager) {
				sm.EXPECT().ListWorkloads(gomock.Any(), true, gomock.Any()).Return(nil, assert.AnError)
			},
		},
		{
			name:          "no workloads",
			groupName:     "test-group",
			mockWorkloads: []core.Workload{},
			expectedNames: []string{},
			expectError:   false,
			setupStatusMgr: func(sm *statusMocks.MockStatusManager) {
				sm.EXPECT().ListWorkloads(gomock.Any(), true, gomock.Any()).Return([]core.Workload{}, nil)

				sm.EXPECT().GetWorkload(gomock.Any(), gomock.Any()).Return(core.Workload{
					Name:   "remote-workload",
					Status: runtime.WorkloadStatusRunning,
				}, nil).AnyTimes()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStatusMgr := statusMocks.NewMockStatusManager(ctrl)
			tt.setupStatusMgr(mockStatusMgr)

			manager := &defaultManager{
				runtime:  nil, // Not needed for this test
				statuses: mockStatusMgr,
			}

			ctx := context.Background()
			result, err := manager.ListWorkloadsInGroup(ctx, tt.groupName)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "failed to list workloads")
				return
			}

			require.NoError(t, err)
			assert.ElementsMatch(t, tt.expectedNames, result)
		})
	}
}
