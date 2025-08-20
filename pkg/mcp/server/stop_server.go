package server

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// stopServerArgs holds the arguments for stopping a server
type stopServerArgs struct {
	Name string `json:"name"`
}

// StopServer stops a running MCP server
func (h *Handler) StopServer(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Parse arguments using BindArguments
	args := &stopServerArgs{}
	if err := request.BindArguments(args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to parse arguments: %v", err)), nil
	}

	// Stop the workload
	group, err := h.workloadManager.StopWorkloads(ctx, []string{args.Name})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to stop server: %v", err)), nil
	}

	// Wait for the stop operation to complete
	if err := group.Wait(); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to stop server: %v", err)), nil
	}

	result := map[string]interface{}{
		"status": "stopped",
		"name":   args.Name,
	}

	return mcp.NewToolResultStructuredOnly(result), nil
}
