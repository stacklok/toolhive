// Package runner provides functionality for running MCP servers
package runner

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/process"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/types"
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
		TargetHost: r.Config.TargetHost,
		Runtime:    r.Config.Runtime,
		Debug:      r.Config.Debug,
	}

	// Add OIDC middleware if OIDC validation is enabled
	if r.Config.OIDCConfig != nil {
		logger.Log.Info("OIDC validation enabled for transport")

		// Create JWT validator
		jwtValidator, err := auth.NewJWTValidator(ctx, auth.JWTValidatorConfig{
			Issuer:   r.Config.OIDCConfig.Issuer,
			Audience: r.Config.OIDCConfig.Audience,
			JWKSURL:  r.Config.OIDCConfig.JWKSURL,
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
		logger.Log.Info("Authorization enabled for transport")

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
	logger.Log.Info(fmt.Sprintf("Setting up %s transport...", r.Config.Transport))
	if err := transportHandler.Setup(
		ctx, r.Config.Runtime, r.Config.ContainerName, r.Config.Image, r.Config.CmdArgs,
		r.Config.EnvVars, r.Config.ContainerLabels, r.Config.PermissionProfile,
	); err != nil {
		return fmt.Errorf("failed to set up transport: %v", err)
	}

	// Start the transport (which also starts the container and monitoring)
	logger.Log.Info(fmt.Sprintf("Starting %s transport...", r.Config.Transport))
	if err := transportHandler.Start(ctx); err != nil {
		return fmt.Errorf("failed to start transport: %v", err)
	}

	logger.Log.Info(fmt.Sprintf("MCP server %s started successfully", r.Config.ContainerName))

	// Save the configuration to the state store
	if err := r.SaveState(ctx); err != nil {
		logger.Log.Warn(fmt.Sprintf("Warning: Failed to save run configuration: %v", err))
	}

	// Update client configurations with the MCP server URL.
	// Note that this function checks the configuration to determine which
	// clients should be updated, if any.
	if err := updateClientConfigurations(r.Config.BaseName, "localhost", r.Config.Port); err != nil {
		logger.Log.Warn(fmt.Sprintf("Warning: Failed to update client configurations: %v", err))
	}

	// Define a function to stop the MCP server
	stopMCPServer := func(reason string) {
		logger.Log.Info(fmt.Sprintf("Stopping MCP server: %s", reason))

		// Stop the transport (which also stops the container, monitoring, and handles removal)
		logger.Log.Info(fmt.Sprintf("Stopping %s transport...", r.Config.Transport))
		if err := transportHandler.Stop(ctx); err != nil {
			logger.Log.Warn(fmt.Sprintf("Warning: Failed to stop transport: %v", err))
		}

		// Remove the PID file if it exists
		if err := process.RemovePIDFile(r.Config.BaseName); err != nil {
			logger.Log.Warn(fmt.Sprintf("Warning: Failed to remove PID file: %v", err))
		}

		logger.Log.Info(fmt.Sprintf("MCP server %s stopped", r.Config.ContainerName))
	}

	// Check if we're a detached process
	if os.Getenv("TOOLHIVE_DETACHED") == "1" {
		// We're a detached process running in foreground mode
		// Write the PID to a file so the stop command can kill the process
		if err := process.WriteCurrentPIDFile(r.Config.BaseName); err != nil {
			logger.Log.Warn(fmt.Sprintf("Warning: Failed to write PID file: %v", err))
		}

		logger.Log.Info(fmt.Sprintf("Running as detached process (PID: %d)", os.Getpid()))
	}

	logger.Log.Info("Press Ctrl+C to stop or wait for container to exit")

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
				logger.Log.Info("Transport handler is nil, exiting monitoring routine...")
				close(doneCh)
				return
			}

			// Check if the transport is still running
			running, err := transportHandler.IsRunning(ctx)
			if err != nil {
				logger.Log.Error(fmt.Sprintf("Error checking transport status: %v", err))
				// Don't exit immediately on error, try again after pause
				time.Sleep(1 * time.Second)
				continue
			}
			if !running {
				// Transport is no longer running (container exited or was stopped)
				logger.Log.Info("Transport is no longer running, exiting...")
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
		// Clean up the PID file and state
		if err := process.RemovePIDFile(r.Config.BaseName); err != nil {
			logger.Log.Warn(fmt.Sprintf("Warning: Failed to remove PID file: %v", err))
		}

		logger.Log.Info(fmt.Sprintf("MCP server %s stopped", r.Config.ContainerName))
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
		logger.Log.Info("No client configuration files found")
		return nil
	}

	// Generate the URL for the MCP server
	url := client.GenerateMCPServerURL(host, port, containerName)

	// Update each configuration file
	for _, config := range configs {
		logger.Log.Info(fmt.Sprintf("Updating client configuration: %s", config.Path))

		// Update the MCP server configuration with locking
		if err := config.SaveWithLock(containerName, url, config.Editor); err != nil {
			logger.Log.Warn(fmt.Sprintf("Warning: Failed to update MCP server configuration in %s: %v", config.Path, err))
			continue
		}

		logger.Log.Info(fmt.Sprintf("Successfully updated client configuration: %s", config.Path))
	}

	return nil
}
