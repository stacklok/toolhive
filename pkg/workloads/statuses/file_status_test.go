package statuses

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/container/runtime/mocks"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/logger"
)

func init() {
	// Initialize logger for all tests
	logger.Initialize()
}

func TestFileStatusManager_SetWorkloadStatus_Create(t *testing.T) {
	t.Parallel()
	// Create temporary directory for tests
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	// Test creating a new workload status
	err := manager.SetWorkloadStatus(ctx, "test-workload", rt.WorkloadStatusStarting, "")
	require.NoError(t, err)

	// Verify file was created
	statusFile := filepath.Join(tempDir, "test-workload.json")
	require.FileExists(t, statusFile)

	// Verify file contents
	data, err := os.ReadFile(statusFile)
	require.NoError(t, err)

	var statusFileData workloadStatusFile
	err = json.Unmarshal(data, &statusFileData)
	require.NoError(t, err)

	assert.Equal(t, rt.WorkloadStatusStarting, statusFileData.Status)
	assert.Empty(t, statusFileData.StatusContext)
	assert.False(t, statusFileData.CreatedAt.IsZero())
	assert.False(t, statusFileData.UpdatedAt.IsZero())
}

func TestFileStatusManager_SetWorkloadStatus_Update(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	// Create workload first time
	err := manager.SetWorkloadStatus(ctx, "test-workload", rt.WorkloadStatusStarting, "")
	require.NoError(t, err)

	// Create again - should just update, not fail
	err = manager.SetWorkloadStatus(ctx, "test-workload", rt.WorkloadStatusRunning, "updated")
	assert.NoError(t, err)
}

func TestFileStatusManager_GetWorkload(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	mockRuntime := mocks.NewMockRuntime(ctrl)
	manager := &fileStatusManager{
		baseDir: tempDir,
		runtime: mockRuntime,
	}
	ctx := context.Background()

	// Create a workload status
	err := manager.SetWorkloadStatus(ctx, "test-workload", rt.WorkloadStatusStarting, "")
	require.NoError(t, err)

	// Get the workload (no runtime call expected for starting workload)
	workload, err := manager.GetWorkload(ctx, "test-workload")
	require.NoError(t, err)
	assert.Equal(t, "test-workload", workload.Name)
	assert.Equal(t, rt.WorkloadStatusStarting, workload.Status)
	assert.Empty(t, workload.StatusContext)
}

