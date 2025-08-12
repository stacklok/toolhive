package statuses

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

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/logger"
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

	return &fileStatusManager{
		baseDir: baseDir,
		runtime: runtime,
	}, nil
}

// fileStatusManager is an implementation of StatusManager that persists
// workload status to files on disk with JSON serialization and file locking
// to prevent concurrent access issues.
type fileStatusManager struct {
	baseDir string
	runtime rt.Runtime
}

// workloadStatusFile represents the JSON structure stored on disk
type workloadStatusFile struct {
	Status        rt.WorkloadStatus `json:"status"`
	StatusContext string            `json:"status_context,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
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
		return nil
	})
	if err != nil {
		return core.Workload{}, err
	}

	// If file was found and workload is running, validate against runtime
	if fileFound && result.Status == rt.WorkloadStatusRunning {
		return f.validateRunningWorkload(ctx, workloadName, result)
	}

	// If file was found and workload is not running, return file data
	if fileFound {
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

	// Create a map of runtime workloads by name for easy lookup
	runtimeWorkloadMap := make(map[string]rt.ContainerInfo)
	for _, container := range runtimeContainers {
		runtimeWorkloadMap[container.Name] = container
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
		workloadMap[container.Name] = workload
	}

	// Then, merge with file workloads, validating running workloads
	for name, fileWorkload := range fileWorkloads {
		if runtimeContainer, exists := runtimeWorkloadMap[name]; exists {
			// Validate running workloads similar to GetWorkload
			validatedWorkload, err := f.validateWorkloadInList(ctx, name, fileWorkload, runtimeContainer)
			if err != nil {
				logger.Warnf("failed to validate workload %s in list: %v", name, err)
				// Fall back to basic merge without validation
				runtimeWorkload := workloadMap[name]
				runtimeWorkload.Status = fileWorkload.Status
				runtimeWorkload.StatusContext = fileWorkload.StatusContext
				runtimeWorkload.CreatedAt = fileWorkload.CreatedAt
				workloadMap[name] = runtimeWorkload
			} else {
				workloadMap[name] = validatedWorkload
			}
		} else {
			// File-only workload (runtime not available)
			workloadMap[name] = fileWorkload
		}
	}

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

// SetWorkloadStatus sets the status of a workload by its name.
func (f *fileStatusManager) SetWorkloadStatus(
	ctx context.Context,
	workloadName string,
	status rt.WorkloadStatus,
	contextMsg string,
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
			// Read existing file to preserve created_at timestamp
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

		// Update status and context
		statusFile.Status = status
		statusFile.StatusContext = contextMsg
		statusFile.UpdatedAt = now

		if err = f.writeStatusFile(statusFilePath, *statusFile); err != nil {
			return fmt.Errorf("failed to write updated status for workload %s: %w", workloadName, err)
		}

		logger.Debugf("workload %s set to status %s (context: %s)", workloadName, status, contextMsg)
		return nil
	})

	if err != nil {
		logger.Errorf("error updating workload %s status: %v", workloadName, err)
	}
	return err
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

// getWorkloadFromRuntime retrieves workload information from the runtime.
func (f *fileStatusManager) getWorkloadFromRuntime(ctx context.Context, workloadName string) (core.Workload, error) {
	info, err := f.runtime.GetWorkloadInfo(ctx, workloadName)
	if err != nil {
		return core.Workload{}, fmt.Errorf("failed to get workload info from runtime: %w", err)
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
	for _, file := range files {
		// Extract workload name from filename (remove .json extension)
		workloadName := strings.TrimSuffix(filepath.Base(file), ".json")

		// Read the status file
		statusFile, err := f.readStatusFile(file)
		if err != nil {
			logger.Warnf("failed to read status file %s: %v", file, err)
			continue
		}

		// Create workload from file data
		workload := core.Workload{
			Name:          workloadName,
			Status:        statusFile.Status,
			StatusContext: statusFile.StatusContext,
			CreatedAt:     statusFile.CreatedAt,
		}

		workloads[workloadName] = workload
	}

	return workloads, nil
}

// validateRunningWorkload validates that a workload marked as running in the file
// is actually running in the runtime and has a healthy proxy process if applicable.
func (f *fileStatusManager) validateRunningWorkload(
	ctx context.Context, workloadName string, result core.Workload,
) (core.Workload, error) {
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
