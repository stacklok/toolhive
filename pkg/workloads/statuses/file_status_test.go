package statuses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	rtmocks "github.com/stacklok/toolhive/pkg/container/runtime/mocks"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/logger"
	stateMocks "github.com/stacklok/toolhive/pkg/state/mocks"
)

const (
	// testWorkloadWithSlash is a test workload name containing slashes
	testWorkloadWithSlash = "test/workload"
)

func init() {
	// Initialize logger for all tests
	logger.Initialize()
}

// newTestFileStatusManager creates a fileStatusManager for testing with proper initialization
func newTestFileStatusManager(t *testing.T, ctrl *gomock.Controller) (*fileStatusManager, *rtmocks.MockRuntime, *stateMocks.MockStore) {
	t.Helper()
	tempDir := t.TempDir()
	mockRuntime := rtmocks.NewMockRuntime(ctrl)
	mockRunConfigStore := stateMocks.NewMockStore(ctrl)

	manager := &fileStatusManager{
		baseDir:        tempDir,
		runtime:        mockRuntime,
		runConfigStore: mockRunConfigStore,
	}

	return manager, mockRuntime, mockRunConfigStore
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

	manager, mockRuntime, mockRunConfigStore := newTestFileStatusManager(t, ctrl)
	ctx := context.Background()

	// Mock the run config store to return true for exists and provide a reader with non-remote data
	mockRunConfigStore.EXPECT().Exists(gomock.Any(), "test-workload").Return(true, nil).AnyTimes()

	// Create a mock reader that returns non-remote configuration data
	mockReader := io.NopCloser(strings.NewReader(`{"name": "test-workload", "transport": "sse"}`))
	mockRunConfigStore.EXPECT().GetReader(gomock.Any(), "test-workload").Return(mockReader, nil).AnyTimes()

	// Create a workload status
	err := manager.SetWorkloadStatus(ctx, "test-workload", rt.WorkloadStatusStarting, "")
	require.NoError(t, err)

	// Mock runtime to return error for fallback case (in case file is not found)
	mockRuntime.EXPECT().GetWorkloadInfo(gomock.Any(), "test-workload").Return(rt.ContainerInfo{}, errors.New("workload not found")).AnyTimes()

	// Get the workload (no runtime call expected for starting workload)
	workload, err := manager.GetWorkload(ctx, "test-workload")
	require.NoError(t, err)
	assert.Equal(t, "test-workload", workload.Name)
	assert.Equal(t, rt.WorkloadStatusStarting, workload.Status)
	assert.Empty(t, workload.StatusContext)
}

func TestFileStatusManager_GetWorkloadSlashes(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	workloadName := testWorkloadWithSlash

	manager, mockRuntime, mockRunConfigStore := newTestFileStatusManager(t, ctrl)
	ctx := context.Background()

	// Mock the run config store to return true for exists and provide a reader with non-remote data
	mockRunConfigStore.EXPECT().Exists(gomock.Any(), workloadName).Return(true, nil).AnyTimes()

	// Create a mock reader that returns non-remote configuration data
	mockReader := io.NopCloser(strings.NewReader(`{"name": "` + testWorkloadWithSlash + `", "transport": "sse"}`))
	mockRunConfigStore.EXPECT().GetReader(gomock.Any(), workloadName).Return(mockReader, nil).AnyTimes()

	// Create a workload status
	err := manager.SetWorkloadStatus(ctx, workloadName, rt.WorkloadStatusStarting, "")
	require.NoError(t, err)

	// Mock runtime to return error for fallback case (in case file is not found)
	mockRuntime.EXPECT().GetWorkloadInfo(gomock.Any(), workloadName).Return(rt.ContainerInfo{}, errors.New("workload not found")).AnyTimes()

	// Get the workload (no runtime call expected for starting workload)
	workload, err := manager.GetWorkload(ctx, workloadName)
	require.NoError(t, err)
	assert.Equal(t, workloadName, workload.Name)
	assert.Equal(t, rt.WorkloadStatusStarting, workload.Status)
	assert.Empty(t, workload.StatusContext)
}

