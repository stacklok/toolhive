package statuses

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adrg/xdg"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/lockfile"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/process"
	"github.com/stacklok/toolhive/pkg/state"
	"github.com/stacklok/toolhive/pkg/transport/proxy"
	"github.com/stacklok/toolhive/pkg/workloads/types"
)

const (
	// statusesPrefix is the prefix used for status files in the XDG data directory
	statusesPrefix = "toolhive/statuses"
	// lockTimeout is the maximum time to wait for a file lock
	lockTimeout = 1 * time.Second
	// lockRetryInterval is the interval between lock attempts
	lockRetryInterval = 100 * time.Millisecond
)

// NewFileStatusManager creates a new file-based StatusManager.
// Status files will be stored in the XDG data directory under "statuses/".
func NewFileStatusManager(runtime rt.Runtime) (StatusManager, error) {
	// Get the base directory using XDG data directory
	baseDir, err := xdg.DataFile(statusesPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to get directory for status files: %w", err)
	}

	// Ensure the base directory exists (equivalent to mkdir -p)
	if err := os.MkdirAll(baseDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create status directory %s: %w", baseDir, err)
	}

	// Create run config store for accessing run configurations
	runConfigStore, err := state.NewRunConfigStore(state.DefaultAppName)
	if err != nil {
		return nil, fmt.Errorf("failed to create run config store: %w", err)
	}

	return &fileStatusManager{
		baseDir:        baseDir,
		runtime:        runtime,
		runConfigStore: runConfigStore,
	}, nil
}

// fileStatusManager is an implementation of StatusManager that persists
// workload status to files on disk with JSON serialization and file locking
// to prevent concurrent access issues.
type fileStatusManager struct {
	baseDir string
	runtime rt.Runtime
	// runConfigStore is used to access run configurations without import cycles
	// TODO: This is a temporary solution to check if a workload is remote
	runConfigStore state.Store
}

// isRemoteWorkload checks if a workload is remote by attempting to load its run configuration
// and checking if it has a RemoteURL field set.
// TODO: This is a temporary solution to check if a workload is remote
// because of the import cycle between this package and the runconfig package.
// We can easily load run config and check if it has a RemoteURL field set when we resolve the import cycle.
func (f *fileStatusManager) isRemoteWorkload(ctx context.Context, workloadName string) (bool, error) {
	// Check if the run configuration exists
	exists, err := f.runConfigStore.Exists(ctx, workloadName)
	if err != nil {
		return false, err
	}

	if !exists {
		return false, rt.ErrWorkloadNotFound
	}

	// Get a reader for the run configuration
	reader, err := f.runConfigStore.GetReader(ctx, workloadName)
	if err != nil {
		return false, err
	}
	defer reader.Close()

	// Read the configuration data
	data, err := io.ReadAll(reader)
	if err != nil {
		return false, err
	}

	// Check if the JSON contains "remote_url" field
	return strings.Contains(string(data), `"remote_url"`), nil
}

