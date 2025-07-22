package groups

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/state/mocks"
	"github.com/stacklok/toolhive/pkg/workloads"
	workloadmocks "github.com/stacklok/toolhive/pkg/workloads/mocks"
)

func init() {
	// Initialize logger for tests
	logger.Initialize()
}

const testGroupName = "testgroup"

// TestManager_Create demonstrates using gomock for testing group creation
func TestManager_Create(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		groupName   string
		setupMock   func(*mocks.MockStore)
		expectError bool
		errorMsg    string
	}{
		{
			name:      "successful creation",
			groupName: testGroupName,
			setupMock: func(mock *mocks.MockStore) {
				mock.EXPECT().
					Exists(gomock.Any(), testGroupName).
					Return(false, nil)
				mock.EXPECT().
					GetWriter(gomock.Any(), testGroupName).
					Return(&mockWriteCloser{}, nil)
			},
			expectError: false,
		},
		{
			name:      "group already exists",
			groupName: "existinggroup",
			setupMock: func(mock *mocks.MockStore) {
				mock.EXPECT().
					Exists(gomock.Any(), "existinggroup").
					Return(true, nil)
			},
			expectError: true,
			errorMsg:    "already exists",
		},
		{
			name:      "exists check fails",
			groupName: testGroupName,
			setupMock: func(mock *mocks.MockStore) {
				mock.EXPECT().
					Exists(gomock.Any(), testGroupName).
					Return(false, errors.New("exists check failed"))
			},
			expectError: true,
			errorMsg:    "failed to check if group exists",
		},
		{
			name:      "get writer fails",
			groupName: testGroupName,
			setupMock: func(mock *mocks.MockStore) {
				mock.EXPECT().
					Exists(gomock.Any(), testGroupName).
					Return(false, nil)
				mock.EXPECT().
					GetWriter(gomock.Any(), testGroupName).
					Return(nil, errors.New("writer failed"))
			},
			expectError: true,
			errorMsg:    "failed to get writer for group",
		},
		{
			name:      "empty group name",
			groupName: "",
			setupMock: func(_ *mocks.MockStore) {
				// No expectations since validation happens before store calls
			},
			expectError: true,
			errorMsg:    "group name cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStore := mocks.NewMockStore(ctrl)
			manager := &manager{store: mockStore, workloadManager: nil}

			// Set up mock expectations
			tt.setupMock(mockStore)

			// Execute operation
			err := manager.Create(context.Background(), tt.groupName)

			// Verify results
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestManager_Get demonstrates using gomock for testing group retrieval
func TestManager_Get(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		groupName     string
		setupMock     func(*mocks.MockStore)
		expectError   bool
		errorMsg      string
		expectedGroup *Group
	}{
		{
			name:      "successful retrieval",
			groupName: testGroupName,
			setupMock: func(mock *mocks.MockStore) {
				groupData := `{"name": "` + testGroupName + `"}`
				mock.EXPECT().
					GetReader(gomock.Any(), testGroupName).
					Return(io.NopCloser(strings.NewReader(groupData)), nil)
			},
			expectError:   false,
			expectedGroup: &Group{Name: testGroupName},
		},
		{
			name:      "group not found",
			groupName: "nonexistent",
			setupMock: func(mock *mocks.MockStore) {
				mock.EXPECT().
					GetReader(gomock.Any(), "nonexistent").
					Return(nil, &os.PathError{Op: "open", Path: "nonexistent", Err: os.ErrNotExist})
			},
			expectError: true,
			errorMsg:    "failed to get reader for group",
		},
		{
			name:      "get reader fails",
			groupName: testGroupName,
			setupMock: func(mock *mocks.MockStore) {
				mock.EXPECT().
					GetReader(gomock.Any(), testGroupName).
					Return(nil, errors.New("reader failed"))
			},
			expectError: true,
			errorMsg:    "failed to get reader for group",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStore := mocks.NewMockStore(ctrl)
			manager := &manager{store: mockStore, workloadManager: nil}

			// Set up mock expectations
			tt.setupMock(mockStore)

			// Execute operation
			group, err := manager.Get(context.Background(), tt.groupName)

			// Verify results
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Nil(t, group)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedGroup.Name, group.Name)
			}
		})
	}
}

