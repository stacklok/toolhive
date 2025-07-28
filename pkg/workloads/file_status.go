package workloads

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adrg/xdg"
	"github.com/gofrs/flock"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/logger"
)

const (
	// statusesPrefix is the prefix used for status files in the XDG data directory
	statusesPrefix = "statuses"
	// lockTimeout is the maximum time to wait for a file lock
	lockTimeout = 1 * time.Second
	// lockRetryInterval is the interval between lock attempts
	lockRetryInterval = 100 * time.Millisecond
)

// NewFileStatusManager creates a new file-based StatusManager.
// Status files will be stored in the XDG data directory under "statuses/".
func NewFileStatusManager() StatusManager {
	// Get the base directory using XDG data directory
	baseDir, err := xdg.DataFile(statusesPrefix)
	if err != nil {
		// Fallback to a basic path if XDG fails
		baseDir = filepath.Join(os.TempDir(), "toolhive", statusesPrefix)
	}
	// Remove the filename part to get just the directory
	baseDir = filepath.Dir(baseDir)

	return &fileStatusManager{
		baseDir: baseDir,
	}
}

// fileStatusManager is an implementation of StatusManager that persists
// workload status to files on disk with JSON serialization and file locking
// to prevent concurrent access issues.
type fileStatusManager struct {
	baseDir string
}

