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
	"golang.org/x/sync/errgroup"

	"github.com/stacklok/toolhive/pkg/config"
	configMocks "github.com/stacklok/toolhive/pkg/config/mocks"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	runtimeMocks "github.com/stacklok/toolhive/pkg/container/runtime/mocks"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/runner"
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

func TestNewManagerFromRuntime(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRuntime := runtimeMocks.NewMockRuntime(ctrl)

	// The NewManagerFromRuntime will try to create a status manager, which requires runtime methods
	// For this test, we can just verify the structure is created correctly
	manager, err := NewManagerFromRuntime(mockRuntime)

	require.NoError(t, err)
	require.NotNil(t, manager)

	// Verify it's a defaultManager with the runtime set
	defaultMgr, ok := manager.(*defaultManager)
	require.True(t, ok)
	assert.Equal(t, mockRuntime, defaultMgr.runtime)
	assert.NotNil(t, defaultMgr.statuses)
	assert.NotNil(t, defaultMgr.configProvider)
}

func TestNewManagerFromRuntimeWithProvider(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRuntime := runtimeMocks.NewMockRuntime(ctrl)
	mockConfigProvider := configMocks.NewMockProvider(ctrl)

	manager, err := NewManagerFromRuntimeWithProvider(mockRuntime, mockConfigProvider)

	require.NoError(t, err)
	require.NotNil(t, manager)

	defaultMgr, ok := manager.(*defaultManager)
	require.True(t, ok)
	assert.Equal(t, mockRuntime, defaultMgr.runtime)
	assert.Equal(t, mockConfigProvider, defaultMgr.configProvider)
	assert.NotNil(t, defaultMgr.statuses)
}

func TestDefaultManager_DoesWorkloadExist(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		workloadName string
		setupMocks   func(*statusMocks.MockStatusManager)
		expected     bool
		expectError  bool
	}{
		{
			name:         "workload exists and running",
			workloadName: "test-workload",
			setupMocks: func(sm *statusMocks.MockStatusManager) {
				sm.EXPECT().GetWorkload(gomock.Any(), "test-workload").Return(core.Workload{
					Name:   "test-workload",
					Status: runtime.WorkloadStatusRunning,
				}, nil)
			},
			expected:    true,
			expectError: false,
		},
		{
			name:         "workload exists but in error state",
			workloadName: "error-workload",
			setupMocks: func(sm *statusMocks.MockStatusManager) {
				sm.EXPECT().GetWorkload(gomock.Any(), "error-workload").Return(core.Workload{
					Name:   "error-workload",
					Status: runtime.WorkloadStatusError,
				}, nil)
			},
			expected:    false,
			expectError: false,
		},
		{
			name:         "workload not found",
			workloadName: "missing-workload",
			setupMocks: func(sm *statusMocks.MockStatusManager) {
				sm.EXPECT().GetWorkload(gomock.Any(), "missing-workload").Return(core.Workload{}, runtime.ErrWorkloadNotFound)
			},
			expected:    false,
			expectError: false,
		},
		{
			name:         "error getting workload",
			workloadName: "problematic-workload",
			setupMocks: func(sm *statusMocks.MockStatusManager) {
				sm.EXPECT().GetWorkload(gomock.Any(), "problematic-workload").Return(core.Workload{}, errors.New("database error"))
			},
			expected:    false,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStatusMgr := statusMocks.NewMockStatusManager(ctrl)
			tt.setupMocks(mockStatusMgr)

			manager := &defaultManager{
				statuses: mockStatusMgr,
			}

			ctx := context.Background()
			result, err := manager.DoesWorkloadExist(ctx, tt.workloadName)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "failed to check if workload exists")
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestDefaultManager_GetWorkload(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStatusMgr := statusMocks.NewMockStatusManager(ctrl)
	expectedWorkload := core.Workload{
		Name:   "test-workload",
		Status: runtime.WorkloadStatusRunning,
	}

	mockStatusMgr.EXPECT().GetWorkload(gomock.Any(), "test-workload").Return(expectedWorkload, nil)

	manager := &defaultManager{
		statuses: mockStatusMgr,
	}

	ctx := context.Background()
	result, err := manager.GetWorkload(ctx, "test-workload")

	require.NoError(t, err)
	assert.Equal(t, expectedWorkload, result)
}

func TestDefaultManager_GetLogs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		workloadName string
		follow       bool
		setupMocks   func(*runtimeMocks.MockRuntime)
		expectedLogs string
		expectError  bool
		errorMsg     string
	}{
		{
			name:         "successful log retrieval",
			workloadName: "test-workload",
			follow:       false,
			setupMocks: func(rt *runtimeMocks.MockRuntime) {
				rt.EXPECT().GetWorkloadLogs(gomock.Any(), "test-workload", false).Return("test log content", nil)
			},
			expectedLogs: "test log content",
			expectError:  false,
		},
		{
			name:         "workload not found",
			workloadName: "missing-workload",
			follow:       false,
			setupMocks: func(rt *runtimeMocks.MockRuntime) {
				rt.EXPECT().GetWorkloadLogs(gomock.Any(), "missing-workload", false).Return("", runtime.ErrWorkloadNotFound)
			},
			expectedLogs: "",
			expectError:  true,
			errorMsg:     "workload not found",
		},
		{
			name:         "runtime error",
			workloadName: "error-workload",
			follow:       true,
			setupMocks: func(rt *runtimeMocks.MockRuntime) {
				rt.EXPECT().GetWorkloadLogs(gomock.Any(), "error-workload", true).Return("", errors.New("runtime failure"))
			},
			expectedLogs: "",
			expectError:  true,
			errorMsg:     "failed to get container logs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRuntime := runtimeMocks.NewMockRuntime(ctrl)
			tt.setupMocks(mockRuntime)

			manager := &defaultManager{
				runtime: mockRuntime,
			}

			ctx := context.Background()
			logs, err := manager.GetLogs(ctx, tt.workloadName, tt.follow)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedLogs, logs)
			}
		})
	}
}

