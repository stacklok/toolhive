// Package workloads contains high-level logic for managing the lifecycle of
// ToolHive-managed containers.
package workloads

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/adrg/xdg"
	"golang.org/x/sync/errgroup"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/config"
	ct "github.com/stacklok/toolhive/pkg/container"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/process"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/state"
	"github.com/stacklok/toolhive/pkg/transport/proxy"
	"github.com/stacklok/toolhive/pkg/workloads/statuses"
	"github.com/stacklok/toolhive/pkg/workloads/types"
)

// Manager is responsible for managing the state of ToolHive-managed containers.
// NOTE: This interface may be split up in future PRs, in particular, operations
// which are only relevant to the CLI/API use case will be split out.
//
//go:generate mockgen -destination=mocks/mock_manager.go -package=mocks -source=manager.go Manager
type Manager interface {
	// GetWorkload retrieves details of the named workload including its status.
	GetWorkload(ctx context.Context, workloadName string) (core.Workload, error)
	// ListWorkloads retrieves the states of all workloads.
	// The `listAll` parameter determines whether to include workloads that are not running.
	// The optional `labelFilters` parameter allows filtering workloads by labels (format: key=value).
	ListWorkloads(ctx context.Context, listAll bool, labelFilters ...string) ([]core.Workload, error)
	// DeleteWorkloads deletes the specified workloads by name.
	// It is implemented as an asynchronous operation which returns an errgroup.Group
	DeleteWorkloads(ctx context.Context, names []string) (*errgroup.Group, error)
	// StopWorkloads stops the specified workloads by name.
	// It is implemented as an asynchronous operation which returns an errgroup.Group
	StopWorkloads(ctx context.Context, names []string) (*errgroup.Group, error)
	// RunWorkload runs a container in the foreground.
	RunWorkload(ctx context.Context, runConfig *runner.RunConfig) error
	// RunWorkloadDetached runs a container in the background.
	RunWorkloadDetached(ctx context.Context, runConfig *runner.RunConfig) error
	// RestartWorkloads restarts the specified workloads by name.
	// It is implemented as an asynchronous operation which returns an errgroup.Group
	RestartWorkloads(ctx context.Context, names []string, foreground bool) (*errgroup.Group, error)
	// GetLogs retrieves the logs of a container.
	GetLogs(ctx context.Context, containerName string, follow bool) (string, error)
	// MoveToGroup moves the specified workloads from one group to another by updating their runconfig.
	MoveToGroup(ctx context.Context, workloadNames []string, groupFrom string, groupTo string) error
	// ListWorkloadsInGroup returns all workload names that belong to the specified group, including stopped workloads.
	ListWorkloadsInGroup(ctx context.Context, groupName string) ([]string, error)
	// DoesWorkloadExist checks if a workload with the given name exists.
	DoesWorkloadExist(ctx context.Context, workloadName string) (bool, error)
}

type defaultManager struct {
	runtime  rt.Runtime
	statuses statuses.StatusManager
}

// ErrWorkloadNotRunning is returned when a container cannot be found by name.
var ErrWorkloadNotRunning = fmt.Errorf("workload not running")

const (
	// AsyncOperationTimeout is the timeout for async workload operations
	AsyncOperationTimeout = 5 * time.Minute
)

// NewManager creates a new container manager instance.
func NewManager(ctx context.Context) (Manager, error) {
	runtime, err := ct.NewFactory().Create(ctx)
	if err != nil {
		return nil, err
	}

	statusManager, err := statuses.NewStatusManager(runtime)
	if err != nil {
		return nil, fmt.Errorf("failed to create status manager: %w", err)
	}

	return &defaultManager{
		runtime:  runtime,
		statuses: statusManager,
	}, nil
}

// NewManagerFromRuntime creates a new container manager instance from an existing runtime.
func NewManagerFromRuntime(runtime rt.Runtime) (Manager, error) {
	statusManager, err := statuses.NewStatusManager(runtime)
	if err != nil {
		return nil, fmt.Errorf("failed to create status manager: %w", err)
	}

	return &defaultManager{
		runtime:  runtime,
		statuses: statusManager,
	}, nil
}

func (d *defaultManager) GetWorkload(ctx context.Context, workloadName string) (core.Workload, error) {
	// For the sake of minimizing changes, delegate to the status manager.
	// Whether this method should still belong to the workload manager is TBD.
	return d.statuses.GetWorkload(ctx, workloadName)
}