func TestFileStatusManager_GetWorkload_NotFound(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	mockRuntime := mocks.NewMockRuntime(ctrl)
	manager := &fileStatusManager{
		baseDir: tempDir,
		runtime: mockRuntime,
	}
	ctx := context.Background()

	// Mock runtime to return error for non-existent workload
	mockRuntime.EXPECT().GetWorkloadInfo(gomock.Any(), "non-existent").Return(rt.ContainerInfo{}, errors.New("workload not found in runtime"))

	// Try to get workload for non-existent workload - should now fall back to runtime
	_, err := manager.GetWorkload(ctx, "non-existent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "workload not found in runtime")
}

func TestFileStatusManager_GetWorkload_RuntimeFallback(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	mockRuntime := mocks.NewMockRuntime(ctrl)
	manager := &fileStatusManager{
		baseDir: tempDir,
		runtime: mockRuntime,
	}
	ctx := context.Background()

	// Mock runtime to return a workload when file doesn't exist
	info := rt.ContainerInfo{
		Name:    "runtime-only-workload",
		Image:   "test-image:latest",
		Status:  "running",
		State:   rt.WorkloadStatusRunning,
		Created: time.Now(),
		Labels: map[string]string{
			"toolhive":           "true",
			"toolhive-name":      "runtime-only-workload",
			"toolhive-transport": "sse",
			"toolhive-port":      "8080",
			"toolhive-tool-type": "mcp",
		},
	}
	mockRuntime.EXPECT().GetWorkloadInfo(gomock.Any(), "runtime-only-workload").Return(info, nil)

	// Get workload that exists only in runtime
	workload, err := manager.GetWorkload(ctx, "runtime-only-workload")
	require.NoError(t, err)
	assert.Equal(t, "runtime-only-workload", workload.Name)
	assert.Equal(t, rt.WorkloadStatusRunning, workload.Status)
	assert.Equal(t, "test-image:latest", workload.Package)
}

func TestFileStatusManager_GetWorkload_FileAndRuntimeCombination(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	mockRuntime := mocks.NewMockRuntime(ctrl)
	manager := &fileStatusManager{
		baseDir: tempDir,
		runtime: mockRuntime,
	}
	ctx := context.Background()

	// Create a workload status file and set it to running
	err := manager.SetWorkloadStatus(ctx, "running-workload", rt.WorkloadStatusStarting, "")
	require.NoError(t, err)
	manager.SetWorkloadStatus(ctx, "running-workload", rt.WorkloadStatusRunning, "container started")

	// Mock runtime to return detailed info for running workload
	info := rt.ContainerInfo{
		Name:    "running-workload",
		Image:   "test-image:latest",
		Status:  "Up 5 minutes",
		State:   rt.WorkloadStatusRunning,
		Created: time.Now(),
		Labels: map[string]string{
			"toolhive":           "true",
			"toolhive-name":      "running-workload",
			"toolhive-transport": "sse",
			"toolhive-port":      "8080",
			"toolhive-tool-type": "mcp",
			"custom-label":       "value1",
		},
	}
	mockRuntime.EXPECT().GetWorkloadInfo(gomock.Any(), "running-workload").Return(info, nil)

	// Get workload - should combine file status with runtime info
	workload, err := manager.GetWorkload(ctx, "running-workload")
	require.NoError(t, err)

	// Should preserve file-based status but get runtime details
	assert.Equal(t, "running-workload", workload.Name)
	assert.Equal(t, rt.WorkloadStatusRunning, workload.Status)   // From file
	assert.Equal(t, "container started", workload.StatusContext) // From file
	assert.Equal(t, "test-image:latest", workload.Package)       // From runtime
	assert.Equal(t, 8080, workload.Port)                         // From runtime
	assert.Contains(t, workload.Labels, "custom-label")          // From runtime
}

func TestFileStatusManager_SetWorkloadStatus(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	// Create a workload status
	err := manager.SetWorkloadStatus(ctx, "test-workload", rt.WorkloadStatusStarting, "")
	require.NoError(t, err)

	// Update the status
	manager.SetWorkloadStatus(ctx, "test-workload", rt.WorkloadStatusRunning, "container started")

	// Note: Cannot verify status was updated via GetWorkload since current implementation returns empty Workload
	// Instead verify by reading the file directly

	// Verify the file on disk
	statusFile := filepath.Join(tempDir, "test-workload.json")
	data, err := os.ReadFile(statusFile)
	require.NoError(t, err)

	var statusFileData workloadStatusFile
	err = json.Unmarshal(data, &statusFileData)
	require.NoError(t, err)

	assert.Equal(t, rt.WorkloadStatusRunning, statusFileData.Status)
	assert.Equal(t, "container started", statusFileData.StatusContext)
	// CreatedAt should be preserved, UpdatedAt should be newer
	assert.False(t, statusFileData.CreatedAt.IsZero())
	assert.False(t, statusFileData.UpdatedAt.IsZero())
	assert.True(t, statusFileData.UpdatedAt.After(statusFileData.CreatedAt) ||
		statusFileData.UpdatedAt.Equal(statusFileData.CreatedAt))
}

func TestFileStatusManager_SetWorkloadStatus_NotFound(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	// Try to set status for non-existent workload - creates file since no runtime check
	err := manager.SetWorkloadStatus(ctx, "non-existent", rt.WorkloadStatusRunning, "test")
	require.NoError(t, err)

	// Verify file was created (current behavior creates files regardless)
	statusFile := filepath.Join(tempDir, "non-existent.json")
	assert.FileExists(t, statusFile)

	// Verify file contents
	data, err := os.ReadFile(statusFile)
	require.NoError(t, err)

	var statusFileData workloadStatusFile
	err = json.Unmarshal(data, &statusFileData)
	require.NoError(t, err)

	assert.Equal(t, rt.WorkloadStatusRunning, statusFileData.Status)
	assert.Equal(t, "test", statusFileData.StatusContext)
	assert.False(t, statusFileData.CreatedAt.IsZero())
	assert.False(t, statusFileData.UpdatedAt.IsZero())
}

func TestFileStatusManager_DeleteWorkloadStatus(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	// Create a workload status
	err := manager.SetWorkloadStatus(ctx, "test-workload", rt.WorkloadStatusStarting, "")
	require.NoError(t, err)

	statusFile := filepath.Join(tempDir, "test-workload.json")
	require.FileExists(t, statusFile)

	// Delete the status
	err = manager.DeleteWorkloadStatus(ctx, "test-workload")
	require.NoError(t, err)

	// Verify file was deleted
	assert.NoFileExists(t, statusFile)
}

func TestFileStatusManager_DeleteWorkloadStatus_NotFound(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	// Try to delete non-existent workload - should not error
	err := manager.DeleteWorkloadStatus(ctx, "non-existent")
	assert.NoError(t, err)
}

func TestFileStatusManager_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	mockRuntime := mocks.NewMockRuntime(ctrl)
	manager := &fileStatusManager{
		baseDir: tempDir,
		runtime: mockRuntime,
	}
	ctx := context.Background()

	// Create a workload status
	err := manager.SetWorkloadStatus(ctx, "test-workload", rt.WorkloadStatusStarting, "")
	require.NoError(t, err)

	// Test concurrent reads - should not conflict
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- true }()
			workload, err := manager.GetWorkload(ctx, "test-workload")
			assert.NoError(t, err)
			assert.Equal(t, "test-workload", workload.Name)
			assert.Equal(t, rt.WorkloadStatusStarting, workload.Status)
		}()
	}

	// Wait for all reads to complete
	for i := 0; i < 10; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for concurrent reads")
		}
	}
}

