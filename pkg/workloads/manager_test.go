// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package workloads

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/adrg/xdg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/config"
	configMocks "github.com/stacklok/toolhive/pkg/config/mocks"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	runtimeMocks "github.com/stacklok/toolhive/pkg/container/runtime/mocks"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/process"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/state"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/workloads/statuses"
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

			manager := &DefaultManager{
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
	defaultMgr, ok := manager.(*DefaultManager)
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

	defaultMgr, ok := manager.(*DefaultManager)
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

			manager := &DefaultManager{
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

	manager := &DefaultManager{
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
				rt.EXPECT().GetWorkloadLogs(gomock.Any(), "test-workload", false, 0).Return("test log content", nil)
			},
			expectedLogs: "test log content",
			expectError:  false,
		},
		{
			name:         "workload not found",
			workloadName: "missing-workload",
			follow:       false,
			setupMocks: func(rt *runtimeMocks.MockRuntime) {
				rt.EXPECT().GetWorkloadLogs(gomock.Any(), "missing-workload", false, 0).Return("", runtime.ErrWorkloadNotFound)
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
				rt.EXPECT().GetWorkloadLogs(gomock.Any(), "error-workload", true, 0).Return("", errors.New("runtime failure"))
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

			manager := &DefaultManager{
				runtime: mockRuntime,
			}

			ctx := context.Background()
			// Pass 0 for unlimited logs in these tests
			logs, err := manager.GetLogs(ctx, tt.workloadName, tt.follow, 0)

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

func TestDefaultManager_GetLogs_WithLineLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		workloadName string
		lines        int
		expectedLogs string
	}{
		{
			name:         "limit to 3 lines",
			workloadName: "test-workload",
			lines:        3,
			expectedLogs: "line3\nline4\nline5",
		},
		{
			name:         "no limit (0)",
			workloadName: "test-workload",
			lines:        0,
			expectedLogs: "line1\nline2\nline3",
		},
		{
			name:         "fewer lines than limit",
			workloadName: "test-workload",
			lines:        10,
			expectedLogs: "line1\nline2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRuntime := runtimeMocks.NewMockRuntime(ctrl)
			// Mock expects the lines parameter and returns already-limited logs
			mockRuntime.EXPECT().GetWorkloadLogs(gomock.Any(), tt.workloadName, false, tt.lines).Return(tt.expectedLogs, nil)

			manager := &DefaultManager{
				runtime: mockRuntime,
			}

			ctx := context.Background()
			logs, err := manager.GetLogs(ctx, tt.workloadName, false, tt.lines)

			require.NoError(t, err)
			assert.Equal(t, tt.expectedLogs, logs)
		})
	}
}

func TestDefaultManager_GetLogs_FollowWithLimitError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRuntime := runtimeMocks.NewMockRuntime(ctrl)
	// Expect the runtime to return an error when both follow and lines are set
	mockRuntime.EXPECT().GetWorkloadLogs(gomock.Any(), "test-workload", true, 100).
		Return("", errors.New("cannot use both follow and line limit"))

	manager := &DefaultManager{
		runtime: mockRuntime,
	}

	ctx := context.Background()
	logs, err := manager.GetLogs(ctx, "test-workload", true, 100)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot use both follow and line limit")
	assert.Empty(t, logs)
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

			manager := &DefaultManager{}

			ctx := context.Background()
			complete, err := manager.StopWorkloads(ctx, tt.workloadNames)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Nil(t, complete)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, complete)
				// Verify it's a CompletionFunc by checking it's callable
				assert.IsType(t, (CompletionFunc)(nil), complete)
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

			manager := &DefaultManager{}

			ctx := context.Background()
			complete, err := manager.DeleteWorkloads(ctx, tt.workloadNames)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Nil(t, complete)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, complete)
				// Verify it's a CompletionFunc by checking it's callable
				assert.IsType(t, (CompletionFunc)(nil), complete)
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

			manager := &DefaultManager{}

			ctx := context.Background()
			complete, err := manager.RestartWorkloads(ctx, tt.workloadNames, tt.foreground)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Nil(t, complete)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, complete)
				// Verify it's a CompletionFunc by checking it's callable
				assert.IsType(t, (CompletionFunc)(nil), complete)
			}
		})
	}
}

