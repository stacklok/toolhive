package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// getServerLogsArgs holds the arguments for getting server logs
type getServerLogsArgs struct {
	Name string `json:"name"`
}

// GetServerLogs gets logs from a running MCP server
func (h *Handler) GetServerLogs(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Parse arguments using BindArguments
	args := &getServerLogsArgs{}
	if err := request.BindArguments(args); err != nil {
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