func (d *defaultManager) DoesWorkloadExist(ctx context.Context, workloadName string) (bool, error) {
	// check if workload exists by trying to get it
	workload, err := d.statuses.GetWorkload(ctx, workloadName)
	if err != nil {
		if errors.Is(err, rt.ErrWorkloadNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check if workload exists: %w", err)
	}

	// now check if the workload is not in error
	if workload.Status == rt.WorkloadStatusError {
		return false, nil
	}
	return true, nil
}

func (d *defaultManager) ListWorkloads(ctx context.Context, listAll bool, labelFilters ...string) ([]core.Workload, error) {
	// For the sake of minimizing changes, delegate to the status manager.
	// Whether this method should still belong to the workload manager is TBD.
	containerWorkloads, err := d.statuses.ListWorkloads(ctx, listAll, labelFilters)
	if err != nil {
		return nil, err
	}

	// Get remote workloads from the state store
	remoteWorkloads, err := d.getRemoteWorkloadsFromState(ctx, listAll, labelFilters)
	if err != nil {
		logger.Warnf("Failed to get remote workloads from state: %v", err)
		// Continue with container workloads only
	} else {
		// Combine container and remote workloads
		containerWorkloads = append(containerWorkloads, remoteWorkloads...)
	}

	return containerWorkloads, nil
}

func (d *defaultManager) StopWorkloads(_ context.Context, names []string) (*errgroup.Group, error) {
	// Validate all workload names to prevent path traversal attacks
	for _, name := range names {
		if err := types.ValidateWorkloadName(name); err != nil {
			return nil, fmt.Errorf("invalid workload name '%s': %w", name, err)
		}
		// Ensure workload name does not contain path traversal or separators
		if strings.Contains(name, "..") || strings.ContainsAny(name, "/\\") {
			return nil, fmt.Errorf("invalid workload name '%s': contains forbidden characters", name)
		}
	}

	group := &errgroup.Group{}
	// Process each workload
	for _, name := range names {
		group.Go(func() error {
			return d.stopSingleWorkload(name)
		})
	}

	return group, nil
}

// stopSingleWorkload stops a single workload (container or remote)
func (d *defaultManager) stopSingleWorkload(name string) error {
	// Create a child context with a longer timeout
	childCtx, cancel := context.WithTimeout(context.Background(), AsyncOperationTimeout)
	defer cancel()

	// First, try to load the run configuration to check if it's a remote workload
	runConfig, err := runner.LoadState(childCtx, name)
	if err != nil {
		// If we can't load the state, it might be a container workload or the workload doesn't exist
		// Try to stop it as a container workload
		return d.stopContainerWorkload(childCtx, name)
	}

	// Check if this is a remote workload
	if runConfig.RemoteURL != "" {
		return d.stopRemoteWorkload(childCtx, name, runConfig)
	}

	// This is a container-based workload
	return d.stopContainerWorkload(childCtx, name)
}

// stopRemoteWorkload stops a remote workload
func (d *defaultManager) stopRemoteWorkload(ctx context.Context, name string, runConfig *runner.RunConfig) error {
	logger.Infof("Stopping remote workload %s...", name)

	// Check if the workload is running by checking its status
	workload, err := d.statuses.GetWorkload(ctx, name)
	if err != nil {
		if errors.Is(err, rt.ErrWorkloadNotFound) {
			// Log but don't fail the entire operation for not found workload
			logger.Warnf("Warning: Failed to stop workload %s: %v", name, err)
			return nil
		}
		return fmt.Errorf("failed to find workload %s: %v", name, err)
	}

	if workload.Status != rt.WorkloadStatusRunning {
		logger.Warnf("Warning: Failed to stop workload %s: %v", name, ErrWorkloadNotRunning)
		return nil
	}

	// Set status to stopping
	if err := d.statuses.SetWorkloadStatus(ctx, name, rt.WorkloadStatusStopping, ""); err != nil {
		logger.Debugf("Failed to set workload %s status to stopping: %v", name, err)
	}

	// Stop proxy if running
	if runConfig.BaseName != "" {
		d.stopProxyIfNeeded(name, runConfig.BaseName)
	}

	// For remote workloads, we only need to clean up client configurations
	// The saved state should be preserved for restart capability
	if err := removeClientConfigurations(name); err != nil {
		logger.Warnf("Warning: Failed to remove client configurations: %v", err)
	} else {
		logger.Infof("Client configurations for %s removed", name)
	}

	// Set status to stopped
	if err := d.statuses.SetWorkloadStatus(ctx, name, rt.WorkloadStatusStopped, ""); err != nil {
		logger.Debugf("Failed to set workload %s status to stopped: %v", name, err)
	}
	logger.Infof("Remote workload %s stopped successfully", name)
	return nil
}

// stopContainerWorkload stops a container-based workload
func (d *defaultManager) stopContainerWorkload(ctx context.Context, name string) error {
	container, err := d.runtime.GetWorkloadInfo(ctx, name)
	if err != nil {
		if errors.Is(err, rt.ErrWorkloadNotFound) {
			// Log but don't fail the entire operation for not found containers
			logger.Warnf("Warning: Failed to stop workload %s: %v", name, err)
			return nil
		}
		return fmt.Errorf("failed to find workload %s: %v", name, err)
	}

	running := container.IsRunning()
	if !running {
		// Log but don't fail the entire operation for not running containers
		logger.Warnf("Warning: Failed to stop workload %s: %v", name, ErrWorkloadNotRunning)
		return nil
	}

	// Transition workload to `stopping` state.
	if err := d.statuses.SetWorkloadStatus(ctx, name, rt.WorkloadStatusStopping, ""); err != nil {
		logger.Debugf("Failed to set workload %s status to stopping: %v", name, err)
	}

	// Use the existing stopWorkloads method for container workloads
	return d.stopSingleContainerWorkload(&container)
}

func (d *defaultManager) RunWorkload(ctx context.Context, runConfig *runner.RunConfig) error {
	// Ensure that the workload has a status entry before starting the process.
	if err := d.statuses.SetWorkloadStatus(ctx, runConfig.BaseName, rt.WorkloadStatusStarting, ""); err != nil {
		// Failure to create the initial state is a fatal error.
		return fmt.Errorf("failed to create workload status: %v", err)
	}

	mcpRunner := runner.NewRunner(runConfig, d.statuses)
	err := mcpRunner.Run(ctx)
	if err != nil {
		// If the run failed, we should set the status to error.
		if statusErr := d.statuses.SetWorkloadStatus(ctx, runConfig.BaseName, rt.WorkloadStatusError, err.Error()); statusErr != nil {
			logger.Warnf("Failed to set workload %s status to error: %v", runConfig.BaseName, statusErr)
		}
	}
	return err
}

func validateSecretParameters(ctx context.Context, runConfig *runner.RunConfig) error {
	// If there are run secrets, validate them
	if len(runConfig.Secrets) > 0 {
		cfg := config.GetConfig()

		providerType, err := cfg.Secrets.GetProviderType()
		if err != nil {
			return fmt.Errorf("error determining secrets provider type: %w", err)
		}

		secretManager, err := secrets.CreateSecretProvider(providerType)
		if err != nil {
			return fmt.Errorf("error instantiating secret manager: %w", err)
		}

		err = runConfig.ValidateSecrets(ctx, secretManager)
		if err != nil {
			return fmt.Errorf("error processing secrets: %w", err)
		}
	}
	return nil
}

func (d *defaultManager) RunWorkloadDetached(ctx context.Context, runConfig *runner.RunConfig) error {
	// before running, validate the parameters for the workload
	err := validateSecretParameters(ctx, runConfig)
	if err != nil {
		return fmt.Errorf("failed to validate workload parameters: %w", err)
	}

	// Get the current executable path
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %v", err)
	}

	// Create a log file for the detached process
	logFilePath, err := xdg.DataFile(fmt.Sprintf("toolhive/logs/%s.log", runConfig.BaseName))
	if err != nil {
		return fmt.Errorf("failed to create log file path: %v", err)
	}
	// #nosec G304 - This is safe as baseName is generated by the application
	logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		logger.Warnf("Warning: Failed to create log file: %v", err)
	} else {
		defer logFile.Close()
		logger.Infof("Logging to: %s", logFilePath)
	}

	// Use the restart command to start the detached process
	// The config has already been saved to disk, so restart can load it
	detachedArgs := []string{"restart", runConfig.BaseName, "--foreground"}

	// Create a new command
	// #nosec G204 - This is safe as execPath is the path to the current binary
	detachedCmd := exec.Command(execPath, detachedArgs...)

	// Set environment variables for the detached process
	detachedCmd.Env = append(os.Environ(), fmt.Sprintf("%s=%s", process.ToolHiveDetachedEnv, process.ToolHiveDetachedValue))

	// If we need the decrypt password, set it as an environment variable in the detached process.
	// NOTE: This breaks the abstraction slightly since this is only relevant for the CLI, but there
	// are checks inside `GetSecretsPassword` to ensure this does not get called in a detached process.
	// This will be addressed in a future re-think of the secrets manager interface.
	if needSecretsPassword(runConfig.Secrets) {
		password, err := secrets.GetSecretsPassword("")
		if err != nil {
			return fmt.Errorf("failed to get secrets password: %v", err)
		}
		detachedCmd.Env = append(detachedCmd.Env, fmt.Sprintf("%s=%s", secrets.PasswordEnvVar, password))
	}

	// Redirect stdout and stderr to the log file if it was created successfully
	if logFile != nil {
		detachedCmd.Stdout = logFile
		detachedCmd.Stderr = logFile
	} else {
		// Otherwise, discard the output
		detachedCmd.Stdout = nil
		detachedCmd.Stderr = nil
	}

	// Detach the process from the terminal
	detachedCmd.Stdin = nil
	detachedCmd.SysProcAttr = getSysProcAttr()

	// Ensure that the workload has a status entry before starting the process.
	if err = d.statuses.SetWorkloadStatus(ctx, runConfig.BaseName, rt.WorkloadStatusStarting, ""); err != nil {
		// Failure to create the initial state is a fatal error.
		return fmt.Errorf("failed to create workload status: %v", err)
	}

	// Start the detached process
	if err := detachedCmd.Start(); err != nil {
		// If the start failed, we need to set the status to error before returning.
		if err := d.statuses.SetWorkloadStatus(ctx, runConfig.BaseName, rt.WorkloadStatusError, ""); err != nil {
			logger.Warnf("Failed to set workload %s status to error: %v", runConfig.BaseName, err)
		}
		return fmt.Errorf("failed to start detached process: %v", err)
	}

	// Write the PID to a file so the stop command can kill the process
	if err := process.WritePIDFile(runConfig.BaseName, detachedCmd.Process.Pid); err != nil {
		logger.Warnf("Warning: Failed to write PID file: %v", err)
	}

	logger.Infof("MCP server is running in the background (PID: %d)", detachedCmd.Process.Pid)
	logger.Infof("Use 'thv stop %s' to stop the server", runConfig.ContainerName)

	return nil
}

