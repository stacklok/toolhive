// Package runner provides functionality for running MCP servers
package runner

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stacklok/vibetool/pkg/auth"
	"github.com/stacklok/vibetool/pkg/client"
	"github.com/stacklok/vibetool/pkg/process"
	"github.com/stacklok/vibetool/pkg/transport"
	"github.com/stacklok/vibetool/pkg/transport/types"
)

// Runner is responsible for running an MCP server with the provided configuration
type Runner struct {
	// Config is the configuration for the runner
	Config *RunConfig
}

// NewRunner creates a new Runner with the provided configuration
func NewRunner(config *RunConfig) *Runner {
	return &Runner{
		Config: config,
	}
}

// Run runs the MCP server with the provided configuration
//
//nolint:gocyclo // This function is complex but manageable
func (r *Runner) Run(ctx context.Context) error {
	// Create transport with runtime
	transportConfig := types.Config{
		Type:       r.Config.Transport,
		Port:       r.Config.Port,
		TargetPort: r.Config.TargetPort,
		Host:       "localhost",
		Runtime:    r.Config.Runtime,
		Debug:      r.Config.Debug,
	}

	// Add OIDC middleware if OIDC validation is enabled
	if r.Config.OIDCConfig != nil {
		fmt.Println("OIDC validation enabled for transport")

		// Create JWT validator
		jwtValidator, err := auth.NewJWTValidator(ctx, auth.JWTValidatorConfig{
			Issuer:   r.Config.OIDCConfig.Issuer,
			Audience: r.Config.OIDCConfig.Audience,
			JWKSURL:  r.Config.OIDCConfig.JwksURL,
			ClientID: r.Config.OIDCConfig.ClientID,
		})
		if err != nil {
			return fmt.Errorf("failed to create JWT validator: %v", err)
		}

		// Add JWT validation middleware to transport config
		transportConfig.Middlewares = append(transportConfig.Middlewares, jwtValidator.Middleware)
	}

	// Add authorization middleware if authorization configuration is provided
	if r.Config.AuthzConfig != nil {
		fmt.Println("Authorization enabled for transport")

		// Get the middleware from the configuration
		middleware, err := r.Config.AuthzConfig.CreateMiddleware()
		if err != nil {
			return fmt.Errorf("failed to get authorization middleware: %v", err)
		}

		// Add authorization middleware to transport config
		transportConfig.Middlewares = append(transportConfig.Middlewares, middleware)
	}

	transportHandler, err := transport.NewFactory().Create(transportConfig)
	if err != nil {
		return fmt.Errorf("failed to create transport: %v", err)
	}

	// Set up the transport
	fmt.Printf("Setting up %s transport...\n", r.Config.Transport)
	if err := transportHandler.Setup(
		ctx, r.Config.Runtime, r.Config.ContainerName, r.Config.Image, r.Config.CmdArgs,
		r.Config.EnvVars, r.Config.ContainerLabels, r.Config.PermissionProfile,
	); err != nil {
		return fmt.Errorf("failed to set up transport: %v", err)
	}

	// Start the transport (which also starts the container and monitoring)
	fmt.Printf("Starting %s transport...\n", r.Config.Transport)
	if err := transportHandler.Start(ctx); err != nil {
		return fmt.Errorf("failed to start transport: %v", err)
	}

	fmt.Printf("MCP server %s started successfully\n", r.Config.ContainerName)

	// Update client configurations if not disabled
	if !r.Config.NoClientConfig {
		if err := updateClientConfigurations(r.Config.BaseName, "localhost", r.Config.Port); err != nil {
			fmt.Printf("Warning: Failed to update client configurations: %v\n", err)
		}
	}

	// Define a function to stop the MCP server
	stopMCPServer := func(reason string) {
		fmt.Printf("Stopping MCP server: %s\n", reason)

		// Stop the transport (which also stops the container, monitoring, and handles removal)
		fmt.Printf("Stopping %s transport...\n", r.Config.Transport)
		if err := transportHandler.Stop(ctx); err != nil {
			fmt.Printf("Warning: Failed to stop transport: %v\n", err)
		}

		// Remove the PID file if it exists
		if err := process.RemovePIDFile(r.Config.BaseName); err != nil {
			fmt.Printf("Warning: Failed to remove PID file: %v\n", err)
		}

		fmt.Printf("MCP server %s stopped\n", r.Config.ContainerName)
	}

	// Check if we're a detached process
	if os.Getenv("VIBETOOL_DETACHED") == "1" {
		// We're a detached process running in foreground mode
		// Write the PID to a file so the stop command can kill the process
		if err := process.WriteCurrentPIDFile(r.Config.BaseName); err != nil {
			fmt.Printf("Warning: Failed to write PID file: %v\n", err)
		}

		fmt.Printf("Running as detached process (PID: %d)\n", os.Getpid())
	}

	fmt.Println("Press Ctrl+C to stop or wait for container to exit")

	// Set up signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Create a done channel to signal when the server has been stopped
	doneCh := make(chan struct{})

	// Start a goroutine to monitor the transport's running state
	go func() {
		for {
			// Safely check if transportHandler is nil
			if transportHandler == nil {
				fmt.Println("Transport handler is nil, exiting monitoring routine...")
				close(doneCh)
				return
			}

			// Check if the transport is still running
			running, err := transportHandler.IsRunning(ctx)
			if err != nil {
				fmt.Printf("Error checking transport status: %v\n", err)
				// Don't exit immediately on error, try again after pause
				time.Sleep(1 * time.Second)
				continue
			}
			if !running {
				// Transport is no longer running (container exited or was stopped)
				fmt.Println("Transport is no longer running, exiting...")
				close(doneCh)
				return
			}

			// Sleep for a short time before checking again
			time.Sleep(1 * time.Second)
		}
	}()

	// Wait for either a signal or the done channel to be closed
	select {
	case sig := <-sigCh:
		stopMCPServer(fmt.Sprintf("Received signal %s", sig))
	case <-doneCh:
		// The transport has already been stopped (likely by the container monitor)
		// Just clean up the PID file
		if err := process.RemovePIDFile(r.Config.BaseName); err != nil {
			fmt.Printf("Warning: Failed to remove PID file: %v\n", err)
		}
		fmt.Printf("MCP server %s stopped\n", r.Config.ContainerName)
	}

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
