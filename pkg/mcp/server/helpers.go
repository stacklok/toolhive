package server

import (
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Helper functions for creating tool results

// NewToolResultError creates a CallToolResult with an error message
func NewToolResultError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{
				Text: message,
			},
		},
		IsError: true,
	}
}

// NewToolResultText creates a CallToolResult with text content
func NewToolResultText(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{
				Text: text,
			},
		},
		IsError: false,
	}
}

// NewToolResultStructuredOnly creates a CallToolResult with only structured content
func NewToolResultStructuredOnly(data interface{}) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		StructuredContent: data,
		Content:          []mcp.Content{}, // Empty content array
		IsError:          false,
	}
}

// BindArguments unmarshals the arguments from a CallToolRequest
func BindArguments(request *mcp.CallToolRequest, target interface{}) error {
	if request.Params.Arguments == nil {
		// No arguments provided, use empty JSON object
		return json.Unmarshal([]byte("{}"), target)
	}
	return json.Unmarshal(request.Params.Arguments, target)
}