func (d *defaultManager) GetLogs(ctx context.Context, workloadName string, follow bool) (string, error) {
	// Get the logs from the runtime
	logs, err := d.runtime.GetWorkloadLogs(ctx, workloadName, follow)
	if err != nil {
		// Propagate the error if the container is not found
		if errors.Is(err, rt.ErrWorkloadNotFound) {
			return "", fmt.Errorf("%w: %s", rt.ErrWorkloadNotFound, workloadName)
		}
		return "", fmt.Errorf("failed to get container logs %s: %v", workloadName, err)
	}

	return logs, nil
}

// deleteWorkload handles deletion of a single workload
func (d *defaultManager) deleteWorkload(name string) error {
	// Create a child context with a longer timeout
	childCtx, cancel := context.WithTimeout(context.Background(), AsyncOperationTimeout)
	defer cancel()

	// First, check if this is a remote workload by trying to load its run configuration
	runConfig, err := runner.LoadState(childCtx, name)
	if err != nil {
		// If we can't load the state, it might be a container workload or the workload doesn't exist
		// Continue with the container-based deletion logic
		return d.deleteContainerWorkload(childCtx, name)
	}

	// If this is a remote workload (has RemoteURL), handle it differently
	if runConfig.RemoteURL != "" {
		return d.deleteRemoteWorkload(childCtx, name, runConfig)
	}

	// This is a container-based workload, use the existing logic
	return d.deleteContainerWorkload(childCtx, name)
}

