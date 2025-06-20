// Package workloads contains high-level logic for managing the lifecycle of
// ToolHive-managed containers.
package workloads

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/adrg/xdg"
	"golang.org/x/sync/errgroup"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/config"
	ct "github.com/stacklok/toolhive/pkg/container"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/process"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/transport/proxy"
)

// Manager is responsible for managing the state of ToolHive-managed containers.
type Manager interface {
	// GetWorkload returns information about the named container.
	GetWorkload(ctx context.Context, name string) (Workload, error)
	// ListWorkloads lists all ToolHive-managed containers.
	ListWorkloads(ctx context.Context, listAll bool) ([]Workload, error)
	// DeleteWorkload deletes a container and all associated processes/containers.
	// It is implemented as an asynchronous operation which returns an errgroup.Group
	DeleteWorkload(ctx context.Context, name string) (*errgroup.Group, error)
	// StopWorkload stops the named workload.
	// It is implemented as an asynchronous operation which returns an errgroup.Group
	StopWorkload(ctx context.Context, name string) (*errgroup.Group, error)
	// StopAllWorkloads stops all running workloads.
	// It is implemented as an asynchronous operation which returns an errgroup.Group
	StopAllWorkloads(ctx context.Context) (*errgroup.Group, error)
	// RunWorkload runs a container in the foreground.
	RunWorkload(ctx context.Context, runConfig *runner.RunConfig) error
	// RunWorkloadDetached runs a container in the background.
	RunWorkloadDetached(runConfig *runner.RunConfig) error
	// RestartWorkload restarts a previously stopped container.
	// It is implemented as an asynchronous operation which returns an errgroup.Group
	RestartWorkload(ctx context.Context, name string) (*errgroup.Group, error)
}

type defaultManager struct {
	runtime rt.Runtime
}

// ErrContainerNotFound is returned when a container cannot be found by name.
var (
	ErrContainerNotFound   = fmt.Errorf("container not found")
	ErrContainerNotRunning = fmt.Errorf("container not running")
)

// NewManager creates a new container manager instance.
func NewManager(ctx context.Context) (Manager, error) {
	runtime, err := ct.NewFactory().Create(ctx)
	if err != nil {
		return nil, err
	}

	return &defaultManager{
		runtime: runtime,
	}, nil
}

func (d *defaultManager) GetWorkload(ctx context.Context, name string) (Workload, error) {
	container, err := d.findContainerByName(ctx, name)
	if err != nil {
		// Note that `findContainerByName` already wraps the error with a more specific message.
		return Workload{}, err
	}

	return WorkloadFromContainerInfo(container)
}

func (d *defaultManager) ListWorkloads(ctx context.Context, listAll bool) ([]Workload, error) {
	// List containers
	containers, err := d.runtime.ListWorkloads(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %v", err)
	}

	// Filter containers to only show those managed by ToolHive
	var workloads []Workload
	for _, c := range containers {
		// If the caller did not set `listAll` to true, only include running containers.
		if labels.IsToolHiveContainer(c.Labels) && (isContainerRunning(&c) || listAll) {
			workload, err := WorkloadFromContainerInfo(&c)
			if err != nil {
				return nil, err
			}
			workloads = append(workloads, workload)
		}
	}

	return workloads, nil
}

func (d *defaultManager) DeleteWorkload(ctx context.Context, name string) (*errgroup.Group, error) {
	// We need several fields from the container struct for deletion.
	container, err := d.findContainerByName(ctx, name)
	if err != nil {
		return nil, err
	}

	containerID := container.ID
	containerLabels := container.Labels
	baseName := labels.GetContainerBaseName(containerLabels)

	// Create second errorgroup for deletion.
	deleteGroup := &errgroup.Group{}
	deleteGroup.Go(func() error {
		// Remove the container
		logger.Infof("Removing container %s...", name)
		if err := d.runtime.RemoveWorkload(ctx, containerID); err != nil {
			return fmt.Errorf("failed to remove container: %v", err)
		}

		// Get the base name from the container labels
		if baseName != "" {
			// Clean up temporary permission profile before deleting saved state
			if err := d.cleanupTempPermissionProfile(ctx, baseName); err != nil {
				logger.Warnf("Warning: Failed to cleanup temporary permission profile: %v", err)
			}

			// Delete the saved state if it exists
			if err := runner.DeleteSavedConfig(ctx, baseName); err != nil {
				logger.Warnf("Warning: Failed to delete saved state: %v", err)
			} else {
				logger.Infof("Saved state for %s removed", baseName)
			}

			logger.Infof("Container %s removed", name)

			if shouldRemoveClientConfig() {
				if err := removeClientConfigurations(name); err != nil {
					logger.Warnf("Warning: Failed to remove client configurations: %v", err)
				} else {
					logger.Infof("Client configurations for %s removed", name)
				}
			}
		}

		return nil
	})

	return deleteGroup, nil
}