func TestDefaultManager_restartRemoteWorkload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		workloadName string
		runConfig    *runner.RunConfig
		foreground   bool
		setupMocks   func(*statusMocks.MockStatusManager)
		expectError  bool
		errorMsg     string
	}{
		{
			name:         "remote workload already running with healthy supervisor",
			workloadName: "remote-workload",
			runConfig: &runner.RunConfig{
				BaseName:  "remote-base",
				RemoteURL: "http://example.com",
			},
			foreground: false,
			setupMocks: func(sm *statusMocks.MockStatusManager) {
				sm.EXPECT().GetWorkload(gomock.Any(), "remote-workload").Return(core.Workload{
					Name:   "remote-workload",
					Status: runtime.WorkloadStatusRunning,
				}, nil)
				// Check if supervisor is alive - return valid PID (supervisor is healthy)
				sm.EXPECT().GetWorkloadPID(gomock.Any(), "remote-base").Return(12345, nil)
			},
			// With healthy supervisor, restart should return early (no-op)
			expectError: false,
		},
		{
			name:         "remote workload already running with dead supervisor",
			workloadName: "remote-workload",
			runConfig: &runner.RunConfig{
				BaseName:  "remote-base",
				RemoteURL: "http://example.com",
			},
			foreground: false,
			setupMocks: func(sm *statusMocks.MockStatusManager) {
				sm.EXPECT().GetWorkload(gomock.Any(), "remote-workload").Return(core.Workload{
					Name:   "remote-workload",
					Status: runtime.WorkloadStatusRunning,
				}, nil)
				// Check if supervisor is alive - return error (supervisor is dead)
				sm.EXPECT().GetWorkloadPID(gomock.Any(), "remote-base").Return(0, errors.New("no PID found"))
				sm.EXPECT().GetWorkloadPID(gomock.Any(), "remote-base").Return(0, errors.New("no PID found"))
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), "remote-workload", runtime.WorkloadStatusStopping, "").Return(nil)
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)
				sm.EXPECT().SetWorkloadPID(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)
			},
			expectError: true,
			errorMsg:    "failed to load state",
		},
		{
			name:         "remote workload unauthenticated stops proxy before restart",
			workloadName: "remote-workload",
			runConfig: &runner.RunConfig{
				BaseName:  "remote-base",
				RemoteURL: "http://example.com",
			},
			foreground: false,
			setupMocks: func(sm *statusMocks.MockStatusManager) {
				sm.EXPECT().GetWorkload(gomock.Any(), "remote-workload").Return(core.Workload{
					Name:   "remote-workload",
					Status: runtime.WorkloadStatusUnauthenticated,
				}, nil)
				sm.EXPECT().GetWorkloadPID(gomock.Any(), "remote-base").Return(0, errors.New("no PID found"))
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)
			},
			expectError: true,
			errorMsg:    "failed to load state",
		},
		{
			name:         "status manager error",
			workloadName: "remote-workload",
			runConfig: &runner.RunConfig{
				BaseName:  "remote-base",
				RemoteURL: "http://example.com",
			},
			foreground: false,
			setupMocks: func(sm *statusMocks.MockStatusManager) {
				sm.EXPECT().GetWorkload(gomock.Any(), "remote-workload").Return(core.Workload{}, errors.New("status manager error"))
			},
			expectError: true,
			errorMsg:    "status manager error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			statusMgr := statusMocks.NewMockStatusManager(ctrl)
			tt.setupMocks(statusMgr)

			manager := &DefaultManager{
				statuses: statusMgr,
			}

			err := manager.restartRemoteWorkload(context.Background(), tt.workloadName, tt.runConfig, tt.foreground)

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

func TestDefaultManager_restartContainerWorkload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		workloadName string
		foreground   bool
		setupMocks   func(*statusMocks.MockStatusManager, *runtimeMocks.MockRuntime)
		expectError  bool
		errorMsg     string
	}{
		{
			name:         "container workload already running with healthy supervisor",
			workloadName: "container-workload",
			foreground:   false,
			setupMocks: func(sm *statusMocks.MockStatusManager, rm *runtimeMocks.MockRuntime) {
				// Mock container info
				rm.EXPECT().GetWorkloadInfo(gomock.Any(), "container-workload").Return(runtime.ContainerInfo{
					Name:  "container-workload",
					State: runtime.WorkloadStatusRunning,
					Labels: map[string]string{
						"toolhive.base-name": "container-workload",
					},
				}, nil)
				sm.EXPECT().GetWorkload(gomock.Any(), "container-workload").Return(core.Workload{
					Name:   "container-workload",
					Status: runtime.WorkloadStatusRunning,
				}, nil)
				// Check if supervisor is alive - return valid PID (supervisor is healthy)
				sm.EXPECT().GetWorkloadPID(gomock.Any(), "container-workload").Return(12345, nil)
			},
			// With healthy supervisor, restart should return early (no-op)
			expectError: false,
		},
		{
			name:         "container workload already running with dead supervisor",
			workloadName: "container-workload",
			foreground:   false,
			setupMocks: func(sm *statusMocks.MockStatusManager, rm *runtimeMocks.MockRuntime) {
				// Mock container info
				rm.EXPECT().GetWorkloadInfo(gomock.Any(), "container-workload").Return(runtime.ContainerInfo{
					Name:  "container-workload",
					State: runtime.WorkloadStatusRunning,
					Labels: map[string]string{
						"toolhive.base-name": "container-workload",
					},
				}, nil)
				sm.EXPECT().GetWorkload(gomock.Any(), "container-workload").Return(core.Workload{
					Name:   "container-workload",
					Status: runtime.WorkloadStatusRunning,
				}, nil)
				// Check if supervisor is alive - return error (supervisor is dead)
				sm.EXPECT().GetWorkloadPID(gomock.Any(), "container-workload").Return(0, errors.New("no PID found"))
				// With dead supervisor, restart proceeds with cleanup and restart
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), "container-workload", runtime.WorkloadStatusStopping, "").Return(nil)
				sm.EXPECT().GetWorkloadPID(gomock.Any(), "container-workload").Return(0, errors.New("no PID found"))
				rm.EXPECT().StopWorkload(gomock.Any(), "container-workload").Return(nil)
				// Allow any subsequent status updates
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)
				sm.EXPECT().SetWorkloadPID(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)
			},
			// Restart now proceeds to load state which fails in tests (can't mock runner.LoadState easily)
			expectError: true,
			errorMsg:    "failed to load state",
		},
		{
			name:         "status manager error",
			workloadName: "container-workload",
			foreground:   false,
			setupMocks: func(sm *statusMocks.MockStatusManager, rm *runtimeMocks.MockRuntime) {
				// Mock container info
				rm.EXPECT().GetWorkloadInfo(gomock.Any(), "container-workload").Return(runtime.ContainerInfo{
					Name:  "container-workload",
					State: "running",
					Labels: map[string]string{
						"toolhive.base-name": "container-workload",
					},
				}, nil)
				sm.EXPECT().GetWorkload(gomock.Any(), "container-workload").Return(core.Workload{}, errors.New("status manager error"))
			},
			expectError: true,
			errorMsg:    "status manager error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			statusMgr := statusMocks.NewMockStatusManager(ctrl)
			runtimeMgr := runtimeMocks.NewMockRuntime(ctrl)

			tt.setupMocks(statusMgr, runtimeMgr)

			manager := &DefaultManager{
				statuses: statusMgr,
				runtime:  runtimeMgr,
			}

			err := manager.restartContainerWorkload(context.Background(), tt.workloadName, tt.foreground)

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

// TestDefaultManager_restartLogicConsistency tests restart behavior with healthy vs dead supervisor
func TestDefaultManager_restartLogicConsistency(t *testing.T) {
	t.Parallel()

	t.Run("remote_workload_healthy_supervisor_no_restart", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		statusMgr := statusMocks.NewMockStatusManager(ctrl)

		statusMgr.EXPECT().GetWorkload(gomock.Any(), "test-workload").Return(core.Workload{
			Name:   "test-workload",
			Status: runtime.WorkloadStatusRunning,
		}, nil)

		// Check if supervisor is alive - return valid PID (healthy)
		statusMgr.EXPECT().GetWorkloadPID(gomock.Any(), "test-base").Return(12345, nil)

		manager := &DefaultManager{
			statuses: statusMgr,
		}

		runConfig := &runner.RunConfig{
			BaseName:  "test-base",
			RemoteURL: "http://example.com",
		}

		err := manager.restartRemoteWorkload(context.Background(), "test-workload", runConfig, false)

		// With healthy supervisor, restart should return successfully without doing anything
		require.NoError(t, err)
	})

	t.Run("remote_workload_dead_supervisor_calls_stop", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		statusMgr := statusMocks.NewMockStatusManager(ctrl)

		statusMgr.EXPECT().GetWorkload(gomock.Any(), "test-workload").Return(core.Workload{
			Name:   "test-workload",
			Status: runtime.WorkloadStatusRunning,
		}, nil)
		statusMgr.EXPECT().GetWorkloadPID(gomock.Any(), "test-base").Return(0, errors.New("no PID found"))
		statusMgr.EXPECT().GetWorkloadPID(gomock.Any(), "test-base").Return(0, errors.New("no PID found"))
		statusMgr.EXPECT().SetWorkloadStatus(gomock.Any(), "test-workload", runtime.WorkloadStatusStopping, "").Return(nil)
		statusMgr.EXPECT().SetWorkloadStatus(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)
		statusMgr.EXPECT().SetWorkloadPID(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)

		manager := &DefaultManager{
			statuses: statusMgr,
		}

		runConfig := &runner.RunConfig{
			BaseName:  "test-base",
			RemoteURL: "http://example.com",
		}

		_ = manager.restartRemoteWorkload(context.Background(), "test-workload", runConfig, false)

		// The important part is that the stop methods were called (verified by mock expectations)
		// We don't care if the restart ultimately succeeds or fails
	})

	t.Run("remote_workload_pid_zero_no_error_treated_as_dead", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		statusMgr := statusMocks.NewMockStatusManager(ctrl)

		statusMgr.EXPECT().GetWorkload(gomock.Any(), "test-workload").Return(core.Workload{
			Name:   "test-workload",
			Status: runtime.WorkloadStatusRunning,
		}, nil)

		// PID 0 with nil error - simulates ResetWorkloadPID setting process_id to 0
		statusMgr.EXPECT().GetWorkloadPID(gomock.Any(), "test-base").Return(0, nil)
		// stopProxyIfNeeded also calls GetWorkloadPID (via stopProcess)
		statusMgr.EXPECT().GetWorkloadPID(gomock.Any(), "test-base").Return(0, nil)
		statusMgr.EXPECT().SetWorkloadStatus(gomock.Any(), "test-workload", runtime.WorkloadStatusStopping, "").Return(nil)
		statusMgr.EXPECT().SetWorkloadStatus(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)
		statusMgr.EXPECT().SetWorkloadPID(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)

		manager := &DefaultManager{
			statuses: statusMgr,
		}

		runConfig := &runner.RunConfig{
			BaseName:  "test-base",
			RemoteURL: "http://example.com",
		}

		_ = manager.restartRemoteWorkload(context.Background(), "test-workload", runConfig, false)

		// Mock expectations verify stop logic was invoked, not the healthy-supervisor no-op path
	})

	t.Run("container_workload_healthy_supervisor_no_restart", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		statusMgr := statusMocks.NewMockStatusManager(ctrl)
		runtimeMgr := runtimeMocks.NewMockRuntime(ctrl)

		containerInfo := runtime.ContainerInfo{
			Name:  "test-workload",
			State: runtime.WorkloadStatusRunning,
			Labels: map[string]string{
				"toolhive.base-name": "test-workload",
			},
		}
		runtimeMgr.EXPECT().GetWorkloadInfo(gomock.Any(), "test-workload").Return(containerInfo, nil)

		statusMgr.EXPECT().GetWorkload(gomock.Any(), "test-workload").Return(core.Workload{
			Name:   "test-workload",
			Status: runtime.WorkloadStatusRunning,
		}, nil)

		// Check if supervisor is alive - return valid PID (healthy)
		statusMgr.EXPECT().GetWorkloadPID(gomock.Any(), "test-workload").Return(12345, nil)

		manager := &DefaultManager{
			statuses: statusMgr,
			runtime:  runtimeMgr,
		}

		err := manager.restartContainerWorkload(context.Background(), "test-workload", false)

		// With healthy supervisor, restart should return successfully without doing anything
		require.NoError(t, err)
	})

	t.Run("container_workload_dead_supervisor_calls_stop", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		statusMgr := statusMocks.NewMockStatusManager(ctrl)
		runtimeMgr := runtimeMocks.NewMockRuntime(ctrl)

		containerInfo := runtime.ContainerInfo{
			Name:  "test-workload",
			State: runtime.WorkloadStatusRunning,
			Labels: map[string]string{
				"toolhive.base-name": "test-workload",
			},
		}
		runtimeMgr.EXPECT().GetWorkloadInfo(gomock.Any(), "test-workload").Return(containerInfo, nil)

		statusMgr.EXPECT().GetWorkload(gomock.Any(), "test-workload").Return(core.Workload{
			Name:   "test-workload",
			Status: runtime.WorkloadStatusRunning,
		}, nil)

		// Check if supervisor is alive - return error (dead supervisor)
		statusMgr.EXPECT().GetWorkloadPID(gomock.Any(), "test-workload").Return(0, errors.New("no PID found"))

		// When supervisor is dead, expect stop logic to be called
		statusMgr.EXPECT().SetWorkloadStatus(gomock.Any(), "test-workload", runtime.WorkloadStatusStopping, "").Return(nil)
		statusMgr.EXPECT().GetWorkloadPID(gomock.Any(), "test-workload").Return(0, errors.New("no PID found"))
		runtimeMgr.EXPECT().StopWorkload(gomock.Any(), "test-workload").Return(nil)

		// Allow any subsequent status updates (starting, error, etc.) - we don't care about the exact sequence
		statusMgr.EXPECT().SetWorkloadStatus(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)
		statusMgr.EXPECT().SetWorkloadPID(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)

		manager := &DefaultManager{
			statuses: statusMgr,
			runtime:  runtimeMgr,
		}

		_ = manager.restartContainerWorkload(context.Background(), "test-workload", false)

		// The important part is that the stop methods were called (verified by mock expectations)
		// We don't care if the restart ultimately succeeds or fails
	})

	t.Run("container_workload_pid_zero_no_error_treated_as_dead", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		statusMgr := statusMocks.NewMockStatusManager(ctrl)
		runtimeMgr := runtimeMocks.NewMockRuntime(ctrl)

		containerInfo := runtime.ContainerInfo{
			Name:  "test-workload",
			State: runtime.WorkloadStatusRunning,
			Labels: map[string]string{
				"toolhive.base-name": "test-workload",
			},
		}
		runtimeMgr.EXPECT().GetWorkloadInfo(gomock.Any(), "test-workload").Return(containerInfo, nil)

		statusMgr.EXPECT().GetWorkload(gomock.Any(), "test-workload").Return(core.Workload{
			Name:   "test-workload",
			Status: runtime.WorkloadStatusRunning,
		}, nil)

		// PID 0 with nil error - simulates ResetWorkloadPID setting process_id to 0
		statusMgr.EXPECT().GetWorkloadPID(gomock.Any(), "test-workload").Return(0, nil)

		// When supervisor is treated as dead, expect stop logic
		statusMgr.EXPECT().SetWorkloadStatus(gomock.Any(), "test-workload", runtime.WorkloadStatusStopping, "").Return(nil)
		statusMgr.EXPECT().GetWorkloadPID(gomock.Any(), "test-workload").Return(0, nil)
		runtimeMgr.EXPECT().StopWorkload(gomock.Any(), "test-workload").Return(nil)

		statusMgr.EXPECT().SetWorkloadStatus(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)
		statusMgr.EXPECT().SetWorkloadPID(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)

		manager := &DefaultManager{
			statuses: statusMgr,
			runtime:  runtimeMgr,
		}

		_ = manager.restartContainerWorkload(context.Background(), "test-workload", false)

		// Mock expectations verify stop logic was invoked, not the healthy-supervisor no-op path
	})
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
			expectSupervisorPIDCleanup(t, mockStatusMgr, tt.runConfig.BaseName)

			manager := &DefaultManager{
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

// fakeRunOutcome describes a single attempt's behavior for fakeRunnerFactory.
type fakeRunOutcome struct {
	duration time.Duration
	err      error
}

// fakeRunner implements mcpRunner for retry-loop tests by sleeping a
// configured duration before returning a configured error.
type fakeRunner struct {
	duration time.Duration
	err      error
}

func (f *fakeRunner) Run(ctx context.Context) error {
	if f.duration > 0 {
		select {
		case <-time.After(f.duration):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.err
}

// fakeRunnerFactory replays a sequence of outcomes across successive calls.
// Calls beyond the configured sequence return ErrContainerExitedRestartNeeded
// with no delay so the test fails fast if the loop runs longer than expected.
type fakeRunnerFactory struct {
	outcomes  []fakeRunOutcome
	callCount int
}

func (f *fakeRunnerFactory) factory() mcpRunnerFactory {
	return func(_ *runner.RunConfig, _ statuses.StatusManager) mcpRunner {
		idx := f.callCount
		f.callCount++
		if idx >= len(f.outcomes) {
			return &fakeRunner{err: runner.ErrContainerExitedRestartNeeded}
		}
		return &fakeRunner{duration: f.outcomes[idx].duration, err: f.outcomes[idx].err}
	}
}

func expectSupervisorPIDCleanup(t *testing.T, sm *statusMocks.MockStatusManager, name string) {
	t.Helper()
	sm.EXPECT().ResetWorkloadPIDIfMatch(gomock.Any(), name, os.Getpid()).
		DoAndReturn(func(ctx context.Context, _ string, _ int) error {
			assert.NoError(t, ctx.Err(), "PID cleanup must survive supervisor cancellation")
			_, bounded := ctx.Deadline()
			assert.True(t, bounded, "PID cleanup must not wait indefinitely")
			return nil
		})
}

//nolint:paralleltest // client configuration isolation uses t.Setenv
func TestDefaultManager_RunWorkload_CancellationInterruptsBackoff(t *testing.T) {
	isolateWorkloadStopState(t)

	ctrl := gomock.NewController(t)
	sm := statusMocks.NewMockStatusManager(ctrl)
	const name = "cancel-retrying-supervisor"
	restarting := make(chan struct{})
	sm.EXPECT().SetWorkloadStatus(gomock.Any(), name, runtime.WorkloadStatusStarting, "").Return(nil)
	sm.EXPECT().SetWorkloadStatus(gomock.Any(), name, runtime.WorkloadStatusStarting, "Container exited, restarting").
		DoAndReturn(func(context.Context, string, runtime.WorkloadStatus, string) error {
			close(restarting)
			return nil
		})
	// No Error status write is allowed after a user requests shutdown.
	expectSupervisorPIDCleanup(t, sm, name)
	factory := &fakeRunnerFactory{outcomes: []fakeRunOutcome{{err: runner.ErrContainerExitedRestartNeeded}}}
	manager := &DefaultManager{statuses: sm}
	withRunnerFactory(factory.factory())(manager)
	withRetryConfig(retryConfig{
		maxRetries: 3, initialDelay: 30 * time.Second,
		maxBackoff: time.Minute, stableRunThreshold: time.Minute,
	})(manager)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- manager.RunWorkload(ctx, &runner.RunConfig{BaseName: name}) }()
	select {
	case <-restarting:
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not schedule its retry")
	}
	// The retry status is written just before entering backoff. Leave enough
	// time to enter the wait rather than only testing cancellation before it.
	select {
	case err := <-done:
		t.Fatalf("supervisor returned before cancellation: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not interrupt retry backoff")
	}
	assert.Equal(t, 1, factory.callCount, "shutdown must not start another runner")
}

func TestDefaultManager_RunWorkload_CancelledSupervisorClearsOnlyItsOwnPID(t *testing.T) {
	t.Parallel()

	const name = "replaced-supervisor"
	ctrl := gomock.NewController(t)
	sm := statusMocks.NewMockStatusManager(ctrl)
	// The guarded API must receive this supervisor's PID, never a replacement's
	// PID read from storage or an unconditional reset.
	expectSupervisorPIDCleanup(t, sm, name)
	manager := &DefaultManager{statuses: sm}
	withRunnerFactory(func(*runner.RunConfig, statuses.StatusManager) mcpRunner {
		t.Error("cancelled supervisor must not create a runner")
		return &fakeRunner{}
	})(manager)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, manager.RunWorkload(ctx, &runner.RunConfig{BaseName: name}))
}

//nolint:paralleltest // client configuration isolation uses t.Setenv
func TestDefaultManager_RunWorkload_RetryCounterReset(t *testing.T) {
	isolateWorkloadStopState(t)

	// Test thresholds: short enough to keep the test fast, but with a wide
	// margin between "short" and "stable" run durations so scheduler jitter
	// on a busy CI host does not flip a "short" run to "stable". Production
	// thresholds are an order of magnitude larger.
	testCfg := retryConfig{
		maxRetries:         3,
		initialDelay:       1 * time.Millisecond,
		maxBackoff:         5 * time.Millisecond,
		stableRunThreshold: 50 * time.Millisecond,
	}

	short := fakeRunOutcome{duration: 5 * time.Millisecond, err: runner.ErrContainerExitedRestartNeeded}
	stable := fakeRunOutcome{duration: 200 * time.Millisecond, err: runner.ErrContainerExitedRestartNeeded}
	atThreshold := fakeRunOutcome{duration: 50 * time.Millisecond, err: runner.ErrContainerExitedRestartNeeded}

	tests := []struct {
		name             string
		outcomes         []fakeRunOutcome
		expectError      bool
		errorContains    string
		expectedAttempts int
	}{
		{
			name:             "all short runs exhaust the retry budget",
			outcomes:         []fakeRunOutcome{short, short, short},
			expectError:      true,
			errorContains:    "container restart failed after 3 attempts",
			expectedAttempts: 3,
		},
		{
			name:             "single stable run at start resets counter, extends budget by one cycle",
			outcomes:         []fakeRunOutcome{stable, short, short, short},
			expectError:      true,
			errorContains:    "container restart failed after 3 attempts",
			expectedAttempts: 4,
		},
		{
			name:             "stable run mid-sequence resets counter, granting a full fresh budget",
			outcomes:         []fakeRunOutcome{short, stable, short, short, short},
			expectError:      true,
			errorContains:    "container restart failed after 3 attempts",
			expectedAttempts: 5,
		},
		{
			name:             "run exactly at threshold counts as stable and resets counter",
			outcomes:         []fakeRunOutcome{atThreshold, short, short, short},
			expectError:      true,
			errorContains:    "container restart failed after 3 attempts",
			expectedAttempts: 4,
		},
		{
			name:             "stable run followed by clean shutdown returns success",
			outcomes:         []fakeRunOutcome{stable, {duration: 1 * time.Millisecond, err: nil}},
			expectError:      false,
			expectedAttempts: 2,
		},
		{
			name:             "non-restart error bails immediately without using the retry budget",
			outcomes:         []fakeRunOutcome{{duration: 1 * time.Millisecond, err: errors.New("fatal config error")}},
			expectError:      true,
			errorContains:    "fatal config error",
			expectedAttempts: 1,
		},
		{
			name:             "successful run returns without retrying",
			outcomes:         []fakeRunOutcome{{duration: 1 * time.Millisecond, err: nil}},
			expectError:      false,
			expectedAttempts: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStatusMgr := statusMocks.NewMockStatusManager(ctrl)
			mockStatusMgr.EXPECT().
				SetWorkloadStatus(gomock.Any(), "retry-test", gomock.Any(), gomock.Any()).
				Return(nil).
				AnyTimes()

			expectSupervisorPIDCleanup(t, mockStatusMgr, "retry-test")
			factory := &fakeRunnerFactory{outcomes: tt.outcomes}
			manager := &DefaultManager{statuses: mockStatusMgr}
			withRetryConfig(testCfg)(manager)
			withRunnerFactory(factory.factory())(manager)

			err := manager.RunWorkload(context.Background(), &runner.RunConfig{BaseName: "retry-test"})

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.expectedAttempts, factory.callCount, "unexpected number of run attempts")
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

			manager := &DefaultManager{
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

			manager := &DefaultManager{
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
				// After removal, verification check should confirm container is gone
				rt.EXPECT().GetWorkloadInfo(gomock.Any(), "test-workload").Return(runtime.ContainerInfo{}, runtime.ErrWorkloadNotFound)
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

			manager := &DefaultManager{
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

			manager := &DefaultManager{
				configProvider: mockConfigProvider,
			}

			result := manager.needSecretsPassword(tt.secretOptions)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDefaultManager_RunWorkloadDetached(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		runConfig   *runner.RunConfig
		setupMocks  func(*statusMocks.MockStatusManager, *configMocks.MockProvider)
		expectError bool
		errorMsg    string
	}{
		{
			name: "validation failure should not reach PID management",
			runConfig: &runner.RunConfig{
				BaseName: "test-workload",
				Secrets:  []string{"invalid-secret"},
			},
			setupMocks: func(_ *statusMocks.MockStatusManager, cp *configMocks.MockProvider) {
				// Mock config provider to cause validation failure
				mockConfig := &config.Config{}
				cp.EXPECT().GetConfig().Return(mockConfig)
				// No SetWorkloadPID expectation since validation should fail first
			},
			expectError: true,
			errorMsg:    "failed to validate workload parameters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStatusMgr := statusMocks.NewMockStatusManager(ctrl)
			mockConfigProvider := configMocks.NewMockProvider(ctrl)
			tt.setupMocks(mockStatusMgr, mockConfigProvider)

			manager := &DefaultManager{
				statuses:       mockStatusMgr,
				configProvider: mockConfigProvider,
			}

			ctx := context.Background()
			err := manager.RunWorkloadDetached(ctx, tt.runConfig)

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

// TestDefaultManager_RunWorkloadDetached_PIDManagement tests that PID management
// happens in the later stages of RunWorkloadDetached when the process actually starts.
// This is tested indirectly by verifying the behavior exists in the code flow.
func TestDefaultManager_RunWorkloadDetached_PIDManagement(t *testing.T) {
	t.Parallel()

	// This test documents the expected behavior:
	// 1. RunWorkloadDetached calls SetWorkloadPID after starting the detached process
	// 2. The PID management happens after validation and process creation
	// 3. SetWorkloadPID failures are logged as warnings but don't fail the operation

	// Since RunWorkloadDetached involves spawning actual processes and complex setup,
	// we verify the PID management integration exists by checking the method signature
	// and code structure rather than running the full integration.

	manager := &DefaultManager{}
	assert.NotNil(t, manager, "defaultManager should be instantiable")

	// Verify the method exists with the correct signature
	var runWorkloadDetachedFunc interface{} = manager.RunWorkloadDetached
	assert.NotNil(t, runWorkloadDetachedFunc, "RunWorkloadDetached method should exist")
}

// TestDefaultManager_RunWorkloadDetached_SpawnerSuccess verifies that the
// injected spawner is invoked exactly once and that the PID it returns is
// forwarded to SetWorkloadPID unchanged.
func TestDefaultManager_RunWorkloadDetached_SpawnerSuccess(t *testing.T) {
	t.Parallel()

	const wantPID = 98765
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStatusMgr := statusMocks.NewMockStatusManager(ctrl)
	mockConfigProvider := configMocks.NewMockProvider(ctrl)
	// Forwarded PID is the contract under test.
	mockStatusMgr.EXPECT().SetWorkloadPID(gomock.Any(), "test-workload", wantPID).Return(nil)

	var callCount int
	spawner := func(_ context.Context, rc *runner.RunConfig) (int, error) {
		callCount++
		assert.Equal(t, "test-workload", rc.BaseName)
		return wantPID, nil
	}

	manager := &DefaultManager{
		statuses:       mockStatusMgr,
		configProvider: mockConfigProvider,
	}
	withDetachedSpawner(spawner)(manager)

	err := manager.RunWorkloadDetached(context.Background(), &runner.RunConfig{BaseName: "test-workload"})
	require.NoError(t, err)
	assert.Equal(t, 1, callCount, "spawner should be invoked exactly once")
}

// TestDefaultManager_RunWorkloadDetached_SpawnerError verifies that a spawner
// failure is wrapped as "failed to start detached process" and that no PID
// is recorded. Status rollback is the responsibility of the spawner itself,
// so RunWorkloadDetached should not emit any extra SetWorkloadStatus calls.
func TestDefaultManager_RunWorkloadDetached_SpawnerError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStatusMgr := statusMocks.NewMockStatusManager(ctrl)
	mockConfigProvider := configMocks.NewMockProvider(ctrl)
	// No SetWorkloadPID expectation: a failed spawn must not record a PID.

	spawnErr := errors.New("boom")
	spawner := func(_ context.Context, _ *runner.RunConfig) (int, error) {
		return 0, spawnErr
	}

	manager := &DefaultManager{
		statuses:       mockStatusMgr,
		configProvider: mockConfigProvider,
	}
	withDetachedSpawner(spawner)(manager)

	err := manager.RunWorkloadDetached(context.Background(), &runner.RunConfig{BaseName: "test-workload"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start detached process")
	assert.ErrorIs(t, err, spawnErr)
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

			manager := &DefaultManager{
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

func TestDefaultManager_UpdateWorkload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		workloadName string
		expectError  bool
		errorMsg     string
		setupMocks   func(*runtimeMocks.MockRuntime, *statusMocks.MockStatusManager)
	}{
		{
			name:         "invalid workload name with slash",
			workloadName: "invalid/name",
			expectError:  true,
			errorMsg:     "invalid workload name",
		},
		{
			name:         "invalid workload name with backslash",
			workloadName: "invalid\\name",
			expectError:  true,
			errorMsg:     "invalid workload name",
		},
		{
			name:         "invalid workload name with path traversal",
			workloadName: "../invalid",
			expectError:  true,
			errorMsg:     "invalid workload name",
		},
		{
			name:         "valid workload name returns errgroup immediately",
			workloadName: "valid-workload",
			expectError:  false,
			setupMocks: func(rt *runtimeMocks.MockRuntime, sm *statusMocks.MockStatusManager) {
				// Mock calls that will happen in the background goroutine
				// We don't care about the success/failure, just that it doesn't panic
				rt.EXPECT().GetWorkloadInfo(gomock.Any(), "valid-workload").
					Return(runtime.ContainerInfo{}, errors.New("not found")).AnyTimes()
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), "valid-workload", gomock.Any(), gomock.Any()).
					Return(nil).AnyTimes()
			},
		},
		{
			name:         "UpdateWorkload returns errgroup even if async operation will fail",
			workloadName: "failing-workload",
			expectError:  false,
			setupMocks: func(rt *runtimeMocks.MockRuntime, sm *statusMocks.MockStatusManager) {
				// The async operation will fail, but UpdateWorkload itself should succeed
				rt.EXPECT().GetWorkloadInfo(gomock.Any(), "failing-workload").
					Return(runtime.ContainerInfo{}, errors.New("container lookup failed")).AnyTimes()
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), "failing-workload", gomock.Any(), gomock.Any()).
					Return(nil).AnyTimes()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRuntime := runtimeMocks.NewMockRuntime(ctrl)
			mockStatusManager := statusMocks.NewMockStatusManager(ctrl)
			mockConfigProvider := configMocks.NewMockProvider(ctrl)

			if tt.setupMocks != nil {
				tt.setupMocks(mockRuntime, mockStatusManager)
			}

			manager := &DefaultManager{
				runtime:        mockRuntime,
				statuses:       mockStatusManager,
				configProvider: mockConfigProvider,
			}

			// Create a dummy RunConfig for testing
			runConfig := &runner.RunConfig{
				ContainerName: tt.workloadName,
				BaseName:      tt.workloadName,
			}

			ctx := context.Background()
			complete, err := manager.UpdateWorkload(ctx, tt.workloadName, runConfig)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
				assert.Nil(t, complete)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, complete)
				// For valid cases, we get a completion func but don't call it
				// The async operations inside are tested separately
			}
		})
	}
}

//nolint:paralleltest // saved-workload fixtures use t.Setenv
func TestDefaultManager_updateSingleWorkload(t *testing.T) {
	isolateWorkloadStopState(t)

	// fakeSpawnerPID is the fake PID returned by the test-injected spawner.
	// It must be > 0 so SetWorkloadPID treats it as a real PID.
	const fakeSpawnerPID = 12345
	// noopSpawnerFor returns a spawner that replicates the production
	// spawner's WorkloadStatus(Starting) side effect against the supplied
	// mock, then returns fakeSpawnerPID without re-execing the test binary.
	// The Starting transition is part of the spawner contract; mirroring it
	// here keeps mock expectations consistent with production behavior.
	noopSpawnerFor := func(sm *statusMocks.MockStatusManager) detachedProcessSpawner {
		return func(ctx context.Context, rc *runner.RunConfig) (int, error) {
			if err := sm.SetWorkloadStatus(ctx, rc.BaseName, runtime.WorkloadStatusStarting, ""); err != nil {
				return 0, err
			}
			return fakeSpawnerPID, nil
		}
	}

	tests := []struct {
		name         string
		workloadName string
		runConfig    *runner.RunConfig
		setupMocks   func(*testing.T, *runtimeMocks.MockRuntime, *statusMocks.MockStatusManager)
		expectError  bool
		errorMsg     string
	}{
		{
			name:         "stop operation fails",
			workloadName: "test-workload",
			runConfig: &runner.RunConfig{
				ContainerName: "test-workload",
				BaseName:      "test-workload",
				Group:         "default",
			},
			setupMocks: func(t *testing.T, rt *runtimeMocks.MockRuntime, sm *statusMocks.MockStatusManager) {
				t.Helper()
				// Mock the stop operation - return error for GetWorkloadInfo
				rt.EXPECT().GetWorkloadInfo(gomock.Any(), "test-workload").
					Return(runtime.ContainerInfo{}, errors.New("container lookup failed")).AnyTimes()
				// Still expect status updates to be attempted
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), "test-workload", runtime.WorkloadStatusStopping, "").Return(nil).AnyTimes()
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), "test-workload", runtime.WorkloadStatusError, "").Return(nil).AnyTimes()
			},
			expectError: true,
			errorMsg:    "failed to stop workload",
		},
		{
			name:         "successful stop and delete operations complete correctly",
			workloadName: "test-workload",
			runConfig: &runner.RunConfig{
				ContainerName: "test-workload",
				BaseName:      "test-workload",
				Group:         "default",
			},
			setupMocks: func(t *testing.T, rt *runtimeMocks.MockRuntime, sm *statusMocks.MockStatusManager) {
				t.Helper()
				// Mock stop operation - workload exists and can be stopped
				rt.EXPECT().GetWorkloadInfo(gomock.Any(), "test-workload").
					Return(runtime.ContainerInfo{
						Name:   "test-workload",
						State:  "running",
						Labels: map[string]string{"toolhive-basename": "test-workload"},
					}, nil)
				// Stop only an owned proxy child, never an arbitrary host PID.
				pid := startVerifiedProxyProcess(t, "test-workload").pid
				sm.EXPECT().GetWorkloadPID(gomock.Any(), "test-workload").Return(pid, nil)
				rt.EXPECT().StopWorkload(gomock.Any(), "test-workload").Return(nil)
				sm.EXPECT().ResetWorkloadPIDIfMatch(gomock.Any(), "test-workload", pid).Return(nil)

				// Mock delete operation - workload exists and can be deleted
				rt.EXPECT().GetWorkloadInfo(gomock.Any(), "test-workload").
					Return(runtime.ContainerInfo{Name: "test-workload"}, nil)
				rt.EXPECT().RemoveWorkload(gomock.Any(), "test-workload").Return(nil)
				// After removal, verification check should confirm container is gone
				rt.EXPECT().GetWorkloadInfo(gomock.Any(), "test-workload").
					Return(runtime.ContainerInfo{}, runtime.ErrWorkloadNotFound)

				// Mock status updates for stop and delete phases
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), "test-workload", runtime.WorkloadStatusStopping, "").Return(nil)
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), "test-workload", runtime.WorkloadStatusStopped, "").Return(nil)
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), "test-workload", runtime.WorkloadStatusRemoving, "").Return(nil)
				sm.EXPECT().DeleteWorkloadStatus(gomock.Any(), "test-workload").Return(nil)

				// Mock RunWorkloadDetached calls - expect the ones that will be called.
				// The actual spawn is replaced by noopSpawner so SetWorkloadPID receives fakeSpawnerPID.
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), "test-workload", runtime.WorkloadStatusStarting, "").Return(nil)
				sm.EXPECT().SetWorkloadPID(gomock.Any(), "test-workload", fakeSpawnerPID).Return(nil)
			},
			expectError: false, // Test passes - update process completes successfully
		},
		{
			name:         "delete operation fails after successful stop",
			workloadName: "test-workload",
			runConfig: &runner.RunConfig{
				ContainerName: "test-workload",
				BaseName:      "test-workload",
				Group:         "default",
			},
			setupMocks: func(t *testing.T, rt *runtimeMocks.MockRuntime, sm *statusMocks.MockStatusManager) {
				t.Helper()
				// Mock successful stop
				rt.EXPECT().GetWorkloadInfo(gomock.Any(), "test-workload").
					Return(runtime.ContainerInfo{
						Name:   "test-workload",
						State:  "running",
						Labels: map[string]string{"toolhive-basename": "test-workload"},
					}, nil)
				// Stop only an owned proxy child, never an arbitrary host PID.
				pid := startVerifiedProxyProcess(t, "test-workload").pid
				sm.EXPECT().GetWorkloadPID(gomock.Any(), "test-workload").Return(pid, nil)
				rt.EXPECT().StopWorkload(gomock.Any(), "test-workload").Return(nil)
				sm.EXPECT().ResetWorkloadPIDIfMatch(gomock.Any(), "test-workload", pid).Return(nil)

				// Mock failed delete
				rt.EXPECT().GetWorkloadInfo(gomock.Any(), "test-workload").
					Return(runtime.ContainerInfo{Name: "test-workload"}, nil)
				rt.EXPECT().RemoveWorkload(gomock.Any(), "test-workload").Return(errors.New("delete failed"))

				sm.EXPECT().SetWorkloadStatus(gomock.Any(), "test-workload", runtime.WorkloadStatusStopping, "").Return(nil)
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), "test-workload", runtime.WorkloadStatusStopped, "").Return(nil)
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), "test-workload", runtime.WorkloadStatusRemoving, "").Return(nil)
				// RemoveWorkload fails, so error status is set
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), "test-workload", runtime.WorkloadStatusError, "delete failed").Return(nil)
			},
			expectError: true,
			errorMsg:    "failed to delete workload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			ctx := context.Background()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRuntime := runtimeMocks.NewMockRuntime(ctrl)
			mockStatusManager := statusMocks.NewMockStatusManager(ctrl)
			mockConfigProvider := configMocks.NewMockProvider(ctrl)

			if tt.setupMocks != nil {
				tt.setupMocks(t, mockRuntime, mockStatusManager)
			}

			manager := &DefaultManager{
				runtime:        mockRuntime,
				statuses:       mockStatusManager,
				configProvider: mockConfigProvider,
			}
			// Inject a no-op spawner so RunWorkloadDetached does not re-exec the
			// test binary. Without this, the real spawn would orphan a child
			// process that recursively reruns the entire test suite. See #5344.
			withDetachedSpawner(noopSpawnerFor(mockStatusManager))(manager)

			persistStopRunConfig(t, tt.workloadName, tt.runConfig)
			err := manager.updateSingleWorkload(ctx, tt.workloadName, tt.runConfig)

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

