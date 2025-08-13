// Package runner provides functionality for running MCP servers
package runner

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/config"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/mcp"
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

	statusManager statuses.StatusManager

	logger *zap.SugaredLogger
}

// NewRunner creates a new Runner with the provided configuration
func NewRunner(runConfig *RunConfig, statusManager statuses.StatusManager, logger *zap.SugaredLogger) *Runner {
	return &Runner{
		Config:        runConfig,
		statusManager: statusManager,
		logger:        logger,
	}
}

// Run runs the MCP server with the provided configuration
//
//nolint:gocyclo // This function is complex but manageable
func (r *Runner) Run(ctx context.Context) error {
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
		middleware, err := factory(&middlewareConfig)
		if err != nil {
			return fmt.Errorf("failed to create middleware of type %s: %v", middlewareConfig.Type, err)
		}

		// Ensure middleware is cleaned up on shutdown.
		defer func() {
			if err := middleware.Close(); err != nil {
				r.logger.Warnf("Failed to close middleware of type %s: %v", middlewareConfig.Type, err)
			}
		}()
		transportConfig.Middlewares = append(transportConfig.Middlewares, middleware.Handler())
	}

	if len(r.Config.ToolsFilter) > 0 {
		toolsFilterMiddleware, err := mcp.NewToolFilterMiddleware(r.Config.ToolsFilter)
		if err != nil {
			return fmt.Errorf("failed to create tools filter middleware: %v", err)
		}
		transportConfig.Middlewares = append(transportConfig.Middlewares, toolsFilterMiddleware)

		toolsCallFilterMiddleware, err := mcp.NewToolCallFilterMiddleware(r.Config.ToolsFilter, r.logger)
		if err != nil {
			return fmt.Errorf("failed to create tools call filter middleware: %v", err)
		}
		transportConfig.Middlewares = append(transportConfig.Middlewares, toolsCallFilterMiddleware)
	}

	authMiddleware, authInfoHandler, err := auth.GetAuthenticationMiddleware(ctx, r.Config.OIDCConfig, r.logger)
	if err != nil {
		return fmt.Errorf("failed to create authentication middleware: %v", err)
	}
	transportConfig.Middlewares = append(transportConfig.Middlewares, authMiddleware)
	transportConfig.AuthInfoHandler = authInfoHandler

	// Add MCP parsing middleware after authentication
	r.logger.Info("MCP parsing middleware enabled for transport")
	transportConfig.Middlewares = append(transportConfig.Middlewares, mcp.ParsingMiddleware)

	// Add telemetry middleware if telemetry configuration is provided
	if r.Config.TelemetryConfig != nil {
		r.logger.Info("OpenTelemetry instrumentation enabled for transport")

		// Create telemetry provider
		telemetryProvider, err := telemetry.NewProvider(ctx, *r.Config.TelemetryConfig)
		if err != nil {
			return fmt.Errorf("failed to create telemetry provider: %w", err)
		}

		// Create telemetry middleware with server name and transport type
		telemetryMiddleware := telemetryProvider.Middleware(r.Config.Name, r.Config.Transport.String())
		transportConfig.Middlewares = append(transportConfig.Middlewares, telemetryMiddleware)

		// Add Prometheus handler to transport config if metrics port is configured
		if r.Config.TelemetryConfig.EnablePrometheusMetricsPath {
			transportConfig.PrometheusHandler = telemetryProvider.PrometheusHandler()
			r.logger.Infof("Prometheus metrics will be exposed on port %d at /metrics", r.Config.Port)
		}

		// Store provider for cleanup
		r.telemetryProvider = telemetryProvider
	}

	// Add authorization middleware if authorization configuration is provided
	if r.Config.AuthzConfig != nil {
		r.logger.Info("Authorization enabled for transport")

		// Get the middleware from the configuration
		middleware, err := r.Config.AuthzConfig.CreateMiddleware(r.logger)
		if err != nil {
			return fmt.Errorf("failed to get authorization middleware: %v", err)
		}

		// Add authorization middleware to transport config
		transportConfig.Middlewares = append(transportConfig.Middlewares, middleware)
	}

	// Add audit middleware if audit configuration is provided
	if r.Config.AuditConfig != nil {
		r.logger.Info("Audit logging enabled for transport")

		// Set the component name if not already set
		if r.Config.AuditConfig.Component == "" {
			r.Config.AuditConfig.Component = r.Config.ContainerName
		}

		// Get the middleware from the configuration
		middleware, err := r.Config.AuditConfig.CreateMiddleware(r.logger)
		if err != nil {
			return fmt.Errorf("failed to create audit middleware: %w", err)
		}

		// Add audit middleware to transport config
		transportConfig.Middlewares = append(transportConfig.Middlewares, middleware)
	}

	// Set proxy mode for stdio transport
	transportConfig.ProxyMode = r.Config.ProxyMode

	transportHandler, err := transport.NewFactory(r.logger).Create(transportConfig)
	if err != nil {
		return fmt.Errorf("failed to create transport: %v", err)
	}

	// Process secrets if provided
	if len(r.Config.Secrets) > 0 {
		cfg := config.GetConfig(r.logger)

		providerType, err := cfg.Secrets.GetProviderType()
		if err != nil {
			return fmt.Errorf("error determining secrets provider type: %w", err)
		}

		secretManager, err := secrets.CreateSecretProvider(providerType, r.logger)
		if err != nil {
			return fmt.Errorf("error instantiating secret manager %v", err)
		}

		// Process secrets
		if _, err = r.Config.WithSecrets(ctx, secretManager); err != nil {
			return err
		}
	}

	// Set up the transport
	r.logger.Infof("Setting up %s transport...", r.Config.Transport)
	if err := transportHandler.Setup(
		ctx, r.Config.Deployer, r.Config.ContainerName, r.Config.Image, r.Config.CmdArgs,
		r.Config.EnvVars, r.Config.ContainerLabels, r.Config.PermissionProfile, r.Config.K8sPodTemplatePatch,
		r.Config.IsolateNetwork, r.Config.IgnoreConfig,
	); err != nil {
		return fmt.Errorf("failed to set up transport: %v", err)
	}

	// Start the transport (which also starts the container and monitoring)
	r.logger.Infof("Starting %s transport for %s...", r.Config.Transport, r.Config.ContainerName)
	if err := transportHandler.Start(ctx); err != nil {
		return fmt.Errorf("failed to start transport: %v", err)
	}

	r.logger.Infof("MCP server %s started successfully", r.Config.ContainerName)

	// Update client configurations with the MCP server URL.
	// Note that this function checks the configuration to determine which
	// clients should be updated, if any.
	clientManager, err := client.NewManager(ctx, r.logger)
	if err != nil {
		r.logger.Warnf("Warning: Failed to create client manager: %v", err)
	} else {
		transportType := labels.GetTransportType(r.Config.ContainerLabels)
		serverURL := transport.GenerateMCPServerURL(transportType, "localhost", r.Config.Port, r.Config.ContainerName)

		if err := clientManager.AddServerToClients(ctx, r.Config.ContainerName, serverURL, transportType, r.Config.Group); err != nil {
			r.logger.Warnf("Warning: Failed to add server to client configurations: %v", err)
		}
	}

	// Define a function to stop the MCP server
	stopMCPServer := func(reason string) {
		r.logger.Infof("Stopping MCP server: %s", reason)

		// Stop the transport (which also stops the container, monitoring, and handles removal)
		r.logger.Infof("Stopping %s transport...", r.Config.Transport)
		if err := transportHandler.Stop(ctx); err != nil {
			r.logger.Warnf("Warning: Failed to stop transport: %v", err)
		}

		// Cleanup telemetry provider
		if err := r.Cleanup(ctx); err != nil {
			r.logger.Warnf("Warning: Failed to cleanup telemetry: %v", err)
		}

		// Remove the PID file if it exists
		if err := process.RemovePIDFile(r.Config.BaseName); err != nil {
			r.logger.Warnf("Warning: Failed to remove PID file: %v", err)
		}

		r.logger.Infof("MCP server %s stopped", r.Config.ContainerName)
	}

	if process.IsDetached() {
		// We're a detached process running in foreground mode
		// Write the PID to a file so the stop command can kill the process
		if err := process.WriteCurrentPIDFile(r.Config.BaseName); err != nil {
			r.logger.Warnf("Warning: Failed to write PID file: %v", err)
		}

		r.logger.Infof("Running as detached process (PID: %d)", os.Getpid())
	} else {
		r.logger.Info("Press Ctrl+C to stop or wait for container to exit")
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
				r.logger.Info("Transport handler is nil, exiting monitoring routine...")
				close(doneCh)
				return
			}

			// Check if the transport is still running
			running, err := transportHandler.IsRunning(ctx)
			if err != nil {
				r.logger.Errorf("Error checking transport status: %v", err)
				// Don't exit immediately on error, try again after pause
				time.Sleep(1 * time.Second)
				continue
			}
			if !running {
				// Transport is no longer running (container exited or was stopped)
				r.logger.Info("Transport is no longer running, exiting...")
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
			r.logger.Warnf("Warning: Failed to remove PID file: %v", err)
		}

		r.logger.Infof("MCP server %s stopped", r.Config.ContainerName)
	}

	return nil
}

// Cleanup performs cleanup operations for the runner, including shutting down telemetry.
func (r *Runner) Cleanup(ctx context.Context) error {
	if r.telemetryProvider != nil {
		r.logger.Debug("Shutting down telemetry provider")
		if err := r.telemetryProvider.Shutdown(ctx); err != nil {
			r.logger.Warnf("Warning: Failed to shutdown telemetry provider: %v", err)
			return err
		}
	}
	return nil
}
