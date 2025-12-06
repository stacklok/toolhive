// Package workloads contains high-level logic for managing the lifecycle of
// ToolHive-managed containers.
package workloads

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/vmcp"
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
	// UpdateWorkload updates a workload by stopping, deleting, and recreating it.
	// It is implemented as an asynchronous operation which returns an errgroup.Group
	UpdateWorkload(ctx context.Context, workloadName string, newConfig *runner.RunConfig) (*errgroup.Group, error)
	// GetLogs retrieves the logs of a container.
	GetLogs(ctx context.Context, containerName string, follow bool) (string, error)
	// GetProxyLogs retrieves the proxy logs from the filesystem.
	GetProxyLogs(ctx context.Context, workloadName string) (string, error)
	// MoveToGroup moves the specified workloads from one group to another by updating their runconfig.
	MoveToGroup(ctx context.Context, workloadNames []string, groupFrom string, groupTo string) error
	// ListWorkloadsInGroup returns all workload names that belong to the specified group, including stopped workloads.
	ListWorkloadsInGroup(ctx context.Context, groupName string) ([]string, error)
	// DoesWorkloadExist checks if a workload with the given name exists.
	DoesWorkloadExist(ctx context.Context, workloadName string) (bool, error)
}

// DefaultManager is the default implementation of the Manager interface.
type DefaultManager struct {
	runtime        rt.Runtime
	statuses       statuses.StatusManager
	configProvider config.Provider
}

// ErrWorkloadNotRunning is returned when a container cannot be found by name.
var ErrWorkloadNotRunning = fmt.Errorf("workload not running")

const (
	// AsyncOperationTimeout is the timeout for async workload operations
	AsyncOperationTimeout = 5 * time.Minute
)

// NewManager creates a new container manager instance.
func NewManager(ctx context.Context) (*DefaultManager, error) {
	runtime, err := ct.NewFactory().Create(ctx)
	if err != nil {
		return nil, err
	}

	statusManager, err := statuses.NewStatusManager(runtime)
	if err != nil {
		return nil, fmt.Errorf("failed to create status manager: %w", err)
	}

	return &DefaultManager{
		runtime:        runtime,
		statuses:       statusManager,
		configProvider: config.NewDefaultProvider(),
	}, nil
}

// NewManagerWithProvider creates a new container manager instance with a custom config provider.
func NewManagerWithProvider(ctx context.Context, configProvider config.Provider) (Manager, error) {
	runtime, err := ct.NewFactory().Create(ctx)
	if err != nil {
		return nil, err
	}

	statusManager, err := statuses.NewStatusManager(runtime)
	if err != nil {
		return nil, fmt.Errorf("failed to create status manager: %w", err)
	}

	return &DefaultManager{
		runtime:        runtime,
		statuses:       statusManager,
		configProvider: configProvider,
	}, nil
}

// NewManagerFromRuntime creates a new container manager instance from an existing runtime.
func NewManagerFromRuntime(runtime rt.Runtime) (Manager, error) {
	statusManager, err := statuses.NewStatusManager(runtime)
	if err != nil {
		return nil, fmt.Errorf("failed to create status manager: %w", err)
	}

	return &DefaultManager{
		runtime:        runtime,
		statuses:       statusManager,
		configProvider: config.NewDefaultProvider(),
	}, nil
}

// NewManagerFromRuntimeWithProvider creates a new container manager instance from an existing runtime with a
// custom config provider.
func NewManagerFromRuntimeWithProvider(runtime rt.Runtime, configProvider config.Provider) (Manager, error) {
	statusManager, err := statuses.NewStatusManager(runtime)
	if err != nil {
		return nil, fmt.Errorf("failed to create status manager: %w", err)
	}

	return &DefaultManager{
		runtime:        runtime,
		statuses:       statusManager,
		configProvider: configProvider,
	}, nil
}

// GetWorkload retrieves details of the named workload including its status.
func (d *DefaultManager) GetWorkload(ctx context.Context, workloadName string) (core.Workload, error) {
	// For the sake of minimizing changes, delegate to the status manager.
	// Whether this method should still belong to the workload manager is TBD.
	return d.statuses.GetWorkload(ctx, workloadName)
}