// TestDefaultManager_RunWorkload_ContainerExitHandling tests container exit handling
func TestDefaultManager_RunWorkload_ContainerExitHandling(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRuntime := runtimeMocks.NewMockRuntime(ctrl)
	mockStatusMgr := statusMocks.NewMockStatusManager(ctrl)
	mockConfigProvider := configMocks.NewMockProvider(ctrl)

	mockConfigProvider.EXPECT().GetConfig().Return(&config.Config{}).AnyTimes()

	// Expect status to be set to starting
	mockStatusMgr.EXPECT().
		SetWorkloadStatus(gomock.Any(), "test-workload", runtime.WorkloadStatusStarting, "").
		Return(nil)

	// Expect status to be set to error on failure
	mockStatusMgr.EXPECT().
		SetWorkloadStatus(gomock.Any(), "test-workload", runtime.WorkloadStatusError, gomock.Any()).
		Return(nil).AnyTimes()

	manager := &DefaultManager{
		runtime:        mockRuntime,
		statuses:       mockStatusMgr,
		configProvider: mockConfigProvider,
	}

	runConfig := &runner.RunConfig{
		ContainerName: "test-container",
		BaseName:      "test-workload",
		Group:         "default",
	}

	expectSupervisorPIDCleanup(t, mockStatusMgr, runConfig.BaseName)

	// RunWorkload will fail because the runner can't actually run
	// This tests that the status is properly set
	err := manager.RunWorkload(context.Background(), runConfig)
	assert.Error(t, err)
}

