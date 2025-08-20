package server

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// removeServerArgs holds the arguments for removing a server
type removeServerArgs struct {
	Name string `json:"name"`
}

// RemoveServer removes a stopped MCP server
func (h *Handler) RemoveServer(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Parse arguments using BindArguments
	args := &removeServerArgs{}
	if err := request.BindArguments(args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to parse arguments: %v", err)), nil
	}

	// Delete the workload
	group, err := h.workloadManager.DeleteWorkloads(ctx, []string{args.Name})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to remove server: %v", err)), nil
	}

	// Wait for the delete operation to complete
	if err := group.Wait(); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to remove server: %v", err)), nil
	}

	result := map[string]interface{}{
		"status": "removed",
		"name":   args.Name,
	}

	return mcp.NewToolResultStructuredOnly(result), nil
}
