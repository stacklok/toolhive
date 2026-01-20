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
				Name:           FindToolName,
				Description:    "Search for tools by description. Returns matching tools ranked by relevance.",
				RawInputSchema: findToolInputSchema,
			},
			Handler: createFindToolHandler(opt),
		},
		{
			Tool: mcp.Tool{
				Name:           CallToolName,
				Description:    "Call a tool by name with the given parameters.",
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
