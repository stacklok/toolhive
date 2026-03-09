// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	"github.com/stacklok/toolhive/pkg/vmcp/schema"
)

// OptimizerToolNames defines the tool names exposed when optimizer is enabled.
const (
	FindToolName = "find_tool"
	CallToolName = "call_tool"
)

// Pre-generated schemas for optimizer tools.
// Generated at package init time so any schema errors panic at startup.
var (
	findToolInputSchema = mustGenerateSchema[optimizer.FindToolInput]()
	callToolInputSchema = mustGenerateSchema[optimizer.CallToolInput]()
)

// CreateOptimizerTools creates the SDK tools for optimizer mode.
// When optimizer is enabled, only these two tools are exposed to clients
// instead of all backend tools.
func CreateOptimizerTools(opt optimizer.Optimizer) []server.ServerTool {
	return []server.ServerTool{
		{
			Tool: mcp.Tool{
				Name: FindToolName,
				Description: "Find and return tools that can help accomplish the user's request. " +
					"This searches available MCP server tools using semantic and keyword-based matching. " +
					"Use this function when you need to: " +
					"(1) discover what tools are available for a specific task, " +
					"(2) find the right tool(s) before attempting to solve a problem, " +
					"(3) check if required functionality exists in the current environment. " +
					"Returns matching tools ranked by relevance including their names, descriptions, " +
					"required parameters and schemas, plus token efficiency metrics showing " +
					"baseline_tokens, returned_tokens, and savings_percent. " +
					"Example: for 'Find good restaurants in San Jose', call with " +
					"tool_description='search the web' and tool_keywords='web search restaurants'. " +
					"Always call this before call_tool to discover the correct tool name and parameter schema.",
				RawInputSchema: findToolInputSchema,
			},
			Handler: createFindToolHandler(opt),
		},
		{
			Tool: mcp.Tool{
				Name: CallToolName,
				Description: "Execute a specific tool with the provided parameters. " +
					"Use this function to: " +
					"(1) run a tool after identifying it with find_tool, " +
					"(2) execute operations that require specific MCP server functionality, " +
					"(3) perform actions that go beyond your built-in capabilities. " +
					"Important: always use find_tool first to get the correct tool_name " +
					"and parameter schema before calling this function. " +
					"The parameters must match the tool's input schema as returned by find_tool. " +
					"Returns the tool's execution result which may include success/failure status, " +
					"result data or content, and error messages if execution failed.",
				RawInputSchema: callToolInputSchema,
			},
			Handler: createCallToolHandler(opt),
		},
	}
}

// createFindToolHandler creates a handler for the find_tool optimizer operation.
func createFindToolHandler(opt optimizer.Optimizer) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		input, err := schema.Translate[optimizer.FindToolInput](request.Params.Arguments)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
		}

		output, err := opt.FindTool(ctx, input)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("find_tool failed: %v", err)), nil
		}

		return mcp.NewToolResultStructuredOnly(output), nil
	}
}

// createCallToolHandler creates a handler for the call_tool optimizer operation.
func createCallToolHandler(opt optimizer.Optimizer) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		input, err := schema.Translate[optimizer.CallToolInput](request.Params.Arguments)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
		}

		result, err := opt.CallTool(ctx, input)
		if err != nil {
			// Exposing the error to the MCP client is important if you want it to correct its behavior.
			// Without information on the failure, the model is pretty much hopeless in figuring out the problem.
			return mcp.NewToolResultError(fmt.Sprintf("call_tool failed: %v", err)), nil
		}

		return result, nil
	}
}

// mustMarshalSchema marshals a schema to JSON, panicking on error.
// This is safe because schemas are generated from known types at startup.
// This should NOT be called by runtime code.
func mustGenerateSchema[T any]() json.RawMessage {
	s, err := schema.GenerateSchema[T]()
	if err != nil {
		panic(fmt.Sprintf("failed to generate schema: %v", err))
	}

	data, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal schema: %v", err))
	}

	return data
}
