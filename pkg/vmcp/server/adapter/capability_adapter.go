// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// OptimizerHandlerProvider provides handlers for optimizer tools.
// This interface allows the adapter to create optimizer tools without
// depending on the optimizer package implementation.
type OptimizerHandlerProvider interface {
	// CreateFindToolHandler returns the handler for optim_find_tool
	CreateFindToolHandler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)

	// CreateCallToolHandler returns the handler for optim_call_tool
	CreateCallToolHandler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
}

// CapabilityAdapter converts aggregator domain models to SDK types.
//
// This is the Anti-Corruption Layer between:
//   - Domain model (aggregator.AggregatedCapabilities)
//   - External library (mark3labs/mcp-go SDK types)
//
// The adapter:
//  1. Converts aggregator types to SDK types
//  2. Creates handlers using HandlerFactory
//  3. Returns SDK-ready capabilities
//
// This keeps the server layer from knowing about aggregator internals.
type CapabilityAdapter struct {
	handlerFactory HandlerFactory
}

// NewCapabilityAdapter creates a new capability adapter.
func NewCapabilityAdapter(handlerFactory HandlerFactory) *CapabilityAdapter {
	return &CapabilityAdapter{
		handlerFactory: handlerFactory,
	}
}

// ToSDKTools converts vmcp tools to SDK ServerTool format.
//
// For each tool:
//   - Marshals InputSchema to JSON (SDK expects RawInputSchema as []byte)
//   - Creates handler via HandlerFactory
//   - Wraps in server.ServerTool struct
//
// Returns error if schema marshaling fails for any tool.
func (a *CapabilityAdapter) ToSDKTools(tools []vmcp.Tool) ([]server.ServerTool, error) {
	if len(tools) == 0 {
		return nil, nil
	}

	sdkTools := make([]server.ServerTool, 0, len(tools))
	for _, tool := range tools {
		// Marshal schema to JSON
		schemaJSON, err := json.Marshal(tool.InputSchema)
		if err != nil {
			logger.Warnw("failed to marshal tool schema",
				"tool", tool.Name,
				"error", err)
			return nil, fmt.Errorf("failed to marshal schema for tool %s: %w", tool.Name, err)
		}

		// Create handler via factory
		handler := a.handlerFactory.CreateToolHandler(tool.Name)

		// Create SDK tool
		sdkTools = append(sdkTools, server.ServerTool{
			Tool: mcp.Tool{
				Name:           tool.Name,
				Description:    tool.Description,
				RawInputSchema: schemaJSON,
			},
			Handler: handler,
		})
	}

	return sdkTools, nil
}

// ToSDKResources converts vmcp resources to SDK ServerResource format.
//
// For each resource:
//   - Maps vmcp.Resource fields to mcp.Resource fields
//   - Creates handler via HandlerFactory
//   - Wraps in server.ServerResource struct
func (a *CapabilityAdapter) ToSDKResources(resources []vmcp.Resource) []server.ServerResource {
	if len(resources) == 0 {
		return nil
	}

	sdkResources := make([]server.ServerResource, 0, len(resources))
	for _, resource := range resources {
		// Create handler via factory
		handler := a.handlerFactory.CreateResourceHandler(resource.URI)

		// Create SDK resource
		sdkResources = append(sdkResources, server.ServerResource{
			Resource: mcp.Resource{
				URI:         resource.URI,
				Name:        resource.Name,
				Description: resource.Description,
				MIMEType:    resource.MimeType,
			},
			Handler: handler,
		})
	}

	return sdkResources
}