func TestFileStatusManager_GetWorkload_NotFound(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	manager, mockRuntime, mockRunConfigStore := newTestFileStatusManager(t, ctrl)
	ctx := context.Background()

	// Mock the run config store to return false for exists (not a remote workload)
	mockRunConfigStore.EXPECT().Exists(gomock.Any(), "non-existent").Return(false, nil).AnyTimes()

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

	manager, mockRuntime, mockRunConfigStore := newTestFileStatusManager(t, ctrl)
	ctx := context.Background()

	// Mock the run config store to return false for exists (not a remote workload)
	mockRunConfigStore.EXPECT().Exists(gomock.Any(), "runtime-only-workload").Return(false, nil).AnyTimes()

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

	manager, mockRuntime, mockRunConfigStore := newTestFileStatusManager(t, ctrl)
	ctx := context.Background()

	// Mock the run config store to return true for exists and provide a reader with non-remote data
	mockRunConfigStore.EXPECT().Exists(gomock.Any(), "running-workload").Return(true, nil).AnyTimes()

	// Create a mock reader that returns non-remote configuration data
	mockReader := io.NopCloser(strings.NewReader(`{"name": "running-workload", "transport": "sse"}`))
	mockRunConfigStore.EXPECT().GetReader(gomock.Any(), "running-workload").Return(mockReader, nil).AnyTimes()

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

func TestFileStatusManager_SetWorkloadStatusSlashes(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	workloadName := testWorkloadWithSlash

	// Create a workload status
	err := manager.SetWorkloadStatus(ctx, workloadName, rt.WorkloadStatusStarting, "")
	require.NoError(t, err)

	// Update the status
	manager.SetWorkloadStatus(ctx, workloadName, rt.WorkloadStatusRunning, "container started")

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

func TestFileStatusManager_SetWorkloadStatus_PreservesPID(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	// First, create a workload with status
	err := manager.SetWorkloadStatus(ctx, "test-workload", rt.WorkloadStatusStarting, "initializing")
	require.NoError(t, err)

	// Then set the PID
	err = manager.SetWorkloadPID(ctx, "test-workload", 12345)
	require.NoError(t, err)

	// Read the file to verify initial state
	statusFile := filepath.Join(tempDir, "test-workload.json")
	originalData, err := os.ReadFile(statusFile)
	require.NoError(t, err)

	var originalStatusFile workloadStatusFile
	err = json.Unmarshal(originalData, &originalStatusFile)
	require.NoError(t, err)

	// Verify initial state
	assert.Equal(t, rt.WorkloadStatusStarting, originalStatusFile.Status)
	assert.Equal(t, "initializing", originalStatusFile.StatusContext)
	assert.Equal(t, 12345, originalStatusFile.ProcessID)

	// Wait a bit to ensure timestamps are different
	time.Sleep(10 * time.Millisecond)

	// Now update ONLY the status using SetWorkloadStatus (should preserve PID)
	err = manager.SetWorkloadStatus(ctx, "test-workload", rt.WorkloadStatusRunning, "container ready")
	require.NoError(t, err)

	// Read the file again to verify PID was preserved
	updatedData, err := os.ReadFile(statusFile)
	require.NoError(t, err)

	var updatedStatusFile workloadStatusFile
	err = json.Unmarshal(updatedData, &updatedStatusFile)
	require.NoError(t, err)

	// Verify that status and context were updated but PID was preserved
	assert.Equal(t, rt.WorkloadStatusRunning, updatedStatusFile.Status)        // Status updated
	assert.Equal(t, "container ready", updatedStatusFile.StatusContext)        // Context updated
	assert.Equal(t, 12345, updatedStatusFile.ProcessID)                        // PID preserved
	assert.Equal(t, originalStatusFile.CreatedAt, updatedStatusFile.CreatedAt) // CreatedAt preserved
	assert.True(t, updatedStatusFile.UpdatedAt.After(originalStatusFile.UpdatedAt) ||
		updatedStatusFile.UpdatedAt.Equal(originalStatusFile.UpdatedAt)) // UpdatedAt updated
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
	mockRuntime := rtmocks.NewMockRuntime(ctrl)
	mockRunConfigStore := stateMocks.NewMockStore(ctrl)
	manager := &fileStatusManager{
		baseDir:        tempDir,
		runtime:        mockRuntime,
		runConfigStore: mockRunConfigStore,
	}
	ctx := context.Background()

	// Mock the run config store to return true for exists and provide a reader with non-remote data
	mockRunConfigStore.EXPECT().Exists(gomock.Any(), "test-workload").Return(true, nil).AnyTimes()

	// Create a new mock reader for each call to avoid race conditions
	mockRunConfigStore.EXPECT().GetReader(gomock.Any(), "test-workload").DoAndReturn(func(context.Context, string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(`{"name": "test-workload", "transport": "sse"}`)), nil
	}).AnyTimes()

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

func TestFileStatusManager_ValidateRunningWorkload_Remote(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	mockRuntime := rtmocks.NewMockRuntime(ctrl)
	manager := &fileStatusManager{
		baseDir: tempDir,
		runtime: mockRuntime,
	}
	ctx := context.Background()

	// Create a remote workload with running status
	remoteWorkload := core.Workload{
		Name:          "remote-test",
		Status:        rt.WorkloadStatusRunning,
		Remote:        true,
		StatusContext: "remote server",
		CreatedAt:     time.Now(),
	}

	// Mock runtime should NOT be called for remote workloads
	// (no expectations set, so any call would fail the test)

	// Validate the remote workload
	result, err := manager.validateRunningWorkload(ctx, "remote-test", remoteWorkload)
	require.NoError(t, err)

	// Should return the workload unchanged without calling runtime
	assert.Equal(t, remoteWorkload.Name, result.Name)
	assert.Equal(t, remoteWorkload.Status, result.Status)
	assert.Equal(t, remoteWorkload.Remote, result.Remote)
	assert.Equal(t, remoteWorkload.StatusContext, result.StatusContext)
}

func TestFileStatusManager_FullLifecycle(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	mockRuntime := rtmocks.NewMockRuntime(ctrl)
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
		setupRuntimeMock func(*rtmocks.MockRuntime)
		expectedCount    int
		expectedError    string
		checkWorkloads   func([]core.Workload)
	}{
		{
			name:    "empty directory",
			setup:   func(_ *fileStatusManager) error { return nil },
			listAll: true,
			setupRuntimeMock: func(m *rtmocks.MockRuntime) {
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
			setupRuntimeMock: func(m *rtmocks.MockRuntime) {
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
			setupRuntimeMock: func(m *rtmocks.MockRuntime) {
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
			setupRuntimeMock: func(m *rtmocks.MockRuntime) {
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
			setupRuntimeMock: func(_ *rtmocks.MockRuntime) {
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
			setupRuntimeMock: func(m *rtmocks.MockRuntime) {
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

			manager, mockRuntime, mockRunConfigStore := newTestFileStatusManager(t, ctrl)
			tt.setupRuntimeMock(mockRuntime)

			// Mock the run config store to return true for exists and provide readers with non-remote data
			// This is a flexible mock that will handle any workload name
			mockRunConfigStore.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(true, nil).AnyTimes()

			// Create a flexible mock reader that returns non-remote configuration data for any workload
			mockRunConfigStore.EXPECT().GetReader(gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, name string) (io.ReadCloser, error) {
					return io.NopCloser(strings.NewReader(fmt.Sprintf(`{"name": "%s", "transport": "sse"}`, name))), nil
				}).AnyTimes()

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

	manager, mockRuntime, mockRunConfigStore := newTestFileStatusManager(t, ctrl)
	ctx := context.Background()

	// Mock the run config store to return true for exists and provide a reader with non-remote data
	mockRunConfigStore.EXPECT().Exists(gomock.Any(), "test-workload").Return(true, nil).AnyTimes()

	// Create a mock reader that returns non-remote configuration data
	mockReader := io.NopCloser(strings.NewReader(`{"name": "test-workload", "transport": "sse"}`))
	mockRunConfigStore.EXPECT().GetReader(gomock.Any(), "test-workload").Return(mockReader, nil).AnyTimes()

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
	statusFilePath := filepath.Join(manager.baseDir, "test-workload.json")
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

	manager, mockRuntime, mockRunConfigStore := newTestFileStatusManager(t, ctrl)
	ctx := context.Background()

	// Mock the run config store to return true for exists and provide a reader with non-remote data
	mockRunConfigStore.EXPECT().Exists(gomock.Any(), "healthy-workload").Return(true, nil).AnyTimes()

	// Create a mock reader that returns non-remote configuration data
	mockReader := io.NopCloser(strings.NewReader(`{"name": "healthy-workload", "transport": "sse"}`))
	mockRunConfigStore.EXPECT().GetReader(gomock.Any(), "healthy-workload").Return(mockReader, nil).AnyTimes()

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

	manager, mockRuntime, mockRunConfigStore := newTestFileStatusManager(t, ctrl)
	ctx := context.Background()

	// Mock the run config store to return true for exists and provide a reader with non-remote data
	mockRunConfigStore.EXPECT().Exists(gomock.Any(), "proxy-down-workload").Return(true, nil).AnyTimes()

	// Create a mock reader that returns non-remote configuration data
	mockReader := io.NopCloser(strings.NewReader(`{"name": "proxy-down-workload", "transport": "sse"}`))
	mockRunConfigStore.EXPECT().GetReader(gomock.Any(), "proxy-down-workload").Return(mockReader, nil).AnyTimes()

	// First, create a status file manually to ensure file is found
	statusFile := workloadStatusFile{
		Status:        rt.WorkloadStatusRunning,
		StatusContext: "container started",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	statusFilePath := filepath.Join(manager.baseDir, "proxy-down-workload.json")
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

	manager, mockRuntime, mockRunConfigStore := newTestFileStatusManager(t, ctrl)
	ctx := context.Background()

	// Mock the run config store to return true for exists and provide a reader with non-remote data
	mockRunConfigStore.EXPECT().Exists(gomock.Any(), "healthy-with-proxy").Return(true, nil).AnyTimes()

	// Create a mock reader that returns non-remote configuration data
	mockReader := io.NopCloser(strings.NewReader(`{"name": "healthy-with-proxy", "transport": "sse"}`))
	mockRunConfigStore.EXPECT().GetReader(gomock.Any(), "healthy-with-proxy").Return(mockReader, nil).AnyTimes()

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

func TestFileStatusManager_ListWorkloads_WithValidation(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	manager, mockRuntime, mockRunConfigStore := newTestFileStatusManager(t, ctrl)
	ctx := context.Background()

	// Mock the run config store to return true for exists and provide readers with non-remote data
	mockRunConfigStore.EXPECT().Exists(gomock.Any(), "healthy-workload").Return(true, nil).AnyTimes()
	mockRunConfigStore.EXPECT().Exists(gomock.Any(), "runtime-mismatch").Return(true, nil).AnyTimes()
	mockRunConfigStore.EXPECT().Exists(gomock.Any(), "proxy-down").Return(true, nil).AnyTimes()

	// Create mock readers that return non-remote configuration data
	mockReader1 := io.NopCloser(strings.NewReader(`{"name": "healthy-workload", "transport": "sse"}`))
	mockReader2 := io.NopCloser(strings.NewReader(`{"name": "runtime-mismatch", "transport": "sse"}`))
	mockReader3 := io.NopCloser(strings.NewReader(`{"name": "proxy-down", "transport": "sse"}`))
	mockRunConfigStore.EXPECT().GetReader(gomock.Any(), "healthy-workload").Return(mockReader1, nil).AnyTimes()
	mockRunConfigStore.EXPECT().GetReader(gomock.Any(), "runtime-mismatch").Return(mockReader2, nil).AnyTimes()
	mockRunConfigStore.EXPECT().GetReader(gomock.Any(), "proxy-down").Return(mockReader3, nil).AnyTimes()

	// Create file workloads - one healthy running, one with runtime mismatch, one with proxy down
	err := manager.SetWorkloadStatus(ctx, "healthy-workload", rt.WorkloadStatusRunning, "container started")
	require.NoError(t, err)

	err = manager.SetWorkloadStatus(ctx, "runtime-mismatch", rt.WorkloadStatusRunning, "container started")
	require.NoError(t, err)

	err = manager.SetWorkloadStatus(ctx, "proxy-down", rt.WorkloadStatusRunning, "container started")
	require.NoError(t, err)

	// Mock runtime containers
	runtimeContainers := []rt.ContainerInfo{
		{
			Name:   "healthy-workload",
			Image:  "healthy:latest",
			Status: "Up 5 minutes",
			State:  rt.WorkloadStatusRunning,
			Labels: map[string]string{
				"toolhive":      "true",
				"toolhive-name": "healthy-workload",
			},
		},
		{
			Name:   "runtime-mismatch",
			Image:  "mismatch:latest",
			Status: "Exited (0) 1 minute ago",
			State:  rt.WorkloadStatusStopped, // Runtime says stopped, file says running
			Labels: map[string]string{
				"toolhive":      "true",
				"toolhive-name": "runtime-mismatch",
			},
		},
		{
			Name:   "proxy-down",
			Image:  "proxy:latest",
			Status: "Up 3 minutes",
			State:  rt.WorkloadStatusRunning,
			Labels: map[string]string{
				"toolhive":          "true",
				"toolhive-name":     "proxy-down",
				"toolhive-basename": "proxy-down", // This will trigger proxy check
			},
		},
	}

	mockRuntime.EXPECT().ListWorkloads(gomock.Any()).Return(runtimeContainers, nil)

	// List all workloads
	workloads, err := manager.ListWorkloads(ctx, true, nil)
	require.NoError(t, err)

	// Should have 3 workloads
	require.Len(t, workloads, 3)

	// Create a map for easier assertion
	workloadMap := make(map[string]core.Workload)
	for _, w := range workloads {
		workloadMap[w.Name] = w
	}

	// Verify healthy workload remains running
	healthyWorkload, exists := workloadMap["healthy-workload"]
	require.True(t, exists)
	assert.Equal(t, rt.WorkloadStatusRunning, healthyWorkload.Status)

	// Verify runtime mismatch workload is marked unhealthy (status might be updated async)
	// We'll check for either unhealthy or the original status with mismatch context
	runtimeMismatch, exists := workloadMap["runtime-mismatch"]
	require.True(t, exists)
	// The workload should either be marked unhealthy or have a status context indicating the issue
	isValidatedUnhealthy := runtimeMismatch.Status == rt.WorkloadStatusUnhealthy ||
		strings.Contains(runtimeMismatch.StatusContext, "mismatch")
	assert.True(t, isValidatedUnhealthy, "Runtime mismatch workload should be detected as unhealthy")

	// Verify proxy down workload is detected (proxy.IsRunning will return false for non-existent proxy)
	proxyDown, exists := workloadMap["proxy-down"]
	require.True(t, exists)
	// Similar check - should be unhealthy or have proxy-related context
	isProxyValidated := proxyDown.Status == rt.WorkloadStatusUnhealthy ||
		strings.Contains(proxyDown.StatusContext, "proxy")
	assert.True(t, isProxyValidated, "Proxy down workload should be detected as unhealthy")
}

func TestFileStatusManager_GetWorkload_vs_ListWorkloads_Consistency(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	manager, mockRuntime, mockRunConfigStore := newTestFileStatusManager(t, ctrl)
	ctx := context.Background()

	// Mock the run config store to return true for exists and provide a reader with non-remote data
	mockRunConfigStore.EXPECT().Exists(gomock.Any(), "test-workload").Return(true, nil).AnyTimes()

	// Create a mock reader that returns non-remote configuration data
	mockReader := io.NopCloser(strings.NewReader(`{"name": "test-workload", "transport": "sse"}`))
	mockRunConfigStore.EXPECT().GetReader(gomock.Any(), "test-workload").Return(mockReader, nil).AnyTimes()

	// Create a workload status file
	err := manager.SetWorkloadStatus(ctx, "test-workload", rt.WorkloadStatusStarting, "")
	require.NoError(t, err)

	// Mock runtime to return empty (workload exists only in file)
	mockRuntime.EXPECT().ListWorkloads(gomock.Any()).Return([]rt.ContainerInfo{}, nil)

	// GetWorkload for a starting workload doesn't call runtime (only running workloads are validated)
	workload, err := manager.GetWorkload(ctx, "test-workload")
	require.NoError(t, err)
	assert.Equal(t, "test-workload", workload.Name)
	assert.Equal(t, rt.WorkloadStatusStarting, workload.Status)

	// ListWorkloads should include the same file-based workload
	workloads, err := manager.ListWorkloads(ctx, true, nil)
	require.NoError(t, err)

	// Should find the file-based workload in the list
	require.Len(t, workloads, 1)
	assert.Equal(t, "test-workload", workloads[0].Name)
	assert.Equal(t, rt.WorkloadStatusStarting, workloads[0].Status)

	// Both operations should return the same workload data for consistency
	assert.Equal(t, workload.Name, workloads[0].Name)
	assert.Equal(t, workload.Status, workloads[0].Status)
	assert.Equal(t, workload.StatusContext, workloads[0].StatusContext)
}

func TestFileStatusManager_ListWorkloads_CorruptedFile(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	manager, mockRuntime, mockRunConfigStore := newTestFileStatusManager(t, ctrl)
	ctx := context.Background()

	// Mock the run config store to return true for exists and provide a reader with non-remote data
	mockRunConfigStore.EXPECT().Exists(gomock.Any(), "good-workload").Return(true, nil).AnyTimes()

	// Create a mock reader that returns non-remote configuration data
	mockReader := io.NopCloser(strings.NewReader(`{"name": "good-workload", "transport": "sse"}`))
	mockRunConfigStore.EXPECT().GetReader(gomock.Any(), "good-workload").Return(mockReader, nil).AnyTimes()

	// Create a valid workload first
	err := manager.SetWorkloadStatus(ctx, "good-workload", rt.WorkloadStatusStarting, "")
	require.NoError(t, err)

	// Create a corrupted status file manually
	corruptedFile := filepath.Join(manager.baseDir, "corrupted-workload.json")
	err = os.WriteFile(corruptedFile, []byte(`{"invalid": json content`), 0644)
	require.NoError(t, err)

	// Create an empty status file
	emptyFile := filepath.Join(manager.baseDir, "empty-workload.json")
	err = os.WriteFile(emptyFile, []byte(``), 0644)
	require.NoError(t, err)

	// Mock runtime to return empty
	mockRuntime.EXPECT().ListWorkloads(gomock.Any()).Return([]rt.ContainerInfo{}, nil)

	// ListWorkloads should handle corrupted files gracefully
	workloads, err := manager.ListWorkloads(ctx, true, nil)
	require.NoError(t, err)

	// Should only return the good workload, corrupted ones should be skipped with warnings
	require.Len(t, workloads, 1)
	assert.Equal(t, "good-workload", workloads[0].Name)
}

func TestFileStatusManager_ListWorkloads_MissingRequiredFields(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	mockRuntime := rtmocks.NewMockRuntime(ctrl)
	manager := &fileStatusManager{
		baseDir: tempDir,
		runtime: mockRuntime,
	}
	ctx := context.Background()

	// Create a status file missing required fields
	invalidStatusFile := workloadStatusFile{
		// Missing Status field
		StatusContext: "some context",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	statusFilePath := filepath.Join(tempDir, "invalid-fields.json")
	data, err := json.MarshalIndent(invalidStatusFile, "", "  ")
	require.NoError(t, err)
	err = os.WriteFile(statusFilePath, data, 0644)
	require.NoError(t, err)

	// Create a status file missing created_at
	invalidStatusFile2 := workloadStatusFile{
		Status:        rt.WorkloadStatusRunning,
		StatusContext: "some context",
		// Missing CreatedAt field (will be zero value)
		UpdatedAt: time.Now(),
	}
	statusFilePath2 := filepath.Join(tempDir, "missing-created.json")
	data2, err := json.MarshalIndent(invalidStatusFile2, "", "  ")
	require.NoError(t, err)
	err = os.WriteFile(statusFilePath2, data2, 0644)
	require.NoError(t, err)

	// Mock runtime to return empty
	mockRuntime.EXPECT().ListWorkloads(gomock.Any()).Return([]rt.ContainerInfo{}, nil)

	// ListWorkloads should handle files with missing required fields gracefully
	workloads, err := manager.ListWorkloads(ctx, true, nil)
	require.NoError(t, err)

	// Should return empty since both files are invalid
	assert.Len(t, workloads, 0)
}

func TestFileStatusManager_ReadStatusFile_Validation(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}

	tests := []struct {
		name        string
		fileContent string
		expectError string
	}{
		{
			name:        "empty file",
			fileContent: "",
			expectError: "status file is empty",
		},
		{
			name:        "invalid json",
			fileContent: `{"invalid": json}`,
			expectError: "status file contains invalid JSON",
		},
		{
			name:        "missing status field",
			fileContent: `{"status_context": "test", "created_at": "2023-01-01T00:00:00Z", "updated_at": "2023-01-01T00:00:00Z"}`,
			expectError: "status file missing required 'status' field",
		},
		{
			name:        "missing created_at field",
			fileContent: `{"status": "running", "status_context": "test", "updated_at": "2023-01-01T00:00:00Z"}`,
			expectError: "status file missing or invalid 'created_at' field",
		},
		{
			name:        "valid file",
			fileContent: `{"status": "running", "status_context": "test", "created_at": "2023-01-01T00:00:00Z", "updated_at": "2023-01-01T00:00:00Z"}`,
			expectError: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create test file
			testFile := filepath.Join(tempDir, tt.name+".json")
			err := os.WriteFile(testFile, []byte(tt.fileContent), 0644)
			require.NoError(t, err)

			// Test readStatusFile
			statusFile, err := manager.readStatusFile(testFile)

			if tt.expectError != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectError)
				assert.Nil(t, statusFile)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, statusFile)
				assert.Equal(t, rt.WorkloadStatusRunning, statusFile.Status)
			}

			// Clean up
			os.Remove(testFile)
		})
	}
}

func TestFileStatusManager_SetWorkloadPID_NonExistentWorkload(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	// Test setting PID for non-existent workload (should be a noop)
	err := manager.SetWorkloadPID(ctx, "test-workload", 12345)
	require.NoError(t, err)

	// Verify no file was created (since it's a noop)
	statusFile := filepath.Join(tempDir, "test-workload.json")
	require.NoFileExists(t, statusFile)
}

func TestFileStatusManager_SetWorkloadPID_Update(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	// Create workload with initial status first
	err := manager.SetWorkloadStatus(ctx, "test-workload", rt.WorkloadStatusStarting, "initializing")
	require.NoError(t, err)

	// Read the file to get the original timestamps
	statusFile := filepath.Join(tempDir, "test-workload.json")
	originalData, err := os.ReadFile(statusFile)
	require.NoError(t, err)

	var originalStatusFile workloadStatusFile
	err = json.Unmarshal(originalData, &originalStatusFile)
	require.NoError(t, err)

	// Set the PID on existing workload
	err = manager.SetWorkloadPID(ctx, "test-workload", 67890)
	require.NoError(t, err)

	// Verify file was updated
	data, err := os.ReadFile(statusFile)
	require.NoError(t, err)

	var statusFileData workloadStatusFile
	err = json.Unmarshal(data, &statusFileData)
	require.NoError(t, err)

	// Verify only PID was updated while preserving other fields
	assert.Equal(t, rt.WorkloadStatusStarting, statusFileData.Status)       // Status preserved
	assert.Equal(t, "initializing", statusFileData.StatusContext)           // Context preserved
	assert.Equal(t, 67890, statusFileData.ProcessID)                        // PID updated
	assert.Equal(t, originalStatusFile.CreatedAt, statusFileData.CreatedAt) // CreatedAt preserved
	assert.True(t, statusFileData.UpdatedAt.After(originalStatusFile.UpdatedAt) ||
		statusFileData.UpdatedAt.Equal(originalStatusFile.UpdatedAt)) // UpdatedAt updated
}

func TestFileStatusManager_SetWorkloadPID_WithSlashes(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	workloadName := testWorkloadWithSlash

	// First create the workload
	err := manager.SetWorkloadStatus(ctx, workloadName, rt.WorkloadStatusRunning, "started")
	require.NoError(t, err)

	// Then set the PID for workload name with slashes
	err = manager.SetWorkloadPID(ctx, workloadName, 11111)
	require.NoError(t, err)

	// Verify file was created with slashes replaced by dashes
	statusFile := filepath.Join(tempDir, "test-workload.json")
	require.FileExists(t, statusFile)

	// Verify file contents
	data, err := os.ReadFile(statusFile)
	require.NoError(t, err)

	var statusFileData workloadStatusFile
	err = json.Unmarshal(data, &statusFileData)
	require.NoError(t, err)

	assert.Equal(t, rt.WorkloadStatusRunning, statusFileData.Status)
	assert.Equal(t, "started", statusFileData.StatusContext)
	assert.Equal(t, 11111, statusFileData.ProcessID)
}

func TestFileStatusManager_SetWorkloadPID_ZeroPID(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	// First create the workload
	err := manager.SetWorkloadStatus(ctx, "test-workload", rt.WorkloadStatusStopped, "container stopped")
	require.NoError(t, err)

	// Test setting PID 0 (which is valid - means no process)
	err = manager.SetWorkloadPID(ctx, "test-workload", 0)
	require.NoError(t, err)

	// Verify file was created with PID 0
	statusFile := filepath.Join(tempDir, "test-workload.json")
	data, err := os.ReadFile(statusFile)
	require.NoError(t, err)

	var statusFileData workloadStatusFile
	err = json.Unmarshal(data, &statusFileData)
	require.NoError(t, err)

	assert.Equal(t, rt.WorkloadStatusStopped, statusFileData.Status)
	assert.Equal(t, "container stopped", statusFileData.StatusContext)
	assert.Equal(t, 0, statusFileData.ProcessID)
}

func TestFileStatusManager_SetWorkloadPID_PreservesCreatedAt(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	// Create workload first
	err := manager.SetWorkloadStatus(ctx, "test-workload", rt.WorkloadStatusStarting, "initializing")
	require.NoError(t, err)

	// Get the original created time
	statusFile := filepath.Join(tempDir, "test-workload.json")
	originalData, err := os.ReadFile(statusFile)
	require.NoError(t, err)

	var originalStatusFile workloadStatusFile
	err = json.Unmarshal(originalData, &originalStatusFile)
	require.NoError(t, err)
	originalCreatedAt := originalStatusFile.CreatedAt

	// Wait a bit to ensure timestamps would be different
	time.Sleep(10 * time.Millisecond)

	// Update using SetWorkloadPID
	err = manager.SetWorkloadPID(ctx, "test-workload", 54321)
	require.NoError(t, err)

	// Verify CreatedAt is preserved
	data, err := os.ReadFile(statusFile)
	require.NoError(t, err)

	var statusFileData workloadStatusFile
	err = json.Unmarshal(data, &statusFileData)
	require.NoError(t, err)

	assert.Equal(t, originalCreatedAt, statusFileData.CreatedAt)
	assert.True(t, statusFileData.UpdatedAt.After(originalCreatedAt))
	assert.Equal(t, rt.WorkloadStatusStarting, statusFileData.Status) // Status should be preserved
	assert.Equal(t, "initializing", statusFileData.StatusContext)     // Context should be preserved
	assert.Equal(t, 54321, statusFileData.ProcessID)                  // PID should be updated
}

func TestFileStatusManager_SetWorkloadPID_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	// Create initial workload
	err := manager.SetWorkloadStatus(ctx, "concurrent-test", rt.WorkloadStatusStarting, "initializing")
	require.NoError(t, err)

	// Wait a tiny bit to ensure the initial status file is fully written
	time.Sleep(10 * time.Millisecond)

	// Test concurrent PID updates with fewer goroutines to reduce contention
	done := make(chan error, 3)

	go func() {
		err := manager.SetWorkloadPID(ctx, "concurrent-test", 1001)
		done <- err
	}()

	go func() {
		err := manager.SetWorkloadPID(ctx, "concurrent-test", 1002)
		done <- err
	}()

	go func() {
		err := manager.SetWorkloadPID(ctx, "concurrent-test", 1003)
		done <- err
	}()

	// Wait for all updates to complete and check for errors
	for i := 0; i < 3; i++ {
		select {
		case err := <-done:
			assert.NoError(t, err, "SetWorkloadPID should not fail")
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for concurrent PID updates")
		}
	}

	// Verify file exists and is valid
	statusFile := filepath.Join(tempDir, "concurrent-test.json")
	require.FileExists(t, statusFile)

	data, err := os.ReadFile(statusFile)
	require.NoError(t, err)

	var statusFileData workloadStatusFile
	err = json.Unmarshal(data, &statusFileData)
	require.NoError(t, err)

	// The status should remain unchanged (starting) since we only updated PIDs
	assert.Equal(t, rt.WorkloadStatusStarting, statusFileData.Status)
	assert.Equal(t, "initializing", statusFileData.StatusContext)

	// The final PID should be one of the three values we set concurrently
	validPIDs := []int{1001, 1002, 1003}
	assert.Contains(t, validPIDs, statusFileData.ProcessID, "PID should be one of the concurrently set values")
}

func TestFileStatusManager_ResetWorkloadPID_NonExistentWorkload(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	// Test resetting PID for non-existent workload (should be a noop)
	err := manager.ResetWorkloadPID(ctx, "test-workload")
	require.NoError(t, err)

	// Verify no file was created (since it's a noop)
	statusFile := filepath.Join(tempDir, "test-workload.json")
	require.NoFileExists(t, statusFile)
}

func TestFileStatusManager_ResetWorkloadPID_ExistingWorkload(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	// First create a workload with a non-zero PID
	err := manager.SetWorkloadStatus(ctx, "test-workload", rt.WorkloadStatusRunning, "container started")
	require.NoError(t, err)

	err = manager.SetWorkloadPID(ctx, "test-workload", 12345)
	require.NoError(t, err)

	// Verify the PID is set to 12345
	statusFile := filepath.Join(tempDir, "test-workload.json")
	data, err := os.ReadFile(statusFile)
	require.NoError(t, err)

	var statusFileData workloadStatusFile
	err = json.Unmarshal(data, &statusFileData)
	require.NoError(t, err)
	assert.Equal(t, 12345, statusFileData.ProcessID)

	// Now reset the PID
	err = manager.ResetWorkloadPID(ctx, "test-workload")
	require.NoError(t, err)

	// Verify the PID is now 0 and other fields are preserved
	data, err = os.ReadFile(statusFile)
	require.NoError(t, err)

	err = json.Unmarshal(data, &statusFileData)
	require.NoError(t, err)

	assert.Equal(t, 0, statusFileData.ProcessID)                       // PID should be reset to 0
	assert.Equal(t, rt.WorkloadStatusRunning, statusFileData.Status)   // Status should be preserved
	assert.Equal(t, "container started", statusFileData.StatusContext) // Context should be preserved
}

func TestFileStatusManager_ResetWorkloadPID_WithSlashes(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	workloadName := testWorkloadWithSlash

	// First create the workload and set a PID
	err := manager.SetWorkloadStatus(ctx, workloadName, rt.WorkloadStatusRunning, "started")
	require.NoError(t, err)

	err = manager.SetWorkloadPID(ctx, workloadName, 9999)
	require.NoError(t, err)

	// Reset the PID for workload name with slashes
	err = manager.ResetWorkloadPID(ctx, workloadName)
	require.NoError(t, err)

	// Verify file exists with slashes replaced by dashes and PID is 0
	statusFile := filepath.Join(tempDir, "test-workload.json")
	require.FileExists(t, statusFile)

	data, err := os.ReadFile(statusFile)
	require.NoError(t, err)

	var statusFileData workloadStatusFile
	err = json.Unmarshal(data, &statusFileData)
	require.NoError(t, err)

	assert.Equal(t, rt.WorkloadStatusRunning, statusFileData.Status)
	assert.Equal(t, "started", statusFileData.StatusContext)
	assert.Equal(t, 0, statusFileData.ProcessID) // PID should be reset to 0
}
