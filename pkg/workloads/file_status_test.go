package workloads

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stacklok/toolhive/pkg/core"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/container/runtime/mocks"
	"github.com/stacklok/toolhive/pkg/logger"
)

func init() {
	// Initialize logger for all tests
	logger.Initialize()
}

func TestFileStatusManager_CreateWorkloadStatus(t *testing.T) {
	t.Parallel()
	// Create temporary directory for tests
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	// Test creating a new workload status
	err := manager.CreateWorkloadStatus(ctx, "test-workload")
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

func TestFileStatusManager_CreateWorkloadStatus_AlreadyExists(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	// Create workload first time
	err := manager.CreateWorkloadStatus(ctx, "test-workload")
	require.NoError(t, err)

	// Try to create again - should fail
	err = manager.CreateWorkloadStatus(ctx, "test-workload")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
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
	err := manager.CreateWorkloadStatus(ctx, "test-workload")
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
	err := manager.CreateWorkloadStatus(ctx, "running-workload")
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
	err := manager.CreateWorkloadStatus(ctx, "test-workload")
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

	// Try to set status for non-existent workload - should not error but do nothing
	manager.SetWorkloadStatus(ctx, "non-existent", rt.WorkloadStatusRunning, "test")

	// Verify no file was created
	statusFile := filepath.Join(tempDir, "non-existent.json")
	assert.NoFileExists(t, statusFile)
}

func TestFileStatusManager_DeleteWorkloadStatus(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	// Create a workload status
	err := manager.CreateWorkloadStatus(ctx, "test-workload")
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
	err := manager.CreateWorkloadStatus(ctx, "test-workload")
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
	err := manager.CreateWorkloadStatus(ctx, workloadName)
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
				return f.CreateWorkloadStatus(ctx, "test-workload")
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
				if err := f.CreateWorkloadStatus(ctx, "starting-workload"); err != nil {
					return err
				}
				// Create a running workload
				if err := f.CreateWorkloadStatus(ctx, "running-workload"); err != nil {
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
				if err := f.CreateWorkloadStatus(ctx, "starting-workload"); err != nil {
					return err
				}
				// Create a running workload
				if err := f.CreateWorkloadStatus(ctx, "running-workload"); err != nil {
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
				return f.CreateWorkloadStatus(ctx, "test-workload")
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
				if err := f.CreateWorkloadStatus(ctx, "merge-workload"); err != nil {
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
