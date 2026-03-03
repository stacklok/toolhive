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
	"log/slog"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
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

// convertToMCPContent converts vmcp.Content to mcp.Content.
// This reconstructs MCP content from the vmcp wrapper type.
func convertToMCPContent(content vmcp.Content) mcp.Content {
	switch content.Type {
	case "text":
		return mcp.NewTextContent(content.Text)
	case "image":
		return mcp.NewImageContent(content.Data, content.MimeType)
	case "audio":
		return mcp.NewAudioContent(content.Data, content.MimeType)
	case "resource":
		if content.Text != "" {
			return mcp.NewEmbeddedResource(mcp.TextResourceContents{
				URI:      content.URI,
				MIMEType: content.MimeType,
				Text:     content.Text,
			})
		}
		if content.Data != "" {
			return mcp.NewEmbeddedResource(mcp.BlobResourceContents{
				URI:      content.URI,
				MIMEType: content.MimeType,
				Blob:     content.Data,
			})
		}
		slog.Warn("embedded resource content has no text or blob data", "uri", content.URI)
		return mcp.NewEmbeddedResource(mcp.TextResourceContents{
			URI:      content.URI,
			MIMEType: content.MimeType,
		})
	default:
		slog.Warn("converting unknown content type to empty text - this may cause data loss", "type", content.Type)
		return mcp.NewTextContent("")
	}
}

// CreateToolHandler creates a tool handler that routes to the appropriate backend.
func (f *DefaultHandlerFactory) CreateToolHandler(
	toolName string,
) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		slog.Debug("handling tool call", "tool", toolName)

		target, err := f.router.RouteTool(ctx, toolName)
		if err != nil {
			if errors.Is(err, router.ErrToolNotFound) {
				wrappedErr := fmt.Errorf("%w: tool %s", vmcp.ErrNotFound, toolName)
				slog.Warn("routing failed", "error", wrappedErr)
				return mcp.NewToolResultError(wrappedErr.Error()), nil
			}
			slog.Warn("failed to route tool", "tool", toolName, "error", err)
			return mcp.NewToolResultError(fmt.Sprintf("Routing error: %v", err)), nil
		}

		args, ok := request.Params.Arguments.(map[string]any)
		if !ok {
			wrappedErr := fmt.Errorf("%w: arguments must be object, got %T", vmcp.ErrInvalidInput, request.Params.Arguments)
			slog.Warn("invalid arguments for tool", "tool", toolName, "error", wrappedErr)
			return mcp.NewToolResultError(wrappedErr.Error()), nil
		}

		// Extract metadata from request to forward to backend
		meta := conversion.FromMCPMeta(request.Params.Meta)

		// Call the backend tool - the backend client handles name translation and metadata forwarding
		result, err := f.backendClient.CallTool(ctx, target, toolName, args, meta)
		if err != nil {
			// Only actual network/transport errors reach here now (IsError=true is handled in result)
			if errors.Is(err, vmcp.ErrBackendUnavailable) {
				slog.Warn("backend unavailable for tool", "tool", toolName, "error", err)
				return mcp.NewToolResultError(fmt.Sprintf("Backend unavailable: %v", err)), nil
			}
			slog.Warn("backend tool call failed", "tool", toolName, "error", err)
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
				Meta: conversion.ToMCPMeta(result.Meta),
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
		slog.Debug("handling resource read", "uri", uri)

		caps, ok := discovery.DiscoveredCapabilitiesFromContext(ctx)
		if !ok {
			slog.Warn("capabilities not discovered in context")
			return nil, fmt.Errorf("capabilities not discovered")
		}

		target, err := f.router.RouteResource(ctx, uri)
		if err != nil {
			if errors.Is(err, router.ErrResourceNotFound) {
				wrappedErr := fmt.Errorf("%w: resource %s", vmcp.ErrNotFound, uri)
				slog.Warn("routing failed", "error", wrappedErr)
				return nil, wrappedErr
			}
			slog.Warn("failed to route resource", "uri", uri, "error", err)
			return nil, fmt.Errorf("routing error: %w", err)
		}

		backendURI := target.GetBackendCapabilityName(uri)

		result, err := f.backendClient.ReadResource(ctx, target, backendURI)
		if err != nil {
			if errors.Is(err, vmcp.ErrBackendUnavailable) {
				slog.Warn("backend unavailable for resource", "uri", uri, "error", err)
				return nil, fmt.Errorf("backend unavailable: %w", err)
			}
			slog.Warn("backend resource read failed", "uri", uri, "error", err)
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
		slog.Debug("handling prompt request", "prompt", promptName)

		// Route to backend
		target, err := f.router.RoutePrompt(ctx, promptName)
		if err != nil {
			if errors.Is(err, router.ErrPromptNotFound) {
				wrappedErr := fmt.Errorf("%w: prompt %s", vmcp.ErrNotFound, promptName)
				slog.Warn("routing failed", "error", wrappedErr)
				return nil, wrappedErr
			}
			slog.Warn("failed to route prompt", "prompt", promptName, "error", err)
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
				slog.Warn("backend unavailable for prompt", "prompt", promptName, "error", err)
				return nil, fmt.Errorf("backend unavailable: %w", err)
			}
			slog.Warn("backend prompt request failed", "prompt", promptName, "error", err)
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
				Meta: conversion.ToMCPMeta(result.Meta),
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
		slog.Debug("handling composite tool call", "tool", toolName)

		// Extract parameters from MCP request
		params, ok := request.Params.Arguments.(map[string]any)
		if !ok {
			wrappedErr := fmt.Errorf("%w: arguments must be object, got %T", vmcp.ErrInvalidInput, request.Params.Arguments)
			slog.Warn("invalid arguments for composite tool", "tool", toolName, "error", wrappedErr)
			return mcp.NewToolResultError(wrappedErr.Error()), nil
		}

		// Execute workflow via composer
		// The workflow engine applies timeout from WorkflowDefinition.Timeout (default: 30 minutes)
		// and handles context cancellation throughout execution.
		result, err := workflow.ExecuteWorkflow(ctx, params)
		if err != nil {
			// Check for timeout errors and provide user-friendly message
			if errors.Is(err, context.DeadlineExceeded) {
				slog.Warn("workflow execution timeout", "tool", toolName, "error", err)
				return mcp.NewToolResultError("Workflow execution timeout exceeded"), nil
			}
			slog.Error("workflow execution failed", "tool", toolName, "error", err)
			return mcp.NewToolResultError(fmt.Sprintf("Workflow execution failed: %v", err)), nil
		}

		// Check if workflow result contains an error
		if result.Error != nil {
			slog.Error("workflow completed with error", "tool", toolName, "error", result.Error)
			return mcp.NewToolResultError(fmt.Sprintf("Workflow error: %v", result.Error)), nil
		}

		// Convert workflow output to MCP tool result
		// The output is typically the result of the last workflow step
		slog.Debug("composite tool completed successfully", "tool", toolName)
		return mcp.NewToolResultStructuredOnly(result.Output), nil
	}
}