func TestFileStatusManager_FullLifecycle(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	mockRuntime := mocks.NewMockRuntime(ctrl)
	manager := &fileStatusManager{
		baseDir: tempDir,
		runtime: mockRuntime,
	}
	ctx := context.Background()

	workloadName := "lifecycle-test"

	// 1. Create workload
	err := manager.SetWorkloadStatus(ctx, workloadName, rt.WorkloadStatusStarting, "")
	require.NoError(t, err)

	// 2. Update to running
	manager.SetWorkloadStatus(ctx, workloadName, rt.WorkloadStatusRunning, "started successfully")

	// 3. Update to stopping
	manager.SetWorkloadStatus(ctx, workloadName, rt.WorkloadStatusStopping, "shutdown initiated")

	// 4. Update to stopped
	manager.SetWorkloadStatus(ctx, workloadName, rt.WorkloadStatusStopped, "shutdown complete")

	// 5. Delete workload
	err = manager.DeleteWorkloadStatus(ctx, workloadName)
	require.NoError(t, err)

	// Mock runtime to return error for deleted workload (now falls back to runtime)
	mockRuntime.EXPECT().GetWorkloadInfo(gomock.Any(), workloadName).Return(rt.ContainerInfo{}, errors.New("workload not found in runtime"))

	// Verify GetWorkload properly returns an error for deleted workload
	_, err = manager.GetWorkload(ctx, workloadName)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "workload not found in runtime")
}