// workloadStatusFile represents the JSON structure stored on disk
type workloadStatusFile struct {
	Status        runtime.WorkloadStatus `json:"status"`
	StatusContext string                 `json:"status_context,omitempty"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
}

// CreateWorkloadStatus creates the initial `starting` status for a new workload.
// It will return an error if the workload already exists.
func (f *fileStatusManager) CreateWorkloadStatus(ctx context.Context, workloadName string) error {
	return f.withFileLock(ctx, workloadName, func(statusFilePath string) error {
		// Check if file already exists
		if _, err := os.Stat(statusFilePath); err == nil {
			return fmt.Errorf("workload %s already exists", workloadName)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("failed to check if workload %s exists: %w", workloadName, err)
		}

		// Create initial status
		now := time.Now()
		statusFile := workloadStatusFile{
			Status:        runtime.WorkloadStatusStarting,
			StatusContext: "",
			CreatedAt:     now,
			UpdatedAt:     now,
		}

		if err := f.writeStatusFile(statusFilePath, statusFile); err != nil {
			return fmt.Errorf("failed to write status file for workload %s: %w", workloadName, err)
		}

		logger.Debugf("workload %s created with starting status", workloadName)
		return nil
	})
}

// GetWorkloadStatus retrieves the status of a workload by its name.
func (f *fileStatusManager) GetWorkloadStatus(ctx context.Context, workloadName string) (runtime.WorkloadStatus, string, error) {
	result := runtime.WorkloadStatusUnknown
	var statusContext string

	err := f.withFileReadLock(ctx, workloadName, func(statusFilePath string) error {
		// Check if file exists
		if _, err := os.Stat(statusFilePath); os.IsNotExist(err) {
			return fmt.Errorf("workload %s not found", workloadName)
		} else if err != nil {
			return fmt.Errorf("failed to check status file for workload %s: %w", workloadName, err)
		}

		statusFile, err := f.readStatusFile(statusFilePath)
		if err != nil {
			return fmt.Errorf("failed to read status for workload %s: %w", workloadName, err)
		}

		result = statusFile.Status
		statusContext = statusFile.StatusContext
		return nil
	})

	return result, statusContext, err
}

// SetWorkloadStatus sets the status of a workload by its name.
// This method will do nothing if the workload does not exist, following the interface contract.
func (f *fileStatusManager) SetWorkloadStatus(
	ctx context.Context, workloadName string, status runtime.WorkloadStatus, contextMsg string,
) {
	err := f.withFileLock(ctx, workloadName, func(statusFilePath string) error {
		// Check if file exists
		if _, err := os.Stat(statusFilePath); os.IsNotExist(err) {
			// File doesn't exist, do nothing as per interface contract
			logger.Debugf("workload %s does not exist, skipping status update", workloadName)
			return nil
		} else if err != nil {
			return fmt.Errorf("failed to check status file for workload %s: %w", workloadName, err)
		}

		// Read existing file to preserve created_at timestamp
		statusFile, err := f.readStatusFile(statusFilePath)
		if err != nil {
			return fmt.Errorf("failed to read existing status for workload %s: %w", workloadName, err)
		}

		// Update status and context
		statusFile.Status = status
		statusFile.StatusContext = contextMsg
		statusFile.UpdatedAt = time.Now()

		if err := f.writeStatusFile(statusFilePath, *statusFile); err != nil {
			return fmt.Errorf("failed to write updated status for workload %s: %w", workloadName, err)
		}

		logger.Debugf("workload %s set to status %s (context: %s)", workloadName, status, contextMsg)
		return nil
	})

	if err != nil {
		logger.Errorf("error updating workload %s status: %v", workloadName, err)
	}
}

// DeleteWorkloadStatus removes the status of a workload by its name.
func (f *fileStatusManager) DeleteWorkloadStatus(ctx context.Context, workloadName string) error {
	return f.withFileLock(ctx, workloadName, func(statusFilePath string) error {
		// Remove status file
		if err := os.Remove(statusFilePath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to delete status file for workload %s: %w", workloadName, err)
		}

		// Remove lock file (best effort) - done by withFileLock after this function returns
		logger.Debugf("workload %s status deleted", workloadName)
		return nil
	})
}

// getStatusFilePath returns the file path for a given workload's status file.
func (f *fileStatusManager) getStatusFilePath(workloadName string) string {
	return filepath.Join(f.baseDir, fmt.Sprintf("%s.json", workloadName))
}

// getLockFilePath returns the lock file path for a given workload.
func (f *fileStatusManager) getLockFilePath(workloadName string) string {
	return filepath.Join(f.baseDir, fmt.Sprintf("%s.lock", workloadName))
}

// ensureBaseDir creates the base directory if it doesn't exist.
func (f *fileStatusManager) ensureBaseDir() error {
	return os.MkdirAll(f.baseDir, 0750)
}

// withFileLock executes the provided function while holding a write lock on the workload's lock file.
func (f *fileStatusManager) withFileLock(ctx context.Context, workloadName string, fn func(string) error) error {
	// Validate workload name
	if strings.Contains(workloadName, "..") || strings.ContainsAny(workloadName, "/\\") {
		return fmt.Errorf("invalid workload name '%s': contains forbidden characters", workloadName)
	}
	if err := f.ensureBaseDir(); err != nil {
		return fmt.Errorf("failed to create base directory: %w", err)
	}

	statusFilePath := f.getStatusFilePath(workloadName)
	lockFilePath := f.getLockFilePath(workloadName)

	// Create file lock
	fileLock := flock.New(lockFilePath)
	defer func() {
		if err := fileLock.Unlock(); err != nil {
			logger.Warnf("failed to unlock file %s: %v", lockFilePath, err)
		}
		// Attempt to remove lock file (best effort)
		if err := os.Remove(lockFilePath); err != nil && !os.IsNotExist(err) {
			logger.Warnf("failed to remove lock file for workload %s: %v", workloadName, err)
		}
	}()

	// Create context with timeout
	lockCtx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()

	// Acquire lock with context
	locked, err := fileLock.TryLockContext(lockCtx, lockRetryInterval)
	if err != nil {
		return fmt.Errorf("failed to acquire lock for workload %s: %w", workloadName, err)
	}
	if !locked {
		return fmt.Errorf("could not acquire lock for workload %s: timeout after %v", workloadName, lockTimeout)
	}

	return fn(statusFilePath)
}

// withFileReadLock executes the provided function while holding a read lock on the workload's lock file.
func (f *fileStatusManager) withFileReadLock(ctx context.Context, workloadName string, fn func(string) error) error {
	statusFilePath := f.getStatusFilePath(workloadName)
	lockFilePath := f.getLockFilePath(workloadName)

	// Create file lock
	fileLock := flock.New(lockFilePath)
	defer func() {
		if err := fileLock.Unlock(); err != nil {
			logger.Warnf("failed to unlock file %s: %v", lockFilePath, err)
		}
	}()

	// Create context with timeout
	lockCtx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()

	// Acquire read lock with context
	locked, err := fileLock.TryRLockContext(lockCtx, lockRetryInterval)
	if err != nil {
		return fmt.Errorf("failed to acquire read lock for workload %s: %w", workloadName, err)
	}
	if !locked {
		return fmt.Errorf("could not acquire read lock for workload %s: timeout after %v", workloadName, lockTimeout)
	}

	return fn(statusFilePath)
}

// readStatusFile reads and parses a workload status file from disk.
func (*fileStatusManager) readStatusFile(statusFilePath string) (*workloadStatusFile, error) {
	data, err := os.ReadFile(statusFilePath) //nolint:gosec // file path is constructed by our own function
	if err != nil {
		return nil, fmt.Errorf("failed to read status file: %w", err)
	}

	var statusFile workloadStatusFile
	if err := json.Unmarshal(data, &statusFile); err != nil {
		return nil, fmt.Errorf("failed to unmarshal status file: %w", err)
	}

	return &statusFile, nil
}

// writeStatusFile writes a workload status file to disk with proper formatting.
func (*fileStatusManager) writeStatusFile(statusFilePath string, statusFile workloadStatusFile) error {
	data, err := json.MarshalIndent(statusFile, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal status file: %w", err)
	}

	if err := os.WriteFile(statusFilePath, data, 0600); err != nil {
		return fmt.Errorf("failed to write status file: %w", err)
	}

	return nil
}