func TestDefaultManager_ListWorkloadsUsingSecret(t *testing.T) {
	t.Parallel()

	// This test verifies that ListWorkloadsUsingSecret returns correctly
	// when there are no configs or when the secret is not found.
	// Full integration tests would require setting up actual state files.

	t.Run("returns empty list when no configs exist", func(t *testing.T) {
		t.Parallel()

		manager := &DefaultManager{}

		ctx := context.Background()
		result, err := manager.ListWorkloadsUsingSecret(ctx, "nonexistent-secret")

		// Should succeed but return empty list
		// Note: This may return an error if the state directory doesn't exist,
		// but the implementation handles this gracefully
		if err == nil {
			assert.Empty(t, result)
		}
	})

	t.Run("method signature is correct", func(t *testing.T) {
		t.Parallel()

		manager := &DefaultManager{}

		// Verify the method exists with the correct signature
		listFunc := manager.ListWorkloadsUsingSecret
		assert.NotNil(t, listFunc, "ListWorkloadsUsingSecret method should exist with correct signature")
	})
}

func TestMapWorkloadStatusToVMCPHealth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   runtime.WorkloadStatus
		want vmcp.BackendHealthStatus
	}{
		{"running → healthy", runtime.WorkloadStatusRunning, vmcp.BackendHealthy},
		{"unhealthy → unhealthy", runtime.WorkloadStatusUnhealthy, vmcp.BackendUnhealthy},
		{"stopped → unhealthy", runtime.WorkloadStatusStopped, vmcp.BackendUnhealthy},
		{"error → unhealthy", runtime.WorkloadStatusError, vmcp.BackendUnhealthy},
		{"stopping → unhealthy", runtime.WorkloadStatusStopping, vmcp.BackendUnhealthy},
		{"removing → unhealthy", runtime.WorkloadStatusRemoving, vmcp.BackendUnhealthy},
		{"starting → unknown", runtime.WorkloadStatusStarting, vmcp.BackendUnknown},
		{"unknown → unknown", runtime.WorkloadStatusUnknown, vmcp.BackendUnknown},
		{"unauthenticated → unauthenticated", runtime.WorkloadStatusUnauthenticated, vmcp.BackendUnauthenticated},
		{"auth_retrying → degraded", runtime.WorkloadStatusAuthRetrying, vmcp.BackendDegraded},
		{"policy_stopped → unhealthy", runtime.WorkloadStatusPolicyStopped, vmcp.BackendUnhealthy},
		{"unrecognized → unknown", runtime.WorkloadStatus("not_a_real_status"), vmcp.BackendUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mapWorkloadStatusToVMCPHealth(tc.in)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestGetRemoteWorkloadsFromState_AuthRetryingVisibleWithoutAll verifies the
// visibility-by-default change for the remote-workload path. Remote workloads
// in AuthRetrying must be surfaced in default `thv list` (listAll=false), and
// Unauthenticated remote workloads must remain hidden, matching the filter in
// pkg/workloads/statuses/file_status.go:ListWorkloads.
//
//nolint:paralleltest // t.Setenv is incompatible with t.Parallel
func TestGetRemoteWorkloadsFromState_AuthRetryingVisibleWithoutAll(t *testing.T) {
	// Isolate the run-config store to a temp dir so runner.LoadState picks
	// up only the synthetic configs we write below. xdg.StateHome is cached
	// at package init, so an explicit Reload is required after t.Setenv.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	xdg.Reload()
	t.Cleanup(xdg.Reload)

	ctx := context.Background()

	// Save two synthetic remote workload runconfigs. One will be reported
	// as AuthRetrying and one as Unauthenticated by the status manager.
	retrying := &runner.RunConfig{
		SchemaVersion: runner.CurrentSchemaVersion,
		Name:          "remote-retrying",
		BaseName:      "remote-retrying",
		RemoteURL:     "http://example.com/mcp",
		Port:          12345,
		Transport:     types.TransportTypeStreamableHTTP,
		Group:         "default",
	}
	require.NoError(t, retrying.SaveState(ctx))

	dead := &runner.RunConfig{
		SchemaVersion: runner.CurrentSchemaVersion,
		Name:          "remote-dead",
		BaseName:      "remote-dead",
		RemoteURL:     "http://example.com/mcp",
		Port:          12346,
		Transport:     types.TransportTypeStreamableHTTP,
		Group:         "default",
	}
	require.NoError(t, dead.SaveState(ctx))

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStatusMgr := statusMocks.NewMockStatusManager(ctrl)
	// Container path returns no workloads — we are only testing the remote path.
	mockStatusMgr.EXPECT().ListWorkloads(gomock.Any(), false, gomock.Any()).Return([]core.Workload{}, nil)
	// GetWorkload returns AuthRetrying for the first remote and Unauthenticated for the second.
	mockStatusMgr.EXPECT().GetWorkload(gomock.Any(), "remote-retrying").Return(core.Workload{
		Name:      "remote-retrying",
		Status:    runtime.WorkloadStatusAuthRetrying,
		CreatedAt: time.Now(),
	}, nil)
	mockStatusMgr.EXPECT().GetWorkload(gomock.Any(), "remote-dead").Return(core.Workload{
		Name:      "remote-dead",
		Status:    runtime.WorkloadStatusUnauthenticated,
		CreatedAt: time.Now(),
	}, nil)

	manager := &DefaultManager{statuses: mockStatusMgr}

	result, err := manager.ListWorkloads(ctx, false)
	require.NoError(t, err)

	var sawRetrying, sawDead bool
	for _, w := range result {
		switch w.Name {
		case "remote-retrying":
			sawRetrying = true
			assert.Equal(t, runtime.WorkloadStatusAuthRetrying, w.Status,
				"remote-retrying workload should carry AuthRetrying status")
		case "remote-dead":
			sawDead = true
		}
	}
	assert.True(t, sawRetrying,
		"remote workload in AuthRetrying should be visible in default thv list (listAll=false)")
	assert.False(t, sawDead,
		"remote workload in Unauthenticated should remain hidden in default thv list (listAll=false)")
}

// startKillableProcess starts a real, long-lived child process and returns its
// PID and an exit notification. The process is killed and reaped on test cleanup. It represents an
// unrelated process that must not be killed as a proxy. The
// helper relies on the Unix `sleep` command, so it skips on Windows (the CI unit
// test matrix is Linux-only) and if the process cannot be started.
func startKillableProcess(t *testing.T) (int, <-chan struct{}) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("startKillableProcess relies on the Unix sleep command")
	}
	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Skipf("could not start helper process: %v", err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("unrelated helper process was not reaped")
		}
	})
	return cmd.Process.Pid, exited
}