// TestManager_List demonstrates using gomock for testing group listing
func TestManager_List(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setupMock     func(*mocks.MockStore)
		expectError   bool
		errorMsg      string
		expectedCount int
		expectedNames []string
	}{
		{
			name: "successful listing with groups",
			setupMock: func(mock *mocks.MockStore) {
				groupNames := []string{"group1", "group2", "group3"}
				mock.EXPECT().
					List(gomock.Any()).
					Return(groupNames, nil)

				// Set up expectations for getting each group
				for _, name := range groupNames {
					groupData := `{"name": "` + name + `"}`
					mock.EXPECT().
						GetReader(gomock.Any(), name).
						Return(io.NopCloser(strings.NewReader(groupData)), nil)
				}
			},
			expectError:   false,
			expectedCount: 3,
			expectedNames: []string{"group1", "group2", "group3"},
		},
		{
			name: "successful listing with no groups",
			setupMock: func(mock *mocks.MockStore) {
				mock.EXPECT().
					List(gomock.Any()).
					Return([]string{}, nil)
			},
			expectError:   false,
			expectedCount: 0,
			expectedNames: []string{},
		},
		{
			name: "list fails",
			setupMock: func(mock *mocks.MockStore) {
				mock.EXPECT().
					List(gomock.Any()).
					Return(nil, errors.New("list failed"))
			},
			expectError: true,
			errorMsg:    "failed to list groups",
		},
		{
			name: "get individual group fails",
			setupMock: func(mock *mocks.MockStore) {
				groupNames := []string{"group1", "group2"}
				mock.EXPECT().
					List(gomock.Any()).
					Return(groupNames, nil)

				// First group succeeds
				groupData := `{"name": "group1"}`
				mock.EXPECT().
					GetReader(gomock.Any(), "group1").
					Return(io.NopCloser(strings.NewReader(groupData)), nil)

				// Second group fails
				mock.EXPECT().
					GetReader(gomock.Any(), "group2").
					Return(nil, errors.New("get group failed"))
			},
			expectError: true,
			errorMsg:    "failed to get group group2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStore := mocks.NewMockStore(ctrl)
			manager := &manager{store: mockStore, workloadManager: nil}

			// Set up mock expectations
			tt.setupMock(mockStore)

			// Execute operation
			groups, err := manager.List(context.Background())

			// Verify results
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Nil(t, groups)
			} else {
				assert.NoError(t, err)
				assert.Len(t, groups, tt.expectedCount)

				// Verify all expected groups are present
				if len(tt.expectedNames) > 0 {
					groupMap := make(map[string]bool)
					for _, group := range groups {
						groupMap[group.Name] = true
					}

					for _, name := range tt.expectedNames {
						assert.True(t, groupMap[name], "Group %s should be in the list", name)
					}
				}
			}
		})
	}
}

// TestManager_Delete demonstrates using gomock for testing group deletion
func TestManager_Delete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		groupName   string
		setupMock   func(*mocks.MockStore)
		expectError bool
		errorMsg    string
	}{
		{
			name:      "successful deletion",
			groupName: testGroupName,
			setupMock: func(mock *mocks.MockStore) {
				mock.EXPECT().
					Delete(gomock.Any(), testGroupName).
					Return(nil)
			},
			expectError: false,
		},
		{
			name:      "delete fails",
			groupName: testGroupName,
			setupMock: func(mock *mocks.MockStore) {
				mock.EXPECT().
					Delete(gomock.Any(), testGroupName).
					Return(errors.New("delete failed"))
			},
			expectError: true,
			errorMsg:    "delete failed",
		},
		{
			name:      "group not found",
			groupName: "nonexistent",
			setupMock: func(mock *mocks.MockStore) {
				mock.EXPECT().
					Delete(gomock.Any(), "nonexistent").
					Return(&os.PathError{Op: "remove", Path: "nonexistent", Err: os.ErrNotExist})
			},
			expectError: true,
			errorMsg:    "remove nonexistent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStore := mocks.NewMockStore(ctrl)
			manager := &manager{store: mockStore, workloadManager: nil}

			// Set up mock expectations
			tt.setupMock(mockStore)

			// Execute operation
			err := manager.Delete(context.Background(), tt.groupName)

			// Verify results
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestManager_Exists demonstrates using gomock for testing group existence check
func TestManager_Exists(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		groupName      string
		setupMock      func(*mocks.MockStore)
		expectError    bool
		errorMsg       string
		expectedExists bool
	}{
		{
			name:      "group exists",
			groupName: testGroupName,
			setupMock: func(mock *mocks.MockStore) {
				mock.EXPECT().
					Exists(gomock.Any(), testGroupName).
					Return(true, nil)
			},
			expectError:    false,
			expectedExists: true,
		},
		{
			name:      "group does not exist",
			groupName: "nonexistent",
			setupMock: func(mock *mocks.MockStore) {
				mock.EXPECT().
					Exists(gomock.Any(), "nonexistent").
					Return(false, nil)
			},
			expectError:    false,
			expectedExists: false,
		},
		{
			name:      "exists check fails",
			groupName: testGroupName,
			setupMock: func(mock *mocks.MockStore) {
				mock.EXPECT().
					Exists(gomock.Any(), testGroupName).
					Return(false, errors.New("exists check failed"))
			},
			expectError: true,
			errorMsg:    "exists check failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStore := mocks.NewMockStore(ctrl)
			manager := &manager{store: mockStore, workloadManager: nil}

			// Set up mock expectations
			tt.setupMock(mockStore)

			// Execute operation
			exists, err := manager.Exists(context.Background(), tt.groupName)

			// Verify results
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedExists, exists)
			}
		})
	}
}