func TestDefaultManager_StopWorkloads(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		workloadNames []string
		expectError   bool
		errorMsg      string
	}{
		{
			name:          "invalid workload name with path traversal",
			workloadNames: []string{"../etc/passwd"},
			expectError:   true,
			errorMsg:      "path traversal",
		},
		{
			name:          "invalid workload name with slash",
			workloadNames: []string{"workload/name"},
			expectError:   true,
			errorMsg:      "invalid workload name",
		},
		{
			name:          "empty workload name list",
			workloadNames: []string{},
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager := &defaultManager{}

			ctx := context.Background()
			group, err := manager.StopWorkloads(ctx, tt.workloadNames)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Nil(t, group)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, group)
				assert.IsType(t, &errgroup.Group{}, group)
			}
		})
	}
}

func TestDefaultManager_DeleteWorkloads(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		workloadNames []string
		expectError   bool
		errorMsg      string
	}{
		{
			name:          "invalid workload name",
			workloadNames: []string{"../../../etc/passwd"},
			expectError:   true,
			errorMsg:      "invalid workload name",
		},
		{
			name:          "mixed valid and invalid names",
			workloadNames: []string{"valid-name", "invalid../name"},
			expectError:   true,
			errorMsg:      "invalid workload name",
		},
		{
			name:          "empty workload name list",
			workloadNames: []string{},
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager := &defaultManager{}

			ctx := context.Background()
			group, err := manager.DeleteWorkloads(ctx, tt.workloadNames)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Nil(t, group)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, group)
				assert.IsType(t, &errgroup.Group{}, group)
			}
		})
	}
}