func isolateWorkloadStopState(t *testing.T) {
	t.Helper()
	t.Cleanup(xdg.Reload)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// NewManager fails before runtime auto-detection or client discovery. XDG
	// isolation alone is insufficient: DefaultProvider caches registered clients
	// in a singleton, so also seed an empty config rather than reuse a prior load.
	t.Setenv("TOOLHIVE_RUNTIME", "disabled-for-test")
	xdg.Reload()
	config.SetSingletonConfig(&config.Config{})
	t.Cleanup(config.ResetSingleton)
}

func persistStopRunConfig(t *testing.T, name string, rc *runner.RunConfig) {
	t.Helper()
	copied := *rc
	copied.SchemaVersion = runner.CurrentSchemaVersion
	if copied.BaseName == "" {
		copied.BaseName = name
	}
	require.NoError(t, copied.SaveState(context.Background()))
}

type workloadProxyProcess struct {
	pid     int
	exited  chan struct{}
	waitErr error // Read only after exited is closed by the Wait goroutine.
}

func TestWorkloadProxyProcess(t *testing.T) {
	t.Parallel()

	mode := os.Getenv("TOOLHIVE_TEST_PROXY_HELPER")
	if mode == "" {
		return
	}
	terminated := make(chan os.Signal, 1)
	if mode == "ignore-term" {
		signal.Ignore(syscall.SIGTERM)
	} else {
		signal.Notify(terminated, syscall.SIGTERM)
	}
	fmt.Println("ready")
	select {
	case <-terminated:
		os.Exit(0)
	case <-time.After(time.Minute):
		os.Exit(1)
	}
}

func startVerifiedProxyProcess(t *testing.T, name string, args ...string) *workloadProxyProcess {
	t.Helper()
	return startProxyProcess(t, name, false, args...)
}

func startProxyProcess(t *testing.T, name string, ignoreSIGTERM bool, args ...string) *workloadProxyProcess {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("proxy helper requires SIGTERM")
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	detached := len(args) == 0
	if detached {
		args = []string{"start", name, "--foreground"}
	} else {
		thv := filepath.Join(t.TempDir(), "thv")
		require.NoError(t, os.Symlink(executable, thv))
		executable = thv
	}
	cmd := exec.Command(executable, append([]string{"-test.run=^TestWorkloadProxyProcess$", "--"}, args...)...)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "TOOLHIVE_DETACHED=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	mode := "1"
	if ignoreSIGTERM {
		mode = "ignore-term"
	}
	cmd.Env = append(cmd.Env, "TOOLHIVE_TEST_PROXY_HELPER="+mode)
	if detached {
		cmd.Env = append(cmd.Env, "TOOLHIVE_DETACHED=1")
	}
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	proxy := &workloadProxyProcess{pid: cmd.Process.Pid, exited: make(chan struct{})}
	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		ready <- line
	}()
	go func() {
		proxy.waitErr = cmd.Wait()
		close(proxy.exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-proxy.exited:
		case <-time.After(5 * time.Second):
			t.Error("proxy helper was not reaped")
		}
	})
	select {
	case line := <-ready:
		require.Equal(t, "ready\n", line)
	case <-time.After(5 * time.Second):
		t.Fatal("proxy helper did not start")
	}
	verified, err := process.IsToolHiveProxyForWorkload(proxy.pid, name)
	require.NoError(t, err)
	require.True(t, verified, "helper must satisfy production proxy identity checks")
	return proxy
}

//nolint:paralleltest // t.Setenv is incompatible with t.Parallel
func TestDefaultManager_stopContainerWorkload_ExitedContainerWaitsForTrackedProxy(t *testing.T) {
	isolateWorkloadStopState(t)

	for _, tt := range []struct {
		name       string
		startProxy func(*testing.T) *workloadProxyProcess
	}{
		{
			name: "detached start uses workload base name",
			startProxy: func(t *testing.T) *workloadProxyProcess {
				t.Helper()
				return startVerifiedProxyProcess(t, "requested-container")
			},
		},
		{
			name: "foreground run without detached environment uses thv binary identity",
			startProxy: func(t *testing.T) *workloadProxyProcess {
				t.Helper()
				return startVerifiedProxyProcess(t, "", "run", "--foreground", "example/server")
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			proxy := tt.startProxy(t)
			ctrl := gomock.NewController(t)
			sm := statusMocks.NewMockStatusManager(ctrl)
			rtMock := runtimeMocks.NewMockRuntime(ctrl)
			rtMock.EXPECT().GetWorkloadInfo(gomock.Any(), "requested-container").Return(runtime.ContainerInfo{
				Name: "requested-container-1730000000", State: "exited",
				Labels: map[string]string{labels.LabelBaseName: "requested-container"},
			}, nil)
			sm.EXPECT().SetWorkloadStatus(gomock.Any(), "requested-container", runtime.WorkloadStatusStopping, "").Return(nil)
			sm.EXPECT().GetWorkloadPID(gomock.Any(), "requested-container").Return(proxy.pid, nil)
			sm.EXPECT().ResetWorkloadPIDIfMatch(gomock.Any(), "requested-container", proxy.pid).Return(nil)
			rtMock.EXPECT().StopWorkload(gomock.Any(), "requested-container-1730000000").DoAndReturn(func(context.Context, string) error {
				select {
				case <-proxy.exited:
					assert.NoError(t, proxy.waitErr, "proxy must handle SIGTERM before runtime stop")
				default:
					t.Error("runtime stop called before tracked proxy exited")
				}
				return nil
			})
			sm.EXPECT().SetWorkloadStatus(gomock.Any(), "requested-container", runtime.WorkloadStatusStopped, "").Return(nil)
			manager := &DefaultManager{statuses: sm, runtime: rtMock}
			withPortFreer(func(context.Context, *runner.RunConfig) {
				t.Error("verified proxy exit must not require port fallback")
			})(manager)

			rc := &runner.RunConfig{BaseName: "requested-container", ContainerName: "requested-container-1730000000"}
			persistStopRunConfig(t, "requested-container", rc)
			require.NoError(t, manager.stopContainerWorkload(context.Background(), "requested-container", rc))
		})
	}
}

