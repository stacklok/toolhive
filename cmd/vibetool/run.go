package main

import (
	"context"
	"fmt"
	"io"
	"os"
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
	runPermissionProfile string
	runEnv               []string
	runNoClientConfig    bool
)

func init() {
	runCmd.Flags().StringVar(&runTransport, "transport", "stdio", "Transport mode (sse or stdio)")
	runCmd.Flags().StringVar(&runName, "name", "", "Name of the MCP server (auto-generated from image if not provided)")
	runCmd.Flags().IntVar(&runPort, "port", 0, "Port to expose (for SSE transport or STDIO reverse proxy)")
	runCmd.Flags().StringVar(&runPermissionProfile, "permission-profile", "stdio", "Permission profile to use (stdio, network, or path to JSON file)")
	runCmd.Flags().StringArrayVarP(&runEnv, "env", "e", []string{}, "Environment variables to pass to the MCP server (format: KEY=VALUE)")
	runCmd.Flags().BoolVar(&runNoClientConfig, "no-client-config", false, "Do not update client configuration files with the MCP server URL")
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

	// Select a port
	port := runPort
	if port == 0 {
		port = networking.FindAvailable()
		if port == 0 {
			return fmt.Errorf("could not find an available port")
		}
		fmt.Printf("Using port: %d\n", port)
	} else if port > 0 && !networking.IsAvailable(port) {
		return fmt.Errorf("port %d is already in use", port)
	}

	// Parse transport mode
	transportType, err := transport.ParseTransportType(runTransport)
	if err != nil {
		return fmt.Errorf("invalid transport mode: %s. Valid modes are: sse, stdio", runTransport)
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
	environment.SetTransportEnvironmentVariables(envVars, string(transportType), port)

	// Create container labels
	containerLabels := make(map[string]string)
	labels.AddStandardLabels(containerLabels, containerName, string(transportType), port)

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
		Type: transportType,
		Port: port,
		Host: "localhost",
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
		// For SSE transport, expose the container port
		containerPortStr := fmt.Sprintf("%d/tcp", port)
		containerOptions.ExposedPorts[containerPortStr] = struct{}{}
		fmt.Printf("Exposing container port %d\n", port)
		
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
	
	fmt.Println("Press Ctrl+C to stop or wait for container to exit")

	// Set up signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Wait for signal or container exit
	select {
	case sig := <-sigCh:
		fmt.Printf("Received signal %s, stopping MCP server...\n", sig)
	case err := <-errorCh:
		fmt.Printf("Container exited unexpectedly: %v\n", err)
	}

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

	fmt.Printf("MCP server %s stopped\n", containerName)
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