package adapter

import (
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

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
func (a *CapabilityAdapter) ToCompositeToolSDKTools(
	tools []vmcp.Tool,
	workflowExecutors map[string]WorkflowExecutor,
) ([]server.ServerTool, error) {
	if len(tools) == 0 {
		return nil, nil
	}

	sdkTools := make([]server.ServerTool, 0, len(tools))
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
