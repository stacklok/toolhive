package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/stacklok/vibetool/pkg/client"
	"github.com/stacklok/vibetool/pkg/container"
	"github.com/stacklok/vibetool/pkg/environment"
	"github.com/stacklok/vibetool/pkg/labels"
	"github.com/stacklok/vibetool/pkg/networking"
	"github.com/stacklok/vibetool/pkg/permissions"
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
	runCmd.Flags().StringVar(&runPermissionProfile, "permission-profile", "stdio", "Permission profile to use (stdio, network, or path to JSON file)")
	runCmd.Flags().StringArrayVarP(&runEnv, "env", "e", []string{}, "Environment variables to pass to the MCP server (format: KEY=VALUE)")
	runCmd.Flags().BoolVar(&runNoClientConfig, "no-client-config", false, "Do not update client configuration files with the MCP server URL")
	runCmd.Flags().BoolVarP(&runForeground, "foreground", "f", false, "Run in foreground mode (block until container exits)")
}
func runCmdFunc(cmd *cobra.Command, args []string) error {
	// Get the image and command arguments
	image := args[0]
	cmdArgs := args[1:]

	// Generate a container name if not provided
	containerName := runName
	var baseName string
	if containerName == "" {
		baseName = generateContainerBaseName(image)
		containerName = appendTimestamp(baseName)
	} else {
		// If container name is provided, use it as the base name
		baseName = containerName
	}
	
	// If not running in foreground mode, start a new detached process and exit
	if !runForeground {
		return detachProcess(cmd, image, baseName, containerName, cmdArgs)
	}

	// Parse transport mode
	transportType, err := transport.ParseTransportType(runTransport)
	if err != nil {
		return fmt.Errorf("invalid transport mode: %s. Valid modes are: sse, stdio", runTransport)
	}
	
	// Select a port for the HTTP proxy (host port)
	port := runPort
	if port == 0 {
		port = networking.FindAvailable()
		if port == 0 {
			return fmt.Errorf("could not find an available port")
		}
		fmt.Printf("Using host port: %d\n", port)
	} else if port > 0 && !networking.IsAvailable(port) {
		return fmt.Errorf("port %d is already in use", port)
	}
	
	// Select a target port for the container (only applicable to SSE transport)
	targetPort := runTargetPort
	if transportType == transport.TransportTypeSSE && targetPort == 0 {
		targetPort = networking.FindAvailable()
		if targetPort == 0 {
			return fmt.Errorf("could not find an available target port")
		}
		fmt.Printf("Using target port: %d\n", targetPort)
	} else if transportType == transport.TransportTypeSSE && targetPort > 0 && !networking.IsAvailable(targetPort) {
		return fmt.Errorf("target port %d is already in use", targetPort)
	} else if transportType == transport.TransportTypeStdio {
		// For STDIO transport, target port is not used
		targetPort = 0
	}

	// Load permission profile
	var profile *permissions.Profile
	switch runPermissionProfile {
	case "stdio":
		profile = permissions.BuiltinStdioProfile()
	case "network":
		profile = permissions.BuiltinNetworkProfile()
	default:
		// Try to load from file
		profile, err = permissions.FromFile(runPermissionProfile)
		if err != nil {
			return fmt.Errorf("failed to load permission profile: %v", err)
		}
	}

	// Convert permission profile to container config
	permissionConfig, err := profile.ToContainerConfigWithTransport(string(transportType))
	if err != nil {
		return fmt.Errorf("failed to convert permission profile: %v", err)
	}

	// Convert permissions.ContainerConfig to container.PermissionConfig
	containerPermConfig := container.PermissionConfig{
		NetworkMode: permissionConfig.NetworkMode,
		CapDrop:     permissionConfig.CapDrop,
		CapAdd:      permissionConfig.CapAdd,
		SecurityOpt: permissionConfig.SecurityOpt,
	}

	// Convert mounts
	for _, m := range permissionConfig.Mounts {
		containerPermConfig.Mounts = append(containerPermConfig.Mounts, container.Mount{
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}

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

	// Create transport
	transportConfig := transport.Config{
		Type:       transportType,
		Port:       port,
		TargetPort: targetPort,
		Host:       "localhost",
	}
	transportHandler, err := transport.NewFactory().Create(transportConfig)
	if err != nil {
		return fmt.Errorf("failed to create transport: %v", err)
	}

	// If using stdio transport, set the runtime
	if transportType == transport.TransportTypeStdio {
		stdioTransport, ok := transportHandler.(*transport.StdioTransport)
		if ok {
			stdioTransport.WithRuntime(runtime)
			// Get a new runtime instance for creating the container
			runtime, err = container.NewFactory().Create(ctx)
			if err != nil {
				return fmt.Errorf("failed to create container runtime: %v", err)
			}
		}
	}

	// Create container options
	containerOptions := container.NewCreateContainerOptions()
	
	// Set options based on transport type
	if transportType == transport.TransportTypeSSE {
		// For SSE transport, expose the target port in the container
		containerPortStr := fmt.Sprintf("%d/tcp", targetPort)
		containerOptions.ExposedPorts[containerPortStr] = struct{}{}
		
		// Create port bindings for localhost
		portBindings := []container.PortBinding{
			{
				HostIP:   "127.0.0.1", // IPv4 localhost
				HostPort: fmt.Sprintf("%d", targetPort),
			},
		}
		
		// Check if IPv6 is available and add IPv6 localhost binding
		if networking.IsIPv6Available() {
			portBindings = append(portBindings, container.PortBinding{
				HostIP:   "::1", // IPv6 localhost
				HostPort: fmt.Sprintf("%d", targetPort),
			})
		}
		
		// Set the port bindings
		containerOptions.PortBindings[containerPortStr] = portBindings
		
		fmt.Printf("Exposing container port %d\n", targetPort)
		
		// For SSE transport, we don't need to attach stdio
		containerOptions.AttachStdio = false
	} else if transportType == transport.TransportTypeStdio {
		// For STDIO transport, we need to attach stdio
		containerOptions.AttachStdio = true
	}
	
	// Create the container
	fmt.Printf("Creating container %s from image %s...\n", containerName, image)
	containerID, err := runtime.CreateContainer(
		ctx,
		image,
		containerName,
		cmdArgs,
		envVars,
		containerLabels,
		containerPermConfig,
		containerOptions,
	)
	if err != nil {
		return fmt.Errorf("failed to create container: %v", err)
	}
	fmt.Printf("Container created with ID: %s\n", containerID)

	// Start the container
	fmt.Printf("Starting container %s...\n", containerName)
	if err := runtime.StartContainer(ctx, containerID); err != nil {
		return fmt.Errorf("failed to start container: %v", err)
	}

	// Get container IP for SSE transport
	var containerIP string
	if transportType == transport.TransportTypeSSE {
		ip, err := runtime.GetContainerIP(ctx, containerID)
		if err != nil {
			fmt.Printf("Warning: Failed to get container IP: %v\n", err)
		} else {
			containerIP = ip
		}
	}

	// Set up the transport
	fmt.Printf("Setting up %s transport...\n", transportType)
	if err := transportHandler.Setup(ctx, containerID, containerName, envVars, containerIP); err != nil {
		return fmt.Errorf("failed to set up transport: %v", err)
	}

	// For STDIO transport, attach to the container
	var stdin io.WriteCloser
	var stdout io.ReadCloser
	if transportType == transport.TransportTypeStdio {
		var err error
		stdin, stdout, err = runtime.AttachContainer(ctx, containerID)
		if err != nil {
			return fmt.Errorf("failed to attach to container: %v", err)
		}
	}

	// Start the transport
	fmt.Printf("Starting %s transport...\n", transportType)
	if err := transportHandler.Start(ctx, stdin, stdout); err != nil {
		return fmt.Errorf("failed to start transport: %v", err)
	}

	// Create a container monitor
	monitorRuntime, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container monitor: %v", err)
	}
	monitor := container.NewMonitor(monitorRuntime, containerID, containerName)

	// Start monitoring the container
	errorCh, err := monitor.StartMonitoring(ctx)
	if err != nil {
		return fmt.Errorf("failed to start container monitoring: %v", err)
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
		
		// Stop monitoring
		monitor.StopMonitoring()
		
		// Stop the transport
		fmt.Printf("Stopping %s transport...\n", transportType)
		if err := transportHandler.Stop(ctx); err != nil {
			fmt.Printf("Warning: Failed to stop transport: %v\n", err)
		}
		
		// Stop the container
		fmt.Printf("Stopping container %s...\n", containerName)
		if err := runtime.StopContainer(ctx, containerID); err != nil {
			fmt.Printf("Warning: Failed to stop container: %v\n", err)
		}
		
		// Remove the container if debug mode is not enabled
		debugMode, _ := cmd.Flags().GetBool("debug")
		if !debugMode {
			fmt.Printf("Removing container %s...\n", containerName)
			if err := runtime.RemoveContainer(ctx, containerID); err != nil {
				fmt.Printf("Warning: Failed to remove container: %v\n", err)
			}
			fmt.Printf("Container %s removed\n", containerName)
		} else {
			fmt.Printf("Debug mode enabled, container %s not removed\n", containerName)
		}
		
		// Remove the PID file if it exists
		process.RemovePIDFile(baseName)
		
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
	
	// Wait for signal or container exit
	select {
	case sig := <-sigCh:
		stopMCPServer(fmt.Sprintf("Received signal %s", sig))
	case err := <-errorCh:
		stopMCPServer(fmt.Sprintf("Container exited unexpectedly: %v", err))
	}
	return nil
}

// generateContainerBaseName generates a base name for a container from the image name
func generateContainerBaseName(image string) string {
	// Extract the base name from the image, preserving registry namespaces
	// Examples:
	// - "nginx:latest" -> "nginx"
	// - "docker.io/library/nginx:latest" -> "docker.io-library-nginx"
	// - "quay.io/stacklok/mcp-server:v1" -> "quay.io-stacklok-mcp-server"

	// First, remove the tag part (everything after the colon)
	imageWithoutTag := strings.Split(image, ":")[0]

	// Replace slashes with dashes to preserve namespace structure
	namespaceName := strings.ReplaceAll(imageWithoutTag, "/", "-")

	// Sanitize the name (allow alphanumeric, dashes, and dots)
	var sanitizedName strings.Builder
	for _, c := range namespaceName {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.' {
			sanitizedName.WriteRune(c)
		} else {
			sanitizedName.WriteRune('-')
		}
	}

	return sanitizedName.String()
}

// appendTimestamp appends a timestamp to a base name to ensure uniqueness
func appendTimestamp(baseName string) string {
	timestamp := time.Now().Unix()
	return fmt.Sprintf("%s-%d", baseName, timestamp)
}

// generateContainerName generates a container name from the image name (for backward compatibility)
func generateContainerName(image string) string {
	return appendTimestamp(generateContainerBaseName(image))
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

// detachProcess starts a new detached process with the same command but with the --foreground flag
func detachProcess(cmd *cobra.Command, image, baseName, containerName string, cmdArgs []string) error {
	// Get the current executable path
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %v", err)
	}
	
	// Create a log file for the detached process
	logFilePath := fmt.Sprintf("/tmp/vibetool-%s.log", baseName)
	logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
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