// deleteRemoteWorkload handles deletion of a remote workload
func (d *defaultManager) deleteRemoteWorkload(childCtx context.Context, name string, runConfig *runner.RunConfig) error {
	logger.Infof("Removing remote workload %s...", name)

	// Set status to removing
	if err := d.statuses.SetWorkloadStatus(childCtx, name, rt.WorkloadStatusRemoving, ""); err != nil {
		logger.Warnf("Failed to set workload %s status to removing: %v", name, err)
		return err
	}

	// Stop proxy if running
	if runConfig.BaseName != "" {
		d.stopProxyIfNeeded(name, runConfig.BaseName)
	}

	// Clean up associated resources
	d.cleanupWorkloadResources(childCtx, name, runConfig.BaseName)

	// Remove the workload status from the status store
	if err := d.statuses.DeleteWorkloadStatus(childCtx, name); err != nil {
		logger.Warnf("failed to delete workload status for %s: %v", name, err)
	}

	logger.Infof("Remote workload %s removed successfully", name)
	return nil
}

// deleteContainerWorkload handles deletion of a container-based workload (existing logic)
func (d *defaultManager) deleteContainerWorkload(childCtx context.Context, name string) error {

	// Find and validate the container
	container, err := d.getWorkloadContainer(childCtx, name)
	if err != nil {
		return err
	}

	// Set status to removing
	if err := d.statuses.SetWorkloadStatus(childCtx, name, rt.WorkloadStatusRemoving, ""); err != nil {
		logger.Warnf("Failed to set workload %s status to removing: %v", name, err)
	}

	if container != nil {
		containerLabels := container.Labels
		baseName := labels.GetContainerBaseName(containerLabels)

		// Stop proxy if running
		if container.IsRunning() {
			d.stopProxyIfNeeded(name, baseName)
		}

		// Remove the container
		if err := d.removeContainer(childCtx, name); err != nil {
			return err
		}

		// Clean up associated resources
		d.cleanupWorkloadResources(childCtx, name, baseName)
	}

	// Remove the workload status from the status store
	if err := d.statuses.DeleteWorkloadStatus(childCtx, name); err != nil {
		logger.Warnf("failed to delete workload status for %s: %v", name, err)
	}

	return nil
}