func TestFileStatusManager_ListWorkloads(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		setup            func(*fileStatusManager) error
		listAll          bool
		labelFilters     []string
		setupRuntimeMock func(*mocks.MockRuntime)
		expectedCount    int
		expectedError    string
		checkWorkloads   func([]core.Workload)
	}{
		{
			name:    "empty directory",
			setup:   func(_ *fileStatusManager) error { return nil },
			listAll: true,
			setupRuntimeMock: func(m *mocks.MockRuntime) {
				// Runtime is always called, even for empty directory
				m.EXPECT().ListWorkloads(gomock.Any()).Return([]rt.ContainerInfo{}, nil)
			},
			expectedCount: 0,
		},
		{
			name: "single starting workload",
			setup: func(f *fileStatusManager) error {
				ctx := context.Background()
				return f.SetWorkloadStatus(ctx, "test-workload", rt.WorkloadStatusStarting, "")
			},
			listAll: true,
			setupRuntimeMock: func(m *mocks.MockRuntime) {
				// Runtime is always called - return empty for file-only workload
				m.EXPECT().ListWorkloads(gomock.Any()).Return([]rt.ContainerInfo{}, nil)
			},
			expectedCount: 1,
			checkWorkloads: func(workloads []core.Workload) {
				assert.Equal(t, "test-workload", workloads[0].Name)
				assert.Equal(t, rt.WorkloadStatusStarting, workloads[0].Status)
			},
		},
		{
			name: "mixed workload statuses with listAll=false",
			setup: func(f *fileStatusManager) error {
				ctx := context.Background()
				// Create a starting workload
				if err := f.SetWorkloadStatus(ctx, "starting-workload", rt.WorkloadStatusStarting, ""); err != nil {
					return err
				}
				// Create a running workload
				if err := f.SetWorkloadStatus(ctx, "running-workload", rt.WorkloadStatusStarting, ""); err != nil {
					return err
				}
				f.SetWorkloadStatus(ctx, "running-workload", rt.WorkloadStatusRunning, "container started")
				return nil
			},
			listAll: false, // Only running workloads
			setupRuntimeMock: func(m *mocks.MockRuntime) {
				// Mock runtime call for running workload
				info := rt.ContainerInfo{
					Name:   "running-workload",
					Image:  "test-image:latest",
					Status: "running",
					State:  rt.WorkloadStatusRunning,
					Labels: map[string]string{
						"toolhive":           "true",
						"toolhive-name":      "running-workload",
						"toolhive-transport": "sse",
						"toolhive-port":      "8080",
						"toolhive-tool-type": "mcp",
					},
				}
				m.EXPECT().ListWorkloads(gomock.Any()).Return([]rt.ContainerInfo{info}, nil)
			},
			expectedCount: 1,
			checkWorkloads: func(workloads []core.Workload) {
				assert.Equal(t, "running-workload", workloads[0].Name)
				assert.Equal(t, rt.WorkloadStatusRunning, workloads[0].Status)
			},
		},
		{
			name: "mixed workload statuses with listAll=true",
			setup: func(f *fileStatusManager) error {
				ctx := context.Background()
				// Create a starting workload
				if err := f.SetWorkloadStatus(ctx, "starting-workload", rt.WorkloadStatusStarting, ""); err != nil {
					return err
				}
				// Create a running workload
				if err := f.SetWorkloadStatus(ctx, "running-workload", rt.WorkloadStatusStarting, ""); err != nil {
					return err
				}
				f.SetWorkloadStatus(ctx, "running-workload", rt.WorkloadStatusRunning, "container started")
				return nil
			},
			listAll: true, // All workloads
			setupRuntimeMock: func(m *mocks.MockRuntime) {
				// Mock runtime call for running workload
				info := rt.ContainerInfo{
					Name:   "running-workload",
					Image:  "test-image:latest",
					Status: "running",
					State:  rt.WorkloadStatusRunning,
					Labels: map[string]string{
						"toolhive":           "true",
						"toolhive-name":      "running-workload",
						"toolhive-transport": "sse",
						"toolhive-port":      "8080",
						"toolhive-tool-type": "mcp",
					},
				}
				m.EXPECT().ListWorkloads(gomock.Any()).Return([]rt.ContainerInfo{info}, nil)
			},
			expectedCount: 2,
		},
		{
			name: "invalid label filter",
			setup: func(f *fileStatusManager) error {
				ctx := context.Background()
				return f.SetWorkloadStatus(ctx, "test-workload", rt.WorkloadStatusStarting, "")
			},
			listAll:      true,
			labelFilters: []string{"invalid-filter"},
			setupRuntimeMock: func(_ *mocks.MockRuntime) {
				// Runtime is not called due to early filter parsing error
			},
			expectedError: "failed to parse label filters",
		},
		{
			name: "merge runtime and file workloads",
			setup: func(f *fileStatusManager) error {
				ctx := context.Background()
				// Create file workload that will merge with runtime
				if err := f.SetWorkloadStatus(ctx, "merge-workload", rt.WorkloadStatusStarting, ""); err != nil {
					return err
				}
				f.SetWorkloadStatus(ctx, "merge-workload", rt.WorkloadStatusStopping, "shutting down")
				return nil
			},
			listAll: true,
			setupRuntimeMock: func(m *mocks.MockRuntime) {
				containers := []rt.ContainerInfo{
					{
						Name:   "merge-workload",
						Image:  "runtime-image:latest",
						Status: "Up 1 hour",
						State:  rt.WorkloadStatusRunning, // Runtime says running
						Labels: map[string]string{
							"toolhive":           "true",
							"toolhive-name":      "merge-workload",
							"toolhive-transport": "http",
							"toolhive-port":      "9090",
							"toolhive-tool-type": "mcp",
							"runtime-label":      "runtime-value",
						},
					},
					{
						Name:   "runtime-only-workload",
						Image:  "runtime-only:latest",
						Status: "Up 30 minutes",
						State:  rt.WorkloadStatusRunning,
						Labels: map[string]string{
							"toolhive":           "true",
							"toolhive-name":      "runtime-only-workload",
							"toolhive-transport": "sse",
							"toolhive-port":      "8080",
							"toolhive-tool-type": "mcp",
						},
					},
				}
				m.EXPECT().ListWorkloads(gomock.Any()).Return(containers, nil)
			},
			expectedCount: 2,
			checkWorkloads: func(workloads []core.Workload) {
				// Find the merged workload
				var mergedWorkload *core.Workload
				var runtimeOnlyWorkload *core.Workload
				for i := range workloads {
					switch workloads[i].Name {
					case "merge-workload":
						mergedWorkload = &workloads[i]
					case "runtime-only-workload":
						runtimeOnlyWorkload = &workloads[i]
					}
				}

				require.NotNil(t, mergedWorkload, "merged workload should exist")
				require.NotNil(t, runtimeOnlyWorkload, "runtime-only workload should exist")

				// Merged workload should prefer file status but have runtime details
				assert.Equal(t, "merge-workload", mergedWorkload.Name)
				assert.Equal(t, rt.WorkloadStatusStopping, mergedWorkload.Status) // From file
				assert.Equal(t, "shutting down", mergedWorkload.StatusContext)    // From file
				assert.Equal(t, "runtime-image:latest", mergedWorkload.Package)   // From runtime
				assert.Equal(t, 9090, mergedWorkload.Port)                        // From runtime
				assert.Contains(t, mergedWorkload.Labels, "runtime-label")        // From runtime

				// Runtime-only workload should be normal
				assert.Equal(t, "runtime-only-workload", runtimeOnlyWorkload.Name)
				assert.Equal(t, rt.WorkloadStatusRunning, runtimeOnlyWorkload.Status)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			tempDir := t.TempDir()
			mockRuntime := mocks.NewMockRuntime(ctrl)
			tt.setupRuntimeMock(mockRuntime)

			manager := &fileStatusManager{
				baseDir: tempDir,
				runtime: mockRuntime,
			}

			// Setup test data
			err := tt.setup(manager)
			require.NoError(t, err)

			ctx := context.Background()
			workloads, err := manager.ListWorkloads(ctx, tt.listAll, tt.labelFilters)

			if tt.expectedError != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
				assert.Nil(t, workloads)
			} else {
				assert.NoError(t, err)
				assert.Len(t, workloads, tt.expectedCount)
				if tt.checkWorkloads != nil {
					tt.checkWorkloads(workloads)
				}
			}
		})
	}
}