// GetWorkloadAsVMCPBackend retrieves a workload and converts it to a vmcp.Backend.
// This method eliminates indirection by directly returning the vmcp.Backend type
// needed by vmcp workload discovery, avoiding the need for callers to convert
// from core.Workload to vmcp.Backend.
// Returns nil if the workload exists but is not accessible (e.g., no URL).
func (d *DefaultManager) GetWorkloadAsVMCPBackend(ctx context.Context, workloadName string) (*vmcp.Backend, error) {
	workload, err := d.statuses.GetWorkload(ctx, workloadName)
	if err != nil {
		return nil, err
	}

	// Skip workloads without a URL (not accessible)
	if workload.URL == "" {
		logger.Debugf("Skipping workload %s without URL", workloadName)
		return nil, nil
	}

	// Map workload status to backend health status
	healthStatus := mapWorkloadStatusToVMCPHealth(workload.Status)

	// Use ProxyMode instead of TransportType to reflect how ToolHive is exposing the workload.
	// For stdio MCP servers, ToolHive proxies them via SSE or streamable-http.
	// ProxyMode tells us which transport the vmcp client should use.
	transportType := workload.ProxyMode
	if transportType == "" {
		// Fallback to TransportType if ProxyMode is not set (for direct transports)
		transportType = workload.TransportType.String()
	}

	backend := &vmcp.Backend{
		ID:            workload.Name,
		Name:          workload.Name,
		BaseURL:       workload.URL,
		TransportType: transportType,
		HealthStatus:  healthStatus,
		Metadata:      make(map[string]string),
	}

	// Copy user labels to metadata first
	for k, v := range workload.Labels {
		backend.Metadata[k] = v
	}

	// Set system metadata (these override user labels to prevent conflicts)
	backend.Metadata["workload_status"] = string(workload.Status)

	return backend, nil
}

// mapWorkloadStatusToVMCPHealth converts a WorkloadStatus to a vmcp BackendHealthStatus.
func mapWorkloadStatusToVMCPHealth(status rt.WorkloadStatus) vmcp.BackendHealthStatus {
	switch status {
	case rt.WorkloadStatusRunning:
		return vmcp.BackendHealthy
	case rt.WorkloadStatusUnhealthy:
		return vmcp.BackendUnhealthy
	case rt.WorkloadStatusStopped, rt.WorkloadStatusError, rt.WorkloadStatusStopping, rt.WorkloadStatusRemoving:
		return vmcp.BackendUnhealthy
	case rt.WorkloadStatusStarting, rt.WorkloadStatusUnknown:
		return vmcp.BackendUnknown
	case rt.WorkloadStatusUnauthenticated:
		return vmcp.BackendUnauthenticated
	default:
		return vmcp.BackendUnknown
	}
}