// getWorkloadContainer retrieves workload container info with error handling
func (d *defaultManager) getWorkloadContainer(childCtx context.Context, name string) (*rt.ContainerInfo, error) {
	container, err := d.runtime.GetWorkloadInfo(childCtx, name)
	if err != nil {
		if errors.Is(err, rt.ErrWorkloadNotFound) {
			// Log but don't fail the entire operation for not found containers
			logger.Warnf("Warning: Failed to get workload %s: %v", name, err)
			return nil, nil
		}
		if statusErr := d.statuses.SetWorkloadStatus(childCtx, name, rt.WorkloadStatusError, err.Error()); statusErr != nil {
			logger.Warnf("Failed to set workload %s status to error: %v", name, statusErr)
		}
		return nil, fmt.Errorf("failed to find workload %s: %v", name, err)
	}
	return &container, nil
}

// stopProxyIfNeeded stops the proxy process if the workload has a base name
func (*defaultManager) stopProxyIfNeeded(name, baseName string) {
	logger.Infof("Removing proxy process for %s...", name)
	if baseName != "" {
		proxy.StopProcess(baseName)
	}
}

// removeContainer removes the container from the runtime
func (d *defaultManager) removeContainer(childCtx context.Context, name string) error {
	logger.Infof("Removing container %s...", name)
	if err := d.runtime.RemoveWorkload(childCtx, name); err != nil {
		if statusErr := d.statuses.SetWorkloadStatus(childCtx, name, rt.WorkloadStatusError, err.Error()); statusErr != nil {
			logger.Warnf("Failed to set workload %s status to error: %v", name, statusErr)
		}
		return fmt.Errorf("failed to remove container: %v", err)
	}
	return nil
}

// cleanupWorkloadResources cleans up all resources associated with a workload
func (d *defaultManager) cleanupWorkloadResources(childCtx context.Context, name, baseName string) {
	if baseName == "" {
		return
	}

	// Clean up temporary permission profile
	if err := d.cleanupTempPermissionProfile(childCtx, baseName); err != nil {
		logger.Warnf("Warning: Failed to cleanup temporary permission profile: %v", err)
	}

	// Remove client configurations
	if err := removeClientConfigurations(name); err != nil {
		logger.Warnf("Warning: Failed to remove client configurations: %v", err)
	} else {
		logger.Infof("Client configurations for %s removed", name)
	}

	// Delete the saved state last
	if err := state.DeleteSavedRunConfig(childCtx, baseName); err != nil {
		logger.Warnf("Warning: Failed to delete saved state: %v", err)
	} else {
		logger.Infof("Saved state for %s removed", baseName)
	}

	logger.Infof("Container %s removed", name)
}

func (d *defaultManager) DeleteWorkloads(_ context.Context, names []string) (*errgroup.Group, error) {
	// Validate all workload names to prevent path traversal attacks
	for _, name := range names {
		if err := types.ValidateWorkloadName(name); err != nil {
			return nil, fmt.Errorf("invalid workload name '%s': %w", name, err)
		}
	}

	group := &errgroup.Group{}

	for _, name := range names {
		group.Go(func() error {
			return d.deleteWorkload(name)
		})
	}

	return group, nil
}

// RestartWorkloads restarts the specified workloads by name.
func (d *defaultManager) RestartWorkloads(_ context.Context, names []string, foreground bool) (*errgroup.Group, error) {
	// Validate all workload names to prevent path traversal attacks
	for _, name := range names {
		if err := types.ValidateWorkloadName(name); err != nil {
			return nil, fmt.Errorf("invalid workload name '%s': %w", name, err)
		}
	}

	group := &errgroup.Group{}

	for _, name := range names {
		group.Go(func() error {
			return d.restartSingleWorkload(name, foreground)
		})
	}

	return group, nil
}

// restartSingleWorkload handles the restart logic for a single workload
func (d *defaultManager) restartSingleWorkload(name string, foreground bool) error {
	// Create a child context with a longer timeout
	childCtx, cancel := context.WithTimeout(context.Background(), AsyncOperationTimeout)
	defer cancel()

	// First, try to load the run configuration to check if it's a remote workload
	runConfig, err := runner.LoadState(childCtx, name)
	if err != nil {
		// If we can't load the state, it might be a container workload or the workload doesn't exist
		// Try to restart it as a container workload
		return d.restartContainerWorkload(childCtx, name, foreground)
	}

	// Check if this is a remote workload
	if runConfig.RemoteURL != "" {
		return d.restartRemoteWorkload(childCtx, name, runConfig, foreground)
	}

	// This is a container-based workload
	return d.restartContainerWorkload(childCtx, name, foreground)
}