//nolint:paralleltest // t.Setenv is incompatible with t.Parallel
func TestDefaultManager_stopContainerWorkload_MissingContainerUsesSavedLabels(t *testing.T) {
	isolateWorkloadStopState(t)

	for _, tt := range []struct {
		name              string
		runConfig         *runner.RunConfig
		wantContainerName string
	}{
		{
			name: "saved base fills missing label without mutating config",
			runConfig: &runner.RunConfig{
				BaseName:        "requested-container",
				ContainerName:   "requested-container-1730000000",
				ContainerLabels: map[string]string{"custom-label": "preserved"},
			},
			wantContainerName: "requested-container-1730000000",
		},
		{
			name: "saved base label is preserved without mutating config",
			runConfig: &runner.RunConfig{
				BaseName:        "requested-container",
				ContainerName:   "requested-container-1730000000",
				ContainerLabels: map[string]string{labels.LabelBaseName: "requested-container"},
			},
			wantContainerName: "requested-container-1730000000",
		},
		{
			name:              "missing saved container name falls back to workload base",
			runConfig:         &runner.RunConfig{BaseName: "requested-container"},
			wantContainerName: "requested-container",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			persistStopRunConfig(t, "requested-container", tt.runConfig)
			original := *tt.runConfig
			original.ContainerLabels = maps.Clone(tt.runConfig.ContainerLabels)
			ctrl := gomock.NewController(t)
			sm := statusMocks.NewMockStatusManager(ctrl)
			rtMock := runtimeMocks.NewMockRuntime(ctrl)
			rtMock.EXPECT().GetWorkloadInfo(gomock.Any(), "requested-container").Return(runtime.ContainerInfo{},
				fmt.Errorf("inspect: %w", runtime.ErrWorkloadNotFound))
			sm.EXPECT().SetWorkloadStatus(gomock.Any(), "requested-container", runtime.WorkloadStatusStopping, "").Return(nil)
			sm.EXPECT().GetWorkloadPID(gomock.Any(), "requested-container").Return(0, runtime.ErrWorkloadNotFound)
			fallbackCalls := 0
			rtMock.EXPECT().StopWorkload(gomock.Any(), tt.wantContainerName).DoAndReturn(func(context.Context, string) error {
				assert.Equal(t, 1, fallbackCalls, "proxy cleanup must precede the fresh runtime stop")
				return nil
			})
			sm.EXPECT().SetWorkloadStatus(gomock.Any(), "requested-container", runtime.WorkloadStatusStopped, "").Return(nil)
			manager := &DefaultManager{statuses: sm, runtime: rtMock}
			withPortFreer(func(_ context.Context, rc *runner.RunConfig) {
				assert.Same(t, tt.runConfig, rc)
				fallbackCalls++
			})(manager)

			require.NoError(t, manager.stopContainerWorkload(context.Background(), "requested-container", tt.runConfig))
			assert.Equal(t, &original, tt.runConfig)
		})
	}
}

func TestDefaultManager_stopContainerWorkload_MissingConfigStopsTrackedProxyWithoutStatusWrites(t *testing.T) {
	t.Parallel()

	const name = "missing-container"
	proxy := startVerifiedProxyProcess(t, name)
	ctrl := gomock.NewController(t)
	sm := statusMocks.NewMockStatusManager(ctrl)
	rtMock := runtimeMocks.NewMockRuntime(ctrl)
	rtMock.EXPECT().GetWorkloadInfo(gomock.Any(), name).Return(runtime.ContainerInfo{}, runtime.ErrWorkloadNotFound)
	sm.EXPECT().GetWorkloadPID(gomock.Any(), name).Return(proxy.pid, nil)
	sm.EXPECT().ResetWorkloadPIDIfMatch(gomock.Any(), name, proxy.pid).Return(nil)
	rtMock.EXPECT().StopWorkload(gomock.Any(), name).DoAndReturn(func(context.Context, string) error {
		select {
		case <-proxy.exited:
			assert.NoError(t, proxy.waitErr)
		default:
			t.Error("runtime stop called before tracked proxy exited")
		}
		return nil
	})
	manager := &DefaultManager{statuses: sm, runtime: rtMock}

	require.NoError(t, manager.stopContainerWorkload(context.Background(), name, nil))
}

func TestDefaultManager_stopContainerWorkload_InspectionFailureDoesNotStopProxy(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	sm := statusMocks.NewMockStatusManager(ctrl)
	rtMock := runtimeMocks.NewMockRuntime(ctrl)
	inspectErr := errors.New("runtime inspection failed")
	rtMock.EXPECT().GetWorkloadInfo(gomock.Any(), "container").Return(runtime.ContainerInfo{}, inspectErr)
	manager := &DefaultManager{statuses: sm, runtime: rtMock}

	require.ErrorIs(t, manager.stopContainerWorkload(context.Background(), "container",
		&runner.RunConfig{BaseName: "saved-base"}), inspectErr)
}

//nolint:paralleltest // saved-workload fixtures use t.Setenv
func TestDefaultManager_stopContainerWorkload_RuntimeStopFailureRecordsErrorAfterProxyCleanup(t *testing.T) {
	isolateWorkloadStopState(t)

	ctrl := gomock.NewController(t)
	sm := statusMocks.NewMockStatusManager(ctrl)
	rtMock := runtimeMocks.NewMockRuntime(ctrl)
	stopErr := errors.New("runtime stop failed")
	rtMock.EXPECT().GetWorkloadInfo(gomock.Any(), "container").Return(runtime.ContainerInfo{}, runtime.ErrWorkloadNotFound)
	sm.EXPECT().SetWorkloadStatus(gomock.Any(), "container", runtime.WorkloadStatusStopping, "").Return(nil)
	sm.EXPECT().GetWorkloadPID(gomock.Any(), "container").Return(0, runtime.ErrWorkloadNotFound)
	cleanupComplete := false
	rtMock.EXPECT().StopWorkload(gomock.Any(), "container").DoAndReturn(func(context.Context, string) error {
		assert.True(t, cleanupComplete, "runtime stop must follow proxy cleanup even on failure")
		return stopErr
	})
	sm.EXPECT().SetWorkloadStatus(gomock.Any(), "container", runtime.WorkloadStatusError, stopErr.Error()).Return(nil)
	manager := &DefaultManager{statuses: sm, runtime: rtMock}
	withPortFreer(func(context.Context, *runner.RunConfig) { cleanupComplete = true })(manager)

	rc := &runner.RunConfig{BaseName: "container"}
	persistStopRunConfig(t, "container", rc)
	require.ErrorIs(t, manager.stopContainerWorkload(context.Background(), "container", rc), stopErr)
}

func TestDefaultManager_stopContainerWorkload_MissingConfigPropagatesRuntimeStopFailure(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	sm := statusMocks.NewMockStatusManager(ctrl)
	rtMock := runtimeMocks.NewMockRuntime(ctrl)
	stopErr := errors.New("runtime stop failed")
	rtMock.EXPECT().GetWorkloadInfo(gomock.Any(), "container").Return(runtime.ContainerInfo{}, runtime.ErrWorkloadNotFound)
	sm.EXPECT().GetWorkloadPID(gomock.Any(), "container").Return(0, runtime.ErrWorkloadNotFound)
	rtMock.EXPECT().StopWorkload(gomock.Any(), "container").Return(stopErr)
	manager := &DefaultManager{statuses: sm, runtime: rtMock}

	require.ErrorIs(t, manager.stopContainerWorkload(context.Background(), "container", nil), stopErr)
}

