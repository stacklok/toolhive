// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package adapter provides a layer between aggregator and SDK.
//
// The HandlerFactory interface and its default implementation create MCP request
// handlers that route to backend workloads, bridging the gap between the MCP SDK
package adapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
)

//go:generate mockgen -destination=mocks/mock_handler_factory.go -package=mocks github.com/stacklok/toolhive/pkg/vmcp/server/adapter HandlerFactory

// HandlerFactory creates handlers that route MCP requests to backends.
type HandlerFactory interface {
	// CreateToolHandler creates a handler that routes tool calls to backends.
	CreateToolHandler(toolName string) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)

	// CreateResourceHandler creates a handler that routes resource reads to backends.
	CreateResourceHandler(uri string) func(context.Context, mcp.ReadResourceRequest) ([]mcp.ResourceContents, error)

	// CreatePromptHandler creates a handler that routes prompt requests to backends.
	CreatePromptHandler(promptName string) func(context.Context, mcp.GetPromptRequest) (*mcp.GetPromptResult, error)

	// CreateCompositeToolHandler creates a handler for composite tool workflows.
	// This handler executes multi-step workflows via the composer instead of routing to a single backend.
	CreateCompositeToolHandler(
		toolName string,
		workflow WorkflowExecutor,
	) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
}

// WorkflowExecutor executes composite tool workflows.
// This interface abstracts the composer to enable testing without full composer setup.
type WorkflowExecutor interface {
	// ExecuteWorkflow executes the workflow with the given parameters.
	ExecuteWorkflow(ctx context.Context, params map[string]any) (*WorkflowResult, error)
}

// WorkflowResult represents the result of a workflow execution.
type WorkflowResult struct {
	// Output contains the workflow output data (typically from the last step).
	Output map[string]any

	// Error contains error information if the workflow failed.
	Error error
}

// DefaultHandlerFactory creates MCP request handlers that route to backend workloads.
type DefaultHandlerFactory struct {
	router        router.Router
	backendClient vmcp.BackendClient
}

// NewDefaultHandlerFactory creates a new default handler factory.
func NewDefaultHandlerFactory(rt router.Router, backendClient vmcp.BackendClient) *DefaultHandlerFactory {
	return &DefaultHandlerFactory{
		router:        rt,
		backendClient: backendClient,
	}
}

// convertToMCPMeta converts vmcp Meta (map[string]any) to mcp.Meta.
// This forwards the _meta field from backend responses to MCP clients.
func convertToMCPMeta(meta map[string]any) *mcp.Meta {
	if len(meta) == 0 {
		return nil
	}

	result := &mcp.Meta{
		AdditionalFields: make(map[string]any),
	}

	for k, v := range meta {
		if k == "progressToken" {
			result.ProgressToken = v
		} else {
			result.AdditionalFields[k] = v
		}
	}

	return result
}

// convertToMCPContent converts vmcp.Content to mcp.Content.
// This reconstructs MCP content from the vmcp wrapper type.
func convertToMCPContent(content vmcp.Content) mcp.Content {
	switch content.Type {
	case "text":
		return mcp.NewTextContent(content.Text)
	case "image":
		return mcp.NewImageContent(content.Data, content.MimeType)
	case "resource":
		// Handle embedded resources if needed
		// For now, convert to text
		return mcp.NewTextContent("")
	default:
		return mcp.NewTextContent("")
	}
}