// DoesWorkloadExist checks if a workload with the given name exists.
func (d *DefaultManager) DoesWorkloadExist(ctx context.Context, workloadName string) (bool, error) {
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

// ListWorkloads retrieves the states of all workloads.
func (d *DefaultManager) ListWorkloads(ctx context.Context, listAll bool, labelFilters ...string) ([]core.Workload, error) {
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

// StopWorkloads stops the specified workloads by name.
func (d *DefaultManager) StopWorkloads(_ context.Context, names []string) (*errgroup.Group, error) {
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
func (d *DefaultManager) stopSingleWorkload(name string) error {
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
func (d *DefaultManager) stopRemoteWorkload(ctx context.Context, name string, runConfig *runner.RunConfig) error {
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
		d.stopProxyIfNeeded(ctx, name, runConfig.BaseName)
	}

	// For remote workloads, we only need to clean up client configurations
	// The saved state should be preserved for restart capability
	if err := removeClientConfigurations(name, false); err != nil {
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
func (d *DefaultManager) stopContainerWorkload(ctx context.Context, name string) error {
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
	return d.stopSingleContainerWorkload(ctx, &container)
}

// RunWorkload runs a workload in the foreground with automatic restart on container exit.
func (d *DefaultManager) RunWorkload(ctx context.Context, runConfig *runner.RunConfig) error {
	// Ensure that the workload has a status entry before starting the process.
	if err := d.statuses.SetWorkloadStatus(ctx, runConfig.BaseName, rt.WorkloadStatusStarting, ""); err != nil {
		// Failure to create the initial state is a fatal error.
		return fmt.Errorf("failed to create workload status: %v", err)
	}

	// Retry loop with exponential backoff for container restarts
	maxRetries := 10 // Allow many retries for transient issues
	retryDelay := 5 * time.Second

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if attempt > 1 {
			logger.Infof("Restart attempt %d/%d for %s after %v delay", attempt, maxRetries, runConfig.BaseName, retryDelay)
			time.Sleep(retryDelay)

			// Exponential backoff: 5s, 10s, 20s, 40s, 60s (capped)
			retryDelay *= 2
			if retryDelay > 60*time.Second {
				retryDelay = 60 * time.Second
			}
		}

		mcpRunner := runner.NewRunner(runConfig, d.statuses)
		err := mcpRunner.Run(ctx)

		if err != nil {
			// Check if this is a "container exited, restart needed" error
			if err.Error() == "container exited, restart needed" {
				logger.Warnf("Container %s exited unexpectedly (attempt %d/%d). Restarting...",
					runConfig.BaseName, attempt, maxRetries)

				// Remove from client config so clients notice the restart
				clientManager, clientErr := client.NewManager(ctx)
				if clientErr == nil {
					logger.Infof("Removing %s from client configurations before restart", runConfig.BaseName)
					if removeErr := clientManager.RemoveServerFromClients(ctx, runConfig.BaseName, runConfig.Group); removeErr != nil {
						logger.Warnf("Warning: Failed to remove from client config: %v", removeErr)
					}
				}

				// Set status to starting (since we're restarting)
				statusErr := d.statuses.SetWorkloadStatus(
					ctx,
					runConfig.BaseName,
					rt.WorkloadStatusStarting,
					"Container exited, restarting",
				)
				if statusErr != nil {
					logger.Warnf("Failed to set workload %s status to starting: %v", runConfig.BaseName, statusErr)
				}

				// If we haven't exhausted retries, continue the loop
				if attempt < maxRetries {
					continue
				}

				// Exhausted all retries
				logger.Errorf("Failed to restart %s after %d attempts. Giving up.", runConfig.BaseName, maxRetries)
				statusErr = d.statuses.SetWorkloadStatus(
					ctx,
					runConfig.BaseName,
					rt.WorkloadStatusError,
					"Failed to restart after container exit",
				)
				if statusErr != nil {
					logger.Warnf("Failed to set workload %s status to error: %v", runConfig.BaseName, statusErr)
				}
				return fmt.Errorf("container restart failed after %d attempts", maxRetries)
			}

			// Some other error - don't retry
			logger.Errorf("Workload %s failed with error: %v", runConfig.BaseName, err)
			if statusErr := d.statuses.SetWorkloadStatus(ctx, runConfig.BaseName, rt.WorkloadStatusError, err.Error()); statusErr != nil {
				logger.Warnf("Failed to set workload %s status to error: %v", runConfig.BaseName, statusErr)
			}
			return err
		}

		// Success - workload completed normally
		return nil
	}

	// Should not reach here, but just in case
	return fmt.Errorf("unexpected end of retry loop for %s", runConfig.BaseName)
}

// validateSecretParameters validates the secret parameters for a workload.
func (d *DefaultManager) validateSecretParameters(ctx context.Context, runConfig *runner.RunConfig) error {
	// If there are run secrets, validate them

	hasRegularSecrets := len(runConfig.Secrets) > 0
	hasRemoteAuthSecret := runConfig.RemoteAuthConfig != nil && runConfig.RemoteAuthConfig.ClientSecret != ""

	if hasRegularSecrets || hasRemoteAuthSecret {
		cfg := d.configProvider.GetConfig()

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

// RunWorkloadDetached runs a workload in the background.
func (d *DefaultManager) RunWorkloadDetached(ctx context.Context, runConfig *runner.RunConfig) error {
	// before running, validate the parameters for the workload
	err := d.validateSecretParameters(ctx, runConfig)
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
	if d.needSecretsPassword(runConfig.Secrets) {
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
	if err := d.statuses.SetWorkloadPID(ctx, runConfig.BaseName, detachedCmd.Process.Pid); err != nil {
		logger.Warnf("Failed to set workload %s PID: %v", runConfig.BaseName, err)
	}

	logger.Infof("MCP server is running in the background (PID: %d)", detachedCmd.Process.Pid)
	logger.Infof("Use 'thv stop %s' to stop the server", runConfig.ContainerName)

	return nil
}

// GetLogs retrieves the logs of a container.
func (d *DefaultManager) GetLogs(ctx context.Context, workloadName string, follow bool) (string, error) {
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

// GetProxyLogs retrieves proxy logs from the filesystem
func (*DefaultManager) GetProxyLogs(_ context.Context, workloadName string) (string, error) {
	// Get the proxy log file path
	logFilePath, err := xdg.DataFile(fmt.Sprintf("toolhive/logs/%s.log", workloadName))
	if err != nil {
		return "", fmt.Errorf("failed to get proxy log file path for workload %s: %w", workloadName, err)
	}

	// Clean the file path to prevent path traversal
	cleanLogFilePath := filepath.Clean(logFilePath)

	// Check if the log file exists
	if _, err := os.Stat(cleanLogFilePath); os.IsNotExist(err) {
		return "", fmt.Errorf("proxy logs not found for workload %s", workloadName)
	}

	// Read and return the entire log file
	content, err := os.ReadFile(cleanLogFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to read proxy log for workload %s: %w", workloadName, err)
	}

	return string(content), nil
}

// deleteWorkload handles deletion of a single workload
func (d *DefaultManager) deleteWorkload(name string) error {
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
func (d *DefaultManager) deleteRemoteWorkload(ctx context.Context, name string, runConfig *runner.RunConfig) error {
	logger.Infof("Removing remote workload %s...", name)

	// Set status to removing
	if err := d.statuses.SetWorkloadStatus(ctx, name, rt.WorkloadStatusRemoving, ""); err != nil {
		logger.Warnf("Failed to set workload %s status to removing: %v", name, err)
		return err
	}

	// Stop proxy if running
	if runConfig.BaseName != "" {
		d.stopProxyIfNeeded(ctx, name, runConfig.BaseName)
	}

	// Clean up associated resources (remote workloads are not auxiliary)
	d.cleanupWorkloadResources(ctx, name, runConfig.BaseName, false)

	// Remove the workload status from the status store
	if err := d.statuses.DeleteWorkloadStatus(ctx, name); err != nil {
		logger.Warnf("failed to delete workload status for %s: %v", name, err)
	}

	logger.Infof("Remote workload %s removed successfully", name)
	return nil
}

// deleteContainerWorkload handles deletion of a container-based workload (existing logic)
func (d *DefaultManager) deleteContainerWorkload(ctx context.Context, name string) error {

	// Find and validate the container
	container, err := d.getWorkloadContainer(ctx, name)
	if err != nil {
		return err
	}

	// Set status to removing
	if err := d.statuses.SetWorkloadStatus(ctx, name, rt.WorkloadStatusRemoving, ""); err != nil {
		logger.Warnf("Failed to set workload %s status to removing: %v", name, err)
	}

	if container != nil {
		containerLabels := container.Labels
		baseName := labels.GetContainerBaseName(containerLabels)

		// Stop proxy if running (skip for auxiliary workloads like inspector)
		if container.IsRunning() {
			// Skip proxy stopping for auxiliary workloads that don't use proxy processes
			if labels.IsAuxiliaryWorkload(containerLabels) {
				logger.Debugf("Skipping proxy stop for auxiliary workload %s", name)
			} else {
				d.stopProxyIfNeeded(ctx, name, baseName)
			}
		}

		// Remove the container
		if err := d.removeContainer(ctx, name); err != nil {
			return err
		}

		// Clean up associated resources
		d.cleanupWorkloadResources(ctx, name, baseName, labels.IsAuxiliaryWorkload(containerLabels))
	}

	// Remove the workload status from the status store
	if err := d.statuses.DeleteWorkloadStatus(ctx, name); err != nil {
		logger.Warnf("failed to delete workload status for %s: %v", name, err)
	}

	return nil
}

// getWorkloadContainer retrieves workload container info with error handling
func (d *DefaultManager) getWorkloadContainer(ctx context.Context, name string) (*rt.ContainerInfo, error) {
	container, err := d.runtime.GetWorkloadInfo(ctx, name)
	if err != nil {
		if errors.Is(err, rt.ErrWorkloadNotFound) {
			// Log but don't fail the entire operation for not found containers
			logger.Warnf("Warning: Failed to get workload %s: %v", name, err)
			return nil, nil
		}
		if statusErr := d.statuses.SetWorkloadStatus(ctx, name, rt.WorkloadStatusError, err.Error()); statusErr != nil {
			logger.Warnf("Failed to set workload %s status to error: %v", name, statusErr)
		}
		return nil, fmt.Errorf("failed to find workload %s: %v", name, err)
	}
	return &container, nil
}

// isSupervisorProcessAlive checks if the supervisor process for a workload is alive
// by checking if a PID exists. If a PID exists, we assume the supervisor is running.
// This is a reasonable assumption because:
// - If the supervisor exits cleanly, it cleans up the PID
// - If killed unexpectedly, the PID remains but stopProcess will handle it gracefully
// - The main issue we're preventing is accumulating zombie supervisors from repeated restarts
func (d *DefaultManager) isSupervisorProcessAlive(ctx context.Context, name string) bool {
	if name == "" {
		return false
	}

	// Try to read the PID - if it exists, assume supervisor is running
	_, err := d.statuses.GetWorkloadPID(ctx, name)
	if err != nil {
		// No PID found, supervisor is not running
		return false
	}

	// PID exists, assume supervisor is alive
	return true
}

// stopProcess stops the proxy process associated with the container
func (d *DefaultManager) stopProcess(ctx context.Context, name string) {
	if name == "" {
		logger.Warnf("Warning: Could not find base container name in labels")
		return
	}

	// Try to read the PID and kill the process
	pid, err := d.statuses.GetWorkloadPID(ctx, name)
	if err != nil {
		logger.Errorf("No PID file found for %s, proxy may not be running in detached mode", name)
		return
	}

	// PID file found, try to kill the process
	logger.Infof("Stopping proxy process (PID: %d)...", pid)
	if err := process.KillProcess(pid); err != nil {
		logger.Warnf("Warning: Failed to kill proxy process: %v", err)
	} else {
		logger.Info("Proxy process stopped")
	}

	// Clean up PID file after successful kill
	if err := process.RemovePIDFile(name); err != nil {
		logger.Warnf("Warning: Failed to remove PID file: %v", err)
	}
}

// stopProxyIfNeeded stops the proxy process if the workload has a base name
func (d *DefaultManager) stopProxyIfNeeded(ctx context.Context, name, baseName string) {
	logger.Infof("Removing proxy process for %s...", name)
	if baseName != "" {
		d.stopProcess(ctx, baseName)
	}
}

// removeContainer removes the container from the runtime
func (d *DefaultManager) removeContainer(ctx context.Context, name string) error {
	logger.Infof("Removing container %s...", name)
	if err := d.runtime.RemoveWorkload(ctx, name); err != nil {
		if statusErr := d.statuses.SetWorkloadStatus(ctx, name, rt.WorkloadStatusError, err.Error()); statusErr != nil {
			logger.Warnf("Failed to set workload %s status to error: %v", name, statusErr)
		}
		return fmt.Errorf("failed to remove container: %v", err)
	}
	return nil
}

// cleanupWorkloadResources cleans up all resources associated with a workload
func (d *DefaultManager) cleanupWorkloadResources(ctx context.Context, name, baseName string, isAuxiliary bool) {
	if baseName == "" {
		return
	}

	// Clean up temporary permission profile
	if err := d.cleanupTempPermissionProfile(ctx, baseName); err != nil {
		logger.Warnf("Warning: Failed to cleanup temporary permission profile: %v", err)
	}

	// Remove client configurations
	if err := removeClientConfigurations(name, isAuxiliary); err != nil {
		logger.Warnf("Warning: Failed to remove client configurations: %v", err)
	} else {
		logger.Infof("Client configurations for %s removed", name)
	}

	// Delete the saved state last (skip for auxiliary workloads that don't have run configs)
	if !isAuxiliary {
		if err := state.DeleteSavedRunConfig(ctx, baseName); err != nil {
			logger.Warnf("Warning: Failed to delete saved state: %v", err)
		} else {
			logger.Infof("Saved state for %s removed", baseName)
		}
	} else {
		logger.Debugf("Skipping saved state deletion for auxiliary workload %s", name)
	}

	logger.Infof("Container %s removed", name)
}

// DeleteWorkloads deletes the specified workloads by name.
func (d *DefaultManager) DeleteWorkloads(_ context.Context, names []string) (*errgroup.Group, error) {
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
func (d *DefaultManager) RestartWorkloads(_ context.Context, names []string, foreground bool) (*errgroup.Group, error) {
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

// UpdateWorkload updates a workload by stopping, deleting, and recreating it
func (d *DefaultManager) UpdateWorkload(_ context.Context, workloadName string, newConfig *runner.RunConfig) (*errgroup.Group, error) { //nolint:lll
	// Validate workload name
	if err := types.ValidateWorkloadName(workloadName); err != nil {
		return nil, fmt.Errorf("invalid workload name '%s': %w", workloadName, err)
	}

	group := &errgroup.Group{}
	group.Go(func() error {
		return d.updateSingleWorkload(workloadName, newConfig)
	})
	return group, nil
}

// updateSingleWorkload handles the update logic for a single workload
func (d *DefaultManager) updateSingleWorkload(workloadName string, newConfig *runner.RunConfig) error {
	// Create a child context with a longer timeout
	childCtx, cancel := context.WithTimeout(context.Background(), AsyncOperationTimeout)
	defer cancel()

	logger.Infof("Starting update for workload %s", workloadName)

	// Stop the existing workload
	if err := d.stopSingleWorkload(workloadName); err != nil {
		return fmt.Errorf("failed to stop workload: %w", err)
	}
	logger.Infof("Successfully stopped workload %s", workloadName)

	// Delete the existing workload
	if err := d.deleteWorkload(workloadName); err != nil {
		return fmt.Errorf("failed to delete workload: %w", err)
	}
	logger.Infof("Successfully deleted workload %s", workloadName)

	// Save the new workload configuration state
	if err := newConfig.SaveState(childCtx); err != nil {
		logger.Errorf("Failed to save workload config: %v", err)
		return fmt.Errorf("failed to save workload config: %w", err)
	}

	// Step 3: Start the new workload
	// TODO: This currently just handles detached processes and wouldn't work for
	// foreground CLI executions. Should be refactored to support both modes.
	if err := d.RunWorkloadDetached(childCtx, newConfig); err != nil {
		return fmt.Errorf("failed to start new workload: %w", err)
	}

	logger.Infof("Successfully completed update for workload %s", workloadName)
	return nil
}

// restartSingleWorkload handles the restart logic for a single workload
func (d *DefaultManager) restartSingleWorkload(name string, foreground bool) error {
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
func (d *DefaultManager) restartRemoteWorkload(
	ctx context.Context,
	name string,
	runConfig *runner.RunConfig,
	foreground bool,
) error {
	// Get workload status using the status manager
	workload, err := d.statuses.GetWorkload(ctx, name)
	if err != nil && !errors.Is(err, rt.ErrWorkloadNotFound) {
		return err
	}

	// If workload is already running, check if the supervisor process is healthy
	if err == nil && workload.Status == rt.WorkloadStatusRunning {
		// Check if the supervisor process is actually alive
		supervisorAlive := d.isSupervisorProcessAlive(ctx, runConfig.BaseName)

		if supervisorAlive {
			// Workload is running and healthy - preserve old behavior (no-op)
			logger.Infof("Remote workload %s is already running", name)
			return nil
		}

		// Supervisor is dead/missing - we need to clean up and restart to fix the damaged state
		logger.Infof("Remote workload %s is running but supervisor is dead, cleaning up before restart", name)

		// Set status to stopping
		if err := d.statuses.SetWorkloadStatus(ctx, name, rt.WorkloadStatusStopping, ""); err != nil {
			logger.Debugf("Failed to set workload %s status to stopping: %v", name, err)
		}

		// Stop the supervisor process (proxy) if it exists (may already be dead)
		// This ensures we clean up any orphaned supervisor processes
		d.stopProxyIfNeeded(ctx, name, runConfig.BaseName)

		// Clean up client configurations
		if err := removeClientConfigurations(name, false); err != nil {
			logger.Warnf("Warning: Failed to remove client configurations: %v", err)
		}

		// Set status to stopped after cleanup is complete
		if err := d.statuses.SetWorkloadStatus(ctx, name, rt.WorkloadStatusStopped, ""); err != nil {
			logger.Debugf("Failed to set workload %s status to stopped: %v", name, err)
		}
	}

	// Load runner configuration from state
	mcpRunner, err := d.loadRunnerFromState(ctx, runConfig.BaseName)
	if err != nil {
		return fmt.Errorf("failed to load state for %s: %v", runConfig.BaseName, err)
	}

	// Set status to starting
	if err := d.statuses.SetWorkloadStatus(ctx, name, rt.WorkloadStatusStarting, ""); err != nil {
		logger.Warnf("Failed to set workload %s status to starting: %v", name, err)
	}

	logger.Infof("Loaded configuration from state for %s", runConfig.BaseName)

	// Start the remote workload using the loaded runner
	// Use background context to avoid timeout cancellation - same reasoning as container workloads
	return d.startWorkload(context.Background(), name, mcpRunner, foreground)
}

// restartContainerWorkload handles restarting a container-based workload
//
//nolint:gocyclo // Complexity is justified - handles multiple restart scenarios and edge cases
func (d *DefaultManager) restartContainerWorkload(ctx context.Context, name string, foreground bool) error {
	// Get container info to resolve partial names and extract proper workload name
	var containerName string
	var workloadName string

	container, err := d.runtime.GetWorkloadInfo(ctx, name)
	if err == nil {
		// If we found the container, use its actual container name for runtime operations
		containerName = container.Name
		// Extract the workload name (base name) from container labels for status operations
		workloadName = labels.GetContainerBaseName(container.Labels)
		if workloadName == "" {
			// Fallback to the provided name if base name is not available
			workloadName = name
		}
	} else {
		// If container not found, use the provided name as both container and workload name
		containerName = name
		workloadName = name
	}

	// Get workload status using the status manager
	workload, err := d.statuses.GetWorkload(ctx, name)
	if err != nil && !errors.Is(err, rt.ErrWorkloadNotFound) {
		return err
	}

	// Check if workload is running and healthy (including supervisor process)
	if err == nil && workload.Status == rt.WorkloadStatusRunning {
		// Check if the supervisor process is actually alive
		supervisorAlive := d.isSupervisorProcessAlive(ctx, workloadName)

		if supervisorAlive {
			// Workload is running and healthy - preserve old behavior (no-op)
			logger.Infof("Container %s is already running", containerName)
			return nil
		}

		// Supervisor is dead/missing - we need to clean up and restart to fix the damaged state
		logger.Infof("Container %s is running but supervisor is dead, cleaning up before restart", containerName)
	}

	// Check if we need to stop the workload before restarting
	// This happens when: 1) container is running, or 2) inconsistent state
	shouldStop := false
	if err == nil && workload.Status == rt.WorkloadStatusRunning {
		// Workload status shows running (and supervisor is dead, otherwise we would have returned above)
		shouldStop = true
	} else if container.IsRunning() {
		// Container is running but status is not running (inconsistent state)
		shouldStop = true
	}

	// If we need to stop, do it now (including cleanup of any remaining supervisor process)
	if shouldStop {
		logger.Infof("Stopping container %s before restart", containerName)

		// Set status to stopping
		if err := d.statuses.SetWorkloadStatus(ctx, workloadName, rt.WorkloadStatusStopping, ""); err != nil {
			logger.Debugf("Failed to set workload %s status to stopping: %v", workloadName, err)
		}

		// Stop the supervisor process (proxy) if it exists (may already be dead)
		// This ensures we clean up any orphaned supervisor processes
		if !labels.IsAuxiliaryWorkload(container.Labels) {
			d.stopProcess(ctx, workloadName)
		}

		// Now stop the container if it's running
		if container.IsRunning() {
			if err := d.runtime.StopWorkload(ctx, containerName); err != nil {
				if statusErr := d.statuses.SetWorkloadStatus(ctx, workloadName, rt.WorkloadStatusError, err.Error()); statusErr != nil {
					logger.Warnf("Failed to set workload %s status to error: %v", workloadName, statusErr)
				}
				return fmt.Errorf("failed to stop container %s: %v", containerName, err)
			}
			logger.Infof("Container %s stopped", containerName)
		}

		// Clean up client configurations
		if err := removeClientConfigurations(workloadName, labels.IsAuxiliaryWorkload(container.Labels)); err != nil {
			logger.Warnf("Warning: Failed to remove client configurations: %v", err)
		}

		// Set status to stopped after cleanup is complete
		if err := d.statuses.SetWorkloadStatus(ctx, workloadName, rt.WorkloadStatusStopped, ""); err != nil {
			logger.Debugf("Failed to set workload %s status to stopped: %v", workloadName, err)
		}
	}

	// Load runner configuration from state
	mcpRunner, err := d.loadRunnerFromState(ctx, workloadName)
	if err != nil {
		return fmt.Errorf("failed to load state for %s: %v", workloadName, err)
	}

	// Set workload status to starting - use the workload name for status operations
	if err := d.statuses.SetWorkloadStatus(ctx, workloadName, rt.WorkloadStatusStarting, ""); err != nil {
		logger.Warnf("Failed to set workload %s status to starting: %v", workloadName, err)
	}
	logger.Infof("Loaded configuration from state for %s", workloadName)

	// Start the workload with background context to avoid timeout cancellation
	// The ctx with AsyncOperationTimeout is only for the restart setup operations,
	// but the actual workload should run indefinitely with its own lifecycle management
	// Use workload name for user-facing operations
	return d.startWorkload(context.Background(), workloadName, mcpRunner, foreground)
}

// startWorkload starts the workload in either foreground or background mode
func (d *DefaultManager) startWorkload(ctx context.Context, name string, mcpRunner *runner.Runner, foreground bool) error {
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
func removeClientConfigurations(containerName string, isAuxiliary bool) error {
	// Get the workload's group by loading its run config
	runConfig, err := runner.LoadState(context.Background(), containerName)
	var group string
	if err != nil {
		// Only warn for non-auxiliary workloads since auxiliary workloads don't have run configs
		if !isAuxiliary {
			logger.Warnf("Warning: Failed to load run config for %s, will use backward compatible behavior: %v", containerName, err)
		}
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
func (d *DefaultManager) loadRunnerFromState(ctx context.Context, baseName string) (*runner.Runner, error) {
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

func (d *DefaultManager) needSecretsPassword(secretOptions []string) bool {
	// If the user did not ask for any secrets, then don't attempt to instantiate
	// the secrets manager.
	if len(secretOptions) == 0 {
		return false
	}
	// Ignore err - if the flag is not set, it's not needed.
	providerType, _ := d.configProvider.GetConfig().Secrets.GetProviderType()
	return providerType == secrets.EncryptedType
}

// cleanupTempPermissionProfile cleans up temporary permission profile files for a given base name
func (*DefaultManager) cleanupTempPermissionProfile(ctx context.Context, baseName string) error {
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
func (d *DefaultManager) stopSingleContainerWorkload(ctx context.Context, workload *rt.ContainerInfo) error {
	childCtx, cancel := context.WithTimeout(context.Background(), AsyncOperationTimeout)
	defer cancel()

	name := labels.GetContainerBaseName(workload.Labels)
	// Stop the proxy process (skip for auxiliary workloads like inspector)
	if labels.IsAuxiliaryWorkload(workload.Labels) {
		logger.Debugf("Skipping proxy stop for auxiliary workload %s", name)
	} else {
		d.stopProcess(ctx, name)
	}

	// TODO: refactor the StopProcess function to stop dealing explicitly with PID files.
	// Note that this is not a blocker for k8s since this code path is not called there.
	if err := d.statuses.ResetWorkloadPID(ctx, name); err != nil {
		logger.Warnf("Warning: Failed to reset workload %s PID: %v", name, err)
	}

	logger.Infof("Stopping containers for %s...", name)
	// Stop the container
	if err := d.runtime.StopWorkload(childCtx, workload.Name); err != nil {
		if statusErr := d.statuses.SetWorkloadStatus(childCtx, name, rt.WorkloadStatusError, err.Error()); statusErr != nil {
			logger.Warnf("Failed to set workload %s status to error: %v", name, statusErr)
		}
		return fmt.Errorf("failed to stop container: %w", err)
	}

	if err := removeClientConfigurations(name, labels.IsAuxiliaryWorkload(workload.Labels)); err != nil {
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
func (*DefaultManager) MoveToGroup(ctx context.Context, workloadNames []string, groupFrom string, groupTo string) error {
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
func (d *DefaultManager) ListWorkloadsInGroup(ctx context.Context, groupName string) ([]string, error) {
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
func (d *DefaultManager) getRemoteWorkloadsFromState(
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

		// Generate the local proxy URL (not the remote server URL)
		proxyURL := ""
		if runConfig.Port > 0 {
			proxyURL = transport.GenerateMCPServerURL(
				transportType.String(),
				transport.LocalhostIPv4,
				runConfig.Port,
				name,
				runConfig.RemoteURL, // Pass remote URL to preserve path
			)
		}

		// Calculate the effective proxy mode that clients should use
		effectiveProxyMode := types.GetEffectiveProxyMode(transportType, string(runConfig.ProxyMode))

		// Create a workload from the run configuration
		workload := core.Workload{
			Name:          name,
			Package:       "remote",
			Status:        workloadStatus.Status,
			URL:           proxyURL,
			Port:          runConfig.Port,
			TransportType: transportType,
			ProxyMode:     effectiveProxyMode,
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