// restartRemoteWorkload handles restarting a remote workload
func (d *defaultManager) restartRemoteWorkload(
	childCtx context.Context,
	name string,
	runConfig *runner.RunConfig,
	foreground bool,
) error {
	workloadState := d.getRemoteWorkloadState(childCtx, name, runConfig.BaseName)

	if d.isWorkloadAlreadyRunning(name, workloadState) {
		return nil
	}

	// Load runner configuration from state
	mcpRunner, err := d.loadRunnerFromState(childCtx, runConfig.BaseName)
	if err != nil {
		return fmt.Errorf("failed to load state for %s: %v", runConfig.BaseName, err)
	}

	// Set status to starting
	if err := d.statuses.SetWorkloadStatus(childCtx, name, rt.WorkloadStatusStarting, ""); err != nil {
		logger.Warnf("Failed to set workload %s status to starting: %v", name, err)
	}

	logger.Infof("Loaded configuration from state for %s", runConfig.BaseName)

	// Start the remote workload using the loaded runner
	return d.startWorkload(childCtx, name, mcpRunner, foreground)
}

// restartContainerWorkload handles restarting a container-based workload
func (d *defaultManager) restartContainerWorkload(childCtx context.Context, name string, foreground bool) error {
	// Get workload state information
	workloadState, err := d.getWorkloadState(childCtx, name)
	if err != nil {
		return err
	}

	// Check if already running
	if d.isWorkloadAlreadyRunning(name, workloadState) {
		return nil
	}

	// Load runner configuration from state
	mcpRunner, err := d.loadRunnerFromState(childCtx, workloadState.BaseName)
	if err != nil {
		return fmt.Errorf("failed to load state for %s: %v", workloadState.BaseName, err)
	}

	// Set workload status to starting
	if err := d.statuses.SetWorkloadStatus(childCtx, name, rt.WorkloadStatusStarting, ""); err != nil {
		logger.Warnf("Failed to set workload %s status to starting: %v", name, err)
	}
	logger.Infof("Loaded configuration from state for %s", workloadState.BaseName)

	// Stop container if running but proxy is not
	if err := d.stopContainerIfNeeded(childCtx, name, workloadState); err != nil {
		return err
	}

	// Start the workload
	return d.startWorkload(childCtx, name, mcpRunner, foreground)
}

// workloadState holds the current state of a workload for restart operations
type workloadState struct {
	BaseName     string
	Running      bool
	ProxyRunning bool
}

// getWorkloadState retrieves the current state of a workload
func (d *defaultManager) getWorkloadState(ctx context.Context, name string) (*workloadState, error) {
	workloadSt := &workloadState{}

	// Try to find the container
	container, err := d.runtime.GetWorkloadInfo(ctx, name)
	if err != nil {
		if errors.Is(err, rt.ErrWorkloadNotFound) {
			logger.Warnf("Warning: Failed to find container: %v", err)
			logger.Warnf("Trying to find state with name %s directly...", name)
			// Try to use the provided name as the base name
			workloadSt.BaseName = name
			workloadSt.Running = false
		} else {
			return nil, fmt.Errorf("failed to find workload %s: %v", name, err)
		}
	} else {
		// Container found, check if it's running and get the base name
		workloadSt.Running = container.IsRunning()
		workloadSt.BaseName = labels.GetContainerBaseName(container.Labels)
	}

	// Check if the proxy process is running
	workloadSt.ProxyRunning = proxy.IsRunning(workloadSt.BaseName)

	return workloadSt, nil
}

// getRemoteWorkloadState retrieves the current state of a remote workload
func (d *defaultManager) getRemoteWorkloadState(ctx context.Context, name, baseName string) *workloadState {
	workloadSt := &workloadState{
		BaseName: baseName,
	}

	// Check the workload status
	workload, err := d.statuses.GetWorkload(ctx, name)
	if err != nil {
		// If we can't get the status, assume it's not running
		logger.Debugf("Failed to get status for remote workload %s: %v", name, err)
		workloadSt.Running = false
	} else {
		workloadSt.Running = (workload.Status == rt.WorkloadStatusRunning)
	}

	// Check if the detached process is actually running
	workloadSt.ProxyRunning = proxy.IsRunning(baseName)

	return workloadSt
}

// isWorkloadAlreadyRunning checks if the workload is already fully running
func (*defaultManager) isWorkloadAlreadyRunning(name string, workloadSt *workloadState) bool {
	if workloadSt.Running && workloadSt.ProxyRunning {
		logger.Infof("Container %s and proxy are already running", name)
		return true
	}
	return false
}

