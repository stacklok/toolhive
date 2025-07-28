package workloads

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/container/runtime"
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

	assert.Equal(t, runtime.WorkloadStatusStarting, statusFileData.Status)
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

func TestFileStatusManager_GetWorkloadStatus(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	// Create a workload status
	err := manager.CreateWorkloadStatus(ctx, "test-workload")
	require.NoError(t, err)

	// Get the status
	status, statusContext, err := manager.GetWorkloadStatus(ctx, "test-workload")
	require.NoError(t, err)
	assert.Equal(t, runtime.WorkloadStatusStarting, status)
	assert.Empty(t, statusContext)
}

func TestFileStatusManager_GetWorkloadStatus_NotFound(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	// Try to get status for non-existent workload
	_, _, err := manager.GetWorkloadStatus(ctx, "non-existent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
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
	manager.SetWorkloadStatus(ctx, "test-workload", runtime.WorkloadStatusRunning, "container started")

	// Verify the status was updated
	status, statusContext, err := manager.GetWorkloadStatus(ctx, "test-workload")
	require.NoError(t, err)
	assert.Equal(t, runtime.WorkloadStatusRunning, status)
	assert.Equal(t, "container started", statusContext)

	// Verify the file on disk
	statusFile := filepath.Join(tempDir, "test-workload.json")
	data, err := os.ReadFile(statusFile)
	require.NoError(t, err)

	var statusFileData workloadStatusFile
	err = json.Unmarshal(data, &statusFileData)
	require.NoError(t, err)

	assert.Equal(t, runtime.WorkloadStatusRunning, statusFileData.Status)
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
	manager.SetWorkloadStatus(ctx, "non-existent", runtime.WorkloadStatusRunning, "test")

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
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	// Create a workload status
	err := manager.CreateWorkloadStatus(ctx, "test-workload")
	require.NoError(t, err)

	// Test concurrent reads - should not conflict
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- true }()
			status, statusContext, err := manager.GetWorkloadStatus(ctx, "test-workload")
			assert.NoError(t, err)
			assert.Equal(t, runtime.WorkloadStatusStarting, status)
			assert.Empty(t, statusContext)
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
	tempDir := t.TempDir()
	manager := &fileStatusManager{baseDir: tempDir}
	ctx := context.Background()

	workloadName := "lifecycle-test"

	// 1. Create workload
	err := manager.CreateWorkloadStatus(ctx, workloadName)
	require.NoError(t, err)

	// 2. Verify initial status
	status, statusContext, err := manager.GetWorkloadStatus(ctx, workloadName)
	require.NoError(t, err)
	assert.Equal(t, runtime.WorkloadStatusStarting, status)
	assert.Empty(t, statusContext)

	// 3. Update to running
	manager.SetWorkloadStatus(ctx, workloadName, runtime.WorkloadStatusRunning, "started successfully")

	status, statusContext, err = manager.GetWorkloadStatus(ctx, workloadName)
	require.NoError(t, err)
	assert.Equal(t, runtime.WorkloadStatusRunning, status)
	assert.Equal(t, "started successfully", statusContext)

	// 4. Update to stopping
	manager.SetWorkloadStatus(ctx, workloadName, runtime.WorkloadStatusStopping, "shutdown initiated")

	status, statusContext, err = manager.GetWorkloadStatus(ctx, workloadName)
	require.NoError(t, err)
	assert.Equal(t, runtime.WorkloadStatusStopping, status)
	assert.Equal(t, "shutdown initiated", statusContext)

	// 5. Update to stopped
	manager.SetWorkloadStatus(ctx, workloadName, runtime.WorkloadStatusStopped, "shutdown complete")

	status, statusContext, err = manager.GetWorkloadStatus(ctx, workloadName)
	require.NoError(t, err)
	assert.Equal(t, runtime.WorkloadStatusStopped, status)
	assert.Equal(t, "shutdown complete", statusContext)

	// 6. Delete workload
	err = manager.DeleteWorkloadStatus(ctx, workloadName)
	require.NoError(t, err)

	// 7. Verify it's gone
	_, _, err = manager.GetWorkloadStatus(ctx, workloadName)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