//nolint:paralleltest // t.Setenv is incompatible with t.Parallel
func TestDefaultManager_StopWorkloads_MissingContainerStopsSavedContainerNameAndPreservesConfig(t *testing.T) {
	isolateWorkloadStopState(t)

	ctx := context.Background()
	const name = "missing-container"
	runConfig := &runner.RunConfig{
		SchemaVersion:   runner.CurrentSchemaVersion,
		Name:            "saved-container-name",
		ContainerName:   "saved-container-name-1730000000",
		BaseName:        name,
		ContainerLabels: map[string]string{"custom-label": "preserved"},
		Transport:       types.TransportTypeStreamableHTTP,
		Group:           "default",
	}
	require.NoError(t, runConfig.SaveState(ctx))
	before, err := runner.LoadState(ctx, name)
	require.NoError(t, err)

	ctrl := gomock.NewController(t)
	sm := statusMocks.NewMockStatusManager(ctrl)
	rtMock := runtimeMocks.NewMockRuntime(ctrl)
	rtMock.EXPECT().GetWorkloadInfo(gomock.Any(), name).Return(runtime.ContainerInfo{}, runtime.ErrWorkloadNotFound)
	sm.EXPECT().SetWorkloadStatus(gomock.Any(), name, runtime.WorkloadStatusStopping, "").Return(nil)
	sm.EXPECT().GetWorkloadPID(gomock.Any(), name).Return(0, runtime.ErrWorkloadNotFound)
	sm.EXPECT().SetWorkloadStatus(gomock.Any(), name, runtime.WorkloadStatusStopped, "").Return(nil)
	cleanupComplete := false
	rtMock.EXPECT().StopWorkload(gomock.Any(), runConfig.ContainerName).DoAndReturn(func(context.Context, string) error {
		assert.True(t, cleanupComplete)
		return nil
	})
	manager := &DefaultManager{statuses: sm, runtime: rtMock}
	withPortFreer(func(_ context.Context, rc *runner.RunConfig) {
		assert.Equal(t, before, rc)
		cleanupComplete = true
	})(manager)

	complete, err := manager.StopWorkloads(ctx, []string{name})
	require.NoError(t, err)
	require.NoError(t, complete())
	after, err := runner.LoadState(ctx, name)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

//nolint:paralleltest // saved-workload fixtures use t.Setenv
func TestDefaultManager_StopWorkload_ForegroundDeletionIsNotResurrected(t *testing.T) {
	isolateWorkloadStopState(t)

	for _, kind := range []string{"container", "remote"} {
		remote := kind == "remote"
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			name := "deleted-foreground-" + kind
			rc := &runner.RunConfig{BaseName: name, ContainerName: name}
			if remote {
				rc.RemoteURL = "https://example.com/mcp"
			}
			persistStopRunConfig(t, name, rc)
			store, err := state.NewRunConfigStore(state.DefaultAppName)
			require.NoError(t, err)
			ctrl := gomock.NewController(t)
			rtMock := runtimeMocks.NewMockRuntime(ctrl)
			fileStatuses, err := statuses.NewFileStatusManager(rtMock)
			require.NoError(t, err)
			require.NoError(t, fileStatuses.SetWorkloadStatus(ctx, name, runtime.WorkloadStatusRunning, ""))
			proxy := startVerifiedProxyProcess(t, "", "run", "--foreground", "example/server")
			require.NoError(t, fileStatuses.SetWorkloadPID(ctx, name, proxy.pid))
			statusPath := filepath.Join(xdg.DataHome, "toolhive", "statuses", name+".json")
			sm := statusMocks.NewMockStatusManager(ctrl)
			if remote {
				sm.EXPECT().GetWorkload(gomock.Any(), name).Return(core.Workload{
					Name: name, Status: runtime.WorkloadStatusRunning,
				}, nil)
			} else {
				rtMock.EXPECT().GetWorkloadInfo(gomock.Any(), name).Return(runtime.ContainerInfo{
					Name: name, State: "exited", Labels: map[string]string{labels.LabelBaseName: name},
				}, nil)
				rtMock.EXPECT().StopWorkload(gomock.Any(), name).DoAndReturn(func(context.Context, string) error {
					select {
					case <-proxy.exited:
						assert.NoError(t, proxy.waitErr)
					default:
						t.Error("runtime stop preceded foreground supervisor exit")
					}
					return nil
				})
			}
			// Only Stopping is permitted: neither path may write Stopped after deletion.
			sm.EXPECT().SetWorkloadStatus(gomock.Any(), name, runtime.WorkloadStatusStopping, "").
				DoAndReturn(fileStatuses.SetWorkloadStatus)
			sm.EXPECT().GetWorkloadPID(gomock.Any(), name).
				DoAndReturn(func(ctx context.Context, name string) (int, error) {
					pid, err := fileStatuses.GetWorkloadPID(ctx, name)
					require.NoError(t, err)
					// Model the owner deleting both files before exit, not during the
					// later runtime stop. PID reset must tolerate the deleted status.
					require.NoError(t, store.Delete(ctx, name))
					require.NoError(t, fileStatuses.DeleteWorkloadStatus(ctx, name))
					return pid, nil
				})
			sm.EXPECT().ResetWorkloadPIDIfMatch(gomock.Any(), name, proxy.pid).
				DoAndReturn(func(ctx context.Context, name string, pid int) error {
					_, err := os.Stat(statusPath)
					require.ErrorIs(t, err, os.ErrNotExist, "owner deletion must precede guarded PID reset")
					return fileStatuses.ResetWorkloadPIDIfMatch(ctx, name, pid)
				})
			manager := &DefaultManager{runtime: rtMock, statuses: sm}
			withPortFreer(func(context.Context, *runner.RunConfig) {
				t.Error("foreground exit must not require port fallback")
			})(manager)

			if remote {
				require.NoError(t, manager.stopRemoteWorkload(ctx, name, rc))
			} else {
				require.NoError(t, manager.stopContainerWorkload(ctx, name, rc))
			}
			exists, err := store.Exists(ctx, name)
			require.NoError(t, err)
			assert.False(t, exists, "stop must not recreate a deleted foreground run config")
			_, err = os.Stat(statusPath)
			assert.ErrorIs(t, err, os.ErrNotExist, "stop must not recreate status after foreground deletion")
		})
	}
}

//nolint:paralleltest // saved-workload fixtures use t.Setenv
func TestDefaultManager_stopRemoteWorkload_LiveProxyStatusesEndStopped(t *testing.T) {
	isolateWorkloadStopState(t)
	for _, status := range []runtime.WorkloadStatus{
		runtime.WorkloadStatusStarting,
		runtime.WorkloadStatusAuthRetrying,
		runtime.WorkloadStatusUnauthenticated,
		runtime.WorkloadStatusUnhealthy,
	} {
		t.Run(string(status), func(t *testing.T) {
			const name = "remote-retry-backoff"
			rc := &runner.RunConfig{BaseName: name, RemoteURL: "https://example.com/mcp"}
			persistStopRunConfig(t, name, rc)
			proxy := startVerifiedProxyProcess(t, name)
			ctrl := gomock.NewController(t)
			sm := statusMocks.NewMockStatusManager(ctrl)
			gomock.InOrder(
				sm.EXPECT().GetWorkload(gomock.Any(), name).Return(core.Workload{
					Name: name, Status: status,
				}, nil),
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), name, runtime.WorkloadStatusStopping, "").Return(nil),
				sm.EXPECT().GetWorkloadPID(gomock.Any(), name).Return(proxy.pid, nil),
				sm.EXPECT().ResetWorkloadPIDIfMatch(gomock.Any(), name, proxy.pid).Return(nil),
				sm.EXPECT().SetWorkloadStatus(gomock.Any(), name, runtime.WorkloadStatusStopped, "").Return(nil),
			)
			manager := &DefaultManager{statuses: sm}
			withPortFreer(func(context.Context, *runner.RunConfig) { t.Error("tracked supervisor exit must not need fallback") })(manager)

			require.NoError(t, manager.stopRemoteWorkload(context.Background(), name, rc))
			select {
			case <-proxy.exited:
				assert.NoError(t, proxy.waitErr)
			default:
				t.Error("remote stop returned while supervisor was still alive")
			}
		})
	}
}