// stopContainerIfNeeded stops the container if it's running but proxy is not
func (d *defaultManager) stopContainerIfNeeded(childCtx context.Context, name string, workloadSt *workloadState) error {
	if !workloadSt.Running {
		return nil
	}

	logger.Infof("Container %s is running but proxy is not. Stopping container...", name)
	if err := d.runtime.StopWorkload(childCtx, name); err != nil {
		if statusErr := d.statuses.SetWorkloadStatus(childCtx, name, rt.WorkloadStatusError, ""); statusErr != nil {
			logger.Warnf("Failed to set workload %s status to error: %v", name, statusErr)
		}
		return fmt.Errorf("failed to stop container %s: %v", name, err)
	}
	logger.Infof("Container %s stopped", name)
	return nil
}

// startWorkload starts the workload in either foreground or background mode
func (d *defaultManager) startWorkload(ctx context.Context, name string, mcpRunner *runner.Runner, foreground bool) error {
	logger.Infof("Starting tooling server %s...", name)

	var err error
	if foreground {
		err = d.RunWorkload(ctx, mcpRunner.Config)
	} else {
		err = d.RunWorkloadDetached(ctx, mcpRunner.Config)
	}

	if err != nil {
		// If we could not start the workload, set the status to error before returning
		if statusErr := d.statuses.SetWorkloadStatus(ctx, name, rt.WorkloadStatusError, ""); statusErr != nil {
			logger.Warnf("Failed to set workload %s status to error: %v", name, statusErr)
		}
	}
	return err
}

// TODO: Move to dedicated config management interface.
// updateClientConfigurations updates client configuration files with the MCP server URL
func removeClientConfigurations(containerName string) error {
	// Get the workload's group by loading its run config
	runConfig, err := runner.LoadState(context.Background(), containerName)
	var group string
	if err != nil {
		logger.Warnf("Warning: Failed to load run config for %s, will use backward compatible behavior: %v", containerName, err)
		// Continue with empty group (backward compatibility)
	} else {
		group = runConfig.Group
	}

	clientManager, err := client.NewManager(context.Background())
	if err != nil {
		logger.Warnf("Warning: Failed to create client manager for %s, skipping client config removal: %v", containerName, err)
		return nil
	}

	return clientManager.RemoveServerFromClients(context.Background(), containerName, group)
}

// loadRunnerFromState attempts to load a Runner from the state store
func (d *defaultManager) loadRunnerFromState(ctx context.Context, baseName string) (*runner.Runner, error) {
	// Load the run config from the state store
	runConfig, err := runner.LoadState(ctx, baseName)
	if err != nil {
		return nil, err
	}

	if runConfig.RemoteURL != "" {
		// For remote workloads, we don't need a deployer
		runConfig.Deployer = nil
	} else {
		// Update the runtime in the loaded configuration
		runConfig.Deployer = d.runtime
	}

	// Create a new runner with the loaded configuration
	return runner.NewRunner(runConfig, d.statuses), nil
}

func needSecretsPassword(secretOptions []string) bool {
	// If the user did not ask for any secrets, then don't attempt to instantiate
	// the secrets manager.
	if len(secretOptions) == 0 {
		return false
	}
	// Ignore err - if the flag is not set, it's not needed.
	providerType, _ := config.GetConfig().Secrets.GetProviderType()
	return providerType == secrets.EncryptedType
}

// cleanupTempPermissionProfile cleans up temporary permission profile files for a given base name
func (*defaultManager) cleanupTempPermissionProfile(ctx context.Context, baseName string) error {
	// Try to load the saved configuration to get the permission profile path
	runConfig, err := runner.LoadState(ctx, baseName)
	if err != nil {
		// If we can't load the state, there's nothing to clean up
		logger.Debugf("Could not load state for %s, skipping permission profile cleanup: %v", baseName, err)
		return nil
	}

	// Clean up the temporary permission profile if it exists
	if runConfig.PermissionProfileNameOrPath != "" {
		if err := runner.CleanupTempPermissionProfile(runConfig.PermissionProfileNameOrPath); err != nil {
			return fmt.Errorf("failed to cleanup temporary permission profile: %v", err)
		}
	}

	return nil
}

// stopSingleContainerWorkload stops a single container workload
func (d *defaultManager) stopSingleContainerWorkload(workload *rt.ContainerInfo) error {
	childCtx, cancel := context.WithTimeout(context.Background(), AsyncOperationTimeout)
	defer cancel()

	name := labels.GetContainerBaseName(workload.Labels)
	// Stop the proxy process
	proxy.StopProcess(name)

	logger.Infof("Stopping containers for %s...", name)
	// Stop the container
	if err := d.runtime.StopWorkload(childCtx, workload.Name); err != nil {
		if statusErr := d.statuses.SetWorkloadStatus(childCtx, name, rt.WorkloadStatusError, err.Error()); statusErr != nil {
			logger.Warnf("Failed to set workload %s status to error: %v", name, statusErr)
		}
		return fmt.Errorf("failed to stop container: %w", err)
	}

	if err := removeClientConfigurations(name); err != nil {
		logger.Warnf("Warning: Failed to remove client configurations: %v", err)
	} else {
		logger.Infof("Client configurations for %s removed", name)
	}

	if err := d.statuses.SetWorkloadStatus(childCtx, name, rt.WorkloadStatusStopped, ""); err != nil {
		logger.Warnf("Failed to set workload %s status to stopped: %v", name, err)
	}
	logger.Infof("Successfully stopped %s...", name)
	return nil
}

