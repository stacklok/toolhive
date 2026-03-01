// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/mark3labs/mcp-go/mcp"

	types "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
)

// SecretMapping represents a secret name and its target environment variable.
// Note: Description is not included because it's only relevant for listing/discovery
// (see SecretInfo). When mapping secrets to a running server, only the name and target
// environment variable are needed.
type SecretMapping struct {
	Name   string `json:"name"`
	Target string `json:"target"`
}

// runServerArgs holds the arguments for running a server
type runServerArgs struct {
	Server  string            `json:"server"`
	Name    string            `json:"name,omitempty"`
	Host    string            `json:"host,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Secrets []SecretMapping   `json:"secrets,omitempty"`
}

// RunServer runs an MCP server
func (h *Handler) RunServer(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Parse and validate arguments
	args, err := parseRunServerArgs(request)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to parse arguments: %v", err)), nil
	}

	// Use retriever to properly fetch and prepare the MCP server
	// TODO: make this configurable so we could warn or even fail
	imageURL, serverMetadata, err := retriever.GetMCPServer(ctx, args.Server, "", "disabled", "", nil)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to get MCP server: %v", err)), nil
	}

	// Build run configuration.
	// Use type assertion with nil check to guard against typed nil pointers.
	var imageMetadata *types.ImageMetadata
	if md, ok := serverMetadata.(*types.ImageMetadata); ok && md != nil {
		imageMetadata = md
	}

	runConfig, err := buildServerConfig(ctx, args, imageURL, imageMetadata)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to build run configuration: %v", err)), nil
	}

	// Save and run the server
	if err := h.saveAndRunServer(ctx, runConfig, args.Name); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to run server: %v", err)), nil
	}

	// Get the actual workload status
	workload, err := h.workloadManager.GetWorkload(ctx, args.Name)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to get server status: %v", err)), nil
	}

	// Build result with actual status
	result := map[string]interface{}{
		"status": string(workload.Status),
		"name":   args.Name,
		"server": args.Server,
	}

	// Add port and URL if available
	if workload.Port > 0 {
		result["port"] = workload.Port
		result["url"] = fmt.Sprintf("http://localhost:%d", workload.Port)
	}

	return mcp.NewToolResultStructuredOnly(result), nil
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
	imageMetadata *types.ImageMetadata,
) (*runner.RunConfig, error) {
	// Create container runtime
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create container runtime: %w", err)
	}

	opts := []runner.RunConfigBuilderOption{
		runner.WithRuntime(rt),
		runner.WithImage(imageURL),
		runner.WithName(args.Name),
		runner.WithHost(args.Host),
	}

	// Configure transport and metadata
	transport := configureTransport(&opts, imageMetadata)
	opts = append(opts, runner.WithTransportAndPorts(transport, 0, 0))

	// Prepare environment variables
	envVars := prepareEnvironmentVariables(imageMetadata, args.Env)

	// Prepare secrets
	secrets := prepareSecrets(args.Secrets)
	if len(secrets) > 0 {
		opts = append(opts, runner.WithSecrets(secrets))
	}

	// Build the configuration
	envVarValidator := &runner.DetachedEnvVarValidator{}
	return runner.NewRunConfigBuilder(ctx, imageMetadata, envVars, envVarValidator, opts...)
}

// configureTransport sets up transport configuration from metadata
func configureTransport(opts *[]runner.RunConfigBuilderOption, imageMetadata *types.ImageMetadata) string {
	transport := transporttypes.TransportTypeSSE.String()

	if imageMetadata != nil {
		if imageMetadata.Transport != "" {
			transport = imageMetadata.Transport
		}
		*opts = append(*opts, runner.WithCmdArgs(imageMetadata.Args))
	}

	return transport
}

// prepareEnvironmentVariables merges default and user environment variables
func prepareEnvironmentVariables(imageMetadata *types.ImageMetadata, userEnv map[string]string) map[string]string {
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

// prepareSecrets converts SecretMapping array to the string format expected by the runner
func prepareSecrets(secretMappings []SecretMapping) []string {
	if len(secretMappings) == 0 {
		return nil
	}

	secrets := make([]string, len(secretMappings))
	for i, mapping := range secretMappings {
		// Convert to the format expected by runner: "secret_name,target=ENV_VAR_NAME"
		secrets[i] = fmt.Sprintf("%s,target=%s", mapping.Name, mapping.Target)
	}

	return secrets
}

// saveAndRunServer saves the configuration and runs the server
func (h *Handler) saveAndRunServer(ctx context.Context, runConfig *runner.RunConfig, name string) error {
	// Save the run configuration state before starting
	if err := runConfig.SaveState(ctx); err != nil {
		//nolint:gosec // G706: server name from function parameter
		slog.Warn("failed to save run configuration",
			"name", name, "error", err)
		// Continue anyway, as this is not critical for running
	}

	// Run the workload in detached mode
	return h.workloadManager.RunWorkloadDetached(ctx, runConfig)
}