func (d *defaultManager) StopWorkload(ctx context.Context, name string) (*errgroup.Group, error) {
	// Find the container
	container, err := d.findContainerByName(ctx, name)
	if err != nil {
		return nil, err
	}

	running := isContainerRunning(container)
	if !running {
		return nil, fmt.Errorf("%w: %s", ErrContainerNotRunning, name)
	}

	return d.stopWorkloads(ctx, []*rt.ContainerInfo{container}), nil
}

func (d *defaultManager) StopAllWorkloads(ctx context.Context) (*errgroup.Group, error) {
	// Get list of all running workloads.
	containers, err := d.runtime.ListWorkloads(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %v", err)
	}

	// Duplicates the logic of GetWorkloads, but is simple enough that it's not
	// worth duplicating.
	var containersToStop []*rt.ContainerInfo
	for _, c := range containers {
		// If the caller did not set `listAll` to true, only include running containers.
		if labels.IsToolHiveContainer(c.Labels) && isContainerRunning(&c) {
			containersToStop = append(containersToStop, &c)
		}
	}

	return d.stopWorkloads(ctx, containersToStop), nil
}

func (*defaultManager) RunWorkload(ctx context.Context, runConfig *runner.RunConfig) error {
	mcpRunner := runner.NewRunner(runConfig)
	return mcpRunner.Run(ctx)
}

