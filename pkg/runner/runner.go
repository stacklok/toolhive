// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package runner provides functionality for running MCP servers
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/remote"
	authsecrets "github.com/stacklok/toolhive/pkg/auth/secrets"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/config"
	ct "github.com/stacklok/toolhive/pkg/container"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/process"
	"github.com/stacklok/toolhive/pkg/runtime"
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

	// namedMiddlewares is a slice of named middleware to apply to the transport
	namedMiddlewares []types.NamedMiddleware

	// authInfoHandler is the authentication info handler set by auth middleware
	authInfoHandler http.Handler

	// prometheusHandler is the Prometheus metrics handler set by telemetry middleware
	prometheusHandler http.Handler

	statusManager statuses.StatusManager

	// authenticatedTokenSource is the wrapped token source for remote workloads with authentication monitoring
	authenticatedTokenSource *auth.MonitoredTokenSource

	// monitoringCtx is the context for background authentication monitoring
	// It is cancelled during Cleanup() to stop monitoring
	monitoringCtx    context.Context
	monitoringCancel context.CancelFunc
}

// statusManagerAdapter adapts statuses.StatusManager to auth.StatusUpdater interface
type statusManagerAdapter struct {
	sm statuses.StatusManager
}

func (a *statusManagerAdapter) SetWorkloadStatus(
	ctx context.Context,
	workloadName string,
	status rt.WorkloadStatus,
	reason string,
) error {
	logger.Debugf("Setting workload status: %s, %s, %s", workloadName, status, reason)
	return a.sm.SetWorkloadStatus(ctx, workloadName, status, reason)
}

// NewRunner creates a new Runner with the provided configuration
func NewRunner(runConfig *RunConfig, statusManager statuses.StatusManager) *Runner {
	return &Runner{
		Config:              runConfig,
		statusManager:       statusManager,
		supportedMiddleware: GetSupportedMiddlewareFactories(),
	}
}

