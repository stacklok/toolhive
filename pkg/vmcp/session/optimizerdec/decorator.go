// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package optimizerdec provides a MultiSession decorator that replaces the
// full tool list with two optimizer tools: find_tool and call_tool.
package optimizerdec

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	"github.com/stacklok/toolhive/pkg/vmcp/schema"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

const (
	// FindToolName is the tool name for semantic tool discovery.
	FindToolName = "find_tool"
	// CallToolName is the tool name for routing a call to any backend tool.
	CallToolName = "call_tool"
	// CallToolArgToolName is the JSON argument key for the backend tool name in a call_tool request.
	// It must match the json tag on optimizer.CallToolInput.ToolName.
	CallToolArgToolName = "tool_name"
	// CallToolArgParameters is the JSON argument key for the backend tool parameters in a call_tool request.
	// It must match the json tag on optimizer.CallToolInput.Parameters.
	CallToolArgParameters = "parameters"
)

// Pre-generated schemas for find_tool and call_tool, computed at init time.
var (
	findToolInputSchema = mustGenerateSchema[optimizer.FindToolInput]()
	callToolInputSchema = mustGenerateSchema[optimizer.CallToolInput]()
)

// optimizerDecorator wraps a MultiSession to expose only find_tool and call_tool.
// Tools() returns only those two tools. CallTool("find_tool") routes through the
// optimizer's FindTool; CallTool("call_tool") routes through the optimizer's
// CallTool so that all optimizer telemetry (traces, metrics) is recorded.
type optimizerDecorator struct {
	sessiontypes.MultiSession
	opt            optimizer.Optimizer
	optimizerTools []vmcp.Tool
}

// NewDecorator wraps sess with optimizer mode. Only find_tool and call_tool are
// exposed via Tools(). find_tool calls opt.FindTool. call_tool calls opt.CallTool,
// which routes through the instrumented optimizer (telemetry, traces, metrics).
func NewDecorator(sess sessiontypes.MultiSession, opt optimizer.Optimizer) sessiontypes.MultiSession {
	return &optimizerDecorator{
		MultiSession: sess,
		opt:          opt,
		optimizerTools: []vmcp.Tool{
			{
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
					"Always call this before call_tool to discover the correct tool name and parameter schema.",
				InputSchema: findToolInputSchema,
			},
			{
				Name: CallToolName,
				Description: "Execute a specific tool with the provided parameters. " +
					"Use this function to run a tool after identifying it with find_tool. " +
					"Important: always use find_tool first to get the correct tool_name " +
					"and parameter schema before calling this function.",
				InputSchema: callToolInputSchema,
			},
		},
	}
}

// Tools returns only find_tool and call_tool, replacing the full backend tool list.
// A defensive copy is returned so callers cannot mutate the decorator's internal slice.
func (d *optimizerDecorator) Tools() []vmcp.Tool {
	result := make([]vmcp.Tool, len(d.optimizerTools))
	copy(result, d.optimizerTools)
	return result
}

// CallTool handles find_tool and call_tool. Both route through the optimizer so
// that all optimizer telemetry is recorded. Any other tool name returns an error.
func (d *optimizerDecorator) CallTool(
	ctx context.Context,
	_ *auth.Identity,
	toolName string,
	arguments map[string]any,
	_ map[string]any,
) (*vmcp.ToolCallResult, error) {
	switch toolName {
	case FindToolName:
		return d.handleFindTool(ctx, arguments)
	case CallToolName:
		return d.handleCallTool(ctx, arguments)
	default:
		return nil, fmt.Errorf("tool not found: %s", toolName)
	}
}

func (d *optimizerDecorator) handleFindTool(ctx context.Context, arguments map[string]any) (*vmcp.ToolCallResult, error) {
	input, err := schema.Translate[optimizer.FindToolInput](arguments)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}

	output, err := d.opt.FindTool(ctx, input)
	if err != nil {
		return errorResult(fmt.Sprintf("find_tool failed: %v", err)), nil
	}
	if output == nil {
		return errorResult("find_tool: optimizer returned nil result"), nil
	}

	jsonBytes, err := json.Marshal(output)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to marshal find_tool output: %v", err)), nil
	}

	var structured map[string]any
	// Unmarshal cannot fail: jsonBytes was just produced by json.Marshal above.
	_ = json.Unmarshal(jsonBytes, &structured)

	return &vmcp.ToolCallResult{
		Content:           []vmcp.Content{{Type: "text", Text: string(jsonBytes)}},
		StructuredContent: structured,
	}, nil
}

func (d *optimizerDecorator) handleCallTool(
	ctx context.Context,
	arguments map[string]any,
) (*vmcp.ToolCallResult, error) {
	input, err := schema.Translate[optimizer.CallToolInput](arguments)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid arguments: %v", err)), nil
	}

	mcpResult, err := d.opt.CallTool(ctx, input)
	if err != nil {
		return errorResult(fmt.Sprintf("call_tool failed: %v", err)), nil
	}
	if mcpResult == nil {
		return errorResult("call_tool: optimizer returned nil result"), nil
	}

	return mcpResultToVMCPResult(mcpResult), nil
}

// mcpResultToVMCPResult converts an MCP SDK CallToolResult to the vmcp domain type.
func mcpResultToVMCPResult(r *mcp.CallToolResult) *vmcp.ToolCallResult {
	structured, _ := r.StructuredContent.(map[string]any)
	return &vmcp.ToolCallResult{
		Content:           conversion.ConvertMCPContents(r.Content),
		StructuredContent: structured,
		IsError:           r.IsError,
	}
}

func errorResult(msg string) *vmcp.ToolCallResult {
	return &vmcp.ToolCallResult{
		Content: []vmcp.Content{{Type: "text", Text: msg}},
		IsError: true,
	}
}

func mustGenerateSchema[T any]() map[string]any {
	s, err := schema.GenerateSchema[T]()
	if err != nil {
		panic(fmt.Sprintf("optimizerdec: failed to generate schema: %v", err))
	}
	return s
}