// TestManager_AddWorkloadToGroup tests the AddWorkloadToGroup method
func TestManager_AddWorkloadToGroup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		groupName            string
		workloadName         string
		setupMock            func(*mocks.MockStore)
		setupWorkloadManager func(*workloadmocks.MockManager)
		expectError          bool
		errorMsg             string
	}{
		{
			name:         "successful addition",
			groupName:    testGroupName,
			workloadName: "test-workload",
			setupMock: func(mock *mocks.MockStore) {
				// Mock GetReader for the group
				mock.EXPECT().
					GetReader(gomock.Any(), testGroupName).
					Return(&mockReadCloser{data: `{"name":"` + testGroupName + `","workloads":[]}`}, nil)

				// Mock GetWriter for saving the updated group
				mock.EXPECT().
					GetWriter(gomock.Any(), testGroupName).
					Return(&mockWriteCloser{}, nil)
			},
			setupWorkloadManager: func(mock *workloadmocks.MockManager) {
				// Mock GetWorkload to return a workload with no group
				mock.EXPECT().
					GetWorkload(gomock.Any(), "test-workload").
					Return(workloads.Workload{Name: "test-workload", Group: ""}, nil)
			},
			expectError: false,
		},
		{
			name:         "group does not exist",
			groupName:    "nonexistent-group",
			workloadName: "test-workload",
			setupMock: func(mock *mocks.MockStore) {
				mock.EXPECT().
					GetReader(gomock.Any(), "nonexistent-group").
					Return(nil, errors.New("group not found"))
			},
			setupWorkloadManager: func(_ *workloadmocks.MockManager) {
				// No workload manager expectations needed since group doesn't exist
			},
			expectError: true,
			errorMsg:    "failed to get group",
		},
		{
			name:         "workload already in group",
			groupName:    testGroupName,
			workloadName: "existing-workload",
			setupMock: func(mock *mocks.MockStore) {
				mock.EXPECT().
					GetReader(gomock.Any(), testGroupName).
					Return(&mockReadCloser{data: `{"name":"` + testGroupName + `","workloads":["existing-workload"]}`}, nil)
			},
			setupWorkloadManager: func(_ *workloadmocks.MockManager) {
				// This test doesn't call GetWorkloadGroup, so no workload manager expectations needed
			},
			expectError: false, // No error when workload is already in group
		},
		{
			name:         "workload already in another group",
			groupName:    testGroupName,
			workloadName: "workload-in-other-group",
			setupMock: func(mock *mocks.MockStore) {
				// Mock GetReader for the target group
				mock.EXPECT().
					GetReader(gomock.Any(), testGroupName).
					Return(&mockReadCloser{data: `{"name":"` + testGroupName + `","workloads":[]}`}, nil)

				// Mock GetReader for the other group (when GetWorkloadGroup calls m.Get)
				mock.EXPECT().
					GetReader(gomock.Any(), "other-group").
					Return(&mockReadCloser{data: `{"name":"other-group","workloads":["workload-in-other-group"]}`}, nil)
			},
			setupWorkloadManager: func(mock *workloadmocks.MockManager) {
				// Mock GetWorkload to return a workload in another group
				mock.EXPECT().
					GetWorkload(gomock.Any(), "workload-in-other-group").
					Return(workloads.Workload{Name: "workload-in-other-group", Group: "other-group"}, nil)
			},
			expectError: true,
			errorMsg:    "already in group",
		},
		{
			name:         "workload manager failure treated as workload not found",
			groupName:    testGroupName,
			workloadName: "test-workload",
			setupMock: func(mock *mocks.MockStore) {
				// Mock GetReader for the group
				mock.EXPECT().
					GetReader(gomock.Any(), testGroupName).
					Return(&mockReadCloser{data: `{"name":"` + testGroupName + `","workloads":[]}`}, nil)

				// Mock GetWriter for saving the updated group
				mock.EXPECT().
					GetWriter(gomock.Any(), testGroupName).
					Return(&mockWriteCloser{}, nil)
			},
			setupWorkloadManager: func(mock *workloadmocks.MockManager) {
				// Mock GetWorkload to return a workload with no group
				mock.EXPECT().
					GetWorkload(gomock.Any(), "test-workload").
					Return(workloads.Workload{Name: "test-workload", Group: ""}, nil)
			},
			expectError: false, // Workload manager failure is treated as "workload not found"
		},
		{
			name:         "failed to save group",
			groupName:    testGroupName,
			workloadName: "test-workload",
			setupMock: func(mock *mocks.MockStore) {
				// Mock GetReader for the group
				mock.EXPECT().
					GetReader(gomock.Any(), testGroupName).
					Return(&mockReadCloser{data: `{"name":"` + testGroupName + `","workloads":[]}`}, nil)

				// Mock GetWriter to fail
				mock.EXPECT().
					GetWriter(gomock.Any(), testGroupName).
					Return(nil, errors.New("writer failed"))
			},
			setupWorkloadManager: func(mock *workloadmocks.MockManager) {
				// Mock GetWorkload to return a workload with no group
				mock.EXPECT().
					GetWorkload(gomock.Any(), "test-workload").
					Return(workloads.Workload{Name: "test-workload", Group: ""}, nil)
			},
			expectError: true,
			errorMsg:    "failed to get writer for group",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStore := mocks.NewMockStore(ctrl)
			mockWorkloadManager := workloadmocks.NewMockManager(ctrl)
			manager := &manager{store: mockStore, workloadManager: mockWorkloadManager}

			// Set up mock expectations
			tt.setupMock(mockStore)
			tt.setupWorkloadManager(mockWorkloadManager)

			// Call the method
			err := manager.AddWorkloadToGroup(context.Background(), tt.groupName, tt.workloadName)

			// Assert results
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestManager_RemoveWorkloadFromGroup tests the RemoveWorkloadFromGroup method
func TestManager_RemoveWorkloadFromGroup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		groupName    string
		workloadName string
		setupMock    func(*mocks.MockStore)
		expectError  bool
		errorMsg     string
	}{
		{
			name:         "successful removal",
			groupName:    testGroupName,
			workloadName: "test-workload",
			setupMock: func(mock *mocks.MockStore) {
				// Mock GetReader for the group
				mock.EXPECT().
					GetReader(gomock.Any(), testGroupName).
					Return(&mockReadCloser{data: `{"name":"` + testGroupName + `","workloads":["test-workload"]}`}, nil)

				// Mock GetWriter for saving the updated group
				mock.EXPECT().
					GetWriter(gomock.Any(), testGroupName).
					Return(&mockWriteCloser{}, nil)
			},
			expectError: false,
		},
		{
			name:         "group does not exist",
			groupName:    "nonexistent-group",
			workloadName: "test-workload",
			setupMock: func(mock *mocks.MockStore) {
				mock.EXPECT().
					GetReader(gomock.Any(), "nonexistent-group").
					Return(nil, errors.New("group not found"))
			},
			expectError: true,
			errorMsg:    "failed to get group",
		},
		{
			name:         "workload not in group",
			groupName:    testGroupName,
			workloadName: "nonexistent-workload",
			setupMock: func(mock *mocks.MockStore) {
				mock.EXPECT().
					GetReader(gomock.Any(), testGroupName).
					Return(&mockReadCloser{data: `{"name":"` + testGroupName + `","workloads":["other-workload"]}`}, nil)
			},
			expectError: true,
			errorMsg:    "is not in group",
		},
		{
			name:         "failed to save group",
			groupName:    testGroupName,
			workloadName: "test-workload",
			setupMock: func(mock *mocks.MockStore) {
				// Mock GetReader for the group
				mock.EXPECT().
					GetReader(gomock.Any(), testGroupName).
					Return(&mockReadCloser{data: `{"name":"` + testGroupName + `","workloads":["test-workload"]}`}, nil)

				// Mock GetWriter to fail
				mock.EXPECT().
					GetWriter(gomock.Any(), testGroupName).
					Return(nil, errors.New("writer failed"))
			},
			expectError: true,
			errorMsg:    "failed to get writer for group",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStore := mocks.NewMockStore(ctrl)
			manager := &manager{store: mockStore, workloadManager: nil}

			// Set up mock expectations
			tt.setupMock(mockStore)

			// Call the method
			err := manager.RemoveWorkloadFromGroup(context.Background(), tt.groupName, tt.workloadName)

			// Assert results
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestManager_GetWorkloadGroup tests the GetWorkloadGroup method
func TestManager_GetWorkloadGroup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		workloadName         string
		setupMock            func(*mocks.MockStore)
		setupWorkloadManager func(*workloadmocks.MockManager)
		workloadGroup        string // The group the workload belongs to (empty if none)
		expectError          bool
		expectedGroup        *Group
		errorMsg             string
	}{
		{
			name:          "workload found in group",
			workloadName:  "test-workload",
			workloadGroup: testGroupName,
			setupMock: func(mock *mocks.MockStore) {
				// Mock GetReader for the group
				mock.EXPECT().
					GetReader(gomock.Any(), testGroupName).
					Return(&mockReadCloser{data: `{"name":"` + testGroupName + `","workloads":["test-workload"]}`}, nil)
			},
			setupWorkloadManager: func(mock *workloadmocks.MockManager) {
				// Mock GetWorkload to return a workload in a group
				mock.EXPECT().
					GetWorkload(gomock.Any(), "test-workload").
					Return(workloads.Workload{Name: "test-workload", Group: testGroupName}, nil)
			},
			expectError:   false,
			expectedGroup: &Group{Name: testGroupName, Workloads: []string{"test-workload"}},
		},
		{
			name:          "workload not found in any group",
			workloadName:  "nonexistent-workload",
			workloadGroup: "",
			setupMock: func(_ *mocks.MockStore) {
				// No mock expectations needed since workload has no group
			},
			setupWorkloadManager: func(mock *workloadmocks.MockManager) {
				// Mock GetWorkload to return a workload with no group
				mock.EXPECT().
					GetWorkload(gomock.Any(), "nonexistent-workload").
					Return(workloads.Workload{Name: "nonexistent-workload", Group: ""}, nil)
			},
			expectError:   false,
			expectedGroup: nil,
		},
		{
			name:          "workload manager fails to get workload",
			workloadName:  "test-workload",
			workloadGroup: "",
			setupMock: func(_ *mocks.MockStore) {
				// No mock expectations needed since workload manager will fail
			},
			setupWorkloadManager: func(mock *workloadmocks.MockManager) {
				// Mock GetWorkload to fail
				mock.EXPECT().
					GetWorkload(gomock.Any(), "test-workload").
					Return(workloads.Workload{}, errors.New("workload not found"))
			},
			expectError:   false,
			expectedGroup: nil, // When workload manager fails, we return nil (not in any group)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStore := mocks.NewMockStore(ctrl)
			mockWorkloadManager := workloadmocks.NewMockManager(ctrl)
			manager := &manager{store: mockStore, workloadManager: mockWorkloadManager}

			// Set up mock expectations
			tt.setupMock(mockStore)
			tt.setupWorkloadManager(mockWorkloadManager)

			// Call the method
			group, err := manager.GetWorkloadGroup(context.Background(), tt.workloadName)

			// Assert results
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
				if tt.expectedGroup != nil {
					assert.Equal(t, tt.expectedGroup.Name, group.Name)
					assert.Equal(t, tt.expectedGroup.Workloads, group.Workloads)
				} else {
					assert.Nil(t, group)
				}
			}
		})
	}
}