//nolint:gocyclo // This function is complex but manageable
func (*defaultManager) RunWorkloadDetached(runConfig *runner.RunConfig) error {
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

	// Prepare the command arguments for the detached process
	// We'll run the same command but with the --foreground flag
	detachedArgs := []string{"run", "--foreground"}

	// Add all the original flags
	if runConfig.Transport != "stdio" {
		detachedArgs = append(detachedArgs, "--transport", string(runConfig.Transport))
	}

	if runConfig.Debug {
		detachedArgs = append(detachedArgs, "--debug")
	}

	if runConfig.IsolateNetwork {
		detachedArgs = append(detachedArgs, "--isolate-network")
	}

	// Use Name if available
	if runConfig.Name != "" {
		detachedArgs = append(detachedArgs, "--name", runConfig.Name)
	}

	// Use ContainerName if available
	if runConfig.ContainerName != "" {
		detachedArgs = append(detachedArgs, "--name", runConfig.ContainerName)
	}

	if runConfig.Host != "" {
		detachedArgs = append(detachedArgs, "--host", runConfig.Host)
	}

	if runConfig.Port != 0 {
		detachedArgs = append(detachedArgs, "--port", fmt.Sprintf("%d", runConfig.Port))
	}

	if runConfig.TargetPort != 0 {
		detachedArgs = append(detachedArgs, "--target-port", fmt.Sprintf("%d", runConfig.TargetPort))
	}

	// Add target host if it's not the default
	if runConfig.TargetHost != "localhost" {
		detachedArgs = append(detachedArgs, "--target-host", runConfig.TargetHost)
	}

	// Pass the permission profile to the detached process
	if runConfig.PermissionProfile != nil {
		// We need to create a temporary file for the permission profile
		permProfilePath, err := runner.CreatePermissionProfileFile(runConfig.BaseName, runConfig.PermissionProfile)
		if err != nil {
			logger.Warnf("Warning: Failed to create permission profile file: %v", err)
		} else {
			detachedArgs = append(detachedArgs, "--permission-profile", permProfilePath)
		}
	}

	// Add environment variables
	for key, value := range runConfig.EnvVars {
		detachedArgs = append(detachedArgs, "--env", fmt.Sprintf("%s=%s", key, value))
	}

	// Add volume mounts if they were provided
	for _, volume := range runConfig.Volumes {
		detachedArgs = append(detachedArgs, "--volume", volume)
	}

	// Add secrets if they were provided
	for _, secret := range runConfig.Secrets {
		detachedArgs = append(detachedArgs, "--secret", secret)
	}

	// Add OIDC flags if they were provided
	if runConfig.OIDCConfig != nil {
		if runConfig.OIDCConfig.Issuer != "" {
			detachedArgs = append(detachedArgs, "--oidc-issuer", runConfig.OIDCConfig.Issuer)
		}
		if runConfig.OIDCConfig.Audience != "" {
			detachedArgs = append(detachedArgs, "--oidc-audience", runConfig.OIDCConfig.Audience)
		}
		if runConfig.OIDCConfig.JWKSURL != "" {
			detachedArgs = append(detachedArgs, "--oidc-jwks-url", runConfig.OIDCConfig.JWKSURL)
		}
		if runConfig.OIDCConfig.ClientID != "" {
			detachedArgs = append(detachedArgs, "--oidc-client-id", runConfig.OIDCConfig.ClientID)
		}
	}

	// Add authz config if it was provided
	if runConfig.AuthzConfigPath != "" {
		detachedArgs = append(detachedArgs, "--authz-config", runConfig.AuthzConfigPath)
	}

	// Add audit config if it was provided
	if runConfig.AuditConfigPath != "" {
		detachedArgs = append(detachedArgs, "--audit-config", runConfig.AuditConfigPath)
	}

	// Add telemetry flags if telemetry config is provided
	if runConfig.TelemetryConfig != nil {
		if runConfig.TelemetryConfig.Endpoint != "" {
			detachedArgs = append(detachedArgs, "--otel-endpoint", runConfig.TelemetryConfig.Endpoint)
		}
		if runConfig.TelemetryConfig.ServiceName != "" {
			detachedArgs = append(detachedArgs, "--otel-service-name", runConfig.TelemetryConfig.ServiceName)
		}
		if runConfig.TelemetryConfig.SamplingRate != 0.1 { // Only add if not default
			detachedArgs = append(detachedArgs, "--otel-sampling-rate", fmt.Sprintf("%f", runConfig.TelemetryConfig.SamplingRate))
		}
		for key, value := range runConfig.TelemetryConfig.Headers {
			detachedArgs = append(detachedArgs, "--otel-headers", fmt.Sprintf("%s=%s", key, value))
		}
		if runConfig.TelemetryConfig.Insecure {
			detachedArgs = append(detachedArgs, "--otel-insecure")
		}
		if runConfig.TelemetryConfig.EnablePrometheusMetricsPath {
			detachedArgs = append(detachedArgs, "--otel-enable-prometheus-metrics-path")
		}
	}

	// Add enable audit flag if audit config is set but no config path is provided
	if runConfig.AuditConfig != nil && runConfig.AuditConfigPath == "" {
		detachedArgs = append(detachedArgs, "--enable-audit")
	}

	// Add the image and any arguments
	detachedArgs = append(detachedArgs, runConfig.Image)
	if len(runConfig.CmdArgs) > 0 {
		detachedArgs = append(detachedArgs, "--")
		detachedArgs = append(detachedArgs, runConfig.CmdArgs...)
	}

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

	// Start the detached process
	if err := detachedCmd.Start(); err != nil {
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

func (d *defaultManager) RestartWorkload(ctx context.Context, name string) (*errgroup.Group, error) {
	var containerBaseName string
	var running bool
	// Try to find the container.
	container, err := d.findContainerByName(ctx, name)
	if err != nil {
		logger.Warnf("Warning: Failed to find container: %v", err)
		logger.Warnf("Trying to find state with name %s directly...", name)

		// Try to use the provided name as the base name
		containerBaseName = name
		running = false
	} else {
		// Container found, check if it's running and get the base name,
		running = isContainerRunning(container)
		containerBaseName = labels.GetContainerBaseName(container.Labels)
	}

	// Check if the proxy process is running
	proxyRunning := proxy.IsRunning(containerBaseName)

	if running && proxyRunning {
		logger.Infof("Container %s and proxy are already running", name)
		// Return empty error group so that client does not need to check for nil.
		return &errgroup.Group{}, nil
	}

	// Load the configuration from the state store
	// This is done synchronously since it is relatively inexpensive operation
	// and it allows for better error handling.
	mcpRunner, err := d.loadRunnerFromState(ctx, containerBaseName)
	if err != nil {
		return nil, fmt.Errorf("failed to load state for %s: %v", containerBaseName, err)
	}
	logger.Infof("Loaded configuration from state for %s", containerBaseName)

	// Run the tooling server inside a detached process.
	// TODO: This will need to be changed when RunWorkloadDetached is converted
	// to be async.
	logger.Infof("Starting tooling server %s...", name)
	runGroup := &errgroup.Group{}
	runGroup.Go(func() error {
		containerID := container.ID
		// If the container is running but the proxy is not, stop the container first
		if container.ID != "" && running { // && !proxyRunning was previously here but is implied by previous if statement.
			logger.Infof("Container %s is running but proxy is not. Stopping container...", name)
			// n.b. - we do not reuse the `StopWorkload` method here because it
			// does some extra things which are not appropriate for resuming a workload.
			if err = d.runtime.StopWorkload(ctx, containerID); err != nil {
				return fmt.Errorf("failed to stop container %s: %v", name, err)
			}
			logger.Infof("Container %s stopped", name)
		}

		return d.RunWorkloadDetached(mcpRunner.Config)
	})

	return runGroup, nil
}

func (d *defaultManager) findContainerByName(ctx context.Context, name string) (*rt.ContainerInfo, error) {
	// List containers to find the one with the given name
	containers, err := d.runtime.ListWorkloads(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %v", err)
	}

	// Find the container with the given name
	for _, c := range containers {
		// Check if the container is managed by ToolHive
		if !labels.IsToolHiveContainer(c.Labels) {
			continue
		}

		// Check if the container name matches
		containerName := labels.GetContainerName(c.Labels)
		if containerName == "" {
			name = c.Name // Fallback to container name
		}

		// Check if the name matches (exact match or prefix match)
		if containerName == name || c.ID == name {
			return &c, nil
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrContainerNotFound, name)
}

func shouldRemoveClientConfig() bool {
	c := config.GetConfig()
	return len(c.Clients.RegisteredClients) > 0 || c.Clients.AutoDiscovery
}

// TODO: Move to dedicated config management interface.
// updateClientConfigurations updates client configuration files with the MCP server URL
func removeClientConfigurations(containerName string) error {
	// Find client configuration files
	configs, err := client.FindClientConfigs()
	if err != nil {
		return fmt.Errorf("failed to find client configurations: %w", err)
	}

	if len(configs) == 0 {
		logger.Info("No client configuration files found")
		return nil
	}

	for _, c := range configs {
		logger.Infof("Removing MCP server from client configuration: %s", c.Path)

		if err := c.ConfigUpdater.Remove(containerName); err != nil {
			logger.Warnf("Warning: Failed to remove MCP server from client configuration %s: %v", c.Path, err)
			continue
		}

		logger.Infof("Successfully removed MCP server from client configuration: %s", c.Path)
	}

	return nil
}

func isContainerRunning(container *rt.ContainerInfo) bool {
	return container.State == "running"
}

// loadRunnerFromState attempts to load a Runner from the state store
func (d *defaultManager) loadRunnerFromState(ctx context.Context, baseName string) (*runner.Runner, error) {
	// Load the runner from the state store
	r, err := runner.LoadState(ctx, baseName)
	if err != nil {
		return nil, err
	}

	// Update the runtime in the loaded configuration
	r.Config.Runtime = d.runtime

	return r, nil
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
	r, err := runner.LoadState(ctx, baseName)
	if err != nil {
		// If we can't load the state, there's nothing to clean up
		logger.Debugf("Could not load state for %s, skipping permission profile cleanup: %v", baseName, err)
		return nil
	}

	// Clean up the temporary permission profile if it exists
	if r.Config.PermissionProfileNameOrPath != "" {
		if err := runner.CleanupTempPermissionProfile(r.Config.PermissionProfileNameOrPath); err != nil {
			return fmt.Errorf("failed to cleanup temporary permission profile: %v", err)
		}
	}

	return nil
}

// stopWorkloads stops the named workloads concurrently.
// It assumes that the workloads exist in the running state.
func (d *defaultManager) stopWorkloads(ctx context.Context, workloads []*rt.ContainerInfo) *errgroup.Group {
	group := errgroup.Group{}
	for _, workload := range workloads {
		group.Go(func() error {
			name := labels.GetContainerBaseName(workload.Labels)
			// Stop the proxy process
			proxy.StopProcess(name)

			logger.Infof("Stopping containers for %s...", name)
			// Stop the container
			if err := d.runtime.StopWorkload(ctx, workload.ID); err != nil {
				return fmt.Errorf("failed to stop container: %w", err)
			}

			if shouldRemoveClientConfig() {
				if err := removeClientConfigurations(name); err != nil {
					logger.Warnf("Warning: Failed to remove client configurations: %v", err)
				} else {
					logger.Infof("Client configurations for %s removed", name)
				}
			}

			logger.Infof("Successfully stopped %s...", name)
			return nil
		})
	}

	return &group
}