func TestFileStatusManager_GetWorkload_UnhealthyDetection(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	mockRuntime := mocks.NewMockRuntime(ctrl)
	manager := &fileStatusManager{
		baseDir: tempDir,
		runtime: mockRuntime,
	}
	ctx := context.Background()

	// First, set the workload status to running in the file
	err := manager.SetWorkloadStatus(ctx, "test-workload", rt.WorkloadStatusRunning, "container started")
	require.NoError(t, err)

	// Mock the runtime to return a stopped workload (mismatch with file)
	stoppedInfo := rt.ContainerInfo{
		Name:    "test-workload",
		Image:   "test-image:latest",
		Status:  "Exited (0) 2 minutes ago",
		State:   rt.WorkloadStatusStopped, // Runtime says stopped
		Created: time.Now().Add(-10 * time.Minute),
		Labels: map[string]string{
			"toolhive":      "true",
			"toolhive-name": "test-workload",
		},
	}

	mockRuntime.EXPECT().
		GetWorkloadInfo(gomock.Any(), "test-workload").
		Return(stoppedInfo, nil)

	// Mock the call to SetWorkloadStatus that will be made to update to unhealthy
	// This is tricky because we need to intercept the call but allow it to proceed
	// For simplicity, we'll just allow the call to succeed
	mockRuntime.EXPECT().
		GetWorkloadInfo(gomock.Any(), "test-workload").
		Return(stoppedInfo, nil).
		AnyTimes() // Allow multiple calls during the SetWorkloadStatus operation

	// Get the workload - this should detect the mismatch and return unhealthy status
	workload, err := manager.GetWorkload(ctx, "test-workload")
	require.NoError(t, err)

	// Verify the workload is marked as unhealthy
	assert.Equal(t, "test-workload", workload.Name)
	assert.Equal(t, rt.WorkloadStatusUnhealthy, workload.Status)
	assert.Contains(t, workload.StatusContext, "workload status mismatch")
	assert.Contains(t, workload.StatusContext, "file indicates running")
	assert.Contains(t, workload.StatusContext, "runtime shows stopped")
	assert.Equal(t, "test-image:latest", workload.Package)

	// Verify the file was updated to unhealthy status
	// Get the workload again (this time without runtime mismatch since status is now unhealthy)
	statusFilePath := filepath.Join(tempDir, "test-workload.json")
	data, err := os.ReadFile(statusFilePath)
	require.NoError(t, err)

	var statusFile workloadStatusFile
	err = json.Unmarshal(data, &statusFile)
	require.NoError(t, err)

	assert.Equal(t, rt.WorkloadStatusUnhealthy, statusFile.Status)
	assert.Contains(t, statusFile.StatusContext, "workload status mismatch")
}