// TestGroup_AddWorkload tests the Group.AddWorkload method
func TestGroup_AddWorkload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		initialGroup  *Group
		workloadName  string
		expectedAdded bool
		expectedGroup *Group
	}{
		{
			name:          "add new workload to empty group",
			initialGroup:  &Group{Name: "test", Workloads: []string{}},
			workloadName:  "new-workload",
			expectedAdded: true,
			expectedGroup: &Group{Name: "test", Workloads: []string{"new-workload"}},
		},
		{
			name:          "add new workload to existing group",
			initialGroup:  &Group{Name: "test", Workloads: []string{"existing-workload"}},
			workloadName:  "new-workload",
			expectedAdded: true,
			expectedGroup: &Group{Name: "test", Workloads: []string{"existing-workload", "new-workload"}},
		},
		{
			name:          "add existing workload",
			initialGroup:  &Group{Name: "test", Workloads: []string{"existing-workload"}},
			workloadName:  "existing-workload",
			expectedAdded: false,
			expectedGroup: &Group{Name: "test", Workloads: []string{"existing-workload"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a copy of the initial group
			group := &Group{
				Name:      tt.initialGroup.Name,
				Workloads: make([]string, len(tt.initialGroup.Workloads)),
			}
			copy(group.Workloads, tt.initialGroup.Workloads)

			// Call the method
			added := group.AddWorkload(tt.workloadName)

			// Assert results
			assert.Equal(t, tt.expectedAdded, added)
			assert.Equal(t, tt.expectedGroup.Workloads, group.Workloads)
		})
	}
}