func TestDefaultManager_RestartWorkloads(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		workloadNames []string
		foreground    bool
		expectError   bool
		errorMsg      string
	}{
		{
			name:          "invalid workload name",
			workloadNames: []string{"invalid/name"},
			foreground:    false,
			expectError:   true,
			errorMsg:      "invalid workload name",
		},
		{
			name:          "empty workload name list",
			workloadNames: []string{},
			foreground:    false,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager := &defaultManager{}

			ctx := context.Background()
			group, err := manager.RestartWorkloads(ctx, tt.workloadNames, tt.foreground)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Nil(t, group)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, group)
				assert.IsType(t, &errgroup.Group{}, group)
			}
		})
	}
}

func TestDefaultManager_RunWorkload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		runConfig   *runner.RunConfig
		setupMocks  func(*statusMocks.MockStatusManager)
		expectError bool
		errorMsg    string
	}{
		{
			name: "successful run - status creation",
			runConfig: &runner.RunConfig{
				BaseName: "test-workload",
			},
			setupMocks: func(sm *statusMocks.MockStatusManager) {
				// Expect starting status first, then error status when the runner fails
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), "test-workload", runtime.WorkloadStatusStarting, "").Return(nil)
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), "test-workload", runtime.WorkloadStatusError, gomock.Any()).Return(nil)
			},
			expectError: true, // The runner will fail without proper setup
		},
		{
			name: "status creation failure",
			runConfig: &runner.RunConfig{
				BaseName: "failing-workload",
			},
			setupMocks: func(sm *statusMocks.MockStatusManager) {
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), "failing-workload", runtime.WorkloadStatusStarting, "").Return(errors.New("status creation failed"))
			},
			expectError: true,
			errorMsg:    "failed to create workload status",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStatusMgr := statusMocks.NewMockStatusManager(ctrl)
			tt.setupMocks(mockStatusMgr)

			manager := &defaultManager{
				statuses: mockStatusMgr,
			}

			ctx := context.Background()
			err := manager.RunWorkload(ctx, tt.runConfig)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDefaultManager_validateSecretParameters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		runConfig   *runner.RunConfig
		setupMocks  func(*configMocks.MockProvider)
		expectError bool
		errorMsg    string
	}{
		{
			name: "no secrets - should pass",
			runConfig: &runner.RunConfig{
				Secrets: []string{},
			},
			setupMocks:  func(*configMocks.MockProvider) {}, // No expectations
			expectError: false,
		},
		{
			name: "config error",
			runConfig: &runner.RunConfig{
				Secrets: []string{"secret1"},
			},
			setupMocks: func(cp *configMocks.MockProvider) {
				mockConfig := &config.Config{}
				cp.EXPECT().GetConfig().Return(mockConfig)
			},
			expectError: true,
			errorMsg:    "error determining secrets provider type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockConfigProvider := configMocks.NewMockProvider(ctrl)
			tt.setupMocks(mockConfigProvider)

			manager := &defaultManager{
				configProvider: mockConfigProvider,
			}

			ctx := context.Background()
			err := manager.validateSecretParameters(ctx, tt.runConfig)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDefaultManager_getWorkloadContainer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		workloadName string
		setupMocks   func(*runtimeMocks.MockRuntime, *statusMocks.MockStatusManager)
		expected     *runtime.ContainerInfo
		expectError  bool
		errorMsg     string
	}{
		{
			name:         "successful retrieval",
			workloadName: "test-workload",
			setupMocks: func(rt *runtimeMocks.MockRuntime, _ *statusMocks.MockStatusManager) {
				expectedContainer := runtime.ContainerInfo{
					Name:  "test-workload",
					State: runtime.WorkloadStatusRunning,
				}
				rt.EXPECT().GetWorkloadInfo(gomock.Any(), "test-workload").Return(expectedContainer, nil)
			},
			expected: &runtime.ContainerInfo{
				Name:  "test-workload",
				State: runtime.WorkloadStatusRunning,
			},
			expectError: false,
		},
		{
			name:         "workload not found",
			workloadName: "missing-workload",
			setupMocks: func(rt *runtimeMocks.MockRuntime, _ *statusMocks.MockStatusManager) {
				rt.EXPECT().GetWorkloadInfo(gomock.Any(), "missing-workload").Return(runtime.ContainerInfo{}, runtime.ErrWorkloadNotFound)
			},
			expected:    nil,
			expectError: false, // getWorkloadContainer returns nil for not found, not error
		},
		{
			name:         "runtime error",
			workloadName: "error-workload",
			setupMocks: func(rt *runtimeMocks.MockRuntime, sm *statusMocks.MockStatusManager) {
				rt.EXPECT().GetWorkloadInfo(gomock.Any(), "error-workload").Return(runtime.ContainerInfo{}, errors.New("runtime failure"))
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), "error-workload", runtime.WorkloadStatusError, "runtime failure").Return(nil)
			},
			expected:    nil,
			expectError: true,
			errorMsg:    "failed to find workload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRuntime := runtimeMocks.NewMockRuntime(ctrl)
			mockStatusMgr := statusMocks.NewMockStatusManager(ctrl)
			tt.setupMocks(mockRuntime, mockStatusMgr)

			manager := &defaultManager{
				runtime:  mockRuntime,
				statuses: mockStatusMgr,
			}

			ctx := context.Background()
			result, err := manager.getWorkloadContainer(ctx, tt.workloadName)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
				if tt.expected == nil {
					assert.Nil(t, result)
				} else {
					assert.Equal(t, tt.expected, result)
				}
			}
		})
	}
}