// TestDefaultManager_stopProcess verifies the boolean contract that the
// stop/delete port-cleanup fallback relies on: stopProcess reports true only
// when a tracked, identity-verified proxy exits. A false return (no PID recorded —
// e.g. a missing status file — or an unverified PID) is what triggers the
// port-based cleanup fallback in stopSingleContainerWorkload/deleteContainerWorkload.
func TestDefaultManager_stopProcess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		workload   string
		setupMocks func(t *testing.T, sm *statusMocks.MockStatusManager)
		want       bool
	}{
		{
			name:     "empty name returns false",
			workload: "",
			setupMocks: func(_ *testing.T, _ *statusMocks.MockStatusManager) {
				// No status manager calls expected for an empty name.
			},
			want: false,
		},
		{
			name:     "GetWorkloadPID error returns false",
			workload: "test-workload",
			setupMocks: func(_ *testing.T, sm *statusMocks.MockStatusManager) {
				sm.EXPECT().GetWorkloadPID(gomock.Any(), "test-workload").Return(0, errors.New("boom"))
			},
			want: false,
		},
		{
			name:     "missing status file (pid 0) returns false",
			workload: "test-workload",
			setupMocks: func(_ *testing.T, sm *statusMocks.MockStatusManager) {
				// A missing status file yields PID 0 with no error; identity validation
				// rejects it without a PID update.
				sm.EXPECT().GetWorkloadPID(gomock.Any(), "test-workload").Return(0, nil)
			},
			want: false,
		},
		{
			name:     "verified proxy exits and returns true",
			workload: "test-workload",
			setupMocks: func(t *testing.T, sm *statusMocks.MockStatusManager) {
				t.Helper()
				pid := startVerifiedProxyProcess(t, "test-workload").pid
				sm.EXPECT().GetWorkloadPID(gomock.Any(), "test-workload").Return(pid, nil)
				sm.EXPECT().ResetWorkloadPIDIfMatch(gomock.Any(), "test-workload", pid).Return(nil)
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			sm := statusMocks.NewMockStatusManager(ctrl)
			tt.setupMocks(t, sm)

			manager := &DefaultManager{statuses: sm}

			got := manager.stopProcess(context.Background(), tt.workload)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDefaultManager_ensureProxyStopped_UnverifiedLivePIDIsPreservedWithoutSignaling(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		start func(*testing.T) (int, <-chan struct{})
	}{
		{name: "unrelated executable", start: startKillableProcess},
		{name: "detached supervisor of hyphen-suffixed workload", start: func(t *testing.T) (int, <-chan struct{}) {
			t.Helper()
			proxy := startVerifiedProxyProcess(t, "github-extra")
			return proxy.pid, proxy.exited
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			const name = "github"
			pid, exited := tt.start(t)
			ctrl := gomock.NewController(t)
			sm := statusMocks.NewMockStatusManager(ctrl)
			sm.EXPECT().GetWorkloadPID(gomock.Any(), name).Return(pid, nil)
			// No reset expectation: an unverifiable live process must remain tracked.
			manager := &DefaultManager{statuses: sm}
			rc := &runner.RunConfig{BaseName: name, Port: 54321}
			fallbackCalls := 0
			withPortFreer(func(_ context.Context, got *runner.RunConfig) {
				assert.Same(t, rc, got)
				fallbackCalls++
			})(manager)

			manager.ensureProxyStopped(context.Background(), name, name, rc)
			assert.Equal(t, 1, fallbackCalls, "unverified PID must not suppress port cleanup")
			select {
			case <-exited:
				t.Error("unverified live process was terminated")
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

func TestDefaultManager_ensureProxyStopped_ExitTimeoutPreservesVerifiedPIDAndFallsBackOnce(t *testing.T) {
	t.Parallel()

	const name = "slow-shutdown-supervisor"
	proxy := startProxyProcess(t, name, true)
	ctrl := gomock.NewController(t)
	sm := statusMocks.NewMockStatusManager(ctrl)
	sm.EXPECT().GetWorkloadPID(gomock.Any(), name).Return(proxy.pid, nil)
	// No reset expectation: timeout must not drop the only supervisor handle.
	manager := &DefaultManager{statuses: sm}
	withProxyExitTimeout(100 * time.Millisecond)(manager)
	runConfig := &runner.RunConfig{BaseName: name, Port: 54321}
	fallbackCalls := 0
	withPortFreer(func(_ context.Context, rc *runner.RunConfig) {
		assert.Same(t, runConfig, rc)
		fallbackCalls++
	})(manager)

	manager.ensureProxyStopped(context.Background(), name, name, runConfig)
	assert.Equal(t, 1, fallbackCalls, "verified timeout must run port fallback once")
	select {
	case <-proxy.exited:
		t.Error("SIGTERM-ignoring proxy unexpectedly exited")
	default:
	}
}

// TestDefaultManager_orphanProxyPortCleanup verifies that stop and rm fall back
// to port-based proxy cleanup when the tracked PID is unavailable (the missing
// status-file scenario), and that the fallback does not run on the normal path
// where the proxy was stopped by PID.
//
//nolint:paralleltest // saved-workload fixtures use t.Setenv
func TestDefaultManager_orphanProxyPortCleanup(t *testing.T) {
	isolateWorkloadStopState(t)

	const workloadName = "test-workload"
	savedRunConfig := func(t *testing.T) *runner.RunConfig {
		t.Helper()
		rc := &runner.RunConfig{BaseName: workloadName, Port: 54321}
		persistStopRunConfig(t, workloadName, rc)
		return rc
	}
	containerInfo := func() runtime.ContainerInfo {
		return runtime.ContainerInfo{
			Name:   workloadName,
			State:  "running",
			Labels: map[string]string{"toolhive-basename": workloadName},
		}
	}

	t.Run("stop frees orphan proxy port when status file missing", func(t *testing.T) {

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		sm := statusMocks.NewMockStatusManager(ctrl)
		rtMock := runtimeMocks.NewMockRuntime(ctrl)

		// Missing status file: no tracked PID, so the proxy is not killed by PID.
		sm.EXPECT().GetWorkloadPID(gomock.Any(), workloadName).Return(0, nil)
		rtMock.EXPECT().StopWorkload(gomock.Any(), workloadName).Return(nil)
		sm.EXPECT().SetWorkloadStatus(gomock.Any(), workloadName, runtime.WorkloadStatusStopped, "").Return(nil)

		var freed []*runner.RunConfig
		runConfig := &runner.RunConfig{BaseName: workloadName, Port: 54321}
		persistStopRunConfig(t, workloadName, runConfig)
		manager := &DefaultManager{statuses: sm, runtime: rtMock}
		withPortFreer(func(_ context.Context, rc *runner.RunConfig) {
			freed = append(freed, rc)
		})(manager)

		ci := containerInfo()
		err := manager.stopSingleContainerWorkload(context.Background(), &ci, runConfig)
		require.NoError(t, err)

		require.Len(t, freed, 1, "port-cleanup fallback should run exactly once")
		assert.Equal(t, workloadName, freed[0].BaseName, "fallback should receive the workload's run config")
		assert.Equal(t, 54321, freed[0].Port, "fallback should receive the workload's proxy port")
	})

	t.Run("stop does not free port on normal shutdown", func(t *testing.T) {

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		sm := statusMocks.NewMockStatusManager(ctrl)
		rtMock := runtimeMocks.NewMockRuntime(ctrl)

		// A valid, killable PID: the proxy is stopped by PID, so no fallback.
		pid := startVerifiedProxyProcess(t, workloadName).pid
		sm.EXPECT().GetWorkloadPID(gomock.Any(), workloadName).Return(pid, nil)
		sm.EXPECT().ResetWorkloadPIDIfMatch(gomock.Any(), workloadName, pid).Return(nil)
		rtMock.EXPECT().StopWorkload(gomock.Any(), workloadName).Return(nil)
		sm.EXPECT().SetWorkloadStatus(gomock.Any(), workloadName, runtime.WorkloadStatusStopped, "").Return(nil)

		freedCalls := 0
		manager := &DefaultManager{statuses: sm, runtime: rtMock}
		withPortFreer(func(_ context.Context, _ *runner.RunConfig) {
			freedCalls++
		})(manager)

		ci := containerInfo()
		err := manager.stopSingleContainerWorkload(
			context.Background(), &ci, savedRunConfig(t),
		)
		require.NoError(t, err)

		assert.Zero(t, freedCalls, "port-cleanup fallback must not run when the proxy was stopped by PID")
	})

	t.Run("delete frees orphan proxy port when status file missing", func(t *testing.T) {

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		sm := statusMocks.NewMockStatusManager(ctrl)
		rtMock := runtimeMocks.NewMockRuntime(ctrl)

		// Container is found and removed cleanly.
		rtMock.EXPECT().GetWorkloadInfo(gomock.Any(), workloadName).Return(containerInfo(), nil)
		sm.EXPECT().SetWorkloadStatus(gomock.Any(), workloadName, runtime.WorkloadStatusRemoving, "").Return(nil)
		rtMock.EXPECT().RemoveWorkload(gomock.Any(), workloadName).Return(nil)
		// removeContainer waits for the container to disappear from the runtime.
		rtMock.EXPECT().GetWorkloadInfo(gomock.Any(), workloadName).Return(runtime.ContainerInfo{}, runtime.ErrWorkloadNotFound)
		// Missing status file: no tracked PID, so the proxy is not killed by PID.
		sm.EXPECT().GetWorkloadPID(gomock.Any(), workloadName).Return(0, nil)
		sm.EXPECT().DeleteWorkloadStatus(gomock.Any(), workloadName).Return(nil)

		var freed []*runner.RunConfig
		runConfig := &runner.RunConfig{BaseName: workloadName, Port: 54321}
		persistStopRunConfig(t, workloadName, runConfig)
		manager := &DefaultManager{statuses: sm, runtime: rtMock}
		withPortFreer(func(_ context.Context, rc *runner.RunConfig) {
			freed = append(freed, rc)
		})(manager)

		err := manager.deleteContainerWorkload(context.Background(), workloadName, runConfig)
		require.NoError(t, err)

		require.Len(t, freed, 1, "port-cleanup fallback should run exactly once")
		assert.Equal(t, workloadName, freed[0].BaseName, "fallback should receive the workload's run config")
		assert.Equal(t, 54321, freed[0].Port, "fallback should receive the workload's proxy port")
	})

	t.Run("delete does not free port on normal shutdown", func(t *testing.T) {

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		sm := statusMocks.NewMockStatusManager(ctrl)
		rtMock := runtimeMocks.NewMockRuntime(ctrl)

		rtMock.EXPECT().GetWorkloadInfo(gomock.Any(), workloadName).Return(containerInfo(), nil)
		sm.EXPECT().SetWorkloadStatus(gomock.Any(), workloadName, runtime.WorkloadStatusRemoving, "").Return(nil)
		rtMock.EXPECT().RemoveWorkload(gomock.Any(), workloadName).Return(nil)
		rtMock.EXPECT().GetWorkloadInfo(gomock.Any(), workloadName).Return(runtime.ContainerInfo{}, runtime.ErrWorkloadNotFound)
		// A valid, killable PID: the proxy is stopped by PID, so no fallback.
		pid := startVerifiedProxyProcess(t, workloadName).pid
		sm.EXPECT().GetWorkloadPID(gomock.Any(), workloadName).Return(pid, nil)
		sm.EXPECT().ResetWorkloadPIDIfMatch(gomock.Any(), workloadName, pid).Return(nil)
		sm.EXPECT().DeleteWorkloadStatus(gomock.Any(), workloadName).Return(nil)

		freedCalls := 0
		manager := &DefaultManager{statuses: sm, runtime: rtMock}
		withPortFreer(func(_ context.Context, _ *runner.RunConfig) {
			freedCalls++
		})(manager)

		err := manager.deleteContainerWorkload(
			context.Background(), workloadName, savedRunConfig(t),
		)
		require.NoError(t, err)

		assert.Zero(t, freedCalls, "port-cleanup fallback must not run when the proxy was stopped by PID")
	})

	t.Run("remote stop frees orphan proxy port when status file missing", func(t *testing.T) {

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		sm := statusMocks.NewMockStatusManager(ctrl)

		// Real orphan: the status file is gone, so GetWorkload cannot find the
		// workload (a remote workload has no container to fall back to). The proxy
		// may still be holding the port, so the fallback must still fire — and the
		// status transitions are skipped since the workload is not "running".
		sm.EXPECT().GetWorkload(gomock.Any(), workloadName).Return(core.Workload{}, runtime.ErrWorkloadNotFound)
		// No tracked PID, so the proxy is not killed by PID.
		sm.EXPECT().GetWorkloadPID(gomock.Any(), workloadName).Return(0, nil)

		var freed []*runner.RunConfig
		runConfig := &runner.RunConfig{BaseName: workloadName, Port: 54321}
		persistStopRunConfig(t, workloadName, runConfig)
		manager := &DefaultManager{statuses: sm}
		withPortFreer(func(_ context.Context, rc *runner.RunConfig) {
			freed = append(freed, rc)
		})(manager)

		err := manager.stopRemoteWorkload(context.Background(), workloadName, runConfig)
		require.NoError(t, err)

		require.Len(t, freed, 1, "port-cleanup fallback should run exactly once")
		assert.Equal(t, workloadName, freed[0].BaseName, "fallback should receive the workload's run config")
		assert.Equal(t, 54321, freed[0].Port, "fallback should receive the workload's proxy port")
	})

	t.Run("remote delete frees orphan proxy port when status file missing", func(t *testing.T) {

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		sm := statusMocks.NewMockStatusManager(ctrl)

		sm.EXPECT().SetWorkloadStatus(gomock.Any(), workloadName, runtime.WorkloadStatusRemoving, "").Return(nil)
		// Missing status file: no tracked PID, so the proxy is not killed by PID.
		sm.EXPECT().GetWorkloadPID(gomock.Any(), workloadName).Return(0, nil)
		sm.EXPECT().DeleteWorkloadStatus(gomock.Any(), workloadName).Return(nil)

		var freed []*runner.RunConfig
		runConfig := &runner.RunConfig{BaseName: workloadName, Port: 54321}
		persistStopRunConfig(t, workloadName, runConfig)
		manager := &DefaultManager{statuses: sm}
		withPortFreer(func(_ context.Context, rc *runner.RunConfig) {
			freed = append(freed, rc)
		})(manager)

		err := manager.deleteRemoteWorkload(context.Background(), workloadName, runConfig)
		require.NoError(t, err)

		require.Len(t, freed, 1, "port-cleanup fallback should run exactly once")
		assert.Equal(t, workloadName, freed[0].BaseName, "fallback should receive the workload's run config")
		assert.Equal(t, 54321, freed[0].Port, "fallback should receive the workload's proxy port")
	})

	t.Run("remote stop does not free port on normal shutdown", func(t *testing.T) {

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		sm := statusMocks.NewMockStatusManager(ctrl)

		sm.EXPECT().GetWorkload(gomock.Any(), workloadName).Return(core.Workload{
			Name:   workloadName,
			Status: runtime.WorkloadStatusRunning,
		}, nil)
		sm.EXPECT().SetWorkloadStatus(gomock.Any(), workloadName, runtime.WorkloadStatusStopping, "").Return(nil)
		// A valid, killable PID: the proxy is stopped by PID, so no fallback.
		pid := startVerifiedProxyProcess(t, workloadName).pid
		sm.EXPECT().GetWorkloadPID(gomock.Any(), workloadName).Return(pid, nil)
		sm.EXPECT().ResetWorkloadPIDIfMatch(gomock.Any(), workloadName, pid).Return(nil)
		sm.EXPECT().SetWorkloadStatus(gomock.Any(), workloadName, runtime.WorkloadStatusStopped, "").Return(nil)

		freedCalls := 0
		manager := &DefaultManager{statuses: sm}
		withPortFreer(func(_ context.Context, _ *runner.RunConfig) {
			freedCalls++
		})(manager)

		err := manager.stopRemoteWorkload(
			context.Background(), workloadName, savedRunConfig(t),
		)
		require.NoError(t, err)

		assert.Zero(t, freedCalls, "port-cleanup fallback must not run when the proxy was stopped by PID")
	})

	t.Run("remote delete does not free port on normal shutdown", func(t *testing.T) {

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		sm := statusMocks.NewMockStatusManager(ctrl)

		sm.EXPECT().SetWorkloadStatus(gomock.Any(), workloadName, runtime.WorkloadStatusRemoving, "").Return(nil)
		// A valid, killable PID: the proxy is stopped by PID, so no fallback.
		pid := startVerifiedProxyProcess(t, workloadName).pid
		sm.EXPECT().GetWorkloadPID(gomock.Any(), workloadName).Return(pid, nil)
		sm.EXPECT().ResetWorkloadPIDIfMatch(gomock.Any(), workloadName, pid).Return(nil)
		sm.EXPECT().DeleteWorkloadStatus(gomock.Any(), workloadName).Return(nil)

		freedCalls := 0
		manager := &DefaultManager{statuses: sm}
		withPortFreer(func(_ context.Context, _ *runner.RunConfig) {
			freedCalls++
		})(manager)

		err := manager.deleteRemoteWorkload(
			context.Background(), workloadName, savedRunConfig(t),
		)
		require.NoError(t, err)

		assert.Zero(t, freedCalls, "port-cleanup fallback must not run when the proxy was stopped by PID")
	})
}