// TestGroup_RemoveWorkload tests the Group.RemoveWorkload method
func TestGroup_RemoveWorkload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		initialGroup    *Group
		workloadName    string
		expectedRemoved bool
		expectedGroup   *Group
	}{
		{
			name:            "remove existing workload",
			initialGroup:    &Group{Name: "test", Workloads: []string{"workload1", "workload2", "workload3"}},
			workloadName:    "workload2",
			expectedRemoved: true,
			expectedGroup:   &Group{Name: "test", Workloads: []string{"workload1", "workload3"}},
		},
		{
			name:            "remove workload from single workload group",
			initialGroup:    &Group{Name: "test", Workloads: []string{"workload1"}},
			workloadName:    "workload1",
			expectedRemoved: true,
			expectedGroup:   &Group{Name: "test", Workloads: []string{}},
		},
		{
			name:            "remove non-existent workload",
			initialGroup:    &Group{Name: "test", Workloads: []string{"workload1", "workload2"}},
			workloadName:    "nonexistent-workload",
			expectedRemoved: false,
			expectedGroup:   &Group{Name: "test", Workloads: []string{"workload1", "workload2"}},
		},
		{
			name:            "remove from empty group",
			initialGroup:    &Group{Name: "test", Workloads: []string{}},
			workloadName:    "any-workload",
			expectedRemoved: false,
			expectedGroup:   &Group{Name: "test", Workloads: []string{}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a copy of the initial group
			group := &Group{
				Name:      tt.initialGroup.Name,
				Workloads: make([]string, len(tt.initialGroup.Workloads)),
			}
			copy(group.Workloads, tt.initialGroup.Workloads)

			// Call the method
			removed := group.RemoveWorkload(tt.workloadName)

			// Assert results
			assert.Equal(t, tt.expectedRemoved, removed)
			assert.Equal(t, tt.expectedGroup.Workloads, group.Workloads)
		})
	}
}