func TestDefaultManager_removeContainer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		workloadName string
		setupMocks   func(*runtimeMocks.MockRuntime, *statusMocks.MockStatusManager)
		expectError  bool
		errorMsg     string
	}{
		{
			name:         "successful removal",
			workloadName: "test-workload",
			setupMocks: func(rt *runtimeMocks.MockRuntime, _ *statusMocks.MockStatusManager) {
				rt.EXPECT().RemoveWorkload(gomock.Any(), "test-workload").Return(nil)
			},
			expectError: false,
		},
		{
			name:         "removal failure",
			workloadName: "failing-workload",
			setupMocks: func(rt *runtimeMocks.MockRuntime, sm *statusMocks.MockStatusManager) {
				rt.EXPECT().RemoveWorkload(gomock.Any(), "failing-workload").Return(errors.New("removal failed"))
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), "failing-workload", runtime.WorkloadStatusError, "removal failed").Return(nil)
			},
			expectError: true,
			errorMsg:    "failed to remove container",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRuntime := runtimeMocks.NewMockRuntime(ctrl)
			mockStatusMgr := statusMocks.NewMockStatusManager(ctrl)
			tt.setupMocks(mockRuntime, mockStatusMgr)

			manager := &defaultManager{
				runtime:  mockRuntime,
				statuses: mockStatusMgr,
			}

			ctx := context.Background()
			err := manager.removeContainer(ctx, tt.workloadName)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDefaultManager_isWorkloadAlreadyRunning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		workloadName  string
		workloadState *workloadState
		expected      bool
	}{
		{
			name:         "both running",
			workloadName: "test-workload",
			workloadState: &workloadState{
				Running:      true,
				ProxyRunning: true,
			},
			expected: true,
		},
		{
			name:         "workload running, proxy not",
			workloadName: "test-workload",
			workloadState: &workloadState{
				Running:      true,
				ProxyRunning: false,
			},
			expected: false,
		},
		{
			name:         "workload not running, proxy running",
			workloadName: "test-workload",
			workloadState: &workloadState{
				Running:      false,
				ProxyRunning: true,
			},
			expected: false,
		},
		{
			name:         "neither running",
			workloadName: "test-workload",
			workloadState: &workloadState{
				Running:      false,
				ProxyRunning: false,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager := &defaultManager{}
			result := manager.isWorkloadAlreadyRunning(tt.workloadName, tt.workloadState)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDefaultManager_needSecretsPassword(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		secretOptions []string
		setupMocks    func(*configMocks.MockProvider)
		expected      bool
	}{
		{
			name:          "no secrets",
			secretOptions: []string{},
			setupMocks:    func(*configMocks.MockProvider) {}, // No expectations
			expected:      false,
		},
		{
			name:          "has secrets but config access fails",
			secretOptions: []string{"secret1"},
			setupMocks: func(cp *configMocks.MockProvider) {
				mockConfig := &config.Config{}
				cp.EXPECT().GetConfig().Return(mockConfig)
			},
			expected: false, // Returns false when provider type detection fails
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockConfigProvider := configMocks.NewMockProvider(ctrl)
			tt.setupMocks(mockConfigProvider)

			manager := &defaultManager{
				configProvider: mockConfigProvider,
			}

			result := manager.needSecretsPassword(tt.secretOptions)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestAsyncOperationTimeout(t *testing.T) {
	t.Parallel()

	// Test that the timeout constant is properly defined
	assert.Equal(t, 5*time.Minute, AsyncOperationTimeout)
}

func TestErrWorkloadNotRunning(t *testing.T) {
	t.Parallel()

	// Test that the error is properly defined
	assert.Error(t, ErrWorkloadNotRunning)
	assert.Contains(t, ErrWorkloadNotRunning.Error(), "workload not running")
}

func TestDefaultManager_ListWorkloads(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		listAll      bool
		labelFilters []string
		setupMocks   func(*statusMocks.MockStatusManager)
		expected     []core.Workload
		expectError  bool
		errorMsg     string
	}{
		{
			name:         "successful listing without filters",
			listAll:      true,
			labelFilters: []string{},
			setupMocks: func(sm *statusMocks.MockStatusManager) {
				workloads := []core.Workload{
					{Name: "workload1", Status: runtime.WorkloadStatusRunning},
					{Name: "workload2", Status: runtime.WorkloadStatusStopped},
				}
				sm.EXPECT().ListWorkloads(gomock.Any(), true, []string{}).Return(workloads, nil)
				sm.EXPECT().GetWorkload(gomock.Any(), gomock.Any()).Return(core.Workload{
					Name:   "remote-workload",
					Status: runtime.WorkloadStatusRunning,
				}, nil).AnyTimes()
			},
			expected: []core.Workload{
				{Name: "workload1", Status: runtime.WorkloadStatusRunning},
				{Name: "workload2", Status: runtime.WorkloadStatusStopped},
			},
			expectError: false,
		},
		{
			name:         "error from status manager",
			listAll:      false,
			labelFilters: []string{"env=prod"},
			setupMocks: func(sm *statusMocks.MockStatusManager) {
				sm.EXPECT().ListWorkloads(gomock.Any(), false, []string{"env=prod"}).Return(nil, errors.New("database error"))
			},
			expected:    nil,
			expectError: true,
			errorMsg:    "database error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStatusMgr := statusMocks.NewMockStatusManager(ctrl)
			tt.setupMocks(mockStatusMgr)

			manager := &defaultManager{
				statuses: mockStatusMgr,
			}

			ctx := context.Background()
			result, err := manager.ListWorkloads(ctx, tt.listAll, tt.labelFilters...)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				// We expect this to succeed but might include remote workloads
				// Since getRemoteWorkloadsFromState will likely fail in unit tests,
				// we mainly verify the container workloads are returned
				require.NoError(t, err)
				assert.GreaterOrEqual(t, len(result), len(tt.expected))
				// Verify at least our expected container workloads are present
				for _, expectedWorkload := range tt.expected {
					found := false
					for _, actualWorkload := range result {
						if actualWorkload.Name == expectedWorkload.Name {
							found = true
							break
						}
					}
					assert.True(t, found, fmt.Sprintf("Expected workload %s not found in result", expectedWorkload.Name))
				}
			}
		})
	}
}
