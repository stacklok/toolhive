package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/stacklok/vibetool/pkg/container"
	rt "github.com/stacklok/vibetool/pkg/container/runtime"
	"github.com/stacklok/vibetool/pkg/labels"
	"github.com/stacklok/vibetool/pkg/transport"
)

var (
	restartForeground bool
)

var restartCmd = &cobra.Command{
	Use:   "restart [container-name]",
	Short: "Restart an MCP server",
	Long:  `Restart an MCP server managed by Vibe Tool.`,
	Args:  cobra.ExactArgs(1),
	RunE:  restartCmdFunc,
}

func init() {
	restartCmd.Flags().BoolVar(&restartForeground, "foreground", false, "Run in foreground mode (default: false)")
}

// getPortConfiguration gets the port configuration based on transport type and container info
func getPortConfiguration(transportType string, containerInfo rt.ContainerInfo) (targetPort int) {
	return transport.GetPortConfiguration(transport.TransportType(transportType), containerInfo)
}

func restartCmdFunc(cmd *cobra.Command, args []string) error {
	// Get container name
	containerName := args[0]

	// Create context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Get debug mode flag
	debugMode, _ := cmd.Flags().GetBool("debug")

	// Create container runtime
	runtime, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}

	// Check if we're running in detached mode
	if os.Getenv("VIBETOOL_DETACHED") != "1" && !restartForeground {
		return detachRestartProcess(cmd, args[0])
	}

	// Find the container with the given name
	containerID, _, err := findContainerAndBaseName(ctx, runtime, containerName)
	if err != nil {
		return err
	}

	// Get container info to preserve configuration
	containerInfo, err := runtime.GetContainerInfo(ctx, containerID)
	if err != nil {
		return fmt.Errorf("failed to get container info: %v", err)
	}

	// Extract transport type from labels
	transportType := labels.GetTransportType(containerInfo.Labels)
	if transportType == "" {
		return fmt.Errorf("failed to determine transport type from container labels")
	}

	// Get port configuration based on transport type
	targetPort := getPortConfiguration(transportType, containerInfo)

	// get the host port from the container labels
	proxyPort, err := labels.GetPort(containerInfo.Labels)
	if err != nil {
		return fmt.Errorf("failed to get port from container labels: %v", err)
	}

	// Create transport configuration
	transportConfig := transport.Config{
		Type:       transport.TransportType(transportType),
		Runtime:    runtime,
		Debug:      debugMode,
		Port:       proxyPort,
		TargetPort: targetPort,
	}

	// Create transport handler
	transportHandler, err := transport.NewFactory().Create(transportConfig)
	if err != nil {
		return fmt.Errorf("failed to create transport: %v", err)
	}

	// Set up the transport
	fmt.Printf("Setting up %s transport...\n", transportType)
	if err := transportHandler.Setup(
		ctx,
		runtime,
		containerName,
		containerInfo,
		nil, // Use default command from image
		nil, // Use default permission profile
	); err != nil {
		return fmt.Errorf("failed to set up transport: %v", err)
	}

	// Start the transport (which also starts the container and proxy)
	fmt.Printf("Starting %s transport...\n", transportType)
	if err := transportHandler.Start(ctx); err != nil {
		return fmt.Errorf("failed to start transport: %v", err)
	}

	fmt.Printf("Container %s restarted successfully\n", containerName)
	return nil
}

// detachRestartProcess starts a new detached process with the restart command
func detachRestartProcess(_ *cobra.Command, containerName string) error {
	// Get the current executable path
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %v", err)
	}

	// Create context to find the container's base name
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create container runtime
	runtime, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}

	// Find the container to get its base name for the log file
	_, containerBaseName, err := findContainerAndBaseName(ctx, runtime, containerName)
	if err != nil {
		return err
	}

	// Create a log file for the detached process using the same pattern as run command
	logFilePath := fmt.Sprintf("/tmp/vibetool-%s.log", containerBaseName)
	// #nosec G304 - This is safe as containerBaseName comes from container labels
	logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		fmt.Printf("Warning: Failed to create log file: %v\n", err)
	} else {
		defer logFile.Close()
		fmt.Printf("Logging to: %s\n", logFilePath)
	}

	// Prepare the command arguments for the detached process
	detachedArgs := []string{"restart", "--foreground", containerName}

	// Create a new command
	// #nosec G204 - This is safe as execPath is the path to the current binary
	detachedCmd := exec.Command(execPath, detachedArgs...)

	// Set environment variables for the detached process
	detachedCmd.Env = append(os.Environ(), "VIBETOOL_DETACHED=1")

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
	detachedCmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true, // Create a new session
	}

	// Start the detached process
	if err := detachedCmd.Start(); err != nil {
		return fmt.Errorf("failed to start detached process: %v", err)
	}

	fmt.Printf("Restarting MCP server %s in the background (PID: %d)\n", containerName, detachedCmd.Process.Pid)
	return nil
}

// findContainerAndBaseName finds a container by name and returns its ID and base name
func findContainerAndBaseName(ctx context.Context, runtime rt.Runtime, containerName string) (string, string, error) {
	containers, err := runtime.ListContainers(ctx)
	if err != nil {
		return "", "", fmt.Errorf("failed to list containers: %v", err)
	}

	for _, c := range containers {
		// Check if the container is managed by Vibe Tool
		if !labels.IsVibeToolContainer(c.Labels) {
			continue
		}

		// Check if the container name matches
		name := labels.GetContainerName(c.Labels)
		if name == "" {
			name = c.Name // Fallback to container name
		}

		// Check if the name matches (exact match or prefix match)
		if name == containerName || strings.HasPrefix(c.ID, containerName) {
			baseName := labels.GetContainerBaseName(c.Labels)
			return c.ID, baseName, nil
		}
	}

	return "", "", fmt.Errorf("container %s not found", containerName)
}

// RestartCmd creates a new cobra command for restarting containers
func RestartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart a container",
		Long:  "Restart a container managed by vibetool",
		RunE:  restartCmdFunc,
	}

	return cmd
}