// workloadStatusFile represents the JSON structure stored on disk
type workloadStatusFile struct {
	Status        rt.WorkloadStatus `json:"status"`
	StatusContext string            `json:"status_context,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	ProcessID     int               `json:"process_id"`
}

// GetWorkload retrieves the status of a workload by its name.
func (f *fileStatusManager) GetWorkload(ctx context.Context, workloadName string) (core.Workload, error) {
	result := core.Workload{Name: workloadName}
	fileFound := false

	err := f.withFileReadLock(ctx, workloadName, func(statusFilePath string) error {
		// Check if file exists
		if _, err := os.Stat(statusFilePath); os.IsNotExist(err) {
			// File doesn't exist, we'll fall back to runtime check
			return nil
		} else if err != nil {
			return fmt.Errorf("failed to check status file for workload %s: %w", workloadName, err)
		}

		statusFile, err := f.readStatusFile(statusFilePath)
		if err != nil {
			return fmt.Errorf("failed to read status for workload %s: %w", workloadName, err)
		}

		result.Status = statusFile.Status
		result.StatusContext = statusFile.StatusContext
		result.CreatedAt = statusFile.CreatedAt
		fileFound = true

		// Check if PID migration is needed
		if statusFile.Status == rt.WorkloadStatusRunning && statusFile.ProcessID == 0 {
			// Try PID migration - the migration function will handle cases
			// where container info is not available gracefully
			if migratedPID, wasMigrated := f.migratePIDFromFile(workloadName, nil); wasMigrated {
				// Update the status file with the migrated PID
				statusFile.ProcessID = migratedPID
				statusFile.UpdatedAt = time.Now()
				if err := f.writeStatusFile(statusFilePath, *statusFile); err != nil {
					logger.Warnf("failed to write migrated PID for workload %s: %v", workloadName, err)
				} else {
					logger.Debugf("successfully migrated PID %d to status file for workload %s", migratedPID, workloadName)
				}
			}
		}

		return nil
	})
	if err != nil {
		return core.Workload{}, err
	}

	// If file was found, check if this is a remote workload
	if fileFound {
		// Check if this is a remote workload using the state package
		remote, err := f.isRemoteWorkload(ctx, workloadName)
		if err != nil {
			// error is expected
			logger.Debugf("failed to check if remote workload %s is remote: %v", workloadName, err)
		}
		if remote {
			result.Remote = true
		}

		// If workload is running, validate against runtime
		if result.Status == rt.WorkloadStatusRunning {
			return f.validateRunningWorkload(ctx, workloadName, result)
		}

		// Return file data
		return result, nil
	}

	// File not found, fall back to runtime check
	return f.getWorkloadFromRuntime(ctx, workloadName)
}

func (f *fileStatusManager) ListWorkloads(ctx context.Context, listAll bool, labelFilters []string) ([]core.Workload, error) {
	// Parse the filters into a format we can use for matching.
	parsedFilters, err := types.ParseLabelFilters(labelFilters)
	if err != nil {
		return nil, fmt.Errorf("failed to parse label filters: %v", err)
	}

	// Get workloads from runtime
	runtimeContainers, err := f.runtime.ListWorkloads(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list workloads from runtime: %w", err)
	}

	// Get workloads from files
	fileWorkloads, err := f.getWorkloadsFromFiles()
	if err != nil {
		return nil, fmt.Errorf("failed to get workloads from files: %w", err)
	}

	// TODO: Fetch the runconfig if present to populate additional fields like package, tool type, group etc.
	// There's currently an import cycle between this package and the runconfig package

	for _, fileWorkload := range fileWorkloads {
		if fileWorkload.Remote { // Remote workloads are not managed by the container runtime
			delete(fileWorkloads, fileWorkload.Name) // Skip remote workloads here, we add them in workload manager
		}
	}

	// Create a map of runtime workloads by name for easy lookup
	workloadMap := f.mergeRuntimeAndFileWorkloads(ctx, runtimeContainers, fileWorkloads)

	// Convert map to slice and apply filters
	var workloads []core.Workload
	for _, workload := range workloadMap {
		// Apply listAll filter
		if !listAll && workload.Status != rt.WorkloadStatusRunning {
			continue
		}

		// Apply label filters
		if len(parsedFilters) > 0 {
			if !types.MatchesLabelFilters(workload.Labels, parsedFilters) {
				continue
			}
		}

		workloads = append(workloads, workload)
	}

	return workloads, nil
}

// setWorkloadStatusInternal handles the core logic for updating workload status files.
// pidPtr controls PID behavior: nil means preserve existing PID, non-nil means set to provided value.
func (f *fileStatusManager) setWorkloadStatusInternal(
	ctx context.Context,
	workloadName string,
	status rt.WorkloadStatus,
	contextMsg string,
	pidPtr *int,
) error {
	err := f.withFileLock(ctx, workloadName, func(statusFilePath string) error {
		// Check if file exists
		fileExists := true
		if _, err := os.Stat(statusFilePath); os.IsNotExist(err) {
			fileExists = false
		} else if err != nil {
			return fmt.Errorf("failed to check status file for workload %s: %w", workloadName, err)
		}

		var statusFile *workloadStatusFile
		var err error
		now := time.Now()

		if fileExists {
			// Read existing file to preserve created_at timestamp and other fields
			statusFile, err = f.readStatusFile(statusFilePath)
			if err != nil {
				return fmt.Errorf("failed to read existing status for workload %s: %w", workloadName, err)
			}
		} else {
			// Create new status file with CreatedAt set
			statusFile = &workloadStatusFile{
				CreatedAt: now,
			}
		}

		// Update status, context, and optionally PID
		statusFile.Status = status
		statusFile.StatusContext = contextMsg
		statusFile.UpdatedAt = now

		// Only update PID if pidPtr is provided
		if pidPtr != nil {
			statusFile.ProcessID = *pidPtr
		}

		if err = f.writeStatusFile(statusFilePath, *statusFile); err != nil {
			return fmt.Errorf("failed to write updated status for workload %s: %w", workloadName, err)
		}

		// Log with appropriate message based on whether PID was set
		if pidPtr != nil {
			logger.Debugf("workload %s set to status %s with PID %d (context: %s)", workloadName, status, *pidPtr, contextMsg)
		} else {
			logger.Debugf("workload %s set to status %s (context: %s)", workloadName, status, contextMsg)
		}
		return nil
	})

	if err != nil {
		if pidPtr != nil {
			logger.Errorf("error updating workload %s status and PID: %v", workloadName, err)
		} else {
			logger.Errorf("error updating workload %s status: %v", workloadName, err)
		}
	}
	return err
}

// SetWorkloadStatus sets the status of a workload by its name.
func (f *fileStatusManager) SetWorkloadStatus(
	ctx context.Context,
	workloadName string,
	status rt.WorkloadStatus,
	contextMsg string,
) error {
	return f.setWorkloadStatusInternal(ctx, workloadName, status, contextMsg, nil)
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

// SetWorkloadPID sets the PID of a workload by its name.
// This method will do nothing if the workload does not exist.
func (f *fileStatusManager) SetWorkloadPID(ctx context.Context, workloadName string, pid int) error {
	err := f.withFileLock(ctx, workloadName, func(statusFilePath string) error {
		// Check if file exists
		if _, err := os.Stat(statusFilePath); os.IsNotExist(err) {
			// File doesn't exist, nothing to do
			logger.Debugf("workload %s does not exist, skipping PID update", workloadName)
			return nil
		} else if err != nil {
			return fmt.Errorf("failed to check status file for workload %s: %w", workloadName, err)
		}

		// Read existing file
		statusFile, err := f.readStatusFile(statusFilePath)
		if err != nil {
			return fmt.Errorf("failed to read existing status for workload %s: %w", workloadName, err)
		}

		// Update only the PID and UpdatedAt timestamp
		statusFile.ProcessID = pid
		statusFile.UpdatedAt = time.Now()

		if err = f.writeStatusFile(statusFilePath, *statusFile); err != nil {
			return fmt.Errorf("failed to write updated PID for workload %s: %w", workloadName, err)
		}

		logger.Debugf("workload %s PID set to %d", workloadName, pid)
		return nil
	})

	if err != nil {
		logger.Errorf("error updating workload %s PID: %v", workloadName, err)
	}
	return err
}

// ResetWorkloadPID resets the PID of a workload to 0.
// This method will do nothing if the workload does not exist.
func (f *fileStatusManager) ResetWorkloadPID(ctx context.Context, workloadName string) error {
	return f.SetWorkloadPID(ctx, workloadName, 0)
}

// migratePIDFromFile migrates PID from legacy PID file to status file if needed.
// This is called when the status is running and ProcessID is 0.
// Returns (migratedPID, wasUpdated) where wasUpdated indicates if the PID was successfully migrated
func (*fileStatusManager) migratePIDFromFile(workloadName string, containerInfo *rt.ContainerInfo) (int, bool) {
	// Get the base name from container labels
	var baseName string
	if containerInfo != nil {
		baseName = labels.GetContainerBaseName(containerInfo.Labels)
	} else {
		// If we don't have container info, try using workload name as base name
		baseName = workloadName
	}

	if baseName == "" {
		logger.Debugf("no base name available for workload %s, skipping PID migration", workloadName)
		return 0, false
	}

	// Try to read PID from PID file
	// The ReadPIDFile function handles checking both old and new locations
	pid, err := process.ReadPIDFile(baseName)
	if err != nil {
		logger.Debugf("failed to read PID file for workload %s (base name: %s): %v", workloadName, baseName, err)
		return 0, false
	}
	logger.Debugf("found PID %d in PID file for workload %s, will update status file", pid, workloadName)

	// TODO: reinstate this once we decide to completely get rid of PID files.
	// Delete the PID file after successful migration
	/*if err := process.RemovePIDFile(baseName); err != nil {
		logger.Warnf("failed to remove PID file for workload %s (base name: %s): %v", workloadName, baseName, err)
		// Don't return false here - the migration succeeded, cleanup just failed
	}*/

	return pid, true
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

// TODO: This can probably be de-duped with withFileReadLock
// withFileLock executes the provided function while holding a write lock on the workload's lock file.
func (f *fileStatusManager) withFileLock(ctx context.Context, workloadName string, fn func(string) error) error {
	// Remove any slashes from the workload name to avoid problems.
	workloadName = strings.ReplaceAll(workloadName, "/", "-")

	// Validate workload name
	if strings.Contains(workloadName, "..") {
		return fmt.Errorf("invalid workload name '%s': contains forbidden characters", workloadName)
	}
	if err := f.ensureBaseDir(); err != nil {
		return fmt.Errorf("failed to create base directory: %w", err)
	}

	statusFilePath := f.getStatusFilePath(workloadName)
	lockFilePath := f.getLockFilePath(workloadName)

	// Create file lock
	fileLock := lockfile.NewTrackedLock(lockFilePath)
	defer lockfile.ReleaseTrackedLock(lockFilePath, fileLock)

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
	// Remove any slashes from the workload name to avoid problems.
	workloadName = strings.ReplaceAll(workloadName, "/", "-")

	// Validate workload name
	if strings.Contains(workloadName, "..") {
		return fmt.Errorf("invalid workload name '%s': contains forbidden characters", workloadName)
	}
	if err := f.ensureBaseDir(); err != nil {
		return fmt.Errorf("failed to create base directory: %w", err)
	}
	statusFilePath := f.getStatusFilePath(workloadName)
	lockFilePath := f.getLockFilePath(workloadName)

	// Create file lock
	fileLock := lockfile.NewTrackedLock(lockFilePath)
	defer lockfile.ReleaseTrackedLock(lockFilePath, fileLock)

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

	// Validate file content before parsing
	if len(data) == 0 {
		return nil, fmt.Errorf("status file is empty")
	}

	// Basic JSON structure validation
	if !json.Valid(data) {
		return nil, fmt.Errorf("status file contains invalid JSON")
	}

	var statusFile workloadStatusFile
	if err := json.Unmarshal(data, &statusFile); err != nil {
		return nil, fmt.Errorf("failed to unmarshal status file: %w", err)
	}

	// Validate essential fields
	if statusFile.Status == "" {
		return nil, fmt.Errorf("status file missing required 'status' field")
	}

	// Validate timestamps
	if statusFile.CreatedAt.IsZero() {
		return nil, fmt.Errorf("status file missing or invalid 'created_at' field")
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

// getWorkloadFromRuntime retrieves workload information from the runtime.
func (f *fileStatusManager) getWorkloadFromRuntime(ctx context.Context, workloadName string) (core.Workload, error) {
	info, err := f.runtime.GetWorkloadInfo(ctx, workloadName)
	if err != nil {
		return core.Workload{}, fmt.Errorf("failed to get workload info from runtime: %w", err)
	}

	// Verify exact name match to prevent Docker prefix matching false positives
	if info.Name != workloadName {
		return core.Workload{}, rt.ErrWorkloadNotFound
	}

	return types.WorkloadFromContainerInfo(&info)
}

// getWorkloadsFromFiles retrieves all workloads from status files.
func (f *fileStatusManager) getWorkloadsFromFiles() (map[string]core.Workload, error) {
	// Ensure base directory exists
	if err := f.ensureBaseDir(); err != nil {
		return nil, fmt.Errorf("failed to ensure base directory: %w", err)
	}

	// List all .json files in the base directory
	files, err := filepath.Glob(filepath.Join(f.baseDir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("failed to list status files: %w", err)
	}

	workloads := make(map[string]core.Workload)
	ctx := context.Background() // Create context for file locking

	for _, file := range files {
		// Extract workload name from filename (remove .json extension)
		workloadName := strings.TrimSuffix(filepath.Base(file), ".json")

		// Use write lock since we may need to update the file for PID migration
		err := f.withFileLock(ctx, workloadName, func(statusFilePath string) error {
			// Check if file exists first
			if _, err := os.Stat(statusFilePath); os.IsNotExist(err) {
				logger.Debugf("status file for workload %s no longer exists, skipping", workloadName)
				return nil // Not an error, file was removed
			} else if err != nil {
				return fmt.Errorf("failed to check status file: %w", err)
			}

			// Read the status file with proper error handling
			statusFile, err := f.readStatusFile(statusFilePath)
			if err != nil {
				// Distinguish between different types of errors
				if os.IsPermission(err) {
					return fmt.Errorf("permission denied reading status file: %w", err)
				}
				// For JSON parsing errors or corrupted files, log details
				logger.Errorf("failed to read or parse status file %s for workload %s: %v", statusFilePath, workloadName, err)
				return fmt.Errorf("corrupted or invalid status file: %w", err)
			}

			// Create workload from file data
			workload := core.Workload{
				Name:          workloadName,
				Status:        statusFile.Status,
				StatusContext: statusFile.StatusContext,
				CreatedAt:     statusFile.CreatedAt,
			}

			// Check if this is a remote workload using the state package
			remote, err := f.isRemoteWorkload(ctx, workloadName)
			if err != nil {
				// This error is expected
				logger.Debugf("failed to check if remote workload %s is remote: %v", workloadName, err)
			}
			if remote {
				workload.Remote = true
			}

			// Check if PID migration is needed
			if statusFile.Status == rt.WorkloadStatusRunning && statusFile.ProcessID == 0 {
				// Try PID migration - the migration function will handle cases
				// where container info is not available gracefully
				if migratedPID, wasMigrated := f.migratePIDFromFile(workloadName, nil); wasMigrated {
					// Update the status file with the migrated PID
					statusFile.ProcessID = migratedPID
					statusFile.UpdatedAt = time.Now()
					if err := f.writeStatusFile(statusFilePath, *statusFile); err != nil {
						logger.Warnf("failed to write migrated PID for workload %s: %v", workloadName, err)
					} else {
						logger.Debugf("successfully migrated PID %d to status file for workload %s", migratedPID, workloadName)
					}
				}
			}

			workloads[workloadName] = workload
			return nil
		})

		if err != nil {
			// Log the specific error but continue processing other workloads
			// This maintains the existing behavior but with better diagnostics
			logger.Warnf("failed to process status file for workload %s: %v", workloadName, err)
			continue
		}
	}

	return workloads, nil
}

// validateRunningWorkload validates that a workload marked as running in the file
// is actually running in the runtime and has a healthy proxy process if applicable.
func (f *fileStatusManager) validateRunningWorkload(
	ctx context.Context, workloadName string, result core.Workload,
) (core.Workload, error) {
	// For remote workloads, we don't need to validate against the container runtime
	// since they don't have containers
	if result.Remote {
		return result, nil
	}

	// Get raw container info from runtime (before label filtering)
	containerInfo, err := f.runtime.GetWorkloadInfo(ctx, workloadName)
	if err != nil {
		return core.Workload{}, err
	}

	// Check if runtime status matches file status
	if containerInfo.State != rt.WorkloadStatusRunning {
		return f.handleRuntimeMismatch(ctx, workloadName, result, containerInfo)
	}

	// Check if proxy process is running when workload is running
	if unhealthyWorkload, isUnhealthy := f.checkProxyHealth(ctx, workloadName, result, containerInfo); isUnhealthy {
		return unhealthyWorkload, nil
	}

	// Runtime and proxy confirm workload is healthy - merge runtime data with file status
	return f.mergeHealthyWorkloadData(containerInfo, result)
}

// handleRuntimeMismatch handles the case where file indicates running but runtime shows different status
func (f *fileStatusManager) handleRuntimeMismatch(
	ctx context.Context, workloadName string, result core.Workload, containerInfo rt.ContainerInfo,
) (core.Workload, error) {
	contextMsg := fmt.Sprintf("workload status mismatch: file indicates running, but runtime shows %s", containerInfo.State)
	if err := f.SetWorkloadStatus(ctx, workloadName, rt.WorkloadStatusUnhealthy, contextMsg); err != nil {
		logger.Warnf("failed to update workload %s status to unhealthy: %v", workloadName, err)
	}

	// Convert to workload and return unhealthy status
	runtimeResult, err := types.WorkloadFromContainerInfo(&containerInfo)
	if err != nil {
		return core.Workload{}, err
	}

	runtimeResult.Status = rt.WorkloadStatusUnhealthy
	runtimeResult.StatusContext = contextMsg
	runtimeResult.CreatedAt = result.CreatedAt // Keep the original file created time
	return runtimeResult, nil
}

// handleRuntimeMissing handles the case where the file indicates running or stopped but the runtime
// does not have the workload running. This can happen if using different versions of ToolHive, for example
// the CLI and UI have different versions.
func (f *fileStatusManager) handleRuntimeMissing(
	ctx context.Context, workloadName string, fileWorkload core.Workload,
) (core.Workload, error) {
	// Check if this is a remote workload using the Remote field
	if fileWorkload.Remote {
		// Remote workloads don't exist in the container runtime, so it's normal for them to be missing
		// Don't mark them as unhealthy
		return fileWorkload, nil
	}

	if fileWorkload.Status == rt.WorkloadStatusRunning || fileWorkload.Status == rt.WorkloadStatusStopped {
		// The workload cannot be running or stopped if the runtime container is not found
		contextMsg := fmt.Sprintf("workload %s not found in runtime, marking as unhealthy", workloadName)
		if err := f.SetWorkloadStatus(ctx, workloadName, rt.WorkloadStatusUnhealthy, contextMsg); err != nil {
			return core.Workload{}, err
		}
		fileWorkload.Status = rt.WorkloadStatusUnhealthy
	}

	// If the workload has another status, like starting or stopping, we can keep it as is
	return fileWorkload, nil
}

// checkProxyHealth checks if the proxy process is running for the workload.
// Returns (unhealthyWorkload, true) if proxy is not running, (emptyWorkload, false) if proxy is healthy or not applicable.
func (f *fileStatusManager) checkProxyHealth(
	ctx context.Context, workloadName string, result core.Workload, containerInfo rt.ContainerInfo,
) (core.Workload, bool) {
	// Use original container labels (before filtering) to get base name
	baseName := labels.GetContainerBaseName(containerInfo.Labels)
	if baseName == "" {
		return core.Workload{}, false // No proxy check needed
	}

	proxyRunning := proxy.IsRunning(baseName)
	if proxyRunning {
		return core.Workload{}, false // Proxy is healthy
	}

	// Proxy is not running, but workload should be running
	contextMsg := fmt.Sprintf("proxy process not running: workload shows running but proxy process for %s is not active",
		baseName)
	if err := f.SetWorkloadStatus(ctx, workloadName, rt.WorkloadStatusUnhealthy, contextMsg); err != nil {
		logger.Warnf("failed to update workload %s status to unhealthy: %v", workloadName, err)
	}

	// Convert to workload and return unhealthy status
	runtimeResult, err := types.WorkloadFromContainerInfo(&containerInfo)
	if err != nil {
		logger.Warnf("failed to convert container info for unhealthy workload %s: %v", workloadName, err)
		return core.Workload{}, false // Return false to avoid double error handling
	}

	runtimeResult.Status = rt.WorkloadStatusUnhealthy
	runtimeResult.StatusContext = contextMsg
	runtimeResult.CreatedAt = result.CreatedAt // Keep the original file created time
	return runtimeResult, true
}

// mergeHealthyWorkloadData merges runtime container data with file-based status information
func (*fileStatusManager) mergeHealthyWorkloadData(containerInfo rt.ContainerInfo, result core.Workload) (core.Workload, error) {
	// Runtime and proxy confirm workload is healthy - use runtime data but preserve file-based status info
	runtimeResult, err := types.WorkloadFromContainerInfo(&containerInfo)
	if err != nil {
		return core.Workload{}, err
	}

	runtimeResult.Status = result.Status               // Keep the file status (running)
	runtimeResult.StatusContext = result.StatusContext // Keep the file status context
	runtimeResult.CreatedAt = result.CreatedAt         // Keep the file created time
	return runtimeResult, nil
}

// validateWorkloadInList validates a workload during list operations, similar to validateRunningWorkload
// but with different error handling to avoid disrupting the entire list operation.
func (f *fileStatusManager) validateWorkloadInList(
	ctx context.Context, workloadName string, fileWorkload core.Workload, containerInfo rt.ContainerInfo,
) (core.Workload, error) {
	// Only validate if file shows running status
	if fileWorkload.Status != rt.WorkloadStatusRunning {
		// For non-running workloads, just merge runtime data with file status
		runtimeWorkload, err := types.WorkloadFromContainerInfo(&containerInfo)
		if err != nil {
			return core.Workload{}, err
		}
		runtimeWorkload.Status = fileWorkload.Status
		runtimeWorkload.StatusContext = fileWorkload.StatusContext
		runtimeWorkload.CreatedAt = fileWorkload.CreatedAt
		return runtimeWorkload, nil
	}

	// For running workloads, apply full validation
	// Check if runtime status matches file status
	if containerInfo.State != rt.WorkloadStatusRunning {
		return f.handleRuntimeMismatch(ctx, workloadName, fileWorkload, containerInfo)
	}

	// Check if proxy process is running when workload is running
	if unhealthyWorkload, isUnhealthy := f.checkProxyHealth(ctx, workloadName, fileWorkload, containerInfo); isUnhealthy {
		return unhealthyWorkload, nil
	}

	// Runtime and proxy confirm workload is healthy - merge runtime data with file status
	return f.mergeHealthyWorkloadData(containerInfo, fileWorkload)
}

// mergeRuntimeAndFileWorkloads returns a map of workloads that combines runtime containers and file-based workloads.
func (f *fileStatusManager) mergeRuntimeAndFileWorkloads(
	ctx context.Context,
	runtimeContainers []rt.ContainerInfo,
	fileWorkloads map[string]core.Workload,
) map[string]core.Workload {
	runtimeWorkloadMap := make(map[string]rt.ContainerInfo)
	for _, container := range runtimeContainers {
		// Use base name from labels for matching, fall back to container name if not available
		baseName := labels.GetContainerBaseName(container.Labels)
		if baseName == "" {
			baseName = container.Name // fallback for containers without base name label
		}
		runtimeWorkloadMap[baseName] = container
	}

	// Create result map to avoid duplicates and merge data
	workloadMap := make(map[string]core.Workload)

	// First, add all runtime workloads
	for _, container := range runtimeContainers {
		workload, err := types.WorkloadFromContainerInfo(&container)
		if err != nil {
			logger.Warnf("failed to convert container info for workload %s: %v", container.Name, err)
			continue
		}
		// Use base name for consistency with file workloads
		baseName := labels.GetContainerBaseName(container.Labels)
		if baseName == "" {
			baseName = container.Name // fallback for containers without base name label
		}
		workloadMap[baseName] = workload
	}

	// Then, merge with file workloads, validating running workloads
	for name, fileWorkload := range fileWorkloads {

		if fileWorkload.Remote { // Remote workloads are not managed by the container runtime
			continue // Skip remote workloads here, we add them in workload manager
		}
		if runtimeContainer, exists := runtimeWorkloadMap[name]; exists {
			// Validate running workloads similar to GetWorkload
			validatedWorkload, err := f.validateWorkloadInList(ctx, name, fileWorkload, runtimeContainer)
			if err != nil {
				logger.Warnf("failed to validate workload %s in list: %v", name, err)
				// Fall back to basic merge without validation
				if runtimeWorkload, exists := workloadMap[name]; exists {
					runtimeWorkload.Status = fileWorkload.Status
					runtimeWorkload.StatusContext = fileWorkload.StatusContext
					runtimeWorkload.CreatedAt = fileWorkload.CreatedAt
					workloadMap[name] = runtimeWorkload
				} else {
					// Runtime workload not found, just use the file workload
					workloadMap[name] = fileWorkload
				}
			} else {
				workloadMap[name] = validatedWorkload
			}
		} else {
			// File-only workload (runtime not available)
			updatedWorkload, err := f.handleRuntimeMissing(ctx, name, fileWorkload)
			if err != nil {
				logger.Warnf("failed to handle missing runtime for workload %s: %v", name, err)
				workloadMap[name] = fileWorkload
			}
			workloadMap[name] = updatedWorkload
		}
	}
	return workloadMap
}
