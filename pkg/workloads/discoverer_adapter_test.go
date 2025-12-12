package workloads

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	vmcpworkloads "github.com/stacklok/toolhive/pkg/vmcp/workloads"
	statusMocks "github.com/stacklok/toolhive/pkg/workloads/statuses/mocks"
)

func TestNewDiscovererAdapter(t *testing.T) {
	t.Parallel()

	manager := &DefaultManager{}
	adapter := NewDiscovererAdapter(manager)

	require.NotNil(t, adapter)
}

func TestDiscovererAdapter_ListWorkloadsInGroup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		groupName      string
		setupMocks     func(*statusMocks.MockStatusManager)
		expectedResult []vmcpworkloads.TypedWorkload
		expectError    bool
	}{
		{
			name:      "successful listing with multiple workloads",
			groupName: "test-group",
			setupMocks: func(sm *statusMocks.MockStatusManager) {
				sm.EXPECT().ListWorkloads(gomock.Any(), true, gomock.Any()).Return([]core.Workload{
					{
						Name:  "workload1",
						Group: "test-group",
					},
					{
						Name:  "workload2",
						Group: "other-group",
					},
					{
						Name:  "workload3",
						Group: "test-group",
					},
				}, nil)
				sm.EXPECT().GetWorkload(gomock.Any(), gomock.Any()).Return(core.Workload{
					Name:   "remote-workload",
					Status: runtime.WorkloadStatusRunning,
				}, nil).AnyTimes()
			},
			expectedResult: []vmcpworkloads.TypedWorkload{
				{
					Name: "workload1",
					Type: vmcpworkloads.WorkloadTypeMCPServer,
				},
				{
					Name: "workload3",
					Type: vmcpworkloads.WorkloadTypeMCPServer,
				},
			},
			expectError: false,
		},
		{
			name:      "empty group returns empty list",
			groupName: "empty-group",
			setupMocks: func(sm *statusMocks.MockStatusManager) {
				sm.EXPECT().ListWorkloads(gomock.Any(), true, gomock.Any()).Return([]core.Workload{
					{Name: "workload1", Group: "other-group"},
				}, nil)
				sm.EXPECT().GetWorkload(gomock.Any(), gomock.Any()).Return(core.Workload{
					Name:   "remote-workload",
					Status: runtime.WorkloadStatusRunning,
				}, nil).AnyTimes()
			},
			expectedResult: []vmcpworkloads.TypedWorkload{},
			expectError:    false,
		},
		{
			name:      "error from manager propagates",
			groupName: "test-group",
			setupMocks: func(sm *statusMocks.MockStatusManager) {
				sm.EXPECT().ListWorkloads(gomock.Any(), true, gomock.Any()).Return(nil, assert.AnError)
			},
			expectedResult: nil,
			expectError:    true,
		},
		{
			name:      "all workloads converted to MCPServer type",
			groupName: "test-group",
			setupMocks: func(sm *statusMocks.MockStatusManager) {
				sm.EXPECT().ListWorkloads(gomock.Any(), true, gomock.Any()).Return([]core.Workload{
					{
						Name:  "server1",
						Group: "test-group",
					},
					{
						Name:  "server2",
						Group: "test-group",
					},
					{
						Name:  "server3",
						Group: "test-group",
					},
				}, nil)
				sm.EXPECT().GetWorkload(gomock.Any(), gomock.Any()).Return(core.Workload{
					Name:   "remote-workload",
					Status: runtime.WorkloadStatusRunning,
				}, nil).AnyTimes()
			},
			expectedResult: []vmcpworkloads.TypedWorkload{
				{
					Name: "server1",
					Type: vmcpworkloads.WorkloadTypeMCPServer,
				},
				{
					Name: "server2",
					Type: vmcpworkloads.WorkloadTypeMCPServer,
				},
				{
					Name: "server3",
					Type: vmcpworkloads.WorkloadTypeMCPServer,
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStatusMgr := statusMocks.NewMockStatusManager(ctrl)
			tt.setupMocks(mockStatusMgr)

			manager := &DefaultManager{
				statuses: mockStatusMgr,
			}

			adapter := NewDiscovererAdapter(manager)

			ctx := context.Background()
			result, err := adapter.ListWorkloadsInGroup(ctx, tt.groupName)

			if tt.expectError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)

			// Verify the count matches
			assert.Len(t, result, len(tt.expectedResult))

			// Verify each workload has correct type
			for i, expected := range tt.expectedResult {
				assert.Equal(t, expected.Name, result[i].Name)
				assert.Equal(t, vmcpworkloads.WorkloadTypeMCPServer, result[i].Type,
					"All CLI workloads should be of type WorkloadTypeMCPServer")
			}
		})
	}
}

