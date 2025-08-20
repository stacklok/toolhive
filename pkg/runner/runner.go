// Package runner provides functionality for running MCP servers
package runner

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/config"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/process"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/workloads/statuses"
)

// Runner is responsible for running an MCP server with the provided configuration
type Runner struct {
	// Config is the configuration for the runner
	Config *RunConfig

	// telemetryProvider is the OpenTelemetry provider for cleanup
	telemetryProvider *telemetry.Provider

	// supportedMiddleware is a map of supported middleware types to their factory functions.
	supportedMiddleware map[string]types.MiddlewareFactory

	// middlewares is a slice of created middleware instances for cleanup
	middlewares []types.Middleware

	// middlewareFunctions is a slice of middleware functions to apply to the transport
	middlewareFunctions []types.MiddlewareFunction

	// authInfoHandler is the authentication info handler set by auth middleware
	authInfoHandler http.Handler

	// prometheusHandler is the Prometheus metrics handler set by telemetry middleware
	prometheusHandler http.Handler

	statusManager statuses.StatusManager
}

// NewRunner creates a new Runner with the provided configuration
func NewRunner(runConfig *RunConfig, statusManager statuses.StatusManager) *Runner {
	return &Runner{
		Config:              runConfig,
		statusManager:       statusManager,
		supportedMiddleware: GetSupportedMiddlewareFactories(),
	}
}

// AddMiddleware adds a middleware instance and its function to the runner
func (r *Runner) AddMiddleware(middleware types.Middleware) {
	r.middlewares = append(r.middlewares, middleware)
	r.middlewareFunctions = append(r.middlewareFunctions, middleware.Handler())
}

// SetAuthInfoHandler sets the authentication info handler
func (r *Runner) SetAuthInfoHandler(handler http.Handler) {
	r.authInfoHandler = handler
}

// SetPrometheusHandler sets the Prometheus metrics handler
func (r *Runner) SetPrometheusHandler(handler http.Handler) {
	r.prometheusHandler = handler
}

// GetConfig returns a config interface for middleware to access runner configuration
func (r *Runner) GetConfig() types.RunnerConfig {
	return r.Config
}

// GetPort returns the port from the runner config (implements types.RunnerConfig)
func (c *RunConfig) GetPort() int {
	return c.Port
}