func TestFileStatusManager_GetWorkload_HealthyRunningWorkload(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	mockRuntime := mocks.NewMockRuntime(ctrl)
	manager := &fileStatusManager{
		baseDir: tempDir,
		runtime: mockRuntime,
	}
	ctx := context.Background()

	// Set the workload status to running in the file
	err := manager.SetWorkloadStatus(ctx, "healthy-workload", rt.WorkloadStatusRunning, "container started")
	require.NoError(t, err)

	// Mock the runtime to return a running workload (matches file)
	runningInfo := rt.ContainerInfo{
		Name:    "healthy-workload",
		Image:   "test-image:latest",
		Status:  "Up 5 minutes",
		State:   rt.WorkloadStatusRunning, // Runtime says running (matches file)
		Created: time.Now().Add(-10 * time.Minute),
		Labels: map[string]string{
			"toolhive":      "true",
			"toolhive-name": "healthy-workload",
		},
	}

	mockRuntime.EXPECT().
		GetWorkloadInfo(gomock.Any(), "healthy-workload").
		Return(runningInfo, nil)

	// Get the workload - this should remain running since file and runtime match
	workload, err := manager.GetWorkload(ctx, "healthy-workload")
	require.NoError(t, err)

	// Verify the workload remains running
	assert.Equal(t, "healthy-workload", workload.Name)
	assert.Equal(t, rt.WorkloadStatusRunning, workload.Status)
	assert.Equal(t, "container started", workload.StatusContext) // Original file context preserved
	assert.Equal(t, "test-image:latest", workload.Package)
}

