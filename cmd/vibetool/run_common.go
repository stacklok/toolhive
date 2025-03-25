package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/stacklok/vibetool/pkg/auth"
	"github.com/stacklok/vibetool/pkg/client"
	"github.com/stacklok/vibetool/pkg/container"
	"github.com/stacklok/vibetool/pkg/environment"
	"github.com/stacklok/vibetool/pkg/labels"
	"github.com/stacklok/vibetool/pkg/networking"
	"github.com/stacklok/vibetool/pkg/permissions"
	"github.com/stacklok/vibetool/pkg/process"
	"github.com/stacklok/vibetool/pkg/transport"
)

// RunOptions contains all the options for running an MCP server
type RunOptions struct {
	// Image is the Docker image to run
	Image string

	// CmdArgs are the arguments to pass to the container
	CmdArgs []string

	// Transport is the transport mode (sse or stdio)
	Transport string

	// Name is the name of the MCP server
	Name string

	// Port is the port for the HTTP proxy to listen on (host port)
	Port int

	// TargetPort is the port for the container to expose (only applicable to SSE transport)
	TargetPort int

	// PermissionProfile is the permission profile to use (stdio, network, or path to JSON file)
	PermissionProfile string

	// EnvVars are the environment variables to pass to the MCP server
	EnvVars []string

	// NoClientConfig indicates whether to update client configuration files
	NoClientConfig bool

	// Foreground indicates whether to run in foreground mode
	Foreground bool

	// OIDCIssuer is the OIDC issuer URL
	OIDCIssuer string

	// OIDCAudience is the OIDC audience
	OIDCAudience string

	// OIDCJwksURL is the OIDC JWKS URL
	OIDCJwksURL string

	// OIDCClientID is the OIDC client ID
	OIDCClientID string

	// Debug indicates whether debug mode is enabled
	Debug bool
}