// Run runs the MCP server with the provided configuration
//
//nolint:gocyclo // This function is complex but manageable
func (r *Runner) Run(ctx context.Context) error {
	// Check if middleware configs are already populated (new direct configuration)
	// If not, use backwards compatibility to populate from old config fields
	if len(r.Config.MiddlewareConfigs) == 0 {
		// Use backwards compatibility - populate from old config fields
		if err := PopulateMiddlewareConfigs(r.Config); err != nil {
			return fmt.Errorf("failed to populate middleware configs: %v", err)
		}
	}

	// Create transport with runtime
	transportConfig := types.Config{
		Type:       r.Config.Transport,
		ProxyPort:  r.Config.Port,
		TargetPort: r.Config.TargetPort,
		Host:       r.Config.Host,
		TargetHost: r.Config.TargetHost,
		Deployer:   r.Config.Deployer,
		Debug:      r.Config.Debug,
	}

	// Create middleware from the MiddlewareConfigs instances in the RunConfig.
	for _, middlewareConfig := range r.Config.MiddlewareConfigs {
		// First, get the correct factory function for the middleware type.
		factory, ok := r.supportedMiddleware[middlewareConfig.Type]
		if !ok {
			return fmt.Errorf("unsupported middleware type: %s", middlewareConfig.Type)
		}

		// Create the middleware instance using the factory function.
		// The factory will add the middleware to the runner and handle any special configuration.
		if err := factory(&middlewareConfig, r); err != nil {
			return fmt.Errorf("failed to create middleware of type %s: %v", middlewareConfig.Type, err)
		}
	}

	// Set all middleware functions and handlers on transport config
	transportConfig.Middlewares = r.middlewareFunctions
	transportConfig.AuthInfoHandler = r.authInfoHandler
	transportConfig.PrometheusHandler = r.prometheusHandler

	// Set proxy mode for stdio transport
	transportConfig.ProxyMode = r.Config.ProxyMode

	transportHandler, err := transport.NewFactory().Create(transportConfig)
	if err != nil {
		return fmt.Errorf("failed to create transport: %v", err)
	}

	// Process secrets if provided
	if len(r.Config.Secrets) > 0 {
		cfg := config.GetConfig()

		providerType, err := cfg.Secrets.GetProviderType()
		if err != nil {
			return fmt.Errorf("error determining secrets provider type: %w", err)
		}

		secretManager, err := secrets.CreateSecretProvider(providerType)
		if err != nil {
			return fmt.Errorf("error instantiating secret manager %v", err)
		}

		// Process secrets
		if _, err = r.Config.WithSecrets(ctx, secretManager); err != nil {
			return err
		}
	}

	// Set up the transport
	logger.Infof("Setting up %s transport...", r.Config.Transport)
	if err := transportHandler.Setup(
		ctx, r.Config.Deployer, r.Config.ContainerName, r.Config.Image, r.Config.CmdArgs,
		r.Config.EnvVars, r.Config.ContainerLabels, r.Config.PermissionProfile, r.Config.K8sPodTemplatePatch,
		r.Config.IsolateNetwork, r.Config.IgnoreConfig,
	); err != nil {
		return fmt.Errorf("failed to set up transport: %v", err)
	}

	// Start the transport (which also starts the container and monitoring)
	logger.Infof("Starting %s transport for %s...", r.Config.Transport, r.Config.ContainerName)
	if err := transportHandler.Start(ctx); err != nil {
		return fmt.Errorf("failed to start transport: %v", err)
	}

	logger.Infof("MCP server %s started successfully", r.Config.ContainerName)

	// Update client configurations with the MCP server URL.
	// Note that this function checks the configuration to determine which
	// clients should be updated, if any.
	clientManager, err := client.NewManager(ctx)
	if err != nil {
		logger.Warnf("Warning: Failed to create client manager: %v", err)
	} else {
		transportType := labels.GetTransportType(r.Config.ContainerLabels)
		serverURL := transport.GenerateMCPServerURL(transportType, "localhost", r.Config.Port, r.Config.ContainerName)

		if err := clientManager.AddServerToClients(ctx, r.Config.ContainerName, serverURL, transportType, r.Config.Group); err != nil {
			logger.Warnf("Warning: Failed to add server to client configurations: %v", err)
		}
	}

	// Define a function to stop the MCP server
	stopMCPServer := func(reason string) {
		logger.Infof("Stopping MCP server: %s", reason)

		// Stop the transport (which also stops the container, monitoring, and handles removal)
		logger.Infof("Stopping %s transport...", r.Config.Transport)
		if err := transportHandler.Stop(ctx); err != nil {
			logger.Warnf("Warning: Failed to stop transport: %v", err)
		}

		// Cleanup telemetry provider
		if err := r.Cleanup(ctx); err != nil {
			logger.Warnf("Warning: Failed to cleanup telemetry: %v", err)
		}

		// Remove the PID file if it exists
		if err := process.RemovePIDFile(r.Config.BaseName); err != nil {
			logger.Warnf("Warning: Failed to remove PID file: %v", err)
		}

		logger.Infof("MCP server %s stopped", r.Config.ContainerName)
	}

	if process.IsDetached() {
		// We're a detached process running in foreground mode
		// Write the PID to a file so the stop command can kill the process
		if err := process.WriteCurrentPIDFile(r.Config.BaseName); err != nil {
			logger.Warnf("Warning: Failed to write PID file: %v", err)
		}

		logger.Infof("Running as detached process (PID: %d)", os.Getpid())
	} else {
		logger.Info("Press Ctrl+C to stop or wait for container to exit")
	}

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
				logger.Info("Transport handler is nil, exiting monitoring routine...")
				close(doneCh)
				return
			}

			// Check if the transport is still running
			running, err := transportHandler.IsRunning(ctx)
			if err != nil {
				logger.Errorf("Error checking transport status: %v", err)
				// Don't exit immediately on error, try again after pause
				time.Sleep(1 * time.Second)
				continue
			}
			if !running {
				// Transport is no longer running (container exited or was stopped)
				logger.Info("Transport is no longer running, exiting...")
				close(doneCh)
				return
			}

			// Sleep for a short time before checking again
			time.Sleep(1 * time.Second)
		}
	}()

	// At this point, we can consider the workload started successfully.
	if err := r.statusManager.SetWorkloadStatus(ctx, r.Config.ContainerName, rt.WorkloadStatusRunning, ""); err != nil {
		// If we can't set the status to `running` - treat it as a fatal error.
		return fmt.Errorf("failed to set workload status: %v", err)
	}

	// Wait for either a signal or the done channel to be closed
	select {
	case sig := <-sigCh:
		stopMCPServer(fmt.Sprintf("Received signal %s", sig))
	case <-doneCh:
		// The transport has already been stopped (likely by the container monitor)
		// Clean up the PID file and state
		if err := process.RemovePIDFile(r.Config.BaseName); err != nil {
			logger.Warnf("Warning: Failed to remove PID file: %v", err)
		}

		logger.Infof("MCP server %s stopped", r.Config.ContainerName)
	}

	return nil
}

// Cleanup performs cleanup operations for the runner, including shutting down all middleware.
func (r *Runner) Cleanup(ctx context.Context) error {
	// For simplicity, return the last error we encounter during cleanup.
	var lastErr error

	// Clean up all middleware instances
	for i, middleware := range r.middlewares {
		if err := middleware.Close(); err != nil {
			logger.Warnf("Failed to close middleware %d: %v", i, err)
			lastErr = err
		}
	}

	// Legacy telemetry provider cleanup (will be removed when telemetry middleware handles it)
	if r.telemetryProvider != nil {
		logger.Debug("Shutting down telemetry provider")
		if err := r.telemetryProvider.Shutdown(ctx); err != nil {
			logger.Warnf("Warning: Failed to shutdown telemetry provider: %v", err)
			lastErr = err
		}
	}
	return lastErr
}
