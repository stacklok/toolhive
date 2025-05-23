// Package runner provides functionality for running MCP servers
package runner

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/network"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/config"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	lb "github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/process"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// Runner is responsible for running an MCP server with the provided configuration
type Runner struct {
	// Config is the configuration for the runner
	Config *RunConfig

	// Mutex for protecting shared state
	mutex sync.Mutex
}

// NewRunner creates a new Runner with the provided configuration
func NewRunner(runConfig *RunConfig) *Runner {
	return &Runner{
		Config: runConfig,
	}
}

// Starts an egress container for the MCP server
func (r *Runner) setupEgressContainer(ctx context.Context, containerName string, egressImage string) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	// Create container options
	containerOptions := rt.NewCreateContainerOptions()

	// container name is name of container + "-egress"
	egressContainerName := fmt.Sprintf("%s-egress", containerName)

	cmdArgs := []string{}
	envVars := map[string]string{}

	labels := map[string]string{}
	lb.AddStandardLabels(labels, egressContainerName, egressContainerName, "stdio", 80)

	// Create the container
	internalNetworkName := fmt.Sprintf("toolhive-%s-internal", containerName)
	networking := map[string]*network.EndpointSettings{
		"toolhive-external": {},
		internalNetworkName: {},
	}
	logger.Infof("Creating container %s from image %s...", egressContainerName, egressImage)
	containerID, err := r.Config.Runtime.CreateContainer(
		ctx,
		egressImage,
		egressContainerName,
		cmdArgs,
		envVars,
		labels,
		permissions.BuiltinEgressProfile(),
		"stdio",
		networking,
		containerOptions,
	)
	if err != nil {
		return fmt.Errorf("failed to create container: %v", err)
	}
	logger.Infof("Container created with ID: %s", containerID)

	return nil
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
		Host:       r.Config.Host,
		TargetHost: r.Config.TargetHost,
		Runtime:    r.Config.Runtime,
		Debug:      r.Config.Debug,
	}

	// Add OIDC middleware if OIDC validation is enabled
	if r.Config.OIDCConfig != nil {
		logger.Info("OIDC validation enabled for transport")

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
		logger.Info("Authorization enabled for transport")

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

	// Save the configuration to the state store
	if err := r.SaveState(ctx); err != nil {
		logger.Warnf("Warning: Failed to save run configuration: %v", err)
	}

	// Process secrets if provided
	// NOTE: This MUST happen after we save the run config to avoid storing
	// the secrets in the state store.
	if len(r.Config.Secrets) > 0 {
		providerType, err := config.GetConfig().Secrets.GetProviderType()
		if err != nil {
			return fmt.Errorf("error determining secrets provider type: %w", err)
		}

		secretManager, err := secrets.CreateSecretProvider(providerType)
		if err != nil {
			return fmt.Errorf("error instantiating secret manager %v", err)
		}

		// Process secrets
		if _, err = r.Config.WithSecrets(secretManager); err != nil {
			return err
		}
	}

	// Create networks if they do not exist
	internalNetworkLabels := map[string]string{}
	networkName := "toolhive-" + r.Config.ContainerName + "-internal"
	lb.AddNetworkLabels(internalNetworkLabels, networkName)
	_, err = r.Config.Runtime.CreateNetwork(ctx, networkName, internalNetworkLabels, true)
	if err != nil {
		return fmt.Errorf("failed to create internal network: %v", err)
	}

	externalNetworkLabels := map[string]string{}
	lb.AddNetworkLabels(externalNetworkLabels, "toolhive-external")
	_, err = r.Config.Runtime.CreateNetwork(ctx, "toolhive-external", externalNetworkLabels, false)
	if err != nil {
		// just log the error and continue
		logger.Warnf("failed to create external network %q: %v", "toolhive-external", err)
	}

	// spin up the egress container
	fmt.Println("i setup egress container")
	if err := r.setupEgressContainer(ctx, r.Config.ContainerName, r.Config.EgressImage); err != nil {
		return fmt.Errorf("failed to set up egress container: %v", err)
	}
	fmt.Println("after")

	// add extra env vars
	egressHost := fmt.Sprintf("http://%s:3128", r.Config.ContainerName+"-egress")
	r.Config.EnvVars["HTTP_PROXY"] = egressHost
	r.Config.EnvVars["HTTPS_PROXY"] = egressHost
	r.Config.EnvVars["NO_PROXY"] = fmt.Sprintf("localhost,127.0.1,::1,%s", r.Config.Host)
	fmt.Println("In en vars")
	fmt.Println(r.Config.EnvVars)

	// Set up the transport
	logger.Infof("Setting up %s transport...", r.Config.Transport)
	if err := transportHandler.Setup(
		ctx, r.Config.Runtime, r.Config.ContainerName, r.Config.Image, r.Config.CmdArgs,
		r.Config.EnvVars, r.Config.ContainerLabels, r.Config.PermissionProfile, r.Config.K8sPodTemplatePatch,
	); err != nil {
		return fmt.Errorf("failed to set up transport: %v", err)
	}

	// Start the transport (which also starts the container and monitoring)
	logger.Infof("Starting %s transport...", r.Config.Transport)
	if err := transportHandler.Start(ctx); err != nil {
		return fmt.Errorf("failed to start transport: %v", err)
	}

	logger.Infof("MCP server %s started successfully", r.Config.ContainerName)

	// Update client configurations with the MCP server URL.
	// Note that this function checks the configuration to determine which
	// clients should be updated, if any.
	if err := updateClientConfigurations(r.Config.ContainerName, "localhost", r.Config.Port); err != nil {
		logger.Warnf("Warning: Failed to update client configurations: %v", err)
	}

	// Define a function to stop the MCP server
	stopMCPServer := func(reason string) {
		logger.Infof("Stopping MCP server: %s", reason)

		// Stop the transport (which also stops the container, monitoring, and handles removal)
		logger.Infof("Stopping %s transport...", r.Config.Transport)
		if err := transportHandler.Stop(ctx); err != nil {
			logger.Warnf("Warning: Failed to stop transport: %v", err)
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

// updateClientConfigurations updates client configuration files with the MCP server URL
func updateClientConfigurations(containerName, host string, port int) error {
	// Find client configuration files
	clientConfigs, err := client.FindClientConfigs()
	if err != nil {
		return fmt.Errorf("failed to find client configurations: %w", err)
	}

	if len(clientConfigs) == 0 {
		logger.Infof("No client configuration files found")
		return nil
	}

	// Generate the URL for the MCP server
	url := client.GenerateMCPServerURL(host, port, containerName)

	// Update each configuration file
	for _, clientConfig := range clientConfigs {
		logger.Infof("Updating client configuration: %s", clientConfig.Path)

		if err := client.Upsert(clientConfig, containerName, url); err != nil {
			fmt.Printf("Warning: Failed to update MCP server configuration in %s: %v\n", clientConfig.Path, err)
			continue
		}

		logger.Infof("Successfully updated client configuration: %s", clientConfig.Path)
	}

	return nil
}