// AddMiddleware adds a middleware instance and its function to the runner with a name
func (r *Runner) AddMiddleware(name string, middleware types.Middleware) {
	r.middlewares = append(r.middlewares, middleware)
	r.namedMiddlewares = append(r.namedMiddlewares, types.NamedMiddleware{
		Name:     name,
		Function: middleware.Handler(),
	})
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

// GetName returns the name of the mcp-service from the runner config (implements types.RunnerConfig)
func (c *RunConfig) GetName() string {
	return c.Name
}

// GetPort returns the port from the runner config (implements types.RunnerConfig)
func (c *RunConfig) GetPort() int {
	return c.Port
}

// Run runs the MCP server with the provided configuration
//
//nolint:gocyclo // This function is complex but manageable
func (r *Runner) Run(ctx context.Context) error {
	// Populate default middlewares from old config fields if not already populated
	if len(r.Config.MiddlewareConfigs) == 0 {
		if err := PopulateMiddlewareConfigs(r.Config); err != nil {
			return fmt.Errorf("failed to populate middleware configs: %w", err)
		}
	}

	// Create transport with runtime
	transportConfig := types.Config{
		Type:              r.Config.Transport,
		ProxyPort:         r.Config.Port,
		TargetPort:        r.Config.TargetPort,
		Host:              r.Config.Host,
		TargetHost:        r.Config.TargetHost,
		Deployer:          r.Config.Deployer,
		Debug:             r.Config.Debug,
		TrustProxyHeaders: r.Config.TrustProxyHeaders,
		EndpointPrefix:    r.Config.EndpointPrefix,
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
			return fmt.Errorf("failed to create middleware of type %s: %w", middlewareConfig.Type, err)
		}
	}

	// Set all named middleware and handlers on transport config
	transportConfig.Middlewares = r.namedMiddlewares
	transportConfig.AuthInfoHandler = r.authInfoHandler
	transportConfig.PrometheusHandler = r.prometheusHandler

	// Set proxy mode for stdio transport
	transportConfig.ProxyMode = r.Config.ProxyMode

	// Process secrets if provided (regular secrets or RemoteAuthConfig secrets in CLI format)
	hasRegularSecrets := len(r.Config.Secrets) > 0
	hasRemoteAuthSecret := r.Config.RemoteAuthConfig != nil &&
		(r.Config.RemoteAuthConfig.ClientSecret != "" || r.Config.RemoteAuthConfig.BearerToken != "")

	logger.Debugf("Secret processing check: hasRegularSecrets=%v, hasRemoteAuthSecret=%v", hasRegularSecrets, hasRemoteAuthSecret)
	if hasRemoteAuthSecret {
		if r.Config.RemoteAuthConfig.ClientSecret != "" {
			logger.Debugf("RemoteAuthConfig.ClientSecret: %s", r.Config.RemoteAuthConfig.ClientSecret)
		}
		if r.Config.RemoteAuthConfig.BearerToken != "" {
			logger.Debugf("RemoteAuthConfig.BearerToken: %s", r.Config.RemoteAuthConfig.BearerToken)
		}
	}

	if hasRegularSecrets || hasRemoteAuthSecret {
		logger.Debugf("Calling WithSecrets to process secrets")
		cfgprovider := config.NewDefaultProvider()
		cfg := cfgprovider.GetConfig()

		providerType, err := cfg.Secrets.GetProviderType()
		if err != nil {
			return fmt.Errorf("error determining secrets provider type: %w", err)
		}

		secretManager, err := secrets.CreateSecretProvider(providerType)
		if err != nil {
			return fmt.Errorf("error instantiating secret manager %w", err)
		}

		// Process secrets (including RemoteAuthConfig.ClientSecret and BearerToken resolution)
		if _, err = r.Config.WithSecrets(ctx, secretManager); err != nil {
			return err
		}
	}

	// Set up the transport
	logger.Infof("Setting up %s transport...", r.Config.Transport)

	// Prepare transport options based on workload type
	var transportOpts []transport.Option
	var setupResult *runtime.SetupResult

	if r.Config.RemoteURL == "" {
		// For local workloads, deploy the container using runtime.Setup first
		result, err := runtime.Setup(
			ctx,
			r.Config.Transport,
			r.Config.Deployer,
			r.Config.ContainerName,
			r.Config.Image,
			r.Config.CmdArgs,
			r.Config.EnvVars,
			r.Config.ContainerLabels,
			r.Config.PermissionProfile,
			r.Config.K8sPodTemplatePatch,
			r.Config.IsolateNetwork,
			r.Config.IgnoreConfig,
			r.Config.Host,
			r.Config.TargetPort,
			r.Config.TargetHost,
		)
		if err != nil {
			return fmt.Errorf("failed to set up workload: %w", err)
		}
		setupResult = result

		// Configure the transport with the setup results using options
		transportOpts = append(transportOpts, transport.WithContainerName(setupResult.ContainerName))
		if setupResult.TargetURI != "" {
			transportOpts = append(transportOpts, transport.WithTargetURI(setupResult.TargetURI))
		}
	}

	// Create transport with options
	transportHandler, err := transport.NewFactory().Create(transportConfig, transportOpts...)
	if err != nil {
		return fmt.Errorf("failed to create transport: %w", err)
	}

	// For remote MCP servers, set the remote URL on HTTP transports
	if r.Config.RemoteURL != "" {
		transportHandler.SetRemoteURL(r.Config.RemoteURL)

		// Handle remote authentication if configured
		tokenSource, err := r.handleRemoteAuthentication(ctx)
		if err != nil {
			return fmt.Errorf("failed to authenticate to remote server: %w", err)
		}

		// Wrap the token source with authentication monitoring for remote workloads
		if tokenSource != nil {
			// Create a child context for monitoring that can be cancelled during cleanup
			r.monitoringCtx, r.monitoringCancel = context.WithCancel(ctx)
			// Create adapter to bridge statuses.StatusManager to auth.StatusUpdater
			adapter := &statusManagerAdapter{sm: r.statusManager}
			r.authenticatedTokenSource = auth.NewMonitoredTokenSource(r.monitoringCtx, tokenSource, r.Config.BaseName, adapter)
			tokenSource = r.authenticatedTokenSource
			r.authenticatedTokenSource.StartBackgroundMonitoring()
		}

		// Set the token source on the transport
		transportHandler.SetTokenSource(tokenSource)

		// Set the health check failure callback for remote servers
		transportHandler.SetOnHealthCheckFailed(func() {
			logger.Warnf("Health check failed for remote server %s, marking as unhealthy", r.Config.BaseName)
			// Use Background context for status update callback - this is triggered by health check
			// failure and is independent of any request context. The callback is fired asynchronously
			// and needs its own lifecycle separate from the transport's parent context.
			if err := r.statusManager.SetWorkloadStatus(
				context.Background(),
				r.Config.BaseName,
				rt.WorkloadStatusUnhealthy,
				"Health check failed",
			); err != nil {
				logger.Errorf("Failed to update workload status: %v", err)
			}
		})

		// Set the unauthorized response callback for bearer token authentication
		errorMsg := "Bearer token authentication failed. Please restart the server with a new token"
		transportHandler.SetOnUnauthorizedResponse(func() {
			logger.Warnf("Received 401 Unauthorized response for remote server %s, marking as unauthenticated", r.Config.BaseName)
			// Use Background context for status update callback - this is triggered by 401 response
			// and is independent of any request context. The callback is fired asynchronously
			// and needs its own lifecycle separate from the transport's parent context.
			if err := r.statusManager.SetWorkloadStatus(
				context.Background(),
				r.Config.BaseName,
				rt.WorkloadStatusUnauthenticated,
				errorMsg,
			); err != nil {
				logger.Errorf("Failed to update workload status: %v", err)
			}
		})
	}

	// Start the transport (which also starts the container and monitoring)
	logger.Infof("Starting %s transport for %s...", r.Config.Transport, r.Config.ContainerName)
	if err := transportHandler.Start(ctx); err != nil {
		return fmt.Errorf("failed to start transport: %w", err)
	}

	logger.Infof("MCP server %s started successfully", r.Config.ContainerName)

	// Wait for the MCP server to accept initialize requests before updating client configurations.
	// This prevents timing issues where clients try to connect before the server is fully ready.
	// We repeatedly call initialize until it succeeds (up to 5 minutes).
	// Note: We skip this check for pure STDIO transport because STDIO servers may reject
	// multiple initialize calls (see #1982).
	transportType := labels.GetTransportType(r.Config.ContainerLabels)
	serverURL := transport.GenerateMCPServerURL(
		transportType,
		string(r.Config.ProxyMode),
		"localhost",
		r.Config.Port,
		r.Config.ContainerName,
		r.Config.RemoteURL)

	// Only wait for initialization on non-STDIO transports
	// STDIO servers communicate directly via stdin/stdout and calling initialize multiple times
	// can cause issues as the behavior is not specified by the MCP spec
	if transportType != "stdio" {
		// Repeatedly try calling initialize until it succeeds (up to 5 minutes)
		// Some servers (like mcp-optimizer) can take significant time to start up
		if err := waitForInitializeSuccess(ctx, serverURL, transportType, 5*time.Minute); err != nil {
			logger.Warnf("Warning: Initialize not successful, but continuing: %v", err)
			// Continue anyway to maintain backward compatibility, but log a warning
		}
	} else {
		logger.Debugf("Skipping initialize check for STDIO transport")
	}

	// Update client configurations with the MCP server URL.
	// Note that this function checks the configuration to determine which
	// clients should be updated, if any.
	clientManager, err := client.NewManager(ctx)
	if err != nil {
		logger.Warnf("Warning: Failed to create client manager: %v", err)
	} else {
		if err := clientManager.AddServerToClients(ctx, r.Config.ContainerName, serverURL, transportType, r.Config.Group); err != nil {
			logger.Warnf("Warning: Failed to add server to client configurations: %v", err)
		}
	}

	// Define a function to stop the MCP server
	stopMCPServer := func(reason string) {
		// Use Background context for cleanup operations. The parent context may already be
		// cancelled when this cleanup function runs (e.g., on graceful shutdown or context
		// cancellation). We need a fresh context with its own timeout to ensure cleanup
		// operations complete successfully regardless of the parent context state.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 1*time.Minute)
		defer cleanupCancel()
		logger.Infof("Stopping MCP server: %s", reason)

		// Stop the transport (which also stops the container, monitoring, and handles removal)
		logger.Infof("Stopping %s transport...", r.Config.Transport)
		if err := transportHandler.Stop(cleanupCtx); err != nil {
			logger.Warnf("Warning: Failed to stop transport: %v", err)
		}

		// Cleanup telemetry provider
		if err := r.Cleanup(cleanupCtx); err != nil {
			logger.Warnf("Warning: Failed to cleanup telemetry: %v", err)
		}

		// Remove the PID file if it exists
		// TODO: Stop writing to PID file once we migrate over to statuses.
		if err := process.RemovePIDFile(r.Config.BaseName); err != nil {
			logger.Warnf("Warning: Failed to remove PID file: %v", err)
		}
		if err := r.statusManager.ResetWorkloadPID(cleanupCtx, r.Config.BaseName); err != nil {
			logger.Warnf("Warning: Failed to reset workload %s PID: %v", r.Config.ContainerName, err)
		}

		logger.Infof("MCP server %s stopped", r.Config.ContainerName)
	}

	if err := r.statusManager.SetWorkloadPID(ctx, r.Config.BaseName, os.Getpid()); err != nil {
		logger.Warnf("Warning: Failed to set workload PID: %v", err)
	}

	if process.IsDetached() {
		// We're a detached process running in foreground mode
		// Write the PID to a file so the stop command can kill the process
		logger.Infof("Running as detached process (PID: %d)", os.Getpid())
	} else {
		logger.Info("Press Ctrl+C to stop or wait for container to exit")
	}

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
				logger.Warn("Transport is no longer running, attempting automatic restart...")
				close(doneCh)
				return
			}

			// Sleep for a short time before checking again
			time.Sleep(1 * time.Second)
		}
	}()

	// At this point, we can consider the workload started successfully.
	// However, we should preserve unauthenticated status if it was already set
	// (e.g., if bearer token authentication failed during initialization)
	currentWorkload, err := r.statusManager.GetWorkload(ctx, r.Config.BaseName)
	if err != nil && !errors.Is(err, rt.ErrWorkloadNotFound) {
		logger.Warnf("Failed to get current workload status: %v", err)
	}

	// Only set status to running if it's not already unauthenticated
	// This preserves the unauthenticated state when bearer token authentication fails
	if err == nil && currentWorkload.Status == rt.WorkloadStatusUnauthenticated {
		logger.Debugf("Preserving unauthenticated status for workload %s", r.Config.BaseName)
	} else {
		if err := r.statusManager.SetWorkloadStatus(ctx, r.Config.BaseName, rt.WorkloadStatusRunning, ""); err != nil {
			// If we can't set the status to `running` - treat it as a fatal error.
			return fmt.Errorf("failed to set workload status: %w", err)
		}
	}

	// Wait for either a signal or the done channel to be closed
	select {
	case <-ctx.Done():
		stopMCPServer("Context cancelled")
	case <-doneCh:
		// The transport has already been stopped (likely by the container exit)
		// Clean up the PID file and state
		// TODO: Stop writing to PID file once we migrate over to statuses.
		if err := process.RemovePIDFile(r.Config.BaseName); err != nil {
			logger.Warnf("Warning: Failed to remove PID file: %v", err)
		}
		if err := r.statusManager.ResetWorkloadPID(ctx, r.Config.BaseName); err != nil {
			logger.Warnf("Warning: Failed to reset workload %s PID: %v", r.Config.BaseName, err)
		}

		// Check if workload still exists (using status manager and runtime)
		// If it doesn't exist, it was removed - clean up client config
		// If it exists, it exited unexpectedly - signal restart needed
		exists, checkErr := r.doesWorkloadExist(ctx, r.Config.BaseName)
		if checkErr != nil {
			logger.Warnf("Warning: Failed to check if workload exists: %v", checkErr)
			// Assume restart needed if we can't check
		} else if !exists {
			// Workload doesn't exist in `thv ls` - it was removed
			logger.Infof(
				"Workload %s no longer exists. Removing from client configurations.",
				r.Config.BaseName,
			)
			clientManager, clientErr := client.NewManager(ctx)
			if clientErr == nil {
				removeErr := clientManager.RemoveServerFromClients(
					ctx,
					r.Config.ContainerName,
					r.Config.Group,
				)
				if removeErr != nil {
					logger.Warnf("Warning: Failed to remove from client config: %v", removeErr)
				} else {
					logger.Infof(
						"Successfully removed %s from client configurations",
						r.Config.ContainerName,
					)
				}
			}
			logger.Infof("MCP server %s stopped and cleaned up", r.Config.ContainerName)
			return nil // Exit gracefully, no restart
		}

		// Workload still exists - signal restart needed
		logger.Infof("MCP server %s stopped, restart needed", r.Config.ContainerName)
		return fmt.Errorf("container exited, restart needed")
	}

	return nil
}