// CreateToolHandler creates a tool handler that routes to the appropriate backend.
func (f *DefaultHandlerFactory) CreateToolHandler(
	toolName string,
) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		logger.Debugf("Handling tool call: %s", toolName)

		target, err := f.router.RouteTool(ctx, toolName)
		if err != nil {
			if errors.Is(err, router.ErrToolNotFound) {
				wrappedErr := fmt.Errorf("%w: tool %s", vmcp.ErrNotFound, toolName)
				logger.Warnf("Routing failed: %v", wrappedErr)
				return mcp.NewToolResultError(wrappedErr.Error()), nil
			}
			logger.Warnf("Failed to route tool %s: %v", toolName, err)
			return mcp.NewToolResultError(fmt.Sprintf("Routing error: %v", err)), nil
		}

		args, ok := request.Params.Arguments.(map[string]any)
		if !ok {
			wrappedErr := fmt.Errorf("%w: arguments must be object, got %T", vmcp.ErrInvalidInput, request.Params.Arguments)
			logger.Warnf("Invalid arguments for tool %s: %v", toolName, wrappedErr)
			return mcp.NewToolResultError(wrappedErr.Error()), nil
		}

		// Call the backend tool - the backend client handles name translation
		result, err := f.backendClient.CallTool(ctx, target, toolName, args)
		if err != nil {
			if errors.Is(err, vmcp.ErrToolExecutionFailed) {
				logger.Debugf("Tool execution failed for %s: %v", toolName, err)
				return mcp.NewToolResultError(err.Error()), nil
			}
			if errors.Is(err, vmcp.ErrBackendUnavailable) {
				logger.Warnf("Backend unavailable for tool %s: %v", toolName, err)
				return mcp.NewToolResultError(fmt.Sprintf("Backend unavailable: %v", err)), nil
			}
			logger.Warnf("Backend tool call failed for %s: %v", toolName, err)
			return mcp.NewToolResultError(fmt.Sprintf("Tool call failed: %v", err)), nil
		}

		// Convert vmcp.Content array to MCP content array
		mcpContent := make([]mcp.Content, len(result.Content))
		for i, content := range result.Content {
			mcpContent[i] = convertToMCPContent(content)
		}

		// Create MCP tool result with _meta field preserved
		mcpResult := &mcp.CallToolResult{
			Result: mcp.Result{
				Meta: convertToMCPMeta(result.Meta),
			},
			Content:           mcpContent,
			StructuredContent: result.StructuredContent,
			IsError:           result.IsError,
		}

		return mcpResult, nil
	}
}

// CreateResourceHandler creates a resource handler that routes to the appropriate backend.
func (f *DefaultHandlerFactory) CreateResourceHandler(uri string) func(
	context.Context, mcp.ReadResourceRequest,
) ([]mcp.ResourceContents, error) {
	return func(ctx context.Context, _ mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		logger.Debugf("Handling resource read: %s", uri)

		caps, ok := discovery.DiscoveredCapabilitiesFromContext(ctx)
		if !ok {
			logger.Warn("Capabilities not discovered in context")
			return nil, fmt.Errorf("capabilities not discovered")
		}

		target, err := f.router.RouteResource(ctx, uri)
		if err != nil {
			if errors.Is(err, router.ErrResourceNotFound) {
				wrappedErr := fmt.Errorf("%w: resource %s", vmcp.ErrNotFound, uri)
				logger.Warnf("Routing failed: %v", wrappedErr)
				return nil, wrappedErr
			}
			logger.Warnf("Failed to route resource %s: %v", uri, err)
			return nil, fmt.Errorf("routing error: %w", err)
		}

		backendURI := target.GetBackendCapabilityName(uri)

		result, err := f.backendClient.ReadResource(ctx, target, backendURI)
		if err != nil {
			if errors.Is(err, vmcp.ErrBackendUnavailable) {
				logger.Warnf("Backend unavailable for resource %s: %v", uri, err)
				return nil, fmt.Errorf("backend unavailable: %w", err)
			}
			logger.Warnf("Backend resource read failed for %s: %v", uri, err)
			return nil, fmt.Errorf("resource read failed: %w", err)
		}

		// Use the MimeType from the result if available, otherwise fall back to discovery
		mimeType := result.MimeType
		if mimeType == "" {
			mimeType = "application/octet-stream" // Default for unknown resources
			for _, res := range caps.Resources {
				if res.URI == uri && res.MimeType != "" {
					mimeType = res.MimeType
					break
				}
			}
		}

		// Note: MCP SDK limitation - resources/read handlers return []ResourceContents directly,
		// not a result wrapper with _meta field. The SDK handler signature does not support
		// forwarding _meta for resources. result.Meta is preserved in the vmcp layer but
		// cannot be forwarded to clients due to SDK constraints.
		contents := []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:      uri,
				MIMEType: mimeType,
				Text:     string(result.Contents),
			},
		}

		return contents, nil
	}
}

