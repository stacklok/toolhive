package app

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
)

// runServerArgs holds the arguments for running a server
type runServerArgs struct {
	Server string            `json:"server"`
	Name   string            `json:"name,omitempty"`
	Host   string            `json:"host,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
}

// parseRunServerArgs parses and validates the arguments for runServer
func parseRunServerArgs(request mcp.CallToolRequest) (*runServerArgs, error) {
	args := &runServerArgs{}
	if err := request.BindArguments(args); err != nil {
		return nil, err
	}

	// Use custom name if provided, otherwise use server name
	if args.Name == "" {
		args.Name = args.Server
	}

	// Use default host if not provided
	if args.Host == "" {
		args.Host = "127.0.0.1"
	}

	return args, nil
}

// buildServerConfig creates the run configuration for the server
func buildServerConfig(
	ctx context.Context,
	args *runServerArgs,
	imageURL string,
	imageMetadata *registry.ImageMetadata,
) (*runner.RunConfig, error) {
	// Create container runtime
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create container runtime: %w", err)
	}

	// Build configuration using the builder pattern
	builder := runner.NewRunConfigBuilder().
		WithRuntime(rt).
		WithImage(imageURL).
		WithName(args.Name).
		WithHost(args.Host)

	// Configure transport and metadata
	transport := configureTransport(builder, imageMetadata)
	builder = builder.WithTransportAndPorts(transport, 0, 0)

	// Prepare environment variables
	envVars := prepareEnvironmentVariables(imageMetadata, args.Env)

	// Build the configuration
	envVarValidator := &runner.DetachedEnvVarValidator{}
	return builder.Build(ctx, imageMetadata, envVars, envVarValidator)
}

// configureTransport sets up transport configuration from metadata
func configureTransport(builder *runner.RunConfigBuilder, imageMetadata *registry.ImageMetadata) string {
	transport := transporttypes.TransportTypeSSE.String()

	if imageMetadata != nil {
		if imageMetadata.Transport != "" {
			transport = imageMetadata.Transport
		}
		builder.WithCmdArgs(imageMetadata.Args)
	}

	return transport
}

// prepareEnvironmentVariables merges default and user environment variables
func prepareEnvironmentVariables(imageMetadata *registry.ImageMetadata, userEnv map[string]string) map[string]string {
	envVarsMap := make(map[string]string)

	// Add default environment variables from metadata
	if imageMetadata != nil && imageMetadata.EnvVars != nil {
		for _, envVar := range imageMetadata.EnvVars {
			if envVar.Default != "" {
				envVarsMap[envVar.Name] = envVar.Default
			}
		}
	}

	// Override with user-provided environment variables
	for k, v := range userEnv {
		envVarsMap[k] = v
	}

	return envVarsMap
}

// saveAndRunServer saves the configuration and runs the server
func (h *toolHiveHandler) saveAndRunServer(ctx context.Context, runConfig *runner.RunConfig, name string) error {
	// Save the run configuration state before starting
	if err := runConfig.SaveState(ctx); err != nil {
		logger.Warnf("Failed to save run configuration for %s: %v", name, err)
		// Continue anyway, as this is not critical for running
	}

	// Run the workload in detached mode
	return h.workloadManager.RunWorkloadDetached(ctx, runConfig)
}

// getServerResult builds the result object with server information
func (h *toolHiveHandler) getServerResult(ctx context.Context, args *runServerArgs) map[string]interface{} {
	result := map[string]interface{}{
		"status": "running",
		"name":   args.Name,
		"server": args.Server,
	}

	// Try to get workload info for port and URL
	workload, err := h.workloadManager.GetWorkload(ctx, args.Name)
	if err == nil && workload.Port > 0 {
		result["port"] = workload.Port
		result["url"] = fmt.Sprintf("http://localhost:%d", workload.Port)
	}

	return result
}