// RunMCPServer runs an MCP server with the specified options
//
//nolint:gocyclo // This function is complex but manageable
func RunMCPServer(ctx context.Context, cmd *cobra.Command, options RunOptions) error {
	// If not running in foreground mode, start a new detached process and exit
	if !options.Foreground {
		return detachProcess(cmd, options)
	}

	// Create container runtime
	runtime, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}

	// Check if the image exists locally, and pull it if not
	imageExists, err := runtime.ImageExists(ctx, options.Image)
	if err != nil {
		return fmt.Errorf("failed to check if image exists: %v", err)
	}
	if !imageExists {
		fmt.Printf("Image %s not found locally, pulling...\n", options.Image)
		if err := runtime.PullImage(ctx, options.Image); err != nil {
			return fmt.Errorf("failed to pull image: %v", err)
		}
		fmt.Printf("Successfully pulled image: %s\n", options.Image)
	}

	// Generate a container name if not provided
	containerName, baseName := container.GetOrGenerateContainerName(options.Name, options.Image)

	// Parse transport mode
	transportType, err := transport.ParseTransportType(options.Transport)
	if err != nil {
		return fmt.Errorf("invalid transport mode: %s. Valid modes are: sse, stdio", options.Transport)
	}

	// Select a port for the HTTP proxy (host port)
	port, err := networking.FindOrUsePort(options.Port)
	if err != nil {
		return err
	}
	fmt.Printf("Using host port: %d\n", port)

	// Select a target port for the container
	targetPort := 0
	if transportType == transport.TransportTypeSSE {
		targetPort, err = networking.FindOrUsePort(options.TargetPort)
		if err != nil {
			return fmt.Errorf("target port error: %w", err)
		}
		fmt.Printf("Using target port: %d\n", targetPort)
	}

	// Parse environment variables
	envVars, err := environment.ParseEnvironmentVariables(options.EnvVars)
	if err != nil {
		return fmt.Errorf("failed to parse environment variables: %v", err)
	}

	// Set transport-specific environment variables
	// Always pass the targetPort, which will be used for the container's environment variables
	environment.SetTransportEnvironmentVariables(envVars, string(transportType), targetPort)

	// Create container labels
	containerLabels := make(map[string]string)
	labels.AddStandardLabels(containerLabels, containerName, baseName, string(transportType), port)

	// Create transport with runtime
	transportConfig := transport.Config{
		Type:       transportType,
		Port:       port,
		TargetPort: targetPort,
		Host:       "localhost",
		Runtime:    runtime,
		Debug:      options.Debug,
	}

	// Add OIDC middleware if OIDC validation is enabled
	if options.OIDCIssuer != "" || options.OIDCAudience != "" || options.OIDCJwksURL != "" || options.OIDCClientID != "" {
		fmt.Println("OIDC validation enabled for transport")

		// Create JWT validator
		jwtValidator, err := auth.NewJWTValidator(ctx, auth.JWTValidatorConfig{
			Issuer:   options.OIDCIssuer,
			Audience: options.OIDCAudience,
			JWKSURL:  options.OIDCJwksURL,
			ClientID: options.OIDCClientID,
		})
		if err != nil {
			return fmt.Errorf("failed to create JWT validator: %v", err)
		}

		// Add JWT validation middleware to transport config
		transportConfig.Middlewares = append(transportConfig.Middlewares, jwtValidator.Middleware)
	}

	transportHandler, err := transport.NewFactory().Create(transportConfig)
	if err != nil {
		return fmt.Errorf("failed to create transport: %v", err)
	}

	// Load permission profile
	var permProfile *permissions.Profile

	switch options.PermissionProfile {
	case "stdio":
		permProfile = permissions.BuiltinStdioProfile()
	case "network":
		permProfile = permissions.BuiltinNetworkProfile()
	default:
		// Try to load from file
		permProfile, err = permissions.FromFile(options.PermissionProfile)
		if err != nil {
			return fmt.Errorf("failed to load permission profile: %v", err)
		}
	}

	// Set up the transport
	fmt.Printf("Setting up %s transport...\n", transportType)
	if err := transportHandler.Setup(
		ctx, runtime, containerName, options.Image, options.CmdArgs,
		envVars, containerLabels, permProfile,
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
	if !options.NoClientConfig {
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

// detachProcess starts a new detached process with the same command but with the --foreground flag
//
//nolint:gocyclo // This function is complex but manageable
func detachProcess(_ *cobra.Command, options RunOptions) error {
	// Get the current executable path
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %v", err)
	}

	// Generate a container name if not provided
	_, baseName := container.GetOrGenerateContainerName(options.Name, options.Image)

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
	if options.Transport != "stdio" {
		detachedArgs = append(detachedArgs, "--transport", options.Transport)
	}
	if options.Name != "" {
		detachedArgs = append(detachedArgs, "--name", options.Name)
	}
	if options.Port != 0 {
		detachedArgs = append(detachedArgs, "--port", fmt.Sprintf("%d", options.Port))
	}
	if options.TargetPort != 0 {
		detachedArgs = append(detachedArgs, "--target-port", fmt.Sprintf("%d", options.TargetPort))
	}

	// Pass the permission profile to the detached process
	if options.PermissionProfile != "" {
		detachedArgs = append(detachedArgs, "--permission-profile", options.PermissionProfile)
	}

	for _, env := range options.EnvVars {
		detachedArgs = append(detachedArgs, "--env", env)
	}
	if options.NoClientConfig {
		detachedArgs = append(detachedArgs, "--no-client-config")
	}

	// Add OIDC flags if they were provided
	if options.OIDCIssuer != "" {
		detachedArgs = append(detachedArgs, "--oidc-issuer", options.OIDCIssuer)
	}
	if options.OIDCAudience != "" {
		detachedArgs = append(detachedArgs, "--oidc-audience", options.OIDCAudience)
	}
	if options.OIDCJwksURL != "" {
		detachedArgs = append(detachedArgs, "--oidc-jwks-url", options.OIDCJwksURL)
	}
	if options.OIDCClientID != "" {
		detachedArgs = append(detachedArgs, "--oidc-client-id", options.OIDCClientID)
	}

	// Add the image and any arguments
	detachedArgs = append(detachedArgs, options.Image)
	if len(options.CmdArgs) > 0 {
		detachedArgs = append(detachedArgs, options.CmdArgs...)
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

	fmt.Printf("MCP server is running in the background (PID: %d)\n", detachedCmd.Process.Pid)
	fmt.Printf("Use 'vibetool stop %s' to stop the server\n", options.Name)

	return nil
}
