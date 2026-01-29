// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// OptimizerToolNames defines the tool names exposed when optimizer is enabled.
const (
	FindToolName = "find_tool"
	CallToolName = "call_tool"
)

// Pre-generated schemas for optimizer tools.
// Generated at package init time so any schema errors panic at startup.
var (
	findToolInputSchema = mustMarshalSchema(findToolSchema)
	callToolInputSchema = mustMarshalSchema(callToolSchema)
)

// Tool schemas defined once to eliminate duplication.
var (
	findToolSchema = mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]any{
			"tool_description": map[string]any{
				"type":        "string",
				"description": "Natural language description of the tool you're looking for",
			},
			"tool_keywords": map[string]any{
				"type":        "string",
				"description": "Optional space-separated keywords for keyword-based search",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of tools to return (default: 10)",
				"default":     10,
			},
		},
		Required: []string{"tool_description"},
	}

	callToolSchema = mcp.ToolInputSchema{
		Type: "object",
		Properties: map[string]any{
			"backend_id": map[string]any{
				"type":        "string",
				"description": "Backend ID from find_tool results",
			},
			"tool_name": map[string]any{
				"type":        "string",
				"description": "Tool name to invoke",
			},
			"parameters": map[string]any{
				"type":        "object",
				"description": "Parameters to pass to the tool",
			},
		},
		Required: []string{"backend_id", "tool_name", "parameters"},
	}
)

// CreateOptimizerTools creates the SDK tools for optimizer mode.
// When optimizer is enabled, only these two tools are exposed to clients
// instead of all backend tools.
//
// This function uses the OptimizerHandlerProvider interface to get handlers,
// allowing it to work with OptimizerIntegration without direct dependency.
func CreateOptimizerTools(provider OptimizerHandlerProvider) ([]server.ServerTool, error) {
	if provider == nil {
		return nil, fmt.Errorf("optimizer handler provider cannot be nil")
	}

	return []server.ServerTool{
		{
			Tool: mcp.Tool{
				Name:           FindToolName,
				Description:    "Semantic search across all backend tools using natural language description and optional keywords",
				RawInputSchema: findToolInputSchema,
			},
			Handler: provider.CreateFindToolHandler(),
		},
		{
			Tool: mcp.Tool{
				Name:           CallToolName,
				Description:    "Dynamically invoke any tool on any backend using the backend_id from find_tool",
				RawInputSchema: callToolInputSchema,
			},
			Handler: provider.CreateCallToolHandler(),
		},
	}, nil
}

// mustMarshalSchema marshals a schema to JSON, panicking on error.
// This is safe because schemas are generated from known types at startup.
// This should NOT be called by runtime code.
func mustMarshalSchema(schema mcp.ToolInputSchema) json.RawMessage {
	data, err := json.Marshal(schema)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal schema: %v", err))
	}

	return data
}