// ToSDKPrompts converts vmcp prompts to SDK ServerPrompt format.
//
// For each prompt:
//   - Maps vmcp.Prompt fields to mcp.Prompt fields
//   - Converts prompt arguments to SDK format
//   - Creates handler via HandlerFactory
//   - Wraps in server.ServerPrompt struct
//
// Note: SDK v0.43.0 does not support per-session prompts yet.
// This method is provided for future use.
func (a *CapabilityAdapter) ToSDKPrompts(prompts []vmcp.Prompt) []server.ServerPrompt {
	if len(prompts) == 0 {
		return nil
	}

	sdkPrompts := make([]server.ServerPrompt, 0, len(prompts))
	for _, prompt := range prompts {
		// Convert prompt arguments
		mcpArguments := make([]mcp.PromptArgument, len(prompt.Arguments))
		for i, arg := range prompt.Arguments {
			mcpArguments[i] = mcp.PromptArgument{
				Name:        arg.Name,
				Description: arg.Description,
				Required:    arg.Required,
			}
		}

		// Create handler via factory
		handler := a.handlerFactory.CreatePromptHandler(prompt.Name)

		// Create SDK prompt
		sdkPrompts = append(sdkPrompts, server.ServerPrompt{
			Prompt: mcp.Prompt{
				Name:        prompt.Name,
				Description: prompt.Description,
				Arguments:   mcpArguments,
			},
			Handler: handler,
		})
	}

	return sdkPrompts
}

// ToCompositeToolSDKTools converts composite tools to SDK ServerTool format with workflow handlers.
//
// This method is similar to ToSDKTools but uses composite tool workflow handlers instead of
// backend routing handlers. For each composite tool:
//   - Marshals InputSchema to JSON (SDK expects RawInputSchema as []byte)
//   - Creates workflow handler via HandlerFactory.CreateCompositeToolHandler
//   - Wraps in server.ServerTool struct
//
// The workflowExecutors map provides the workflow executor for each tool name.
// Returns error if schema marshaling fails or workflow executor is missing for any tool.
//
// Authorization note: Composite tools are registered per-session based on session-discovered
// tools. Currently, if a workflow references tools that a user lacks access to, the workflow
// registration will fail hard with an error. Future enhancement: gracefully disable workflows
// with missing required tools while logging for audit purposes, preventing privilege escalation
// while improving user experience.
func (a *CapabilityAdapter) ToCompositeToolSDKTools(
	tools []vmcp.Tool,
	workflowExecutors map[string]WorkflowExecutor,
) ([]server.ServerTool, error) {
	var sdkTools []server.ServerTool
	for _, tool := range tools {
		// Get workflow executor for this tool
		executor, exists := workflowExecutors[tool.Name]
		if !exists {
			logger.Warnw("workflow executor not found for composite tool",
				"tool", tool.Name)
			return nil, fmt.Errorf("workflow executor not found for composite tool: %s", tool.Name)
		}

		// Marshal schema to JSON
		schemaJSON, err := json.Marshal(tool.InputSchema)
		if err != nil {
			logger.Warnw("failed to marshal composite tool schema",
				"tool", tool.Name,
				"error", err)
			return nil, fmt.Errorf("failed to marshal schema for composite tool %s: %w", tool.Name, err)
		}

		// Create handler via factory (uses composite tool handler instead of backend router)
		handler := a.handlerFactory.CreateCompositeToolHandler(tool.Name, executor)

		// Create SDK tool
		sdkTools = append(sdkTools, server.ServerTool{
			Tool: mcp.Tool{
				Name:           tool.Name,
				Description:    tool.Description,
				RawInputSchema: schemaJSON,
			},
			Handler: handler,
		})
	}

	return sdkTools, nil
}

// CreateOptimizerTools creates SDK tools for optimizer mode.
//
// When optimizer is enabled, only optim_find_tool and optim_call_tool are exposed
// to clients instead of all backend tools. This method delegates to the standalone
// CreateOptimizerTools function in optimizer_adapter.go for consistency.
//
// This keeps optimizer tool creation consistent with other tool types (backend,
// composite) by going through the adapter layer.
func (a *CapabilityAdapter) CreateOptimizerTools(provider OptimizerHandlerProvider) ([]server.ServerTool, error) {
	return CreateOptimizerTools(provider)
}