// doesWorkloadExist checks if a workload exists in the status manager and runtime.
// For remote workloads, it trusts the status manager.
// For container workloads, it verifies the container exists in the runtime.
func (r *Runner) doesWorkloadExist(ctx context.Context, workloadName string) (bool, error) {
	// Check if workload exists by trying to get it from status manager
	workload, err := r.statusManager.GetWorkload(ctx, workloadName)
	if err != nil {
		if errors.Is(err, rt.ErrWorkloadNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check if workload exists: %w", err)
	}

	// If remote workload, check if it should exist
	if workload.Remote {
		// For remote workloads, trust the status manager
		return workload.Status != rt.WorkloadStatusError, nil
	}

	// For container workloads, verify the container actually exists in the runtime
	// Create a runtime instance to check if container exists
	backend, err := ct.NewFactory().Create(ctx)
	if err != nil {
		logger.Warnf("Failed to create runtime to check container existence: %v", err)
		// Fall back to status manager only
		return workload.Status != rt.WorkloadStatusError, nil
	}

	// Check if container exists in the runtime (not just running)
	// GetWorkloadInfo will return an error if the container doesn't exist
	_, err = backend.GetWorkloadInfo(ctx, workloadName)
	if err != nil {
		// Container doesn't exist
		logger.Debugf("Container %s not found in runtime: %v", workloadName, err)
		return false, nil
	}

	// Container exists (may be running or stopped)
	return true, nil
}

// handleRemoteAuthentication handles authentication for remote MCP servers
func (r *Runner) handleRemoteAuthentication(ctx context.Context) (oauth2.TokenSource, error) {
	if r.Config.RemoteAuthConfig == nil {
		return nil, nil
	}

	// Get the secret manager for token storage
	secretManager, err := authsecrets.GetSecretsManager()
	if err != nil {
		// Secret manager not available - log warning but continue
		// OAuth will work but tokens won't be persisted across restarts
		logger.Warnf("Secret manager not available, OAuth tokens will not be persisted: %v", err)
	}

	// Create remote authentication handler
	authHandler := remote.NewHandler(r.Config.RemoteAuthConfig)

	// Set the secret provider for retrieving cached tokens
	if secretManager != nil {
		authHandler.SetSecretProvider(secretManager)
	}

	// Set up token persister to save tokens across restarts
	if secretManager != nil {
		authHandler.SetTokenPersister(func(refreshToken string, expiry time.Time) error {
			// Generate a unique secret name for this workload's refresh token
			secretName, err := authsecrets.GenerateUniqueSecretNameWithPrefix(
				r.Config.Name,
				"OAUTH_REFRESH_TOKEN_",
				secretManager,
			)
			if err != nil {
				return fmt.Errorf("failed to generate secret name: %w", err)
			}

			// Store the refresh token in the secret manager
			if err := authsecrets.StoreSecretInManagerWithProvider(ctx, secretName, refreshToken, secretManager); err != nil {
				return fmt.Errorf("failed to store refresh token: %w", err)
			}

			// Store the secret reference (not the actual token) in the config
			r.Config.RemoteAuthConfig.CachedRefreshTokenRef = secretName
			r.Config.RemoteAuthConfig.CachedTokenExpiry = expiry

			// Save the updated config to persist the reference
			if err := r.Config.SaveState(ctx); err != nil {
				return fmt.Errorf("failed to save config with token reference: %w", err)
			}

			logger.Debugf("Stored OAuth refresh token in secret manager as %s", secretName)
			return nil
		})
	}

	// Perform authentication
	tokenSource, err := authHandler.Authenticate(ctx, r.Config.RemoteURL)
	if err != nil {
		return nil, fmt.Errorf("remote authentication failed: %w", err)
	}

	return tokenSource, nil
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

	// Stop background authentication monitoring for remote workloads
	// Cancel the monitoring context to stop the background goroutine
	if r.monitoringCancel != nil {
		r.monitoringCancel()
		r.monitoringCancel = nil
	}

	return lastErr
}

// waitForInitializeSuccess repeatedly checks if the MCP server is ready to accept requests.
// This prevents timing issues where clients try to connect before the server is fully ready.
// It makes repeated attempts with exponential backoff up to a maximum timeout.
// Note: This function should not be called for STDIO transport.
func waitForInitializeSuccess(ctx context.Context, serverURL, transportType string, maxWaitTime time.Duration) error {
	// Determine the endpoint and method to use based on transport type
	var endpoint string
	var method string
	var payload string

	switch transportType {
	case "streamable-http", "streamable":
		// For streamable-http, send initialize request to /mcp endpoint
		// Format: http://localhost:port/mcp
		endpoint = serverURL
		method = "POST"
		payload = `{"jsonrpc":"2.0","method":"initialize","id":"toolhive-init-check",` +
			`"params":{"protocolVersion":"2024-11-05","capabilities":{},` +
			`"clientInfo":{"name":"toolhive","version":"1.0"}}}`
	case "sse":
		// For SSE, just check if the SSE endpoint is available
		// We can't easily call initialize without establishing a full SSE connection,
		// so we just verify the endpoint responds.
		// Format: http://localhost:port/sse#container-name -> http://localhost:port/sse
		endpoint = serverURL
		// Remove fragment if present (everything after #)
		if idx := strings.Index(endpoint, "#"); idx != -1 {
			endpoint = endpoint[:idx]
		}
		method = "GET"
		payload = ""
	default:
		// For other transports, no HTTP check is needed
		logger.Debugf("Skipping readiness check for transport type: %s", transportType)
		return nil
	}

	// Setup retry logic with exponential backoff
	startTime := time.Now()
	attempt := 0
	delay := 100 * time.Millisecond
	maxDelay := 2 * time.Second // Cap at 2 seconds between retries

	logger.Infof("Waiting for MCP server to be ready at %s (timeout: %v)", endpoint, maxWaitTime)

	// Create HTTP client with a reasonable timeout for requests
	httpClient := &http.Client{
		Timeout: 10 * time.Second,
	}

	for {
		attempt++

		// Make the readiness check request
		var req *http.Request
		var err error
		if payload != "" {
			req, err = http.NewRequestWithContext(ctx, method, endpoint, bytes.NewBufferString(payload))
		} else {
			req, err = http.NewRequestWithContext(ctx, method, endpoint, nil)
		}

		if err != nil {
			logger.Debugf("Failed to create request (attempt %d): %v", attempt, err)
		} else {
			if method == "POST" {
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json, text/event-stream")
				req.Header.Set("MCP-Protocol-Version", "2024-11-05")
			}

			resp, err := httpClient.Do(req)
			if err == nil {
				//nolint:errcheck // Ignoring close error on response body in error path
				defer resp.Body.Close()

				// For GET (SSE), accept 200 OK
				// For POST (streamable-http), also accept 200 OK
				if resp.StatusCode == http.StatusOK {
					elapsed := time.Since(startTime)
					logger.Infof("MCP server is ready after %v (attempt %d)", elapsed, attempt)
					return nil
				}

				logger.Debugf("Server returned status %d (attempt %d)", resp.StatusCode, attempt)
			} else {
				logger.Debugf("Failed to reach endpoint (attempt %d): %v", attempt, err)
			}
		}

		// Check if we've exceeded the maximum wait time
		elapsed := time.Since(startTime)
		if elapsed >= maxWaitTime {
			return fmt.Errorf("initialize not successful after %v (%d attempts)", elapsed, attempt)
		}

		// Wait before retrying
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while waiting for initialize")
		case <-time.After(delay):
			// Continue to next attempt
		}

		// Update delay for next iteration with exponential backoff
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
}
