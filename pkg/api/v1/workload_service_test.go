package v1

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	groupsmocks "github.com/stacklok/toolhive/pkg/groups/mocks"
	workloadsmocks "github.com/stacklok/toolhive/pkg/workloads/mocks"
)

func TestWorkloadService_GetWorkloadNamesFromRequest(t *testing.T) {
	t.Parallel()

	t.Run("with names", func(t *testing.T) {
		t.Parallel()

		service := &WorkloadService{}

		req := bulkOperationRequest{
			Names: []string{"workload1", "workload2"},
		}

		result, err := service.GetWorkloadNamesFromRequest(context.Background(), req)

		require.NoError(t, err)
		assert.Equal(t, []string{"workload1", "workload2"}, result)
	})

	t.Run("with group", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockGroupManager := groupsmocks.NewMockManager(ctrl)
		mockGroupManager.EXPECT().
			Exists(gomock.Any(), "test-group").
			Return(true, nil)

		mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
		mockWorkloadManager.EXPECT().
			ListWorkloadsInGroup(gomock.Any(), "test-group").
			Return([]string{"workload1", "workload2"}, nil)

		service := &WorkloadService{
			groupManager:    mockGroupManager,
			workloadManager: mockWorkloadManager,
		}

		req := bulkOperationRequest{
			Group: "test-group",
		}

		result, err := service.GetWorkloadNamesFromRequest(context.Background(), req)

		require.NoError(t, err)
		assert.Equal(t, []string{"workload1", "workload2"}, result)
	})

	t.Run("invalid group name", func(t *testing.T) {
		t.Parallel()

		service := &WorkloadService{}

		req := bulkOperationRequest{
			Group: "invalid-group-name-with-special-chars!@#",
		}

		result, err := service.GetWorkloadNamesFromRequest(context.Background(), req)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "invalid group name")
	})

	t.Run("group does not exist", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockGroupManager := groupsmocks.NewMockManager(ctrl)
		mockGroupManager.EXPECT().
			Exists(gomock.Any(), "non-existent-group").
			Return(false, nil)

		service := &WorkloadService{
			groupManager: mockGroupManager,
		}

		req := bulkOperationRequest{
			Group: "non-existent-group",
		}

		result, err := service.GetWorkloadNamesFromRequest(context.Background(), req)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "group 'non-existent-group' does not exist")
	})

	t.Run("list workloads error", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockGroupManager := groupsmocks.NewMockManager(ctrl)
		mockGroupManager.EXPECT().
			Exists(gomock.Any(), "test-group").
			Return(true, nil)

		mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
		mockWorkloadManager.EXPECT().
			ListWorkloadsInGroup(gomock.Any(), "test-group").
			Return(nil, errors.New("database error"))

		service := &WorkloadService{
			groupManager:    mockGroupManager,
			workloadManager: mockWorkloadManager,
		}

		req := bulkOperationRequest{
			Group: "test-group",
		}

		result, err := service.GetWorkloadNamesFromRequest(context.Background(), req)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "failed to list workloads in group")
	})
}

func TestNewWorkloadService(t *testing.T) {
	t.Parallel()

	service := NewWorkloadService(nil, nil, nil, false)
	require.NotNil(t, service)
}