func TestDiscovererAdapter_ListWorkloadsInGroup_TypeConsistency(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStatusMgr := statusMocks.NewMockStatusManager(ctrl)
	mockStatusMgr.EXPECT().ListWorkloads(gomock.Any(), true, gomock.Any()).Return([]core.Workload{
		{
			Name:  "workload1",
			Group: "test-group",
		},
		{
			Name:  "workload2",
			Group: "test-group",
		},
	}, nil)
	mockStatusMgr.EXPECT().GetWorkload(gomock.Any(), gomock.Any()).Return(core.Workload{
		Name:   "remote-workload",
		Status: runtime.WorkloadStatusRunning,
	}, nil).AnyTimes()

	manager := &DefaultManager{
		statuses: mockStatusMgr,
	}

	adapter := NewDiscovererAdapter(manager)

	ctx := context.Background()
	result, err := adapter.ListWorkloadsInGroup(ctx, "test-group")

	require.NoError(t, err)
	require.Len(t, result, 2)

	// All workloads must be MCPServer type in CLI context
	for _, workload := range result {
		assert.Equal(t, vmcpworkloads.WorkloadTypeMCPServer, workload.Type,
			"DiscovererAdapter should always return WorkloadTypeMCPServer for CLI context")
	}
}

func TestDiscovererAdapter_GetWorkloadAsVMCPBackend(t *testing.T) {
	t.Parallel()

	t.Run("delegates to manager correctly", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockStatusMgr := statusMocks.NewMockStatusManager(ctrl)

		manager := &DefaultManager{
			statuses: mockStatusMgr,
		}

		adapter := NewDiscovererAdapter(manager)

		ctx := context.Background()
		workload := vmcpworkloads.TypedWorkload{
			Name: "test-workload",
			Type: vmcpworkloads.WorkloadTypeMCPServer,
		}

		// GetWorkloadAsVMCPBackend will attempt to get workload info which will fail
		// because we haven't set up the full runtime. This is expected behavior.
		mockStatusMgr.EXPECT().GetWorkload(gomock.Any(), "test-workload").Return(core.Workload{
			Name:   "test-workload",
			Status: runtime.WorkloadStatusRunning,
		}, nil)

		result, err := adapter.GetWorkloadAsVMCPBackend(ctx, workload)

		// The call will fail at some point due to incomplete setup, but we verify
		// that the adapter correctly extracts the Name from TypedWorkload
		// and passes it to the manager
		if err != nil {
			// Expected - manager's GetWorkloadAsVMCPBackend requires full setup
			assert.Nil(t, result)
		}
	})

	t.Run("uses workload name from TypedWorkload", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockStatusMgr := statusMocks.NewMockStatusManager(ctrl)

		// Verify the correct name is passed to GetWorkload
		mockStatusMgr.EXPECT().GetWorkload(gomock.Any(), "specific-workload-name").Return(core.Workload{
			Name:   "specific-workload-name",
			Status: runtime.WorkloadStatusRunning,
		}, nil)

		manager := &DefaultManager{
			statuses: mockStatusMgr,
		}

		adapter := NewDiscovererAdapter(manager)

		ctx := context.Background()
		workload := vmcpworkloads.TypedWorkload{
			Name: "specific-workload-name",
			Type: vmcpworkloads.WorkloadTypeMCPServer,
		}

		// We don't care about the result, just that the correct name was used
		_, _ = adapter.GetWorkloadAsVMCPBackend(ctx, workload)
	})

	t.Run("ignores workload type parameter", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockStatusMgr := statusMocks.NewMockStatusManager(ctrl)

		// Even with a different type, the adapter should still work
		// because CLI context only has MCPServer workloads
		mockStatusMgr.EXPECT().GetWorkload(gomock.Any(), "test-workload").Return(core.Workload{
			Name:   "test-workload",
			Status: runtime.WorkloadStatusRunning,
		}, nil)

		manager := &DefaultManager{
			statuses: mockStatusMgr,
		}

		adapter := NewDiscovererAdapter(manager)

		ctx := context.Background()
		// Pass MCPRemoteProxy type - adapter should ignore it
		workload := vmcpworkloads.TypedWorkload{
			Name: "test-workload",
			Type: vmcpworkloads.WorkloadTypeMCPRemoteProxy,
		}

		// The adapter ignores the type and just uses the name
		_, _ = adapter.GetWorkloadAsVMCPBackend(ctx, workload)
	})
}