func TestFileStatusManager_GetWorkload_ProxyNotRunning(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	mockRuntime := mocks.NewMockRuntime(ctrl)

	// Create file status manager directly instead of using NewFileStatusManager
	manager := &fileStatusManager{
		baseDir: tempDir,
		runtime: mockRuntime,
	}
	ctx := context.Background()

	// First, create a status file manually to ensure file is found
	statusFile := workloadStatusFile{
		Status:        rt.WorkloadStatusRunning,
		StatusContext: "container started",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	statusFilePath := filepath.Join(tempDir, "proxy-down-workload.json")
	statusData, err := json.Marshal(statusFile)
	require.NoError(t, err)
	err = os.WriteFile(statusFilePath, statusData, 0644)
	require.NoError(t, err)

	// Mock the runtime to return a running workload with proper labels
	runningInfo := rt.ContainerInfo{
		Name:    "proxy-down-workload",
		Image:   "test-image:latest",
		Status:  "Up 5 minutes",
		State:   rt.WorkloadStatusRunning, // Runtime says running (matches file)
		Created: time.Now().Add(-10 * time.Minute),
		Labels: map[string]string{
			"toolhive":          "true",
			"toolhive-name":     "proxy-down-workload",
			"toolhive-basename": "proxy-down-workload", // This is the base name for proxy
		},
	}

	// Mock the GetWorkloadInfo call that will be made during the proxy check
	mockRuntime.EXPECT().
		GetWorkloadInfo(gomock.Any(), "proxy-down-workload").
		Return(runningInfo, nil).
		AnyTimes() // Allow multiple calls during the SetWorkloadStatus operation as well

	// Note: proxy.IsRunning will check the actual system, but since there's no proxy
	// process running for "proxy-down-workload", it will return false

	// Get the workload - this should detect the proxy is not running and return unhealthy
	workload, err := manager.GetWorkload(ctx, "proxy-down-workload")
	require.NoError(t, err)

	// Verify the workload is marked as unhealthy due to proxy not running
	assert.Equal(t, "proxy-down-workload", workload.Name)
	assert.Equal(t, rt.WorkloadStatusUnhealthy, workload.Status)
	assert.Contains(t, workload.StatusContext, "proxy process not running")
	assert.Contains(t, workload.StatusContext, "proxy-down-workload")
	assert.Contains(t, workload.StatusContext, "not active")
	assert.Equal(t, "test-image:latest", workload.Package)

	// Verify the file was updated to unhealthy status
	data, err := os.ReadFile(statusFilePath)
	require.NoError(t, err)

	var updatedStatusFile workloadStatusFile
	err = json.Unmarshal(data, &updatedStatusFile)
	require.NoError(t, err)

	assert.Equal(t, rt.WorkloadStatusUnhealthy, updatedStatusFile.Status)
	assert.Contains(t, updatedStatusFile.StatusContext, "proxy process not running")
}

func TestFileStatusManager_GetWorkload_HealthyWithProxy(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	mockRuntime := mocks.NewMockRuntime(ctrl)
	manager := &fileStatusManager{
		baseDir: tempDir,
		runtime: mockRuntime,
	}
	ctx := context.Background()

	// Set the workload status to running in the file
	err := manager.SetWorkloadStatus(ctx, "healthy-with-proxy", rt.WorkloadStatusRunning, "container started")
	require.NoError(t, err)

	// Mock the runtime to return a running workload without base name (no proxy check)
	runningInfo := rt.ContainerInfo{
		Name:    "healthy-with-proxy",
		Image:   "test-image:latest",
		Status:  "Up 5 minutes",
		State:   rt.WorkloadStatusRunning,
		Created: time.Now().Add(-10 * time.Minute),
		Labels: map[string]string{
			"toolhive":      "true",
			"toolhive-name": "healthy-with-proxy",
			// No toolhive-base-name label, so proxy check will be skipped
		},
	}

	mockRuntime.EXPECT().
		GetWorkloadInfo(gomock.Any(), "healthy-with-proxy").
		Return(runningInfo, nil)

	// Get the workload - this should remain running since there's no base name for proxy check
	workload, err := manager.GetWorkload(ctx, "healthy-with-proxy")
	require.NoError(t, err)

	// Verify the workload remains running (no proxy check due to missing base name)
	assert.Equal(t, "healthy-with-proxy", workload.Name)
	assert.Equal(t, rt.WorkloadStatusRunning, workload.Status)
	assert.Equal(t, "container started", workload.StatusContext) // Original file context preserved
	assert.Equal(t, "test-image:latest", workload.Package)
}