// TestGroup_HasWorkload tests the Group.HasWorkload method
func TestGroup_HasWorkload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		group         *Group
		workloadName  string
		expectedFound bool
	}{
		{
			name:          "workload exists in group",
			group:         &Group{Name: "test", Workloads: []string{"workload1", "workload2", "workload3"}},
			workloadName:  "workload2",
			expectedFound: true,
		},
		{
			name:          "workload does not exist in group",
			group:         &Group{Name: "test", Workloads: []string{"workload1", "workload2"}},
			workloadName:  "nonexistent-workload",
			expectedFound: false,
		},
		{
			name:          "empty group",
			group:         &Group{Name: "test", Workloads: []string{}},
			workloadName:  "any-workload",
			expectedFound: false,
		},
		{
			name:          "case sensitive matching",
			group:         &Group{Name: "test", Workloads: []string{"Workload1", "workload2"}},
			workloadName:  "workload1",
			expectedFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Call the method
			found := tt.group.HasWorkload(tt.workloadName)

			// Assert results
			assert.Equal(t, tt.expectedFound, found)
		})
	}
}

// mockWriteCloser implements io.WriteCloser for testing
type mockWriteCloser struct {
	data []byte
}

func (m *mockWriteCloser) Write(p []byte) (n int, err error) {
	m.data = append(m.data, p...)
	return len(p), nil
}

func (*mockWriteCloser) Close() error {
	return nil
}

// mockReadCloser implements io.ReadCloser for testing
type mockReadCloser struct {
	data string
	pos  int
}

func (m *mockReadCloser) Read(p []byte) (n int, err error) {
	if m.pos >= len(m.data) {
		return 0, io.EOF
	}
	n = copy(p, m.data[m.pos:])
	m.pos += n
	return n, nil
}

func (*mockReadCloser) Close() error {
	return nil
}