// CreatePromptHandler creates a prompt handler that routes to the appropriate backend.
func (f *DefaultHandlerFactory) CreatePromptHandler(promptName string) func(
	context.Context, mcp.GetPromptRequest,
) (*mcp.GetPromptResult, error) {
	return func(ctx context.Context, request mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		logger.Debugf("Handling prompt request: %s", promptName)

		// Route to backend
		target, err := f.router.RoutePrompt(ctx, promptName)
		if err != nil {
			if errors.Is(err, router.ErrPromptNotFound) {
				wrappedErr := fmt.Errorf("%w: prompt %s", vmcp.ErrNotFound, promptName)
				logger.Warnf("Routing failed: %v", wrappedErr)
				return nil, wrappedErr
			}
			logger.Warnf("Failed to route prompt %s: %v", promptName, err)
			return nil, fmt.Errorf("routing error: %w", err)
		}

		args := make(map[string]any)
		for k, v := range request.Params.Arguments {
			args[k] = v
		}

		// Get the name to use when calling the backend (handles conflict resolution renaming)
		backendPromptName := target.GetBackendCapabilityName(promptName)

		// Forward request to backend
		result, err := f.backendClient.GetPrompt(ctx, target, backendPromptName, args)
		if err != nil {
			if errors.Is(err, vmcp.ErrBackendUnavailable) {
				logger.Warnf("Backend unavailable for prompt %s: %v", promptName, err)
				return nil, fmt.Errorf("backend unavailable: %w", err)
			}
			logger.Warnf("Backend prompt request failed for %s: %v", promptName, err)
			return nil, fmt.Errorf("prompt request failed: %w", err)
		}

		// Use description from backend result if available
		description := result.Description
		if description == "" {
			description = fmt.Sprintf("Prompt: %s", promptName)
		}

		// Create MCP prompt result with _meta field preserved
		mcpResult := &mcp.GetPromptResult{
			Result: mcp.Result{
				Meta: convertToMCPMeta(result.Meta),
			},
			Description: description,
			Messages: []mcp.PromptMessage{
				{
					Role:    "assistant",
					Content: mcp.NewTextContent(result.Messages),
				},
			},
		}

		return mcpResult, nil
	}
}

// CreateCompositeToolHandler creates a handler that executes composite tool workflows.
//
// This handler differs from backend tool handlers in that it executes multi-step
// workflows via the composer instead of routing to a single backend. The workflow
// orchestrates calls to multiple backend tools and handles elicitation, conditions,
// and error handling.
//
// The handler:
//  1. Extracts parameters from the MCP request
//  2. Invokes the workflow executor
//  3. Converts workflow results to MCP tool result format
//  4. Handles workflow errors gracefully
//
// Workflow execution errors are returned as MCP tool errors (not HTTP errors),
// ensuring consistent error handling across all tool types.
func (*DefaultHandlerFactory) CreateCompositeToolHandler(
	toolName string,
	workflow WorkflowExecutor,
) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		logger.Debugf("Handling composite tool call: %s", toolName)

		// Extract parameters from MCP request
		params, ok := request.Params.Arguments.(map[string]any)
		if !ok {
			wrappedErr := fmt.Errorf("%w: arguments must be object, got %T", vmcp.ErrInvalidInput, request.Params.Arguments)
			logger.Warnf("Invalid arguments for composite tool %s: %v", toolName, wrappedErr)
			return mcp.NewToolResultError(wrappedErr.Error()), nil
		}

		// Execute workflow via composer
		// The workflow engine applies timeout from WorkflowDefinition.Timeout (default: 30 minutes)
		// and handles context cancellation throughout execution.
		result, err := workflow.ExecuteWorkflow(ctx, params)
		if err != nil {
			// Check for timeout errors and provide user-friendly message
			if errors.Is(err, context.DeadlineExceeded) {
				logger.Warnf("Workflow execution timeout for %s: %v", toolName, err)
				return mcp.NewToolResultError("Workflow execution timeout exceeded"), nil
			}
			logger.Errorf("Workflow execution failed for %s: %v", toolName, err)
			return mcp.NewToolResultError(fmt.Sprintf("Workflow execution failed: %v", err)), nil
		}

		// Check if workflow result contains an error
		if result.Error != nil {
			logger.Errorf("Workflow completed with error for %s: %v", toolName, result.Error)
			return mcp.NewToolResultError(fmt.Sprintf("Workflow error: %v", result.Error)), nil
		}

		// Convert workflow output to MCP tool result
		// The output is typically the result of the last workflow step
		logger.Debugf("Composite tool %s completed successfully", toolName)
		return mcp.NewToolResultStructuredOnly(result.Output), nil
	}
}