// MoveToGroup moves the specified workloads from one group to another by updating their runconfig.
func (*defaultManager) MoveToGroup(ctx context.Context, workloadNames []string, groupFrom string, groupTo string) error {
	for _, workloadName := range workloadNames {
		// Validate workload name
		if err := types.ValidateWorkloadName(workloadName); err != nil {
			return fmt.Errorf("invalid workload name %s: %w", workloadName, err)
		}

		// Load the runner state to check and update the configuration
		runnerConfig, err := runner.LoadState(ctx, workloadName)
		if err != nil {
			return fmt.Errorf("failed to load runner state for workload %s: %w", workloadName, err)
		}

		// Check if the workload is actually in the specified group
		if runnerConfig.Group != groupFrom {
			logger.Debugf("Workload %s is not in group %s (current group: %s), skipping",
				workloadName, groupFrom, runnerConfig.Group)
			continue
		}

		// Move the workload to the default group
		runnerConfig.Group = groupTo

		// Save the updated configuration
		if err = runnerConfig.SaveState(ctx); err != nil {
			return fmt.Errorf("failed to save updated configuration for workload %s: %w", workloadName, err)
		}

		logger.Infof("Moved workload %s to default group", workloadName)
	}

	return nil
}

// ListWorkloadsInGroup returns all workload names that belong to the specified group
func (d *defaultManager) ListWorkloadsInGroup(ctx context.Context, groupName string) ([]string, error) {
	workloads, err := d.ListWorkloads(ctx, true) // listAll=true to include stopped workloads
	if err != nil {
		return nil, fmt.Errorf("failed to list workloads: %w", err)
	}

	// Filter workloads that belong to the specified group
	var groupWorkloads []string
	for _, workload := range workloads {
		if workload.Group == groupName {
			groupWorkloads = append(groupWorkloads, workload.Name)
		}
	}

	return groupWorkloads, nil
}

// getRemoteWorkloadsFromState retrieves remote servers from the state store
func (d *defaultManager) getRemoteWorkloadsFromState(
	ctx context.Context,
	listAll bool,
	labelFilters []string,
) ([]core.Workload, error) {
	// Create a state store
	store, err := state.NewRunConfigStore(state.DefaultAppName)
	if err != nil {
		return nil, fmt.Errorf("failed to create state store: %w", err)
	}

	// List all configurations
	configNames, err := store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list configurations: %w", err)
	}

	// Parse the filters into a format we can use for matching
	parsedFilters, err := types.ParseLabelFilters(labelFilters)
	if err != nil {
		return nil, fmt.Errorf("failed to parse label filters: %v", err)
	}

	var remoteWorkloads []core.Workload

	for _, name := range configNames {
		// Load the run configuration
		runConfig, err := runner.LoadState(ctx, name)
		if err != nil {
			logger.Warnf("failed to load state for %s: %v", name, err)
			continue
		}

		// Only include remote servers (those with RemoteURL set)
		if runConfig.RemoteURL == "" {
			continue
		}

		// Check the status from the status file
		workloadStatus, err := d.statuses.GetWorkload(ctx, name)
		if err != nil {
			logger.Warnf("failed to get status for remote workload %s: %v", name, err)
			continue
		}

		// Apply listAll filter - only include running workloads unless listAll is true
		if !listAll && workloadStatus.Status != rt.WorkloadStatusRunning {
			continue
		}

		// Use the transport type directly since it's already parsed
		transportType := runConfig.Transport

		// Create a workload from the run configuration
		workload := core.Workload{
			Name:          name,
			Package:       "remote",
			Status:        workloadStatus.Status,
			URL:           runConfig.RemoteURL,
			Port:          runConfig.Port,
			TransportType: transportType,
			ToolType:      "remote",
			Group:         runConfig.Group,
			CreatedAt:     workloadStatus.CreatedAt,
			Labels:        runConfig.ContainerLabels,
			Remote:        true,
		}

		// Apply label filtering
		if types.MatchesLabelFilters(workload.Labels, parsedFilters) {
			remoteWorkloads = append(remoteWorkloads, workload)
		}
	}

	return remoteWorkloads, nil
}
