// Package lifecycle contains high-level logic for managing the lifecycle of
// ToolHive-managed containers.
package lifecycle

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/adrg/xdg"

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
	// GetContainer returns information about the named container.
	GetContainer(ctx context.Context, name string) (*rt.ContainerInfo, error)
	// ListContainers lists all ToolHive-managed containers.
	ListContainers(ctx context.Context, listAll bool) ([]rt.ContainerInfo, error)
	// DeleteContainer deletes a container and its associated proxy process.
	DeleteContainer(ctx context.Context, name string, forceDelete bool) error
	// StopContainer stops a container and its associated proxy process.
	StopContainer(ctx context.Context, name string) error
	// RunContainer runs a container in the foreground.
	RunContainer(ctx context.Context, runConfig *runner.RunConfig) error
	// RunContainerDetached runs a container in the background.
	RunContainerDetached(runConfig *runner.RunConfig) error
	// RestartContainer restarts a previously stopped container.
	RestartContainer(ctx context.Context, name string) error
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

func (d *defaultManager) GetContainer(ctx context.Context, name string) (*rt.ContainerInfo, error) {
	return d.findContainerByName(ctx, name)
}

func (d *defaultManager) ListContainers(ctx context.Context, listAll bool) ([]rt.ContainerInfo, error) {
	// List containers
	containers, err := d.runtime.ListWorkloads(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %v", err)
	}

	// Filter containers to only show those managed by ToolHive
	var toolHiveContainers []rt.ContainerInfo
	for _, c := range containers {
		// If the caller did not set `listAll` to true, only include running containers.
		if labels.IsToolHiveContainer(c.Labels) && (isContainerRunning(&c) || listAll) {
			toolHiveContainers = append(toolHiveContainers, c)
		}
	}

	return toolHiveContainers, nil
}

func (d *defaultManager) DeleteContainer(ctx context.Context, name string, forceDelete bool) error {
	// We need several fields from the container struct for deletion.
	container, err := d.findContainerByName(ctx, name)
	if err != nil {
		return err
	}

	containerID := container.ID
	isRunning := isContainerRunning(container)
	containerLabels := container.Labels

	// Check if the container is running
	if isRunning {
		if !forceDelete {
			return fmt.Errorf("container %s is running. Stop the container or use -f to force remove", name)
		}
		// Stop the container if it's running
		if err := d.stopContainer(ctx, containerID, name); err != nil {
			logger.Warnf("Warning: Failed to stop container: %v", err)
		}
	}

	// Remove the container
	logger.Infof("Removing container %s...", name)
	if err := d.runtime.RemoveWorkload(ctx, containerID); err != nil {
		return fmt.Errorf("failed to remove container: %v", err)
	}

	// Get the base name from the container labels
	baseName := labels.GetContainerBaseName(containerLabels)
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
	}

	logger.Infof("Container %s removed", name)

	if shouldRemoveClientConfig() {
		if err := removeClientConfigurations(name); err != nil {
			logger.Warnf("Warning: Failed to remove client configurations: %v", err)
		} else {
			logger.Infof("Client configurations for %s removed", name)
		}
	}

	return nil
}

func (d *defaultManager) StopContainer(ctx context.Context, name string) error {
	// Find the container ID
	containerID, err := d.findContainerID(ctx, name)
	if err != nil {
		return err
	}

	// Check if the container is running
	running, err := d.runtime.IsWorkloadRunning(ctx, containerID)
	if err != nil {
		return fmt.Errorf("failed to check if container is running: %v", err)
	}

	if !running {
		return fmt.Errorf("%w: %s", ErrContainerNotRunning, name)
	}

	// Get the base container name
	containerBaseName, _ := d.getContainerBaseName(ctx, containerID)

	// Stop the proxy process
	proxy.StopProcess(containerBaseName)

	// Stop the container
	return d.stopContainer(ctx, containerID, name)
}

func (*defaultManager) RunContainer(ctx context.Context, runConfig *runner.RunConfig) error {
	mcpRunner := runner.NewRunner(runConfig)
	return mcpRunner.Run(ctx)
}

//nolint:gocyclo // This function is complex but manageable
func (*defaultManager) RunContainerDetached(runConfig *runner.RunConfig) error {
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
		permProfilePath, err := CreatePermissionProfileFile(runConfig.BaseName, runConfig.PermissionProfile)
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
		password, err := secrets.GetSecretsPassword()
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

func (d *defaultManager) RestartContainer(ctx context.Context, name string) error {
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
		return nil
	}

	containerID := container.ID
	// If the container is running but the proxy is not, stop the container first
	if container.ID != "" && running { // && !proxyRunning was previously here but is implied by previous if statement.
		logger.Infof("Container %s is running but proxy is not. Stopping container...", name)
		if err := d.runtime.StopWorkload(ctx, containerID); err != nil {
			return fmt.Errorf("failed to stop container: %v", err)
		}
		logger.Infof("Container %s stopped", name)
	}

	// Load the configuration from the state store
	mcpRunner, err := d.loadRunnerFromState(ctx, containerBaseName)
	if err != nil {
		return fmt.Errorf("failed to load state for %s: %v", containerBaseName, err)
	}

	logger.Infof("Loaded configuration from state for %s", containerBaseName)

	// Run the tooling server inside a detached process.
	logger.Infof("Starting tooling server %s...", name)
	return d.RunContainerDetached(mcpRunner.Config)
}

func (d *defaultManager) findContainerID(ctx context.Context, name string) (string, error) {
	c, err := d.findContainerByName(ctx, name)
	if err != nil {
		return "", err
	}
	return c.ID, nil
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

// getContainerBaseName gets the base container name from the container labels
func (d *defaultManager) getContainerBaseName(ctx context.Context, containerID string) (string, error) {
	containers, err := d.runtime.ListWorkloads(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list containers: %v", err)
	}

	for _, c := range containers {
		if c.ID == containerID {
			return labels.GetContainerBaseName(c.Labels), nil
		}
	}

	return "", fmt.Errorf("container %s not found", containerID)
}

// stopContainer stops the container
func (d *defaultManager) stopContainer(ctx context.Context, containerID, containerName string) error {
	logger.Infof("Stopping container %s...", containerName)
	if err := d.runtime.StopWorkload(ctx, containerID); err != nil {
		return fmt.Errorf("failed to stop container: %w", err)
	}

	logger.Infof("Container %s stopped", containerName)
	return nil
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
		if err := CleanupTempPermissionProfile(r.Config.PermissionProfileNameOrPath); err != nil {
			return fmt.Errorf("failed to cleanup temporary permission profile: %v", err)
		}
	}

	return nil
}
