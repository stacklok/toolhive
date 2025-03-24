package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/stacklok/vibetool/pkg/client"
	"github.com/stacklok/vibetool/pkg/container"
	"github.com/stacklok/vibetool/pkg/environment"
	"github.com/stacklok/vibetool/pkg/labels"
	"github.com/stacklok/vibetool/pkg/networking"
	"github.com/stacklok/vibetool/pkg/process"
	"github.com/stacklok/vibetool/pkg/transport"
)

var runCmd = &cobra.Command{
	Use:   "run [flags] IMAGE [-- ARGS...]",
	Short: "Run an MCP server",
	Long: `Run an MCP server in a container with the specified image and arguments.
The container will be started with minimal permissions and the specified transport mode.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runCmdFunc,
}

var (
	runTransport         string
	runName              string
	runPort              int
	runTargetPort        int
	runPermissionProfile string
	runEnv               []string
	runNoClientConfig    bool
	runForeground        bool
)

func init() {
	runCmd.Flags().StringVar(&runTransport, "transport", "stdio", "Transport mode (sse or stdio)")
	runCmd.Flags().StringVar(&runName, "name", "", "Name of the MCP server (auto-generated from image if not provided)")
	runCmd.Flags().IntVar(&runPort, "port", 0, "Port for the HTTP proxy to listen on (host port)")
	runCmd.Flags().IntVar(&runTargetPort, "target-port", 0, "Port for the container to expose (only applicable to SSE transport)")
	runCmd.Flags().StringVar(
		&runPermissionProfile,
		"permission-profile",
		"stdio",
		"Permission profile to use (stdio, network, or path to JSON file)",
	)
	runCmd.Flags().StringArrayVarP(
		&runEnv,
		"env",
		"e",
		[]string{},
		"Environment variables to pass to the MCP server (format: KEY=VALUE)",
	)
	runCmd.Flags().BoolVar(
		&runNoClientConfig,
		"no-client-config",
		false,
		"Do not update client configuration files with the MCP server URL",
	)
	runCmd.Flags().BoolVarP(&runForeground, "foreground", "f", false, "Run in foreground mode (block until container exits)")
}
func runCmdFunc(cmd *cobra.Command, args []string) error {
	// Get the image and command arguments
	image := args[0]
	cmdArgs := args[1:]

	// Generate a container name if not provided
	containerName, baseName := container.GetOrGenerateContainerName(runName, image)

	// If not running in foreground mode, start a new detached process and exit
	if !runForeground {
		return detachProcess(image, baseName, containerName, cmdArgs)
	}

	// Parse transport mode
	transportType, err := transport.ParseTransportType(runTransport)
	if err != nil {
		return fmt.Errorf("invalid transport mode: %s. Valid modes are: sse, stdio", runTransport)
	}

	// Select a port for the HTTP proxy (host port)
	port, err := networking.FindOrUsePort(runPort)
	if err != nil {
		return err
	}
	fmt.Printf("Using host port: %d\n", port)

	// Select a target port for the container
	targetPort, err := getTargetPort(transportType, runTargetPort)
	if err != nil {
		return err
	}

	// We don't need to get permission configuration here anymore as it's handled by the transport

	// Parse environment variables
	envVars, err := environment.ParseEnvironmentVariables(runEnv)
	if err != nil {
		return fmt.Errorf("failed to parse environment variables: %v", err)
	}

	// Set transport-specific environment variables
	// Always pass the targetPort, which will be used for the container's environment variables
	environment.SetTransportEnvironmentVariables(envVars, string(transportType), targetPort)

	// Create container labels
	containerLabels := make(map[string]string)
	labels.AddStandardLabels(containerLabels, containerName, baseName, string(transportType), port)

	// Create context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create container runtime
	runtime, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}

	// Get debug mode flag
	debugMode, _ := cmd.Flags().GetBool("debug")

	// Create transport with runtime
	transportConfig := transport.Config{
		Type:       transportType,
		Port:       port,
		TargetPort: targetPort,
		Host:       "localhost",
		Runtime:    runtime,
		Debug:      debugMode,
	}
	transportHandler, err := transport.NewFactory().Create(transportConfig)
	if err != nil {
		return fmt.Errorf("failed to create transport: %v", err)
	}

	// Set up the transport
	fmt.Printf("Setting up %s transport...\n", transportType)
	if err := transportHandler.Setup(
		ctx, runtime, containerName, image, cmdArgs,
		envVars, containerLabels, runPermissionProfile,
	); err != nil {
		return fmt.Errorf("failed to set up transport: %v", err)
	}

	// Start the transport (which also starts the container and monitoring)
	fmt.Printf("Starting %s transport...\n", transportType)
	if err := transportHandler.Start(ctx); err != nil {
		return fmt.Errorf("failed to start transport: %v", err)
	}

	fmt.Printf("MCP server %s started successfully\n", containerName)

	// Update client configurations if not disabled
	if !runNoClientConfig {
		if err := updateClientConfigurations(baseName, "localhost", port); err != nil {
			fmt.Printf("Warning: Failed to update client configurations: %v\n", err)
		}
	}

	// Define a function to stop the MCP server
	stopMCPServer := func(reason string) {
		fmt.Printf("Stopping MCP server: %s\n", reason)

		// Stop the transport (which also stops the container, monitoring, and handles removal)
		fmt.Printf("Stopping %s transport...\n", transportType)
		if err := transportHandler.Stop(ctx); err != nil {
			fmt.Printf("Warning: Failed to stop transport: %v\n", err)
		}

		// Remove the PID file if it exists
		if err := process.RemovePIDFile(baseName); err != nil {
			fmt.Printf("Warning: Failed to remove PID file: %v\n", err)
		}

		fmt.Printf("MCP server %s stopped\n", containerName)
	}

	// Check if we're a detached process
	if os.Getenv("VIBETOOL_DETACHED") == "1" {
		// We're a detached process running in foreground mode
		// Write the PID to a file so the stop command can kill the process
		if err := process.WriteCurrentPIDFile(baseName); err != nil {
			fmt.Printf("Warning: Failed to write PID file: %v\n", err)
		}

		fmt.Printf("Running as detached process (PID: %d)\n", os.Getpid())
	}

	fmt.Println("Press Ctrl+C to stop or wait for container to exit")

	// Set up signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Wait for signal
	sig := <-sigCh
	stopMCPServer(fmt.Sprintf("Received signal %s", sig))
	return nil
}

// updateClientConfigurations updates client configuration files with the MCP server URL
func updateClientConfigurations(containerName, host string, port int) error {
	// Find client configuration files
	configs, err := client.FindClientConfigs()
	if err != nil {
		return fmt.Errorf("failed to find client configurations: %w", err)
	}

	if len(configs) == 0 {
		fmt.Println("No client configuration files found")
		return nil
	}

	// Generate the URL for the MCP server
	url := client.GenerateMCPServerURL(host, port, containerName)

	// Update each configuration file
	for _, config := range configs {
		fmt.Printf("Updating client configuration: %s\n", config.Path)

		// Update the MCP server configuration with locking
		if err := config.SaveWithLock(containerName, url); err != nil {
			fmt.Printf("Warning: Failed to update MCP server configuration in %s: %v\n", config.Path, err)
			continue
		}

		fmt.Printf("Successfully updated client configuration: %s\n", config.Path)
	}

	return nil
}

// getTargetPort selects a target port for the container based on the transport type
// For SSE transport, it finds an available port if none is provided
// For STDIO transport, it returns 0 as no port is needed
func getTargetPort(transportType transport.TransportType, requestedPort int) (int, error) {
	if transportType == transport.TransportTypeSSE {
		targetPort, err := networking.FindOrUsePort(requestedPort)
		if err != nil {
			return 0, fmt.Errorf("target port error: %w", err)
		}
		fmt.Printf("Using target port: %d\n", targetPort)
		return targetPort, nil
	}

	// For STDIO transport, target port is not used
	return 0, nil
}

// detachProcess starts a new detached process with the same command but with the --foreground flag
func detachProcess(image, baseName, containerName string, cmdArgs []string) error {
	// Get the current executable path
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %v", err)
	}

	// Create a log file for the detached process
	logFilePath := fmt.Sprintf("/tmp/vibetool-%s.log", baseName)
	// #nosec G304 - This is safe as baseName is generated by the application
	logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		fmt.Printf("Warning: Failed to create log file: %v\n", err)
	} else {
		defer logFile.Close()
		fmt.Printf("Logging to: %s\n", logFilePath)
	}

	// Prepare the command arguments for the detached process
	// We'll run the same command but with the --foreground flag
	detachedArgs := []string{"run", "--foreground"}

	// Add all the original flags
	if runTransport != "stdio" {
		detachedArgs = append(detachedArgs, "--transport", runTransport)
	}
	if runName != "" {
		detachedArgs = append(detachedArgs, "--name", runName)
	}
	if runPort != 0 {
		detachedArgs = append(detachedArgs, "--port", fmt.Sprintf("%d", runPort))
	}
	if runTargetPort != 0 {
		detachedArgs = append(detachedArgs, "--target-port", fmt.Sprintf("%d", runTargetPort))
	}
	if runPermissionProfile != "stdio" {
		detachedArgs = append(detachedArgs, "--permission-profile", runPermissionProfile)
	}
	for _, env := range runEnv {
		detachedArgs = append(detachedArgs, "--env", env)
	}
	if runNoClientConfig {
		detachedArgs = append(detachedArgs, "--no-client-config")
	}

	// Add the image and any arguments
	detachedArgs = append(detachedArgs, image)
	if len(cmdArgs) > 0 {
		detachedArgs = append(detachedArgs, cmdArgs...)
	}

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

	// Write the PID to a file so the stop command can kill the process
	if err := process.WritePIDFile(baseName, detachedCmd.Process.Pid); err != nil {
		fmt.Printf("Warning: Failed to write PID file: %v\n", err)
	}

	fmt.Printf("MCP server %s is running in the background (PID: %d)\n", containerName, detachedCmd.Process.Pid)
	fmt.Printf("Use 'vibetool stop %s' to stop the server\n", containerName)

	return nil
}
