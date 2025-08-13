// Package server provides the MCP (Model Context Protocol) server implementation for ToolHive.
package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// Handler handles MCP tool requests for ToolHive
type Handler struct {
	ctx              context.Context
	workloadManager  workloads.Manager
	registryProvider registry.Provider
}

// NewHandler creates a new ToolHive handler
func NewHandler(ctx context.Context) (*Handler, error) {
	// Create workload manager
	workloadManager, err := workloads.NewManager(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create workload manager: %w", err)
	}

	// Create registry provider
	registryProvider, err := registry.GetDefaultProvider()
	if err != nil {
		return nil, fmt.Errorf("failed to get registry provider: %w", err)
	}

	return &Handler{
		ctx:              ctx,
		workloadManager:  workloadManager,
		registryProvider: registryProvider,
	}, nil
}

// SearchRegistry searches the ToolHive registry
func (h *Handler) SearchRegistry(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Parse arguments using BindArguments
	args := struct {
		Query string `json:"query"`
	}{}

	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to parse arguments: %v", err)), nil
	}

	// Search the registry
	servers, err := h.registryProvider.SearchServers(args.Query)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to search registry: %v", err)), nil
	}

	// Format results with all available information
	type ServerInfo struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Transport   string   `json:"transport"`
		Image       string   `json:"image,omitempty"`
		Args        []string `json:"args,omitempty"`
		Tools       []string `json:"tools,omitempty"`
		Tags        []string `json:"tags,omitempty"`
	}

	var results []ServerInfo
	for _, srv := range servers {
		info := ServerInfo{
			Name:        srv.GetName(),
			Description: srv.GetDescription(),
			Transport:   srv.GetTransport(),
		}

		// Add image-specific fields if it's an ImageMetadata
		if imgMeta, ok := srv.(*registry.ImageMetadata); ok {
			info.Image = imgMeta.Image
			info.Args = imgMeta.Args
			info.Tools = imgMeta.Tools
			info.Tags = imgMeta.Tags
		}

		results = append(results, info)
	}

	// Use StructuredOnly to get JSON serialization automatically
	return mcp.NewToolResultStructuredOnly(results), nil
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
	imageURL, imageMetadata, err := retriever.GetMCPServer(ctx, args.Server, "", "disabled")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to get MCP server: %v", err)), nil
	}

	// Build run configuration
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

// ListServers lists all running MCP servers
func (h *Handler) ListServers(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// List all workloads (including stopped ones)
	wklds, err := h.workloadManager.ListWorkloads(ctx, true)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to list workloads: %v", err)), nil
	}

	// Format results with structured data
	type WorkloadInfo struct {
		Name      string `json:"name"`
		Server    string `json:"server,omitempty"`
		Status    string `json:"status"`
		CreatedAt string `json:"created_at"`
		URL       string `json:"url,omitempty"`
	}

	var results []WorkloadInfo
	for _, workload := range wklds {
		info := WorkloadInfo{
			Name:      workload.Name,
			Status:    string(workload.Status),
			CreatedAt: workload.CreatedAt.Format("2006-01-02 15:04:05"),
		}

		// Add server name from labels if available
		if serverName, ok := workload.Labels["toolhive.server"]; ok {
			info.Server = serverName
		}

		// Add URL if port is available
		if workload.Port > 0 {
			info.URL = fmt.Sprintf("http://localhost:%d", workload.Port)
		}

		results = append(results, info)
	}

	return mcp.NewToolResultStructuredOnly(results), nil
}

// StopServer stops a running MCP server
func (h *Handler) StopServer(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Parse arguments using BindArguments
	args := struct {
		Name string `json:"name"`
	}{}

	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to parse arguments: %v", err)), nil
	}

	// Stop the workload
	group, err := h.workloadManager.StopWorkloads(ctx, []string{args.Name})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to stop server: %v", err)), nil
	}

	// Wait for the stop operation to complete
	if err := group.Wait(); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to stop server: %v", err)), nil
	}

	result := map[string]interface{}{
		"status": "stopped",
		"name":   args.Name,
	}

	return mcp.NewToolResultStructuredOnly(result), nil
}

// RemoveServer removes a stopped MCP server
func (h *Handler) RemoveServer(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Parse arguments using BindArguments
	args := struct {
		Name string `json:"name"`
	}{}

	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to parse arguments: %v", err)), nil
	}

	// Delete the workload
	group, err := h.workloadManager.DeleteWorkloads(ctx, []string{args.Name})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to remove server: %v", err)), nil
	}

	// Wait for the delete operation to complete
	if err := group.Wait(); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to remove server: %v", err)), nil
	}

	result := map[string]interface{}{
		"status": "removed",
		"name":   args.Name,
	}

	return mcp.NewToolResultStructuredOnly(result), nil
}

// GetServerLogs gets logs from a running MCP server
func (h *Handler) GetServerLogs(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Parse arguments using BindArguments
	args := struct {
		Name string `json:"name"`
	}{}

	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to parse arguments: %v", err)), nil
	}

	// Get logs
	logs, err := h.workloadManager.GetLogs(ctx, args.Name, false)
	if err != nil {
		// Check if it's a not found error
		if strings.Contains(err.Error(), "not found") {
			return mcp.NewToolResultError(fmt.Sprintf("Server '%s' not found", args.Name)), nil
		}
		return mcp.NewToolResultError(fmt.Sprintf("Failed to get server logs: %v", err)), nil
	}

	return mcp.NewToolResultText(logs), nil
}

// Helper types and functions

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
func (h *Handler) saveAndRunServer(ctx context.Context, runConfig *runner.RunConfig, name string) error {
	// Save the run configuration state before starting
	if err := runConfig.SaveState(ctx); err != nil {
		logger.Warnf("Failed to save run configuration for %s: %v", name, err)
		// Continue anyway, as this is not critical for running
	}

	// Run the workload in detached mode
	return h.workloadManager.RunWorkloadDetached(ctx, runConfig)
}
